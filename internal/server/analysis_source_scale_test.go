package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/buildinfo"
)

func TestAnalysisSourceMillionCSVRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_ANALYSIS_SOURCE_SCALE") != "1" {
		t.Skip("optional generated million-record analysis source check")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-analysis.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(f, 1<<20)
	if _, err = io.WriteString(writer, "类别,金额,编号\r\n"); err != nil {
		t.Fatal(err)
	}
	for row := 1; row <= records; row++ {
		if _, err = fmt.Fprintf(writer, "甲,9007199254740993.31,000%013d\r\n", row); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
		input, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		digest := sha256.New()
		if _, err = io.Copy(digest, input); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", digest.Sum(nil))
	}
	before := hash()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: meta.Name, Change: &rowViewChange{Action: "range", First: 2, Last: records/2 + 1, Hidden: true}}), "done")
	runtime.GC()
	var pre, post runtime.MemStats
	runtime.ReadMemStats(&pre)
	captured, err := a.captureAnalysisSource(wb, analysisSourceCaptureRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: "wb:" + meta.Name, Mode: "visible", ViewID: view})
	if err != nil {
		t.Fatal(err)
	}
	defer captured.close()
	runtime.ReadMemStats(&post)
	allocated := post.TotalAlloc - pre.TotalAlloc
	if allocated >= 2<<20 {
		t.Fatal("capture appears to materialize source-sized row storage", allocated)
	}
	if captured.data.plan.Len() != records/2+1 {
		t.Fatal("captured visible source count differs", captured.data.plan.Len())
	}
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	start := time.Now()
	job, err := a.startAnalysisTask(wb, analysis.Query{Source: "wb:" + meta.Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}, nil, captured)
	if err != nil {
		for i := 0; i < cap(a.analysisSem); i++ {
			<-a.analysisSem
		}
		t.Fatal(err)
	}
	captured.close()
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	for i := 0; i < cap(a.analysisSem); i++ {
		<-a.analysisSem
	}
	select {
	case <-job.done:
	case <-time.After(90 * time.Second):
		job.cancel()
		t.Fatal("million-record scoped aggregation timed out")
	}
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	amount, _ := new(big.Rat).SetString("9007199254740993.31")
	amount.Mul(amount, big.NewRat(records/2, 1))
	expected := analysis.ExactDecimalString(amount)
	if job.Result.Scanned != records/2 || job.Result.Included != records/2 || job.Result.Grand.Exact != expected {
		t.Fatal("scoped million-record sum or count differs", job.Result, expected)
	}
	aggregateElapsed := time.Since(start)
	a.cacheMu.RLock()
	src, err := a.sourceResult(context.Background(), wb, job.Query, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = src.scan(context.Background(), []string{"C1", "C2", "C3"}, func(row analysis.Record) error {
		expectedID := records/2 + checked + 1
		if row.Number != expectedID+1 || row.Values["C1"].Text != "甲" || row.Values["C2"].Text != "9007199254740993.31" || !row.Values["C2"].Numeric || row.Values["C3"].Text != fmt.Sprintf("000%013d", expectedID) || row.Values["C3"].Numeric {
			return fmt.Errorf("record %d source/type/identity mismatch: %+v", checked, row)
		}
		checked++
		return nil
	})
	src.close()
	a.cacheMu.RUnlock()
	if err != nil || checked != records/2 {
		t.Fatal("independent selected-record verification failed", checked, err)
	}
	if hash() != before {
		t.Fatal("analysis wrote the original CSV")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "os": runtime.GOOS, "go": runtime.Version(), "sourceRecords": records, "visibleDataRecords": records / 2, "verifiedRecords": checked, "exactSum": expected, "captureAllocatedBytesObserved": allocated, "aggregationElapsedMsObserved": aggregateElapsed.Milliseconds(), "sourceSHA256": before, "sourceUnchanged": true, "desktopManualAcceptance": false, "notes": "Generated CSV and an existing visible-row snapshot; every selected record/type checked. Allocation/timing are observations, not a before/after performance claim."}
	data, _ := json.MarshalIndent(report, "", "  ")
	if output := os.Getenv("EXCELLENS_ANALYSIS_SOURCE_RECEIPT"); output != "" {
		if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
}
