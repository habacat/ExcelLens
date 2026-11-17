package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/clustering"
)

// Real CSV adapter, view capture, task API and exported source values. The
// oracle counts generated raw strings independently of candidate key logic.
func TestClusterMillionActualCSVIndependentRawCounts(t *testing.T) {
	if os.Getenv("EXCELLENS_CLUSTER_SCALE") != "1" {
		t.Skip("optional actual million-record CSV clustering probe")
	}
	isolateTestStorage(t)
	const rows = 1_000_000
	file := filepath.Join(t.TempDir(), "million-clusters.csv")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(out, 1<<20)
	if _, err := writer.WriteString("机构,编号\r\n"); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]int64)
	for i := 0; i < rows; i++ {
		id := i / 10000
		raw := fmt.Sprintf("机构%03d（上海）", id)
		switch i % 4 {
		case 1:
			raw = fmt.Sprintf(" 机构%03d(上海) ", id)
		case 2:
			raw = fmt.Sprintf("机构%03d （上海）", id)
		case 3:
			raw = fmt.Sprintf("机构%03d(上海)", id)
		}
		if _, err := fmt.Fprintf(writer, "%s,000%010d\r\n", raw, i); err != nil {
			t.Fatal(err)
		}
		if i >= rows/2 {
			want[raw]++
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
		input, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		sum := sha256.New()
		if _, err := io.Copy(sum, input); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sum.Sum(nil))
	}
	before := hash()
	a := openTreeTestApp(t, file)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: wb.Sheets[0].Name, Change: &rowViewChange{Action: "range", First: 2, Last: rows/2 + 1, Hidden: true}}), "done")
	source := "wb:" + wb.Sheets[0].Name
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Source: source, Mode: "visible", ViewID: view})
	req := clusterStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: source, SourceID: captured.ID, HeaderRow: 1, Field: "C1", Options: clustering.Options{FoldWidth: true, Space: "remove", IgnorePunctuation: true}}
	started := time.Now()
	id := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/clusters/start", req)).JobID
	a.mu.RLock()
	job := a.clusterJobs[id]
	a.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(180 * time.Second):
		job.cancel()
		t.Fatal("actual million candidate task timed out")
	}
	if job.Status != "done" || job.Snapshot == nil {
		t.Fatal(job.Status, job.Error)
	}
	result := job.Snapshot.Result
	if result.Counts.Scanned != rows/2 || result.Counts.Eligible != rows/2 || result.Counts.SimilarGroups != 50 || result.Counts.ExactGroups != 0 || len(result.Groups) != 50 {
		t.Fatal(result.Counts, len(result.Groups))
	}
	taskMS := time.Since(started).Milliseconds()
	checked := 0
	for _, group := range result.Groups {
		if group.Count != 10000 || len(group.Variants) != 4 || group.Kind != "similar" {
			t.Fatal(group)
		}
		for _, variant := range group.Variants {
			if want[variant.Raw] != variant.Count || variant.Type != "inlineStr" || len(variant.Rows) != 8 {
				t.Fatal("independent raw oracle mismatch", variant, want[variant.Raw])
			}
			for _, row := range variant.Rows {
				if row <= rows/2+1 || row > rows+1 {
					t.Fatal("sample escaped captured visible range", row)
				}
			}
			delete(want, variant.Raw)
			checked++
		}
	}
	if len(want) != 0 || checked != 200 {
		t.Fatal("missing raw variants", len(want), checked)
	}
	exportID := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/clusters/export", map[string]any{"jobId": job.ID, "protectCSV": false})).JobID
	exported := awaitReconcileExport(t, a, exportID)
	f, err := os.Open(exported.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(data) != 201 {
		t.Fatal(len(data), err)
	}
	var total int64
	for _, row := range data[1:] {
		n, err := strconv.ParseInt(row[9], 10, 64)
		if err != nil || n != 2500 || strings.TrimSpace(row[7]) == "" || row[8] != "inlineStr" {
			t.Fatal(row, err)
		}
		total += n
	}
	if total != rows/2 || hash() != before {
		t.Fatal("export coverage or source content changed", total)
	}
	receipt := map[string]any{"result": "PASS", "version": buildinfo.Version, "actualCSVRecords": rows, "visibleScanned": result.Counts.Scanned, "candidateGroups": len(result.Groups),
		"independentRawVariants": checked, "sampleRowsPerVariant": 8, "allCountsVerified": true, "exportRows": 200, "exportCountsVerified": true, "sourceSHA256": before, "sourceUnchanged": true, "warmTaskMs": taskMS,
		"budgetChargedBytes": result.BudgetBytes(), "desktopManualAcceptance": false}
	encoded, _ := json.MarshalIndent(receipt, "", "  ")
	if path := os.Getenv("EXCELLENS_CLUSTER_RECEIPT"); path != "" {
		if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}
