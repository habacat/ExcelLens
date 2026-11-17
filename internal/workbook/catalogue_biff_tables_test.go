package workbook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
)

func biffColumnFixture(id uint32, name, caption string, aggregate, flags uint32, extra []byte) []byte {
	data := make([]byte, 36)
	binary.LittleEndian.PutUint32(data, id)
	binary.LittleEndian.PutUint32(data[12:], aggregate)
	binary.LittleEndian.PutUint32(data[20:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(data[24:], flags)
	binary.LittleEndian.PutUint32(data[32:], 0xFFFFFFFF)
	data = append(data, xlsU16(name)...)
	if caption != "" {
		data = append(data, xlsU16(caption)...)
	}
	return append(data, extra...)
}

func biffTableFixture(record uint16, name string, id, lt uint32, header, total uint32, flags uint32, columns ...[]byte) []byte {
	data := make([]byte, 35+64)
	binary.LittleEndian.PutUint16(data, record)
	// Deliberately invalid ignored ref8; the real Ref8U follows the 27-byte header.
	for i := 4; i < 12; i++ {
		data[i] = 0xFF
	}
	binary.LittleEndian.PutUint16(data[12:], 5)
	binary.LittleEndian.PutUint16(data[19:], 1)
	binary.LittleEndian.PutUint16(data[27:], 0)
	binary.LittleEndian.PutUint16(data[29:], 2)
	binary.LittleEndian.PutUint16(data[31:], 0)
	binary.LittleEndian.PutUint16(data[33:], uint16(len(columns)-1))
	fixed := data[35:]
	binary.LittleEndian.PutUint32(fixed, lt)
	binary.LittleEndian.PutUint32(fixed[4:], id)
	binary.LittleEndian.PutUint32(fixed[8:], header)
	binary.LittleEndian.PutUint32(fixed[12:], total)
	binary.LittleEndian.PutUint32(fixed[16:], uint32(len(columns)+1))
	binary.LittleEndian.PutUint32(fixed[20:], 64)
	binary.LittleEndian.PutUint32(fixed[28:], flags|12<<16)
	data = append(data, xlsU16(name)...)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(columns)))
	if flags&(1<<20) != 0 {
		data = append(data, xlsU16(fmt.Sprint(id))...)
	}
	for _, column := range columns {
		data = append(data, column...)
	}
	binary.LittleEndian.PutUint32(data[21:], uint32(len(data)-35))
	return data
}

func biffFeatureRecords(data []byte) [][]byte {
	first := min(len(data), 8224)
	records := [][]byte{xlsRecord(xlsUint16(data, 0), data[:first])}
	for at := first; at < len(data); {
		end := min(at+8212, len(data))
		part := make([]byte, 12, 12+end-at)
		binary.LittleEndian.PutUint16(part, biffContinue11)
		records = append(records, xlsRecord(biffContinue11, append(part, data[at:end]...)))
		at = end
	}
	return records
}

func biffSmallTableFixture() []byte {
	return biffTableFixture(biffFeature11, "账表", 7, 0, 1, 0, 2,
		biffColumnFixture(1, "原单号", "单号", 0, 1, make([]byte, 6)),
		// The enclosing fAutoFilter, rather than the column bit, requires this block.
		biffColumnFixture(2, "原金额", "金额", 0, 0, make([]byte, 6)))
}

