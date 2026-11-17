package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/buildinfo"
)

// Optional real CSV/index/server probe. This is a generated, local scale
// observation, not an Office/WPS round trip or a universal speed comparison.
func TestRecipeScaleRealSources(t *testing.T) {
	output := os.Getenv("EXCELLENS_RECIPE_PROBE")
	if output == "" {
		t.Skip("set EXCELLENS_RECIPE_PROBE to an isolated generated-data directory")
	}
	count := 1_000_001
	if raw := os.Getenv("EXCELLENS_RECIPE_ROWS"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 300_001 || count > 5_000_001 {
			t.Fatal("probe needs 300001 through 5000001 records")
		}
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	isolateTestStorage(t)
	files := [2]string{filepath.Join(output, "generated-recipe-source.csv"), filepath.Join(output, "generated-recipe-target.csv")}
	for side, file := range files {
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		writer := bufio.NewWriterSize(f, 1<<20)
		header := "机构,金额,编号,标记\n"
		if side == 1 {
			header = "编号,标记,金额,机构\n"
		}
		_, err = writer.WriteString(header)
		for pos := 0; pos < count && err == nil; pos++ {
			i := pos
			if side == 1 {
				i = count - pos - 1
			}
			amount, flag := "9007199254740993.31", "纳入"
			if i%2 == 0 {
				amount = "9007199254740993.30"
			}
			if i%7 == 0 {
				flag = "剔除"
			}
			if side == 0 {
				_, err = fmt.Fprintf(writer, "机构%03d,%s,%012d,%s\n", i%97, amount, i, flag)
			} else {
				_, err = fmt.Fprintf(writer, "%012d,%s,%s,机构%03d\n", i, flag, amount, i%97)
			}
		}
		if err == nil {
			err = writer.Flush()
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
		sum := sha256.New()
		if _, err = io.Copy(sum, f); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sum.Sum(nil))
	}
	before := [2]string{digest(files[0]), digest(files[1])}
	type measurement struct {
		Phase     string `json:"phase"`
		ElapsedMS int64  `json:"elapsedMs"`
		PeakHeap  uint64 `json:"sampledPeakGoHeapBytes"`
		Allocated uint64 `json:"allocatedBytes"`
	}
	var measures []measurement
	measure := func(phase string, start func() <-chan struct{}) {
		runtime.GC()
		var initial, mem runtime.MemStats
		runtime.ReadMemStats(&initial)
		m := measurement{Phase: phase, PeakHeap: initial.HeapAlloc}
		began := time.Now()
		done := start()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.NewTimer(3 * time.Minute)
		defer timeout.Stop()
		waiting := true
		for waiting {
			select {
			case <-done:
				waiting = false
			case <-ticker.C:
				runtime.ReadMemStats(&mem)
				m.PeakHeap = max(m.PeakHeap, mem.HeapAlloc)
			case <-timeout.C:
				t.Fatal("recipe scale timeout", phase)
			}
		}
		runtime.ReadMemStats(&mem)
		m.PeakHeap = max(m.PeakHeap, mem.HeapAlloc)
		m.ElapsedMS = time.Since(began).Milliseconds()
		m.Allocated = mem.TotalAlloc - initial.TotalAlloc
		measures = append(measures, m)
		t.Logf("%s: %d ms; sampled Go heap %d bytes", phase, m.ElapsedMS, m.PeakHeap)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(files[0]); err != nil {
		t.Fatal(err)
	}
	indexStart := time.Now()
	wb := a.currentWorkbook()
	reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
	sourceIndexMS := time.Since(indexStart).Milliseconds()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Metrics: []analysis.Metric{{Field: "C2", Aggregate: "sum"}, {Field: "D_scaled", Aggregate: "sum"}, {Field: "C3", Aggregate: "count"}}, Transforms: []analysis.Transform{{ID: "D_scaled", Name: "十分之一", Source: "C2", Op: "divide", Arg: "10"}}, Filters: []analysis.Filter{{Field: "C4", Op: "eq", Value: "纳入"}, {Field: "C3", Op: "startswith", Value: "0"}}, Sort: "label_asc", Precision: 3}
	var original *analysisTask
	measure("ordinary-analysis", func() <-chan struct{} {
		original, err = a.startAnalysisTask(wb, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		return original.done
	})
	if original.Status != "done" {
		t.Fatal(original.Status, original.Error)
	}
	var captured *recipeCaptureTask
	measure("full-input-type-capture", func() <-chan struct{} {
		started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/capture/start", map[string]string{"jobId": original.ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "format": "csv"}))
		captured = a.getRecipeCapture(started.JobID)
		return captured.done
	})
	if captured.Status != "done" || len(captured.Draft.Fields) != 4 {
		t.Fatal(captured.Status, captured.Error, captured.Draft.Fields)
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/recipes/capture/commit", commitRecipeRequest(a, captured.ID, "规模分析方案", 0)))
	if err = a.OpenPath(files[1]); err != nil {
		t.Fatal(err)
	}
	indexStart = time.Now()
	wb = a.currentWorkbook()
	source := "wb:" + wb.Sheets[0].Name
	reconcileRef(t, a, sourceBookID(wb), source, 1)
	targetIndexMS := time.Since(indexStart).Milliseconds()
	preview := previewTestRecipe(t, a, captured.Draft.ID, source)
	mapping := map[string]string{}
	for _, match := range preview.Matches {
		if match.Status != "match" {
			t.Fatal(match)
		}
		mapping[match.Field] = match.Target
	}
	request := recipeRunRequest(a, preview, source, mapping)
	startReplay := func() *analysisTask {
		started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/run/start", request))
		return a.getAnalysisJob(started.JobID)
	}
	var replay *analysisTask
	measure("guarded-replay", func() <-chan struct{} { replay = startReplay(); return replay.done })
	if replay.Status != "done" {
		t.Fatal(replay.Status, replay.Error)
	}
	type expectedGroup struct {
		sum   big.Int
		count int64
	}
	expected := make([]expectedGroup, 97)
	cent31, _ := new(big.Int).SetString("900719925474099331", 10)
	cent30, _ := new(big.Int).SetString("900719925474099330", 10)
	var included int64
	for i := 0; i < count; i++ {
		if i%7 == 0 {
			continue
		}
		value := cent31
		if i%2 == 0 {
			value = cent30
		}
		group := &expected[i%97]
		group.sum.Add(&group.sum, value)
		group.count++
		included++
	}
	for _, job := range []*analysisTask{original, replay} {
		if job.Result.Included != included || len(job.Result.Rows) != 97 {
			t.Fatal(job.Result.Included, len(job.Result.Rows))
		}
		for i, row := range job.Result.Rows {
			want := &expected[i]
			if len(row.Keys) != 1 || row.Keys[0] != fmt.Sprintf("机构%03d", i) {
				t.Fatal(row.Keys)
			}
			for metric, divisor := range []int64{100, 1000} {
				actual, ok := new(big.Rat).SetString(row.MetricTotals[metric].Exact)
				if !ok || actual.Cmp(new(big.Rat).SetFrac(&want.sum, big.NewInt(divisor))) != 0 {
					t.Fatal("exact replay total differs", i, metric, row.MetricTotals)
				}
			}
			if row.MetricTotals[2].Exact != strconv.FormatInt(want.count, 10) {
				t.Fatal("record count differs", i, row.MetricTotals[2])
			}
		}
	}
	// Cancel after actual processing starts, rather than only while queued.
	cancelled := startReplay()
	deadline := time.Now().Add(20 * time.Second)
	for {
		cancelled.mu.RLock()
		progress, status := cancelled.Progress, cancelled.Status
		cancelled.mu.RUnlock()
		if progress > 0 && status == "running" {
			break
		}
		if status == "done" || status == "error" || time.Now().After(deadline) {
			t.Fatal("probe did not observe active replay", status, progress)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reconcilePost(t, a, "/api/recipes/run/cancel", map[string]string{"id": cancelled.ID})
	select {
	case <-cancelled.done:
	case <-time.After(10 * time.Second):
		t.Fatal("active replay cancellation timed out")
	}
	status := decodeResponse[map[string]json.RawMessage](t, analysisRequest(t, a, "GET", "/api/recipes/run/status?id="+cancelled.ID, ""))
	if cancelled.Status != "cancelled" || status["result"] != nil {
		t.Fatal("cancelled replay published partial result", cancelled.Status)
	}
	a.sourceMu.Lock()
	pins := len(a.sourcePins)
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("finished jobs retained source pins", pins)
	}
	after := [2]string{digest(files[0]), digest(files[1])}
	if before != after {
		t.Fatal("recipe modified source files")
	}
	report := map[string]any{"result": "PASS", "version": buildinfo.Version, "generatedRecordsPerFile": count, "included": included, "groupsVerified": 97, "sourceIndexMs": sourceIndexMS, "targetIndexMs": targetIndexMS, "measurements": measures, "sourceSHA256": before, "sourceUnchanged": true, "activeCancellationVerified": true, "partialResultNotPublished": true, "desktopManualAcceptance": false, "limitations": "Local generated CSVs; analysis/capture/replay phases exclude initial indexing. Sampled Go heap excludes mapped indexes, OS file cache and the browser; not an application memory cap or a universal speed comparison."}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	path := os.Getenv("EXCELLENS_RECIPE_REPORT")
	if path == "" {
		path = filepath.Join(output, "report.json")
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
