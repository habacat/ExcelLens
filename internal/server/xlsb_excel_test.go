package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

func TestXLSBExcelReadOnlyWorkflow(t *testing.T) {
	dir := os.Getenv("EXCELLENS_XLSB_EXCEL_PAIRS")
	if dir == "" {
		dir = filepath.Join("..", "workbook", "testdata", "xlsb-excel")
	}
	isolateTestStorage(t)
	path := filepath.Join(dir, "fidelity2.xlsb")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	w := a.currentWorkbook()
	for _, meta := range w.Sheets[:2] {
		if _, err := w.BuildIndex(meta, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	meta, ledger := w.Sheets[0], w.Sheets[1]
	sheet := url.QueryEscape(meta.Name)
	if !w.SourceInfo.ReadOnly || w.Format != "xlsb" {
		t.Fatal(w.SourceInfo)
	}
	view := decodeResponse[freezeSettings](t, featureCall(t, a, "GET", "/api/view?sheet="+sheet, ""))
	if view != (freezeSettings{Rows: 3, Cols: 1}) {
		t.Fatal("source freeze lost", view)
	}
	cell := decodeResponse[struct{ Cell workbook.CellData }](t, featureCall(t, a, "GET", "/api/cell?sheet="+sheet+"&ref=B2", "")).Cell
	if cell.Raw != "123456789.123456" || cell.Conditional == nil || cell.Conditional.Visual.Background != "#88FF88" {
		t.Fatal("native conditional style or exact source value lost", cell)
	}
	search := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/search/start", map[string]string{
		"sheet": meta.Name, "query": "formula:SUM", "scope": "current", "mode": "advanced",
	}))
	a.mu.RLock()
	job := a.searchJobs[search.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("search was not admitted")
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("search timed out")
	}
	result := decodeResponse[struct {
		Status  string
		Matches []workbook.SearchResult
	}](t, featureCall(t, a, "GET", "/api/search/status?id="+search.JobID, ""))
	if result.Status != "done" || len(result.Matches) != 3 {
		t.Fatal("formula search lost an indexed row", result)
	}
	filter := startViewFilter(t, a, filterSpec{HeaderRow: 1,
		Rules:    []workbook.FilterRule{{Column: 1, Operator: "in", Values: []string{"000012", "000020", "000021", "000025"}}},
		SortKeys: []workbook.SortKey{{Column: 2, Mode: "number", Descending: true}},
	})
	window := decodeResponse[struct{ Rows []workbook.RowData }](t, featureCall(t, a, "GET", "/api/sheet?sheet="+sheet+"&filterId="+filter.ResultID+"&limit=10", ""))
	var coordinates []int
	for _, row := range window.Rows {
		coordinates = append(coordinates, row.Number)
	}
	if !reflect.DeepEqual(coordinates, []int{1, 2, 5, 4, 3}) {
		t.Fatal("sort did not retain source row coordinates", coordinates)
	}
	for _, sample := range []struct {
		col, sum string
		count    int
	}{{"A", "0", 0}, {"B", "107.5011", 3}} {
		stats := decodeResponse[selectionStatResult](t, reconcilePost(t, a, "/api/selection/stats", map[string]any{
			"sheet": meta.Name, "ranges": []map[string]string{{"start": sample.col + "3", "end": sample.col + "5"}},
		}))
		if stats.Sum != sample.sum || int(stats.Number) != sample.count {
			t.Fatal("native text and numeric values conflated", sample, stats)
		}
	}
	query, err := json.Marshal(map[string]any{"source": "wb:" + ledger.Name, "headerRow": 1, "rows": []string{"C4"}, "value": "C5", "aggregate": "sum", "precision": 2})
	if err != nil {
		t.Fatal(err)
	}
	analysis := decodeResponse[struct{ JobID string }](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", string(query)))
	if total := awaitAnalysis(t, a, analysis.JobID).Grand.Exact; total != "231.75" {
		t.Fatal("analysis total differs", total)
	}
	right := attachReconcileCSV(t, a, "comparison.csv", "ID,Amount\nA,123\nB,-12.5\nC,1.25\nD,120\n")
	checked := startReconcile(t, a, reconcileRef(t, a, sourceBookID(w), "wb:"+ledger.Name, 1), reconcileRef(t, a, right, "wb:数据", 1),
		reconcile.Query{Keys: []reconcile.Pair{{Left: 4, Right: 1}}, Fields: []reconcile.Pair{{Left: 5, Right: 2, Mode: "number"}}})
	if checked.Status != "done" || checked.Result.Counts != (reconcile.Counts{Equal: 4, LeftRecords: 4, RightRecords: 4}) {
		t.Fatal("XLSB/CSV reconciliation differs", checked.Status, checked.Error, checked.Result.Counts)
	}
	output := startRowExport(t, a, exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: filter.ResultID, Format: "csv", Mode: "values"})
	waitRowExport(t, output, "done")
	data, err := os.ReadFile(output.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 5 || rows[1][0] != "000012" || rows[1][1] != "123456789.123456" || rows[4][0] != "000020" {
		t.Fatal("export lost raw value or sort order", rows, err)
	}
	formulaOutput := startRowExport(t, a, exportRequest{Scope: "sheet", Sheet: meta.Name, Format: "xlsx", Mode: "formula"})
	waitRowExport(t, formulaOutput, "done")
	zipFile, err := zip.OpenReader(formulaOutput.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zipFile.Close()
	content, err := zipFile.Open("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	xml, err := io.ReadAll(content)
	if err != nil || !bytes.Contains(xml, []byte(`<c r="F2"><f t="array" ref="F2:F5">(B2:B5*2)</f>`)) ||
		!bytes.Contains(xml, []byte(`<c r="F3"><v>-25</v></c>`)) {
		t.Fatal("XLSX export changed the array range into independent formulas", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(original, after) {
		t.Fatal("XLSB source changed", err)
	}
}
