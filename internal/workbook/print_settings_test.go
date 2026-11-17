package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"excellens/internal/formats"
)

func printTestParts(sheet, names string) map[string]string {
	return map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="` + catalogueTestNS + `" xmlns:r="` + catalogueTestRel + `"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets><definedNames>` + names + `</definedNames></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="` + catalogueTestRel + `/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet,
	}
}

func preparePrintFixture(t *testing.T, file string) (*Workbook, SheetMeta, *SheetIndex) {
	t.Helper()
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	if value, err := w.SourcePrintSettings(context.Background(), meta, nil, nil); err == nil || value != nil {
		t.Fatal("print metadata prepared its own index", value, err)
	}
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.printCache) != 0 || len(w.printLoading) != 0 {
		t.Fatal("ordinary opening/indexing scanned print metadata")
	}
	return w, meta, idx
}

func TestPrintSettingsOOXMLRealAdapterSourceNamesFieldsRawEvidenceAndImmutableWarmRead(t *testing.T) {
	isolateTestStorage(t)
	setup := `<pageSetup paperSize="9" orientation="landscape" scale="87" fitToWidth="1" fitToHeight="0" pageOrder="overThenDown" firstPageNumber="3" useFirstPageNumber="1"/>`
	header := `<headerFooter differentOddEven="1" differentFirst="1"><oddHeader xml:space="preserve"> &amp;L原文&amp;P&amp;D </oddHeader><evenFooter>&amp;R第&amp;P页</evenFooter><firstHeader/></headerFooter>`
	rows := `<rowBreaks count="2" manualBreakCount="1"><brk id="10" max="16383" man="1"/><brk id="1048575" min="1" max="255" man="0" pt="1"/></rowBreaks>`
	sheet := `<worksheet xmlns="` + catalogueTestNS + `"><sheetPr><pageSetUpPr fitToPage="1" autoPageBreaks="0"/></sheetPr>` + dataRuleTestValues + `<printOptions horizontalCentered="true" verticalCentered="false" headings="1" gridLines="0"/><pageMargins left="0.7000000000001" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>` + setup + header + rows + `<colBreaks count="1" manualBreakCount="1"><brk id="2" min="0" max="1048575" man="1"/></colBreaks></worksheet>`
	names := `<definedName name="_xlnm.Print_Area" localSheetId="0">'Data'!$A$1:$C$9</definedName><definedName name="_xlnm.Print_Titles" localSheetId="0">'Data'!$1:$2,'Data'!$A:$B</definedName>`
	file := writeCatalogueZIPFixture(t, printTestParts(sheet, names), false)
	before, _ := os.ReadFile(file)
	w, meta, idx := preparePrintFixture(t, file)
	beforeRow, err := w.ReadRow(idx, 0, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	index, err := w.SourcePrintSettings(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := index.Snapshot()
	if !value.Complete || !value.NamesComplete || len(value.Names) != 2 || len(value.RowBreaks) != 2 || len(value.ColBreaks) != 1 || len(value.Definitions) != 7 {
		t.Fatal(value)
	}
	if value.Properties["pageSetup"]["orientation"].Value != "landscape" || value.Properties["pageMargins"]["left"].Value != "0.7000000000001" || value.Properties["headerFooter"]["oddHeader"].Value != " &L原文&P&D " || value.Properties["pageSetUpPr"]["fitToPage"].Value != "true" || value.RowBreaks[1].Index != 1048575 || value.ColBreaks[0].Max != 1048575 {
		t.Fatal(value)
	}
	for _, item := range value.Names {
		if item.Status != "ready" || len(item.Areas) == 0 || item.Scope != "Data" {
			t.Fatal(item)
		}
	}
	if value.Definitions[3].RawXML != setup || value.Definitions[4].RawXML != header || value.Definitions[5].RawXML != rows {
		t.Fatal("source XML changed", value.Definitions)
	}
	value.Properties["pageSetup"]["orientation"] = SourcePrintProperty{Value: "changed"}
	value.Names[0].Areas[0].Start = "changed"
	value.RowBreaks[0].Attributes["id"] = "changed"
	*value.RowBreaks[0].Manual = false
	value.Definitions[0].Attributes["fitToPage"] = "changed"
	if err := os.Rename(idx.XMLPath, idx.XMLPath+".held"); err != nil {
		t.Fatal(err)
	}
	warm, err := w.SourcePrintSettings(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm read rescanned XML") })
	if restoreErr := os.Rename(idx.XMLPath+".held", idx.XMLPath); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err != nil || warm != index {
		t.Fatal(warm, err)
	}
	again := warm.Snapshot()
	if again.Properties["pageSetup"]["orientation"].Value != "landscape" || again.Names[0].Areas[0].Start == "changed" || !*again.RowBreaks[0].Manual || again.Definitions[0].Attributes["fitToPage"] != "1" || again.RowBreaks[0].Attributes["id"] != "10" {
		t.Fatal("mutable public snapshot changed shared source", again)
	}
	afterRow, err := w.ReadRow(idx, 0, 1, 3)
	if err != nil || !reflect.DeepEqual(beforeRow, afterRow) || afterRow.Cells[0].Raw != "001" || afterRow.Cells[1].Raw != "9007199254740993.31" || !afterRow.Cells[2].CacheMissing {
		t.Fatal(afterRow, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("print read modified source")
	}
}

func scanPrintFixture(text string) (*SourcePrintIndex, error) {
	b := newPrintMetadataBuilder("Data", "xlsx")
	if err := scanPrintSettingsXML(context.Background(), strings.NewReader(text), int64(len(text)), b, nil); err != nil {
		return nil, err
	}
	return b.finish()
}

func TestPrintSettingsXMLStructuralFailuresDoNotBecomePartialSuccess(t *testing.T) {
	wrap := func(text string) string { return `<worksheet><sheetData/>` + text + `</worksheet>` }
	for name, text := range map[string]string{
		"duplicate setup":      wrap(`<pageSetup/><pageSetup/>`),
		"duplicate attributes": wrap(`<pageSetup scale="70" scale="80"/>`),
		"bad namespace root":   `<worksheet xmlns="urn:bad"><sheetData/></worksheet>`,
		"missing data":         `<worksheet><pageSetup/></worksheet>`,
		"duplicate data":       `<worksheet><sheetData/><sheetData/></worksheet>`,
		"duplicate header":     wrap(`<headerFooter><oddHeader>a</oddHeader><oddHeader>b</oddHeader></headerFooter>`),
		"bad count":            wrap(`<rowBreaks count="2"><brk id="10" max="16383"/></rowBreaks>`),
		"bad manual count":     wrap(`<rowBreaks manualBreakCount="1"><brk id="10" max="16383" man="0"/></rowBreaks>`),
		"mismatched end":       wrap(`<headerFooter><oddHeader>x</evenHeader></headerFooter>`),
		"truncated":            `<worksheet><sheetData/><pageSetup/>`,
		"extra root":           wrap("") + `<worksheet/>`,
		"root text":            wrap("") + `x`,
		"doctype":              `<!DOCTYPE worksheet><worksheet><sheetData/></worksheet>`,
		"oversize block":       wrap(`<headerFooter><!--` + strings.Repeat("x", formats.PrintBlockBytes) + `--></headerFooter>`),
		"oversize text":        wrap(`<headerFooter><oddHeader>` + strings.Repeat("x", formats.PrintTextBytes+1) + `</oddHeader></headerFooter>`),
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := scanPrintFixture(text); err == nil || result != nil {
				t.Fatal("invalid source became a successful partial snapshot", result, err)
			}
		})
	}
	for _, text := range []string{wrap(""), `<s:worksheet xmlns:s="` + catalogueTestNS + `"><s:sheetData/><s:pageSetup orientation="portrait"/></s:worksheet>`} {
		index, err := scanPrintFixture(text)
		if err != nil || !index.Snapshot().Complete {
			t.Fatal(index, err)
		}
	}
}

func TestPrintSettingsXMLUnknownAndInvalidFieldsNeverAcquireDefaults(t *testing.T) {
	for name, body := range map[string]string{
		"unknown namespace": `<e:pageSetup xmlns:e="urn:extra" paperSize="9"/>`,
		"nested setup":      `<extLst><ext><pageSetup paperSize="9"/></ext></extLst>`,
		"invalid numeric":   `<pageSetup scale="NaN"/>`,
		"invalid bool":      `<printOptions gridLines="2"/>`,
		"unknown attr":      `<pageSetup secret="source"/>`,
		"missing margins":   `<pageMargins left="0.1"/>`,
		"ambiguous measure": `<pageSetup paperWidth="210"/>`,
		"invalid break":     `<rowBreaks><brk id="1048577" max="16383"/></rowBreaks>`,
		"nested break":      `<rowBreaks><brk id="10" max="16383"><unknown/></brk></rowBreaks>`,
		"unknown header":    `<headerFooter><oddHeader>left<unknown/>right</oddHeader></headerFooter>`,
	} {
		t.Run(name, func(t *testing.T) {
			index, err := scanPrintFixture(`<worksheet><sheetData/>` + body + `</worksheet>`)
			if err != nil || index.Snapshot().Complete || len(index.Snapshot().Notices) == 0 {
				t.Fatal(index, err)
			}
			value := index.Snapshot()
			if name == "unknown namespace" || name == "nested setup" {
				if len(value.Properties["pageSetup"]) != 0 {
					t.Fatal("foreign setup replaced source settings", value)
				}
			}
			if name == "unknown header" && value.Properties["headerFooter"]["oddHeader"].Value != "" {
				t.Fatal("unknown source header flattened into valid text", value)
			}
		})
	}
	index, err := scanPrintFixture(`<worksheet><sheetData/><customSheetViews><customSheetView><pageSetup paperSize="99"/></customSheetView></customSheetViews></worksheet>`)
	if err != nil || !index.Snapshot().Complete || len(index.Snapshot().Properties) != 0 {
		t.Fatal("custom view settings escaped to active sheet", index, err)
	}
}

func TestPrintSettingsNamesAmbiguityAndPrinterRelationshipsKeepSourceState(t *testing.T) {
	isolateTestStorage(t)
	for name, names := range map[string]string{
		"global scope":   `<definedName name="_xlnm.Print_Area">'Data'!$A$1:$B$9</definedName>`,
		"dynamic":        `<definedName name="_xlnm.Print_Area" localSheetId="0">OFFSET('Data'!$A$1,0,0,9,2)</definedName>`,
		"partial titles": `<definedName name="_xlnm.Print_Titles" localSheetId="0">'Data'!$A$1:$B$9</definedName>`,
		"duplicate":      strings.Repeat(`<definedName name="_xlnm.Print_Area" localSheetId="0">'Data'!$A$1:$B$9</definedName>`, 2),
		"invalid scope":  `<definedName name="_xlnm.Print_Area" localSheetId="99">'Data'!$A$1:$B$9</definedName>`,
	} {
		t.Run(name, func(t *testing.T) {
			file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/></worksheet>`, names), false)
			w, meta, idx := preparePrintFixture(t, file)
			index, err := w.SourcePrintSettings(context.Background(), meta, idx, nil)
			if err != nil || index.Snapshot().Complete || len(index.Snapshot().Names) == 0 {
				t.Fatal(index, err)
			}
			for _, item := range index.Snapshot().Names {
				if item.Status == "ready" || item.Definition == "" {
					t.Fatal("ambiguous print name became an effective range", item)
				}
			}
		})
	}
	for _, target := range []struct{ mode, path, status string }{
		{"", "../printerSettings/settings.bin", "referenced"},
		{"External", "https://example.invalid/never-contact", "unresolved"},
		{"", "../printerSettings/missing.bin", "unresolved"},
	} {
		t.Run(target.path, func(t *testing.T) {
			parts := printTestParts(`<worksheet xmlns:r="`+catalogueTestRel+`"><sheetData/><pageSetup r:id="p1"/></worksheet>`, "")
			parts["xl/worksheets/_rels/sheet1.xml.rels"] = `<Relationships><Relationship Id="p1" Type="` + catalogueTestRel + `/printerSettings" Target="` + target.path + `" TargetMode="` + target.mode + `"/></Relationships>`
			parts["xl/printerSettings/settings.bin"] = "opaque invalid printer bytes"
			w, meta, idx := preparePrintFixture(t, writeCatalogueZIPFixture(t, parts, false))
			index, err := w.SourcePrintSettings(context.Background(), meta, idx, nil)
			if err != nil || index.Snapshot().Complete || len(index.Snapshot().References) != 1 || index.Snapshot().References[0].Status != target.status || index.Snapshot().References[0].Target != target.path {
				t.Fatal(index, err)
			}
		})
	}
}

func biffPrintSetupFixture(flags uint16) []byte {
	data := make([]byte, 34)
	for offset, number := range map[int]uint16{0: 9, 2: 87, 4: 0xFFF9, 6: 1, 8: 0, 10: flags, 12: 600, 14: 600, 32: 2} {
		binary.LittleEndian.PutUint16(data[offset:], number)
	}
	binary.LittleEndian.PutUint64(data[16:], math.Float64bits(0.3))
	binary.LittleEndian.PutUint64(data[24:], math.Float64bits(0.4))
	return data
}

func TestPrintSettingsBIFFRealAdapterScopesContinuationsMarginsBreaksAndSourceUnchanged(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "print.xls")
	header := xlsU16("A😀B")
	setup := biffPrintSetupFixture(1 | 128)
	breaks := []byte{1, 0, 10, 0, 0, 0, 255, 63}
	margin := binary.LittleEndian.AppendUint64(nil, math.Float64bits(0.7000000000001))
	writeBIFF8Fixture(t, file,
		xlsRecord(0x0809, []byte{0, 6, 0x20, 0}), xlsRecord(0x00A1, []byte{1}), xlsRecord(0x000A, nil),
		xlsRecord(0x01AA, make([]byte, 64)), xlsRecord(0x00A1, []byte{1}), xlsRecord(0x0014, []byte{1}), xlsRecord(0x01AB, nil),
		xlsRecord(0x0081, []byte{1, 1}), xlsRecord(0x0083, []byte{1, 0}), xlsRecord(0x0026, margin),
		xlsRecord(0x00A1, setup), xlsRecord(0x0014, header[:7]), xlsRecord(0x003C, append([]byte{1}, header[7:]...)),
		xlsRecord(0x0015, nil), xlsRecord(0x001B, breaks))
	before, _ := os.ReadFile(file)
	w, meta, idx := preparePrintFixture(t, file)
	beforeRow, _ := w.ReadRow(idx, 1, 1, 2)
	index, err := w.SourcePrintSettings(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := index.Snapshot()
	if !value.Complete || !value.NamesComplete || len(value.Definitions) != 7 || len(value.RowBreaks) != 1 || value.Properties["headerFooter"]["oddHeader"].Value != "A😀B" || value.Properties["headerFooter"]["oddFooter"].Value != "" || value.Properties["pageMargins"]["left"].Value != "0.7000000000001" || value.Properties["pageSetup"]["firstPageNumber"].Value != "-7" || value.Properties["pageSetup"]["orientation"].Value != "landscape" || value.RowBreaks[0].Max != 16383 || !*value.RowBreaks[0].Manual {
		t.Fatal(value)
	}
	if value.Definitions[3].RecordHex != hex.EncodeToString(setup) || value.Definitions[4].Continuation[0] != hex.EncodeToString(append([]byte{1}, header[7:]...)) {
		t.Fatal("original physical records were not preserved", value.Definitions)
	}
	value.Definitions[4].Continuation[0] = "changed"
	if index.Snapshot().Definitions[4].Continuation[0] == "changed" {
		t.Fatal("shared continuation mutated")
	}
	afterRow, _ := w.ReadRow(idx, 1, 1, 2)
	after, _ := os.ReadFile(file)
	if !reflect.DeepEqual(beforeRow, afterRow) || afterRow.Cells[0].Raw != "000000123456789" || !bytes.Equal(before, after) {
		t.Fatal("BIFF print read changed source values or bytes", afterRow)
	}
}

func TestPrintSettingsBIFFUndefinedSetupFieldsAndUnknownPrinterStateStayExplicit(t *testing.T) {
	for _, flags := range []uint16{4, 4 | 2 | 64} {
		b := newPrintMetadataBuilder("Data", "xls")
		data := biffPrintSetupFixture(flags)
		for _, offset := range []int{0, 2, 12, 14, 32} {
			binary.LittleEndian.PutUint16(data[offset:], 65535)
		}
		if err := readBIFFPrintRecord(context.Background(), 0x00A1, [][]byte{data}, b); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"paperSize", "scale", "horizontalDpi", "verticalDpi", "copies", "orientation"} {
			item := b.value.Properties["pageSetup"][name]
			if item.Status != "unresolved" || item.Value != "" || item.Reason == "" {
				t.Fatal("undefined stored bytes became effective settings", item)
			}
		}
		if b.value.Complete || b.value.Properties["pageSetup"]["fitToWidth"].Value != "1" || b.value.Properties["pageMargins"]["header"].Value != "0.3" {
			t.Fatal(b.value)
		}
	}
	b := newPrintMetadataBuilder("Data", "xls")
	if err := readBIFFPrintRecord(context.Background(), 0x00A1, [][]byte{biffPrintSetupFixture(64)}, b); err != nil || b.value.Properties["pageSetup"]["orientation"].Value != "portrait" {
		t.Fatal("fNoOrient override lost", b.value, err)
	}
	for _, id := range []uint16{0x004D, 0x0866, 0x0033} {
		b := newPrintMetadataBuilder("Data", "xls")
		if err := readBIFFPrintRecord(context.Background(), id, [][]byte{{1, 2, 3}}, b); err != nil || b.value.Complete || b.value.Definitions[0].RecordHex != "010203" {
			t.Fatal(b.value, err)
		}
	}
}

func TestPrintSettingsBIFFMalformedDefinitionsDoNotSucceedAndExtendedHeadersStayExact(t *testing.T) {
	for id, size := range map[uint16]int{0x00A1: 34, 0x0026: 8, 0x0081: 2, 0x0083: 2, 0x089C: 38} {
		for length := 0; length < size; length++ {
			if err := readBIFFPrintRecord(context.Background(), id, [][]byte{make([]byte, length)}, newPrintMetadataBuilder("Data", "xls")); err == nil {
				t.Fatal("truncated print record accepted", id, length)
			}
		}
	}
	for _, parts := range [][][]byte{{{1}}, {{1, 0}}, {{1, 0, 1, 0, 0, 0, 255}}, {{3, 0, 0, 0}}, {{0, 0, 1, 0xD8}}, {xlsU16(strings.Repeat("x", 256))}} {
		if err := readBIFFPrintRecord(context.Background(), 0x0014, parts, newPrintMetadataBuilder("Data", "xls")); err == nil {
			t.Fatal("invalid source header accepted", parts)
		}
	}
	for _, data := range [][]byte{{1, 0}, {0, 0, 0}, {0xFF, 0xFF}} {
		if err := readBIFFPrintRecord(context.Background(), 0x001B, [][]byte{data}, newPrintMetadataBuilder("Data", "xls")); err == nil {
			t.Fatal("bad break count accepted", data)
		}
	}
	header := make([]byte, 38)
	binary.LittleEndian.PutUint16(header, 0x089C)
	binary.LittleEndian.PutUint16(header[28:], 15)
	for at, text := range []string{"&L双页", "", "&R😀", "&P"} {
		binary.LittleEndian.PutUint16(header[30+at*2:], uint16(len(utf16.Encode([]rune(text)))))
		if text != "" {
			header = append(header, xlsU16(text)...)
		}
	}
	b := newPrintMetadataBuilder("Data", "xls")
	if err := readBIFFPrintRecord(context.Background(), 0x089C, [][]byte{header}, b); err != nil || !b.value.Complete || b.value.Properties["headerFooter"]["firstHeader"].Value != "&R😀" || b.value.Properties["headerFooter"]["evenFooter"].Value != "" {
		t.Fatal(b.value, err)
	}
	header[12] = 1
	b = newPrintMetadataBuilder("Data", "xls")
	if err := readBIFFPrintRecord(context.Background(), 0x089C, [][]byte{header}, b); err != nil || b.value.Complete || len(b.value.Properties) != 0 {
		t.Fatal("custom view header applied to current sheet", b.value, err)
	}
}

func TestPrintSettingsSourceIdentityCRCAndCancellationRejectPartialSnapshots(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/><pageSetup paperSize="9"/></worksheet>`, ""), false)
	w, meta, idx := preparePrintFixture(t, file)
	wrong := *idx
	if value, err := w.SourcePrintSettings(context.Background(), meta, &wrong, nil); err == nil || value != nil {
		t.Fatal("copied source index accepted", value, err)
	}
	data, _ := os.ReadFile(idx.XMLPath)
	if err := os.WriteFile(idx.XMLPath, bytes.Replace(data, []byte(`paperSize="9"`), []byte(`paperSize="8"`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourcePrintSettings(context.Background(), meta, idx, nil); err == nil || value != nil || len(w.printCache) != 0 {
		t.Fatal("CRC mismatch became successful settings", value, err)
	}
	if err := os.WriteFile(idx.XMLPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if value, err := w.SourcePrintSettings(ctx, meta, idx, func(int64, int64, string) { cancel() }); !errors.Is(err, context.Canceled) || value != nil || len(w.printCache) != 0 {
		t.Fatal("cancelled scan published partial settings", value, err)
	}
	if value, err := w.SourcePrintSettings(context.Background(), meta, idx, nil); err != nil || !value.Snapshot().Complete {
		t.Fatal(value, err)
	}
	changed := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/><pageSetup paperSize="7"/></worksheet>`, ""), false)
	changedData, _ := os.ReadFile(changed)
	if err := os.WriteFile(file, changedData, 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourcePrintSettings(context.Background(), meta, idx, nil); err == nil || value != nil {
		t.Fatal("warm metadata hid source change", value, err)
	}
}

func TestPrintSettingsSharedLoadingOwnerCancellationWaiterRetryAndLRU(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, printTestParts(`<worksheet><sheetData/><pageSetup paperSize="9"/></worksheet>`, ""), false)
	w, meta, idx := preparePrintFixture(t, file)
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		_, err := w.SourcePrintSettings(ctx, meta, idx, func(int64, int64, string) { close(entered); <-release })
		ownerDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not start")
	}
	observed := &observedDataRuleContext{Context: context.Background(), observed: make(chan struct{})}
	var attempts atomic.Int32
	var group sync.WaitGroup
	results := make(chan *SourcePrintIndex, 6)
	failures := make(chan error, 6)
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := w.SourcePrintSettings(observed, meta, idx, func(int64, int64, string) { attempts.Add(1) })
			if err != nil {
				failures <- err
			}
			results <- value
		}()
	}
	select {
	case <-observed.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not observe shared load")
	}
	cancel()
	close(release)
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first *SourcePrintIndex
	for result := range results {
		if first == nil {
			first = result
		}
		if result != first || !result.Snapshot().Complete {
			t.Fatal(result)
		}
	}
	if attempts.Load() != 1 {
		t.Fatal("duplicate retry scans", attempts.Load())
	}
	w.printMu.Lock()
	state := sheetMetadataState[SourcePrintIndex]{&w.printMu, &w.printCache, &w.printLoading, &w.printClock, &w.printBytes}
	for at := 0; at < formats.PrintCacheSheets+3; at++ {
		cacheSheetMetadataLocked(fmt.Sprint(at), first, state, sheetMetadataLimits{formats.PrintCacheBytes, formats.PrintCacheSheets}, (*SourcePrintIndex).BudgetBytes)
	}
	if len(w.printCache) > formats.PrintCacheSheets || w.printBytes > formats.PrintCacheBytes {
		t.Error("unbounded metadata cache")
	}
	w.printMu.Unlock()
	if !first.Snapshot().Complete {
		t.Fatal("eviction destroyed retained snapshot")
	}
	encoded, err := json.Marshal(first.Snapshot())
	if err != nil || len(encoded) > formats.PrintMetadataBytes {
		t.Fatal(len(encoded), err)
	}
}
