package server

import (
	"context"
	"encoding/json"
	"html"
	"math/rand"
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

func recordFixture(t *testing.T, columns int, text string) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "record.xlsx")
	cell := func(ref, value string) string {
		return `<c r="` + ref + `" t="inlineStr"><is><t>` + html.EscapeString(value) + `</t></is></c>`
	}
	header := cell("A1", "客户") + cell("B1", "金额") + cell("C1", "重复") + cell("D1", "重复") + cell("F1", "公式") + cell("G1", "备注") + cell(workbook.ColumnName(columns)+"1", "最后字段")
	values := cell("A2", "001") + `<c r="B2" s="1"><v>9007199254740993.31</v></c>` + cell("C2", "1,234.50") + cell("D2", "00123") + `<c r="F2"><f>SUM(B2:B2)</f></c><c r="G2" t="inlineStr"><is><r><rPr><b/></rPr><t>中😀</t></r><r><t>原文</t></r></is></c><c r="H2" s="2"><v>45292</v></c>` + cell(workbook.ColumnName(columns)+"2", "末值")
	if text != "" {
		values += cell("I2", text)
	}
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><name val="Arial"/><sz val="11"/></font></fonts><cellXfs count="3"><xf numFmtId="0" fontId="0"/><xf numFmtId="4" fontId="0"/><xf numFmtId="14" fontId="0"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1">` + header + `</row><row r="2">` + values + `</row><row r="4"><c r="B4"><v>2.25</v></c></row></sheetData></worksheet>`,
	})
	a := prepareFeatureApp(t, file)
	t.Cleanup(a.Close)
	return a, file
}

