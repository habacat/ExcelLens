// sortprobe validates the actual adapter/filter/order/export path with generated
// records. Instrumented synthetic timings are not desktop or billion-row QA.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"excellens/internal/buildinfo"
	exporter "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type measurement struct {
	Phase              string `json:"phase"`
	Milliseconds       int64  `json:"milliseconds"`
	SampledPeakHeap    uint64 `json:"sampledPeakGoHeapBytes"`
	AllocatedBytes     uint64 `json:"allocatedBytes"`
	SampledQueryBytes  int64  `json:"sampledPeakQueryFileBytes"`
	SampledQueryFiles  int    `json:"sampledPeakQueryFileCount"`
	RetainedQueryBytes int64  `json:"retainedQueryFileBytes"`
}

var amounts = []string{"9007199254740993.31", "9007199254740993.30", "57%", "0.57", "(2.00)", "1234.50", "0002", "", "文字"}

func main() {
	rows := flag.Int("rows", 5_000_001, "data records, excluding the header")
	out := flag.String("out", "validation/"+buildinfo.Version+"/private/sort-probe", "isolated generated data directory")
	flag.Parse()
	if err := run(*rows, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func queryUsage(dir string) (bytes int64, count int) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && !info.IsDir() {
			bytes += info.Size()
			count++
		}
	}
	return
}

func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil)), err
}

