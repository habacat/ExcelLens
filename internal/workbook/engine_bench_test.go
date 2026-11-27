package workbook

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const engineBenchRows = 16384

func engineLedger(b *testing.B, format string) string {
	b.Helper()
	sep := ","
	if format == "txt" {
		sep = "|"
	}
	var source bytes.Buffer
	for i := range engineBenchRows {
		memo := "费用报销 Ledger entry with mixed CASE and a quoted field"
		if i == engineBenchRows-1 {
			memo = "审计 Needle-XYZ"
		}
		fmt.Fprintf(&source, "%08d%sGroup%d%sBatch%d%s9007199254740993.125%s\"%s%s凭证\"\r\n", i, sep, i%4, sep, (i/4)%2, sep, sep, memo, sep)
	}
	path := filepath.Join(b.TempDir(), "ledger."+format)
	if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
		b.Fatal(err)
	}
	return path
}

func engineOpenIndex(b *testing.B, path string) (*Workbook, *SheetIndex) {
	b.Helper()
	w, err := OpenWorkbook(path)
	if err != nil {
		b.Fatal(err)
	}
	// Use an empty index directory per cold open, even when testing the same bytes.
	w.CacheDir = b.TempDir()
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil || idx.RowCount() != engineBenchRows || idx.MaxCol != 5 {
		b.Fatal("incomplete ledger", err)
	}
	return w, idx
}

func engineSearch(b *testing.B, w *Workbook, idx *SheetIndex) {
	b.Helper()
	hits, truncated, err := w.SearchSheet(idx, w.Sheets[0].Name, "needle-xyz", "value", 2, nil, nil)
	if err != nil || truncated || len(hits) != 1 || hits[0].Ref != "E16384" {
		b.Fatal(hits, truncated, err)
	}
}

func engineFilter(b *testing.B, w *Workbook, idx *SheetIndex) {
	b.Helper()
	rules := []FilterRule{{Column: 2, Operator: "equals", Value: "group1"}, {Column: 3, Operator: "in", Values: []string{"BATCH1"}}}
	order, err := w.FilterRowOrder(context.Background(), idx, rules, FilterOptions{}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer order.Close()
	if order.Len() != engineBenchRows/8 || order.At(0) != 5 || order.At(order.Len()-1) != engineBenchRows-3 {
		b.Fatal("incorrect filter coordinates")
	}
}

func BenchmarkEngineLedger(b *testing.B) {
	for _, format := range []string{"csv", "txt"} {
		path := engineLedger(b, format)
		b.Run(format+"/parse", func(b *testing.B) {
			w, err := OpenWorkbook(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				n := 0
				err := w.streamTextRows(func(fields []string) error {
					if len(fields) != 5 || fields[3] != "9007199254740993.125" {
						b.Fatal("invalid record", n)
					}
					n++
					return nil
				}, nil)
				if err != nil || n != engineBenchRows {
					b.Fatal(n, err)
				}
			}
		})
		b.Run(format+"/workflow", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				w, idx := engineOpenIndex(b, path)
				engineSearch(b, w, idx)
				engineFilter(b, w, idx)
				engineFilter(b, w, idx)
				rows, err := w.ReadRowsContext(context.Background(), idx, 5, 1, 1, 5)
				if err != nil || len(rows) != 1 || rows[0].Number != 6 || len(rows[0].Cells) != 5 ||
					rows[0].Cells[0].Raw != "00000005" || rows[0].Cells[0].Type != "inlineStr" ||
					rows[0].Cells[3].Raw != "9007199254740993.125" {
					b.Fatal("incorrect source row", rows, err)
				}
				idx.RowIndex.ReleaseMapping()
			}
		})
		if format == "csv" {
			w, idx := engineOpenIndex(b, path)
			b.Cleanup(idx.RowIndex.ReleaseMapping)
			b.Run("csv/search", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					engineSearch(b, w, idx)
				}
			})
			for _, temp := range []string{"cold", "warm"} {
				b.Run("csv/filter-"+temp, func(b *testing.B) {
					engineFilter(b, w, idx)
					b.ReportAllocs()
					for b.Loop() {
						if temp == "cold" {
							w.clearColumnCache()
						}
						engineFilter(b, w, idx)
					}
				})
			}
		}
	}
}

func BenchmarkEngineXMLWorkflow(b *testing.B) {
	const n = 4096
	var source bytes.Buffer
	source.WriteString("<worksheet><sheetData>")
	for row := 1; row <= n; row++ {
		fmt.Fprintf(&source, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%08d</t></is></c><c r="C%d"><v>%d.125</v></c></row>`, row, row, row, row, row)
	}
	source.WriteString("</sheetData></worksheet>")
	path := sourceMetadataWorkbook(b, source.String(), "")
	b.ReportAllocs()
	for b.Loop() {
		w, err := OpenWorkbook(path)
		if err != nil {
			b.Fatal(err)
		}
		w.CacheDir = b.TempDir()
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil || idx.RowCount() != n || idx.MaxCol != 3 {
			b.Fatal("incomplete XML index", err)
		}
		hits, truncated, err := w.SearchSheet(idx, "Data", "00004096", "value", 2, nil, nil)
		if err != nil || truncated || len(hits) != 1 || hits[0].Ref != "A4096" {
			b.Fatal("incorrect XML search", hits, err)
		}
		rows, err := w.ReadRowsContext(b.Context(), idx, n-1, 1, 1, 3)
		if err != nil || len(rows) != 1 || len(rows[0].Cells) != 2 || rows[0].Cells[1].Ref != "C4096" || rows[0].Cells[1].Raw != "4096.125" {
			b.Fatal("incorrect source value", rows, err)
		}
		idx.RowIndex.ReleaseMapping()
	}
}

func BenchmarkEngineDenseSearch(b *testing.B) {
	w, idx := engineOpenIndex(b, engineLedger(b, "csv"))
	defer idx.RowIndex.ReleaseMapping()
	b.ReportAllocs()
	for b.Loop() {
		n := 0
		err := w.StreamSearch(b.Context(), idx, w.Sheets[0].Name, "LEDGER", "value", 0, func(hit SearchResult) error {
			n++
			if hit.Row != n || hit.Col != 5 {
				b.Fatal("incorrect hit", hit)
			}
			return nil
		}, func(int, bool) error { return nil }, nil)
		if err != nil || n != engineBenchRows-1 {
			b.Fatal(n, err)
		}
	}
}

func BenchmarkEngineLiteralSearch(b *testing.B) {
	w, idx := engineOpenIndex(b, engineLedger(b, "csv"))
	defer idx.RowIndex.ReleaseMapping()
	for _, test := range []struct{ label, needle, ref string }{
		{"ID", "00016383", "A16384"}, {"Han", "审计", "E16384"},
	} {
		b.Run(test.label, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				got, truncated, err := w.SearchSheet(idx, w.Sheets[0].Name, test.needle, "value", 2, nil, nil)
				if err != nil || truncated || len(got) != 1 || got[0].Ref != test.ref {
					b.Fatal(got, truncated, err)
				}
			}
		})
	}
}
