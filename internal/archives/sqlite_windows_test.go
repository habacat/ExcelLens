//go:build windows

package archives

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

func TestSQLiteArchiveIncludesCommittedWALRows(t *testing.T) {
	isolateArchiveStorage(t)
	a, err := Open(context.Background(), filepath.Join("testdata", "sqlite-wal.zip"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Members) != 1 || len(a.walMembers) != 1 {
		t.Fatal("WAL should accompany DB rather than appear as an unrelated data file")
	}
	path, _, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "ledger" {
		t.Fatalf("schema in WAL was lost: %+v", wb.Sheets)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 3, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1].Cells[0].Raw != "000123" || rows[2].Cells[1].Raw != "2.000001" {
		t.Fatalf("committed WAL rows missing: %+v", rows)
	}
	after, err := os.ReadFile(path + "-wal")
	if err != nil || string(before) != string(after) {
		t.Fatal("read-only query changed stored WAL bytes", err)
	}
}
