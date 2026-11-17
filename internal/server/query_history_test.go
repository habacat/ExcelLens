package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

const queryHistoryProbeLimit = formats.QueryTaskHistory

func queryHistoryFixture(t *testing.T) *App {
	t.Helper()
	isolateTestStorage(t)
	a := checkpointOpen(t, filterCheckpointCSV(t, 200))
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRepeatedFilterHistoryIsBoundedWithoutInvalidatingBorrowedRows(t *testing.T) {
	a := queryHistoryFixture(t)
	wb := a.currentWorkbook()
	req := filterCheckpointRequest(true)
	req.Sheet, req.WorkbookKey, req.Book = wb.Sheets[0].Name, wb.Fingerprint, sourceBookID(wb)
	data, _ := json.Marshal(req)
	var borrowed *FilterResult
	for i := 0; i < queryHistoryProbeLimit+20; i++ {
		started := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/filter/start", string(data)))
		a.mu.RLock()
		job := a.filterJobs[started.JobID]
		a.mu.RUnlock()
		filterCheckpointWait(t, job)
		if job.Status != "done" {
			t.Fatal(job.Status, job.Error)
		}
		if i == 0 {
			borrowed = a.filterSnapshot(job.ResultID)
			defer borrowed.close()
		}
		featureCall(t, a, "POST", "/api/filter/clear", fmt.Sprintf(`{"id":%q}`, job.ResultID))
	}
	a.mu.RLock()
	jobs, results := len(a.filterJobs), len(a.filterResults)
	a.mu.RUnlock()
	if jobs > queryHistoryProbeLimit || results != 0 {
		t.Errorf("80 complete filter/clear operations retained %d job histories and %d results", jobs, results)
	}
	if borrowed == nil || borrowed.Rows.Len() != 173 {
		t.Fatal("history or clear invalidated independently borrowed source rows")
	}
}

func TestCachedSearchHistoryUsesTheSameCapacityAsScannedSearch(t *testing.T) {
	a := queryHistoryFixture(t)
	wb := a.currentWorkbook()
	req := searchRequest{Sheet: wb.Sheets[0].Name, Query: "keep", Mode: "value", Scope: "current"}
	data, _ := json.Marshal(req)
	started := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/search/start", string(data)))
	a.mu.RLock()
	first := a.searchJobs[started.JobID]
	a.mu.RUnlock()
	checkpointWait(t, first)
	if first.Status != "done" || len(first.Matches) != 172 {
		t.Fatal("baseline search did not produce complete real cached results", first.Status, len(first.Matches))
	}
	a.mu.Lock()
	a.searchJobs = make(map[string]*SearchJob)
	for i := range queryHistoryProbeLimit {
		ctx, cancel := context.WithCancel(context.Background())
		job := &SearchJob{ID: randomID(), Query: fmt.Sprintf("old-%d", i), Status: "done", Started: time.Now().Add(time.Duration(i-queryHistoryProbeLimit) * time.Minute), ctx: ctx, cancel: cancel, done: make(chan struct{})}
		close(job.done)
		a.searchJobs[job.ID] = job
	}
	a.mu.Unlock()
	response := decodeResponse[struct {
		JobID  string
		Cached bool
	}](t, featureCall(t, a, "POST", "/api/search/start", string(data)))
	a.mu.RLock()
	count, cached := len(a.searchJobs), a.searchJobs[response.JobID]
	a.mu.RUnlock()
	if !response.Cached || cached == nil || len(cached.Matches) != 172 {
		t.Fatal("request did not exercise the actual disk cache", response)
	}
	if count > queryHistoryProbeLimit {
		t.Fatal("disk-cache hit bypassed search history capacity", count)
	}
}

func TestSearchHistoryDoesNotEvictTerminalWorkerBeforeCacheWriteExit(t *testing.T) {
	jobs := make(map[string]*SearchJob)
	for i := range queryHistoryProbeLimit {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		job := &SearchJob{ID: fmt.Sprintf("pending-%03d", i), Status: "done", ctx: ctx, cancel: cancel, done: make(chan struct{})}
		jobs[job.ID] = job
		defer close(job.done)
	}
	pruneSearchJobsLocked(jobs, queryHistoryProbeLimit)
	if len(jobs) != queryHistoryProbeLimit {
		t.Fatal("displayed completion allowed removal before cache write and worker exit", len(jobs))
	}
	for _, job := range jobs {
		if job.ctx.Err() != nil {
			t.Fatal("history cancelled a worker that had not exited")
		}
	}
}

func occupyQueryHistory(t *testing.T, a *App, kind string) func() {
	t.Helper()
	var done []chan struct{}
	var cancels []context.CancelFunc
	a.mu.Lock()
	oldFilters, oldSearches := a.filterJobs, a.searchJobs
	if kind == "filter" {
		a.filterJobs = make(map[string]*FilterJob)
	} else {
		a.searchJobs = make(map[string]*SearchJob)
	}
	for range formats.QueryTaskHistory {
		ctx, cancel := context.WithCancel(context.Background())
		id, exited := randomID(), make(chan struct{})
		done, cancels = append(done, exited), append(cancels, cancel)
		if kind == "filter" {
			a.filterJobs[id] = &FilterJob{ID: id, Status: "done", ctx: ctx, cancel: cancel, done: exited}
		} else {
			a.searchJobs[id] = &SearchJob{ID: id, Query: id, Status: "done", ctx: ctx, cancel: cancel, done: exited}
		}
	}
	a.mu.Unlock()
	release := sync.OnceFunc(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, channel := range done {
			cancels[i]()
			close(channel)
		}
		if kind == "filter" {
			a.filterJobs = oldFilters
		} else {
			a.searchJobs = oldSearches
		}
	})
	t.Cleanup(release)
	return release
}

