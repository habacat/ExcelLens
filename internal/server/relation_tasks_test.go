package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

func relationStart(t *testing.T, a *App, req relationStartRequest) *relationTask {
	t.Helper()
	wb := a.currentWorkbook()
	req.WorkbookKey, req.Book = wb.Fingerprint, sourceBookID(wb)
	out := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/start", req))
	a.mu.RLock()
	job := a.relationJobs[out.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("relation task missing after accepted start")
	}
	return job
}

func waitRelation(t *testing.T, a *App, job *relationTask, status string) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(20 * time.Second):
		t.Fatal("relation task did not finish", job.ID)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if job.Status != status {
		t.Fatal("unexpected relation task state", job.Status, job.Error)
	}
}

func relationResult(t *testing.T, a *App, id string) relationTaskResult {
	t.Helper()
	return decodeResponse[relationTaskResult](t, analysisRequest(t, a, "GET", "/api/relation/result?id="+id, ""))
}

func clearRelation(t *testing.T, a *App, id string) {
	t.Helper()
	if out := reconcilePost(t, a, "/api/relation/clear", map[string]string{"id": id}); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
}

func relationPrepared(t *testing.T, a *App, refs [2]reconcileSourceRef) *relationTask {
	t.Helper()
	query := relationServerQuery()
	job := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
	waitRelation(t, a, job, "done")
	return job
}

func holdRelationSlots(t *testing.T, sem chan struct{}) func() {
	t.Helper()
	for range cap(sem) {
		sem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range cap(sem) {
				<-sem
			}
		})
	}
	t.Cleanup(release)
	return release
}

type relationBlockedResponse struct {
	*httptest.ResponseRecorder
	entered, resume chan struct{}
}

func (w *relationBlockedResponse) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.resume
	return w.ResponseRecorder.Write(data)
}

func TestRelationTasksSevenModesShareOneCompletePlanAndKeepStatusLightweight(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	result := relationResult(t, a, root.ID)
	if result.Preview.Total != 2 || result.Counts.InnerRows != 1 || result.Counts.LeftInvalid != 1 || result.Counts.RightOnly != 1 || result.Left.ID != refs[0].Book+"/"+refs[0].Source || result.Right.ID != refs[1].Book+"/"+refs[1].Source {
		t.Fatal("preflight result lost exact counts or identity", result)
	}
	status := analysisRequest(t, a, "GET", "/api/relation/status?id="+root.ID, "")
	var state map[string]json.RawMessage
	if json.Unmarshal(status.Body.Bytes(), &state) != nil || string(state["total"]) != `"1"` || state["preview"] != nil || state["result"] != nil || state["rows"] != nil {
		t.Fatal("polling returned large rows or inexact counters", status.Body.String())
	}
	for _, mode := range []string{"left", "inner", "left_matched", "left_unmatched", "right_unmatched", "left_invalid", "right_invalid"} {
		job := relationStart(t, a, relationStartRequest{Mode: "preview", ParentID: root.ID, OutputMode: mode, KeepID: root.ID})
		waitRelation(t, a, job, "done")
		result := relationResult(t, a, job.ID)
		total, _ := result.Counts.Rows(mode)
		if result.Preview.Total != total || int64(len(result.Preview.Rows)) != total || result.Preview.Truncated || result.OutputMode != mode || !job.Source.Same(root.Source) {
			t.Fatal("preview repeated/replaced the complete preflight", mode, result)
		}
		a.mu.RLock()
		count := len(a.relationJobs)
		a.mu.RUnlock()
		if count > formats.RelationTaskHistory {
			t.Fatal("mode changes grew unbounded history", count)
		}
	}
	request := map[string]any{"jobId": root.ID, "book": refs[0].Book, "side": 0, "row": 2, "col": 2}
	jump := decodeResponse[struct {
		SameBook bool
		Row, Col int
	}](t, reconcilePost(t, a, "/api/relation/navigate", request))
	if !jump.SameBook || jump.Row != 2 || jump.Col != 2 {
		t.Fatal("source coordinate navigation changed", jump)
	}
	request["row"] = 3
	if response := reconcilePost(t, a, "/api/relation/navigate", request); response.Code != 400 {
		t.Fatal("sparse absent row became a source record", response.Code)
	}
	request["row"], request["book"] = 2, refs[1].Book
	if response := reconcilePost(t, a, "/api/relation/navigate", request); response.Code != 409 {
		t.Fatal("navigation accepted the other side's identity", response.Code)
	}
	a.Close()
	if relationUsed(a) != 0 {
		t.Fatal("task history leaked retained sources/results", relationUsed(a))
	}
}

