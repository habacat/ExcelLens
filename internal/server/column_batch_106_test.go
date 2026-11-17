package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestColumnRangeBatchHideShowAndPersists(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "columns.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	read := func() sheetLayout {
		var value sheetLayout
		if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"range","index":2,"end":4,"hidden":true}`)
	got := read()
	for i := 2; i <= 4; i++ {
		if !got.ColumnVisibility[string('0'+byte(i))] {
			t.Fatalf("failed to hide %d: %+v", i, got.ColumnVisibility)
		}
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"range","index":1,"end":5,"hidden":false}`)
	got = read()
	for i := 1; i <= 5; i++ {
		if got.ColumnVisibility[string('0'+byte(i))] {
			t.Fatalf("failed to show %d: %+v", i, got.ColumnVisibility)
		}
	}
	app.Close()
	reopened := prepareFeatureApp(t, file)
	defer reopened.Close()
	var layout sheetLayout
	if err := json.Unmarshal(featureCall(t, reopened, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &layout); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"1", "2", "3", "4", "5"} {
		v, ok := layout.ColumnVisibility[key]
		if !ok || v {
			t.Fatalf("range not persisted: %s=%v,%v", key, v, ok)
		}
	}
}
func TestColumnRangeInvalidRejectedAtomically(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "columns.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	for _, body := range []string{
		`{"sheet":"First","kind":"visibility","mode":"range","index":0,"end":2,"hidden":true}`,
		`{"sheet":"First","kind":"visibility","mode":"range","index":2,"end":1,"hidden":true}`,
		`{"sheet":"First","kind":"visibility","mode":"range","index":1,"end":16385,"hidden":true}`,
		`{"sheet":"First","kind":"visibility","mode":"range","index":5,"hidden":true}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/layout", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:2222"
		req.Header.Set("X-ExcelLens-Token", app.token)
		out := httptest.NewRecorder()
		app.Handler().ServeHTTP(out, req)
		if out.Code != 400 {
			t.Errorf("accepted invalid batch %s: %d", body, out.Code)
		}
	}
	var layout sheetLayout
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.ColumnVisibility) != 0 {
		t.Fatalf("invalid inputs mutated layout: %+v", layout.ColumnVisibility)
	}
}
