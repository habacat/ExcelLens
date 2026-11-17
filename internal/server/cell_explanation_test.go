package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func cellExplanationFixture(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "explanation.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2">
<c r="A2"><v>9007199254740993.31</v></c><c r="B2" t="inlineStr"><is><t>1,234.50</t></is></c><c r="C2" t="inlineStr"><is><t>00123</t></is></c>
<c r="D2"><f>A2*2</f><v>18014398509481986.62</v></c><c r="E2"><f>A2*2</f></c><c r="F2"><f t="shared" si="19"/><v>17</v></c><c r="G2" t="e"><f>1/0</f><v>#DIV/0!</v></c>
<c r="H2" t="b"><v>1</v></c><c r="I2" t="str"><f>""</f><v/></c><c r="J2"><v>0</v></c><c r="K2"><v>invalid</v></c><c r="L2" t="d"><v>2026-11-16</v></c>
</row><row r="4"><c r="A4"><v>-2.25</v></c></row></sheetData></worksheet>`,
	})
	a := prepareFeatureApp(t, file)
	t.Cleanup(a.Close)
	return a, file
}

func cellExplanationSource(t *testing.T, a *App, sheet, ref string, view rowViewRequest) cellExplanationRequest {
	t.Helper()
	r := recordSource(t, a, sheet, view)
	return cellExplanationRequest{WorkbookKey: r.WorkbookKey, Book: r.Book, Sheet: sheet, ViewID: r.ViewID, FilterID: r.FilterID, Ref: ref, PinnedThroughRow: 1}
}

func cellExplanationCall(t *testing.T, a *App, req cellExplanationRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return rowViewCall(t, a, "POST", "/api/cell/explain", string(data))
}

func cellExplanationOK(t *testing.T, a *App, req cellExplanationRequest) cellExplanationResponse {
	t.Helper()
	r := cellExplanationCall(t, a, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	return decodeResponse[cellExplanationResponse](t, r)
}

func TestCellExplanationPreservesExactTypesAndDistinguishesFormulaStates(t *testing.T) {
	a, file := cellExplanationFixture(t)
	before := printScaleHash(t, file)
	req := cellExplanationSource(t, a, "First", "A2", rowViewRequest{})
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet(req.Sheet)
	idx := wb.GetCachedIndex(meta.Path)
	for _, tc := range []struct {
		ref, reason, exact, formula string
		available                   bool
	}{
		{"A2", "number", "9007199254740993.31", "none", false},
		{"B2", "source-text", "", "none", false}, {"C2", "source-text", "", "none", false},
		{"D2", "number", "18014398509481986.62", "cached", true}, {"E2", "missing-result", "", "missing", true},
		{"F2", "number", "17", "cached", false}, {"G2", "error", "", "cached", true}, {"H2", "boolean", "", "none", false},
		{"I2", "blank", "", "cached", true}, {"J2", "number", "0", "none", false}, {"K2", "invalid-number", "", "none", false}, {"L2", "date", "", "none", false},
		{"A3", "blank", "", "none", false}, {"XFD1048576", "blank", "", "none", false},
	} {
		req.Ref = tc.ref
		r := cellExplanationOK(t, a, req)
		if r.Numeric.Reason != tc.reason || r.Numeric.Eligible != (tc.exact != "") || r.Numeric.Exact != tc.exact || r.FormulaState != tc.formula || r.FormulaAvailable != tc.available || r.cellExplanationRequest != req {
			t.Fatal(tc.ref, r)
		}
		col, row, _ := workbook.ParseCellRef(tc.ref)
		rows, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{row}, col, col, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range rows {
			for _, cell := range data.Cells {
				if cell.Col == col && !reflect.DeepEqual(cell, r.Cell) {
					t.Fatal("changed source cell", tc.ref, cell, r.Cell)
				}
			}
		}
		if tc.ref == "XFD1048576" && r.RowState != "outside" {
			t.Fatal(r.RowState)
		}
	}
	if printScaleHash(t, file) != before {
		t.Fatal("source changed")
	}
}

func TestCellExplanationUsesOnlySuccessfulFilterSnapshotAndReportsPinnedRows(t *testing.T) {
	a, _ := cellExplanationFixture(t)
	spec := newFilterSpec([]workbook.FilterRule{{Column: 1, Operator: "lt", Value: "0"}}, workbook.FilterOptions{HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 1, Descending: true, Mode: "number"}}})
	job := startViewFilter(t, a, spec)
	req := cellExplanationSource(t, a, "First", "A4", rowViewRequest{FilterID: job.ResultID})
	for _, tc := range []struct {
		ref, state string
		pinned     int
	}{{"A4", "matched", 1}, {"A1", "header", 1}, {"A2", "pinned", 2}, {"A2", "filtered-out", 1}} {
		req.Ref, req.PinnedThroughRow = tc.ref, tc.pinned
		r := cellExplanationOK(t, a, req)
		if r.Filter == nil || !reflect.DeepEqual(*r.Filter, spec) || r.RowState != tc.state {
			t.Fatal(tc, r)
		}
	}
	// Removing the filter map owner must not substitute a different query into
	// the view's retained immutable result.
	a.mu.Lock()
	f := a.filterResults[job.ResultID]
	delete(a.filterResults, job.ResultID)
	f.close()
	a.mu.Unlock()
	req.Ref = "A4"
	r := cellExplanationOK(t, a, req)
	if r.Filter == nil || !reflect.DeepEqual(*r.Filter, spec) {
		t.Fatal(r.Filter)
	}
}

func TestCellExplanationRejectsStaleBindingsMalformedRequestsAndSourceChanges(t *testing.T) {
	a, file := cellExplanationFixture(t)
	req := cellExplanationSource(t, a, "First", "A2", rowViewRequest{})
	for _, change := range []func(*cellExplanationRequest){func(r *cellExplanationRequest) { r.Book = "foreign" }, func(r *cellExplanationRequest) { r.WorkbookKey = "foreign" }, func(r *cellExplanationRequest) { r.ViewID = "expired" }, func(r *cellExplanationRequest) { r.FilterID = "foreign" }} {
		copy := req
		change(&copy)
		if r := cellExplanationCall(t, a, copy); r.Code != 409 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, ref := range []string{"", "A0", "$A$2", "a2", "A1048577", "XFE2"} {
		copy := req
		copy.Ref = ref
		if r := cellExplanationCall(t, a, copy); r.Code != 400 {
			t.Fatal(ref, r.Code, r.Body.String())
		}
	}
	for _, change := range []func(*cellExplanationRequest){func(r *cellExplanationRequest) { r.ViewID = "" }, func(r *cellExplanationRequest) { r.PinnedThroughRow = 31 }, func(r *cellExplanationRequest) { r.PinnedThroughRow = -1 }} {
		copy := req
		change(&copy)
		if r := cellExplanationCall(t, a, copy); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	data, _ := json.Marshal(req)
	for _, body := range []string{string(data) + ` {}`, strings.TrimSuffix(string(data), "}") + `,"unknown":1}`, strings.Repeat(" ", formats.CellExplanationRequestBytes+1)} {
		if r := rowViewCall(t, a, "POST", "/api/cell/explain", body); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := rowViewCall(t, a, "GET", "/api/cell/explain", ""); r.Code != 405 {
		t.Fatal(r.Code)
	}
	original, _ := os.ReadFile(file)
	other := filepath.Join(t.TempDir(), "same-content.xlsx")
	if err := os.WriteFile(other, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(other); err != nil {
		t.Fatal(err)
	}
	if r := cellExplanationCall(t, a, req); r.Code != 409 {
		t.Fatal("same content different path accepted", r.Code)
	}
	wb := a.currentWorkbook()
	req.Book = sourceBookID(wb)
	if r := cellExplanationCall(t, a, req); r.Code != 409 {
		t.Fatal("old index/view accepted", r.Code)
	}
	meta, _ := wb.FindSheet("First")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	req = cellExplanationSource(t, a, "First", "A2", rowViewRequest{})
	wb.ClearIndexes()
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := cellExplanationCall(t, a, req); r.Code != 409 {
		t.Fatal("replacement index accepted", r.Code)
	}
	req = cellExplanationSource(t, a, "First", "A2", rowViewRequest{})
	if err := os.WriteFile(other, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := cellExplanationCall(t, a, req); r.Code != 409 {
		t.Fatal("changed source published", r.Code, r.Body.String())
	}
}

func TestCellExplanationCapacityBeforeRowIOAndBeforeEscapedResponse(t *testing.T) {
	idx := &workbook.SheetIndex{XMLPath: filepath.Join(t.TempDir(), "absent.xml"), XMLSize: formats.CellExplanationSourceBytes + 1, Rows: []workbook.RowOffset{{Number: 2, End: formats.CellExplanationSourceBytes + 1}}}
	_, err := readCellExplanation(context.Background(), &workbook.Workbook{}, idx, cellExplanationRequest{Ref: "A2"}, 2, 1)
	if !errors.Is(err, errCellExplanationCapacity) {
		t.Fatal("oversized source reached IO", err)
	}
	idx.Rows[0].Start = -1
	_, err = readCellExplanation(context.Background(), &workbook.Workbook{}, idx, cellExplanationRequest{Ref: "A2"}, 2, 1)
	if err == nil || errors.Is(err, errCellExplanationCapacity) || !strings.Contains(err.Error(), "缓存范围") {
		t.Fatal(err)
	}
	a, _ := recordFixture(t, 10, strings.Repeat("<", 400_000))
	req := cellExplanationSource(t, a, "First", "I2", rowViewRequest{})
	if r := cellExplanationCall(t, a, req); r.Code != 413 || strings.Contains(r.Body.String(), `"cell"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	b, _ := recordFixture(t, 10, strings.Repeat("中😀", 50_000))
	req = cellExplanationSource(t, b, "First", "I2", rowViewRequest{})
	if got := cellExplanationOK(t, b, req); got.Cell.Raw != strings.Repeat("中😀", 50_000) {
		t.Fatal("Unicode truncated")
	}
	if cellExplanationFits(&cellExplanationResponse{Filter: &filterSpec{Rules: []workbook.FilterRule{{Value: strings.Repeat("&", 400_000)}}}}) {
		t.Fatal("filter budget bypassed")
	}
}

