// largeprobe exercises the actual read-only pipeline with synthetic records.
// It is a development probe, not a claim of billion-record acceptance.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/buildinfo"
	exporter "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type measurement struct {
	Phase          string `json:"phase"`
	Milliseconds   int64  `json:"milliseconds"`
	LiveHeapBytes  uint64 `json:"liveHeapBytes"`
	AllocatedBytes uint64 `json:"allocatedBytes"`
}

func main() {
	rows := flag.Int("rows", 5_000_001, "synthetic data records excluding the header")
	out := flag.String("out", "validation/"+buildinfo.Version+"/private/large-data-probe", "private evidence directory")
	flag.Parse()
	if err := run(*rows, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(count int, output string) error {
	if count < 1 || count >= formats.ExternalRows {
		return fmt.Errorf("invalid probe row count")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	for name, dir := range map[string]string{"LOCALAPPDATA": "cache", "APPDATA": "config", "XDG_CACHE_HOME": "cache", "XDG_CONFIG_HOME": "config"} {
		if err := os.Setenv(name, filepath.Join(output, dir)); err != nil {
			return err
		}
	}
	ctx := context.Background()
	file := filepath.Join(output, "synthetic.csv")
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	_, err = bw.WriteString("编号,金额,分组\n")
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		group := "common"
		if i%1000 == 0 {
			group = "rare"
		}
		if i == count-1 {
			group = "tail-marker"
		}
		if _, err := fmt.Fprintf(bw, "00000000000000000123,100.01,%s\n", group); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	digest := func() (string, error) {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		h := sha256.New()
		_, err = io.Copy(h, f)
		return fmt.Sprintf("%x", h.Sum(nil)), err
	}
	before, err := digest()
	if err != nil {
		return err
	}
	var results []measurement
	measure := func(name string, run func() error) error {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		if err := run(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		m := measurement{name, time.Since(start).Milliseconds(), after.HeapAlloc, after.TotalAlloc - before.TotalAlloc}
		results = append(results, m)
		data, _ := json.Marshal(m)
		fmt.Println(string(data))
		return nil
	}
	var wb *workbook.Workbook
	var idx *workbook.SheetIndex
	if err := measure("cold-open-index", func() error {
		var err error
		wb, err = workbook.OpenWorkbook(file)
		if err != nil {
			return err
		}
		idx, err = wb.BuildIndex(wb.Sheets[0], nil, nil)
		if err == nil && (idx.RowCount() != count+1 || idx.MaxRow != count+1) {
			return fmt.Errorf("shape mismatch")
		}
		return err
	}); err != nil {
		return err
	}
	if idx.RowsMapped() != (idx.RowCount() > workbook.MaxResidentRowOffsets) {
		return fmt.Errorf("unexpected row index storage for %d rows", idx.RowCount())
	}
	indexStat, _ := os.Stat(idx.IndexPath)
	dataStat, _ := os.Stat(idx.XMLPath)
	if err := measure("cached-reopen-index", func() error {
		again, err := workbook.OpenWorkbook(file)
		if err != nil {
			return err
		}
		loaded, err := again.BuildIndex(again.Sheets[0], nil, nil)
		if err != nil {
			return err
		}
		if loaded.RowCount() != idx.RowCount() {
			return fmt.Errorf("cached count mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := measure("random-viewport-100x80", func() error {
		for i := 0; i < 100; i++ {
			at := (i * 104729) % max(1, idx.RowCount()-80)
			got, err := wb.ReadRowsContext(ctx, idx, at, min(80, idx.RowCount()-at), 1, 3)
			if err != nil {
				return err
			}
			if len(got) == 0 {
				return fmt.Errorf("empty viewport")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := measure("full-search-tail", func() error {
		hits, truncated, err := wb.SearchSheet(idx, wb.Sheets[0].Name, "tail-marker", "value", 10, nil, nil)
		if err != nil {
			return err
		}
		if truncated || len(hits) != 1 || hits[0].Row != count+1 {
			return fmt.Errorf("search mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := measure("condition-filter", func() error {
		hits, err := wb.FilterSheet(idx, []workbook.FilterRule{{Column: 3, Operator: "equals", Value: "rare"}}, nil, nil)
		if err != nil {
			return err
		}
		want := (count-1)/1000 + 1
		if (count-1)%1000 == 0 {
			want--
		}
		if len(hits) != want {
			return fmt.Errorf("filter got %d want %d", len(hits), want)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := measure("column-distinct", func() error {
		values, truncated, err := wb.DistinctColumnValues(idx, 3, 1, 2500, nil)
		if err != nil {
			return err
		}
		if truncated || len(values) != 3 {
			return fmt.Errorf("distinct mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if idx.RowCount() > formats.LargeDataRows {
		if err := measure("large-stable-external-sort", func() error {
			order, err := wb.FilterRowOrder(ctx, idx, nil, workbook.FilterOptions{HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 2, Mode: "number"}}}, nil)
			if err != nil {
				return err
			}
			defer order.Close()
			if order.Len() != count+1 {
				return fmt.Errorf("sort count mismatch")
			}
			for i := 0; i < order.Len(); i++ {
				if order.At(i) != i {
					return fmt.Errorf("equal sort keys changed source order at %d", i)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err := measure("complete-filter-without-materializing-rows", func() error {
			order, err := wb.FilterRowOrder(ctx, idx, []workbook.FilterRule{{Column: 1, Operator: "contains", Value: "000"}}, workbook.FilterOptions{HeaderRow: 1}, nil)
			if err != nil {
				return err
			}
			defer order.Close()
			if order.Len() != count+1 || order.At(count) != count {
				return fmt.Errorf("full filter count/address mismatch")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := measure("streaming-group-count", func() error {
		scan := func(ctx context.Context, _ []string, visit func(analysis.Record) error) error {
			positions := func(yield func(int) bool) {
				for i := 1; i < idx.RowCount(); i++ {
					if !yield(i) {
						return
					}
				}
			}
			return wb.VisitRowsContext(ctx, idx, iter.Seq[int](positions), 3, 3, func(row workbook.RowData) error {
				return visit(analysis.Record{Number: row.Number, Values: map[string]analysis.Value{"C3": {Text: row.Cells[0].Raw}}})
			})
		}
		result, err := analysis.Run(ctx, analysis.Query{Source: "sheet", Rows: []string{"C3"}, Aggregate: "count"}, []analysis.Field{{ID: "C3", Name: "分组", Column: 3}}, scan, nil)
		if err != nil {
			return err
		}
		if result.Scanned != int64(count) || len(result.Rows) != 3 {
			return fmt.Errorf("analysis mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := measure("complete-streaming-csv-export", func() error {
		return exporter.Write(ctx, io.Discard, wb, []exporter.Selection{{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: 3}}, exporter.Options{Format: "csv", Mode: "values"}, nil)
	}); err != nil {
		return err
	}
	after, err := digest()
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("source changed")
	}
	resident := idx.RowCount()
	if idx.RowsMapped() {
		resident = 0
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "rows": count + 1, "columns": 3, "sourceSHA256": before, "sourceUnchanged": true, "indexBytes": indexStat.Size(), "rowCacheBytes": dataStat.Size(), "residentOffsets": resident, "phases": results, "billionRecordBenchmark": false, "desktopManualAcceptance": false}
	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(filepath.Join(output, "result.json"), append(data, '\n'), 0600)
}
