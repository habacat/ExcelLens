package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"excellens/internal/formats"
)

func biffCatalogueNameFixture(name string, flags uint16, scope int, tokens []byte) []byte {
	u := utf16.Encode([]rune(name))
	data := make([]byte, 15)
	binary.LittleEndian.PutUint16(data, flags)
	data[3] = byte(len(u))
	binary.LittleEndian.PutUint16(data[4:], uint16(len(tokens)))
	binary.LittleEndian.PutUint16(data[8:], uint16(scope))
	data[14] = 1
	for _, ch := range u {
		data = binary.LittleEndian.AppendUint16(data, ch)
	}
	return append(data, tokens...)
}

func biffCatalogueSheetFixture(name string, kind byte) []byte {
	u := utf16.Encode([]rune(name))
	data := make([]byte, 8)
	data[5], data[6], data[7] = kind, byte(len(u)), 1
	for _, ch := range u {
		data = binary.LittleEndian.AppendUint16(data, ch)
	}
	return data
}

func biffCatalogueAliasFixture(index uint32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{0x23}, index)
}

func biffCatalogueRefFixture(link, row, col uint16) []byte {
	data := binary.LittleEndian.AppendUint16([]byte{0x3A}, link)
	data = binary.LittleEndian.AppendUint16(data, row)
	return binary.LittleEndian.AppendUint16(data, col)
}

func TestCatalogueBIFF8CFBRecordIdentitySurvivesScopeShadowingBuiltinsAndNonGridSheets(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "names.xls")
	global := biffCatalogueRefFixture(0, 1, 1)
	local := []byte{0x24, 8, 0, 4, 0}
	union := append(biffCatalogueAliasFixture(1), biffCatalogueRefFixture(0, 3, 2)...)
	union = append(union, 0x10, 0x15)
	globals := [][]byte{
		xlsRecord(0x0085, biffCatalogueSheetFixture("图表页", 2)),
		xlsRecord(0x01AE, []byte{2, 0, 1, 4}),
		xlsRecord(0x01AE, []byte{0, 0, 0, 0}),
		xlsRecord(0x0017, []byte{2, 0, 0, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 0}),
		xlsRecord(0x0018, biffCatalogueNameFixture("金额", 0, 0, global)),
		xlsRecord(0x0018, biffCatalogueNameFixture("金额", 1, 2, local)),
		xlsRecord(0x0018, biffCatalogueNameFixture("指向全局", 0, 2, biffCatalogueAliasFixture(1))),
		xlsRecord(0x0018, biffCatalogueNameFixture("\x06", 0x20, 2, global)),
		xlsRecord(0x0018, biffCatalogueNameFixture("打印别名", 0, 2, biffCatalogueAliasFixture(4))),
		xlsRecord(0x0018, biffCatalogueNameFixture("多个区域", 0, 2, union)),
		xlsRecord(0x0018, biffCatalogueNameFixture("图表局部", 0, 1, local)),
		xlsRecord(0x0018, biffCatalogueNameFixture("外部", 0, 0, biffCatalogueRefFixture(1, 0, 0))),
		xlsRecord(0x0018, biffCatalogueNameFixture("相对", 0, 2, []byte{0x24, 0, 0, 0, 0xC0})),
		xlsRecord(0x0018, biffCatalogueNameFixture("循环甲", 0, 0, biffCatalogueAliasFixture(11))),
		xlsRecord(0x0018, biffCatalogueNameFixture("循环乙", 0, 0, biffCatalogueAliasFixture(10))),
		xlsRecord(0x0018, biffCatalogueNameFixture("宏", 0x0E, 0, global)),
		xlsRecord(0x0018, biffCatalogueNameFixture("坏作用域", 0, 9, global)),
	}
	writeBIFF8FixtureWithGlobals(t, file, globals)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "财务台账" || wb.source.xlsCatalogue == nil {
		t.Fatal("real adapter did not capture metadata", wb.Sheets)
	}
	raw := *wb.source.xlsCatalogue
	encodedBefore, _ := json.Marshal(raw)
	result := wb.ResolveCatalogue(raw)
	live, err := wb.ReadCatalogue(context.Background())
	if err != nil || live.Complete || len(live.Notices) == 0 || catalogueFind(t, live, "指向全局", "财务台账").Areas[0].Start != "B2" {
		t.Fatal("production read lost bindings or claimed BIFF table coverage", live, err)
	}
	for _, name := range []string{"金额", "指向全局", "_xlnm.Print_Area", "打印别名"} {
		scope, start := "财务台账", "B2"
		if name == "金额" {
			start = "E9"
		}
		item := catalogueFind(t, result, name, scope)
		if item.Status != "ready" || len(item.Areas) != 1 || item.Areas[0].Start != start || item.ScopeID != "2" {
			t.Fatal("record binding changed or source sheet order lost", name, item)
		}
	}
	localItem := catalogueFind(t, result, "金额", "财务台账")
	if !localItem.Hidden || catalogueFind(t, result, "金额", "").Areas[0].Start != "B2" {
		t.Fatal("hidden or global metadata lost", result)
	}
	multi := catalogueFind(t, result, "多个区域", "财务台账")
	if multi.Status != "ready" || len(multi.Areas) != 2 || multi.Areas[0].Start != "B2" || multi.Areas[1].Start != "C4" {
		t.Fatal(multi)
	}
	for _, item := range result.Items[6:] {
		if item.Status == "ready" || len(item.Areas) > 0 || item.TokenHex == "" || item.Reason == "" {
			t.Fatal("unsupported metadata became a guessed location", item)
		}
	}
	encodedAfter, _ := json.Marshal(raw)
	if !bytes.Equal(encodedBefore, encodedAfter) || wb.GetCachedIndex(wb.Sheets[0].Path) != nil {
		t.Fatal("resolution modified source metadata or indexed worksheet")
	}
	// A display-only JSON copy has no source token plan. It must not resolve by
	// the textual alias, which could bind to a local name in a different scope.
	var displayOnly Catalogue
	if err := json.Unmarshal(encodedBefore, &displayOnly); err != nil {
		t.Fatal(err)
	}
	for _, item := range wb.ResolveCatalogue(displayOnly).Items {
		if item.Status == "ready" {
			t.Fatal("source bindings reconstructed from display text", item)
		}
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source bytes changed", err)
	}
}

