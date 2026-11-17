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

func TestAnalysisTreeMillionCSVRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_ANALYSIS_TREE_SCALE") != "1" {
		t.Skip("optional generated million-record lazy-tree probe")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-tree.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.WriteString("机构,团队,编号,金额,代码,列\r\n"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < records; i++ {
		amount := "-0.01"
		if i%1000 == 0 {
			amount = "9007199254740993.31"
		}
		if _, err := fmt.Fprintf(w, "机构%03d,团队%03d,000%010d,%s,K%d,列%d\r\n", i/20000, i%20000/1000, i, amount, i%7, i%3); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
		input, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, input); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", digest.Sum(nil))
	}
	before := hash()
	a := openTreeTestApp(t, file)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: wb.Sheets[0].Name, Change: &rowViewChange{Action: "range", First: 2, Last: records/2 + 1, Hidden: true}}), "done")
	captured, err := a.captureAnalysisSource(wb, analysisSourceCaptureRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: "wb:" + wb.Sheets[0].Name, Mode: "visible", ViewID: view})
	if err != nil {
		t.Fatal(err)
	}
	defer captured.close()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1", "C2", "C3"}, Columns: []string{"C6"}, Value: "C4", Aggregate: "sum", Layout: "tree", Precision: 2, Sort: "label_asc", Metrics: []analysis.Metric{{Field: "C4", Aggregate: "sum"}, {Field: "C4", Aggregate: "average"}, {Field: "C5", Aggregate: "distinct"}, {Aggregate: "count"}}}
	started := time.Now()
	root, err := a.startAnalysisTask(wb, q, nil, captured)
	if err != nil {
		t.Fatal(err)
	}
	captured.close()
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	select {
	case <-root.done:
	case <-time.After(120 * time.Second):
		root.cancel()
		t.Fatal("tree root timed out")
	}
	rootMs := time.Since(started).Milliseconds()
	if root.Status != "done" {
		t.Fatal(root.Status, root.Error)
	}
	// Independent integer cents oracle; no aggregation helper computes this.
	teamCents, _ := new(big.Int).SetString("900719925474099331", 10)
	teamCents.Sub(teamCents, big.NewInt(999))
	exact := func(cents *big.Int) string {
		return analysis.ExactDecimalString(new(big.Rat).SetFrac(cents, big.NewInt(100)))
	}
	companyCents := new(big.Int).Mul(teamCents, big.NewInt(20))
	visibleCents := new(big.Int).Mul(teamCents, big.NewInt(500))
	if root.Result.Scanned != records/2 || root.Result.Included != records/2 || len(root.Result.Rows) != 25 || root.Result.Grand.Exact != exact(visibleCents) || root.Result.MetricGrand[2].Exact != "7" || root.Result.MetricGrand[3].Exact != "500000" {
		t.Fatal("million tree root mismatch", root.Result.Grand, len(root.Result.Rows), root.Result.Scanned)
	}
	started = time.Now()
	middle := treeStart(t, a, root.ID, []string{"机构025"})
	parent := treeResult(t, a, middle, "done")
	middleMs := time.Since(started).Milliseconds()
	if parent.Included != 20000 || len(parent.Rows) != 20 || parent.Grand.Exact != exact(companyCents) || parent.MetricGrand[2].Exact != "7" {
		t.Fatal(parent)
	}
	started = time.Now()
	leaf := treeStart(t, a, root.ID, []string{"机构025", "团队010"})
	result := treeResult(t, a, leaf, "done")
	leafMs := time.Since(started).Milliseconds()
	if len(result.Rows) != 1000 || result.Tree.HasChildren || result.Grand.Exact != exact(teamCents) || result.Included != 1000 {
		t.Fatal(result.Grand, len(result.Rows), result.Included)
	}
	for i, row := range result.Rows {
		id := 510000 + i
		amount := "-0.01"
		if i == 0 {
			amount = "9007199254740993.31"
		}
		if len(row.Keys) != 3 || row.Keys[2] != fmt.Sprintf("000%010d", id) || row.Total.Exact != amount || row.Count != 1 {
			t.Fatal("leaf was flattened, rounded or lost its source ID", i, row)
		}
	}
	// Verify every member of the actual captured source, independent of tree
	// grouping, including original row addresses and numeric interpretation.
	a.cacheMu.RLock()
	source, err := a.sourceResult(context.Background(), wb, root.Query, root.ID)
	if err != nil {
		a.cacheMu.RUnlock()
		t.Fatal(err)
	}
	checked := 0
	err = source.scan(context.Background(), []string{"C1", "C2", "C3", "C4"}, func(rec analysis.Record) error {
		i := records/2 + checked
		amount := "-0.01"
		if i%1000 == 0 {
			amount = "9007199254740993.31"
		}
		if rec.Number != i+2 || rec.Values["C1"].Text != fmt.Sprintf("机构%03d", i/20000) || rec.Values["C2"].Text != fmt.Sprintf("团队%03d", i%20000/1000) || rec.Values["C3"].Text != fmt.Sprintf("000%010d", i) || rec.Values["C3"].Numeric || rec.Values["C4"].Text != amount || !rec.Values["C4"].Numeric {
			return fmt.Errorf("source record %d differs", i)
		}
		checked++
		return nil
	})
	source.close()
	a.cacheMu.RUnlock()
	if err != nil || checked != records/2 {
		t.Fatal(checked, err)
	}
	// Cancel a real scan after the first progress publication, not just a
	// queued task; no serialized partial result may become visible.
	active := treeStart(t, a, root.ID, []string{"机构025"})
	deadline := time.Now().Add(30 * time.Second)
	observed := false
	for time.Now().Before(deadline) {
		active.mu.RLock()
		progress, status := active.Progress, active.Status
		active.mu.RUnlock()
		if status == "done" || status == "error" {
			break
		}
		if progress > 0 {
			observed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observed {
		active.cancel()
		t.Fatal("cancellation probe never observed an active scan")
	}
	active.cancel()
	treeResult(t, a, active, "cancelled")
	if len(active.Result) != 0 {
		t.Fatal("cancelled tree published partial data")
	}
	if hash() != before {
		t.Fatal("tree changed the CSV source")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "os": runtime.GOOS, "go": runtime.Version(), "sourceRecords": records, "visibleRecords": records / 2, "verifiedSourceRecords": checked, "verifiedLeafGroups": len(result.Rows), "loadedLevels": []int{25, 20, 1000}, "rootExactSum": root.Result.Grand.Exact, "parentExactSum": parent.Grand.Exact, "leafExactSum": result.Grand.Exact, "rootElapsedMsObserved": rootMs, "parentElapsedMsObserved": middleMs, "leafElapsedMsObserved": leafMs, "activeScanCancellation": true, "sourceSHA256": before, "sourceUnchanged": true, "desktopManualAcceptance": false, "treeChoiceUIAcceptance": false, "notes": "Generated CSV; independent integer-cents oracle and every selected source row/leaf verified. Timings are observations, not a before/after performance comparison or application memory bound."}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if output := os.Getenv("EXCELLENS_ANALYSIS_TREE_RECEIPT"); output != "" {
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(encoded))
}
