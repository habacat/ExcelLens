package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

func conditionalServerFixture(t *testing.T) (*App, *workbook.SheetIndex, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "conditional.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><b/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="1"><dxf><font><b val="0"/><color rgb="FFFF0033"/></font><fill><patternFill patternType="solid"><fgColor rgb="FFFFFF00"/></patternFill></fill></dxf></dxfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>9007199254740993.31</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c><c r="D1"><f>1+1</f></c></row><row r="3"><c r="A3"><v>9007199254740993.30</v></c></row><row r="9"><c r="F9"><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1:A9"><cfRule type="cellIs" operator="greaterThan" priority="1" dxfId="0"><formula>9007199254740993.30</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="B1:B9"><cfRule type="cellIs" operator="equal" priority="2" dxfId="0"><formula>"001"</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="C1:C9"><cfRule type="expression" priority="3" dxfId="0"><formula>$F$9&gt;0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="D1:D9"><cfRule type="cellIs" operator="greaterThan" priority="4" dxfId="0"><formula>0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="E1:E9"><cfRule type="containsBlanks" priority="5" dxfId="0"><formula>LEN(TRIM(E1))=0</formula></cfRule></conditionalFormatting></worksheet>`,
	})
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(file); err != nil {
		a.Close()
		t.Fatal(err)
	}
	w := a.currentWorkbook()
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Close()
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Error("conditional endpoints changed source", err)
		}
	})
	return a, idx, file
}

func TestConditionalXLSXPublicationFailureAndLateCancellationKeepExistingFilesAndRemoveTemporaryFiles(t *testing.T) {
	a, idx, _ := conditionalServerFixture(t)
	w := a.currentWorkbook()
	selection := excelExport.Selection{Sheet: w.Sheets[0], Index: idx, FirstRow: 1, LastRow: 3, FirstCol: 1, LastCol: 3}
	if err := os.MkdirAll(filepath.Join(ExportDir(), "occupied.xlsx"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(ExportDir(), "occupied.xlsx", "keep.txt")
	if err := os.WriteFile(marker, []byte("existing destination"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"publish", "cancel", "write"} {
		ctx, cancel := context.WithCancel(context.Background())
		job := &ExportJob{ctx: ctx}
		name := "occupied.xlsx"
		if phase != "publish" {
			name = phase + ".xlsx"
		}
		sentinel := errors.New("disk-full write failure")
		err := publishExportFile(job, name, func(tmp *os.File) error {
			if err := excelExport.Write(ctx, tmp, w, []excelExport.Selection{selection}, excelExport.Options{Format: "xlsx", Mode: "values", Styles: true}, nil); err != nil {
				return err
			}
			if phase == "cancel" {
				cancel() // complete ZIP, cancellation before final publication
			}
			if phase == "write" {
				return sentinel
			}
			return nil
		})
		cancel()
		if err == nil || job.FilePath != "" || job.Size != 0 {
			t.Fatal("failed/canceled snapshot published metadata", phase, err, job.FilePath)
		}
		if phase == "cancel" && !errors.Is(err, context.Canceled) || phase == "write" && !errors.Is(err, sentinel) {
			t.Fatal("failure cause was lost", phase, err)
		}
		if phase != "publish" {
			if _, err := os.Stat(filepath.Join(ExportDir(), name)); !os.IsNotExist(err) {
				t.Fatal("failed snapshot left a final file", phase, err)
			}
		}
		leftovers, err := filepath.Glob(filepath.Join(ExportDir(), ".export-*.tmp"))
		if err != nil || len(leftovers) > 0 {
			t.Fatal("failed publication retained temporary output", phase, leftovers, err)
		}
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "existing destination" {
		t.Fatal("existing destination was damaged", err)
	}
}

func TestConditionalHTTPViewportCellClipboardAndSortedRowsUseOneSourceCoordinateModel(t *testing.T) {
	a, idx, _ := conditionalServerFixture(t)
	url := "/api/sheet?sheet=Data&row=1&rowWindow=1&limit=3&cols=6"
	full := decodeResponse[struct{ Rows []workbook.RowData }](t, featureCall(t, a, "GET", url, ""))
	if len(full.Rows) != 3 || full.Rows[1].Number != 2 || idx.RowCount() != 3 || idx.MaxRow != 9 || idx.MaxCol != 6 {
		t.Fatal("sparse row presentation changed extent", full.Rows)
	}
	find := func(row workbook.RowData, col int) workbook.CellData {
		for _, cell := range row.Cells {
			if cell.Col == col {
				return cell
			}
		}
		t.Fatal("missing requested conditional cell", row.Number, col)
		return workbook.CellData{}
	}
	number := find(full.Rows[0], 1)
	if number.Raw != "9007199254740993.31" || number.Style != 0 || number.Conditional == nil || number.Conditional.Visual.Color != "#FF0033" || number.Conditional.Visual.Bold {
		t.Fatal(number)
	}
	if find(full.Rows[0], 2).Type != "inlineStr" || find(full.Rows[0], 2).Raw != "001" || find(full.Rows[2], 1).Conditional != nil {
		t.Fatal("native types or exact decimal comparison changed", full.Rows)
	}
	for _, row := range full.Rows {
		for _, col := range []int{3, 5} {
			blank := find(row, col)
			if !blank.PresentationOnly || blank.Raw != "" || blank.Type != "" || blank.Conditional == nil || blank.Conditional.Notice != "" {
				t.Fatal("proven blank or out-of-window dependency was lost", blank)
			}
		}
	}
	if find(full.Rows[0], 4).Conditional.Notice == "" || !find(full.Rows[0], 4).CacheMissing {
		t.Fatal("missing formula result became zero", full.Rows[0])
	}
	compact := decodeResponse[struct{ Rows []wireRow }](t, featureCall(t, a, "GET", url+"&compact=1", ""))
	if !reflect.DeepEqual(compact.Rows, packViewportRows(full.Rows)) || compact.Rows[1].Cells[0].Flags&16 == 0 {
		t.Fatal("compact viewport lost conditional provenance", compact)
	}
	cell := decodeResponse[struct{ Cell workbook.CellData }](t, featureCall(t, a, "GET", "/api/cell?sheet=Data&ref=E2", ""))
	if !reflect.DeepEqual(cell.Cell, find(full.Rows[1], 5)) {
		t.Fatal("blank inspector and viewport differ", cell)
	}
	copy := decodeResponse[clipboardPayload](t, featureCall(t, a, "GET", "/api/range?sheet=Data&start=A1&end=F3&format=json", ""))
	if len(copy.Rows) != 3 || !reflect.DeepEqual(copy.Rows[1].Cells, full.Rows[1].Cells) {
		t.Fatal("copy lost sparse conditional row", copy)
	}
	plain := decodeResponse[clipboardPayload](t, featureCall(t, a, "GET", "/api/range?sheet=Data&start=A1&end=F3&format=json&richText=0", ""))
	for _, row := range plain.Rows {
		for _, cell := range row.Cells {
			if cell.Conditional != nil || cell.PresentationOnly {
				t.Fatal("plain copy read presentation", cell)
			}
		}
	}
	a.filterResults["reordered"] = &FilterResult{ID: "reordered", SheetPath: idx.SheetPath, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 1, 0)}
	sorted := decodeResponse[struct{ Rows []workbook.RowData }](t, featureCall(t, a, "GET", "/api/sheet?sheet=Data&filterId=reordered&offset=0&limit=2&cols=6", ""))
	if sorted.Rows[0].Number != 3 || find(sorted.Rows[0], 1).Conditional != nil || sorted.Rows[1].Number != 1 || !reflect.DeepEqual(find(sorted.Rows[1], 1), number) {
		t.Fatal("sort changed source condition coordinates", sorted)
	}
}

func TestConditionalFormattedXLSXPublishesProvenSnapshotAndRejectsMissingFormulaCache(t *testing.T) {
	a, _, _ := conditionalServerFixture(t)
	for _, tc := range []struct {
		start, end string
		complete   bool
	}{{"A1", "C3", true}, {"D1", "D1", false}} {
		started := decodeResponse[struct {
			JobID string `json:"jobId"`
		}](t, featureCall(t, a, "POST", "/api/export/start", fmt.Sprintf(`{"scope":"selection","sheet":"Data","start":%q,"end":%q,"format":"xlsx","mode":"values","styles":true}`, tc.start, tc.end)))
		a.mu.RLock()
		job := a.exportJobs[started.JobID]
		a.mu.RUnlock()
		if job == nil {
			t.Fatal("missing export")
		}
		select {
		case <-job.done:
		case <-time.After(5 * time.Second):
			t.Fatal("export timeout")
		}
		state := decodeResponse[struct {
			DownloadURL string `json:"downloadUrl"`
		}](t, featureCall(t, a, "GET", "/api/export/status?id="+started.JobID, ""))
		if !tc.complete {
			if job.Status != "failed" || !strings.Contains(job.Error, "条件格式未完整还原") || job.FilePath != "" || state.DownloadURL != "" {
				t.Fatal("unproven effect was published", job.Status, job.Error, job.FilePath, state.DownloadURL)
			}
			leftovers, err := filepath.Glob(filepath.Join(ExportDir(), ".export-*.tmp"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("failed snapshot left temporary output", leftovers, err)
			}
			continue
		}
		if job.Status != "done" || job.FilePath == "" || state.DownloadURL == "" {
			t.Fatal("proven snapshot was not published", job.Status, job.Error, state.DownloadURL)
		}
		download := featureCall(t, a, "GET", state.DownloadURL, "")
		data, err := os.ReadFile(job.FilePath)
		if err != nil || download.Code != 200 || !bytes.Equal(data, download.Body.Bytes()) {
			t.Fatal("download did not match the complete published file", err, download.Code)
		}
		w, err := workbook.OpenWorkbook(job.FilePath)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := w.ReadRowsWithOptionsContext(context.Background(), idx, 0, 3, 1, 3, workbook.RowReadOptions{FormatValue: true, ConditionalFormats: true})
		if err != nil || len(rows) != 3 || rows[0].Cells[0].Raw != "9007199254740993.31" || rows[0].Cells[1].Raw != "001" || rows[0].Cells[1].Type != "inlineStr" {
			t.Fatal("published values or sparse row were lost", rows, err)
		}
		for _, row := range rows {
			for _, cell := range row.Cells {
				if cell.Conditional != nil {
					t.Fatal("snapshot copied live conditional rules", cell)
				}
				if cell.Col == 3 && w.VisualStyles[cell.Style].Background != "#FFFF00" {
					t.Fatal("styled blank row was not frozen", cell)
				}
			}
		}
	}
}

func TestConditionalCellAndRangeRejectAddressesBeyondTheSourceFormat(t *testing.T) {
	a, _, _ := conditionalServerFixture(t)
	for _, url := range []string{"/api/cell?sheet=Data&ref=A1048577", "/api/range?sheet=Data&start=A1048577&end=A1048577&format=json"} {
		response := analysisRequest(t, a, "GET", url, "")
		if response.Code != 400 || !strings.Contains(response.Body.String(), "源格式范围") {
			t.Fatal("invalid source address became a fabricated blank or server error", url, response.Code, response.Body.String())
		}
	}
}