func biffCatalogueFixture(t *testing.T, records ...[]byte) (*Workbook, SheetMeta, *SheetIndex, string) {
	t.Helper()
	isolateTestStorage(t) // Identical source bytes intentionally share a fingerprint.
	file := filepath.Join(t.TempDir(), "tables.xls")
	writeBIFF8Fixture(t, file, records...)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	initial, err := wb.ReadCatalogue(context.Background())
	if err != nil || initial.Complete || len(initial.Items) != 0 || wb.GetCachedIndex(meta.Path) != nil {
		t.Fatal("listing built an index or scanned the worksheet", initial, err)
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return wb, meta, idx, file
}

func assertBIFFTable(t *testing.T, wb *Workbook, value Catalogue, name string, columns int) {
	t.Helper()
	item := catalogueFind(t, value, name, "")
	if item.Status != "ready" || !item.Table.Verified || len(item.Areas) != 1 || item.Areas[0].Start != "A1" || item.Areas[0].End != ColumnName(columns)+"3" || len(item.Table.Columns) != columns {
		t.Fatal("actual worksheet table not confirmed", item)
	}
	probe := value
	probe.Items = append(append([]CatalogueItem(nil), value.Items...), CatalogueItem{ID: "probe-name", Kind: "name", Name: "数据区引用", Definition: name + "[#Data]", Status: "unresolved"})
	resolved := wb.ResolveCatalogue(probe)
	area := catalogueFind(t, resolved, "数据区引用", "")
	if area.Status != "ready" || area.Areas[0].Start != "A2" || area.Areas[0].End != ColumnName(columns)+"3" {
		t.Fatal("BIFF table did not reuse common structured-reference resolver", area)
	}
}

func TestCatalogueBIFFTablesRealCFBCaptureWarmReopenAndImmutablePublicResult(t *testing.T) {
	isolateTestStorage(t)
	wb, meta, idx, file := biffCatalogueFixture(t, biffFeatureRecords(biffSmallTableFixture())...)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	value, err := wb.ReadCatalogue(context.Background())
	if err != nil || !value.Complete {
		t.Fatal(value, err)
	}
	assertBIFFTable(t, wb, value, "账表", 2)
	if len(wb.source.xlsCatalogue.Items) != 0 {
		t.Fatal("sheet tables modified immutable Globals")
	}
	row, err := wb.ReadRow(idx, 1, 1, 2)
	if err != nil || len(row.Cells) != 2 || row.Cells[0].Raw != "000000123456789" || row.Cells[0].Type != "inlineStr" || row.Cells[0].Ref != "A2" || row.Cells[1].Raw != "1234.5" || row.Cells[1].Type != "n" || row.Cells[1].Ref != "B2" {
		t.Fatal("source values/types/coordinates changed", row, err)
	}
	value.Items[0].Table.Columns[0].Name = "被调用方改写"
	value.Items[0].Table.Verified = false
	value, err = wb.ReadCatalogue(context.Background())
	if err != nil || value.Items[0].Table.Columns[0].Name != "单号" {
		t.Fatal("public pointer mutated shared proof", value, err)
	}
	assertBIFFTable(t, wb, value, "账表", 2)
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.GetCachedIndex(meta.Path)
	if loaded == nil {
		t.Fatal("ordinary cached index did not reopen")
	}
	aside := idx.XMLPath + ".aside"
	if err := os.Rename(idx.XMLPath, aside); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(aside, idx.XMLPath)
	calls := 0
	value, err = reopened.ConfirmCatalogue(context.Background(), meta, loaded, func(int64, int64, string) { calls++ })
	if err != nil || calls != 0 {
		t.Fatal("warm metadata proof scanned source/cell cache", calls, err)
	}
	assertBIFFTable(t, reopened, value, "账表", 2)
	if _, err := reopened.ConfirmCatalogue(context.Background(), meta, &SheetIndex{SheetPath: meta.Path}, nil); err == nil {
		t.Fatal("foreign index accepted")
	}
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only catalogue changed source", err)
	}
}

