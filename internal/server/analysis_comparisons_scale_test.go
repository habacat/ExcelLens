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
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/buildinfo"
)

// This oracle accumulates integer source cents and original code sets. It
// does not call the analysis accumulator, comparison or decimal-render helper.
type comparisonScaleOracle struct {
	sum, min, max *big.Int
	count         int64
	codes         map[int]bool
}

func (o *comparisonScaleOracle) add(amount *big.Int, code int) {
	if o.sum == nil {
		o.sum, o.min, o.max = new(big.Int), new(big.Int).Set(amount), new(big.Int).Set(amount)
		o.codes = make(map[int]bool)
	}
	o.sum.Add(o.sum, amount)
	if o.min.Cmp(amount) > 0 {
		o.min.Set(amount)
	}
	if o.max.Cmp(amount) < 0 {
		o.max.Set(amount)
	}
	o.count++
	o.codes[code] = true
}

func TestAnalysisComparisonsMillionActualCSVRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_COMPARISON_SCALE") != "1" {
		t.Skip("optional generated million-record exact comparisons probe")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-comparisons.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(f, 1<<20)
	if _, err := writer.WriteString("机构,参与,编号,金额,代码,列\r\n"); err != nil {
		t.Fatal(err)
	}
	var total comparisonScaleOracle
	var groups [100][4]comparisonScaleOracle // three columns and the whole row
	var columns [3]comparisonScaleOracle
	huge, _ := new(big.Int).SetString("900719925474099331", 10)
	for i := 0; i < records; i++ {
		g, c := i/10000, i%3
		participation := "keep"
		if i%11 == 0 {
			participation = "skip"
		}
		amount := big.NewInt(-1)
		text := "-0.01"
		if i%1000 == 0 {
			amount = new(big.Int).Add(huge, big.NewInt(int64(g*100)))
			text = new(big.Rat).SetFrac(amount, big.NewInt(100)).FloatString(2)
		}
		if _, err := fmt.Fprintf(writer, "机构%03d,%s,000%010d,%s,K%d,列%d\r\n", g, participation, i, text, i%7, c); err != nil {
			t.Fatal(err)
		}
		if i >= records/2 && participation == "keep" {
			total.add(amount, i%7)
			columns[c].add(amount, i%7)
			groups[g][c].add(amount, i%7)
			groups[g][3].add(amount, i%7)
		}
	}
	if err := writer.Flush(); err != nil {
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
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Source: "wb:" + wb.Sheets[0].Name, Mode: "visible", ViewID: view})
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, SourceScope: &captured.SourceScope, Rows: []string{"C1"}, Columns: []string{"C6"}, Sort: "label_asc", Precision: 4, Filters: []analysis.Filter{{Field: "C2", Op: "eq", Value: "keep"}}, Metrics: []analysis.Metric{
		{Field: "C4", Aggregate: "sum", Comparison: "running_total"},
		{Field: "C4", Aggregate: "average", Comparison: "running_percent"},
		{Field: "C5", Aggregate: "distinct", Comparison: "running_total", ComparisonAxis: "columns"},
		{Aggregate: "count", Comparison: "percent_column"},
		{Field: "C4", Aggregate: "min", Comparison: "running_total", ComparisonAxis: "columns"},
		{Field: "C4", Aggregate: "max", Comparison: "percent_grand"},
	}}
	started := time.Now()
	job, err := a.startAnalysisTask(wb, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(120 * time.Second):
		job.cancel()
		t.Fatal("million comparison task timed out")
	}
	elapsed := time.Since(started).Milliseconds()
	r := job.Result
	if job.Status != "done" || r.Scanned != records/2 || r.Included != total.count || len(r.Rows) != 50 || r.Grand.Comparison == nil {
		t.Fatal(job.Status, job.Error, r.Scanned, r.Included, len(r.Rows))
	}
	assertRatio := func(cell analysis.Cell, numerator, denominator *big.Int) {
		t.Helper()
		if cell.Comparison == nil || cell.Comparison.Status != "" {
			t.Fatal("missing exact compared value", cell)
		}
		got, ok := new(big.Rat).SetString(cell.Comparison.Exact)
		if !ok || got.Cmp(new(big.Rat).SetFrac(numerator, denominator)) != 0 {
			t.Fatal("independent integer-cent oracle mismatch", cell.Comparison.Exact, numerator, denominator)
		}
	}
	prefixSums := [4]*big.Int{new(big.Int), new(big.Int), new(big.Int), new(big.Int)}
	var prefixCounts [4]int64
	checks := 0
	for i, row := range r.Rows {
		g := i + 50
		if row.Keys[0] != fmt.Sprintf("机构%03d", g) {
			t.Fatal("unexpected sorted prefix", row.Keys)
		}
		prefixCodes := make(map[int]bool)
		var prefixMin *big.Int
		for c := 0; c < 4; c++ {
			o := &groups[g][c]
			prefixSums[c].Add(prefixSums[c], o.sum)
			prefixCounts[c] += o.count
			cells := row.MetricTotals
			denominator := &total
			if c < 3 {
				cells, denominator = row.MetricValues[c], &columns[c]
				for code := range o.codes {
					prefixCodes[code] = true
				}
				if prefixMin == nil || prefixMin.Cmp(o.min) > 0 {
					prefixMin = new(big.Int).Set(o.min)
				}
			}
			assertRatio(cells[0], prefixSums[c], big.NewInt(100))
			assertRatio(cells[1], new(big.Int).Mul(prefixSums[c], big.NewInt(denominator.count)), new(big.Int).Mul(denominator.sum, big.NewInt(prefixCounts[c])))
			if c == 3 {
				prefixCodes, prefixMin = o.codes, o.min
			}
			assertRatio(cells[2], big.NewInt(int64(len(prefixCodes))), big.NewInt(1))
			assertRatio(cells[3], big.NewInt(o.count), big.NewInt(denominator.count))
			assertRatio(cells[4], prefixMin, big.NewInt(100))
			assertRatio(cells[5], o.max, total.max)
			checks += 6
		}
	}
	assertRatio(r.Grand, total.sum, big.NewInt(100))
	q.Top = 3
	limited := completedRecipeAnalysis(t, a, q)
	if !limited.Result.Truncated || len(limited.Result.Rows) != 3 || limited.Result.Grand.Comparison.Exact != r.Grand.Comparison.Exact {
		t.Fatal("Top-N redefined complete scale totals", limited.Result.Grand)
	}
	for i := range limited.Result.Rows {
		for m := range q.Metrics {
			if limited.Result.Rows[i].MetricTotals[m].Comparison.Exact != r.Rows[i].MetricTotals[m].Comparison.Exact {
				t.Fatal("Top-N changed previously computed prefixes")
			}
		}
	}
	if after := hash(); after != before {
		t.Fatal("million-record source changed")
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	receipt := map[string]any{"version": buildinfo.Version, "result": "PASS", "actualCSVRecords": records, "visibleScanned": r.Scanned, "included": r.Included, "groups": len(r.Rows), "columns": len(r.ColumnKeys), "independentComparedCells": checks, "sixIndependentMetrics": true, "topNRetainsFullTotals": true, "sourceSHA256": before, "sourceUnchanged": true, "warmTaskMs": elapsed, "observedHeapAlloc": memory.HeapAlloc, "desktopManualAcceptance": false}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	if path := os.Getenv("EXCELLENS_COMPARISON_RECEIPT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
}
