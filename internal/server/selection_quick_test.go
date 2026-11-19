package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func quickFixture(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "quick.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>金额</t></is></c><c r="F1" t="inlineStr"><is><t>尾列</t></is></c></row>
<row r="2"><c r="A2"><v>9007199254740993.31</v></c><c r="B2"><v>9007199254740993.30</v></c><c r="C2" t="inlineStr"><is><t>7</t></is></c><c r="D2" t="e"><v>#DIV/0!</v></c><c r="E2"><f>A2+B2</f></c><c r="F2"><v>NaN</v></c></row>
<row r="3"><c r="A3"><v>-0.01</v></c><c r="B3"><v>0.01</v></c><c r="C3" t="inlineStr"><is><t>001</t></is></c><c r="D3" t="b"><v>1</v></c></row>
<row r="4"><c r="A4"><v>-0</v></c></row>
<row r="6"><c r="A6"><v>1e-308</v></c><c r="B6"><v>0</v></c><c r="C6"><f>1/2</f><v>0.5</v></c></row>
</sheetData></worksheet>`,
	})
	a := prepareFeatureApp(t, file)
	t.Cleanup(a.Close)
	return a, file
}

func quickRequest(t *testing.T, a *App, sheet string, view rowViewRequest, start, end string) selectionQuickRequest {
	t.Helper()
	record := recordSource(t, a, sheet, view)
	return selectionQuickRequest{WorkbookKey: record.WorkbookKey, Book: record.Book, Sheet: sheet, FilterID: record.FilterID,
		analysisSelectionCoordinates: analysisSelectionCoordinates{ViewID: record.ViewID}, Start: start, End: end}
}

func quickCall(t *testing.T, a *App, req selectionQuickRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return rowViewCall(t, a, "POST", "/api/selection/quick", string(data))
}

func quickOK(t *testing.T, a *App, req selectionQuickRequest) selectionQuickResponse {
	t.Helper()
	r := quickCall(t, a, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	return decodeResponse[selectionQuickResponse](t, r)
}

func TestSelectionQuickExactTypesCompleteRowsAndPairedDifferences(t *testing.T) {
	a, file := quickFixture(t)
	before := printScaleHash(t, file)
	req := quickRequest(t, a, "First", rowViewRequest{}, "A1", "F6")
	req.SkipFirstRow, req.Minuend, req.Subtrahend = true, 2, 1
	r := quickOK(t, a, req)
	if r.SelectedRows != 6 || r.Rows != 5 || len(r.Columns) != 6 || r.Book != req.Book || r.ViewID != req.ViewID {
		t.Fatal(r)
	}
	counts := r.Columns[0].Counts
	if counts.Number != 4 || counts.Negative != 1 || counts.Zero != 1 || counts.Positive != 2 || counts.Blank != 1 {
		t.Fatal(counts)
	}
	if r.Columns[2].Counts.Other != 2 || r.Columns[2].Counts.Number != 1 || r.Columns[3].Counts.Errors != 1 || r.Columns[3].Counts.Other != 1 || r.Columns[4].Counts.Missing != 1 || r.Columns[5].Counts.Invalid != 1 {
		t.Fatal("source types collapsed", r.Columns)
	}
	for _, col := range r.Columns {
		c := col.Counts
		if c.Number != c.Positive+c.Negative+c.Zero || c.Number+c.Blank+c.Other+c.Errors+c.Missing+c.Invalid != int64(r.Rows) || col.Summary.Count != c.Number || len(col.Segments) != r.Rows {
			t.Fatal("incomplete partition", col)
		}
	}
	if r.Difference.Paired != 3 || r.Difference.Unpaired != 2 || r.Difference.Segments[3].Summary.Count != 0 || r.Difference.Segments[3].FirstRow != 5 {
		t.Fatal(r.Difference)
	}
	got, ok := new(big.Rat).SetString(r.Difference.Summary.Sum)
	want := new(big.Rat).Sub(big.NewRat(1, 100), new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(308), nil)))
	if !ok || got.Cmp(want) != 0 || r.Difference.Summary.MinimumRow != 2 || r.Difference.Summary.MaximumRow != 3 {
		t.Fatal("difference rounded or unpaired zeros included", r.Difference.Summary, want)
	}
	if printScaleHash(t, file) != before {
		t.Fatal("read-only source changed")
	}
	req.Start, req.End = "A1", "A1"
	req.Minuend, req.Subtrahend = 0, 0
	empty := quickOK(t, a, req)
	if empty.Rows != 0 || empty.Columns[0].Summary.Average != "" || len(empty.Columns[0].Segments) != 0 {
		t.Fatal("skipping sole row created values", empty)
	}
}

func TestSelectionQuickUsesHiddenSortedFrozenEndpointPairs(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	req := quickRequest(t, a, "First", rowViewRequest{}, "A1", "B7")
	req.ExcludeThroughRow = 1
	r := quickOK(t, a, req)
	if r.Rows != 4 || r.Columns[0].Segments[1].FirstRow != 4 || r.Columns[0].Segments[2].FirstRow != 6 {
		t.Fatal("hidden rows entered selected stream", r)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["quick-order"] = &FilterResult{ID: "quick-order", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	req = quickRequest(t, a, "First", rowViewRequest{FilterID: "quick-order"}, "A4", "B1")
	req.VisualStart, req.VisualEnd, req.ExcludeThroughRow = 2, 1, 1
	r = quickOK(t, a, req)
	if r.Rows != 3 || r.Columns[0].Segments[0].FirstRow != 1 || r.Columns[0].Segments[1].FirstRow != 7 || r.Columns[0].Segments[2].FirstRow != 4 {
		t.Fatal("drag endpoint pairing or frozen prefix changed", r)
	}
	req.VisualStart = 1
	if out := quickCall(t, a, req); out.Code != 400 {
		t.Fatal("mismatched source/visual endpoint accepted", out.Code, out.Body.String())
	}
}

func TestSelectionQuickRejectsForeignStaleMalformedAndChangedSources(t *testing.T) {
	a, file := quickFixture(t)
	req := quickRequest(t, a, "First", rowViewRequest{}, "A1", "B6")
	for _, mutate := range []func(*selectionQuickRequest){
		func(r *selectionQuickRequest) { r.Book = "foreign" },
		func(r *selectionQuickRequest) { r.WorkbookKey = "foreign" },
		func(r *selectionQuickRequest) { r.ViewID = "" },
		func(r *selectionQuickRequest) { r.ViewID = "expired" },
		func(r *selectionQuickRequest) { r.FilterID = "foreign" },
		func(r *selectionQuickRequest) { r.Start = "A0" },
		func(r *selectionQuickRequest) { r.End = "Q6" },
		func(r *selectionQuickRequest) { r.ExcludeThroughRow = -1 },
		func(r *selectionQuickRequest) { r.Minuend, r.Subtrahend = 1, 1 },
		func(r *selectionQuickRequest) { r.Minuend = 1 },
		func(r *selectionQuickRequest) { r.Minuend, r.Subtrahend = 1, 3 },
	} {
		bad := req
		mutate(&bad)
		if out := quickCall(t, a, bad); out.Code == 200 || strings.Contains(out.Body.String(), `"columns"`) {
			t.Fatal("invalid selection published", bad, out.Code, out.Body.String())
		}
	}
	data, _ := json.Marshal(req)
	for _, body := range []string{string(data) + `{}`, strings.TrimSuffix(string(data), "}") + `,"unknown":1}`, `{"sheet":"` + strings.Repeat("x", formats.SelectionQuickRequestBytes) + `"}`} {
		if out := rowViewCall(t, a, "POST", "/api/selection/quick", body); out.Code != 400 {
			t.Fatal("malformed JSON accepted", out.Code)
		}
	}
	if out := rowViewCall(t, a, "GET", "/api/selection/quick", ""); out.Code != 405 {
		t.Fatal(out.Code)
	}
	a.mu.Lock()
	a.maintenance = true
	a.mu.Unlock()
	out := quickCall(t, a, req)
	a.mu.Lock()
	a.maintenance = false
	a.mu.Unlock()
	if out.Code != 409 {
		t.Fatal("maintenance published result", out.Code)
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := quickCall(t, a, req); out.Code != 409 {
		t.Fatal("changed source published result", out.Code, out.Body.String())
	}
}

func TestSelectionQuickBudgetBeforeIOAndCancellation(t *testing.T) {
	idx := &workbook.SheetIndex{MaxRow: 400001, MaxCol: 16, XMLPath: filepath.Join(t.TempDir(), "absent"), XMLSize: formats.SelectionQuickSourceBytes + 1,
		RowIndex: workbook.NewRowIndex([]workbook.RowOffset{{Number: 1, Start: 0, End: formats.SelectionQuickSourceBytes + 1}})}
	req := selectionQuickRequest{Start: "A1", End: "A400001"}
	if _, _, _, err := quickSelectionPlan(idx, selectionSnapshot{}, req); err == nil {
		t.Fatal("excess cell count accepted")
	}
	view, _ := newSelectionRowView(idx, nil, 0)
	plan, _ := view.plan(1, 1, 0, 0)
	if _, err := readSelectionQuick(context.Background(), &workbook.Workbook{}, plan, 1, 1, req); !errors.Is(err, errQuickCapacity) {
		t.Fatal("oversized source reached IO", err)
	}
	corrupt := idx.RowAt(0)
	corrupt.End = idx.XMLSize + 1
	idx.RowIndex = workbook.NewRowIndex([]workbook.RowOffset{corrupt})
	if _, err := readSelectionQuick(context.Background(), &workbook.Workbook{}, plan, 1, 1, req); err == nil || errors.Is(err, errQuickCapacity) {
		t.Fatal("corrupt descriptor disguised as limit", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readSelectionQuick(ctx, &workbook.Workbook{}, plan, 1, 1, req); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled analysis reached IO", err)
	}
}
