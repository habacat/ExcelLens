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
	"runtime"
	"testing"
	"time"

	"excellens/internal/buildinfo"
)

func TestVisibleRowExportMillionSourceRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_ROW_STREAM_SCALE") != "1" {
		t.Skip("optional generated-million-record export check")
	}
	isolateTestStorage(t)
	const count = 1_000_000
	file := filepath.Join(t.TempDir(), "million.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := io.WriteString(w, "ID,Amount\r\n"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		if _, err := fmt.Fprintf(w, "R%07d,9007199254740993.31\r\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func(path string) string {
		r, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	before := hash(file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: meta.Name, Change: &rowViewChange{Action: "range", First: 2, Last: count/2 + 1, Hidden: true}}), "done")
	req := exportRequest{Scope: "visible", Sheet: meta.Name, ViewID: id, Format: "csv", Mode: "values"}
	var pre, post runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&pre)
	probe, err := a.captureExportRows(wb, req)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&post)
	allocation := post.TotalAlloc - pre.TotalAlloc
	if probe.plan == nil || probe.plan.Len() != count/2+1 || allocation > 1<<20 {
		t.Fatal("large selection expanded its row-number vector", allocation)
	}
	probe.close()
	started := time.Now()
	job := startRowExport(t, a, req)
	waitRowExport(t, job, "done")
	duration := time.Since(started)
	r, err := os.Open(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	reader := csv.NewReader(bufio.NewReaderSize(r, 1<<20))
	header, err := reader.Read()
	if err != nil || len(header) != 2 || header[0] != "ID" || header[1] != "Amount" {
		t.Fatal(header, err)
	}
	verified := 0
	for i := count/2 + 1; i <= count; i++ {
		record, err := reader.Read()
		if err != nil || len(record) != 2 || record[0] != fmt.Sprintf("R%07d", i) || record[1] != "9007199254740993.31" {
			t.Fatal("visible export changed/skipped a record", i, record, err)
		}
		verified++
	}
	if _, err := reader.Read(); err != io.EOF {
		t.Fatal("export had extra hidden records or a truncated ending", err)
	}
	if after := hash(file); after != before {
		t.Fatal("source file changed")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRecords": count, "headerRows": 1, "hiddenSourceRecords": count / 2, "verifiedExportRecords": verified, "exportRowsIncludingHeader": job.TotalRows, "selectionCaptureAllocatedBytes": allocation, "sourceSHA256": before, "sourceUnchanged": true, "exportDurationObservedMs": duration.Milliseconds(), "performanceComparison": false, "desktopManualAcceptance": false}
	if target := os.Getenv("EXCELLENS_ROW_STREAM_SCALE_REPORT"); target != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	encoded, _ := json.Marshal(report)
	t.Log(string(encoded))
}