func TestCellExplanationQueuedCancellationAndEvictionReleasePins(t *testing.T) {
	for _, action := range []string{"cancel", "evict", "maintenance", "switch"} {
		t.Run(action, func(t *testing.T) {
			a, file := cellExplanationFixture(t)
			req := cellExplanationSource(t, a, "First", "A2", rowViewRequest{})
			wb := a.currentWorkbook()
			a.sourceMu.Lock()
			baseline := a.sourcePins[wb]
			a.sourceMu.Unlock()
			for i := 0; i < cap(a.querySem); i++ {
				a.querySem <- struct{}{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body, _ := json.Marshal(req)
			r := httptest.NewRequest("POST", "/api/cell/explain", strings.NewReader(string(body))).WithContext(ctx)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); a.handleCellExplanation(w, r) }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				a.sourceMu.Lock()
				pins := a.sourcePins[wb]
				a.sourceMu.Unlock()
				if pins > baseline {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatal("source not pinned before queue")
				}
				time.Sleep(time.Millisecond)
			}
			switch action {
			case "cancel":
				cancel()
			case "evict":
				a.mu.Lock()
				v := a.rowViews[req.ViewID]
				delete(a.rowViews, req.ViewID)
				v.close()
				a.mu.Unlock()
			case "maintenance":
				a.mu.Lock()
				a.maintenance = true
				a.mu.Unlock()
			case "switch":
				data, _ := os.ReadFile(file)
				other := filepath.Join(t.TempDir(), "other.xlsx")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := a.OpenPath(other); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < cap(a.querySem); i++ {
				<-a.querySem
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("queued explanation did not exit")
			}
			a.mu.Lock()
			a.maintenance = false
			a.mu.Unlock()
			if action == "cancel" && w.Body.Len() != 0 || action != "cancel" && w.Code != 409 {
				t.Fatal(action, w.Code, w.Body.String())
			}
			a.sourceMu.Lock()
			pins := a.sourcePins[wb]
			a.sourceMu.Unlock()
			expected := baseline
			if action == "evict" || action == "switch" {
				expected--
			}
			if pins != expected {
				t.Fatal("source pins leaked", pins, expected)
			}
		})
	}
}