func run(count int, output string) error {
	if count < 262145 || count >= formats.ExternalRows {
		return errors.New("probe requires at least 262145 data records within the source capacity")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	for name, child := range map[string]string{"LOCALAPPDATA": "cache", "APPDATA": "config", "XDG_CACHE_HOME": "cache", "XDG_CONFIG_HOME": "config"} {
		if err := os.Setenv(name, filepath.Join(output, child)); err != nil {
			return err
		}
	}
	file := filepath.Join(output, "synthetic-sort.csv")
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	_, err = bw.WriteString("顺序,金额,项目,分组,编号\n")
	for i := 0; i < count && err == nil; i++ {
		_, err = fmt.Fprintf(bw, "%d,%s,项目%d,组%d,000%016d\n", count-i, amounts[i%len(amounts)], i%1000, i%7, i)
	}
	if err == nil {
		err = bw.Flush()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	before, err := digest(file)
	if err != nil {
		return err
	}
	var wb *workbook.Workbook
	var idx *workbook.SheetIndex
	var results []measurement
	measure := func(name string, work func() error) error {
		runtime.GC()
		var initial, final runtime.MemStats
		runtime.ReadMemStats(&initial)
		peak := initial.HeapAlloc
		var diskPeak int64
		var filesPeak int
		queryDir := ""
		if wb != nil {
			queryDir = filepath.Join(wb.CacheDir, "queries")
		}
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					var sample runtime.MemStats
					runtime.ReadMemStats(&sample)
					peak = max(peak, sample.HeapAlloc)
					if queryDir != "" {
						bytes, files := queryUsage(queryDir)
						diskPeak, filesPeak = max(diskPeak, bytes), max(filesPeak, files)
					}
				}
			}
		}()
		start := time.Now()
		err := work()
		elapsed := time.Since(start).Milliseconds()
		close(done)
		wg.Wait()
		runtime.ReadMemStats(&final)
		peak = max(peak, final.HeapAlloc)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		var retained int64
		if wb != nil {
			var files int
			retained, files = queryUsage(filepath.Join(wb.CacheDir, "queries"))
			diskPeak, filesPeak = max(diskPeak, retained), max(filesPeak, files)
		}
		m := measurement{name, elapsed, peak, final.TotalAlloc - initial.TotalAlloc, diskPeak, filesPeak, retained}
		results = append(results, m)
		data, _ := json.Marshal(m)
		fmt.Println(string(data))
		return nil
	}
	if err := measure("open-index", func() error {
		var err error
		wb, err = workbook.OpenWorkbook(file)
		if err != nil {
			return err
		}
		idx, err = wb.BuildIndex(wb.Sheets[0], nil, nil)
		if err == nil && (idx.RowCount() != count+1 || idx.MaxRow != count+1 || count > 262144 && len(idx.Rows) != 0) {
			return errors.New("index shape or residency mismatch")
		}
		return err
	}); err != nil {
		return err
	}
	defer wb.ClearIndexes()
	queries := filepath.Join(wb.CacheDir, "queries")
	for _, name := range []string{"complete-filter", "sparse-filter"} {
		if err := measure(name, func() error {
			rules := []workbook.FilterRule{{Column: 1, Operator: "notBlank"}}
			if name == "sparse-filter" {
				rules = []workbook.FilterRule{{Column: 4, Operator: "in", Values: []string{"组1", "组3", "组5"}}}
			}
			order, err := wb.FilterRowOrder(context.Background(), idx, rules, workbook.FilterOptions{HeaderRow: 1}, nil)
			if err != nil {
				return err
			}
			defer order.Close()
			next := 0
			for source := 0; source <= count; source++ {
				if name == "sparse-filter" && source != 0 && (source-1)%7 != 1 && (source-1)%7 != 3 && (source-1)%7 != 5 {
					continue
				}
				if next >= order.Len() || order.At(next) != source {
					return fmt.Errorf("filter source position %d missing", source)
				}
				next++
			}
			if order.Len() != next {
				return errors.New("filter returned extra records")
			}
			if name == "complete-filter" {
				if bytes, files := queryUsage(queries); bytes != 0 || files != 0 {
					return errors.New("contiguous full result allocated a query file")
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	var order *workbook.RowOrder
	verify := func() error {
		if order.Len() != count+1 || order.At(0) != 0 {
			return errors.New("sorted shape/header mismatch")
		}
		for i := 1; i <= count; i++ {
			if order.At(i) != count-i+1 {
				return fmt.Errorf("sorted position %d = %d", i, order.At(i))
			}
		}
		for i := 1; i <= count; i += 104729 {
			if got, ok := order.Position(i); !ok || got != count-i+1 {
				return fmt.Errorf("source lookup %d = %d / %v", i, got, ok)
			}
		}
		lease, err := order.Retain()
		if err != nil {
			return err
		}
		if err := order.Close(); err != nil {
			_ = lease.Close()
			return err
		}
		order = lease
		return nil
	}
	keys := []workbook.SortKey{{Column: 1, Mode: "number"}}
	for _, name := range []string{"one-key-external-sort", "five-key-external-sort"} {
		if name == "five-key-external-sort" {
			keys = append(keys, workbook.SortKey{Column: 2, Mode: "number", Descending: true}, workbook.SortKey{Column: 3, Mode: "natural"}, workbook.SortKey{Column: 4, Mode: "text"}, workbook.SortKey{Column: 5})
		}
		if err := measure(name, func() error {
			var err error
			order, err = wb.FilterRowOrder(context.Background(), idx, nil, workbook.FilterOptions{HeaderRow: 1, SortKeys: keys}, nil)
			return err
		}); err != nil {
			return err
		}
		if err := verify(); err != nil {
			return err
		}
		if name == "one-key-external-sort" {
			if err := order.Close(); err != nil {
				return err
			}
		}
	}
	defer func() { _ = order.Close() }()
	exported := filepath.Join(output, "sorted.csv")
	if err := measure("complete-sorted-csv-export", func() error {
		f, err := os.Create(exported)
		if err != nil {
			return err
		}
		err = exporter.Write(context.Background(), f, wb, []exporter.Selection{{Sheet: wb.Sheets[0], Index: idx, Order: order, Filtered: true, Compact: true, FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: 5}}, exporter.Options{Format: "csv", Mode: "values"}, nil)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}); err != nil {
		return err
	}
	if err := measure("verify-every-exported-record", func() error {
		f, err := os.Open(exported)
		if err != nil {
			return err
		}
		defer f.Close()
		r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
		if _, err := r.Read(); err != nil {
			return err
		}
		for i := 1; i <= count; i++ {
			row, err := r.Read()
			source := count - i
			if err != nil || len(row) != 5 || row[0] != strconv.Itoa(i) || row[1] != amounts[source%len(amounts)] || row[4] != fmt.Sprintf("000%016d", source) {
				return fmt.Errorf("export row %d mismatch: %v / %v", i, row, err)
			}
		}
		if _, err := r.Read(); err != io.EOF {
			return errors.New("export had extra records")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := order.Close(); err != nil {
		return err
	}
	for _, phase := range []string{"cancel-during-scan", "cancel-during-merge"} {
		if err := measure(phase, func() error {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := workbook.FilterOptions{HeaderRow: 1, SortKeys: keys, Phase: func(stage string, _, _ int) {
				if phase == "cancel-during-merge" && stage == "正在归并排序结果" {
					cancel()
				}
			}}
			result, err := wb.FilterRowOrder(ctx, idx, nil, opts, func(done, total int) {
				if phase == "cancel-during-scan" && done > total/2 {
					cancel()
				}
			})
			_ = result.Close()
			if !errors.Is(err, context.Canceled) || result != nil {
				return fmt.Errorf("cancellation state mismatch: %v", err)
			}
			if bytes, files := queryUsage(queries); bytes != 0 || files != 0 {
				return fmt.Errorf("cancelled query retained %d files / %d bytes", files, bytes)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	after, err := digest(file)
	if err != nil || before != after {
		return fmt.Errorf("source changed: %v", err)
	}
	indexInfo, _ := os.Stat(idx.IndexPath)
	dataInfo, _ := os.Stat(idx.XMLPath)
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "dataRows": count, "columns": 5,
		"sourceSHA256": before, "sourceUnchanged": true, "allSortPositionsVerified": true, "allExportedRecordsVerified": true,
		"indexBytes": indexInfo.Size(), "rowCacheBytes": dataInfo.Size(), "queryMemoryBudgetBytes": formats.QuerySortMemoryBytes,
		"sampleIntervalMs": 100, "heapExcludesMappingsAndOSFileCache": true, "timingsAreInstrumentedSynthetic": true,
		"indexMayReuseCache": true, "allFilterPositionsVerified": true,
		"billionRecordBenchmark": false, "desktopManualAcceptance": false, "phases": results}
	data, _ := json.MarshalIndent(report, "", "  ")
	return os.WriteFile(filepath.Join(output, "result.json"), append(data, '\n'), 0600)
}