func TestRelationQueuedPreviewCancellationRetryAndParentClearKeepIndependentSource(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	release := holdRelationSlots(t, a.querySem)
	queued := relationStart(t, a, relationStartRequest{Mode: "preview", ParentID: root.ID})
	busy := relationStartRequest{Mode: "preview", ParentID: root.ID, WorkbookKey: a.currentWorkbook().Fingerprint, Book: sourceBookID(a.currentWorkbook())}
	if response := reconcilePost(t, a, "/api/relation/start", busy); response.Code != 429 {
		t.Fatal("active preview bound was bypassed", response.Code)
	}
	if response := reconcilePost(t, a, "/api/relation/cancel", map[string]string{"id": queued.ID}); response.Code != 200 {
		t.Fatal(response.Code)
	}
	waitRelation(t, a, queued, "cancelled")
	retry := relationStart(t, a, relationStartRequest{Mode: "preview", ParentID: root.ID, OutputMode: "inner"})
	clearRelation(t, a, root.ID)
	release()
	waitRelation(t, a, retry, "done")
	if result := relationResult(t, a, retry.ID); result.Preview.Total != 1 || result.Preview.Rows[0].Left.Number != 2 {
		t.Fatal("clearing parent cancelled an already borrowed mode preview", result)
	}
	clearRelation(t, a, retry.ID)
	clearRelation(t, a, queued.ID)
	if relationUsed(a) != 0 {
		t.Fatal("cancel/retry/clear leaked plan ownership", relationUsed(a))
	}
}

func TestRelationQueuedFullCSVSurvivesResultClearAndPreservesDefaultProtectionAndRawOption(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	wb := a.currentWorkbook()
	id := attachReconcileCSV(t, a, "formulas.csv", "编号,内容\n001,\"=SUM(1,2)\"\nright,右文\n")
	right := a.resolveReconcileBook(id)
	refs := [2]reconcileSourceRef{reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1), reconcileRef(t, a, id, "wb:"+right.Sheets[0].Name, 1)}
	query := relationServerQuery()
	query.LeftColumns = []int{1, 2, 6}
	root := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
	waitRelation(t, a, root, "done")
	release := holdRelationSlots(t, a.exportSem)
	var exports []string
	for _, raw := range []bool{false, true} {
		request := map[string]any{"jobId": root.ID, "mode": "left", "maximum": "100"}
		if raw {
			request["protectCSV"] = false
		}
		response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", request))
		exports = append(exports, response.JobID)
	}
	clearRelation(t, a, root.ID)
	if response := reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": id}); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if relationUsed(a) == 0 {
		t.Fatal("queued exports lost their source ownership")
	}
	release()
	for i, id := range exports {
		job := awaitReconcileExport(t, a, id)
		data, err := os.ReadFile(job.FilePath)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))).ReadAll()
		if err != nil || len(rows) != 3 {
			t.Fatal("complete CSV publication failed", len(rows), err)
		}
		columns := map[string]int{}
		for at, name := range rows[0] {
			columns[name] = at
		}
		var rightValue string
		for name, col := range columns {
			if strings.HasPrefix(name, "右 B ") {
				rightValue = rows[1][col]
			}
		}
		want := "=SUM(1,2)"
		if i == 0 {
			want = "'" + want
		}
		if rightValue != want || rows[1][columns["左源行"]] != "2" || rows[1][columns["左缺少缓存(JSON)"]] != "[false,false,true]" || rows[2][columns["右记录存在"]] != "false" {
			t.Fatal("CSV changed raw/protected values, source row or cache/absence state", rows)
		}
	}
	if relationUsed(a) != 0 {
		t.Fatal("last queued export did not release retained resources", relationUsed(a))
	}
}

func TestRelationFailedReplacementAndChangedSourceNeverPublishPartialExportOrDiscardOldResult(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	query := relationServerQuery()
	query.LeftColumns = []int{999}
	failed := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query, KeepID: root.ID})
	waitRelation(t, a, failed, "error")
	if result := relationResult(t, a, root.ID); result.Preview.Total != 2 {
		t.Fatal("failed replacement discarded old accepted result")
	}
	if response := reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": "1"}); response.Code != 413 {
		t.Fatal("inflated output was admitted past the chosen bound", response.Code)
	}
	right := root.Source.Value().inputs.Value().books[1]
	if err := os.WriteFile(right.Path, []byte("changed other source"), 0600); err != nil {
		t.Fatal(err)
	}
	if response := analysisRequest(t, a, "GET", "/api/relation/result?id="+root.ID, ""); response.Code != 409 {
		t.Fatal("changed source returned an apparently current result", response.Code)
	}
	out := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": "100"}))
	a.mu.RLock()
	export := a.exports[out.JobID]
	a.mu.RUnlock()
	select {
	case <-export.done:
	case <-time.After(5 * time.Second):
		t.Fatal("changed source export did not finish")
	}
	export.mu.RLock()
	status, file := export.Status, export.FilePath
	export.mu.RUnlock()
	if status != "failed" || file != "" {
		t.Fatal("failed export published a partial file", status, file)
	}
	clearRelation(t, a, root.ID)
	clearRelation(t, a, failed.ID)
	if relationUsed(a) != 0 {
		t.Fatal("source failure leaked retained plan or results")
	}
}

