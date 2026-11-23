package server

import (
	"archive/zip"
	"encoding/json"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
)

func writeSelectionWorkbook(t *testing.T, path string) {
	t.Helper()
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	files := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:D4"/><sheetData>` +
			`<row r="1"><c r="A1"><v>1.1</v></c><c r="B1"><v>2.2</v></c><c r="C1" t="inlineStr"><is><t>7</t></is></c><c r="D1"><v>0.00000000000001</v></c></row>` +
			`<row r="2"><c r="A2"><v>9007199254740993</v></c><c r="B2"><v>0.1</v></c><c r="C2"><f>A1+B1</f><v>3.3</v></c></row>` +
			`<row r="3"><c r="A3"><v>-0.2</v></c><c r="B3" s="1"/><c r="C3" t="str"><f>IF(TRUE,"done","")</f><v>done</v></c></row>` +
			`<row r="4"><c r="A4"><v>0</v></c><c r="B4"><f>SUM(A1:A3)</f><v>-0</v></c></row>` +
			`</sheetData></worksheet>`,
	}
	for name, data := range files {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(data)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestSelectionStatsExactDisjointAndOverlapping(t *testing.T) {
	isolateTestStorage(t)
	p := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeSelectionWorkbook(t, p)
	a := prepareFeatureApp(t, p)
	defer a.Close()
	cases := []struct {
		label, input             string
		cells, nonempty, numbers int64
		sum, avg                 string
	}{
		{"disjoint", `{"sheet":"First","ranges":[{"start":"A1","end":"A1"},{"start":"B1","end":"B1"},{"start":"C2","end":"C2"}]}`, 3, 3, 3, "6.6", "2.2"},
		{"overlap", `{"sheet":"First","ranges":[{"start":"A1","end":"B2"},{"start":"B2","end":"C3"}]}`, 7, 6, 5, "9007199254740999.7", "1801439850948199.94"},
		{"scientific-precision", `{"sheet":"First","ranges":[{"start":"D1","end":"D1"},{"start":"A4","end":"B4"}]}`, 3, 3, 3, "0.00000000000001", "0.0000000000000033333333333333 ≈"},
		{"empty-and-text", `{"sheet":"First","ranges":[{"start":"B3","end":"C3"},{"start":"C1","end":"C1"}]}`, 3, 2, 0, "0", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			out := featureCall(t, a, http.MethodPost, "/api/selection/stats", tc.input)
			var got selectionStatResult
			if e := json.Unmarshal(out.Body.Bytes(), &got); e != nil {
				t.Fatal(e)
			}
			if got.Total != tc.cells || got.NonEmpty != tc.nonempty || got.Number != tc.numbers || got.Sum != tc.sum || got.Average != tc.avg {
				t.Fatalf("got %+v, want cells=%d nonempty=%d number=%d sum=%s avg=%s", got, tc.cells, tc.nonempty, tc.numbers, tc.sum, tc.avg)
			}
		})
	}
}
func TestSelectionStatsConfirmedRevisionAndSource(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "统计.csv")
	if err := os.WriteFile(file, []byte("金额,调整\n12,\n8,0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	book := sourceBookID(wb)
	doc := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: book}, 200)).Document
	ch := func(row, col int, raw string) editdoc.Change {
		return editdoc.Change{Address: editdoc.Address{Sheet: meta.Name, Row: row, Col: col}, Value: editdoc.Value{Kind: "text", Raw: raw}}
	}
	op := editdoc.Operation{Revision: doc.Revision, Request: strings.Repeat("a", 32), Action: "change", Changes: []editdoc.Change{ch(2, 1, "0012"), ch(2, 2, "9007199254740993.25")}}
	editHTTP(t, a, "commit", editRequest{Document: doc.Key, Operation: op}, 200)
	req := selectionStatRequest{Sheet: meta.Name, Ranges: []selectionStatRect{{Start: "A2", End: "B3"}, {Start: "A2", End: "B2"}}}
	call := func(status int) selectionStatResult {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		r := localTestConnection(httptest.NewRequest(http.MethodPost, "/api/selection/stats", strings.NewReader(string(data))))
		r.Header.Set("X-ExcelLens-Token", a.token)
		out := httptest.NewRecorder()
		a.Handler().ServeHTTP(out, r)
		if out.Code != status {
			t.Fatalf("status %d, response %s", out.Code, out.Body.String())
		}
		if status != 200 {
			return selectionStatResult{}
		}
		return decodeResponse[selectionStatResult](t, out)
	}
	if got := call(200); got.Sum != "20" || got.NonEmpty != 3 || got.Number != 3 {
		t.Fatal(got)
	}
	req.Edit = &selectionStatEdit{Book: book, Document: doc.Key, Revision: doc.Revision + 1}
	if got := call(200); got.Sum != "9007199254741001.25" || got.Minimum != "0" || got.Maximum != "9007199254740993.25" || got.NonEmpty != 4 || got.Number != 3 {
		t.Fatal(got)
	}
	req.Edit.Revision = doc.Revision
	call(409)
}

func TestSelectionStatsFilteredUsesVisualOrder(t *testing.T) {
	isolateTestStorage(t)
	p := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeSelectionWorkbook(t, p)
	a := prepareFeatureApp(t, p)
	defer a.Close()
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	if idx.RowCount() != 4 {
		t.Fatalf("rows=%d", idx.RowCount())
	}
	a.mu.Lock()
	a.filterResults["sorted"] = &FilterResult{ID: "sorted", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 2, 0)}
	a.mu.Unlock()
	got := selectionStatResult{}
	res := featureCall(t, a, http.MethodPost, "/api/selection/stats", `{"sheet":"First","filterId":"sorted","ranges":[{"start":"A3","end":"A3","visualStart":1,"visualEnd":1},{"start":"B1","end":"B1","visualStart":2,"visualEnd":2}]}`)
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Number != 2 || got.Sum != "2" || got.Average != "1" || !got.Filtered {
		t.Fatalf("filtered stats=%+v", got)
	}
}

