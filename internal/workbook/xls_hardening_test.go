package workbook

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBIFF8FormulaTokenClassVariantsAndRelativeReference(t *testing.T) {
	// BIFF ptgRef in class R/V/A differs by 0x20; the reference must not be lost.
	for _, op := range []byte{0x24, 0x44, 0x64} {
		got := decodeBIFFFormula([]byte{op, 2, 0, 1, 0}, 4, 3)
		if got != "=$B$3" {
			t.Fatalf("ptgRef class %02X: %s", op, got)
		}
	}
	got := decodeBIFFFormula([]byte{0x4C, 0xFE, 0xFF, 0xFE, 0xFF}, 4, 3)
	if got != "=B3" {
		t.Fatalf("BIFF relative row/col reference: %s", got)
	}
}

func TestBIFF8RejectsEncryptedWorkbook(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "encrypted.xls")
	writeBIFF8Fixture(t, path)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	// Insert FILEPASS in the BIFF global stream directly after BOF.
	pos := 512 + 12
	copy(raw[pos+6:], raw[pos:512+4096-6])
	binary.LittleEndian.PutUint16(raw[pos:], 0x002F)
	binary.LittleEndian.PutUint16(raw[pos+2:], 2)
	raw[pos+4], raw[pos+5] = 0, 0
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, e = OpenWorkbook(path)
	if e == nil || !strings.Contains(e.Error(), "加密") {
		t.Fatalf("encrypted workbook must fail explicitly: %v", e)
	}
}
