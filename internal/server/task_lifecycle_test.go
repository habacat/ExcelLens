package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

type lifecycleStatusWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *lifecycleStatusWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestTaskStatusSlowClientCannotPreventCancelledWorkerExit(t *testing.T) {
	for _, kind := range []string{"index", "row-view", "analysis", "tree", "reconcile", "capture", "archive"} {
		t.Run(kind, func(t *testing.T) {
			a, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			// Keep every real worker queued, so cancellation must finish without
			// reading a source, waiting for another slot or waiting for HTTP.
			for _, slots := range []chan struct{}{a.indexSem, a.querySem, a.analysisSem} {
				for range cap(slots) {
					slots <- struct{}{}
				}
				defer func() {
					for range cap(slots) {
						<-slots
					}
				}()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, done := randomID(), make(chan struct{})
			var handle http.HandlerFunc
			var run func()
			switch kind {
			case "index":
				job := &IndexJob{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done}
				a.indexJobs[id] = job
				handle, run = a.handleIndexStatus, func() { a.runIndex(nil, workbook.SheetMeta{}, job) }
			case "row-view":
				job := &rowViewTask{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done}
				a.rowViewJobs[id] = job
				handle, run = a.handleRowViewStatus, func() {
					a.runRowView(nil, workbook.SheetMeta{}, nil, nil, workbook.AxisViewSettings{}, 0, "", rowViewRequest{}, "", func() {}, nil, job)
				}
			case "analysis":
				job := &analysisTask{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done}
				a.analysisJobs[id] = job
				handle, run = a.handleAnalysisQueryStatus, func() { a.runAnalysisQuery(job, analysis.Query{}) }
			case "tree":
				job := &analysisTreeTask{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done, release: func() {}}
				a.analysisTreeJobs[id] = job
				handle, run = a.handleAnalysisTreeStatus, func() { a.runAnalysisTree(job) }
			case "reconcile":
				job := &reconcileTask{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done, release: func() {}}
				a.reconcileJobs[id] = job
				handle, run = a.handleReconcileStatus, func() { a.runReconcile(job) }
			case "capture":
				job := &recipeCaptureTask{ID: id, Status: "queued", ctx: ctx, cancel: cancel, done: done, release: func() {}}
				a.recipeCaptures[id] = job
				handle, run = a.handleRecipeCaptureStatus, func() { a.runRecipeCapture(job) }
			case "archive":
				job := &archiveTask{id: id, status: "queued", cancel: cancel, done: done}
				a.archiveTask = job
				// Exercise the same progress/terminal lock used by extraction;
				// archive extraction itself has its own source/failure regressions.
				handle, run = a.handleArchiveJob, func() {
					defer close(done)
					<-ctx.Done()
					job.mu.Lock()
					job.status = "cancelled"
					job.mu.Unlock()
				}
			}
			w := &lifecycleStatusWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			unblock := sync.OnceFunc(func() { close(w.release) })
			defer unblock()
			responseDone := make(chan struct{})
			go func() {
				defer close(responseDone)
				handle(w, httptest.NewRequest("GET", "/status?id="+id, nil))
			}()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("status response never reached its blocked network write")
			}
			go run()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				unblock()
				<-done
				t.Error("slow status client prevented cancelled worker exit")
			}
			unblock()
			<-responseDone
			var payload map[string]any
			if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload["status"] != "queued" {
				t.Fatal("response did not retain its original consistent state", w.Body.String())
			}
		})
	}
}

func TestExportAdmissionWaitsForActualWorkerExit(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for range 2 {
		job, err := a.newExportJob(nil, "csv")
		if err != nil {
			t.Fatal(err)
		}
		job.complete(0) // publication can precede deferred resource release
		defer close(job.done)
	}
	if extra, err := a.newExportJob(nil, "csv"); !errors.Is(err, errExportBusy) {
		if extra != nil {
			extra.cancel()
			close(extra.done)
		}
		t.Fatal("completed display status admitted work before prior resources were returned", err)
	}
}

func TestExportHistoryRetainsExitingWorkersAndNewestDownloads(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	stamp := time.Now().Add(-time.Hour)
	pending, err := a.newExportJob(nil, "csv")
	if err != nil {
		t.Fatal(err)
	}
	pending.Started = stamp
	pending.complete(0)
	defer close(pending.done)
	var completed []*ExportJob
	for i := range 25 {
		ctx, cancel := context.WithCancel(context.Background())
		job := &ExportJob{ID: randomID(), Status: "done", Started: stamp.Add(time.Duration(i+1) * time.Second), ctx: ctx, cancel: cancel, done: make(chan struct{})}
		close(job.done)
		defer cancel()
		a.exportJobs[job.ID] = job
		completed = append(completed, job)
	}
	newest, err := a.newExportJob(nil, "csv")
	if err != nil {
		t.Fatal(err)
	}
	defer close(newest.done)
	if len(a.exportJobs) != 20 || a.exportJobs[pending.ID] != pending || a.exportJobs[newest.ID] != newest {
		t.Fatal("history lost a live worker or exceeded its pruning target", len(a.exportJobs))
	}
	for i, job := range completed {
		if retained := a.exportJobs[job.ID] != nil; retained != (i >= 7) {
			t.Errorf("history did not discard the oldest exited download: index=%d retained=%v", i, retained)
		}
	}
}

func TestUnsuccessfulReconcileReleasesRemovedSourceBeforeHistoryClear(t *testing.T) {
	for _, outcome := range []string{"cancelled", "error"} {
		t.Run(outcome, func(t *testing.T) {
			isolateTestStorage(t)
			path := filepath.Join(t.TempDir(), "primary.csv")
			if err := os.WriteFile(path, []byte("ID,Amount\n001,9007199254740993.31\n"), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := a.OpenPath(path); err != nil {
				t.Fatal(err)
			}
			wb := a.currentWorkbook()
			book := attachReconcileCSV(t, a, "other.csv", "ID,Amount\n001,9007199254740993.31\n")
			rightBook := a.resolveReconcileBook(book)
			left := reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
			right := reconcileRef(t, a, book, "wb:"+rightBook.Sheets[0].Name, 1)
			if outcome == "error" {
				right.Stamp = strings.Repeat("0", 64)
			}
			releaseSlots := holdRelationSlots(t, a.querySem)
			request := map[string]any{"workbookKey": wb.Fingerprint, "primaryBook": sourceBookID(wb), "left": left, "right": right, "query": reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}}}
			started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/start", request))
			job := a.getReconcileTask(started.JobID)
			decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": book}))
			if outcome == "cancelled" {
				decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/cancel", map[string]string{"id": job.ID}))
			} else {
				releaseSlots()
			}
			select {
			case <-job.done:
			case <-time.After(5 * time.Second):
				t.Fatal("unsuccessful reconciliation did not exit")
			}
			a.sourceMu.Lock()
			pins := len(a.sourcePins)
			a.sourceMu.Unlock()
			if job.Status != outcome || pins != 0 || job.Books[0] != nil || job.Books[1] != nil || job.release != nil {
				t.Error("unsuccessful history retained removed sources", job.Status, job.Error, pins)
			}
			response := featureCall(t, a, "GET", "/api/reconcile/status?id="+job.ID, "")
			if response.Code != 200 || !strings.Contains(response.Body.String(), outcome) {
				t.Fatal("releasing sources lost terminal status", response.Body.String())
			}
			releaseSlots()
			decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/clear", map[string]string{"id": job.ID}))
		})
	}
}
