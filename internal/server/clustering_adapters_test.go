package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/analysis"
)

func TestClusterRealTextHTMLAndBIFFAdaptersKeepReadTypesAndLeadingZeroes(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,其他\n0001,x\n0001,y\n\"(1,234.50)\",z\n"},
		{"source.tsv", "tsv", "编号\t其他\n0001\tx\n0001\ty\n(1,234.50)\tz\n"},
		{"source.txt", "txt", "编号\t其他\n0001\tx\n0001\ty\n(1,234.50)\tz\n"},
		{"html.xls", "xls-html", "<html><table><tr><td>编号</td><td>其他</td></tr><tr><td>0001</td><td>x</td></tr><tr><td>0001</td><td>y</td></tr><tr><td>(1,234.50)</td><td>z</td></tr></table></html>"},
		{"native.xls", "xls", ""},
	} {
		t.Run(tc.format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body == "" {
				printBIFFSource(t, file, 4)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			if wb.Format != tc.format {
				t.Fatal("wrong actual adapter", wb.Format)
			}
			req := clusterStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: "wb:" + meta.Name, HeaderRow: 1, Field: "C1"}
			id := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/clusters/start", req)).JobID
			a.mu.RLock()
			job := a.clusterJobs[id]
			a.mu.RUnlock()
			result := clusterWait(t, a, job, "done").Result
			if result.Counts.Scanned != 3 {
				t.Fatal(result.Counts)
			}
			if tc.format == "xls" {
				if result.Counts.Eligible != 3 || result.Counts.Numeric != 0 || result.Counts.Unique != 3 || len(result.Groups) != 0 {
					t.Fatal("native text identifiers changed", result)
				}
			} else if result.Counts.Eligible != 2 || result.Counts.Numeric != 1 || len(result.Groups) != 1 || result.Groups[0].Variants[0].Raw != "0001" ||
				result.Groups[0].Variants[0].Type != "inlineStr" || result.Groups[0].Count != 2 {
				t.Fatal("text amount or leading-zero interpretation changed", result)
			}
			a.cacheMu.RLock()
			src, err := a.source(wb, req.Source, 1)
			if err != nil {
				a.cacheMu.RUnlock()
				t.Fatal(err)
			}
			err = src.scan(context.Background(), []string{"C1"}, func(record analysis.Record) error {
				value := record.Values["C1"]
				if record.Number == 2 && (!value.Textual || value.Numeric) {
					return fmt.Errorf("source text eligibility lost: %+v", value)
				}
				return nil
			})
			src.close()
			a.cacheMu.RUnlock()
			if err != nil || printScaleHash(t, file) != before {
				t.Fatal("source changed", err)
			}
		})
	}
}
