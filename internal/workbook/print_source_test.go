package workbook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPrintSourceJointIdentityIndexChangeAndSourceChange(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/>`+dataRuleTestValues+`</worksheet>`, ""), false)
	w, meta, idx := preparePrintFixture(t, file)
	if value, err := w.ReadPrintSource(context.Background(), meta, nil, nil); err == nil || value != nil {
		t.Fatal("implicit index preparation", value, err)
	}
	source, err := w.ReadPrintSource(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source.Identity() == "" || source.UsedRange().EndCol != 3 || source.UsedRange().EndRow != 1 || source.Geometry() == nil || source.Settings().Sheet != meta.Name {
		t.Fatal(source)
	}
	data, _ := os.ReadFile(file)
	other := filepath.Join(t.TempDir(), "same.xlsx")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	w2, meta2, idx2 := preparePrintFixture(t, other)
	source2, err := w2.ReadPrintSource(context.Background(), meta2, idx2, nil)
	if err != nil || source2.Identity() == source.Identity() {
		t.Fatal("content-only identity hid a different path", err)
	}
	w.ClearIndexes()
	if err := source.Validate(context.Background()); err == nil {
		t.Fatal("old exact index binding survived replacement")
	}
	changed := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/></worksheet>`, ""), false)
	changedBytes, _ := os.ReadFile(changed)
	if err := os.WriteFile(other, changedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source2.Validate(context.Background()); err == nil {
		t.Fatal("changed source remained valid")
	}
}

func TestPrintSourcePublicationRejectsMidReadIndexReplacementAndCancellation(t *testing.T) {
	isolateTestStorage(t)
	for _, cancelled := range []bool{false, true} {
		file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/>`+dataRuleTestValues+`</worksheet>`, ""), false)
		w, meta, idx := preparePrintFixture(t, file)
		ctx, cancel := context.WithCancel(context.Background())
		var once sync.Once
		value, err := w.ReadPrintSource(ctx, meta, idx, func(int64, int64, string) {
			once.Do(func() {
				if cancelled {
					cancel()
				} else {
					w.ClearIndexes()
				}
			})
		})
		cancel()
		if err == nil || value != nil {
			t.Fatal("late invalid source published", value, err)
		}
		if cancelled && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestPrintGeometryDerivedDefaultsShareSourceAndNeverRepairInvalidSavedSizes(t *testing.T) {
	index, err := scanPrintGeometryFixture(`<worksheet><sheetData><row r="3" ht="20"/><row r="4" hidden="1"/><row r="5" ht="bad"/></sheetData></worksheet>`)
	if err != nil {
		t.Fatal(err)
	}
	before := index.Summary()
	one, err := index.ResolveDefaults(context.Background(), PrintGeometryDefaults{Row: &PrintGeometryDefault{"15", "point"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := index.ResolveDefaults(context.Background(), PrintGeometryDefaults{Row: &PrintGeometryDefault{"7", "point"}})
	if err != nil {
		t.Fatal(err)
	}
	if one.base != index || two.base != index {
		t.Fatal("derived view copied source prefix")
	}
	for _, query := range []struct {
		view     *PrintGeometryView
		expected int64
	}{{one, 50_000_000_000}, {two, 34_000_000_000}} {
		value, err := query.view.Extent(context.Background(), "row", 1, 4)
		if err != nil || value.Units != query.expected || !value.Defaulted {
			t.Fatal(value, err)
		}
		if _, err := query.view.Extent(context.Background(), "row", 5, 5); err == nil {
			t.Fatal("preview default repaired an invalid saved height")
		}
	}
	if _, err := index.Extent(context.Background(), "row", 1, 2); err == nil {
		t.Fatal("derived view changed source index")
	}
	if index.Summary().DefaultRow.RawSize != before.DefaultRow.RawSize || index.Summary().DefaultRow.Status != before.DefaultRow.Status {
		t.Fatal("derived default became saved source metadata")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				value, err := one.Extent(context.Background(), "row", 1, 3)
				if err != nil || value.Units != 50_000_000_000 {
					t.Error(value, err)
				}
			}
		}()
	}
	group.Wait()
}

func TestPrintBreakSchemaDefaultsRetainAbsentSourceAttributes(t *testing.T) {
	index, err := scanPrintFixture(`<worksheet><sheetData/><rowBreaks count="1" manualBreakCount="1"><brk id="10" man="1"/></rowBreaks></worksheet>`)
	if err != nil || !index.Snapshot().Complete {
		t.Fatal(index, err)
	}
	item := index.Snapshot().RowBreaks[0]
	if item.Min != 0 || item.Max != 0 || item.Status != "stored" || item.Index != 10 {
		t.Fatal(item)
	}
	if _, found := item.Attributes["max"]; found {
		t.Fatal("schema default injected into source attributes")
	}
	if !bytes.Contains([]byte(index.Snapshot().Definitions[0].RawXML), []byte(`<brk id="10" man="1"/>`)) {
		t.Fatal("source definition changed")
	}
}

func TestPrintSourceBIFFUsesItsOwnPreparedRowsAndImmutableMetadata(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "source.xls")
	writeBIFF8Fixture(t, file)
	w, meta, idx := preparePrintFixture(t, file)
	before, _ := os.ReadFile(file)
	source, err := w.ReadPrintSource(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source.Format() != "xls" || source.Identity() == "" || source.UsedRange().EndRow != 3 {
		t.Fatal(source)
	}
	row, err := w.ReadRow(idx, 1, 1, 2)
	if err != nil || row.Cells[0].Raw != "000000123456789" {
		t.Fatal(row, err)
	}
	view, err := source.Geometry().ResolveDefaults(context.Background(), PrintGeometryDefaults{Row: &PrintGeometryDefault{"15", "point"}, Column: &PrintGeometryDefault{"8", "base-char"}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := view.Extent(context.Background(), "row", 1, 3)
	if err != nil || value.Units != 45_000_000_000 || !value.Defaulted {
		t.Fatal(value, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("joint source read changed BIFF file")
	}
}
