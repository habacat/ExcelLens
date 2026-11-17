package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	excelExport "excellens/internal/export"
)

func exportResumeCompareAdapter(t *testing.T, file string) {
	t.Helper()
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	before := printScaleHash(t, file)
	req := exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: "jsonl", Mode: "formula", ProtectCSV: true}
	selections := exportCheckpointPrepare(t, a, req)
	want := exportCheckpointOutput(t, wb, selections, req.options())
	store, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	journal, err := newExportCheckpointJournal(context.Background(), wb, lease, selections)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = excelExport.Resume(ctx, wb, selections, req.options(), pausingExportJournal{ResumeJournal: journal, after: 1, pause: cancel}, nil)
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	_ = lease.Close()
	_ = store.Close()
	a.Close()
	b := checkpointOpen(t, file)
	got := exportResumeDownload(t, b, exportResumeJob(t, b, m))
	compareResumedOutput(t, req.Format, got, want, m.Created)
	if printScaleHash(t, file) != before {
		t.Fatal("adapter recovery modified its original source")
	}
}

func TestExportResumeAllRealTextHTMLOOXMLAndBIFFAdapters(t *testing.T) {
	for _, test := range []struct{ file, body string }{
		{"data.csv", "编号,金额\r\n001,9007199254740993.31\r\n002,\"one\r\ntwo\"\r\n"},
		{"data.tsv", "编号\t金额\n001\t9007199254740993.31\n002\t-1.005\n"},
		{"data.txt", "编号\t金额\n001\t9007199254740993.31\n002\t-1.005\n"},
		{"html.xls", "<table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr><tr><td>=SUM(A1)</td><td> -1.005</td></tr></table>"},
		{"native.xls", ""}, {"source.xlsx", ""}, {"source.xlsm", ""},
	} {
		t.Run(test.file, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), test.file)
			if test.file == "native.xls" {
				printBIFFSource(t, file, 5)
			} else if test.body == "" {
				writeSelectionWorkbook(t, file)
			} else if err := os.WriteFile(file, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			exportResumeCompareAdapter(t, file)
		})
	}
}

func TestExportResumeSparseJSONLSkipsBlankRecordsWithoutResettingSourceProgress(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "sparse-formulas.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>001</t></is></c></row><row r="4097"><c r="A4097"><f>SUM(A1:A4096)</f></c></row><row r="5001"><c r="A5001"><v>9007199254740993.31</v></c></row></sheetData></worksheet>`,
	})
	exportResumeCompareAdapter(t, file)
}