func TestCatalogueBIFFTableContinuationUnicodeAndEmbeddedChartBoundaries(t *testing.T) {
	isolateTestStorage(t)
	columns := make([][]byte, 40)
	for i := range columns {
		name := fmt.Sprintf("字段%d", i) + strings.Repeat("甲", 248)
		columns[i] = biffColumnFixture(uint32(i+1), name, name, 0, 0, nil)
	}
	data := biffTableFixture(biffFeature11, "大表é", 11, 0, 1, 0, 0, columns...)
	if len(data) <= 8224 {
		t.Fatal("fixture missed real FRT continuation")
	}
	records := biffFeatureRecords(data)
	chart := [][]byte{xlsRecord(0x0809, []byte{0, 6, 0x20, 0}), xlsRecord(biffFeature11, biffSmallTableFixture()), xlsRecord(0x000A, nil)}
	records = append(chart, records...)
	wb, _, idx, _ := biffCatalogueFixture(t, records...)
	value, err := wb.ReadCatalogue(context.Background())
	if err != nil || !value.Complete || len(value.Items) != 1 {
		t.Fatal("child chart metadata leaked or parent ended early", value, err)
	}
	assertBIFFTable(t, wb, value, "大表é", len(columns))
	if idx.RowCount() != 3 || cellValue(t, wb, idx, 2, 2) != "1234.5" {
		t.Fatal("child chart EOF truncated actual worksheet cells")
	}
	// Force a split after the low byte of a UTF-16 code unit. Continuation
	// headers, rather than SST encoding flags, are removed before interpretation.
	data = biffSmallTableFixture()
	at := 103
	first := xlsRecord(biffFeature11, data[:at])
	part := make([]byte, 12)
	binary.LittleEndian.PutUint16(part, biffContinue11)
	part = append(part, data[at:]...)
	wb, _, _, _ = biffCatalogueFixture(t, first, xlsRecord(biffContinue11, part))
	value, err = wb.ReadCatalogue(context.Background())
	if err != nil || !value.Complete {
		t.Fatal("split UTF-16 character was corrupted", value, err)
	}
	assertBIFFTable(t, wb, value, "账表", 2)
}

func TestCatalogueBIFFTableFeature12MetadataVariantsDoNotExecuteOrContactSources(t *testing.T) {
	isolateTestStorage(t)
	for _, variant := range []string{"totalFormula", "totalText", "noHeader", "query", "xmlSingle", "xmlMap", "reservedStringBits", "caseSensitive"} {
		t.Run(variant, func(t *testing.T) {
			name, record, lt, h, total, flags := "账表", uint16(biffFeature12), uint32(0), uint32(1), uint32(0), uint32(0)
			colFlags, aggregate, extra, caption := uint32(0), uint32(0), []byte{}, "金额"
			switch variant {
			case "totalFormula":
				total, colFlags, aggregate, extra = 1, 1<<7, 9, []byte{3, 0, 0x1E, 1, 0}
			case "totalText":
				total, colFlags, extra = 1, 1<<10, xlsU16("合计原文")
			case "noHeader":
				h, colFlags = 0, 1<<9
				extra = append(make([]byte, 4), xlsU16("样式原名")...)
			case "query":
				lt, extra = 3, []byte{1, 0, 0, 0}
			case "xmlSingle":
				record, lt, h, flags, caption = biffFeature11, 2, 0, 1<<9|1<<20, ""
			case "xmlMap":
				record, lt, colFlags = biffFeature11, 2, 4
				extra = append([]byte{1, 0, 2, 0, 0, 0, 1, 0, 0, 0}, xlsU16("/原始/金额")...)
			}
			column := biffColumnFixture(1, "原字段", caption, aggregate, colFlags, extra)
			if variant == "reservedStringBits" {
				column[38] |= 0xFC
			}
			data := biffTableFixture(record, name, 1, lt, h, total, flags, column)
			if variant == "xmlSingle" {
				binary.LittleEndian.PutUint16(data[29:], 0)
			}
			records := biffFeatureRecords(data)
			if variant == "caseSensitive" {
				name = "Table"
				records = append(biffFeatureRecords(biffTableFixture(biffFeature11, name, 1, 0, 1, 0, 0, column)), biffFeatureRecords(biffTableFixture(biffFeature11, "table", 2, 0, 1, 0, 0, column))...)
			}
			wb, _, _, _ := biffCatalogueFixture(t, records...)
			value, err := wb.ReadCatalogue(context.Background())
			if err != nil || !value.Complete {
				t.Fatal(variant, value, err)
			}
			for _, item := range value.Items {
				if item.Status != "ready" || !item.Table.Verified || len(item.Areas) != 1 || item.Table.HeaderRows != int(h) || item.Table.TotalRows != int(total) {
					t.Fatal(variant, item)
				}
			}
		})
	}
}

