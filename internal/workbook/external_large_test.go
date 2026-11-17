package workbook

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unlike XLSX, a plain-text financial ledger has no OOXML row limit.
// Exercise the real parser, durable on-disk index and random-seek path across
// row 1,048,576 (not merely a mocked writer counter).
func TestTSVLedgerCrossesExcelRowLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("large-file integration test")
	}
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "ledger.tsv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	if _, err := bw.WriteString("编号\t金额\t摘要\n"); err != nil {
		t.Fatal(err)
	}
	const dataRows = 1048581
	for i := 1; i <= dataRows; i++ {
		if i == dataRows {
			_, err = bw.WriteString(fmt.Sprintf("%012d\t1234567890.123456\tEND_OF_LONG_LEDGER\n", i))
		} else {
			_, err = bw.WriteString("000000001234\t1.23\t普通凭证\n")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
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
	if idx.MaxRow != dataRows+1 || idx.MaxCol != 3 || !strings.HasSuffix(idx.XMLPath, ".rows") {
		t.Fatalf("long ledger was truncated: rows=%d cols=%d cache=%q", idx.MaxRow, idx.MaxCol, idx.XMLPath)
	}
	last := cellValue(t, wb, idx, dataRows+1, 3)
	if last != "END_OF_LONG_LEDGER" {
		t.Fatalf("last line not addressable: %q", last)
	}
	if refCol, refRow, err := ParseCellRef(fmt.Sprintf("C%d", dataRows+1)); err != nil || refCol != 3 || refRow != dataRows+1 {
		t.Fatalf("large source reference unavailable: %d/%d %v", refCol, refRow, err)
	}
	reopened, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	cached := reopened.GetCachedIndex(reopened.Sheets[0].Path)
	if cached == nil || cached.MaxRow != dataRows+1 || cellValue(t, reopened, cached, dataRows+1, 3) != last {
		t.Fatal("large ledger cache did not reopen at the last row")
	}
}