func recordSource(t *testing.T, a *App, sheet string, request rowViewRequest) recordRequest {
	t.Helper()
	request.Sheet = sheet
	id := waitRowView(t, a, rowViewStart(t, a, request), "done")
	wb := a.currentWorkbook()
	return recordRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: sheet, ViewID: id, FilterID: request.FilterID, Row: 2, HeaderRow: 1, FirstColumn: 1, Columns: formats.RecordCardColumns}
}
func recordCall(t *testing.T, a *App, req recordRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return rowViewCall(t, a, "POST", "/api/record", string(data))
}
func recordOK(t *testing.T, a *App, req recordRequest) recordResponse {
	t.Helper()
	r := recordCall(t, a, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	return decodeResponse[recordResponse](t, r)
}

func TestRecordFieldsPreserveSourceTypesCoordinatesMissingCacheAndWidePages(t *testing.T) {
	a, file := recordFixture(t, formats.ExcelColumns, "")
	before, _ := os.ReadFile(file)
	req := recordSource(t, a, "First", rowViewRequest{})
	req.PinnedThroughRow = 1
	r := recordOK(t, a, req)
	if r.WorkbookKey != req.WorkbookKey || r.Book != req.Book || r.ViewID != req.ViewID || r.Row != 2 || r.HeaderRow != 1 || r.Position != 2 || r.TotalRecords != 4 || r.PreviousRow != 1 || r.NextRow != 3 || r.TotalColumns != formats.ExcelColumns || len(r.Fields) != formats.RecordCardColumns || !r.RowIndexed {
		t.Fatalf("record envelope: %+v", r)
	}
	if r.Fields[0].Header.Raw != "客户" || r.Fields[0].Cell.Raw != "001" || r.Fields[0].Cell.Type != "inlineStr" || r.Fields[1].Cell.Raw != "9007199254740993.31" || r.Fields[1].Cell.Display != "9,007,199,254,740,993.31" || r.Fields[2].Cell.Type != "inlineStr" || r.Fields[2].Cell.Raw != "1,234.50" || r.Fields[3].Cell.Raw != "00123" {
		t.Fatal("source types/precision changed", r.Fields[:4])
	}
	if r.Fields[2].Header.Raw != "重复" || r.Fields[3].Header.Raw != "重复" || r.Fields[4].Header.Raw != "" || r.Fields[4].Header.Ref != "E1" || r.Fields[4].Cell.Ref != "E2" {
		t.Fatal("blank/duplicate fields changed coordinates", r.Fields[2:5])
	}
	if f := r.Fields[5].Cell; !f.HasFormula || !f.CacheMissing || f.Raw != "" || f.Formula != "=SUM(B2:B2)" {
		t.Fatal("missing cache fabricated", f)
	}
	if c := r.Fields[6].Cell; c.Raw != "中😀原文" || c.RichText == nil || len(c.RichText.Runs) != 2 {
		t.Fatal("rich source text lost", c)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	rows, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), wb.GetCachedIndex(meta.Path), []int{2}, 1, formats.RecordCardColumns, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows[0].Cells {
		if !reflect.DeepEqual(r.Fields[c.Col-1].Cell, c) {
			t.Fatal("common CellData diverged", c.Ref)
		}
	}
	req.FirstColumn = formats.ExcelColumns - formats.RecordCardColumns + 1
	r = recordOK(t, a, req)
	last := r.Fields[len(r.Fields)-1]
	if last.Column != formats.ExcelColumns || last.Cell.Ref != "XFD2" || last.Cell.Raw != "末值" || last.Header.Raw != "最后字段" {
		t.Fatal("wide page", last)
	}
	req.FirstColumn, req.HeaderRow = formats.ExcelColumns, 0
	r = recordOK(t, a, req)
	if r.Columns != 1 || len(r.Fields) != 1 || r.Fields[0].Header != nil || r.Fields[0].Cell.Ref != "XFD2" {
		t.Fatal("last field/header-free mode", r)
	}
	req.Row = 3
	r = recordOK(t, a, req)
	if r.RowIndexed || r.Fields[0].Cell.Ref != "XFD3" || r.Fields[0].Cell.Raw != "" || r.PreviousRow != 2 || r.NextRow != 4 {
		t.Fatal("sparse blank fabricated", r)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
}

func TestRecordNavigationSharesHiddenSortedAndFrozenViewWithoutChangingMatches(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	req := recordSource(t, a, "First", rowViewRequest{})
	req.Columns, req.Row, req.PinnedThroughRow = 2, 4, 1
	r := recordOK(t, a, req)
	if r.Position != 2 || r.TotalRecords != 4 || r.PreviousRow != 1 || r.NextRow != 6 || r.Fields[1].Cell.Raw != "9007199254740993.31" {
		t.Fatal(r)
	}
	req.Row = 3
	if response := recordCall(t, a, req); response.Code != 409 {
		t.Fatal("hidden record accepted", response.Code)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["record-order"] = &FilterResult{ID: "record-order", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	req = recordSource(t, a, "First", rowViewRequest{FilterID: "record-order"})
	req.Columns, req.Row, req.PinnedThroughRow = 2, 7, 1
	r = recordOK(t, a, req)
	if r.Position != 2 || r.TotalRecords != 3 || r.PreviousRow != 1 || r.NextRow != 4 {
		t.Fatal("sorted frozen order", r)
	}
	req.PinnedThroughRow, req.Row = 6, 4
	r = recordOK(t, a, req)
	if r.Position != 2 || r.TotalRecords != 4 || r.PreviousRow != 1 || r.NextRow != 6 {
		t.Fatal("frozen visible blank/prefix", r)
	}
	if a.filterResults["record-order"].Rows.Len() != 6 {
		t.Fatal("full match set changed")
	}
}

func TestRecordNeighborsMatchesIndependentOrderAndHugeSparseDomain(t *testing.T) {
	offsets := make([]workbook.RowOffset, 100)
	for i := range offsets {
		offsets[i].Number = i + 1
	}
	idx := &workbook.SheetIndex{MaxRow: 100, RowIndex: workbook.NewRowIndex(offsets)}
	rng := rand.New(rand.NewSource(20261116))
	for sample := 0; sample < 120; sample++ {
		order := rng.Perm(100)[:20+rng.Intn(81)]
		through := rng.Intn(31)
		filter := &FilterResult{Sorted: true, Rows: rowOrderFixture(t, 100, order...)}
		v, err := newSelectionRowView(idx, filter, through)
		if err != nil {
			t.Fatal(err)
		}
		want := make([]int, through)
		for i := range want {
			want[i] = i + 1
		}
		for _, p := range order {
			if p+1 > through {
				want = append(want, p+1)
			}
		}
		for i, row := range want {
			p, total, previous, next, err := recordNeighbors(v, row)
			prev, following := 0, 0
			if i > 0 {
				prev = want[i-1]
			}
			if i+1 < len(want) {
				following = want[i+1]
			}
			if err != nil || p != i+1 || total != len(want) || previous != prev || next != following {
				t.Fatal(sample, row, p, total, previous, next, err, want)
			}
		}
	}
	sparse := &workbook.SheetIndex{MaxRow: formats.ExternalRows, RowIndex: workbook.NewRowIndex([]workbook.RowOffset{{Number: 1}, {Number: formats.ExternalRows}})}
	v, err := newSelectionRowView(sparse, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, total, previous, next, err := recordNeighbors(v, formats.ExternalRows-1)
	if err != nil || p != formats.ExternalRows-1 || total != formats.ExternalRows || previous != formats.ExternalRows-2 || next != formats.ExternalRows {
		t.Fatal(p, total, previous, next, err)
	}
}

func TestRecordTextBudgetMatchesJSONEscapesAndUnicodeCapacity(t *testing.T) {
	for _, value := range []string{"", "中😀", "<>&", "\x00\b\f\n\r\t\x1f", "\"\\", "\u2028\u2029", string([]byte{0xff, 0xfe})} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		budget := len(encoded) - 2
		if !jsonTextFits(value, &budget) || budget != 0 {
			t.Fatal("JSON cost", encoded, budget)
		}
		if len(value) > 0 {
			budget = len(encoded) - 3
			if jsonTextFits(value, &budget) {
				t.Fatal("under-budget admitted", encoded)
			}
		}
	}
	text := strings.Repeat("中😀", 100_000)
	a, _ := recordFixture(t, 10, text)
	req := recordSource(t, a, "First", rowViewRequest{})
	req.FirstColumn, req.Columns, req.HeaderRow = 9, 1, 0
	r := recordOK(t, a, req)
	if r.Fields[0].Cell.Raw != text {
		t.Fatal("Unicode truncated")
	}
	b, _ := recordFixture(t, 10, strings.Repeat("<", 750_000))
	req = recordSource(t, b, "First", rowViewRequest{})
	req.FirstColumn, req.Columns, req.HeaderRow = 9, 1, 0
	response := recordCall(t, b, req)
	if response.Code != 413 || strings.Contains(response.Body.String(), `"fields"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestRecordRejectsMalformedBindingsExpiredViewsAndSourceChanges(t *testing.T) {
	a, file := recordFixture(t, 33, "")
	req := recordSource(t, a, "First", rowViewRequest{})
	for _, change := range []func(*recordRequest){func(r *recordRequest) { r.Book = "other" }, func(r *recordRequest) { r.WorkbookKey = "other" }, func(r *recordRequest) { r.ViewID = "expired" }, func(r *recordRequest) { r.FilterID = "foreign" }} {
		copy := req
		change(&copy)
		if response := recordCall(t, a, copy); response.Code != 409 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	for _, change := range []func(*recordRequest){func(r *recordRequest) { r.ViewID = "" }, func(r *recordRequest) { r.FirstColumn = 0 }, func(r *recordRequest) { r.FirstColumn = 34 }, func(r *recordRequest) { r.Columns = 33 }, func(r *recordRequest) { r.Row = 0 }, func(r *recordRequest) { r.Row = 100 }, func(r *recordRequest) { r.HeaderRow = -1 }, func(r *recordRequest) { r.PinnedThroughRow = 31 }} {
		copy := req
		change(&copy)
		if response := recordCall(t, a, copy); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	encoded, _ := json.Marshal(req)
	for _, body := range []string{string(encoded) + ` {}`, strings.TrimSuffix(string(encoded), "}") + `,"unexpected":1}`, strings.Repeat(" ", formats.RecordCardRequestBytes+1)} {
		if response := rowViewCall(t, a, "POST", "/api/record", body); response.Code != 400 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if r := rowViewCall(t, a, "GET", "/api/record", ""); r.Code != 405 {
		t.Fatal(r.Code)
	}
	data, _ := os.ReadFile(file)
	other := filepath.Join(t.TempDir(), "same-bytes.xlsx")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(other); err != nil {
		t.Fatal(err)
	}
	if r := recordCall(t, a, req); r.Code != 409 {
		t.Fatal("equal-content other path accepted", r.Code)
	}
	wb := a.currentWorkbook()
	req.Book = sourceBookID(wb)
	if r := recordCall(t, a, req); r.Code != 409 {
		t.Fatal("old exact-index/view accepted", r.Code)
	}
	meta, _ := wb.FindSheet("First")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	req = recordSource(t, a, "First", rowViewRequest{})
	if err := os.WriteFile(other, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := recordCall(t, a, req); r.Code != 409 {
		t.Fatal("changed source published", r.Code, r.Body.String())
	}
}

func TestRecordQueuedCancellationAndExpiredOwnerReleaseSourcePins(t *testing.T) {
	a, _ := recordFixture(t, 33, "")
	req := recordSource(t, a, "First", rowViewRequest{})
	wb := a.currentWorkbook()
	for _, cancelRequest := range []bool{true, false} {
		a.rootApp().sourceMu.Lock()
		baselinePins := a.rootApp().sourcePins[wb]
		a.rootApp().sourceMu.Unlock()
		a.querySem <- struct{}{}
		a.querySem <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		data, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", "/api/record", strings.NewReader(string(data))).WithContext(ctx)
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); a.handleRecord(w, r) }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			a.rootApp().sourceMu.Lock()
			pins := a.rootApp().sourcePins[wb]
			a.rootApp().sourceMu.Unlock()
			if pins > baselinePins {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("source not pinned before queue")
			}
			time.Sleep(time.Millisecond)
		}
		if cancelRequest {
			cancel()
		} else {
			a.mu.Lock()
			v := a.rowViews[req.ViewID]
			delete(a.rowViews, req.ViewID)
			v.close()
			a.mu.Unlock()
		}
		<-a.querySem
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("queued record did not terminate")
		}
		<-a.querySem
		cancel()
		if cancelRequest && w.Body.Len() != 0 || !cancelRequest && w.Code != 409 {
			t.Fatal(cancelRequest, w.Code, w.Body.String())
		}
		a.rootApp().sourceMu.Lock()
		pins := a.rootApp().sourcePins[wb]
		a.rootApp().sourceMu.Unlock()
		expectedPins := baselinePins
		if !cancelRequest {
			expectedPins-- // The evicted view's own source pin is released too.
		}
		if pins != expectedPins {
			t.Fatal("source pin leaked", pins)
		}
	}
}

func FuzzRecordJSONTextBudget(f *testing.F) {
	for _, s := range []string{"中😀", "<>&\n", string([]byte{0xff}), "\u2028"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		budget := len(encoded) - 2
		if !jsonTextFits(value, &budget) || budget != 0 {
			t.Fatal("JSON cost", len(encoded), budget)
		}
	})
}
