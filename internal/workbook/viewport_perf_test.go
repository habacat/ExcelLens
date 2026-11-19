package workbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeViewportBenchmark(b *testing.B) (*Workbook, *SheetIndex) {
	b.Helper()
	file := filepath.Join(b.TempDir(), "viewport.xml")
	var buf strings.Builder
	idx := &SheetIndex{XMLPath: file, SharedAnchors: make(map[string]SharedFormulaAnchor)}
	for row := 1; row <= 6000; row++ {
		start := int64(buf.Len())
		fmt.Fprintf(&buf, `<row r="%d">`, row)
		for col := 1; col <= 32; col++ {
			fmt.Fprintf(&buf, `<c r="%s%d"><v>%d.%02d</v></c>`, ColumnName(col), row, row*col, col)
		}
		buf.WriteString("</row>")
		idx.ResidentRows = append(idx.ResidentRows, RowOffset{Number: row, Start: start, End: int64(buf.Len())})
	}
	idx.XMLSize = int64(buf.Len())
	if err := os.WriteFile(file, []byte(buf.String()), 0600); err != nil {
		b.Fatal(err)
	}
	return &Workbook{NumFmts: make(map[int]string)}, idx
}

func BenchmarkViewportRead(b *testing.B) {
	wb, idx := makeViewportBenchmark(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := wb.ReadRowsContext(context.Background(), idx, 2600, 80, 2, 27)
		if err != nil || len(rows) != 80 {
			b.Fatalf("rows=%d error=%v", len(rows), err)
		}
	}
}

func BenchmarkFilteredSortThreeKeys(b *testing.B) {
	wb, idx := makeViewportBenchmark(b)
	opts := FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 2}, {Column: 9, Descending: true}, {Column: 25}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		positions, err := wb.FilterSheetWithOptions(idx, nil, opts, nil, nil)
		if err != nil || len(positions) != 6000 {
			b.Fatalf("positions=%d err=%v", len(positions), err)
		}
	}
}

func BenchmarkCachedSort100k(b *testing.B) {
	const n = 100000
	idx := &SheetIndex{ResidentRows: make([]RowOffset, n), XMLSize: 1}
	wb := &Workbook{}
	cols := []int{2, 4, 9}
	for _, col := range cols {
		view := beginColumnCache(n)
		for i := range idx.ResidentRows {
			idx.ResidentRows[i].Number = i + 1
			value := fmt.Sprintf("%06d", (i*1789+col*11)%20000)
			view = writeColumnCache(view, i, CellData{Display: value}, true)
		}
		wb.cacheColumn(idx, col, view)
	}
	opts := FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 2}, {Column: 4, Descending: true}, {Column: 9}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := wb.FilterSheetWithOptions(idx, nil, opts, nil, nil)
		if err != nil || len(got) != n {
			b.Fatalf("got=%d err=%v", len(got), err)
		}
	}
}

// Benchmark the common unstyled numeric path without changing cached formula
// bytes or the General display semantics used by ExcelLens.
func BenchmarkGeneralNumericDisplay(b *testing.B) {
	w := &Workbook{Styles: []StyleInfo{{NumFmtID: 0}}}
	values := []string{"123456789.01234", "23.005", "9007199254740993", "-1234.125", "2.35e12", "1.042336", "98765432", "0.0000000000123"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := values[i%len(values)]
		if w.formatNumeric(v, 0) != v {
			b.Fatal(v)
		}
	}
}