func TestQueryHistoryCapacityRejectsNewWorkWithoutLeakingOrDeletingRecovery(t *testing.T) {
	for _, kind := range []string{"filter", "search"} {
		for _, mode := range []string{"ordinary", "new-recoverable", "resume"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				a := queryHistoryFixture(t)
				wb := a.currentWorkbook()
				filter := filterCheckpointRequest(false)
				filter.Sheet, filter.Book, filter.WorkbookKey = wb.Sheets[0].Name, sourceBookID(wb), wb.Fingerprint
				search := searchRequest{Sheet: wb.Sheets[0].Name, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint, Mode: "value", Scope: "current", Query: "keep"}
				var saved checkpoint.Manifest
				if mode == "resume" {
					var def checkpoint.Definition
					var cursor []byte
					var err error
					if kind == "filter" {
						def, err = filterTaskDefinition(wb, filter.Sheet, newFilterSpec(filter.Rules, filter.options()))
						cursor, _ = json.Marshal(filterTaskCursor{})
					} else {
						def, err = searchTaskDefinition(wb, search)
						cursor, _ = json.Marshal(searchTaskCursor{})
					}
					if err != nil {
						t.Fatal(err)
					}
					store, err := openTaskCheckpoints()
					if err != nil {
						t.Fatal(err)
					}
					lease, err := store.Create(context.Background(), def, "prepare", cursor)
					if err != nil {
						t.Fatal(err)
					}
					saved = lease.Snapshot()
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
				}
				release := occupyQueryHistory(t, a, kind)
				if mode == "resume" {
					checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: saved.ID, Revision: saved.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, http.StatusTooManyRequests)
				} else {
					filter.Recoverable, search.Recoverable = mode == "new-recoverable", mode == "new-recoverable"
					var request any = filter
					if kind == "search" {
						request = search
					}
					checkpointCall(t, a, "/api/"+kind+"/start", request, http.StatusTooManyRequests)
				}
				store, err := openTaskCheckpoints()
				if err != nil {
					t.Fatal(err)
				}
				entries, err := store.List()
				_ = store.Close()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "resume" {
					if len(entries) != 1 || entries[0].Manifest == nil || entries[0].ID != saved.ID || entries[0].Manifest.Revision != saved.Revision {
						t.Fatal("rejected resume changed an existing confirmed checkpoint", entries)
					}
				} else if len(entries) != 0 {
					t.Fatal("rejected start leaked an unpublished recovery task", entries)
				}
				a.sourceMu.Lock()
				pins := len(a.sourcePins)
				a.sourceMu.Unlock()
				if pins != 0 || len(a.querySem) != 0 || len(a.recoverySem) != 0 {
					t.Fatal("rejected admission leaked a source or shared slot", pins)
				}
				release()
			})
		}
	}
}
