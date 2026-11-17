package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSheetLayoutPersistsAndIsolated(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "size.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"row","index":2,"size":42}`)
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"col","index":3,"size":210}`)
	var read sheetLayout
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Rows["2"] != 42 || read.Cols["3"] != 210 {
		t.Fatalf("unexpected layout %+v", read)
	}
	app.Close()
	app = prepareFeatureApp(t, file)
	defer app.Close()
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Rows["2"] != 42 || read.Cols["3"] != 210 {
		t.Fatal("row/col layout did not persist")
	}
	read = sheetLayout{}
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=Second", "").Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Rows) != 0 || len(read.Cols) != 0 {
		t.Fatal("sheet layout leaked")
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"row","index":2,"size":0}`)
	read = sheetLayout{}
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Rows) != 0 {
		t.Fatal("reset row did not delete override")
	}
	req := httptest.NewRequest("POST", "/api/layout", strings.NewReader(`{"sheet":"First","kind":"col","index":16385,"size":12}`))
	req.RemoteAddr = "127.0.0.1:2222"
	req.Header.Set("X-ExcelLens-Token", app.token)
	out := httptest.NewRecorder()
	app.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("accepted invalid layout: %d", out.Code)
	}
	if _, err := os.Stat(layoutFile(app.currentWorkbook())); err != nil {
		t.Fatal(err)
	}
	// Rebuilding disposable indexes must not delete view preferences.
	if err := os.RemoveAll(filepath.Dir(app.currentWorkbook().CacheDir)); err != nil {
		t.Fatal(err)
	}
	read = sheetLayout{}
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Cols["3"] != 210 {
		t.Fatal("layout vanished with search cache")
	}
}

func TestSourceDimensionsStaySeparateFromUserOverrides(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "source.xls")
	if err := os.WriteFile(file, []byte(`<html><table style="width:300px"><col width="100"><col width="200"><tr height="18"><td>账号</td><td>000123</td></tr></table></html>`), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	meta := app.currentWorkbook().Sheets[0]
	if _, err = app.currentWorkbook().BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	sheet := meta.Name
	read := func() sheetLayout {
		var l sheetLayout
		if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet="+sheet, "").Body.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	original := read()
	if original.SourceGridLines == nil || *original.SourceGridLines || len(original.SourceRows) != 1 || original.SourceRows[0].Size != 18 || original.SourceCols[2] != 200 {
		t.Fatal(original)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"source","kind":"row","index":1,"size":44}`)
	changed := read()
	if changed.Rows["1"] != 44 || changed.SourceRows[0].Size != 18 {
		t.Fatal(changed)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"source","kind":"row","index":1,"size":0}`)
	reset := read()
	if len(reset.Rows) != 0 || reset.SourceRows[0].Size != 18 {
		t.Fatal(reset)
	}
	saved, err := os.ReadFile(layoutFile(app.currentWorkbook()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "sourceRows") || strings.Contains(string(saved), "sourceCols") {
		t.Fatal("source metadata leaked into user settings")
	}
}

func TestColumnVisibilityModeAndOverridesPersist(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "columns.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	// Test XML metadata from the source sheet, independent of UI overrides.
	wb := app.currentWorkbook()
	meta, ok := wb.FindSheet("First")
	if !ok {
		t.Fatal("sheet missing")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		t.Fatal("index missing")
	}
	original, err := os.ReadFile(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(original), "<sheetData>", `<cols><col min="2" max="4" hidden="1"/><col min="3" max="3" hidden="0"/></cols><sheetData>`, 1)
	if modified == string(original) {
		t.Skip("fixture sheet uses namespaced sheetData")
	}
	if err := os.WriteFile(idx.XMLPath, []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	read := func() sheetLayout {
		var value sheetLayout
		if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	got := read()
	if len(got.SourceHidden) != 2 || got.SourceHidden[0].Min != 2 || got.SourceHidden[1].Min != 4 {
		t.Fatalf("source metadata %+v", got.SourceHidden)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"column","index":2,"hidden":false}`)
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"column","index":3,"hidden":true}`)
	got = read()
	if got.ColumnVisibility["2"] || !got.ColumnVisibility["3"] {
		t.Fatalf("explicit overrides %+v", got)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"hide-all"}`)
	got = read()
	if got.ColumnMode != "hide-all" || len(got.ColumnVisibility) != 0 {
		t.Fatalf("hide all failed %+v", got)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"show-all"}`)
	got = read()
	if got.ColumnMode != "show-all" || len(got.ColumnVisibility) != 0 {
		t.Fatalf("show all failed %+v", got)
	}
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"visibility","mode":"original"}`)
	got = read()
	if got.ColumnMode != "original" || len(got.ColumnVisibility) != 0 || len(got.SourceHidden) != 2 {
		t.Fatalf("restore failed %+v", got)
	}
	app.Close()
	reopened := prepareFeatureApp(t, file)
	defer reopened.Close()
	var reopenedLayout sheetLayout
	if err := json.Unmarshal(featureCall(t, reopened, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &reopenedLayout); err != nil {
		t.Fatal(err)
	}
	if reopenedLayout.ColumnMode != "original" {
		t.Fatal("visibility mode did not persist")
	}
}

func TestColumnVisibilityRejectsBadCommands(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "columns.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	for _, body := range []string{
		`{"sheet":"First","kind":"visibility","mode":"column","index":0,"hidden":true}`,
		`{"sheet":"First","kind":"visibility","mode":"column","index":16385,"hidden":true}`,
		`{"sheet":"First","kind":"visibility","mode":"bogus"}`,
		`{"sheet":"First","kind":"visibility","mode":""}`,
	} {
		req := httptest.NewRequest("POST", "/api/layout", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:2222"
		req.Header.Set("X-ExcelLens-Token", app.token)
		resp := httptest.NewRecorder()
		app.Handler().ServeHTTP(resp, req)
		if resp.Code != 400 {
			t.Errorf("input %s code=%d: %s", body, resp.Code, resp.Body.String())
		}
	}
}
