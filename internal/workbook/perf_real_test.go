package workbook

import (
	"archive/zip"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRealSearchPerformance(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("requires workbook fixture")
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.OpenReader(file)
	defer zr.Close()
	var biggest string
	var size uint64
	for _, sh := range wb.Sheets {
		for _, f := range zr.File {
			if f.Name == sh.Path && f.UncompressedSize64 > size {
				size = f.UncompressedSize64
				biggest = sh.Name
			}
		}
	}
	sh, _ := wb.FindSheet(biggest)
	t.Logf("largest sheet %q XML %.1f MB", biggest, float64(size)/1e6)
	t0 := time.Now()
	idx, err := wb.BuildIndex(sh, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("index %s rows=%d", time.Since(t0), len(idx.ResidentRows))
	n := len(idx.ResidentRows)
	if n > 20000 {
		n = 20000
	}
	piece := *idx
	piece.ResidentRows = idx.ResidentRows[:n]
	for _, mode := range []string{"formula", "value"} {
		t0 = time.Now()
		result, truncated, err := wb.SearchSheet(&piece, sh.Name, "__non_existing_query_xyz_999", mode, 500, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(fmt.Sprintf("baseline %s %d rows took %s results=%d truncated=%v", mode, n, time.Since(t0), len(result), truncated))
	}
}

func TestRealSearchParity(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("requires workbook fixture")
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	var sh SheetMeta
	for _, v := range wb.Sheets {
		if v.Name == "最低资本EAST交易和资金流水表 " {
			sh = v
			break
		}
	}
	if sh.Name == "" {
		t.Skip("target not present")
	}
	idx, err := wb.BuildIndex(sh, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	piece := *idx
	if len(piece.ResidentRows) > 60 {
		piece.ResidentRows = piece.ResidentRows[:60]
	}
	for _, tt := range []struct{ query, mode string }{{"INDEX(", "formula"}, {"__no_matching_xyz__", "formula"}, {"2000", "value"}, {"__no_matching_xyz__", "value"}, {"A1", "reference"}} {
		t.Run(tt.mode+"-"+tt.query, func(t *testing.T) {
			start := time.Now()
			got, gt, err := wb.SearchSheet(&piece, sh.Name, tt.query, tt.mode, 80, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			optimized := time.Since(start)
			start = time.Now()
			want, wt, err := wb.searchSheetLegacy(&piece, sh.Name, tt.query, tt.mode, 80, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			baseline := time.Since(start)
			if gt != wt || len(got) != len(want) {
				t.Fatalf("mode=%s query=%q mismatched counts fast=%d slow=%d truncFast=%v truncSlow=%v", tt.mode, tt.query, len(got), len(want), gt, wt)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("hit %d mismatch fast=%+v slow=%+v", i, got[i], want[i])
				}
			}
			t.Logf("fast=%s legacy=%s hits=%d", optimized, baseline, len(got))
		})
	}
}

func TestRealMultiSortPerformance(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("requires real XLSM fixture")
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	var sheet SheetMeta
	for _, candidate := range wb.Sheets {
		if strings.Contains(candidate.Name, "最低资本EAST交易") {
			sheet = candidate
			break
		}
	}
	if sheet.Name == "" {
		t.Skip("large sheet missing")
	}
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	positions, err := wb.FilterSheetWithOptions(idx, nil, FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 2}, {Column: 20, Descending: true}, {Column: 500}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real 3-column stable sort: rows=%d, scannedXML=%d bytes, elapsed=%s", len(positions), idx.XMLSize, time.Since(started))
	if len(positions) < 9000 {
		t.Fatalf("wrong sorted row count %d", len(positions))
	}
}