func TestCatalogueBIFF8BoundedSequencesNeverReindexAfterTruncationOrBrokenRelationships(t *testing.T) {
	c := new(biffCatalogueCapture)
	sheet := biffCatalogueSheetFixture("原表", 0)
	for i := 0; i < formats.CatalogueEntries+2; i++ {
		c.capture(0x0085, sheet)
		c.capture(0x01AE, []byte{1, 0, 1, 4})
		c.capture(0x0017, []byte{1, 0, 0, 0, 0, 0, 0, 0})
		c.capture(0x0018, biffCatalogueNameFixture("别名", 0, 0, biffCatalogueAliasFixture(uint32(i+1))))
	}
	if len(c.sheets) != formats.CatalogueEntries || len(c.books) != formats.CatalogueEntries || len(c.links) != formats.CatalogueEntries || len(c.names) != formats.CatalogueEntries || !c.incomplete || c.bytes > formats.CatalogueBytes {
		t.Fatal("metadata sequence capacity ignored", len(c.sheets), len(c.books), len(c.links), len(c.names), c.bytes)
	}
	if result := c.finish(); result.Complete || len(result.Notices) == 0 {
		t.Fatal("truncation hidden", result.Complete, result.Notices)
	}
	broken := new(biffCatalogueCapture)
	broken.capture(0x0085, sheet)
	broken.capture(0x01AE, []byte{1, 0, 1, 4})
	broken.capture(0x0017, []byte{1, 0, 0})
	broken.capture(0x0017, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	if _, err := broken.sheet(0); err == nil || len(broken.links) != 0 {
		t.Fatal("a later relationship filled a missing source ordinal", broken.links)
	}
	limited := new(biffCatalogueCapture)
	limited.bytes = formats.CatalogueBytes - 65
	limited.capture(0x0018, make([]byte, 100))
	limited.capture(0x0018, nil)
	if len(limited.names) != 0 || limited.nameCount != 2 || !limited.incomplete {
		t.Fatal("later smaller name replaced a skipped record ordinal", limited)
	}
}

func TestCatalogueBIFF8MalformedUnicodeContinuationAndUnsupportedTokensRetainTruthfulStatus(t *testing.T) {
	c := new(biffCatalogueCapture)
	c.capture(0x0085, biffCatalogueSheetFixture("原表", 0))
	c.capture(0x01AE, []byte{1, 0, 1, 4})
	c.capture(0x0017, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	ref := biffCatalogueRefFixture(0, 0, 0)
	badUnicode := biffCatalogueNameFixture("坏", 0, 0, ref)
	badUnicode[15], badUnicode[16] = 0, 0xD8
	c.capture(0x0018, badUnicode)
	continued := biffCatalogueNameFixture("跨记录", 0, 0, ref)
	c.capture(0x0018, continued[:len(continued)-2])
	c.capture(0x003C, continued[len(continued)-2:])
	badToken := append([]byte(nil), ref...)
	badToken[0] |= 0x80
	c.capture(0x0018, biffCatalogueNameFixture("保留位", 0, 0, badToken))
	c.capture(0x0018, biffCatalogueNameFixture("Print_Area", 0x20, 0, ref))
	c.capture(0x0018, biffCatalogueNameFixture("指向坏项", 0, 0, biffCatalogueAliasFixture(1)))
	c.capture(0x0018, biffCatalogueNameFixture("末项", 0, 0, biffCatalogueAliasFixture(4)))
	result := catalogueFixtureBook().ResolveCatalogue(c.finish())
	if result.Complete || len(result.Notices) == 0 || len(result.Items) != 6 {
		t.Fatal("incomplete record hidden or sequence compacted", result)
	}
	for _, item := range result.Items[:3] {
		if item.Status == "ready" || item.Definition != "" || item.TokenHex == "" || item.Reason == "" {
			t.Fatal("damaged input presented as valid expression", item)
		}
	}
	if item := catalogueFind(t, result, "末项", ""); item.Status != "ready" || item.Areas[0].Start != "A1" || item.Definition != "_xlnm.Print_Area" {
		t.Fatal("record ordinal or builtin identity changed", item)
	}
	for _, data := range [][]byte{{1, 0, 0xD8}, {1, 0, 0xDC}, {1, 0, 0xD8, 0, 0xD8}, {3, 'A'}} {
		if value, _, err := biffCatalogueString(data, (len(data)-1)/2); err == nil || value != "" {
			t.Fatal("invalid Unicode accepted", data, value)
		}
	}
	good := utf16.Encode([]rune("🌍"))
	data := []byte{1}
	for _, ch := range good {
		data = binary.LittleEndian.AppendUint16(data, ch)
	}
	if value, _, err := biffCatalogueString(data, 2); err != nil || value != "🌍" || strings.ContainsRune(value, '\uFFFD') {
		t.Fatal("valid surrogate pair lost", value, err)
	}
}
