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

func TestJSONErrorSendsMediaTypeBeforeStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	methodNotAllowed(rec)
	response := rec.Result()
	defer response.Body.Close()
	var body struct{ Error string }
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
		json.NewDecoder(response.Body).Decode(&body) != nil || body.Error == "" {
		t.Fatalf("error response: %d %v %+v", response.StatusCode, response.Header, body)
	}
}

func TestInitialPageClearlyExplainsHowToStart(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = localTestConnection(req)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
	body := res.Body.String()
	for _, required := range []string{
		"浏览数据", "选择数据文件", "连接在线数据库", `data-menu="workbookOpenMenu"`,
		"全屏", "追踪引用", "追踪从属", "筛选条件", "冻结窗格",
		"gridCanvas", "columnHeaders", "rowsLayer", "cellTooltip", "freezeTop", "freezeLeft",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("initial page missing %q", required)
		}
	}
	if strings.Contains(body, "上一屏") || strings.Contains(body, "下一屏") || strings.Contains(body, "前面列") || strings.Contains(body, "后面列") {
		t.Error("spreadsheet UI must use continuous scrolling, not page navigation controls")
	}
	if strings.Contains(body, "已就绪") {
		t.Error("initial page must not claim ready before a workbook is opened")
	}
}

func TestGridAssetsExposeNativeSpreadsheetInteractions(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for path, required := range map[string][]string{
		"/assets/app.js": {
			"ArrowRight", "ArrowDown", "requestFullscreen", "parseFormulaReferences",
			"tracePrecedents", "applyFilter", "showTooltip", "selectInitialCell",
		},
		"/assets/style.css": {
			".active-row", ".grid-cell.selected",
			"scrollbar-width: thin", ".column-filter", ".grid-scroll",
		},
		// The row and column highlight is drawn by the crosshair rules.
		"/assets/themes.css": {".active-row", ".active-column"},
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = localTestConnection(req)
		res := httptest.NewRecorder()
		app.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, res.Code)
		}
		for _, item := range required {
			if !strings.Contains(res.Body.String(), item) {
				t.Errorf("%s missing %q", path, item)
			}
		}
	}
}

