package workbook

import (
	"bytes"
	"encoding/binary"
	"excellens/internal/formats"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Microsoft MS-XLS 3.3.6: B7 contains SQRT(B5*2); PtgRef stores row 4,
// column 1 with both relative-display flags. These are coordinates, not offsets.
func TestBIFFFormulaOfficialReferenceCoordinates(t *testing.T) {
	tokens := []byte{0x44, 4, 0, 1, 0xC0, 0x1E, 2, 0, 0x05, 0x41, 0x14, 0}
	if got := decodeBIFFFormula(tokens, 6, 1); got != "=SQRT((B5*2))" {
		t.Fatalf("ordinary PtgRef was interpreted as an offset: %s", got)
	}
}

func TestBIFFFormulaCoordinatesOffsetsLimitsAndStrictTokens(t *testing.T) {
	for _, item := range []struct {
		tokens   []byte
		row, col int
		want     string
	}{
		{[]byte{0x44, 4, 0, 1, 0xC0}, 6, 1, "=B5"},
		{[]byte{0x64, 4, 0, 1, 0x40}, 6, 1, "=B$5"},
		{[]byte{0x24, 4, 0, 1, 0x80}, 6, 1, "=$B5"},
		{[]byte{0x45, 0, 0, 2, 0, 0, 0, 1, 0xC0}, 4, 3, "=$A$1:B3"},
		{[]byte{0x4C, 0xFE, 0xFF, 0xFE, 0xFF}, 4, 3, "=B3"},
		{[]byte{0x2C, 0xFF, 0xFF, 0xFF, 0xFF}, 0, 0, "=IV65536"},
		{[]byte{0x41, 34, 0}, 0, 0, "=TRUE()"},
		{[]byte{0x41, 63, 0}, 0, 0, "=RAND()"},
		{[]byte{0x4C, 4, 0, 1, 0}, -1, -1, "=$B$5"},
	} {
		if got := decodeBIFFFormula(item.tokens, item.row, item.col); got != item.want {
			t.Fatal(item, got)
		}
	}
	for _, tokens := range [][]byte{{0x44, 0, 0, 0, 1}, {0x1D, 2}, {0x17, 1, 1, 0, 0xD8}, {0x41, 29, 0}, {0x1e, 1, 0, 0x1e, 2, 0}} {
		if got := decodeBIFFFormula(tokens, 4, 3); strings.HasPrefix(got, "=") {
			t.Fatal("invalid token sequence became a valid formula", tokens, got)
		}
	}
	if got := decodeBIFFFormula([]byte{0x4C, 0, 0, 0, 0xC0}, -1, -1); strings.HasPrefix(got, "=") {
		t.Fatal("origin-free metadata guessed a relative origin", got)
	}
}

func FuzzBIFFFormulaReconstruction(f *testing.F) {
	f.Add([]byte{0x44, 4, 0, 1, 0xC0, 0x1e, 2, 0, 0x05, 0x41, 0x14, 0}, uint16(6), byte(1))
	f.Add([]byte{0x4C, 0xFE, 0xFF, 0xFE, 0xFF}, uint16(4), byte(3))
	f.Add([]byte{0x17, 1, 1, 0, 0xD8}, uint16(0), byte(0))
	f.Fuzz(func(t *testing.T, tokens []byte, row uint16, col byte) {
		if len(tokens) > formats.DataRuleBIFFBytes {
			t.Skip()
		}
		before := bytes.Clone(tokens)
		formula := decodeBIFFFormula(tokens, int(row), int(col))
		if !bytes.Equal(tokens, before) {
			t.Fatal("formula reconstruction modified source tokens")
		}
		if strings.HasPrefix(formula, "=") && (len(formula) > formats.BIFFFormulaBytes+1 || !utf8.ValidString(formula)) {
			t.Fatal("rendered formula exceeded text/Unicode constraints")
		}
	})
}

func TestBIFFFormulaCorrectedCacheIgnoresPreviousBIFFNamespace(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "formula-coordinates.xls")
	formula := make([]byte, 22)
	binary.LittleEndian.PutUint16(formula, 6)
	binary.LittleEndian.PutUint16(formula[2:], 1)
	tokens := []byte{0x44, 4, 0, 1, 0xC0, 0x1e, 2, 0, 0x05, 0x41, 0x14, 0}
	binary.LittleEndian.PutUint16(formula[20:], uint16(len(tokens)))
	stream := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	offset := len(stream) + 4
	stream = appendBIFF(stream, 0x0085, biffCatalogueSheetFixture("原表", 0))
	stream = appendBIFF(stream, 0x000A, nil)
	binary.LittleEndian.PutUint32(stream[offset:], uint32(len(stream)))
	stream = appendBIFF(stream, 0x0809, []byte{0, 6, 0x10, 0})
	stream = appendBIFF(stream, 0x0006, append(formula, tokens...))
	stream = appendBIFF(stream, 0x000A, nil)
	writeBIFFCFBFixture(t, file, stream)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := w.ReadRow(idx, idx.FindRowOffset(7), 2, 2)
	if err != nil || len(row.Cells) != 1 || row.Cells[0].Formula != "=SQRT((B5*2))" {
		t.Fatal(row, err)
	}
	for _, path := range []string{idx.XMLPath, idx.IndexPath} {
		previous := strings.Replace(path, fmt.Sprintf("-biff%d", biffRowReaderVersion), "-biff2", 1)
		if previous == path {
			t.Fatal("new reader still uses old BIFF namespace")
		}
		if err := os.Rename(path, previous); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.GetCachedIndex(meta.Path) != nil {
		t.Fatal("old BIFF formula interpretation was reused")
	}
	idx, err = reopened.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err = reopened.ReadRow(idx, idx.FindRowOffset(7), 2, 2)
	if err != nil || row.Cells[0].Formula != "=SQRT((B5*2))" {
		t.Fatal(row, err)
	}
}
