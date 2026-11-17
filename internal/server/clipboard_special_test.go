package server

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestClipboardSpecialHeaderUsesSameSourceAndKeepsSortedRowsTypesAndFormulas(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "clipboard.xlsx")
	writeClipboardTestWorkbook(t, file)
	before, _ := os.ReadFile(file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.filterResults["clipboard-order"] = &FilterResult{ID: "clipboard-order", Sheet: "Data", SheetPath: wb.Sheets[0].Path, Rows: rowOrderFixture(t, idx.RowCount(), 0, 3, 2, 1), Sorted: true, Created: time.Now()}
	params := url.Values{"sheet": {"Data"}, "start": {"A4"}, "end": {"B3"}, "format": {"json"}, "headerRow": {"1"}, "filterId": {"clipboard-order"}, "visualStart": {"1"}, "visualEnd": {"2"}, "excludeThroughRow": {"1"}, "richText": {"0"}, "conditional": {"1"}, "workbookKey": {wb.Fingerprint}, "book": {sourceBookID(wb)}}
	payload := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""))
	if payload.Header == nil || payload.Header.Row != 1 || payload.Header.Cells[0].Raw != "header" || payload.Header.Cells[0].Type != "inlineStr" || len(payload.Rows) != 2 || payload.Rows[0].Row != 4 || payload.Rows[1].Row != 3 {
		t.Fatal("header or visual row ordering was changed", payload)
	}
	formula := payload.Rows[1].Cells[1]
	if formula.Formula != "=B2+1" || formula.Raw != "3" || formula.CacheMissing || !formula.HasFormula {
		t.Fatal("copy values replaced exact formula metadata", formula)
	}
	for _, key := range []string{"workbookKey", "book"} {
		bad := params.Clone()
		bad.Set(key, "wrong-source")
		if out := analysisRequest(t, a, "GET", "/api/range?"+bad.Encode(), ""); out.Code != 409 {
			t.Fatal("source identity mismatch accepted", out.Code, out.Body.String())
		}
	}
	for _, header := range []string{"", "0", "-1", "1.5", "NaN", strconv.Itoa(idx.MaxRow + formats.ExcelRows)} {
		bad := params.Clone()
		bad.Set("headerRow", header)
		if out := analysisRequest(t, a, "GET", "/api/range?"+bad.Encode(), ""); out.Code != 400 {
			t.Fatal("invalid header accepted", header, out.Code, out.Body.String())
		}
	}
	params.Del("filterId")
	params.Del("visualStart")
	params.Del("visualEnd")
	params.Del("excludeThroughRow")
	for _, region := range [][2]string{{"A2", "A251"}, {"A2", "AX101"}} {
		params.Set("start", region[0])
		params.Set("end", region[1])
		if out := analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""); out.Code != 400 {
			t.Fatal("extra header bypassed clipboard capacity", region, out.Code, out.Body.String())
		}
	}
	params.Set("start", "A1")
	params.Set("end", "AX100")
	boundary := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""))
	if len(boundary.Rows) != 100 || boundary.Header.Row != 1 {
		t.Fatal("already-selected header was counted twice", len(boundary.Rows))
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("clipboard read modified its source")
	}
}
