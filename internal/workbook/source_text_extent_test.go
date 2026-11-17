package workbook

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"strings"
	"testing"
)

func TestNativeExtentIgnoresNestedTextMetadataAndKeepsRealInlineAndFormulaContent(t *testing.T) {
	for _, metadata := range []string{
		`<is><extra><t>only metadata</t></extra></is>`,
		`<is><r><rPr><unknown><t>only metadata</t></unknown></rPr><t/></r></is>`,
		`<extra><v>only metadata</v><f>1+1</f></extra>`,
		`<is><rPh><t>only metadata</t></rPh></is>`,
	} {
		x := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row><row r="9"><c r="Z9" t="inlineStr">` + metadata + `</c></row></sheetData></worksheet>`
		rows, _, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(x), int64(len(x)), nil, nil, nil)
		if err != nil || maxRow != 1 || maxCol != 1 || len(rows) != 1 {
			t.Fatal("metadata became a record or extended used range", metadata, rows, maxRow, maxCol, err)
		}
	}
	x := `<worksheet><sheetData><row r="7"><c r="J7" t="inlineStr"><is><extra><t>ignored</t></extra><r><rPr><unknown><t>ignored</t></unknown></rPr><t><![CDATA[001]]></t></r></is></c></row><row r="8"><c r="K8"><f t="shared" si="0" ref="K8:K9">J7</f><v>1</v></c></row><row r="9"><c r="K9"><f t="shared" si="0"/></c></row></sheetData></worksheet>`
	rows, anchors, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(x), int64(len(x)), nil, nil, nil)
	if err != nil || len(rows) != 3 || maxRow != 9 || maxCol != 11 || anchors["0"].Formula != "J7" {
		t.Fatal("real rich text or shared formula state disappeared", rows, anchors, maxRow, maxCol, err)
	}
}

func rewriteIndexMagic(t *testing.T, path, magic string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copy(data[:8], magic)
	binary.LittleEndian.PutUint32(data[len(data)-4:], crc32.ChecksumIEEE(data[:len(data)-4]))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyNativeExtentIsRebuiltFromVerifiedCachedXML(t *testing.T) {
	isolateTestStorage(t)
	file := richWorkbookFile(t, `<sst><si><t>原文</t></si><si><t>数据</t></si><si><t>编号</t></si></sst>`)
	// Use the existing real native package and raw XML cache. A pre-fix index
	// can claim an extent well beyond the current source content.
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	index, err := w.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	originalMaxRow, originalMaxCol := index.MaxRow, index.MaxCol
	index.MaxRow, index.MaxCol = 999, 999
	if err := saveIndex(index); err != nil {
		t.Fatal(err)
	}
	rewriteIndexMagic(t, index.IndexPath, "XLIDX008")
	before, err := os.ReadFile(index.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(index.XMLPath)
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if stale := reopened.GetCachedIndex(meta.Path); stale != nil {
		t.Fatal("obsolete native extent accepted", stale.MaxRow, stale.MaxCol)
	}
	current, err := reopened.BuildIndex(meta, nil, nil)
	if err != nil || current.MaxRow != originalMaxRow || current.MaxCol != originalMaxCol {
		t.Fatal(current, err)
	}
	after, _ := os.ReadFile(index.XMLPath)
	statAfter, _ := os.Stat(index.XMLPath)
	encoded, _ := os.ReadFile(index.IndexPath)
	if !bytes.Equal(before, after) || !statBefore.ModTime().Equal(statAfter.ModTime()) || string(encoded[:8]) != "XLIDX009" {
		t.Fatal("upgrade rewrote source XML or did not publish current index")
	}
}

func TestLegacyExternalRowIndexKeepsItsExistingValueCache(t *testing.T) {
	w, index := openIndexText(t, "legacy.csv", []byte("编号,金额\n001,9007199254740993.31\n"))
	rewriteIndexMagic(t, index.IndexPath, "XLIDX008")
	loaded, err := loadIndex(index.IndexPath, index.XMLPath, index.SheetPath)
	if err != nil || loaded.RowCount() != index.RowCount() {
		t.Fatal("unaffected external index was rejected", loaded, err)
	}
	row, err := w.ReadRow(loaded, 1, 1, 2)
	if err != nil || row.Cells[0].Raw != "001" || row.Cells[1].Raw != "9007199254740993.31" {
		t.Fatal("external raw cache changed during index upgrade", row, err)
	}
}
