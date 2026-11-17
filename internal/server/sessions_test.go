package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sessionCall(t *testing.T, a *App, session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-ExcelLens-Token", a.token)
	if session != "" {
		req.Header.Set("X-ExcelLens-Session", session)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	result := httptest.NewRecorder()
	a.Handler().ServeHTTP(result, req)
	return result
}

func TestBrowserTabsKeepIndependentWorkbooksAndCaches(t *testing.T) {
	isolateTestStorage(t)
	fileA := filepath.Join(t.TempDir(), "audit_a.xlsx")
	fileB := filepath.Join(t.TempDir(), "audit_b.xlsx")
	writeMultiSheetWorkbook(t, fileA)
	writeAnalysisWorkbook(t, fileB)
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sessionA, err := root.OpenPathForLaunch(fileA)
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := root.OpenPathForLaunch(fileB)
	if err != nil {
		t.Fatal(err)
	}
	if sessionA == sessionB {
		t.Fatal("two tabs received one session")
	}
	type status struct {
		Opened   bool   `json:"opened"`
		FileName string `json:"fileName"`
		Sheets   []struct {
			Name string `json:"name"`
		} `json:"sheets"`
	}
	checkStatus := func(session, filename, sheet string) {
		t.Helper()
		result := sessionCall(t, root, session, "GET", "/api/status", "")
		if result.Code != 200 {
			t.Fatalf("status %s: %d %s", session, result.Code, result.Body.String())
		}
		var data status
		if err := json.Unmarshal(result.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if !data.Opened || data.FileName != filename || len(data.Sheets) == 0 || data.Sheets[0].Name != sheet {
			t.Fatalf("incorrect session %s status: %+v", session, data)
		}
	}
	checkStatus(sessionA, "audit_a.xlsx", "First")
	checkStatus(sessionB, "audit_b.xlsx", "Invest")
	for _, session := range []string{"", randomID()} {
		result := sessionCall(t, root, session, "GET", "/api/status", "")
		var home status
		if err := json.Unmarshal(result.Body.Bytes(), &home); err != nil {
			t.Fatal(err)
		}
		if home.Opened {
			t.Fatalf("new home tab auto-opened another tab's workbook (%s)", session)
		}
	}
	// Independent indexes and work continue after opening another workbook.
	start := sessionCall(t, root, sessionA, "POST", "/api/index/start", `{"sheet":"First"}`)
	if start.Code != 200 {
		t.Fatalf("first tab index failed after second tab launch: %s", start.Body.String())
	}
	checkStatus(sessionA, "audit_a.xlsx", "First")
	checkStatus(sessionB, "audit_b.xlsx", "Invest")
	// Clearing unused caches from the tray never invalidates active tabs.
	oldCache := filepath.Join(CacheDir(), "history-unused")
	if err := os.MkdirAll(oldCache, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sessionA, sessionB} {
		child, _ := root.sessionFor(s)
		if _, err := os.Stat(child.app.currentWorkbook().CacheDir); err != nil {
			t.Fatalf("active cache removed: %v", err)
		}
	}
	if _, err := os.Stat(oldCache); !os.IsNotExist(err) {
		t.Fatalf("unused cache not cleared: %v", err)
	}
	recent := sessionCall(t, root, sessionA, "GET", "/api/recent", "")
	var recentData struct {
		Items []recentEntry `json:"items"`
	}
	if err := json.Unmarshal(recent.Body.Bytes(), &recentData); err != nil {
		t.Fatal(err)
	}
	if len(recentData.Items) < 2 {
		t.Fatalf("recent list not globally shared: %s", recent.Body.String())
	}
}

func TestMultipleTabsSameWorkbookAnalysisRevisionIsAtomic(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "shared_analysis.xlsx")
	writeAnalysisWorkbook(t, path)
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sid1, err := root.OpenPathForLaunch(path)
	if err != nil {
		t.Fatal(err)
	}
	sid2, err := root.OpenPathForLaunch(path)
	if err != nil {
		t.Fatal(err)
	}
	created := sessionCall(t, root, sid1, "POST", "/api/analysis/sheet/create", `{"name":"双标签共享分析","columns":["标记"]}`)
	if created.Code != 200 {
		t.Fatalf("create: %s", created.Body.String())
	}
	var sheet struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &sheet); err != nil {
		t.Fatal(err)
	}
	workspace := sessionCall(t, root, sid2, "GET", "/api/analysis/workspace", "")
	if workspace.Code != 200 || !strings.Contains(workspace.Body.String(), sheet.ID) {
		t.Fatalf("second tab did not see shared saved sheet: %s", workspace.Body.String())
	}
	// Both clients read revision zero; only one writer may commit.
	results := make([]int, 2)
	var wg sync.WaitGroup
	for i, sid := range []string{sid1, sid2} {
		wg.Add(1)
		go func(i int, sid string) {
			defer wg.Done()
			body := fmt.Sprintf(`{"id":%q,"expectedRevision":0,"changes":[{"row":0,"col":0,"value":%q}]}`, sheet.ID, fmt.Sprintf("tab%d", i+1))
			results[i] = sessionCall(t, root, sid, http.MethodPost, "/api/analysis/sheet/batch", body).Code
		}(i, sid)
	}
	wg.Wait()
	if !((results[0] == 200 && results[1] == 409) || (results[1] == 200 && results[0] == 409)) {
		t.Fatalf("concurrent edits overwrote one another: %v", results)
	}
	cell := sessionCall(t, root, sid2, "GET", "/api/analysis/sheet?id="+sheet.ID, "")
	if cell.Code != 200 {
		t.Fatalf("sheet read: %s", cell.Body.String())
	}
	var current struct {
		Revision int64      `json:"revision"`
		Rows     [][]string `json:"rows"`
	}
	if err := json.Unmarshal(cell.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.Revision != 1 || len(current.Rows) != 1 || len(current.Rows[0]) != 1 || (current.Rows[0][0] != "tab1" && current.Rows[0][0] != "tab2") {
		t.Fatalf("invalid shared sheet state: %+v", current)
	}
}

func TestInvalidBrowserSessionDoesNotReachAnotherWorkbook(t *testing.T) {
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, sid := range []string{"../other", "tiny", "aaaaaaaaaaaaaaaaaaa!", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		response := sessionCall(t, root, sid, "GET", "/api/status", "")
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("invalid session %q response %d", sid, response.Code)
		}
	}
}
