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
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/reconcile"
)

// Optional real adapter/server/export probe. Inputs are generated in a private
// validation directory; no user workbook or running desktop app is involved.
func TestReconcileScaleRealSources(t *testing.T) {
	output := os.Getenv("EXCELLENS_RECONCILE_PROBE")
	if output == "" {
		t.Skip("set EXCELLENS_RECONCILE_PROBE to an isolated generated-data directory")
	}
	count := 300_001
	if raw := os.Getenv("EXCELLENS_RECONCILE_ROWS"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 300_001 || count > 5_000_001 {
			t.Fatal("probe requires 300001 through 5000001 generated records per side")
		}
	}
	output, err := filepath.Abs(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	isolateTestStorage(t)
	key := func(i int) string {
		return fmt.Sprintf("%016d_来源编号保持文本_核对_abcdefghijklmnopqrstuvwxyz", i)
	}
	files := [2]string{filepath.Join(output, "generated-left.csv"), filepath.Join(output, "generated-right.csv")}
	for side, file := range files {
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		bw := bufio.NewWriterSize(f, 1<<20)
		_, err = bw.WriteString("编号,金额\n")
		for pos := 0; pos < count && err == nil; pos++ {
			i := pos
			if side == 1 {
				i = count - pos - 1
			}
			amount := "9007199254740993.31"
			if side == 1 && i%97 == 0 {
				amount = "9007199254740993.30"
			}
			_, err = fmt.Fprintf(bw, "%s,%s\n", key(i), amount)
		}
		if err == nil {
			_, err = fmt.Fprintf(bw, "ONLY%d,1\nDUP,1\nDUP,2\n,3\n", side)
		}
		if side == 1 && err == nil {
			_, err = bw.WriteString("DUP,3\n")
		}
		if err == nil {
			err = bw.Flush()
		}
		closed := f.Close()
		if err != nil || closed != nil {
			t.Fatal(err, closed)
		}
	}
	digest := func(file string) string {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	before := [2]string{digest(files[0]), digest(files[1])}
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(files[0]); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	// A real second browser source avoids copying a large multipart body into
	// the test heap; source discovery is shared with attached uploads.
	_, err = a.OpenPathForLaunch(files[1])
	if err != nil {
		t.Fatal(err)
	}
	var otherID string
	for _, book := range a.referencedWorkbooks() {
		if book.Path == files[1] {
			otherID = sourceBookID(book)
		}
	}
	rightBook := a.resolveReconcileBook(otherID)
	if rightBook == nil {
		t.Fatal("second source missing")
	}
	left := reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
	right := reconcileRef(t, a, otherID, "wb:"+rightBook.Sheets[0].Name, 1)
	query := reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}, Fields: []reconcile.Pair{{Left: 2, Right: 2, Mode: "number"}}, IncludeEqual: true}
	request := map[string]any{"workbookKey": wb.Fingerprint, "primaryBook": sourceBookID(wb), "left": left, "right": right, "query": query}
	start := func() *reconcileTask {
		result := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/start", request))
		return a.getReconcileTask(result.JobID)
	}
	queries := filepath.Join(wb.CacheDir, "queries")
	usage := func() (bytes int64, count int) {
		entries, _ := os.ReadDir(queries)
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && !info.IsDir() {
				bytes += info.Size()
				count++
			}
		}
		return
	}
	type measurement struct {
		Phase      string `json:"phase"`
		ElapsedMS  int64  `json:"elapsedMs"`
		PeakHeap   uint64 `json:"sampledPeakGoHeapBytes"`
		Allocated  uint64 `json:"allocatedBytes"`
		QueryBytes int64  `json:"sampledPeakQueryBytes"`
		QueryFiles int    `json:"sampledPeakQueryFiles"`
	}
	var measures []measurement
	measure := func(phase string, work func() <-chan struct{}) {
		runtime.GC()
		var initial, final runtime.MemStats
		runtime.ReadMemStats(&initial)
		m := measurement{Phase: phase, PeakHeap: initial.HeapAlloc}
		start := time.Now()
		done := work()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		timer := time.NewTimer(4 * time.Minute)
		defer timer.Stop()
		waiting := true
		for waiting {
			select {
			case <-done:
				waiting = false
			case <-ticker.C:
				runtime.ReadMemStats(&final)
				m.PeakHeap = max(m.PeakHeap, final.HeapAlloc)
				bytes, files := usage()
				m.QueryBytes, m.QueryFiles = max(m.QueryBytes, bytes), max(m.QueryFiles, files)
			case <-timer.C:
				t.Fatal("probe phase timed out", phase)
			}
		}
		runtime.ReadMemStats(&final)
		m.ElapsedMS, m.Allocated = time.Since(start).Milliseconds(), final.TotalAlloc-initial.TotalAlloc
		m.PeakHeap = max(m.PeakHeap, final.HeapAlloc)
		if bytes, files := usage(); bytes != 0 || files != 0 {
			t.Fatal("finished phase retained query runs", phase, bytes, files)
		}
		if m.QueryFiles < 1 {
			t.Fatal("probe did not observe real disk spilling", phase)
		}
		measures = append(measures, m)
		t.Logf("%s: %d ms; sampled heap %d bytes; query runs %d bytes/%d files", phase, m.ElapsedMS, m.PeakHeap, m.QueryBytes, m.QueryFiles)
	}
	var job *reconcileTask
	measure("compare", func() <-chan struct{} { job = start(); return job.done })
	differences := int64((count + 96) / 97)
	want := reconcile.Counts{Equal: int64(count) - differences, Different: differences, LeftOnly: 1, RightOnly: 1, Duplicate: 1, InvalidKeys: 2, LeftRecords: int64(count + 4), RightRecords: int64(count + 5)}
	if job.Status != "done" || job.Result.Counts != want {
		t.Fatal(job.Status, job.Error, job.Result.Counts, want)
	}
	var exportJob *ExportJob
	measure("full-csv", func() <-chan struct{} {
		started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/export", map[string]any{"jobId": job.ID, "protectCSV": false}))
		a.mu.RLock()
		exportJob = a.exports[started.JobID]
		a.mu.RUnlock()
		return exportJob.done
	})
	if exportJob.Status != "done" {
		t.Fatal("full export failed", exportJob.Error)
	}
	f, err := os.Open(exportJob.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	if _, err := reader.Read(); err != nil {
		t.Fatal(err)
	}
	seen := make([]byte, (count+7)/8)
	normal, members, summaries, special := 0, 0, 0, 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) != 28 {
			t.Fatal("invalid complete CSV", row, err)
		}
		if row[0] == "重复键记录" {
			members++
			continue
		}
		if row[0] == "主键汇总" {
			summaries++
			continue
		}
		var keys []string
		if err := json.Unmarshal([]byte(row[4]), &keys); err != nil || len(keys) != 1 {
			t.Fatal("key provenance lost", row[4], err)
		}
		if len(keys[0]) < 17 || keys[0][16] != '_' {
			special++
			continue
		}
		i, err := strconv.Atoi(keys[0][:16])
		if err != nil || i < 0 || i >= count || keys[0] != key(i) || seen[i/8]&(1<<uint(i%8)) != 0 {
			t.Fatal("key lost, repeated or changed", keys[0], err)
		}
		seen[i/8] |= 1 << uint(i%8)
		delta, amount, status := "0", "9007199254740993.31", "一致"
		if i%97 == 0 {
			delta, amount, status = "0.01", "9007199254740993.30", "差异"
		}
		if row[1] != status || row[13] != "9007199254740993.31" || row[22] != amount || row[26] != delta || row[9] != strconv.Itoa(i+2) || row[18] != strconv.Itoa(count-i+1) {
			t.Fatal("value, exact difference, status or source coordinate mismatch", i, row)
		}
		normal++
	}
	if normal != count || members != 5 || summaries != 1 || special != 4 {
		t.Fatal("full export omitted records or expanded duplicates", normal, members, summaries, special)
	}
	// Cancel only after a real sort run exists, so this covers unwind of
	// on-disk state and both suspended iterators rather than queued cancellation.
	cancelled := start()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, files := usage(); files > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation probe never reached a disk sort run")
		}
		select {
		case <-cancelled.done:
			t.Fatal("cancellation probe finished before disk-run interception")
		case <-time.After(5 * time.Millisecond):
		}
	}
	reconcilePost(t, a, "/api/reconcile/cancel", map[string]string{"id": cancelled.ID})
	select {
	case <-cancelled.done:
	case <-time.After(20 * time.Second):
		t.Fatal("active spill cancellation did not finish")
	}
	if bytes, files := usage(); cancelled.Status != "cancelled" || bytes != 0 || files != 0 {
		t.Fatal("cancelled sort retained runs or published a result", cancelled.Status, bytes, files)
	}
	after := [2]string{digest(files[0]), digest(files[1])}
	if before != after {
		t.Fatal("comparison modified source bytes")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedRecordsPerSide": count, "counts": want, "measurements": measures, "fullCSVDataRowsVerified": normal + members + summaries + special, "sourceSHA256": before, "sourceUnchanged": true, "cancelledAfterDiskSpill": true, "queryRunsAfterCompletion": 0, "desktopManualAcceptance": false, "limits": "synthetic Windows CSV/server probe; not a billion-row benchmark or manual desktop acceptance"}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	path := os.Getenv("EXCELLENS_RECONCILE_REPORT")
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(output, "report.json")
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
