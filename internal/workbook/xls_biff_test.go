package workbook

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func xlsRecord(id uint16, b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.LittleEndian.PutUint16(out, id)
	binary.LittleEndian.PutUint16(out[2:], uint16(len(b)))
	copy(out[4:], b)
	return out
}
func appendBIFF(dst []byte, id uint16, data []byte) []byte {
	return append(dst, xlsRecord(id, data)...)
}
func xlsU16(s string) []byte {
	runes := utf16.Encode([]rune(s))
	result := make([]byte, 3+2*len(runes))
	binary.LittleEndian.PutUint16(result, uint16(len(runes)))
	result[2] = 1
	for i, r := range runes {
		binary.LittleEndian.PutUint16(result[3+i*2:], r)
	}
	return result
}
func writeBIFF8Fixture(t *testing.T, path string, layoutRecords ...[]byte) {
	t.Helper()
	writeBIFF8FixtureWithGlobals(t, path, nil, layoutRecords...)
}

func writeBIFF8FixtureWithGlobals(t *testing.T, path string, globals [][]byte, layoutRecords ...[]byte) {
	t.Helper()
	bof := []byte{0, 6, 5, 0, 0, 0, 0, 0}
	wb := appendBIFF(nil, 0x0809, bof)
	font := make([]byte, 16)
	binary.LittleEndian.PutUint16(font[0:], 220)
	binary.LittleEndian.PutUint16(font[6:], 400)
	font[14] = 1
	font[15] = 0
	wb = appendBIFF(wb, 0x0031, font)
	xf := make([]byte, 20)
	binary.LittleEndian.PutUint16(xf[2:], 4) // #,##0.00
	wb = appendBIFF(wb, 0x00E0, xf)
	for _, record := range globals {
		wb = append(wb, record...)
	}
	bounds := make([]byte, 6)
	bounds = append(bounds, byte(len([]rune("财务台账"))), byte(1))
	for _, u := range utf16.Encode([]rune("财务台账")) {
		bounds = binary.LittleEndian.AppendUint16(bounds, u)
	}
	boundsPos := len(wb) + 4
	wb = appendBIFF(wb, 0x0085, bounds)
	labels := []string{"单号", "金额", "000000123456789", "测试客户"}
	sst := make([]byte, 8)
	binary.LittleEndian.PutUint32(sst[0:], uint32(len(labels)))
	binary.LittleEndian.PutUint32(sst[4:], uint32(len(labels)))
	for _, v := range labels {
		sst = append(sst, xlsU16(v)...)
	}
	wb = appendBIFF(wb, 0x00FC, sst)
	wb = appendBIFF(wb, 0x000A, nil)
	binary.LittleEndian.PutUint32(wb[boundsPos:], uint32(len(wb)))
	wsBof := []byte{0, 6, 0x10, 0, 0, 0, 0, 0}
	wb = appendBIFF(wb, 0x0809, wsBof)
	var trailing [][]byte
	for _, record := range layoutRecords {
		if len(record) >= 4 && binary.LittleEndian.Uint16(record) == 0x00E5 {
			trailing = append(trailing, record)
		} else {
			wb = append(wb, record...)
		}
	}
	// C is hidden in the original workbook; user can choose to display it.
	colInfo := make([]byte, 12)
	binary.LittleEndian.PutUint16(colInfo[0:], 2)
	binary.LittleEndian.PutUint16(colInfo[2:], 2)
	binary.LittleEndian.PutUint16(colInfo[8:], 1)
	wb = appendBIFF(wb, 0x007D, colInfo)
	label := func(row, col, xf, index uint16) []byte {
		b := make([]byte, 10)
		binary.LittleEndian.PutUint16(b[0:], row)
		binary.LittleEndian.PutUint16(b[2:], col)
		binary.LittleEndian.PutUint16(b[4:], xf)
		binary.LittleEndian.PutUint32(b[6:], uint32(index))
		return b
	}
	wb = appendBIFF(wb, 0x00FD, label(0, 0, 0, 0))
	wb = appendBIFF(wb, 0x00FD, label(0, 1, 0, 1))
	wb = appendBIFF(wb, 0x00FD, label(1, 0, 0, 2))
	number := make([]byte, 14)
	binary.LittleEndian.PutUint16(number[0:], 1)
	binary.LittleEndian.PutUint16(number[2:], 1)
	binary.LittleEndian.PutUint64(number[6:], math.Float64bits(1234.5))
	wb = appendBIFF(wb, 0x0203, number)
	wb = appendBIFF(wb, 0x00FD, label(2, 0, 0, 3))
	formula := make([]byte, 22)
	binary.LittleEndian.PutUint16(formula[0:], 2)
	binary.LittleEndian.PutUint16(formula[2:], 1)
	binary.LittleEndian.PutUint64(formula[6:], math.Float64bits(1234.5))
	// ptgArea B2:B2, ptgFuncVar 1 argument SUM (function ID 4)
	toks := []byte{0x25, 1, 0, 1, 0, 1, 0xC0, 1, 0xC0, 0x22, 1, 4, 0}
	binary.LittleEndian.PutUint16(formula[20:], uint16(len(toks)))
	wb = appendBIFF(wb, 0x0006, append(formula, toks...))
	for _, record := range trailing {
		wb = append(wb, record...)
	}
	wb = appendBIFF(wb, 0x000A, nil)
	writeBIFFCFBFixture(t, path, wb)
}

