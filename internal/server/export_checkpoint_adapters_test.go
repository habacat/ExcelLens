package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"excellens/internal/workbook"
)

func exportCheckpointCompareAdapter(t *testing.T, file string) {
	t.Helper()
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	before := printScaleHash(t, file)
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	order, err := wb.FilterRowOrder(context.Background(), idx, nil, workbook.FilterOptions{SortColumn: 1, Descending: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.filterResults["adapter-order"] = &FilterResult{ID: "adapter-order", SheetPath: meta.Path, Rows: order, Sorted: true}
	req := exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: "adapter-order", Format: "jsonl", Mode: "formula", ProtectCSV: true}
	selections, release := exportCheckpointPrepareOwned(t, a, req)
	want := exportCheckpointOutput(t, wb, selections, req.options())
	store, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	_ = lease.Close()
	release()
	a.Close()
	b := checkpointOpen(t, file)
	lease, err = store.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	restored, err := readExportCheckpointSelections(context.Background(), b.currentWorkbook(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	got := exportCheckpointOutput(t, b.currentWorkbook(), restored.selections, req.options())
	if !reflect.DeepEqual(got, want) || printScaleHash(t, file) != before || lease.Snapshot().Revision != m.Revision {
		t.Fatal("adapter inputs changed values, types, coordinates, formulas or source bytes", wb.Format, string(got), string(want))
	}
}

func TestExportCheckpointRealTextHTMLOOXMLAndBIFFAdapters(t *testing.T) {
	for _, test := range []struct{ file, body string }{
		{"data.csv", "编号,金额\r\n001,9007199254740993.31\r\n002,\"x\r\ny\"\r\n"},
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
			exportCheckpointCompareAdapter(t, file)
		})
	}
}

func TestExportCheckpointEmptyFilterRemainsEmptyAndDoesNotBecomeWholeSheet(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["empty"] = &FilterResult{ID: "empty", SheetPath: meta.Path, Rows: rowOrderFixture(t, idx.RowCount())}
	req := exportRequest{Scope: "filtered", Sheet: "First", FilterID: "empty", Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
	_, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	out, err := readExportCheckpointSelections(context.Background(), wb, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer out.close()
	if out.selections[0].RowCount() != 0 || len(exportCheckpointOutput(t, wb, out.selections, req.options())) != 0 || lease.Snapshot().Blocks != 0 {
		t.Fatal("empty frozen order became source rows")
	}
}

func TestExportCheckpointFormulaCacheStateAndNativeTextSurviveFrozenSource(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "formula.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>001</t></is></c><c r="B1" t="inlineStr"><is><t>9007199254740993.31</t></is></c></row><row r="3"><c r="A3"><f>1+1</f></c><c r="B3" t="str"><f>"done"</f><v>done</v></c></row></sheetData></worksheet>`,
	})
	exportCheckpointCompareAdapter(t, file)
	// Inspect typed public output independently, including a real missing cache.
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		t.Fatal("cached source index missing")
	}
	rows, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{1, 3}, 1, 2, workbook.RowReadOptions{IncludeFormula: true})
	if err != nil || rows[0].Cells[1].Raw != "9007199254740993.31" || rows[0].Cells[1].Type != "inlineStr" || !rows[1].Cells[0].CacheMissing || rows[1].Cells[0].Formula != "=1+1" {
		t.Fatal("source states were lost", rows, err)
	}
}
