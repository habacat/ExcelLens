package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestSearchRetryNeverAdoptsCancelledRunningIntent(t *testing.T) {
	a := queryHistoryFixture(t)
	wb := a.currentWorkbook()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	old := &SearchJob{ID: randomID(), Sheet: wb.Sheets[0].Name, Query: "keep", Mode: "value", Scope: "current", Status: "running", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	defer close(old.done)
	a.searchJobs[old.ID] = old
	data, _ := json.Marshal(searchRequest{Sheet: old.Sheet, Query: old.Query, Mode: old.Mode, Scope: old.Scope, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint})
	started := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/search/start", string(data)))
	if started.JobID == old.ID {
		t.Fatal("retry reused an already cancelled running intent")
	}
	a.mu.RLock()
	next := a.searchJobs[started.JobID]
	a.mu.RUnlock()
	checkpointWait(t, next)
	if next.Status != "done" || len(next.Matches) != 172 {
		t.Fatal("retry lost the complete intended results", next.Status, next.Error)
	}
}

func TestOrdinarySearchHonoursProvidedSourceBinding(t *testing.T) {
	for _, kind := range []string{"old-book", "old-fingerprint", "matching", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			a := queryHistoryFixture(t)
			wb := a.currentWorkbook()
			req := searchRequest{Sheet: wb.Sheets[0].Name, Query: "keep", Mode: "value", Scope: "current", Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
			switch kind {
			case "old-book":
				req.Book = "a different source path"
			case "old-fingerprint":
				req.WorkbookKey = "a different source revision"
			case "legacy":
				req.Book, req.WorkbookKey = "", ""
			}
			data, _ := json.Marshal(req)
			response := analysisRequest(t, a, "POST", "/api/search/start", string(data))
			stale := kind == "old-book" || kind == "old-fingerprint"
			if stale {
				if response.Code != http.StatusConflict {
					t.Error("stale source search was accepted", response.Code, response.Body.String())
				}
				return
			}
			started := decodeResponse[struct{ JobID string }](t, response)
			a.mu.RLock()
			job := a.searchJobs[started.JobID]
			a.mu.RUnlock()
			checkpointWait(t, job)
			if job.Status != "done" || len(job.Matches) != 172 {
				t.Fatal("matching or legacy request lost its valid results", job.Status, job.Error)
			}
		})
	}
}

func TestCompletedSearchRemainsReusableAfterLateCancellation(t *testing.T) {
	a := queryHistoryFixture(t)
	wb := a.currentWorkbook()
	data, _ := json.Marshal(searchRequest{Sheet: wb.Sheets[0].Name, Query: "keep", Mode: "value", Scope: "current"})
	first := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/search/start", string(data)))
	a.mu.RLock()
	job := a.searchJobs[first.JobID]
	a.mu.RUnlock()
	checkpointWait(t, job)
	featureCall(t, a, "POST", "/api/search/cancel", `{"id":"`+job.ID+`"}`)
	again := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/search/start", string(data)))
	if again.JobID != job.ID || len(job.Matches) != 172 {
		t.Fatal("late cancellation forced a completed immutable result to rescan")
	}
}
