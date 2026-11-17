package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchHistoryAndCachedQueryWithoutSheetIndex(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "multi.xlsx")
	writeMultiSheetWorkbook(t, path)
	app := prepareFeatureApp(t, path)
	body := `{"sheet":"First","query":"A1+1","mode":"formula","scope":"current"}`
	var started struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(featureCall(t, app, "POST", "/api/search/start", body).Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	job := app.searchJobs[started.JobID]
	app.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("search timeout")
	}
	history := featureCall(t, app, "GET", "/api/search/history", "")
	var list struct {
		Items []SearchHistoryItem `json:"items"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Query != "A1+1" || list.Items[0].Hits != 1 {
		t.Fatalf("history=%+v", list.Items)
	}
	wb := app.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	cached := wb.GetCachedIndex(meta.Path)
	xml, index := cached.XMLPath, cached.IndexPath
	app.Close()
	if err := os.Remove(xml); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	next, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if err = next.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	// Different query case must reuse one canonical cached result.
	again := featureCall(t, next, "POST", "/api/search/start", `{"sheet":"First","query":"a1+1","mode":"formula","scope":"current"}`)
	var reused struct {
		Cached bool   `json:"cached"`
		JobID  string `json:"jobId"`
	}
	if err := json.Unmarshal(again.Body.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if !reused.Cached || reused.JobID == "" {
		t.Fatalf("expected instant disk cache, got %s", again.Body.String())
	}
}