func TestCatalogueBIFFMalformedOrUnsupportedTableNeverCertifiesPartialSheet(t *testing.T) {
	isolateTestStorage(t)
	for _, variant := range []string{"short", "length", "range", "count", "fixed", "columns", "duplicateID", "unicode", "duplicateTableID", "continuationHeader", "wrongContinuation", "trailing", "provider", "arrayTotal"} {
		t.Run(variant, func(t *testing.T) {
			data := biffSmallTableFixture()
			switch variant {
			case "short":
				data = data[:20]
			case "length":
				binary.LittleEndian.PutUint32(data[21:], uint32(len(data)))
			case "range":
				binary.LittleEndian.PutUint16(data[33:], 256)
			case "count":
				binary.LittleEndian.PutUint16(data[19:], 2)
			case "fixed":
				binary.LittleEndian.PutUint32(data[55:], 63)
			case "columns":
				binary.LittleEndian.PutUint16(data[106:], 1)
			case "duplicateID":
				first := 108
				second := first + 36 + len(xlsU16("原单号")) + len(xlsU16("单号")) + 6
				binary.LittleEndian.PutUint32(data[second:], 1)
			case "unicode":
				binary.LittleEndian.PutUint16(data[102:], 0xD800)
			case "trailing":
				data = append(data, 1)
				binary.LittleEndian.PutUint32(data[21:], uint32(len(data)-35))
			case "provider":
				binary.LittleEndian.PutUint32(data[35:], 1)
			case "arrayTotal":
				data = biffTableFixture(biffFeature12, "账表", 7, 0, 1, 1, 0, biffColumnFixture(1, "金额", "金额", 9, 1<<7|1<<8, []byte{1, 0, 0}))
			}
			records := biffFeatureRecords(data)
			if variant == "continuationHeader" {
				records = append(records, xlsRecord(biffContinue11, make([]byte, 12)))
			}
			if variant == "wrongContinuation" {
				records = append(records, xlsRecord(0x087F, make([]byte, 12)))
			}
			other := biffSmallTableFixture()
			if variant != "duplicateTableID" {
				binary.LittleEndian.PutUint32(other[39:], 8)
			}
			records = append(records, biffFeatureRecords(other)...)
			wb, _, _, _ := biffCatalogueFixture(t, records...)
			value, err := wb.ReadCatalogue(context.Background())
			if err != nil || value.Complete || len(value.Items) != 2 || len(value.Notices) == 0 {
				t.Fatal("lost incomplete/invalid state", variant, value, err)
			}
			for _, item := range value.Items {
				if item.Status == "ready" || len(item.Areas) != 0 || item.Table.Verified || item.Reason == "" {
					t.Fatal("partial/unsupported sheet became navigable", variant, item)
				}
			}
		})
	}
}

func TestCatalogueBIFFTableMetadataCapacityDoesNotFailOrdinarySourceIndex(t *testing.T) {
	isolateTestStorage(t)
	data := biffSmallTableFixture()
	data = append(data, make([]byte, formats.CataloguePartBytes+100-len(data))...)
	binary.LittleEndian.PutUint32(data[21:], uint32(len(data)-35))
	wb, _, idx, _ := biffCatalogueFixture(t, biffFeatureRecords(data)...)
	value, err := wb.ReadCatalogue(context.Background())
	if err != nil || value.Complete || len(value.Items) != 1 || value.Items[0].Status == "ready" || value.Items[0].Table.Verified || !strings.Contains(value.Items[0].Reason, "容量") {
		t.Fatal("over-budget table definition lost explicit status", value, err)
	}
	if idx.RowCount() != 3 || cellValue(t, wb, idx, 2, 1) != "000000123456789" {
		t.Fatal("metadata budget prevented ordinary read-only value index")
	}
	if wb.catalogueBytes > formats.CatalogueSheetCacheBytes {
		t.Fatal("metadata proof exceeded resident LRU budget")
	}
}

