package workbook

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The high/low UTF-16 surrogate of a single emoji may be split between two
// BIFF SST/CONTINUE records. The result must be a single codepoint, not two
// Unicode replacement characters.
func TestBIFF8SSTContinuationAcrossSurrogatePair(t *testing.T) {
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header, 1)
	binary.LittleEndian.PutUint32(header[4:], 1)
	prefix := []byte{2, 0, 1} // 2 UTF-16 units, uncompressed.
	first := append(append([]byte{}, header...), prefix...)
	first = binary.LittleEndian.AppendUint16(first, 0xD83D)
	second := []byte{1, 0, 0xDE} // CONTINUE compression flag then low surrogate.
	strings_, err := parseXLSSST([][]byte{first, second})
	if err != nil || len(strings_) != 1 || strings_[0] != "😀" {
		t.Fatalf("SST surrogate across CONTINUE: %q %v", strings_, err)
	}
}

func TestBIFF8SSTContinuationChangesCompression(t *testing.T) {
	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header, 2)
	binary.LittleEndian.PutUint32(header[4:], 1)
	// The first segment holds A as a compressed character.
	first := append(append([]byte{}, header...), 2, 0, 0, 'A')
	second := []byte{1, 0x2D, 0x4E} // CONTINUE flags (UTF-16), 中.
	values, err := parseXLSSST([][]byte{first, second})
	if err != nil || len(values) != 1 || values[0] != "A中" {
		t.Fatalf("SST compressed to unicode CONTINUE: %q %v", values, err)
	}
}

func TestBIFF8FormulaAreaMixedReferences(t *testing.T) {
	// PtgAreaN in D5 refers to $A$1:B3; the second corner stores offsets.
	tokens := []byte{0x4D, 0, 0, 0xFE, 0xFF, 0, 0, 0xFE, 0xFF}
	if got := decodeBIFFFormula(tokens, 4, 3); got != "=$A$1:B3" {
		t.Fatalf("ptgArea relative anchors: %s", got)
	}
}

func TestBIFF8DefaultColorPaletteComplete(t *testing.T) {
	if got := biffPaletteColor(0x38, nil); got != "#003366" {
		t.Fatalf("default palette index 0x38 lost: %s", got)
	}
	if got := biffPaletteColor(0x18, nil); got != "#9999FF" {
		t.Fatalf("default palette index 0x18 lost: %s", got)
	}
	if got := biffPaletteColor(0x3f, map[uint16]string{0x3f: "#123ABC"}); got != "#123ABC" {
		t.Fatalf("custom palette not respected: %s", got)
	}
}

// Small BIFF workbooks store Workbook in the OLE2 MiniStream. Construct an
// actual small CFB with a root MiniStream and MiniFAT, not a mock stream.
func TestBIFF8CFBMiniStream(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "normal.xls")
	writeBIFF8Fixture(t, big)
	payload, e := os.ReadFile(big)
	if e != nil {
		t.Fatal(e)
	}
	workbook := payload[512 : 512+4096]
	end, eofs := 0, 0
	for end+4 <= len(workbook) {
		id := binary.LittleEndian.Uint16(workbook[end:])
		sz := int(binary.LittleEndian.Uint16(workbook[end+2:]))
		end += 4 + sz
		if id == 0x000A {
			eofs++
			if eofs == 2 {
				break
			}
		}
	}
	if eofs != 2 || end > 512 {
		t.Fatalf("test workbook unexpected size: %d, eof=%d", end, eofs)
	}
	mini := make([]byte, 512)
	copy(mini, workbook[:end])
	out := make([]byte, 512+4*512)
	h := out[:512]
	copy(h[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	binary.LittleEndian.PutUint16(h[24:], 0x003e)
	binary.LittleEndian.PutUint16(h[26:], 3)
	binary.LittleEndian.PutUint16(h[28:], 0xfffe)
	binary.LittleEndian.PutUint16(h[30:], 9)
	binary.LittleEndian.PutUint16(h[32:], 6)
	binary.LittleEndian.PutUint32(h[44:], 1) // FAT count
	binary.LittleEndian.PutUint32(h[48:], 1) // dir first sector
	binary.LittleEndian.PutUint32(h[56:], 4096)
	binary.LittleEndian.PutUint32(h[60:], 3) // minifat first sector
	binary.LittleEndian.PutUint32(h[64:], 1)
	binary.LittleEndian.PutUint32(h[68:], cfbEnd)
	for i := 0; i < 109; i++ {
		binary.LittleEndian.PutUint32(h[76+i*4:], cfbFree)
	}
	binary.LittleEndian.PutUint32(h[76:], 2)
	copy(out[512:], mini)
	d := out[512+512:]
	mk := func(at int, name string, typ byte, sector, size uint32) {
		p := d[at : at+128]
		for i, c := range []rune(name) {
			binary.LittleEndian.PutUint16(p[2*i:], uint16(c))
		}
		binary.LittleEndian.PutUint16(p[64:], uint16((len([]rune(name))+1)*2))
		p[66] = typ
		binary.LittleEndian.PutUint32(p[68:], cfbFree)
		binary.LittleEndian.PutUint32(p[72:], cfbFree)
		binary.LittleEndian.PutUint32(p[76:], cfbFree)
		binary.LittleEndian.PutUint32(p[116:], sector)
		binary.LittleEndian.PutUint64(p[120:], uint64(size))
	}
	mk(0, "Root Entry", 5, 0, 512)
	mk(128, "Workbook", 2, 0, uint32(end))
	fat := out[512+2*512:]
	for i := 0; i < 128; i++ {
		binary.LittleEndian.PutUint32(fat[4*i:], cfbFree)
	}
	binary.LittleEndian.PutUint32(fat[0:], cfbEnd)
	binary.LittleEndian.PutUint32(fat[4:], cfbEnd)
	binary.LittleEndian.PutUint32(fat[8:], cfbFat)
	binary.LittleEndian.PutUint32(fat[12:], cfbEnd)
	mf := out[512+3*512:]
	for i := 0; i < 128; i++ {
		binary.LittleEndian.PutUint32(mf[4*i:], cfbFree)
	}
	count := (end + 63) / 64
	for i := 0; i < count; i++ {
		next := cfbEnd
		if i+1 < count {
			next = uint32(i + 1)
		}
		binary.LittleEndian.PutUint32(mf[4*i:], next)
	}
	small := filepath.Join(dir, "small.xls")
	if e = os.WriteFile(small, out, 0600); e != nil {
		t.Fatal(e)
	}
	wb, e := OpenWorkbook(small)
	if e != nil {
		t.Fatalf("small BIFF8: %v", e)
	}
	if wb.Sheets[0].Name != "财务台账" {
		t.Fatalf("small BIFF8 sheet: %+v", wb.Sheets)
	}
	idx, e := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if e != nil {
		t.Fatalf("small BIFF8 index: %v", e)
	}
	if got := cellValue(t, wb, idx, 2, 1); got != "000000123456789" {
		t.Fatalf("small BIFF8 value: %q", got)
	}
}

func TestBIFF8MalformedSSTRejected(t *testing.T) {
	input := [][]byte{{0, 0, 0, 0, 1, 0, 0, 0, 3, 0, 1, 0xff}}
	_, err := parseXLSSST(input)
	if err == nil || !strings.Contains(err.Error(), "SST") && !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "UTF") {
		t.Fatalf("malformed SST unexpectedly accepted: %v", err)
	}
}
