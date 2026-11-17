package server

import (
	"archive/zip"
	"context"
	"excellens/internal/analysis"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// Opt-in real-workbook benchmark. Never logs any business cell values and
// never modifies the input, including its timestamps or VBA package parts.
// EXCELLENS_ANALYSIS_REAL_PATH=/path/to/book.xlsx go test ... -run TestAnalysisRealWorkbook
func TestAnalysisRealWorkbook(t *testing.T) {
	path := os.Getenv("EXCELLENS_ANALYSIS_REAL_PATH")
	if path == "" {
		t.Skip("opt-in actual workbook")
	}
	isolateTestStorage(t)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	largestName := ""
	var largestSize uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > largestSize && len(f.Name) > 13 && f.Name[:13] == "xl/worksheets" && len(f.Name) > 4 && f.Name[len(f.Name)-4:] == ".xml" {
			largestName = f.Name
			largestSize = f.UncompressedSize64
		}
	}
	zr.Close()
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	begin := time.Now()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	opened := time.Since(begin)
	wb := app.currentWorkbook()
	var name string
	for _, m := range wb.Sheets {
		if m.Path == largestName {
			name = m.Name
			break
		}
	}
	if name == "" {
		t.Fatalf("cannot locate largest sheet %s", largestName)
	}
	meta, _ := wb.FindSheet(name)
	begin = time.Now()
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	indexed := time.Since(begin)
	begin = time.Now()
	src, err := app.source(wb, "wb:"+name, 1)
	if err != nil {
		t.Fatal(err)
	}
	header := time.Since(begin)
	q := analysis.Query{Source: "wb:" + name, HeaderRow: 1, Aggregate: "count", Precision: 0}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	begin = time.Now()
	res, err := analysis.Run(context.Background(), q, src.fields, src.scan, nil)
	duration := time.Since(begin)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned <= 0 || res.Included != res.Scanned {
		t.Fatalf("row count invalid %+v", res)
	}
	fmt.Printf("ANALYTICS_REAL file_mib=%.1f max_sheet_xml_mib=%.1f sheet_rows=%d source_fields=%d open_ms=%d index_ms=%d header_ms=%d count_scan_ms=%d allocated_mib=%.1f\n", float64(mustFileSize(path))/1048576, float64(largestSize)/1048576, res.Scanned, len(src.fields), opened.Milliseconds(), indexed.Milliseconds(), header.Milliseconds(), duration.Milliseconds(), float64(after.TotalAlloc-before.TotalAlloc)/1048576)
}
func mustFileSize(p string) int64 { st, _ := os.Stat(p); return st.Size() }
