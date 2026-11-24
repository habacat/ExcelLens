package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/browserlease"
)

func TestSubmittedSearchOutlivesExpiredPageAndCanBeReclaimed(t *testing.T) {
	isolateTestStorage(t)
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(t.TempDir(), "source.csv")
	if err = os.WriteFile(path, []byte("id\nhit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := root.OpenPathForLaunch(path)
	if err != nil {
		t.Fatal(err)
	}
	page := randomID()
	if got := documentCall(root, id, page, "attach", "1"); got.Code != 204 {
		t.Fatal(got.Body)
	}
	child := root.sessions[id].app
	wb := child.currentWorkbook()
	root.scanQueries <- struct{}{}
	body, _ := json.Marshal(searchRequest{Sheet: wb.Sheets[0].Name, Query: "hit", Mode: "value", Scope: "all"})
	started := sessionCall(t, root, id, "POST", "/api/search/start", string(body))
	var result struct{ JobID string }
	if json.Unmarshal(started.Body.Bytes(), &result) != nil || result.JobID == "" {
		t.Fatal(started.Body)
	}
	if got := documentCall(root, id, page, "detach", ""); got.Code != 204 {
		t.Fatal(got.Body)
	}
	root.sessionsMu.Lock()
	expired := root.takeExpiredLocked(time.Now().Add(browserlease.SilenceLimit * 2))
	root.sessionsMu.Unlock()
	if len(expired) != 0 {
		t.Fatal("queued user job reaped")
	}
	<-root.scanQueries
	child.mu.RLock()
	job := child.searchJobs[result.JobID]
	child.mu.RUnlock()
	checkpointWait(t, job)
	response := sessionCall(t, root, "", "GET", "/api/background", "")
	var list struct{ Items []backgroundItem }
	if json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Items) != 1 || !list.Items[0].CanAttach || list.Items[0].State != "done" {
		t.Fatal(response.Body)
	}
	req := localTestConnection(httptest.NewRequest("POST", "/api/session/attach", nil))
	for k, v := range map[string]string{"Token": root.token, "Session": id, "Page": randomID(), "Generation": "2", "Background": "1"} {
		req.Header.Set("X-ExcelLens-"+k, v)
	}
	res := httptest.NewRecorder()
	root.Handler().ServeHTTP(res, req)
	if res.Code != 204 {
		t.Fatal(res.Code, res.Body)
	}
	status := sessionCall(t, root, id, "GET", "/api/search/status?id="+result.JobID, "")
	if !strings.Contains(status.Body.String(), `"value":"hit"`) {
		t.Fatal(status.Body)
	}
	// A second result link cannot evict an already attached live document.
	req.Header.Set("X-ExcelLens-Page", randomID())
	req.Header.Set("X-ExcelLens-Generation", "3")
	res = httptest.NewRecorder()
	root.Handler().ServeHTTP(res, req)
	if res.Code != 409 {
		t.Fatal(res.Code)
	}
}
