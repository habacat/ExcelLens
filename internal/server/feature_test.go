package server

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func featureCall(t *testing.T, a *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", a.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	a.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("%s %s: HTTP %d %s", method, path, res.Code, res.Body.String())
	}
	return res
}

func prepareFeatureApp(t *testing.T, file string) *App {
	t.Helper()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta, ok := wb.FindSheet("First")
	if !ok {
		t.Fatal("missing First sheet")
	}
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFreezeSettingsPersistAcrossAppReopen(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	var initial struct {
		Rows   int    `json:"rows"`
		Cols   int    `json:"cols"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(featureCall(t, a, "GET", "/api/view?sheet=First", "").Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Rows != 0 || initial.Cols != 0 {
		t.Fatalf("unexpected initial freeze: %+v", initial)
	}
	featureCall(t, a, "POST", "/api/view", `{"sheet":"First","rows":2,"cols":1}`)
	a.Close()
	b := prepareFeatureApp(t, file)
	defer b.Close()
	var restored struct {
		Rows   int    `json:"rows"`
		Cols   int    `json:"cols"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/view?sheet=First", "").Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Rows != 2 || restored.Cols != 1 || restored.Source != "saved" {
		t.Fatalf("freeze settings not persisted: %+v", restored)
	}
	var untouched struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/view?sheet=Second", "").Body.Bytes(), &untouched); err != nil {
		t.Fatal(err)
	}
	if untouched.Rows != 0 || untouched.Cols != 0 {
		t.Fatalf("freeze settings leaked across sheets: %+v", untouched)
	}
	req := httptest.NewRequest("POST", "/api/view", strings.NewReader(`{"sheet":"First","rows":31,"cols":0}`))
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", b.token)
	res := httptest.NewRecorder()
	b.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid freeze accepted: %d", res.Code)
	}
}

func TestOriginalWorkbookFreezePaneIsImported(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "native.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane xSplit="2" ySplit="3" topLeftCell="C4" state="frozen"/></sheetView></sheetViews><sheetData><row r="1"><c r="A1"><v>18</v></c></row></sheetData></worksheet>`,
	}
	for name, data := range entries {
		z, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	app := prepareFeatureApp(t, file)
	defer app.Close()
	var pane struct {
		Rows   int    `json:"rows"`
		Cols   int    `json:"cols"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(featureCall(t, app, "GET", "/api/view?sheet=First", "").Body.Bytes(), &pane); err != nil {
		t.Fatal(err)
	}
	if pane.Rows != 3 || pane.Cols != 2 || pane.Source != "original" {
		t.Fatalf("did not import OOXML pane: %+v", pane)
	}
}

func TestSearchResultsDiskCacheAcrossReopen(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "multi.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	req := `{"sheet":"First","query":"A1+1","mode":"formula","scope":"all"}`
	res := featureCall(t, a, "POST", "/api/search/start", req)
	var start struct {
		JobID  string `json:"jobId"`
		Cached bool   `json:"cached"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if start.JobID == "" || start.Cached {
		t.Fatalf("unexpected first search: %+v", start)
	}
	a.mu.RLock()
	job := a.searchJobs[start.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("search job missing")
	}
	select {
	case <-job.done:
	case <-time.After(8 * time.Second):
		t.Fatal("search did not finish")
	}
	var found struct {
		Status  string `json:"status"`
		Matches []struct {
			Sheet string `json:"sheet"`
			Ref   string `json:"ref"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(featureCall(t, a, "GET", "/api/search/status?id="+start.JobID, "").Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if found.Status != "done" || len(found.Matches) != 2 {
		t.Fatalf("unexpected search: %+v", found)
	}
	if _, err := os.Stat(searchCachePath(a.currentWorkbook(), "First", "A1+1", "formula", "all")); err != nil {
		t.Fatalf("cache not persisted: %v", err)
	}
	a.Close()
	b := prepareFeatureApp(t, file)
	defer b.Close()
	again := featureCall(t, b, "POST", "/api/search/start", req)
	var reused struct {
		JobID  string `json:"jobId"`
		Cached bool   `json:"cached"`
	}
	if err := json.Unmarshal(again.Body.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.JobID == "" || !reused.Cached {
		t.Fatalf("search was rescanned after reopen: %+v", reused)
	}
	var cachedResults struct {
		Status  string `json:"status"`
		Matches []struct {
			Sheet string `json:"sheet"`
			Ref   string `json:"ref"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/search/status?id="+reused.JobID, "").Body.Bytes(), &cachedResults); err != nil {
		t.Fatal(err)
	}
	if cachedResults.Status != "done" || len(cachedResults.Matches) != 2 {
		t.Fatalf("cached results not retained: %+v", cachedResults)
	}
}

func TestRangeCopyPlainValuesAndFormulas(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "data.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	value := featureCall(t, a, "GET", "/api/range?sheet=First&start=A1&end=C2", "")
	if got, want := value.Body.String(), "1\t2\t\n\t\t"; got != want {
		t.Fatalf("value paste = %q; expected %q", got, want)
	}
	formula := featureCall(t, a, "GET", "/api/range?sheet=First&start=A1&end=B1&mode=formula", "")
	if got, want := formula.Body.String(), "1\t=A1+1"; got != want {
		t.Fatalf("formula paste = %q; expected %q", got, want)
	}
}

func TestFreezeUIAndSearchResultsAreAvailableAsStaticAssets(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string][]string{
		"/assets/freeze.js": {"class FrozenPanes", "followScroll", "fetchPart"},
		"/assets/app.js":    {"searchSession", "searchCache", "goSearchResult", "openFreezeDialog", "applyFreeze", "copySelection", "stepSearch", "recordVisit"},
		"/assets/style.css": {".freeze-top", ".freeze-left", ".search-dialog[open]", ".grid-cell.range-cell"},
	}
	for path, required := range assets {
		r := featureCall(t, app, "GET", path, "")
		for _, part := range required {
			if !strings.Contains(r.Body.String(), part) {
				t.Errorf("asset %s missing %s", path, part)
			}
		}
	}
}
