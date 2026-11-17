package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"excellens/internal/analysis"
	"excellens/internal/formats"
)

const snapshotRoute = "/api/analysis/snapshots"

func snapshotSaveRequest(a *App, job *analysisTask, revision int64) map[string]any {
	wb := a.currentWorkbook()
	return map[string]any{"op": "capture", "expectedRevision": revision, "name": "比较一", "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "jobId": job.ID, "metricIndex": 0}
}

func snapshotStoreForTest(t *testing.T) comparisonSnapshotStore {
	t.Helper()
	path, _ := savedConfigPath("snapshots", "comparisons")
	store, err := readComparisonSnapshots(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSnapshotHTTPStoresServerResultRestartsAndNeverRescans(t *testing.T) {
	isolateTestStorage(t)
	a, file, q := treeFixture(t)
	q.Layout = ""
	q.Metrics = []analysis.Metric{{Field: "C4", Aggregate: "sum"}, {Field: "C4", Aggregate: "average"}}
	job := completedRecipeAnalysis(t, a, q)
	before, _ := os.ReadFile(file)
	req := snapshotSaveRequest(a, job, 0)
	req["metricIndex"] = 1
	if out := reconcilePost(t, a, snapshotRoute, req); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	store := snapshotStoreForTest(t)
	item := store.Items[0]
	if store.Revision != 1 || item.MetricIndex != 1 || item.Result.Grand.Exact != job.Result.MetricGrand[1].Exact || item.WorkbookKey != a.currentWorkbook().Fingerprint || item.Book != sourceBookID(a.currentWorkbook()) {
		t.Fatal(store)
	}
	// Snapshot reading must not require any retained analysis job, source file,
	// or running workbook. Removing the fixture proves it is not rescanned.
	a.clearAnalysisTasks()
	a.Close()
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got := decodeResponse[analysis.ComparisonSnapshot](t, analysisRequest(t, b, "GET", snapshotRoute+"?id="+item.ID, ""))
	if got.Result.Grand.Exact != item.Result.Grand.Exact || got.SourceStamp != item.SourceStamp {
		t.Fatal(got)
	}
	response := analysisRequest(t, b, "GET", snapshotRoute+"/compare?left="+item.ID+"&right="+item.ID, "")
	comparison := decodeResponse[analysis.SnapshotComparison](t, response)
	if comparison.Grand.Difference.Exact != "0" {
		t.Fatal(comparison)
	}
	if out := reconcilePost(t, b, snapshotRoute, map[string]any{"op": "rename", "id": item.ID, "expectedRevision": 1, "name": "已重命名"}); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := reconcilePost(t, b, snapshotRoute, map[string]any{"op": "delete", "id": item.ID, "expectedRevision": 2}); out.Code != 200 || len(snapshotStoreForTest(t).Items) != 0 {
		t.Fatal(out.Code, out.Body.String())
	}
	if len(before) == 0 {
		t.Fatal("empty source fixture")
	}
}

func TestSnapshotCaptureRejectsIdentityIncompleteForgedAndOversizedResults(t *testing.T) {
	isolateTestStorage(t)
	a, file, q := treeFixture(t)
	job := completedRecipeAnalysis(t, a, q)
	before, _ := os.ReadFile(file)
	for _, update := range []func(map[string]any){
		func(r map[string]any) { r["book"] = "different-path" },
		func(r map[string]any) { r["workbookKey"] = "different-content" },
		func(r map[string]any) { r["metricIndex"] = 99 },
		func(r map[string]any) { r["result"] = map[string]any{"grand": 999} },
		func(r map[string]any) { delete(r, "expectedRevision") },
	} {
		req := snapshotSaveRequest(a, job, 0)
		update(req)
		if out := reconcilePost(t, a, snapshotRoute, req); out.Code < 400 {
			t.Fatal("invalid capture accepted", req, out.Body.String())
		}
	}
	job.mu.Lock()
	job.Result.Rows = make([]analysis.ResultRow, formats.SnapshotRows+1)
	job.mu.Unlock()
	if out := reconcilePost(t, a, snapshotRoute, snapshotSaveRequest(a, job, 0)); out.Code != 422 {
		t.Fatal(out.Code, out.Body.String())
	}
	if len(snapshotStoreForTest(t).Items) != 0 {
		t.Fatal("failed capture published a partial snapshot")
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("capturing a read-only snapshot altered the source")
	}
}

func TestSnapshotConcurrentTabsConflictAndDamagedStoreIsPreserved(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	job := completedRecipeAnalysis(t, a, q)
	request := snapshotSaveRequest(a, job, 0)
	codes := make(chan int, 6)
	var group sync.WaitGroup
	for range 6 {
		group.Go(func() { codes <- reconcilePost(t, a, snapshotRoute, request).Code })
	}
	group.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 200 {
			successes++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if successes != 1 || snapshotStoreForTest(t).Revision != 1 {
		t.Fatal("stale catalogs overwrote durable snapshots", successes)
	}
	path, _ := savedConfigPath("snapshots", "comparisons")
	data, _ := os.ReadFile(path)
	data[len(data)/2] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		body, _ := json.Marshal(snapshotSaveRequest(a, job, 1))
		if out := analysisRequest(t, a, method, snapshotRoute, string(body)); out.Code != 422 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(data, after) {
		t.Fatal("damaged evidence was replaced by an empty directory")
	}
}

func TestSnapshotEntryLimitPreservesPreviousConfirmedStore(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	job := completedRecipeAnalysis(t, a, q)
	if out := reconcilePost(t, a, snapshotRoute, snapshotSaveRequest(a, job, 0)); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	store := snapshotStoreForTest(t)
	for i := 1; i < formats.SnapshotEntries; i++ {
		item := store.Items[0]
		item.ID = fmt.Sprintf("%024d", i)
		store.Items = append(store.Items, item)
	}
	path, _ := savedConfigPath("snapshots", "comparisons")
	if err := writeSavedConfig(path, formats.SnapshotStoreBytes, store, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if out := reconcilePost(t, a, snapshotRoute, snapshotSaveRequest(a, job, 1)); out.Code != 422 || !strings.Contains(out.Body.String(), "上限") {
		t.Fatal(out.Code, out.Body.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("entry limit replaced previous confirmed snapshots")
	}
}
