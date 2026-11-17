package server

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func TestCompositionAnalysisActualMillionCSVAndFullBIFF(t *testing.T) {
	if os.Getenv("EXCELLENS_COMPOSITION_ANALYSIS_SCALE") != "1" {
		t.Skip("set EXCELLENS_COMPOSITION_ANALYSIS_SCALE=1 for actual append analysis scale checks")
	}
	for _, format := range []string{"csv", "xls"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "actual."+format)
			sourceRows := 1_000_000
			if format == "csv" {
				writeCompositionScaleCSV(t, file, sourceRows)
			} else {
				printBIFFSource(t, file, 65_536)
				sourceRows = 65_535
			}
			before := printScaleHash(t, file)
			a, _ := recordOpen(t, file)
			definition := compositionDefinition(t, a)
			q := analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2", Precision: 2}
			started := time.Now()
			job := compositionAnalysisStart(t, a, q, "")
			select {
			case <-job.done:
			case <-time.After(3 * time.Minute):
				t.Fatal("actual scale analysis timed out")
			}
			result := waitCompositionAnalysis(t, job, "done")
			var expected *big.Rat
			if format == "csv" {
				expected, _ = new(big.Rat).SetString("9007199254740993.31")
				expected.Mul(expected, big.NewRat(int64(sourceRows), 1))
			} else {
				// Sum row+0.5 over native physical rows 2..65536.
				expected = big.NewRat(int64(sourceRows)*(2+65536+1), 2)
			}
			expected.Sub(expected, big.NewRat(215, 100))
			actual, ok := analysis.Decimal(result.Grand.Exact)
			if !ok || actual.Cmp(expected) != 0 || result.Scanned != int64(sourceRows+2) || result.InvalidNumbers != 0 || result.MissingFormulaCaches != 0 {
				t.Fatal("actual exact append aggregate differs", result.Grand, result.Scanned, expected.RatString())
			}
			a.cacheMu.RLock()
			src, err := a.sourceResult(context.Background(), a.currentWorkbook(), job.Query, job.ID)
			if err != nil {
				a.cacheMu.RUnlock()
				t.Fatal(err)
			}
			var records int64
			err = src.scan(context.Background(), []string{"C1", "C2"}, func(rec analysis.Record) error {
				records++
				if rec.Sequence != records || rec.Origin == nil {
					return fmt.Errorf("sequence/provenance missing at %d", records)
				}
				if records <= int64(sourceRows) {
					if rec.Number != int(records)+1 || rec.Origin.Input != 0 || rec.Origin.Columns[1] != 2 || !rec.Values["C2"].Numeric || rec.Values["C1"].Numeric {
						return fmt.Errorf("source row/type changed at %d", records)
					}
					if format == "csv" && rec.Values["C2"].Text != "9007199254740993.31" {
						return fmt.Errorf("raw exact CSV amount changed at %d", records)
					}
					if format == "xls" && rec.Values["C2"].Text != fmt.Sprintf("%d.5", rec.Number) {
						return fmt.Errorf("raw BIFF amount changed at %d", records)
					}
				}
				return nil
			})
			src.close()
			a.cacheMu.RUnlock()
			if err != nil || records != int64(sourceRows+2) {
				t.Fatal(records, err)
			}
			a.clearAnalysisTasks()
			if compositionUsed(a) != 0 || printScaleHash(t, file) != before {
				t.Fatal("source budget leaked or source changed", compositionUsed(a))
			}
			t.Logf("format=%s sourceRows=%d unionRows=%d exact=%s; every source row/value/numeric qualification checked; elapsedMs=%d; sourceSHA256=%s", format, sourceRows, records, result.Grand.Exact, time.Since(started).Milliseconds(), before)
		})
	}
}
