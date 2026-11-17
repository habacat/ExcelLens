package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestCellExplanationRealTextHTMLBIFFAndMacroContainerAdapters(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,金额\n001,\"(1,234.50)\"\n"},
		{"source.tsv", "tsv", "编号\t金额\n001\t(1,234.50)\n"},
		{"source.txt", "txt", "编号\t金额\n001\t(1,234.50)\n"},
		{"source.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>(1,234.50)</td></tr></table></html>"},
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
			req := cellExplanationSource(t, a, meta.Name, "B2", rowViewRequest{})
			amount := cellExplanationOK(t, a, req)
			want, reason, raw := "-1234.5", "imported-amount", "(1,234.50)"
			if tc.format == "xls" {
				want, reason, raw = "2.5", "number", "2.5"
			}
			if amount.Numeric.Exact != want || !amount.Numeric.Eligible || amount.Numeric.Reason != reason || amount.Cell.Raw != raw || amount.FormulaState != "none" {
				t.Fatal(amount)
			}
			req.Ref = "A2"
			id := cellExplanationOK(t, a, req)
			if id.Numeric.Eligible || id.Cell.Raw == "" {
				t.Fatal("identifier changed", id)
			}
			if before != printScaleHash(t, file) {
				t.Fatal("source changed")
			}
		})
	}
	t.Run("xlsm", func(t *testing.T) {
		a, file := cellExplanationFixture(t)
		data, _ := os.ReadFile(file)
		copy := filepath.Join(t.TempDir(), "container.xlsm")
		if err := os.WriteFile(copy, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.OpenPath(copy); err != nil {
			t.Fatal(err)
		}
		wb := a.currentWorkbook()
		if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
			t.Fatal(err)
		}
		req := cellExplanationSource(t, a, "First", "B2", rowViewRequest{})
		if r := cellExplanationOK(t, a, req); r.Numeric.Eligible || r.Numeric.Reason != "source-text" {
			t.Fatal(r)
		}
	})
}

func TestCellExplanationActualMillionCSVAndCompleteBIFF(t *testing.T) {
	if os.Getenv("EXCELLENS_CELL_SCALE") != "1" {
		t.Skip("set EXCELLENS_CELL_SCALE=1 for actual million CSV/full BIFF")
	}
	var evidence []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "source."+kind)
			count := 1_000_000
			if kind == "csv" {
				f, err := os.Create(file)
				if err != nil {
					t.Fatal(err)
				}
				w := bufio.NewWriterSize(f, 1<<20)
				for row := 1; row <= count; row++ {
					if _, err := fmt.Fprintf(w, "%012d,9007199254740993.31\n", row); err != nil {
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
			idx := a.currentWorkbook().GetCachedIndex(meta.Path)
			if idx.RowCount() != count || idx.MaxRow != count {
				t.Fatal("not an actual scale fixture", idx.RowCount(), idx.MaxRow)
			}
			req := cellExplanationSource(t, a, meta.Name, "B2", rowViewRequest{})
			var cells []map[string]any
			for _, row := range []int{2, count / 2, count} {
				req.Ref = "B" + strconv.Itoa(row)
				started := time.Now()
				response := cellExplanationCall(t, a, req)
				if response.Code != 200 {
					t.Fatal(response.Code, response.Body.String())
				}
				r := decodeResponse[cellExplanationResponse](t, response)
				want := "9007199254740993.31"
				if kind == "xls" {
					want = strconv.FormatFloat(float64(row)+.5, 'f', 1, 64)
				}
				if r.Numeric.Exact != want || r.Cell.Raw != want || r.Cell.Ref != req.Ref || response.Body.Len() > 4096 {
					t.Fatal("wrong/oversized single cell", r, response.Body.Len())
				}
				cells = append(cells, map[string]any{"row": row, "responseBytes": response.Body.Len(), "elapsedMs": float64(time.Since(started).Microseconds()) / 1000, "exact": r.Numeric.Exact})
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source changed")
			}
			evidence = append(evidence, map[string]any{"format": kind, "physicalRows": count, "cells": cells, "sourceUnchanged": true})
		})
	}
	if output := os.Getenv("EXCELLENS_CELL_SCALE_REPORT"); output != "" {
		data, err := json.MarshalIndent(map[string]any{"result": "PASS", "sources": evidence}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