// Real regular-stream CFB fixture with a bounded, variable-size FAT. This lets
// adapter tests exercise >8224-byte records, continuations and full BIFF sheets.
func writeBIFFCFBFixture(t *testing.T, path string, wb []byte) {
	t.Helper()
	size := max(4096, len(wb))
	sectors := (size + 511) / 512
	fats := 1
	for (sectors+1+fats+127)/128 > fats {
		fats = (sectors + 1 + fats + 127) / 128
	}
	if fats > 109 {
		t.Fatalf("CFB fixture exceeds header DIFAT capacity: %d", fats)
	}
	out := make([]byte, 512+512*(sectors+1+fats))
	h := out[:512]
	copy(h[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	binary.LittleEndian.PutUint16(h[24:], 0x3E)
	binary.LittleEndian.PutUint16(h[26:], 3)
	binary.LittleEndian.PutUint16(h[28:], 0xFFFE)
	binary.LittleEndian.PutUint16(h[30:], 9)
	binary.LittleEndian.PutUint16(h[32:], 6)
	binary.LittleEndian.PutUint32(h[44:], uint32(fats))
	binary.LittleEndian.PutUint32(h[48:], uint32(sectors))
	binary.LittleEndian.PutUint32(h[56:], 4096)
	binary.LittleEndian.PutUint32(h[60:], cfbEnd)
	binary.LittleEndian.PutUint32(h[68:], cfbEnd)
	for i := 0; i < 109; i++ {
		binary.LittleEndian.PutUint32(h[76+i*4:], cfbFree)
	}
	for i := 0; i < fats; i++ {
		binary.LittleEndian.PutUint32(h[76+i*4:], uint32(sectors+1+i))
	}
	copy(out[512:], wb)
	dir := out[512+sectors*512:]
	mkDir := func(at int, name string, typ byte, first uint32, size uint64) {
		d := dir[at : at+128]
		u := utf16.Encode([]rune(name))
		for i, v := range u {
			binary.LittleEndian.PutUint16(d[2*i:], v)
		}
		binary.LittleEndian.PutUint16(d[64:], uint16((len(u)+1)*2))
		d[66] = typ
		d[67] = 1
		binary.LittleEndian.PutUint32(d[68:], cfbFree)
		binary.LittleEndian.PutUint32(d[72:], cfbFree)
		binary.LittleEndian.PutUint32(d[76:], cfbFree)
		binary.LittleEndian.PutUint32(d[116:], first)
		binary.LittleEndian.PutUint64(d[120:], size)
	}
	mkDir(0, "Root Entry", 5, cfbEnd, 0)
	binary.LittleEndian.PutUint32(dir[76:], 1)
	mkDir(128, "Workbook", 2, 0, uint64(size))
	fat := out[512+(sectors+1)*512:]
	for i := 0; i < fats*128; i++ {
		binary.LittleEndian.PutUint32(fat[4*i:], cfbFree)
	}
	for i := 0; i < sectors-1; i++ {
		binary.LittleEndian.PutUint32(fat[4*i:], uint32(i+1))
	}
	binary.LittleEndian.PutUint32(fat[4*(sectors-1):], cfbEnd)
	binary.LittleEndian.PutUint32(fat[4*sectors:], cfbEnd)
	for i := 0; i < fats; i++ {
		binary.LittleEndian.PutUint32(fat[4*(sectors+1+i):], cfbFat)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestBIFF8XLSReadOnlyMetadataFormulaAndSearch(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "legacy.xls")
	writeBIFF8Fixture(t, path)
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Format != "xls" || len(wb.Sheets) != 1 || wb.Sheets[0].Name != "财务台账" {
		t.Fatalf("XLS metadata: %+v", wb.Sheets)
	}
	hidden, err := wb.SourceHiddenColumns(wb.Sheets[0], nil)
	if err != nil || len(hidden) != 1 || hidden[0].Min != 3 || hidden[0].Max != 3 {
		t.Fatalf("XLS source hidden columns: %+v %v", hidden, err)
	}
	if len(wb.Styles) == 0 || wb.Styles[0].NumFmtID != 4 {
		t.Fatalf("XLS XF: %+v", wb.Styles)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 3 || idx.MaxCol != 2 {
		t.Fatalf("extent %d x %d", idx.MaxRow, idx.MaxCol)
	}
	if cellValue(t, wb, idx, 2, 1) != "000000123456789" {
		t.Fatalf("long ID was damaged")
	}
	if cellValue(t, wb, idx, 2, 2) != "1234.5" {
		t.Fatalf("numeric cell damaged")
	}
	cell, err := wb.ReadRow(idx, 2, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cell.Cells) != 1 || !cell.Cells[0].HasFormula || cell.Cells[0].Formula != "=SUM(B2:B2)" {
		t.Fatalf("formula: %+v", cell.Cells)
	}
	results, _, err := wb.SearchSheet(idx, "财务台账", "测试客户", "value", 10, nil, nil)
	if err != nil || len(results) != 1 || results[0].Ref != "A3" {
		t.Fatalf("search: %v %#v", err, results)
	}
	reopen, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopen.GetCachedIndex(reopen.Sheets[0].Path) == nil {
		t.Fatal("native XLS row cache not reused")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(initial, after) {
		t.Fatal("XLS must never be changed")
	}
}
func TestBIFF8NegativeNumberKeepsFormatColor(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "red-negative.xls")
	writeBIFF8Fixture(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	xf := make([]byte, 20)
	binary.LittleEndian.PutUint16(xf[2:], 4)
	at := bytes.Index(data[512:], xlsRecord(0x00E0, xf))
	if at < 0 {
		t.Fatal("fixture XF missing")
	}
	binary.LittleEndian.PutUint16(data[512+at+6:], 38) // built-in red negatives
	number := make([]byte, 14)
	binary.LittleEndian.PutUint16(number, 1)
	binary.LittleEndian.PutUint16(number[2:], 1)
	binary.LittleEndian.PutUint64(number[6:], math.Float64bits(1234.5))
	at = bytes.Index(data[512:], xlsRecord(0x0203, number))
	if at < 0 {
		t.Fatal("fixture NUMBER missing")
	}
	binary.LittleEndian.PutUint64(data[512+at+10:], math.Float64bits(-1234.5))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := wb.ReadRow(idx, idx.FindRowOffset(2), 2, 2)
	if err != nil || len(row.Cells) != 1 {
		t.Fatal(row, err)
	}
	if cell := row.Cells[0]; cell.Raw != "-1234.5" || cell.ValueColor != "#FF0000" {
		t.Fatalf("XLS numeric color differs from OOXML: %+v", cell)
	}
}

func TestBIFF8RejectsRenamedText(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "fake.xls")
	if err := os.WriteFile(path, []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenWorkbook(path)
	if err == nil || !strings.Contains(err.Error(), "CFB") {
		t.Fatalf("renamed text accepted: %v", err)
	}
}
