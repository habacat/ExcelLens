package workbook

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkNativeJournalWriter(b *testing.B) {
	f, err := os.CreateTemp(b.TempDir(), "native-*.rows")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	w, err := newExternalRowWriter(f)
	if err != nil {
		b.Fatal(err)
	}
	defer w.offsets.close()
	fields := []string{"费控系统", "11110000 210923 费控系统 A 101016", "00000000000000000022", "记账", "23-09-21", "记账", "人员,", "人员,", "不需要审批", "CNY", "123456.78", "", "CNY", "123456.78", ""}
	for segment := 0; segment < 12; segment++ {
		fields = append(fields, "000000", "缺省科目")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.row(fields); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.finish(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkNativeViewportRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "viewport.rows")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	w, err := newExternalRowWriter(f)
	if err != nil {
		b.Fatal(err)
	}
	fields := make([]string, 39)
	for i := range fields {
		fields[i] = "001234567890.123456"
	}
	for row := 0; row < 6000; row++ {
		if err := w.row(fields); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.finish(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	idx := &SheetIndex{XMLPath: path, RowIndex: NewRowIndex(w.offsets.rows), XMLSize: w.count, MaxRow: w.rows, MaxCol: w.cols}
	wb := &Workbook{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := wb.ReadRowsContext(context.Background(), idx, 2600, 80, 2, 27)
		if err != nil || len(rows) != 80 {
			b.Fatalf("rows=%d error=%v", len(rows), err)
		}
	}
}

func makeNativeScanBenchmark(b *testing.B) (*Workbook, *SheetIndex) {
	b.Helper()
	f, err := os.CreateTemp(b.TempDir(), "scan-*.rows")
	if err != nil {
		b.Fatal(err)
	}
	w, err := newExternalRowWriter(f)
	if err != nil {
		b.Fatal(err)
	}
	fields := make([]string, 39)
	for i := range fields {
		fields[i] = "1234.567890"
	}
	for i := 0; i < 100000; i++ {
		if err := w.row(fields); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.finish(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return &Workbook{}, &SheetIndex{XMLPath: f.Name(), XMLSize: w.count, RowIndex: NewRowIndex(w.offsets.rows), MaxRow: w.rows, MaxCol: w.cols}
}

func BenchmarkNativeColdFilter(b *testing.B) {
	w, idx := makeNativeScanBenchmark(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.clearColumnCache()
		rows, err := w.FilterSheet(idx, []FilterRule{{Column: 2, Operator: "contains", Value: "234"}}, nil, nil)
		if err != nil || len(rows) != 100000 {
			b.Fatal(len(rows), err)
		}
	}
}

func BenchmarkNativeColdDistinct(b *testing.B) {
	w, idx := makeNativeScanBenchmark(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.clearColumnCache()
		values, truncated, err := w.DistinctColumnValues(idx, 2, 0, 10000, nil)
		if err != nil || truncated || len(values) != 1 || values[0].Count != 100000 {
			b.Fatal(values, truncated, err)
		}
	}
}