func TestRelationQueuedSwitchAndShutdownCancelWithoutWaitingForQuerySlots(t *testing.T) {
	for _, mode := range []string{"switch", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			a, refs := relationSnapshotFixture(t)
			release := holdRelationSlots(t, a.querySem)
			query := relationServerQuery()
			job := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
			finished := make(chan error, 1)
			if mode == "shutdown" {
				go func() { a.Close(); finished <- nil }()
			} else {
				path := filepath.Join(t.TempDir(), "new.csv")
				if err := os.WriteFile(path, []byte("字段\n新文件\n"), 0600); err != nil {
					t.Fatal(err)
				}
				go func() { finished <- a.OpenPath(path) }()
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lifecycle waited for a query slot while holding a source/cache lock")
			}
			release()
			waitRelation(t, a, job, "cancelled")
			if relationUsed(a) != 0 || relationPins(a) != 0 {
				t.Fatal("old queued task leaked sources after lifecycle change", relationUsed(a), relationPins(a))
			}
			if mode == "switch" && analysisRequest(t, a, "GET", "/api/relation/status?id="+job.ID, "").Code != 404 {
				t.Fatal("old task published into the new workbook")
			}
		})
	}
}

func TestRelationTaskCleanupReleasesSourceAndResultOutsideApplicationLock(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	var sourceOutside, resultOutside bool
	job := &relationTask{ID: randomID(), Primary: a.currentWorkbook(), Status: "done", ctx: ctx, cancel: cancel, done: done}
	job.Source = taskmanager.NewLease(relationSnapshotData{}, func() {
		sourceOutside = a.mu.TryLock()
		if sourceOutside {
			a.mu.Unlock()
		}
	})
	job.Result = taskmanager.NewLease(json.RawMessage(`{}`), func() {
		resultOutside = a.mu.TryLock()
		if resultOutside {
			a.mu.Unlock()
		}
	})
	a.mu.Lock()
	a.relationJobs[job.ID] = job
	a.mu.Unlock()
	clearRelation(t, a, job.ID)
	if !sourceOutside || !resultOutside {
		t.Fatal("source or encoded-result teardown occurred under App.mu")
	}
}

func TestRelationTaskRequestBoundsAndResultResponseLeaseSurvivesOwnerClear(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	for _, request := range []any{
		map[string]any{"mode": "preview", "parentId": root.ID, "outputMode": "invented", "workbookKey": a.currentWorkbook().Fingerprint, "book": sourceBookID(a.currentWorkbook())},
		map[string]any{"mode": "preview", "parentId": root.ID, "query": relationServerQuery(), "workbookKey": a.currentWorkbook().Fingerprint, "book": sourceBookID(a.currentWorkbook())},
		map[string]any{"mode": "preview", "parentId": root.ID, "workbookKey": "stale", "book": sourceBookID(a.currentWorkbook())},
	} {
		if response := reconcilePost(t, a, "/api/relation/start", request); response.Code == 200 {
			t.Fatal("invalid request admitted another task", request)
		}
	}
	if response := reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": 100}); response.Code != 400 {
		t.Fatal("numeric instead of exact string maximum accepted", response.Code)
	}
	response := &relationBlockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(response.resume) }) }
	t.Cleanup(unblock)
	finished := make(chan struct{})
	go func() {
		a.handleRelationResult(response, httptest.NewRequest("GET", "/api/relation/result?id="+root.ID, nil))
		close(finished)
	}()
	select {
	case <-response.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("result handler did not enter its response write")
	}
	if len(a.querySem) != 0 || !a.cacheMu.TryLock() {
		t.Fatal("slow result response retained a query slot/cache lock")
	}
	a.cacheMu.Unlock()
	before := relationUsed(a)
	clearRelation(t, a, root.ID)
	if relationUsed(a) <= 0 || relationUsed(a) >= before {
		t.Fatal("response body reservation was dropped with the task owner", relationUsed(a), before)
	}
	unblock()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted response could not finish after its owner cleared")
	}
	var result relationTaskResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Preview.Total != 2 {
		t.Fatal("retained response lost the accepted source result")
	}
	if relationUsed(a) != 0 || workbook.QueryFilesInUse(filepath.Join(a.currentWorkbook().CacheDir, "queries")) {
		t.Fatal("last response borrow leaked resources")
	}
}
