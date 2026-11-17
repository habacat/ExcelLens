package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestExportRoundTripXLSXAndZIP(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "original.xlsx")
	writeMultiSheetWorkbook(t, file)
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, file)
	defer a.Close()
	request := `{"scope":"all","format":"xlsx","mode":"formula","styles":true,"includeHidden":true}`
	start := featureCall(t, a, "POST", "/api/export/start", request)
	var initiated struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &initiated); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.exportJobs[initiated.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("missing job")
	}
	select {
	case <-job.done:
	case <-time.After(12 * time.Second):
		job.cancel()
		t.Fatal("export timed out")
	}
	job.mu.RLock()
	status, path, ticket := job.Status, job.FilePath, job.Ticket
	job.mu.RUnlock()
	if status != "done" {
		t.Fatalf("export failed: %s %s", status, job.Error)
	}
	out, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sheets) != 2 {
		t.Fatalf("wanted 2 sheets got %d", len(out.Sheets))
	}
	meta, _ := out.FindSheet("Second")
	idx, err := out.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := out.ReadRows(idx, 0, 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Cells[2].Formula; got != "='First'!$A$1+1" {
		t.Fatalf("cross-sheet formula missing: %q", got)
	}
	if got := rows[0].Cells[2].Raw; got != "2" {
		t.Fatalf("cached value changed: %q", got)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("original source was modified")
	}
	// The ticket permits native browser downloads without exposing the app token.
	download := httptest.NewRequest("GET", "/api/export/download?id="+initiated.JobID+"&ticket="+ticket, nil)
	download.RemoteAddr = "127.0.0.1:11111"
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, download)
	if response.Code != 200 || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download failed: %d %s", response.Code, response.Body.String())
	}
	invalid := httptest.NewRequest("GET", "/api/export/download?id="+initiated.JobID+"&ticket=invalid", nil)
	invalid.RemoteAddr = "127.0.0.1:11111"
	blocked := httptest.NewRecorder()
	a.Handler().ServeHTTP(blocked, invalid)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %d", blocked.Code)
	}
	// All-sheet ZIP produces one CSV per sheet and a machine-readable manifest.
	start = featureCall(t, a, "POST", "/api/export/start", `{"scope":"all","format":"zipcsv","mode":"values","bom":true,"protectCSV":true,"includeHidden":true}`)
	if err := json.Unmarshal(start.Body.Bytes(), &initiated); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job = a.exportJobs[initiated.JobID]
	a.mu.RUnlock()
	<-job.done
	job.mu.RLock()
	status, path = job.Status, job.FilePath
	job.mu.RUnlock()
	if status != "done" {
		t.Fatalf("zip failed: %s %s", status, job.Error)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 3 {
		t.Fatalf("want 2 CSV + manifest, got %d", len(z.File))
	}
}

func TestExportSelectionPositionsAndProfile(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "source.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	p := featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","start":"A1","end":"B1"}`)
	var summary struct {
		Total, Number, Formulas int64
		Sum                     string
	}
	if err := json.Unmarshal(p.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Total != 2 || summary.Number != 2 || summary.Formulas != 1 || summary.Sum != "3" {
		t.Fatalf("bad exact summary: %+v", summary)
	}
	response := featureCall(t, a, "POST", "/api/export/start", `{"scope":"selection","sheet":"First","start":"B1","end":"B1","format":"csv","mode":"values","bom":true,"protectCSV":true}`)
	var begun struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &begun); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.exportJobs[begun.JobID]
	a.mu.RUnlock()
	<-job.done
	job.mu.RLock()
	status, path := job.Status, job.FilePath
	job.mu.RUnlock()
	if status != "done" {
		t.Fatalf("export status: %s", status)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body[:3], []byte{0xef, 0xbb, 0xbf}) || !strings.Contains(string(body), "2") {
		t.Fatalf("bad CSV: %q", body)
	}
	invalid := httptest.NewRequest("POST", "/api/export/start", strings.NewReader(`{"scope":"selection","sheet":"First","start":"A1","end":"B1","format":"xlsx","mode":"formula"}`))
	invalid.RemoteAddr = "127.0.0.1:11111"
	invalid.Header.Set("X-ExcelLens-Token", a.token)
	invalidResult := httptest.NewRecorder()
	a.Handler().ServeHTTP(invalidResult, invalid)
	if invalidResult.Code != 400 {
		t.Fatalf("formula in remapped range not blocked: %d %s", invalidResult.Code, invalidResult.Body.String())
	}
}
