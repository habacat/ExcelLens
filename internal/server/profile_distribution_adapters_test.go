package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestProfileDistributionRealTextHTMLBIFFAndMacroContainers(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,金额\n001,\"(1,234.50)\"\n002,9007199254740993.31\n003,00123\n"},
		{"source.tsv", "tsv", "编号\t金额\n001\t(1,234.50)\n002\t9007199254740993.31\n003\t00123\n"},
		{"source.txt", "txt", "编号\t金额\n001\t(1,234.50)\n002\t9007199254740993.31\n003\t00123\n"},
		{"source.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>(1,234.50)</td></tr><tr><td>002</td><td>9007199254740993.31</td></tr><tr><td>003</td><td>00123</td></tr></table></html>"},
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
			if a.currentWorkbook().Format != tc.format {
				t.Fatal(a.currentWorkbook().Format)
			}
			req := profileSource(t, a, meta.Name, "B2", "B4", rowViewRequest{})
			r := profileOK(t, a, req)
			raw := []string{"-1234.5", "9007199254740993.31"}
			if tc.format == "xls" {
				raw = []string{"2.5", "3.5", "4.5"}
			}
			assertProfileDistribution(t, r, raw)
			req.Start, req.End = "A2", "A4"
			r = profileOK(t, a, req)
			assertProfileDistribution(t, r, nil)
			if r.Text != 3 {
				t.Fatal("identifiers changed", r)
			}
			if before != printScaleHash(t, file) {
				t.Fatal("source changed")
			}
		})
	}
	t.Run("xlsm", func(t *testing.T) {
		a, file := quickFixture(t)
		data, _ := os.ReadFile(file)
		target := filepath.Join(t.TempDir(), "macro-container.xlsm")
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.OpenPath(target); err != nil {
			t.Fatal(err)
		}
		wb := a.currentWorkbook()
		if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
			t.Fatal(err)
		}
		req := profileSource(t, a, "First", "B2", "B6", rowViewRequest{})
		assertProfileDistribution(t, profileOK(t, a, req), []string{"9007199254740993.30", "0.01", "0"})
	})
}

func TestProfileDistributionActualMillionCSVAndCompleteBIFF(t *testing.T) {
	if os.Getenv("EXCELLENS_PROFILE_SCALE") != "1" {
		t.Skip("set EXCELLENS_PROFILE_SCALE=1 for actual million CSV/full BIFF distribution")
	}
	var evidence []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "distribution."+kind)
			count := 1_000_000
			if kind == "csv" {
				f, err := os.Create(file)
				if err != nil {
					t.Fatal(err)
				}
				w := bufio.NewWriterSize(f, 1<<20)
				for row := 1; row <= count; row++ {
					if _, err := fmt.Fprintf(w, "%012d,%d.%02d\n", row, int64(9007199254740993)+int64(row/100), row%100); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				count = formats.BIFFRows
				printBIFFSource(t, file, count)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(meta.Path)
			if idx.RowCount() != count || idx.MaxRow != count {
				t.Fatal("not actual source capacity", idx.RowCount(), idx.MaxRow)
			}
			req := profileSource(t, a, meta.Name, "B1", "B"+strconv.Itoa(count), rowViewRequest{})
			cancelled := false
			if kind == "csv" {
				data, _ := json.Marshal(req)
				ctx, cancel := context.WithCancel(context.Background())
				request := httptest.NewRequest("POST", "/api/profile", bytes.NewReader(data)).WithContext(ctx)
				response := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); a.handleProfile(response, request) }()
				deadline := time.Now().Add(30 * time.Second)
				for !workbook.QueryFilesInUse(wb.CacheDir) {
					select {
					case <-done:
						cancel()
						t.Fatal("profile completed before observed spill", response.Code, response.Body.String())
					default:
					}
					if time.Now().After(deadline) {
						cancel()
						t.Fatal("no spilled run observed")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("spilled profile ignored cancellation")
				}
				if response.Body.Len() != 0 || workbook.QueryFilesInUse(wb.CacheDir) {
					t.Fatal("cancelled profile published or retained runs", response.Code, response.Body.String())
				}
				cancelled = true
			}
			started := time.Now()
			response := profileCall(t, a, req)
			elapsed := time.Since(started)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			r := decodeResponse[profileReport](t, response)
			var sum, minimum, step *big.Rat
			if kind == "csv" {
				minimum = new(big.Rat).Add(big.NewRat(9007199254740993, 1), big.NewRat(1, 100))
				step = big.NewRat(1, 100)
				sum = new(big.Rat).Add(new(big.Rat).Mul(big.NewRat(9007199254740993, 1), big.NewRat(int64(count), 1)), big.NewRat(int64(count)*int64(count+1), 200))
			} else {
				minimum = big.NewRat(3, 2)
				step = big.NewRat(1, 1)
				sum = big.NewRat(int64(count)*int64(count+2), 2)
			}
			got, ok := new(big.Rat).SetString(r.SumExact)
			if !ok || got.Cmp(sum) != 0 || r.Number != int64(count) || r.Total != int64(count) || r.Distribution.Count != int64(count) {
				t.Fatal("incomplete/inexact scale statistics", r.Number, r.Total, r.SumExact, sum)
			}
			for _, q := range r.Distribution.Quantiles {
				offset := big.NewRat(int64(count-1)*int64(q.Percent), 100)
				want := new(big.Rat).Add(minimum, new(big.Rat).Mul(step, offset))
				actual, ok := new(big.Rat).SetString(q.Value)
				if !ok || actual.Cmp(want) != 0 {
					t.Fatal("scale quantile mismatch", kind, q, want)
				}
			}
			var total int64
			for at, bin := range r.Distribution.Bins {
				lower := (int64(at)*int64(count-1) + 9) / 10
				upper := (int64(at+1)*int64(count-1) + 9) / 10
				if at == 9 {
					upper = int64(count)
				}
				if bin.Count != upper-lower {
					t.Fatal("scale bin mismatch", kind, at, bin.Count, upper-lower)
				}
				total += bin.Count
			}
			if total != int64(count) || workbook.QueryFilesInUse(wb.CacheDir) {
				t.Fatal("scale rows or runs leaked", total)
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source changed")
			}
			evidence = append(evidence, map[string]any{"format": kind, "physicalRows": count, "numericRows": r.Number, "sumExact": r.SumExact, "quantiles": r.Distribution.Quantiles, "bins": r.Distribution.Bins, "elapsedMs": float64(elapsed.Microseconds()) / 1000, "responseBytes": response.Body.Len(), "cancelledAfterSpill": cancelled, "sourceUnchanged": true, "queryFilesReleased": true})
		})
	}
	if output := os.Getenv("EXCELLENS_PROFILE_SCALE_REPORT"); output != "" {
		data, err := json.MarshalIndent(map[string]any{"result": "PASS", "sources": evidence}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
