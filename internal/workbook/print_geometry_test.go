package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"excellens/internal/formats"
)

func preparePrintGeometryFixture(t *testing.T, file string) (*Workbook, SheetMeta, *SheetIndex) {
	t.Helper()
	w, meta, idx := preparePrintFixture(t, file)
	if value, err := w.SourcePrintGeometry(context.Background(), meta, nil, nil); err == nil || value != nil || len(w.printGeometryCache) != 0 {
		t.Fatal("geometry prepared its own source index", value, err)
	}
	if len(w.printGeometryLoading) != 0 {
		t.Fatal("indexing scanned print geometry")
	}
	return w, meta, idx
}

func assertPrintExtent(t *testing.T, index *SourcePrintGeometryIndex, axis string, first, last int, units int64, approximate bool) {
	t.Helper()
	value, err := index.Extent(context.Background(), axis, first, last)
	if err != nil || value.Units != units || value.Approximate != approximate {
		t.Fatal(axis, first, last, value, err, "expected units", units)
	}
}

func TestPrintGeometryOOXMLRealSourceTinyTallSparseHiddenAndImmutablePages(t *testing.T) {
	isolateTestStorage(t)
	data := strings.Replace(dataRuleTestValues, `<row r="1">`, `<row r="1" ht="0.1">`, 1)
	data = strings.Replace(data, `</sheetData>`, `<row r="2" ht="2000"/><row r="4" hidden="1" ht="75"/><row r="5" ht="0"/><row r="6"/><row r="1000000" ht="0.0000000001"/></sheetData>`, 1)
	sheet := `<worksheet xmlns="` + catalogueTestNS + `"><sheetFormatPr defaultRowHeight="15" defaultColWidth="8.43"/><cols><col min="1" max="4" width="255"/><col min="2" max="2" hidden="1" width="8"/><col min="3" max="3" width="0"/><col min="4" max="4" width="10"/></cols>` + data + `</worksheet>`
	file := writeCatalogueZIPFixture(t, printTestParts(sheet, ""), false)
	before, _ := os.ReadFile(file)
	w, meta, idx := preparePrintGeometryFixture(t, file)
	rowBefore, _ := w.ReadRow(idx, 0, 1, 3)
	index, err := w.SourcePrintGeometry(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary := index.Summary()
	if !summary.Complete || summary.RowSegments != 6 || summary.ColumnSegments != 4 || idx.MaxRow != 1 {
		t.Fatal(summary, idx.MaxRow)
	}
	assertPrintExtent(t, index, "row", 1, 6, 2_030_100_000_000, false)
	assertPrintExtent(t, index, "row", 1000000, 1000000, 1, true)
	assertPrintExtent(t, index, "col", 1, 1, 1_338_750_000_000, true)
	assertPrintExtent(t, index, "col", 2, 3, 0, false)
	page, err := index.Page("row", 0, 100)
	if err != nil || page.Items[0].Property.RawSize != "0.1" || page.Items[1].Property.Points != 2000 || page.Items[5].First != 1000000 || page.Items[5].Property.Status != "approximate" {
		t.Fatal(page, err)
	}
	page.Items[0].Property.Attributes["ht"] = "changed"
	summary.DefaultRow.Attributes["defaultRowHeight"] = "changed"
	if err := os.Rename(idx.XMLPath, idx.XMLPath+".held"); err != nil {
		t.Fatal(err)
	}
	warm, err := w.SourcePrintGeometry(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm geometry rescanned XML") })
	if restore := os.Rename(idx.XMLPath+".held", idx.XMLPath); restore != nil {
		t.Fatal(restore)
	}
	if err != nil || warm != index {
		t.Fatal(warm, err)
	}
	page, _ = warm.Page("row", 0, 1)
	if page.Items[0].Property.Attributes["ht"] != "0.1" || warm.Summary().DefaultRow.Attributes["defaultRowHeight"] != "15" {
		t.Fatal("public geometry mutated shared data")
	}
	rowAfter, _ := w.ReadRow(idx, 0, 1, 3)
	after, _ := os.ReadFile(file)
	if !reflect.DeepEqual(rowBefore, rowAfter) || !bytes.Equal(before, after) || rowAfter.Cells[0].Raw != "001" || rowAfter.Cells[1].Raw != "9007199254740993.31" || !rowAfter.Cells[2].CacheMissing {
		t.Fatal("geometry changed source", rowAfter)
	}
}

func scanPrintGeometryFixture(text string) (*SourcePrintGeometryIndex, error) {
	b := newPrintGeometryBuilder("Data", "xlsx", "", 7)
	if err := scanPrintGeometryXML(context.Background(), strings.NewReader(text), int64(len(text)), b, nil); err != nil {
		return nil, err
	}
	return b.finish(context.Background())
}

func TestPrintGeometryDefaultHiddenMissingInvalidAndNamespaceStates(t *testing.T) {
	text := `<worksheet><sheetFormatPr defaultRowHeight="15" defaultColWidth="8.43" zeroHeight="1"/><sheetData><row r="1" hidden="0"/><row r="3" ht="100"/><row r="4" hidden="0" ht="1"/></sheetData></worksheet>`
	index, err := scanPrintGeometryFixture(text)
	if err != nil {
		t.Fatal(err)
	}
	assertPrintExtent(t, index, "row", 1, 5, 16_000_000_000, false)
	index, err = scanPrintGeometryFixture(`<worksheet><sheetData><row r="1" ht="10"/><row r="2" hidden="1"/><row r="3" ht="NaN"/><row r="4" ht="0" hidden="maybe"/></sheetData></worksheet>`)
	if err != nil || index.Summary().Complete {
		t.Fatal(index, err)
	}
	assertPrintExtent(t, index, "row", 1, 1, 10_000_000_000, false)
	assertPrintExtent(t, index, "row", 2, 2, 0, false)
	for _, row := range []int{3, 4, 5} {
		if _, err := index.Extent(context.Background(), "row", row, row); err == nil {
			t.Fatal("unknown source dimension became a default", row)
		}
	}
	for name, body := range map[string]string{
		"duplicate row":     `<sheetData><row r="1" ht="1"/><row r="1" ht="2"/></sheetData>`,
		"reversed row":      `<sheetData><row r="2"/><row r="1"/></sheetData>`,
		"invalid row":       `<sheetData><row r="0"/></sheetData>`,
		"overflow row":      `<sheetData><row r="1048577"/></sheetData>`,
		"duplicate attr":    `<sheetData><row r="1" ht="1" ht="2"/></sheetData>`,
		"foreign row":       `<sheetData><row xmlns="urn:foreign" r="1" ht="1"/></sheetData>`,
		"duplicate default": `<sheetFormatPr defaultRowHeight="15"/><sheetFormatPr defaultRowHeight="16"/><sheetData/>`,
		"bad columns":       `<cols><col min="3" max="2" width="1"/></cols><sheetData/>`,
	} {
		t.Run(name, func(t *testing.T) {
			if value, err := scanPrintGeometryFixture(`<worksheet>` + body + `</worksheet>`); err == nil || value != nil {
				t.Fatal("invalid metadata became partial geometry", value, err)
			}
		})
	}
	index, err = scanPrintGeometryFixture(`<s:worksheet xmlns:s="` + catalogueTestNS + `"><s:sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><s:sheetData><s:row r="1" ht="0.25"/></s:sheetData></s:worksheet>`)
	if err != nil {
		t.Fatal(err)
	}
	assertPrintExtent(t, index, "row", 1, 1, 250_000_000, false)
	index, err = scanPrintGeometryFixture(`<worksheet xmlns:e="urn:extra"><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><sheetData><row r="1" e:ht="20"/></sheetData></worksheet>`)
	if err != nil || index.Summary().Complete {
		t.Fatal(index, err)
	}
	if _, err := index.Extent(context.Background(), "row", 1, 1); err == nil {
		t.Fatal("foreign height became default size")
	}
}

func TestPrintGeometryOverlappingColumnsAndWidePrefixArithmetic(t *testing.T) {
	index, err := scanPrintGeometryFixture(`<worksheet><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><cols><col min="1" max="10" width="20" hidden="1"/><col min="3" max="4" width="1" hidden="0"/></cols><sheetData><row r="1" ht="9000000000"/><row r="2" ht="9000000000"/><row r="3" ht="0.000000001"/></sheetData></worksheet>`)
	if err != nil {
		t.Fatal(err)
	}
	assertPrintExtent(t, index, "row", 3, 3, 1, false)
	assertPrintExtent(t, index, "row", 4, 5, 30_000_000_000, false)
	if _, err := index.Extent(context.Background(), "row", 1, 2); err == nil {
		t.Fatal("overflowing range silently wrapped")
	}
	assertPrintExtent(t, index, "col", 1, 10, 10_500_000_000, true)
	page, _ := index.Page("col", 0, 100)
	if len(page.Items) != 3 || page.Items[1].First != 3 || page.Items[1].Last != 4 || page.Items[1].Property.Hidden {
		t.Fatal(page)
	}
	for _, query := range [][2]int{{0, 1}, {2, 1}, {1, 1048577}} {
		if _, err := index.Extent(context.Background(), "row", query[0], query[1]); err == nil {
			t.Fatal(query)
		}
	}
	for _, query := range [][2]int{{-1, 1}, {0, 0}, {0, 101}, {4, 1}} {
		if _, err := index.Page("col", query[0], query[1]); err == nil {
			t.Fatal(query)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.Extent(ctx, "row", 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPrintGeometryMoreThan100000AlternatingSegmentsAndIndependentRangeOracle(t *testing.T) {
	b := newPrintGeometryBuilder("Data", "xlsx", "", 7)
	const records = 120005
	first := b.specification(map[string]string{"ht": "0.1"}, "ht", "point", "hidden")
	second := b.specification(map[string]string{"ht": "2000"}, "ht", "point", "hidden")
	for row := 1; row <= records; row++ {
		spec := first
		if row%2 == 0 {
			spec = second
		}
		if err := b.addRow(row, spec); err != nil {
			t.Fatal(row, err)
		}
	}
	index, err := b.finish(context.Background())
	if err != nil || len(index.rows.rules) != records || len(index.rows.runs) < records || index.BudgetBytes() > formats.PrintGeometryBytes {
		t.Fatal(err, len(index.rows.rules), index.BudgetBytes())
	}
	random := rand.New(rand.NewSource(20261116))
	for range 2000 {
		first := 1 + random.Intn(records)
		last := min(records, first+random.Intn(5000))
		var expected int64
		for row := first; row <= last; row++ {
			if row%2 == 0 {
				expected += 2_000_000_000_000
			} else {
				expected += 100_000_000
			}
		}
		assertPrintExtent(t, index, "row", first, last, expected, false)
	}
	page, err := index.Page("row", 100001, 2)
	if err != nil || page.Items[0].First != 100002 || page.Items[1].Property.RawSize != "0.1" {
		t.Fatal(page, err)
	}
}

func TestPrintGeometryMeasurementsKeepInvalidAndQuantizationExplicit(t *testing.T) {
	for _, raw := range []string{"NaN", "+Inf", "-1", "0x1p2", "1/3", "1e999999999", "1e-999999999", "9223372037", strings.Repeat("1", 129)} {
		if units, status, reason := printGeometryMeasure(raw, "point", 7); units != 0 || status == "stored" || reason == "" {
			t.Fatal(raw, units, status, reason)
		}
	}
	for _, measure := range []struct {
		raw, unit string
		units     int64
		status    string
	}{
		{"0.0000000001", "point", 1, "approximate"}, {"1e-9", "point", 1, "stored"}, {"0", "point", 0, "stored"}, {" -0 ", "point", 0, "stored"}, {"2", "twip", 100_000_000, "stored"}, {"8192", "twip", 409_600_000_000, "stored"},
	} {
		if units, status, reason := printGeometryMeasure(measure.raw, measure.unit, 7); units != measure.units || status != measure.status {
			t.Fatal(measure, units, status, reason)
		}
	}
	b := newPrintGeometryBuilder("Data", "xlsx", "", 7)
	b.allocated = formats.PrintGeometryBytes
	if _, err := b.intern(printAxisSpec{raw: "new"}); err == nil {
		t.Fatal("over-budget dimensions accepted")
	}
}

func TestPrintGeometryBIFFRealTwipsDefaultPrecedenceHiddenAndSourceIntegrity(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "geometry.xls")
	row := func(number, height, flags uint16) []byte {
		data := make([]byte, 16)
		binary.LittleEndian.PutUint16(data, number)
		binary.LittleEndian.PutUint16(data[6:], height)
		binary.LittleEndian.PutUint16(data[12:], flags)
		return xlsRecord(0x0208, data)
	}
	col := make([]byte, 12)
	binary.LittleEndian.PutUint16(col[4:], 255*256)
	writeBIFF8Fixture(t, file, xlsRecord(0x0225, []byte{0, 0, 44, 1}), xlsRecord(0x0055, []byte{8, 0}), xlsRecord(0x0099, []byte{0, 10}), xlsRecord(0x007D, col),
		xlsRecord(0x0809, []byte{0, 6, 0x20, 0}), row(0, 12, 0), xlsRecord(0x000A, nil),
		xlsRecord(0x01AA, make([]byte, 64)), row(0, 12, 0), xlsRecord(0x01AB, nil),
		row(0, 2, 0), row(1, 8192, 0), row(2, 400, 0x20), row(65535, 0x8000, 0))
	before, _ := os.ReadFile(file)
	w, meta, idx := preparePrintGeometryFixture(t, file)
	index, err := w.SourcePrintGeometry(context.Background(), meta, idx, nil)
	if err != nil || index.Summary().Complete {
		t.Fatal(index, err)
	}
	assertPrintExtent(t, index, "row", 1, 3, 409_700_000_000, false)
	assertPrintExtent(t, index, "row", 4, 4, 15_000_000_000, false)
	assertPrintExtent(t, index, "col", 1, 1, 1_338_750_000_000, true)
	if _, err := index.Extent(context.Background(), "row", 65536, 65536); err == nil {
		t.Fatal("invalid BIFF high-bit height became default")
	}
	page, _ := index.Page("row", 0, 100)
	if len(page.Items) != 4 || page.Items[0].Property.RawSize != "2" || page.Items[0].Property.Unit != "twip" || page.Items[3].Property.Attributes["heightHex"] != "0080" || index.Summary().DefaultColumn.RawSize != "2560" || index.Summary().DefaultColumn.Unit != "biff-col256" {
		t.Fatal(page, index.Summary())
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
}

func TestPrintGeometrySharedCancellationCRCIdentityAndBoundedCache(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><sheetData><row r="1" ht="1"/></sheetData></worksheet>`, ""), false)
	w, meta, idx := preparePrintGeometryFixture(t, file)
	copy := *idx
	if value, err := w.SourcePrintGeometry(context.Background(), meta, &copy, nil); err == nil || value != nil {
		t.Fatal(value, err)
	}
	raw, _ := os.ReadFile(idx.XMLPath)
	if err := os.WriteFile(idx.XMLPath, bytes.Replace(raw, []byte(`ht="1"`), []byte(`ht="2"`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourcePrintGeometry(context.Background(), meta, idx, nil); err == nil || value != nil || len(w.printGeometryCache) != 0 {
		t.Fatal("bad CRC became partial success", value, err)
	}
	if err := os.WriteFile(idx.XMLPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := w.SourcePrintGeometry(ctx, meta, idx, func(int64, int64, string) { close(entered); <-release })
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("geometry owner did not start")
	}
	observed := &observedDataRuleContext{Context: context.Background(), observed: make(chan struct{})}
	var count atomic.Int32
	var group sync.WaitGroup
	results := make(chan *SourcePrintGeometryIndex, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := w.SourcePrintGeometry(observed, meta, idx, func(int64, int64, string) { count.Add(1) })
			if err != nil {
				t.Error(err)
			}
			results <- value
		}()
	}
	select {
	case <-observed.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("geometry waiter did not wait")
	}
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	group.Wait()
	close(results)
	var first *SourcePrintGeometryIndex
	for value := range results {
		if first == nil {
			first = value
		}
		if value != first || value == nil {
			t.Fatal("shared geometry did not retry once")
		}
	}
	if count.Load() != 1 {
		t.Fatal("duplicate source scan", count.Load())
	}
	w.printGeometryMu.Lock()
	state := sheetMetadataState[SourcePrintGeometryIndex]{&w.printGeometryMu, &w.printGeometryCache, &w.printGeometryLoading, &w.printGeometryClock, &w.printGeometryBytes}
	for at := 0; at < formats.PrintGeometryCacheSheets+2; at++ {
		cacheSheetMetadataLocked(fmt.Sprint(at), first, state, sheetMetadataLimits{formats.PrintGeometryCacheBytes, formats.PrintGeometryCacheSheets}, (*SourcePrintGeometryIndex).BudgetBytes)
	}
	if len(w.printGeometryCache) > formats.PrintGeometryCacheSheets || w.printGeometryBytes > formats.PrintGeometryCacheBytes {
		t.Error("unbounded geometry LRU")
	}
	w.printGeometryMu.Unlock()
	assertPrintExtent(t, first, "row", 1, 1, 1_000_000_000, false)
	changed := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/></worksheet>`, ""), false)
	changedBytes, _ := os.ReadFile(changed)
	if err := os.WriteFile(file, changedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourcePrintGeometry(context.Background(), meta, idx, nil); err == nil || value != nil {
		t.Fatal("source change hidden", value, err)
	}
}
