package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func prepareIssueAnalysis(t *testing.T) (*App, string, string, analysis.Result) {
	t.Helper()
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "source.xlsx")
	writeAnalysisWorkbook(t, path)
	a, _ := New()
	t.Cleanup(a.Close)
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	created := decodeResponse[struct{ ID string }](t, analysisRequest(t, a, "POST", "/api/analysis/sheet/create", `{"name":"问题来源","columns":["金额"]}`))
	values := []string{"0001", "=1+2", "中文,\"原文\"\n下一行"}
	changes := make([]analysis.CellChange, 121)
	for i := range changes {
		changes[i] = analysis.CellChange{Row: i, Col: 0, Value: values[i%len(values)]}
	}
	body, _ := json.Marshal(map[string]any{"id": created.ID, "workbookKey": a.currentWorkbook().Fingerprint, "expectedRevision": 0, "changes": changes})
	decodeResponse[map[string]any](t, analysisRequest(t, a, "POST", "/api/analysis/sheet/batch", string(body)))
	q, _ := json.Marshal(analysis.Query{Source: "scratch:" + created.ID, Value: "C1", Aggregate: "sum"})
	started := decodeResponse[struct{ JobID string }](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", string(q)))
	result := awaitAnalysis(t, a, started.JobID)
	return a, created.ID, started.JobID, result
}

func TestIssuesMaterializationAndCompleteCSVUseSameSnapshotAndRawText(t *testing.T) {
	a, source, id, result := prepareIssueAnalysis(t)
	if result.InvalidNumbers != 121 || len(result.Issues) != 100 || !result.IssuesTruncated {
		t.Fatal(result.InvalidNumbers, len(result.Issues))
	}
	before, _ := os.ReadFile(a.currentWorkbook().Path)
	materialized := decodeResponse[struct {
		ID        string
		Rows      int
		Total     int
		Truncated bool
	}](t, analysisRequest(t, a, "POST", "/api/analysis/issues/materialize", fmt.Sprintf(`{"jobId":%q,"name":"核对清单","limit":2}`, id)))
	if materialized.Rows != 2 || materialized.Total != 121 || !materialized.Truncated {
		t.Fatal(materialized)
	}
	sheet := decodeResponse[struct{ Rows [][]string }](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+materialized.ID, ""))
	if len(sheet.Rows) != 2 || sheet.Rows[0][4] != "1" || sheet.Rows[0][9] != "0001" || sheet.Rows[1][9] != "=1+2" {
		t.Fatal(sheet.Rows)
	}
	for _, protect := range []bool{false, true} {
		started := decodeResponse[struct{ JobID string }](t, analysisRequest(t, a, "POST", "/api/analysis/issues/export", fmt.Sprintf(`{"jobId":%q,"protectCSV":%t}`, id, protect)))
		a.mu.RLock()
		job := a.exportJobs[started.JobID]
		a.mu.RUnlock()
		select {
		case <-job.done:
		case <-time.After(12 * time.Second):
			job.cancel()
			t.Fatal("issue export timed out")
		}
		job.mu.RLock()
		status, path, message := job.Status, job.FilePath, job.Error
		job.mu.RUnlock()
		if status != "done" {
			t.Fatalf("export failed: %s %s", status, message)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
			t.Fatal("missing CSV BOM")
		}
		rows, err := csv.NewReader(bytes.NewReader(data[3:])).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 122 || rows[1][9] != "0001" || rows[3][9] != "中文,\"原文\"\n下一行" || rows[121][4] != "121" || rows[1][2] != source {
			t.Fatal("complete CSV lost provenance, rows, numbering or original text", len(rows), rows[3])
		}
		want := "=1+2"
		if protect {
			want = "'=1+2"
		}
		if rows[2][9] != want || strings.Contains(rows[0][9], "文本保护") != protect {
			t.Fatal("CSV protection setting ignored", rows[0], rows[2])
		}
	}
	after, _ := os.ReadFile(a.currentWorkbook().Path)
	if !bytes.Equal(before, after) {
		t.Fatal("source workbook was modified")
	}
}

func TestIssuesRejectModifiedSourceEvenWhenRevisionIsRestoredToSameNumber(t *testing.T) {
	a, source, id, _ := prepareIssueAnalysis(t)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error { s := ws.Sheet(source); s.Rows[0][0] = "999"; return nil }); err != nil {
		t.Fatal(err)
	}
	// Backup import may restore a numeric revision used by another snapshot.
	// Its content stamp, rather than the numeric revision alone, must reject it.
	response := analysisRequest(t, a, "POST", "/api/analysis/issues/materialize", fmt.Sprintf(`{"jobId":%q,"name":"旧来源","limit":100}`, id))
	if response.Code != 409 || !strings.Contains(response.Body.String(), "来源已修改") {
		t.Fatal(response.Code, response.Body.String())
	}
	started := decodeResponse[struct{ JobID string }](t, analysisRequest(t, a, "POST", "/api/analysis/issues/export", fmt.Sprintf(`{"jobId":%q}`, id)))
	a.mu.RLock()
	job := a.exportJobs[started.JobID]
	a.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(12 * time.Second):
		job.cancel()
		t.Fatal("export timed out")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != "failed" || job.FilePath != "" || !strings.Contains(job.Error, "来源已修改") {
		t.Fatal("stale source exported", job.Status, job.Error)
	}
}