func TestSelectionStatsFilterFrozenHeaderAndSortedOrder(t *testing.T) {
	isolateTestStorage(t)
	p := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeSelectionWorkbook(t, p)
	a := prepareFeatureApp(t, p)
	defer a.Close()
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	// The sorted order deliberately places the frozen header in the middle:
	// the old prefix-based exclusion silently counted it as another data row.
	a.mu.Lock()
	a.filterResults["frozen"] = &FilterResult{ID: "frozen", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, wb.GetCachedIndex(meta.Path).RowCount(), 2, 0, 1)}
	a.mu.Unlock()
	cases := []struct {
		body, sum string
		total     int64
	}{
		{`{"sheet":"First","filterId":"frozen","excludeThroughRow":1,"ranges":[{"start":"A1","end":"A1","visualStart":1,"visualEnd":1}]}`, "1.1", 1},
		{`{"sheet":"First","filterId":"frozen","excludeThroughRow":1,"ranges":[{"start":"A3","end":"A3","visualStart":1,"visualEnd":1}]}`, "-0.2", 1},
		{`{"sheet":"First","filterId":"frozen","excludeThroughRow":1,"ranges":[{"start":"A1","end":"A3","visualStart":1,"visualEnd":1}]}`, "0.9", 2},
		{`{"sheet":"First","filterId":"frozen","excludeThroughRow":1,"ranges":[{"start":"A3","end":"A1","visualStart":1,"visualEnd":1}]}`, "0.9", 2},
		{`{"sheet":"First","filterId":"frozen","excludeThroughRow":1,"ranges":[{"start":"A1","end":"A3","visualStart":1}]}`, "", -1},
	}
	for _, tc := range cases {
		if tc.total < 0 {
			req := httptest.NewRequest(http.MethodPost, "/api/selection/stats", strings.NewReader(tc.body))
			req = localTestConnection(req)
			req.Header.Set("X-ExcelLens-Token", a.token)
			out := httptest.NewRecorder()
			a.Handler().ServeHTTP(out, req)
			if out.Code == http.StatusOK {
				t.Fatalf("accepted invalid frozen request: %s", out.Body.String())
			}
			continue
		}
		out := featureCall(t, a, http.MethodPost, "/api/selection/stats", tc.body)
		if out.Code != http.StatusOK {
			t.Fatalf("response %d: %s", out.Code, out.Body.String())
		}
		var got selectionStatResult
		if e := json.Unmarshal(out.Body.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if got.Total != tc.total || got.Sum != tc.sum {
			t.Errorf("filtered frozen %+v, want %d %s", got, tc.total, tc.sum)
		}
	}
}

func TestSelectionStatsRejectsInvalidWithoutReading(t *testing.T) {
	isolateTestStorage(t)
	p := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeSelectionWorkbook(t, p)
	a := prepareFeatureApp(t, p)
	defer a.Close()
	for _, body := range []string{
		`{"sheet":"First","ranges":[]}`,
		`{"sheet":"First","ranges":[{"start":"A1","end":"A400001"}]}`,
		`{"sheet":"First","ranges":[{"start":"ZZZ0","end":"A1"}]}`,
		`{"sheet":"First","filterId":"missing","ranges":[{"start":"A1","end":"A1","visualStart":1,"visualEnd":1}]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/selection/stats", strings.NewReader(body))
		req = localTestConnection(req)
		req.Header.Set("X-ExcelLens-Token", a.token)
		out := httptest.NewRecorder()
		a.Handler().ServeHTTP(out, req)
		if out.Code == http.StatusOK {
			t.Fatalf("invalid body accepted: %s => %s", body, out.Body.String())
		}
	}
}
func TestMergeStatIntervalsProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261110))
	for k := 0; k < 500; k++ {
		items := make([]statColInterval, rng.Intn(18)+1)
		ref := map[int]bool{}
		for i := range items {
			lo := rng.Intn(40) + 1
			hi := lo + rng.Intn(12)
			items[i] = statColInterval{lo, hi}
			for c := lo; c <= hi; c++ {
				ref[c] = true
			}
		}
		out := mergeStatIntervals(items)
		got := map[int]bool{}
		for i, part := range out {
			if i > 0 && out[i-1].hi >= part.lo {
				t.Fatalf("overlapping merged spans: %+v", out)
			}
			for c := part.lo; c <= part.hi; c++ {
				got[c] = true
			}
		}
		if len(got) != len(ref) {
			t.Fatalf("coverage mismatch: ref=%v got=%v", ref, got)
		}
	}
}
func TestSelectionRatTextNoFloatLoss(t *testing.T) {
	for input, want := range map[string]string{"9007199254740993": "9007199254740993", "1/8": "0.125", "-0/1": "0", "1/100000000000000": "0.00000000000001", "1/3": "0.33333333333333 ≈"} {
		v, ok := new(big.Rat).SetString(input)
		if !ok {
			t.Fatal(input)
		}
		if got := selectionRatText(v); got != want {
			t.Errorf("%s: got %q want %q", input, got, want)
		}
	}
}