func TestCatalogueBIFFLegacyRowsRebuildAndDerivedProofWriteFailure(t *testing.T) {
	for _, mode := range []string{"legacyRows", "proofWriteFailure"} {
		t.Run(mode, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "legacy.xls")
			records := [][]byte{xlsRecord(0x0809, []byte{0, 6, 0x20, 0}), xlsRecord(0x000A, nil)}
			records = append(records, biffFeatureRecords(biffSmallTableFixture())...)
			writeBIFF8Fixture(t, file, records...)
			wb, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			meta := wb.Sheets[0]
			if mode == "proofWriteFailure" {
				if err := os.Mkdir(wb.biffCataloguePath(meta), 0700); err != nil {
					t.Fatal(err)
				}
			}
			idx, err := wb.BuildIndex(meta, nil, nil)
			if err != nil || idx.RowCount() != 3 {
				t.Fatal("ordinary index failed", idx, err)
			}
			if mode == "legacyRows" {
				for _, path := range []string{idx.XMLPath, idx.IndexPath} {
					legacy := strings.Replace(path, fmt.Sprintf("-biff%d", biffRowReaderVersion), "", 1)
					if legacy == path {
						t.Fatal("BIFF reader namespace missing")
					}
					if err := os.Rename(path, legacy); err != nil {
						t.Fatal(err)
					}
				}
			}
			reopened, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "legacyRows" {
				if reopened.GetCachedIndex(meta.Path) != nil {
					t.Fatal("pre-fix BIFF rows accepted")
				}
				idx, err = reopened.BuildIndex(meta, nil, nil)
			} else {
				idx = reopened.GetCachedIndex(meta.Path)
			}
			if err != nil || idx == nil || idx.RowCount() != 3 {
				t.Fatal("cache migration lost cells after child EOF", err)
			}
			value, err := reopened.ConfirmCatalogue(context.Background(), meta, idx, nil)
			if err != nil {
				t.Fatal("derived proof failure broke explicit read", err)
			}
			assertBIFFTable(t, reopened, value, "账表", 2)
		})
	}
}

func TestCatalogueBIFFProofRejectsWrongBindingAndRebuildsOnlyExplicitly(t *testing.T) {
	isolateTestStorage(t)
	for _, variant := range []string{"missing", "version", "fingerprint", "sheet", "offset", "checksum", "truncated", "unknown", "invalidValue"} {
		t.Run(variant, func(t *testing.T) {
			wb, meta, _, file := biffCatalogueFixture(t, biffFeatureRecords(biffSmallTableFixture())...)
			path := wb.biffCataloguePath(meta)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cache biffCatalogueCache
			if err := json.Unmarshal(raw, &cache); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "version":
				cache.Version++
			case "fingerprint":
				cache.Fingerprint = "foreign"
			case "sheet":
				cache.Sheet = "foreign"
			case "offset":
				cache.Offset++
			case "invalidValue":
				cache.Value.Items[0].Table.Columns[1].ID = "1"
			}
			cache.Checksum = cache.checksum()
			if variant == "checksum" {
				cache.Checksum = "bad"
			}
			raw, _ = json.Marshal(cache)
			if variant == "truncated" {
				raw = raw[:len(raw)/2]
			}
			if variant == "unknown" {
				raw = append([]byte(`{"unknown":1,`), raw[1:]...)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if variant == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			value, err := reopened.ReadCatalogue(context.Background())
			if err != nil || value.Complete || len(value.Items) != 0 {
				t.Fatal("listing trusted bad proof or rebuilt implicitly", variant, value, err)
			}
			idx := reopened.GetCachedIndex(meta.Path)
			calls := 0
			value, err = reopened.ConfirmCatalogue(context.Background(), meta, idx, func(int64, int64, string) { calls++ })
			if err != nil || calls < 1 {
				t.Fatal("explicit rebuild failed", variant, calls, err)
			}
			assertBIFFTable(t, reopened, value, "账表", 2)
		})
	}
}

