package server

import (
	"context"
	"encoding/json"
	"excellens/internal/browserlease"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"excellens/internal/workbook"
)

// Look up the tab's app in tests which manage its lifetime directly.
func (a *App) sessionFor(id string) (*browserSession, error) {
	s, err := a.acquireSession(id, "", sessionAccess{})
	if err == nil {
		a.releaseSession(s)
	}
	return s, err
}

func sessionCall(t *testing.T, a *App, session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = localTestConnection(req)
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

func TestNativeLaunchWaitsForItsOwnTabAndOpensOnce(t *testing.T) {
	isolateTestStorage(t)
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file := filepath.Join(t.TempDir(), "deferred.xlsx")
	id, err := root.PreparePathForLaunch(file)
	if err != nil {
		t.Fatal(err)
	}
	session, err := root.sessionFor(id)
	if err != nil {
		t.Fatal(err)
	}
	other := sessionCall(t, root, randomID(), "GET", "/api/status", "")
	if other.Code != 200 || !strings.Contains(other.Body.String(), `"opened":false`) || session.app.currentWorkbook() != nil {
		t.Fatal("an idle tab consumed the native launch")
	}
	writeMultiSheetWorkbook(t, file)
	first := sessionCall(t, root, id, "GET", "/api/status", "")
	if first.Code != 200 || !strings.Contains(first.Body.String(), "deferred.xlsx") {
		t.Fatal(first.Body.String())
	}
	book := session.app.currentWorkbook()
	sessionCall(t, root, id, "GET", "/api/status", "")
	if book == nil || session.app.currentWorkbook() != book {
		t.Fatal("reload reopened the native source")
	}
	missing, err := root.PreparePathForLaunch(filepath.Join(t.TempDir(), "missing.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	failed := sessionCall(t, root, missing, "GET", "/api/status", "")
	var status struct {
		Opened bool
		Error  string
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &status); err != nil || status.Opened || status.Error == "" {
		t.Fatal("missing launch error", failed.Body.String(), err)
	}
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
	oldCache := filepath.Join(CacheDir(), strings.Repeat("a", 64))
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

// A previous workbook can take time to release its metadata worker. Anything
// registered after the new binding becomes visible belongs to the new source.
func TestSwitchRetiresOnlyPreviousWorkbookJobs(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "new.csv")
	if err := os.WriteFile(path, []byte("ID,金额\n001,12\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		a, err := New()
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		ctx, cancel := context.WithCancel(t.Context())
		job := &catalogueTask{ctx: ctx, cancel: cancel, done: make(chan struct{})}
		a.catalogueJobs["old"] = job
		finish := sync.OnceFunc(func() { close(job.done) })
		defer finish()
		switched := make(chan struct{})
		go func() { a.replaceWorkbook(next); close(switched) }()
		synctest.Wait()
		if a.currentWorkbook() != next || ctx.Err() == nil {
			t.Fatal("new binding was not installed while the old worker drained")
		}
		id := attachReconcileCSV(t, a, "right.csv", "ID,金额\n001,11\n")
		finish()
		<-switched
		if a.resolveReconcileBook(id) == nil {
			t.Fatal("old workbook retirement released the new workbook's attachment")
		}
	})
}

func documentCall(a *App, id, page, path, generation string) *httptest.ResponseRecorder {
	r := localTestConnection(httptest.NewRequest("POST", "/api/session/"+path, nil))
	r.Header.Set("X-ExcelLens-Token", a.token)
	r.Header.Set("X-ExcelLens-Session", id)
	r.Header.Set("X-ExcelLens-Page", page)
	r.Header.Set("X-ExcelLens-Generation", generation)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestSessionReaperReturnsFullCapacityWithoutAnotherRequest(t *testing.T) {
	isolateTestStorage(t)
	synctest.Test(t, func(t *testing.T) {
		root, _ := New()
		defer root.Close()
		stop, tick := make(chan struct{}), make(chan time.Time)
		go root.runSessionReaper(stop, tick)
		defer close(stop)
		var ids []string
		page := randomID()
		for i := 0; i < maxBrowserSessions; i++ {
			id := randomID()
			if got := documentCall(root, id, page, "attach", "1"); got.Code != 204 {
				t.Fatal(got.Body)
			}
			ids = append(ids, id)
		}
		for _, id := range ids {
			if got := documentCall(root, id, page, "detach", ""); got.Code != 204 {
				t.Fatal(got.Body)
			}
		}
		if got := documentCall(root, randomID(), page, "attach", "1"); got.Code != 429 {
			t.Fatal(got.Code)
		}
		time.Sleep(browserlease.ReconnectGrace)
		tick <- time.Now()
		synctest.Wait()
		if len(root.activeBrowserWorkbooks()) != 1 {
			t.Fatal("closed tabs still retained")
		}
		if got := documentCall(root, ids[0], page, "heartbeat", ""); got.Code != 410 {
			t.Fatal("late heartbeat resurrected tab", got.Code)
		}
		if got := documentCall(root, randomID(), page, "attach", "1"); got.Code != 204 {
			t.Fatal(got.Body)
		}
	})
}

func TestSessionReloadFencesLateRequestsAndPinsInFlightWork(t *testing.T) {
	isolateTestStorage(t)
	synctest.Test(t, func(t *testing.T) {
		root, _ := New()
		defer root.Close()
		id, first, second := randomID(), randomID(), randomID()
		call := func(page, action, generation string, want int) {
			t.Helper()
			if got := documentCall(root, id, page, action, generation); got.Code != want {
				t.Fatalf("%s: %d %s", action, got.Code, got.Body)
			}
		}
		call(first, "attach", "1", 204)
		s, err := root.acquireSession(id, "", sessionAccess{page: first, existingOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		call(first, "detach", "", 204)
		time.Sleep(browserlease.ReconnectGrace / 2)
		call(second, "attach", "2", 204)
		call(first, "detach", "", 204)
		call(first, "attach", "1", 409)
		call(first, "heartbeat", "", 409)
		time.Sleep(browserlease.ReconnectGrace)
		call(second, "heartbeat", "", 204)
		if root.sessions[id] != s {
			t.Fatal("reload replaced the tab")
		}
		time.Sleep(browserlease.SilenceLimit + time.Minute)
		if root.sessions[id].lease.Expired(time.Now()) {
			t.Fatal("in-flight request expired")
		}
		root.releaseSession(s)
		time.Sleep(browserlease.SilenceLimit)
		call(second, "heartbeat", "", 410)
		if len(root.activeBrowserWorkbooks()) != 1 {
			t.Fatal("silent tab retained")
		}
	})
}
