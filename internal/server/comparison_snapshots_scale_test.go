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
	"testing"
	"time"

	"excellens/internal/analysis"
)

func TestSnapshotMillionActualCSVIndependentExactResults(t *testing.T) {
	if os.Getenv("EXCELLENS_SNAPSHOT_SCALE") != "1" {
		t.Skip("optional actual million-record CSV snapshot probe")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-snapshots.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(f, 1<<20)
	_, _ = writer.WriteString("分类,系列,金额,参与\r\n")
	var left, right [100][3]big.Int
	huge, _ := new(big.Int).SetString("900719925474099331", 10)
	for i := 0; i < records; i++ {
		g, s := i%100, i/100%3
		amount, cents := "0.01", big.NewInt(1)
		if i < 100 {
			amount, cents = "9007199254740993.31", huge
		}
		part := "后"
		if i < records/2 {
			part = "前"
			left[g][s].Add(&left[g][s], cents)
		}
		right[g][s].Add(&right[g][s], cents)
		if _, err := fmt.Fprintf(writer, "组%03d,系列%d,%s,%s\r\n", g, s, amount, part); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
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
	before := hash()
	a := openTreeTestApp(t, file)
	q := analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Columns: []string{"C2"}, Value: "C3", Aggregate: "sum", Precision: 2, Sort: "label_asc", Filters: []analysis.Filter{{Field: "C4", Op: "eq", Value: "前"}}}
	complete := func(q analysis.Query) *analysisTask {
		job, err := a.startAnalysisTask(a.currentWorkbook(), q, nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-job.done:
		case <-time.After(90 * time.Second):
			t.Fatal("million-record aggregation timed out")
		}
		if job.Status != "done" {
			t.Fatal(job.Status, job.Error)
		}
		return job
	}
	for i := range 2 {
		if i == 1 {
			q.Filters = nil
		}
		job := complete(q)
		if job.Result.Scanned != records || job.Result.Included != int64(records/2*(i+1)) {
			t.Fatal("not an actual full scan", job.Result.Scanned, job.Result.Included)
		}
		if response := reconcilePost(t, a, snapshotRoute, snapshotSaveRequest(a, job, int64(i))); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	store := snapshotStoreForTest(t)
	comparison := decodeResponse[analysis.SnapshotComparison](t, analysisRequest(t, a, "GET", snapshotRoute+"/compare?left="+store.Items[0].ID+"&right="+store.Items[1].ID, ""))
	if comparison.Grand.Difference.Exact != "5000" || len(comparison.Rows) != 100 || len(comparison.ColumnKeys) != 3 {
		t.Fatal(comparison.Grand, len(comparison.Rows), comparison.ColumnKeys)
	}
	for g, row := range comparison.Rows {
		if row.Keys[0] != fmt.Sprintf("组%03d", g) {
			t.Fatal("source category order changed", row.Keys)
		}
		for s, value := range row.Values {
			l, lok := new(big.Rat).SetString(value.Left.Exact)
			r, rok := new(big.Rat).SetString(value.Right.Exact)
			d, dok := new(big.Rat).SetString(value.Difference.Exact)
			wantL, wantR := new(big.Rat).SetFrac(&left[g][s], big.NewInt(100)), new(big.Rat).SetFrac(&right[g][s], big.NewInt(100))
			if !lok || !rok || !dok || l.Cmp(wantL) != 0 || r.Cmp(wantR) != 0 || d.Cmp(new(big.Rat).Sub(wantR, wantL)) != 0 {
				t.Fatal("independent cent oracle differs", g, s, value)
			}
		}
	}
	if hash() != before {
		t.Fatal("source bytes changed")
	}
	if receipt := os.Getenv("EXCELLENS_SNAPSHOT_RECEIPT"); receipt != "" {
		info, _ := os.Stat(file)
		data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "records": records, "sourceBytes": info.Size(), "sourceSHA256": before, "sourceUnchanged": true, "snapshots": 2, "checkedClassSeriesCells": 300, "grandDifferenceExact": "5000", "desktopManualAcceptance": false}, "", "  ")
		if err := os.WriteFile(receipt, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
