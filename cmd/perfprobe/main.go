// A local-only performance probe. Does not print workbook cell values or write to source.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"excellens/internal/workbook"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"time"
)

func stats(label string, before time.Time) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("%s: time=%s live_heap=%.1fMiB total_alloc=%.1fMiB gc=%d\n", label, time.Since(before), float64(m.HeapAlloc)/(1<<20), float64(m.TotalAlloc)/(1<<20), m.NumGC)
}
func main() {
	f := flag.String("file", "", "input xlsx/xlsm")
	index := flag.Bool("index", true, "build largest sheet index")
	search := flag.Bool("search", false, "search first 4000 indexed rows")
	sortFlag := flag.Bool("sort", false, "sort 3 sparse columns")
	digestFlag := flag.Bool("digest", false, "hash exact metadata/rows/cell data without exposing cell values")
	profile := flag.String("cpuprofile", "", "CPU profile path")
	flag.Parse()
	if *profile != "" {
		f, e := os.Create(*profile)
		if e != nil {
			panic(e)
		}
		if e = pprof.StartCPUProfile(f); e != nil {
			panic(e)
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	if *f == "" {
		fmt.Fprintln(os.Stderr, "missing -file")
		os.Exit(2)
	}
	start := time.Now()
	wb, err := workbook.OpenWorkbook(*f)
	if err != nil {
		panic(err)
	}
	stats("open", start)
	if !*index {
		return
	}
	zr, err := zip.OpenReader(*f)
	if err != nil {
		panic(err)
	}
	defer zr.Close()
	sizes := make(map[string]uint64, len(zr.File))
	for _, v := range zr.File {
		sizes[v.Name] = v.UncompressedSize64
	}
	sort.Slice(wb.Sheets, func(i, j int) bool { return sizes[wb.Sheets[i].Path] > sizes[wb.Sheets[j].Path] })
	meta := wb.Sheets[0]
	fmt.Printf("largest_sheet=%q inflatedMiB=%.1f sheetCount=%d\n", meta.Name, float64(sizes[meta.Path])/(1<<20), len(wb.Sheets))
	start = time.Now()
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		panic(err)
	}
	stats("index", start)
	fmt.Printf("indexed_rows=%d maxRow=%d maxCol=%d\n", idx.RowCount(), idx.MaxRow, idx.MaxCol)
	if *digestFlag {
		h := sha256.New()
		fmt.Fprintf(h, "%s|%d|%d|%d|%s\n", idx.Dimension, idx.MaxRow, idx.MaxCol, idx.XMLSize, meta.Name)
		for _, r := range idx.RowOffsets() {
			var b [24]byte
			binary.LittleEndian.PutUint64(b[0:8], uint64(r.Number))
			binary.LittleEndian.PutUint64(b[8:16], uint64(r.Start))
			binary.LittleEndian.PutUint64(b[16:24], uint64(r.End))
			h.Write(b[:])
		}
		anchors, _ := json.Marshal(idx.SharedAnchors)
		h.Write(anchors)
		// Deterministically spread 64 sample rows across even the largest sheet.
		ids := make([]int, 0, 64)
		limit := idx.RowCount()
		for j := 0; j < 64 && j < limit; j++ {
			n := j * (limit - 1) / max(1, min(63, limit-1))
			if len(ids) == 0 || ids[len(ids)-1] != n {
				ids = append(ids, n)
			}
		}
		sample, err := wb.ReadRowsByIndices(idx, ids, 1, idx.MaxCol)
		if err != nil {
			panic(err)
		}
		for _, r := range sample {
			b, e := json.Marshal(r)
			if e != nil {
				panic(e)
			}
			h.Write(b)
		}
		fmt.Printf("fidelity_digest=%x sample_rows=%d\n", h.Sum(nil), len(sample))
	}

	if idx.RowCount() == 0 {
		return
	}
	for _, n := range []int{55, 100, 220} {
		start = time.Now()
		lo := idx.RowCount() / 2
		if lo+n > idx.RowCount() {
			lo = idx.RowCount() - n
		}
		if lo < 0 {
			lo = 0
		}
		r, err := wb.ReadRows(idx, lo, n, 1, 40)
		if err != nil {
			panic(err)
		}
		stats(fmt.Sprintf("viewport_%d(actual_%d)", n, len(r)), start)
	}
	if *sortFlag {
		for attempt := 1; attempt <= 2; attempt++ {
			start = time.Now()
			result, err := wb.FilterSheetWithOptions(idx, nil, workbook.FilterOptions{HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 2}, {Column: 20, Descending: true}, {Column: idx.MaxCol}}}, nil, nil)
			if err != nil {
				panic(err)
			}
			h := sha256.New()
			var v [8]byte
			for _, x := range result {
				binary.LittleEndian.PutUint64(v[:], uint64(x))
				h.Write(v[:])
			}
			digest := h.Sum(nil)
			fmt.Printf("sort_hash=%x\n", digest[:12])
			stats(fmt.Sprintf("sort_3_columns_run%d(actual_%d)", attempt, len(result)), start)
		}
	}

	if *search {
		piece := workbook.SheetIndex{SourceFormat: idx.SourceFormat, SheetPath: idx.SheetPath,
			XMLPath: idx.XMLPath, IndexPath: idx.IndexPath, Dimension: idx.Dimension,
			MaxRow: idx.MaxRow, MaxCol: idx.MaxCol, XMLSize: idx.XMLSize,
			SharedAnchors: idx.SharedAnchors, HyperlinkStart: idx.HyperlinkStart,
			ResidentRows: make([]workbook.RowOffset, min(idx.RowCount(), 4000))}
		for i := range piece.ResidentRows {
			piece.ResidentRows[i] = idx.RowAt(i)
		}
		start = time.Now()
		r, _, err := wb.SearchSheet(&piece, meta.Name, "__bench__never__matches__", "value", 20, nil, nil)
		if err != nil {
			panic(err)
		}
		stats(fmt.Sprintf("search_4000(matches_%d)", len(r)), start)
	}
}
