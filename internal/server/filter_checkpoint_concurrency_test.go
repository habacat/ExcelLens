package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"excellens/internal/checkpoint"
)

type filterStatusBlockedWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *filterStatusBlockedWriter) Header() http.Header { return w.header }
func (w *filterStatusBlockedWriter) WriteHeader(int)     {}
func (w *filterStatusBlockedWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}

func TestFilterCheckpointSlowStatusClientDoesNotBlockWriterOrNextIntent(t *testing.T) {
	isolateTestStorage(t)
	a := checkpointOpen(t, filterCheckpointCSV(t, 60000))
	a.querySem <- struct{}{}
	a.querySem <- struct{}{}
	defer func() { <-a.querySem }() // one manual token remains after admitting work
	first := filterCheckpointStart(t, a, filterCheckpointRequest(false))
	w := &filterStatusBlockedWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(w.release) })
	defer release()
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		a.handleFilterStatus(w, httptest.NewRequest("GET", "/api/filter/status?id="+first.ID, nil))
	}()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("status did not reach its network write")
	}
	<-a.querySem
	// Reach either an active writer or its pending write lock. A held status
	// read lock plus a writer would otherwise deadlock the next intent's lock.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !first.mu.TryRLock() {
			break
		}
		status := first.Status
		first.mu.RUnlock()
		if status != "queued" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	req := filterCheckpointRequest(false)
	wb := a.currentWorkbook()
	req.Sheet, req.Book, req.WorkbookKey, req.Recoverable = wb.Sheets[0].Name, sourceBookID(wb), wb.Fingerprint, true
	data, _ := json.Marshal(req)
	started := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		a.handleFilterStart(recorder, httptest.NewRequest("POST", "/api/filter/start", bytes.NewReader(data)))
		started <- recorder
	}()
	var response *httptest.ResponseRecorder
	select {
	case response = <-started:
	case <-time.After(3 * time.Second):
		release()
		response = <-started
		t.Error("a stalled HTTP status response blocked the next filter intent")
	}
	release()
	<-statusDone
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var ids struct{ JobID string }
	if err := json.Unmarshal(response.Body.Bytes(), &ids); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	second := a.filterJobs[ids.JobID]
	a.mu.RUnlock()
	filterCheckpointWait(t, first)
	filterCheckpointWait(t, second)
	if second.Status != "done" {
		t.Fatal(second.Status, second.Error)
	}
}

func TestFilterCheckpointConcurrentDirectoryAndTwoTabsKeepCompleteResults(t *testing.T) {
	isolateTestStorage(t)
	file := filterCheckpointCSV(t, 50000)
	a := checkpointOpen(t, file)
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := store.List(); err != nil && !errors.Is(err, checkpoint.ErrBusy) {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { close(stop); <-stopped }()
	first := filterCheckpointStart(t, a, filterCheckpointRequest(true))
	second := filterCheckpointStart(t, child.app, filterCheckpointRequest(false))
	filterCheckpointWait(t, first)
	filterCheckpointWait(t, second)
	if first.Status != "done" || second.Status != "done" || len(filterCheckpointPositions(t, a, first)) != len(filterCheckpointPositions(t, child.app, second)) {
		t.Fatal("directory contention failed a durable filter", first.Status, first.Error, second.Status, second.Error)
	}
}
