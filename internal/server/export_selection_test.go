package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectionExportMatchesFilteredCopyAndFrozenOrder(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "selection.xlsx")
	writeClipboardTestWorkbook(t, file)
	before, _ := os.ReadFile(file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("Data")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.filterResults["f"] = &FilterResult{SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, wb.GetCachedIndex(meta.Path).RowCount(), 3, 0, 1)}
	for _, tc := range []struct {
		start, end string
		v1, v2     int
		want       string
	}{
		{"A4", "B2", 1, 2, "gamma,4\r\nalpha,2\r\n"},
		{"A2", "B4", 2, 1, "gamma,4\r\nalpha,2\r\n"},
		{"A2", "B1", 2, 1, "header,1\r\ngamma,4\r\nalpha,2\r\n"},
	} {
		body := fmt.Sprintf(`{"scope":"selection","sheet":"Data","start":%q,"end":%q,"filterId":"f","visualStart":%d,"visualEnd":%d,"excludeThroughRow":1,"format":"csv","mode":"values"}`, tc.start, tc.end, tc.v1, tc.v2)
		response := featureCall(t, a, "POST", "/api/export/start", body)
		var started struct {
			JobID string `json:"jobId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
			t.Fatal(err)
		}
		a.mu.RLock()
		job := a.exportJobs[started.JobID]
		a.mu.RUnlock()
		if job == nil {
			t.Fatal(response.Body.String())
		}
		select {
		case <-job.done:
		case <-time.After(5 * time.Second):
			t.Fatal("export timeout")
		}
		if job.Status != "done" {
			t.Fatal(job.Error)
		}
		got, err := os.ReadFile(job.FilePath)
		if err != nil || string(got) != tc.want {
			t.Fatalf("exported hidden rows or changed visual order: %q %v", got, err)
		}
		copy := featureCall(t, a, "GET", fmt.Sprintf("/api/range?sheet=Data&start=%s&end=%s&filterId=f&visualStart=%d&visualEnd=%d&excludeThroughRow=1", tc.start, tc.end, tc.v1, tc.v2), "")
		if strings.ReplaceAll(strings.ReplaceAll(tc.want, ",", "\t"), "\r\n", "\n") != copy.Body.String()+"\n" {
			t.Fatalf("copy/export disagree: %q / %q", copy.Body.String(), got)
		}
	}
	invalid := httptest.NewRequest("POST", "/api/export/start", strings.NewReader(`{"scope":"selection","sheet":"Data","start":"A4","end":"B2","filterId":"f","format":"csv","mode":"values"}`))
	invalid.RemoteAddr = "127.0.0.1:1234"
	invalid.Header.Set("X-ExcelLens-Token", a.token)
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, invalid)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("accepted stale/missing visual coordinates: %d %s", out.Code, out.Body.String())
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
}
