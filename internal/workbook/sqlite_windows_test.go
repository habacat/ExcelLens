//go:build windows

package workbook

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSQLiteNativeReadonlyEndToEnd(t *testing.T) {
	isolateTestStorage(t)
	sample, err := os.ReadFile("testdata/ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "财务台账.db")
	if err := os.WriteFile(path, sample, 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Format != "sqlite" || len(wb.Sheets) != 3 {
		t.Fatalf("unexpected tables: %s %+v", wb.Format, wb.Sheets)
	}
	var sheet SheetMeta
	for _, m := range wb.Sheets {
		if strings.HasPrefix(m.Name, "财务") {
			sheet = m
			break
		}
	}
	if sheet.Name == "" {
		t.Fatalf("no finance table in %+v", wb.Sheets)
	}
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 3 || idx.MaxCol != 4 {
		t.Fatalf("shape: %+v", idx)
	}
	if got := cellValue(t, wb, idx, 2, 1); got != "0000000123456789" {
		t.Fatalf("identifier %q", got)
	}
	if got := cellValue(t, wb, idx, 2, 2); got != "100000000000.123456" {
		t.Fatalf("precise amount %q", got)
	}
	if got := cellValue(t, wb, idx, 2, 3); got != "[NULL]" {
		t.Fatalf("null value %q", got)
	}
	if got := cellValue(t, wb, idx, 2, 4); got != "[BLOB 3 bytes]" {
		t.Fatalf("blob label %q", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sample, after) {
		t.Fatal("native SQLite reader modified the database")
	}
	// Existing search works on the same generated row/index representation.
	matches, _, err := wb.SearchSheet(idx, sheet.Name, "0000000123456789", "value", 8, nil, nil)
	if err != nil || len(matches) != 1 || matches[0].Ref != "A2" {
		t.Fatalf("search: %v %+v", err, matches)
	}
}
func TestWindowsSQLiteCannotExecuteInjectedTableNames(t *testing.T) {
	sql := quoteSQLiteIdentifier(`A"; DROP TABLE finance;--`)
	if sql != `"A""; DROP TABLE finance;--"` {
		t.Fatalf("identifier is not safely escaped: %s", sql)
	}
}

func TestWindowsGB18030OracleFinancialExport(t *testing.T) {
	isolateTestStorage(t)
	raw, err := os.ReadFile("testdata/accounting-gbk.txt")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "oracle-gbk.txt")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if wb.SourceInfo.Encoding != "GB18030" {
		t.Fatalf("detection: %+v", wb.SourceInfo)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 3 || idx.MaxCol != 2 {
		t.Fatalf("shape: %+v", idx)
	}
	if x := cellValue(t, wb, idx, 2, 1); x != "现金" {
		t.Errorf("GB18030 account = %q", x)
	}
	if x := cellValue(t, wb, idx, 2, 2); x != "1,234.56" {
		t.Errorf("GB18030 amount = %q", x)
	}
}