func TestCatalogueBIFFRebuildCancellationSourceChangeAndSharedOwner(t *testing.T) {
	isolateTestStorage(t)
	for _, variant := range []string{"cancel", "sourceChange", "sharedOwner"} {
		t.Run(variant, func(t *testing.T) {
			padding := bytes.Repeat(xlsRecord(0x00EC, make([]byte, 8224)), 40)
			records := append([][]byte{padding}, biffFeatureRecords(biffSmallTableFixture())...)
			wb, meta, _, file := biffCatalogueFixture(t, records...)
			if err := os.Remove(wb.biffCataloguePath(meta)); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			idx := reopened.GetCachedIndex(meta.Path)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if variant == "sharedOwner" {
				entered, release := make(chan struct{}), make(chan struct{})
				owner := make(chan error, 1)
				go func() {
					_, err := reopened.ConfirmCatalogue(ctx, meta, idx, func(int64, int64, string) { close(entered); <-release })
					owner <- err
				}()
				<-entered
				wait := &catalogueWaitContext{Context: context.Background(), waiting: make(chan struct{})}
				waiter := make(chan error, 1)
				go func() {
					value, err := reopened.ConfirmCatalogue(wait, meta, idx, nil)
					if err == nil && (!value.Complete || len(value.Items) != 1 || value.Items[0].Status != "ready") {
						err = errors.New("waiter lost complete table")
					}
					waiter <- err
				}()
				select {
				case <-wait.waiting:
				case <-time.After(5 * time.Second):
					close(release)
					t.Fatal("waiter did not join load")
				}
				cancel()
				close(release)
				if err := <-owner; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if err := <-waiter; err != nil {
					t.Fatal(err)
				}
				return
			}
			cancelledAt := int64(0)
			value, err := reopened.ConfirmCatalogue(ctx, meta, idx, func(done, total int64, _ string) {
				if done == 0 || cancelledAt > 0 {
					return
				}
				cancelledAt = done
				if done >= total {
					t.Fatal("fixture did not interrupt active sheet scan")
				}
				if variant == "cancel" {
					cancel()
					return
				}
				stat, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, stat.ModTime(), stat.ModTime()); err != nil {
					t.Fatal(err)
				}
			})
			if cancelledAt == 0 || err == nil || len(value.Items) != 0 {
				t.Fatal("partial/changed source published", cancelledAt, value, err)
			}
			if variant == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := os.Stat(wb.biffCataloguePath(meta)); !os.IsNotExist(err) {
				t.Fatal("failed scan published durable proof", err)
			}
			if len(reopened.catalogueSheets) != 0 || len(reopened.catalogueLoading) != 0 {
				t.Fatal("failed load retained proof/ownership")
			}
		})
	}
}

func FuzzCatalogueBIFFTableRecords(f *testing.F) {
	f.Add(biffSmallTableFixture())
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		original := append([]byte(nil), data...)
		meta := SheetMeta{Name: "原表", Path: "external/xls/0"}
		first, err := readBIFFTable(biffFeature11, data, meta)
		second, again := readBIFFTable(biffFeature11, data, meta)
		if !bytes.Equal(data, original) || !reflect.DeepEqual(first, second) || (err == nil) != (again == nil) {
			t.Fatal("parse mutated source or changed on repeat")
		}
		if err == nil {
			first.ID, first.Table.Verified = "table-1", true
			if _, valid := biffCatalogueEntry(meta, Catalogue{Items: []CatalogueItem{first}, Complete: true}); !valid {
				t.Fatal("parser produced invalid proof", first)
			}
		}
	})
}
