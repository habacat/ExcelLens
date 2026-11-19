package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

// An optional integration smoke test for a large real-world workbook.
// It is skipped by default; the sample workbook is never distributed.
func TestRealWorkbookReadonlySmoke(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("set EXCELLENS_REAL_XLSM to a local XLSM workbook")
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	start := time.Now()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	t.Logf("workbook opened: sheets=%d duration=%s", len(wb.Sheets), time.Since(start))
	if len(wb.Sheets) < 2 {
		t.Fatalf("expected multi-sheet workbook, got %d", len(wb.Sheets))
	}
	var metaName string
	for _, sheet := range wb.Sheets {
		if sheet.Path == "xl/worksheets/sheet8.xml" {
			metaName = sheet.Name
			break
		}
	}
	if metaName == "" {
		metaName = wb.Sheets[0].Name
	}
	meta, _ := wb.FindSheet(metaName)
	start = time.Now()
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sheet %s indexed: rows=%d maxcol=%d duration=%s", meta.Name, idx.RowCount(), idx.MaxCol, time.Since(start))
	if idx.RowCount() == 0 {
		t.Fatal("sheet has no rows")
	}
	first := featureCall(t, a, "GET", "/api/sheet?sheet="+url.QueryEscape(meta.Name)+"&row=1&limit=3&col=1&cols=4", "")
	if first.Body.Len() < 100 {
		t.Fatalf("sheet body too small: %s", first.Body.String())
	}
	view := featureCall(t, a, "GET", "/api/view?sheet="+url.QueryEscape(meta.Name), "")
	t.Logf("native/saved freeze: %s", view.Body.String())
}

func TestRealWholeWorkbookSearchPerformance(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("requires local workbook")
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	sheet := app.currentWorkbook().Sheets[0].Name
	req := fmt.Sprintf(`{"sheet":%q,"query":"__NEVER_MATCH_2026_BENCHMARK__","mode":"formula","scope":"all"}`, sheet)
	start := time.Now()
	response := featureCall(t, app, "POST", "/api/search/start", req)
	var begin struct {
		JobID  string `json:"jobId"`
		Cached bool   `json:"cached"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	job := app.searchJobs[begin.JobID]
	app.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(180 * time.Second):
		job.cancel()
		t.Fatal("whole book search exceeded 180s")
	}
	job.mu.RLock()
	status := job.Status
	stage := job.Stage
	hits := len(job.Matches)
	done := job.SheetsDone
	job.mu.RUnlock()
	t.Logf("all-sheet search: elapsed=%s status=%s sheets=%d hits=%d cached=%v stage=%s", time.Since(start), status, done, hits, begin.Cached, stage)
	if status != "done" || done != len(app.currentWorkbook().Sheets) {
		t.Fatal("unexpected whole-workbook result")
	}
}
