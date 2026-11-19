package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"os"
	"strconv"
	"testing"
)

func TestProvidedBIFFLedger2025Layout(t *testing.T) {
	path := os.Getenv("EXCELLENS_BIFF_LEDGER_2025")
	if path == "" {
		t.Skip("set EXCELLENS_BIFF_LEDGER_2025 to the private BIFF8 ledger")
	}
	isolateTestStorage(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(before)
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := wb.SourceLayout(wb.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 38347 || idx.MaxCol != 14 || len(layout.Merges) != 260 {
		t.Fatal(idx.MaxRow, idx.MaxCol, len(layout.Merges))
	}
	for _, expected := range []CellMerge{{Row: 2, Col: 1, EndRow: 2, EndCol: 13}, {Row: 6, Col: 3, EndRow: 6, EndCol: 13}, {Row: 8, Col: 5, EndRow: 9, EndCol: 5}} {
		found := false
		for _, m := range layout.Merges {
			found = found || m == expected
		}
		if !found {
			t.Fatal("missing source merge", expected)
		}
	}
	positions := iter.Seq[int](func(yield func(int) bool) {
		for i := range idx.RowCount() {
			if !yield(i) {
				return
			}
		}
	})
	digest := sha256.New()
	count := 0
	err = wb.VisitRowsContext(context.Background(), idx, positions, 1, 14, func(row RowData) error {
		for _, cell := range row.Cells {
			if cell.Raw == "" {
				continue
			}
			value := "s" + cell.Raw
			switch cell.Type {
			case "n":
				n, e := strconv.ParseFloat(cell.Raw, 64)
				if e != nil {
					return e
				}
				value = fmt.Sprintf("n%016x", math.Float64bits(n))
			case "b":
				value = "b" + cell.Raw
			case "e":
				value = "e" + cell.Raw
			}
			var header [12]byte
			binary.BigEndian.PutUint32(header[:], uint32(row.Number))
			binary.BigEndian.PutUint32(header[4:], uint32(cell.Col))
			binary.BigEndian.PutUint32(header[8:], uint32(len(value)))
			digest.Write(header[:])
			digest.Write([]byte(value))
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", digest.Sum(nil))
	if want := os.Getenv("EXCELLENS_BIFF_LEDGER_DIGEST"); want != "" && got != want {
		t.Fatalf("all-cell digest differs: %s != %s", got, want)
	}
	t.Logf("%d nonempty source coordinates and values: SHA-256 %s", count, got)
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("source changed", err)
	}
	t.Logf("BIFF8 ledger: %d rows x %d columns, %d source merges, original SHA-256 unchanged", idx.MaxRow, idx.MaxCol, len(layout.Merges))
}
