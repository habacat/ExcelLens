package server

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestTXTTableHeaderStatusManualHeaderAndFilterCoordinates(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "ledger.txt")
	original := []byte("账号@金额@摘要\n00001@1.2500001@收款\n00002@2.50@付款\n")
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Source workbook.SourceInfo `json:"source"`
	}
	if err := json.Unmarshal(featureCall(t, a, "GET", "/api/status", "").Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if h := status.Source.TableHeader; h == nil || h.Row != 1 || !h.Detected || !reflect.DeepEqual(h.Columns, []string{"账号", "金额", "摘要"}) {
		t.Fatal("header not sent to client", h)
	}
	wb := a.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var manual workbook.TextTableHeader
	if err := json.Unmarshal(featureCall(t, a, "GET", "/api/table/header?sheet=%E6%95%B0%E6%8D%AE&row=2", "").Body.Bytes(), &manual); err != nil {
		t.Fatal(err)
	}
	if manual.Row != 2 || manual.Detected || !reflect.DeepEqual(manual.Columns, []string{"00001", "1.2500001", "收款"}) {
		t.Fatal("manual label changed raw record", manual)
	}
	for _, row := range []string{"0", "-1", "4"} {
		req := httptest.NewRequest("GET", "/api/table/header?sheet=%E6%95%B0%E6%8D%AE&row="+row, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-ExcelLens-Token", a.token)
		res := httptest.NewRecorder()
		a.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatal("invalid row accepted", row, res.Code)
		}
	}
	start := featureCall(t, a, "POST", "/api/filter/start", `{"sheet":"数据","headerRow":1,"rules":[{"column":1,"operator":"in","values":["00002"]}]}`)
	var begun struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &begun); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.filterJobs[begun.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("filter missing")
	}
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("filter timed out")
	}
	job.mu.RLock()
	resultID, jobStatus, jobError := job.ResultID, job.Status, job.Error
	job.mu.RUnlock()
	if jobStatus != "done" {
		t.Fatal(jobStatus, jobError)
	}
	a.mu.RLock()
	result := a.filterResults[resultID]
	a.mu.RUnlock()
	if result == nil || !reflect.DeepEqual(slices.Collect(result.Rows.Iter()), []int{0, 2}) {
		t.Fatal("header lost or record addresses shifted", result)
	}
	if idx.MaxRow != 3 || wb.GetCachedIndex(wb.Sheets[0].Path) != idx {
		t.Fatal("header rebuilt or truncated source")
	}
	bytes, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(bytes) != sha256.Sum256(original) {
		t.Fatal("source modified", err)
	}
	for _, asset := range []string{"virtualscroll.js", "tableheaders.js"} {
		featureCall(t, a, "GET", "/assets/"+asset, "")
	}
}
