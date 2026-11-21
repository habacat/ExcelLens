package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayFontPrefsPreserveWidthAndSurviveReload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "layout.json")
	original := workbookLayouts{Version: 1, Sheets: map[string]sheetLayout{
		"表1": {Rows: map[string]int{"3": 42}, Cols: map[string]int{"5": 210}, Font: "仿宋"},
	}}
	if err := writeLayouts(file, original); err != nil {
		t.Fatal(err)
	}
	loaded := readLayouts(file)
	value := loaded.Sheets["表1"]
	if value.Font != "仿宋" || value.Rows["3"] != 42 || value.Cols["5"] != 210 {
		t.Fatalf("bad layout: %+v", value)
	}
}
func TestDisplayFontPrefsRejectUnknownName(t *testing.T) {
	for _, name := range []string{"", "宋体", "楷体", "仿宋", "微软雅黑", "等线", "黑体", "Calibri"} {
		if !validFontOverride(name) {
			t.Errorf("known font %s rejected", name)
		}
	}
	for _, name := range []string{"Unknown", "<script>", "超长非常用字体"} {
		if validFontOverride(name) {
			t.Errorf("unknown font %s accepted", name)
		}
	}
}

func TestFontOverrideHTTPPersistsIndependentlyPerSheet(t *testing.T) {
	isolateTestStorage(t)
	book := filepath.Join(t.TempDir(), "font.xlsx")
	writeMultiSheetWorkbook(t, book)
	app := prepareFeatureApp(t, book)
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"font","font":"宋体"}`)
	featureCall(t, app, "POST", "/api/layout", `{"sheet":"First","kind":"col","index":3,"size":223}`)
	app.Close()
	app = prepareFeatureApp(t, book)
	defer app.Close()
	var got sheetLayout
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=First", "").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Font != "宋体" || got.Cols["3"] != 223 {
		t.Fatalf("lost font or widths: %+v", got)
	}
	var other sheetLayout
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/layout?sheet=Second", "").Body.Bytes(), &other); err != nil {
		t.Fatal(err)
	}
	if other.Font != "" {
		t.Fatalf("font leaked between sheets: %+v", other)
	}
	invalid := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/layout", strings.NewReader(`{"sheet":"First","kind":"font","font":"not-a-platform-font"}`))
	request = localTestConnection(request)
	request.Header.Set("X-ExcelLens-Token", app.token)
	app.Handler().ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid font accepted: %d", invalid.Code)
	}
}
