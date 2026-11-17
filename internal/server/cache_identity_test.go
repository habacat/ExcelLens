package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenamedWorkbookReusesSearchAndViewCache(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "audit.xlsx")
	copyName := filepath.Join(dir, "audit-copy-renamed.xlsx")
	writeMultiSheetWorkbook(t, source)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyName, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, source)
	featureCall(t, a, "POST", "/api/view", `{"sheet":"First","rows":2,"cols":1}`)
	query := `{"sheet":"First","query":"A1+1","mode":"formula","scope":"all"}`
	res := featureCall(t, a, "POST", "/api/search/start", query)
	var started struct {
		JobID  string `json:"jobId"`
		Cached bool   `json:"cached"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.searchJobs[started.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("missing search job")
	}
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("search timed out")
	}
	cacheA := a.currentWorkbook().CacheDir
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.OpenPath(copyName); err != nil {
		t.Fatal(err)
	}
	if got := b.currentWorkbook().CacheDir; got != cacheA {
		t.Fatalf("cache dir changed after rename %q != %q", got, cacheA)
	}
	var freeze struct {
		Rows   int    `json:"rows"`
		Cols   int    `json:"cols"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/view?sheet=First", "").Body.Bytes(), &freeze); err != nil {
		t.Fatal(err)
	}
	if freeze.Rows != 2 || freeze.Cols != 1 || freeze.Source != "saved" {
		t.Fatalf("view lost across rename: %+v", freeze)
	}
	res = featureCall(t, b, "POST", "/api/search/start", query)
	var restart struct {
		JobID  string `json:"jobId"`
		Cached bool   `json:"cached"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &restart); err != nil {
		t.Fatal(err)
	}
	if !restart.Cached {
		t.Fatalf("search cache did not survive rename: %s", res.Body.String())
	}
}

func TestUploadCopiesOnlyManuallyRemoved(t *testing.T) {
	isolateTestStorage(t)
	original := filepath.Join(t.TempDir(), "document.xlsx")
	writeMultiSheetWorkbook(t, original)
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := persistUploadedFile(io.NopCloser(bytes.NewReader(data)), ".xlsx", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(uploaded); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if _, err := os.Stat(uploaded); err != nil {
		t.Fatalf("upload was removed automatically: %v", err)
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(uploaded); !os.IsNotExist(err) {
		t.Fatalf("upload was not cleared on explicit request: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original source was changed or deleted: %v", err)
	}
}

func TestCacheCleanupProtectsCurrentWorkbook(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "protected.xlsx")
	writeMultiSheetWorkbook(t, path)
	a := prepareFeatureApp(t, path)
	defer a.Close()
	active := a.currentWorkbook().CacheDir
	old := filepath.Join(CacheDir(), "old-workbook")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "dummy"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active cache was deleted: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("unused cache survived: %v", err)
	}
}

// Build fixture workbooks with ordinary ZIP entries in tests only.
func writeZipArchive(t *testing.T, dest string, parts map[string]string) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range parts {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
