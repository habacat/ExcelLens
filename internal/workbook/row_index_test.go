package workbook

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"excellens/internal/formats"
)

func releaseTestIndex(t testing.TB, idx *SheetIndex) {
	t.Helper()
	if idx.mappedRows != nil {
		mapping := idx.mappedRows
		t.Cleanup(func() {
			runtime.SetFinalizer(mapping, nil)
			mapping.release()
		})
	}
}

func TestSpilledIndexPreservesBillionthRowAndCacheCompatibility(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "data.rows"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := newExternalRowWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.offsets.close()
	for i := 0; i <= residentRowOffsets; i++ {
		if err := writer.row([]string{"ordinary", "0000123"}); err != nil {
			t.Fatal(err)
		}
	}
	if writer.offsets.file == nil || writer.offsets.rows != nil {
		t.Fatal("offsets did not spill")
	}
	// This is a sparse boundary fixture, not a billion-record throughput test.
	writer.rows = formats.ExternalRows - 1
	if err := writer.row([]string{"tail-value", "00000000000000000123"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.row([]string{"one-too-many"}); err == nil {
		t.Fatal("row limit ignored")
	}
	if err := writer.cells([]externalCell{{Col: 1, Raw: "one-too-many"}}, 1); err == nil {
		t.Fatal("styled row limit ignored")
	}
	if err := writer.finish(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	idx := &SheetIndex{XMLPath: f.Name(), IndexPath: filepath.Join(dir, "data.idx"), Dimension: writer.dimension(), MaxRow: writer.rows, MaxCol: writer.cols, XMLSize: writer.count, HyperlinkStart: -1, rowSpool: writer.offsets.file, spooledRows: writer.offsets.count}
	if err := saveIndex(idx); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadIndex(idx.IndexPath, idx.XMLPath, "sheet")
	if err != nil {
		t.Fatal(err)
	}
	releaseTestIndex(t, loaded)
	owner := &Workbook{indexes: map[string]*SheetIndex{"sheet": loaded}}
	if !owner.HasMappedIndexes() {
		t.Fatal("mapped cache was not protected")
	}
	owner.ClearIndexes()
	if owner.HasMappedIndexes() {
		t.Fatal("discarded snapshot remained owned")
	}
	if loaded.mappedRows == nil || loaded.Rows != nil || loaded.RowCount() != residentRowOffsets+2 {
		t.Fatal("large index was materialized", loaded.RowCount())
	}
	last := loaded.RowCount() - 1
	if loaded.FindRowOffset(formats.ExternalRows) != last || loaded.RowAt(last).Number != formats.ExternalRows {
		t.Fatal("large address was truncated")
	}
	wb := &Workbook{Format: "csv"}
	rows, err := wb.ReadRowsContext(context.Background(), loaded, last, 1, 1, 2)
	if err != nil || len(rows) != 1 || rows[0].Cells[1].Ref != "B1000000000" || rows[0].Cells[1].Raw != "00000000000000000123" || rows[0].Cells[1].Type != "inlineStr" {
		t.Fatal(rows, err)
	}
	hits, truncated, err := wb.SearchSheet(loaded, "sheet", "tail-value", "value", 10, nil, nil)
	if err != nil || truncated || len(hits) != 1 || hits[0].Ref != "A1000000000" {
		t.Fatal(hits, truncated, err)
	}
	matched, err := wb.FilterSheet(loaded, []FilterRule{{Column: 1, Operator: "equals", Value: "tail-value"}}, nil, nil)
	if err != nil || len(matched) != 1 || matched[0] != last {
		t.Fatal(matched, err)
	}
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Go(func() {
			for i := 0; i < 20000; i++ {
				if loaded.RowAt(last).Number != formats.ExternalRows {
					t.Error("concurrent mapped read changed")
					return
				}
			}
		})
	}
	runtime.GC()
	group.Wait()
	data, err := os.ReadFile(idx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	// Reject a plausible but corrupt row offset and forged huge count before
	// any large heap allocation. The fixed-width row record stays unchanged.
	for name, mutate := range map[string]func([]byte){
		"checksum": func(b []byte) { b[indexRowsStart(idx.Dimension)+4] ^= 1 },
		"count": func(b []byte) {
			binary.LittleEndian.PutUint64(b[indexRowsStart(idx.Dimension)-8:], uint64(formats.ExternalRows))
		},
	} {
		copy := append([]byte(nil), data...)
		mutate(copy)
		bad := filepath.Join(dir, name+".idx")
		if err := os.WriteFile(bad, copy, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadIndex(bad, idx.XMLPath, "sheet"); err == nil {
			t.Fatal("corrupt index accepted", name)
		}
	}
}

func TestExternalAddressAndLayoutBillionBoundary(t *testing.T) {
	if MaxRowsForIndex(&SheetIndex{SourceFormat: "xls", XMLPath: "biff.rows"}) != 65536 {
		t.Fatal("BIFF source ceiling expanded")
	}
	for _, ref := range []string{"A50000001", "XFD1000000000"} {
		if _, _, err := ParseCellRef(ref); err != nil {
			t.Fatal(ref, err)
		}
	}
	for _, ref := range []string{"A1000000001", "XFE1000000000"} {
		if _, _, err := ParseCellRef(ref); err == nil {
			t.Fatal("invalid address", ref)
		}
	}
	if MaxRowsForIndex(&SheetIndex{XMLPath: "data.rows"}) != formats.ExternalRows || MaxRowsForIndex(&SheetIndex{XMLPath: "sheet.xml"}) != formats.ExcelRows {
		t.Fatal("format ceilings changed")
	}
	layout := SourceLayout{Rows: []SourceSizeRange{{Min: 1, Max: formats.ExternalRows, Size: 20}}, Merges: []CellMerge{{Row: formats.ExternalRows, Col: 1, EndRow: formats.ExternalRows, EndCol: 2}}}
	if !layout.valid() {
		t.Fatal("valid large layout rejected")
	}
	layout.Merges[0].EndRow++
	if layout.valid() {
		t.Fatal("over-limit layout accepted")
	}
}
