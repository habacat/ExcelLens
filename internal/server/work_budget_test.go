package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestBrowserTabsShareWorkBudgetAndQueuedFilterCanCancel(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	child, err := app.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if child.app.indexSem != app.indexSem || child.app.querySem != app.querySem || child.app.analysisSem != app.analysisSem || child.app.exportSem != app.exportSem || cap(app.indexSem) != 2 || cap(app.querySem) != 2 || cap(app.exportSem) != 2 {
		t.Fatal("tabs created independent unbounded background pools")
	}
	app.querySem <- struct{}{}
	app.querySem <- struct{}{}
	defer func() { <-app.querySem; <-app.querySem }()
	ctx, cancel := context.WithCancel(context.Background())
	job := &FilterJob{ctx: ctx, cancel: cancel, done: make(chan struct{}), Status: "queued"}
	go child.app.runFilter(nil, workbook.SheetMeta{}, nil, nil, workbook.FilterOptions{}, job)
	cancel()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("queued query ignored cancellation")
	}
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	if status != "cancelled" {
		t.Fatal(status)
	}
}

func TestQueuedExportSharesSlotsAcrossTabsAndCancelsBeforeReading(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	child, err := app.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	app.exportSem <- struct{}{}
	app.exportSem <- struct{}{}
	defer func() { <-app.exportSem; <-app.exportSem }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &ExportJob{ctx: ctx, cancel: cancel, done: make(chan struct{}), Status: "queued"}
	go child.app.runExport(exportRequest{}, nil, job)
	select {
	case <-job.done:
		t.Fatal("export ran without a shared slot")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("queued export ignored cancellation")
	}
	if job.Status != "cancelled" || job.FilePath != "" {
		t.Fatal("cancelled queue wrote a file", job.Status)
	}
}

func TestStatisticsWaitForSharedBudgetAndCancelWithoutReading(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "budget.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	app.querySem <- struct{}{}
	app.querySem <- struct{}{}
	defer func() { <-app.querySem; <-app.querySem }()
	for _, endpoint := range []string{"/api/profile", "/api/selection/stats"} {
		body := `{"sheet":"First","start":"A1","end":"D4"}`
		if endpoint == "/api/selection/stats" {
			body = `{"sheet":"First","ranges":[{"start":"A1","end":"D4"}]}`
		}
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest("POST", endpoint, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			if endpoint == "/api/profile" {
				app.handleProfile(w, r)
			} else {
				app.handleSelectionStats(w, r)
			}
		}()
		select {
		case <-done:
			cancel()
			t.Fatal("statistics bypassed shared query budget", endpoint)
		case <-time.After(20 * time.Millisecond):
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("queued statistics ignored cancellation", endpoint)
		}
		if w.Body.Len() != 0 {
			t.Fatal("cancelled statistics published a result", w.Body.String())
		}
	}
}
