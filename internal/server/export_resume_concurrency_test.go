package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"excellens/internal/checkpoint"
)

func TestExportResumeSlowStatusClientDoesNotHoldTaskWriterLock(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	releaseSlots := holdRowExportSlots(a)
	defer releaseSlots()
	job := startRowExport(t, a, exportRequest{Scope: "sheet", Sheet: "First", Format: "csv", Mode: "values", Recoverable: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint})
	w := &filterStatusBlockedWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(w.release) })
	defer release()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.handleExportStatus(w, httptest.NewRequest("GET", "/api/export/status?id="+job.ID, nil))
	}()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("status response did not reach its blocked network write")
	}
	releaseSlots()
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("slow HTTP status response stalled the durable export writer")
	}
	release()
	<-done
	waitRowExport(t, job, "done")
}

func TestExportResumeTwoTabsAndConcurrentTaskDirectoryKeepCompletedDownloads(t *testing.T) {
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
	start := func(app *App) *ExportJob {
		wb := app.currentWorkbook()
		return startRowExport(t, app, exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: "csv", Mode: "values", Recoverable: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint})
	}
	first, second := start(a), start(child.app)
	waitRowExport(t, first, "done")
	waitRowExport(t, second, "done")
	entries, err := listTaskCheckpoints(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	complete := map[string]bool{}
	for _, entry := range entries {
		complete[entry.ID] = entry.Error == "" && entry.Manifest != nil && entry.Manifest.Complete
	}
	if first.TaskID == second.TaskID || first.Size != second.Size || !complete[first.TaskID] || !complete[second.TaskID] {
		t.Fatal("two tabs shared durable output or lost complete publications")
	}
}
