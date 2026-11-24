package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/localstore"
)

func TestDetachedResultsDoNotBlockRestoreOrConsumePageSlots(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	path := filepath.Join(t.TempDir(), "test.csv")
	if err := os.WriteFile(path, []byte("code,value\n001,needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sid, err := a.OpenPathForLaunch(path)
	if err != nil {
		t.Fatal(err)
	}
	page := randomID()
	if r := documentCall(a, sid, page, "attach", "1"); r.Code != 204 {
		t.Fatal(r.Body)
	}
	old := a.sessions[sid].app
	job, _ := checkpointStart(t, old, "needle", "value", "current")
	checkpointWait(t, job)
	id := job.ID
	roots, _ := dataRoots()
	folder, err := roots.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.restoreStorage(t.Context(), roots, folder, localstore.TransferOptions{}); err == nil || !strings.Contains(err.Error(), "test.csv") {
		t.Fatal(err)
	}
	if r := documentCall(a, sid, page, "detach", ""); r.Code != 204 {
		t.Fatal(r.Body)
	}
	if err := a.restoreStorage(t.Context(), roots, folder, localstore.TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	if a.sessions[sid].saved == nil || a.sessions[sid].app != nil || old.currentWorkbook() != nil {
		t.Fatal("closed page still owns result")
	}
	ids := make([]string, 0, maxBrowserSessions)
	for range maxBrowserSessions {
		next := randomID()
		ids = append(ids, next)
		if r := documentCall(a, next, randomID(), "attach", "1"); r.Code != 204 {
			t.Fatal("retained result consumed slot", r.Body)
		}
	}
	if r := documentCall(a, sid, randomID(), "attach", "2"); r.Code != 429 {
		t.Fatal("reattached beyond capacity", r.Code)
	}
	// Retire one unused synthetic page before claiming the saved result.
	a.sessionsMu.Lock()
	empty := a.sessions[ids[0]]
	delete(a.sessions, ids[0])
	a.sessionsMu.Unlock()
	empty.close()
	newPage := randomID()
	if r := documentCall(a, sid, newPage, "attach", "2"); r.Code != 204 {
		t.Fatal(r.Body)
	}
	result := sessionCall(t, a, sid, "GET", "/api/search/status?id="+id, "")
	if result.Code != 200 || !strings.Contains(result.Body.String(), "needle") || a.sessions[sid].app == old {
		t.Fatal(result.Body)
	}
	if r := documentCall(a, sid, newPage, "detach", ""); r.Code != 204 {
		t.Fatal(r.Body)
	}
	body := fmt.Sprintf(`{"action":"discard","session":%q,"id":%q,"kind":"search"}`, sid, id)
	if r := sessionCall(t, a, "", "POST", "/api/background", body); r.Code != 200 || a.sessions[sid] != nil {
		t.Fatal(r.Body)
	}
}

func TestBackgroundCenterCancelsWorkerBeforeSessionRelease(t *testing.T) {
	isolateTestStorage(t)
	a, _ := New()
	defer a.Close()
	sid, page := randomID(), randomID()
	documentCall(a, sid, page, "attach", "1")
	s := a.sessions[sid]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	id := randomID()
	job := &SearchJob{ID: id, Status: "running", Started: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.app.searchJobs[id] = job
	s.app.retainSubmitted(id, job.done)
	go func() { <-ctx.Done(); job.mu.Lock(); job.Status = "cancelled"; job.mu.Unlock(); close(job.done) }()
	documentCall(a, sid, page, "detach", "")
	action := func(name string) int {
		return sessionCall(t, a, "", "POST", "/api/background", fmt.Sprintf(`{"action":%q,"session":%q,"kind":"search","id":%q}`, name, sid, id)).Code
	}
	if action("release") != 409 || action("cancel") != 200 {
		t.Fatal("running task management failed")
	}
	<-job.done
	if action("release") != 200 || a.sessions[sid] != nil {
		t.Fatal("release failed")
	}
}

func TestBackgroundDiscardProtectsLiveFilter(t *testing.T) {
	isolateTestStorage(t)
	a, _ := New()
	defer a.Close()
	sid, page := randomID(), randomID()
	documentCall(a, sid, page, "attach", "1")
	app := a.sessions[sid].app
	id := randomID()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	close(done)
	order := rowOrderFixture(t, 5, 1, 3)
	app.filterJobs[id] = &FilterJob{ID: id, ResultID: id, Status: "done", ctx: ctx, cancel: cancel, done: done}
	app.filterResults[id] = &FilterResult{ID: id, Rows: order}
	body := fmt.Sprintf(`{"action":"discard","session":%q,"id":%q,"kind":"filter"}`, sid, id)
	if got := sessionCall(t, a, randomID(), "POST", "/api/background", body); got.Code != 409 {
		t.Fatal(got.Code, got.Body)
	}
	snapshot := app.filterSnapshot(id)
	if snapshot == nil || snapshot.Rows.Len() != 2 {
		t.Fatal("live filter lost rows")
	}
	snapshot.close()
	documentCall(a, sid, page, "detach", "")
	if got := sessionCall(t, a, "", "POST", "/api/background", body); got.Code != 200 {
		t.Fatal(got.Body)
	}
}