func TestCellEndpointReturnsExactFormulaAndBlankCell(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "multi.xlsx")
	writeMultiSheetWorkbook(t, file)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	meta, ok := wb.FindSheet("First")
	if !ok {
		t.Fatal("First sheet not found")
	}
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	call := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/cell?sheet=First&ref="+ref, nil)
		req = localTestConnection(req)
		req.Header.Set("X-ExcelLens-Token", app.token)
		res := httptest.NewRecorder()
		app.Handler().ServeHTTP(res, req)
		return res
	}
	var formula struct {
		Cell struct {
			Ref        string `json:"ref"`
			Formula    string `json:"formula"`
			HasFormula bool   `json:"hasFormula"`
		} `json:"cell"`
	}
	response := call("B1")
	if response.Code != http.StatusOK {
		t.Fatalf("cell status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &formula); err != nil {
		t.Fatal(err)
	}
	if formula.Cell.Ref != "B1" || !formula.Cell.HasFormula || formula.Cell.Formula != "=A1+1" {
		t.Fatalf("unexpected formula cell: %+v", formula.Cell)
	}
	var blank struct {
		Cell struct {
			Ref        string `json:"ref"`
			HasFormula bool   `json:"hasFormula"`
			Display    string `json:"display"`
		} `json:"cell"`
	}
	response = call("C1")
	if response.Code != http.StatusOK {
		t.Fatalf("blank cell status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &blank); err != nil {
		t.Fatal(err)
	}
	if blank.Cell.Ref != "C1" || blank.Cell.HasFormula || blank.Cell.Display != "" {
		t.Fatalf("unexpected blank cell: %+v", blank.Cell)
	}
}

func TestAPITokenIsRequired(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req = localTestConnection(req)
		if token != "" {
			req.Header.Set("X-ExcelLens-Token", token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if got := request("").Code; got != http.StatusForbidden {
		t.Fatalf("without token status=%d", got)
	}
	if got := request(app.token).Code; got != http.StatusOK {
		t.Fatalf("with token status=%d", got)
	}
}

func TestNonLoopbackRemoteIsRejected(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = localTestConnection(req)
	req.RemoteAddr = "192.0.2.1:12345"
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d", res.Code)
	}
}

func TestWorkbookWideSearchFindsFormulaOnMultipleSheets(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "multi.xlsx")
	writeMultiSheetWorkbook(t, file)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	call := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req = localTestConnection(req)
		req.Header.Set("X-ExcelLens-Token", app.token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	start := call(http.MethodPost, "/api/search/start", `{"sheet":"First","query":"A1+1","mode":"formula","scope":"all"}`)
	if start.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	var startData struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &startData); err != nil {
		t.Fatal(err)
	}
	if startData.JobID == "" {
		t.Fatal("search job id missing")
	}
	var result struct {
		Status  string `json:"status"`
		Matches []struct {
			Sheet string `json:"sheet"`
			Ref   string `json:"ref"`
		} `json:"matches"`
		Error string `json:"error"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := call(http.MethodGet, "/api/search/status?id="+startData.JobID, "")
		if response.Code != http.StatusOK {
			t.Fatalf("status code=%d body=%s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status == "done" || result.Status == "error" || result.Status == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.Status != "done" {
		t.Fatalf("search status=%s error=%s", result.Status, result.Error)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("want 2 matches, got %+v", result.Matches)
	}
	if result.Matches[0].Sheet != "First" || result.Matches[1].Sheet != "Second" {
		t.Fatalf("unexpected sheet order: %+v", result.Matches)
	}
}

func TestWorkbookWideReferenceSearchUsesTargetSheetContext(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "multi.xlsx")
	writeMultiSheetWorkbook(t, file)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/search/start", strings.NewReader(`{"sheet":"First","query":"'First'!A1","mode":"reference","scope":"all"}`))
	request = localTestConnection(request)
	request.Header.Set("X-ExcelLens-Token", app.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var start struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var result struct {
		Status  string `json:"status"`
		Matches []struct {
			Sheet string `json:"sheet"`
			Ref   string `json:"ref"`
		} `json:"matches"`
		Error string `json:"error"`
	}
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/search/status?id="+start.JobID, nil)
		req = localTestConnection(req)
		req.Header.Set("X-ExcelLens-Token", app.token)
		res := httptest.NewRecorder()
		app.Handler().ServeHTTP(res, req)
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status == "done" || result.Status == "error" || result.Status == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result.Status != "done" {
		t.Fatalf("search status=%s error=%s", result.Status, result.Error)
	}
	if len(result.Matches) != 2 || result.Matches[0].Sheet != "First" || result.Matches[0].Ref != "B1" || result.Matches[1].Sheet != "Second" || result.Matches[1].Ref != "C1" {
		t.Fatalf("reference search matched wrong dependents: %+v", result.Matches)
	}
}

func writeMultiSheetWorkbook(t *testing.T, file string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView activeTab="0"/></bookViews><sheets><sheet name="First" sheetId="1" r:id="rId1"/><sheet name="Second" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:B1"/><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="B1"><f>A1+1</f><v>2</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:C1"/><sheetData><row r="1"><c r="A1"><v>2</v></c><c r="B1"><f>A1+1</f><v>3</v></c><c r="C1"><f>'First'!$A$1+1</f><v>2</v></c></row></sheetData></worksheet>`,
	}
	for name, content := range entries {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
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

func TestPersistUploadedDiskFileWithoutSecondCopy(t *testing.T) {
	source, err := os.CreateTemp(t.TempDir(), "multipart-*.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("local-test-payload")
	if _, err := source.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := source.Sync(); err != nil {
		t.Fatal(err)
	}
	path, err := persistUploadedFile(source, ".xlsx", int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: %q", got)
	}
}

func TestStartupFileErrorIsVisibleToUser(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	_ = app.OpenPath(filepath.Join(t.TempDir(), "missing.xlsx"))
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", app.token)
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	var status struct {
		Opened bool   `json:"opened"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Opened || status.Error == "" {
		t.Fatalf("startup error was not exposed: %+v", status)
	}
}
