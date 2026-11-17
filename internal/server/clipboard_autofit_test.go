package server

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestAutofitSamplingIsStrictlyBoundedAndDistributed(t *testing.T) {
	for _, size := range []int{0, 1, 10, 511, 512, 513, 777, 10000, 1048576} {
		rows := autofitSampleRows(size)
		want := size
		if want > 512 {
			want = 512
		}
		if len(rows) != want {
			t.Fatalf("size %d: got %d samples; want %d", size, len(rows), want)
		}
		for i, pos := range rows {
			if pos < 0 || pos >= size || i > 0 && rows[i-1] >= pos {
				t.Fatalf("size %d: indices not strictly increasing: %v", size, rows)
			}
		}
		if size > 0 && (rows[0] != 0 || rows[len(rows)-1] != size-1) {
			t.Errorf("size %d: first/last row not sampled", size)
		}
		if size > 512 && (rows[31] != 31 || rows[len(rows)-32] != size-32) {
			t.Errorf("size %d: head/tail sample missing", size)
		}
	}
}

func TestFilteredClipboardMatchesVisualSortOrder(t *testing.T) {
	idx := &workbook.SheetIndex{Rows: []workbook.RowOffset{
		{Number: 1}, {Number: 2}, {Number: 3}, {Number: 5}, {Number: 8},
	}}
	// First row is a fixed header; remaining indexes reflect a desc sort.
	result := &FilterResult{Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 0, 3, 1, 4, 2)}
	numbers, positions, err := selectClipboardRows(idx, result, 5, 8, 1, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{5, 2, 8}; !reflect.DeepEqual(numbers, want) {
		t.Fatalf("visual order = %v, want %v", numbers, want)
	}
	if want := []int{3, 1, 4}; !reflect.DeepEqual(positions, want) {
		t.Fatalf("source indexes = %v, want %v", positions, want)
	}
	if _, _, err := selectClipboardRows(idx, result, 1, 8, 1, 5, 1); err == nil {
		t.Error("expected range overflow to be rejected")
	}
	plain, off, err := selectClipboardRows(idx, nil, 2, 5, 0, 0, 0)
	if err != nil || off != nil || !reflect.DeepEqual(plain, []int{2, 3, 4, 5}) {
		t.Fatalf("unfiltered selection wrong: %v / %v / %v", plain, off, err)
	}
	if escapedClipboardCell("a\tb\n\"q\"") != "\"a\tb\n\"\"q\"\"\"" {
		t.Error("clipboard must quote literal tabs, newline and double quotes")
	}
}

func writeClipboardTestWorkbook(t *testing.T, file string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:B4"/><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>header</t></is></c><c r="B1"><v>1</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>alpha</t></is></c><c r="B2"><v>2</v></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>beta</t></is></c><c r="B3"><f>B2+1</f><v>3</v></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>gamma</t></is></c><c r="B4"><v>4</v></c></row></sheetData></worksheet>`,
	}
	for name, content := range entries {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClipboardJSONEndpointPreservesFilteredVisualOrder(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "clipboard.xlsx")
	writeClipboardTestWorkbook(t, file)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	meta, _ := wb.FindSheet("Data")
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Rows) != 4 {
		t.Fatalf("test workbook indexed %d rows", len(idx.Rows))
	}
	app.mu.Lock()
	app.filterResults["sort-test"] = &FilterResult{ID: "sort-test", Sheet: "Data", SheetPath: meta.Path,
		Rows: rowOrderFixture(t, idx.RowCount(), 0, 3, 1, 2), Sorted: true, Created: time.Now()}
	app.mu.Unlock()
	call := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/range?"+query, nil)
		req.RemoteAddr = "127.0.0.1:9999"
		req.Header.Set("X-ExcelLens-Token", app.token)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, req)
		return response
	}
	args := "sheet=Data&start=A4&end=B3&filterId=sort-test&visualStart=1&visualEnd=3&excludeThroughRow=1"
	resp := call(args + "&format=json")
	if resp.Code != http.StatusOK {
		t.Fatalf("JSON status %d: %s", resp.Code, resp.Body.String())
	}
	var payload clipboardPayload
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var numbers []int
	var firstCells []string
	for _, row := range payload.Rows {
		numbers = append(numbers, row.Row)
		for _, cell := range row.Cells {
			if cell.Col == 1 {
				firstCells = append(firstCells, cell.Display)
			}
		}
	}
	if !reflect.DeepEqual(numbers, []int{4, 2, 3}) || !reflect.DeepEqual(firstCells, []string{"gamma", "alpha", "beta"}) {
		t.Fatalf("clipboard visual order mismatch rows=%v values=%v", numbers, firstCells)
	}
	resp = call(args)
	if resp.Code != http.StatusOK || strings.TrimSpace(resp.Body.String()) != "gamma\t4\nalpha\t2\nbeta\t3" {
		t.Fatalf("clipboard TSV wrong %d: %q", resp.Code, resp.Body.String())
	}
	if bad := call(args + "&format=xml"); bad.Code != http.StatusBadRequest {
		t.Errorf("unsupported format status=%d", bad.Code)
	}
	if bad := call("sheet=Data&start=A1&end=B400"); bad.Code != http.StatusBadRequest {
		t.Errorf("large region status=%d", bad.Code)
	}
}
