package workbook

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func openIndexText(t *testing.T, name string, content []byte) (*Workbook, *SheetIndex) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if wb.Format == "" || len(wb.Sheets) != 1 {
		t.Fatalf("unexpected source meta: %#v", wb)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("import modified the source")
	}
	return wb, idx
}
func cellValue(t *testing.T, w *Workbook, idx *SheetIndex, row, col int) string {
	t.Helper()
	got, err := w.ReadRow(idx, row-1, col, col)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range got.Cells {
		if cell.Col == col {
			return cell.Raw
		}
	}
	return ""
}
func TestCSVPreservesFinancialTextAndQuotedMultiline(t *testing.T) {
	isolateTestStorage(t)
	src := "单号,金额,摘要,公式字面量\r\n0000123456789012345,123456789012345.000001,\"到账,\n跨行\",=1+2\r\n0002,-123.45,\"A&B<测试>\",\" \"\r\n"
	wb, idx := openIndexText(t, "ledger.csv", []byte(src))
	if idx.MaxRow != 3 || idx.MaxCol != 4 {
		t.Fatalf("bad shape rows=%d cols=%d", idx.MaxRow, idx.MaxCol)
	}
	if got := cellValue(t, wb, idx, 2, 1); got != "0000123456789012345" {
		t.Fatal(got)
	}
	if got := cellValue(t, wb, idx, 2, 2); got != "123456789012345.000001" {
		t.Fatal(got)
	}
	if got := cellValue(t, wb, idx, 2, 3); got != "到账,\n跨行" {
		t.Fatalf("multiline %q", got)
	}
	if got := cellValue(t, wb, idx, 2, 4); got != "=1+2" {
		t.Fatalf("formula must be literal: %q", got)
	}
	if got := cellValue(t, wb, idx, 3, 3); got != "A&B<测试>" {
		t.Fatalf("xml escaping: %q", got)
	}
	if got := cellValue(t, wb, idx, 3, 4); got != " " {
		t.Fatalf("whitespace should survive: %q", got)
	}
	matches, _, err := wb.SearchSheet(idx, "数据", "0000123456789012345", "value", 10, nil, nil)
	if err != nil || len(matches) != 1 || matches[0].Ref != "A2" {
		t.Fatalf("search %v %+v", err, matches)
	}
	if wb2, err := OpenWorkbook(wb.Path); err != nil || wb2.GetCachedIndex(wb2.Sheets[0].Path) == nil {
		t.Fatal("cache was not reused", err)
	}
}
func TestTSVUTF16AndPipeOracleExport(t *testing.T) {
	isolateTestStorage(t)
	for _, tc := range []struct{ name, text, enc string }{
		{"journal.tsv", "科目\t期初余额\t借方\n001\t0.000\t12.50\n", "utf8"},
		{"oracle.txt", "科目|流水号|金额\n现金|0000123|1,234.50\n", "utf8"},
		{"journal-u16.txt", "科目\t余额\n现金\t1000.00\n", "u16le"},
		{"journal-u16be.txt", "科目\t余额\n《现金》\t1000.00\n", "u16be"},
	} {
		data := []byte(tc.text)
		if tc.enc != "utf8" {
			bom := []byte{0xFF, 0xFE}
			if tc.enc == "u16be" {
				bom = []byte{0xFE, 0xFF}
			}
			data = append([]byte{}, bom...)
			for _, r := range utf16.Encode([]rune(tc.text)) {
				var b [2]byte
				if tc.enc == "u16be" {
					binary.BigEndian.PutUint16(b[:], r)
				} else {
					binary.LittleEndian.PutUint16(b[:], r)
				}
				data = append(data, b[:]...)
			}
		}
		t.Run(tc.name, func(t *testing.T) {
			wb, idx := openIndexText(t, tc.name, data)
			if idx.MaxRow != 3 && idx.MaxRow != 2 {
				t.Fatalf("rows=%d", idx.MaxRow)
			}
			if got := cellValue(t, wb, idx, 2, 1); got != "001" && got != "现金" && got != "《现金》" {
				t.Fatalf("first data: %q", got)
			}
			if tc.enc != "utf8" && wb.SourceInfo.Encoding != "UTF-16 LE" && wb.SourceInfo.Encoding != "UTF-16 BE" {
				t.Fatal(wb.SourceInfo.Encoding)
			}
		})
	}
}
func TestSQLPlusFixedWidthRecognizesHeadersAndPages(t *testing.T) {
	isolateTestStorage(t)
	src := "账户      借方金额      贷方金额\n--------  ------------  ------------\n现金          1000.05          0.00\n银行           250.00          0.00\n\f\n账户      借方金额      贷方金额\n--------  ------------  ------------\n往来           500.00         12.40\n3 rows selected.\n"
	wb, idx := openIndexText(t, "oracle-ledger.txt", []byte(src))
	if wb.SourceInfo.Delimiter != "Oracle SQL*Plus 定宽列" {
		t.Fatalf("detection %q", wb.SourceInfo.Delimiter)
	}
	if idx.MaxCol != 3 || idx.MaxRow != 4 {
		t.Fatalf("fixed shape %d %d", idx.MaxCol, idx.MaxRow)
	}
	if got := cellValue(t, wb, idx, 2, 1); !strings.Contains(got, "现金") {
		t.Fatalf("first field %q", got)
	}
	if got := cellValue(t, wb, idx, 4, 3); !strings.Contains(got, "12.40") {
		t.Fatalf("last credit %q", got)
	}
}
func TestExternalIndexCancellationAndRowLimits(t *testing.T) {
	isolateTestStorage(t)
	p := filepath.Join(t.TempDir(), "cancel.csv")
	if err := os.WriteFile(p, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(p)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, done); err == nil {
		t.Fatal("cancel was ignored")
	}
	f, err := os.CreateTemp(t.TempDir(), "large-*.rows")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	writer, err := newExternalRowWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	writer.rows = maxExcelRows
	if err := writer.row([]string{"one-more"}); err != nil {
		t.Fatalf("external data must not inherit Excel row limit: %v", err)
	}
	writer.rows = maxExternalRows
	if err := writer.row([]string{"over safety limit"}); err == nil {
		t.Fatal("external index safety limit was ignored")
	}
}
func TestExtensionAndEscaping(t *testing.T) {
	for _, ext := range []string{".XLSX", ".xlsm", ".xls", ".CSV", ".tsv", ".txt", ".db", ".sqlite", ".sqlite3"} {
		if !SupportedExtensions(ext) {
			t.Fatal(ext)
		}
	}
	if SupportedExtensions(".exe") {
		t.Fatal("unsupported extension accepted")
	}
	got := escapeExternalXML("\x00&<\r=SUM(1,2)")
	for _, text := range []string{"�", "&amp;", "&lt;", "&#13;", "=SUM(1,2)"} {
		if !strings.Contains(got, text) {
			t.Fatalf("missing %q in %q", text, got)
		}
	}
}

func TestSQLiteWALFingerprintAndSnapshotInvalidation(t *testing.T) {
	isolateTestStorage(t)
	root := t.TempDir()
	path := filepath.Join(root, "ledger.db")
	walPath := path + "-wal"
	if err := os.WriteFile(path, []byte("SQLite main body snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPath, []byte("WAL-one"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, st, err := fingerprintFile(path, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	wb, err := newExternalWorkbook(path, "sqlite", fingerprint, st)
	if err != nil {
		t.Fatal(err)
	}
	if err = wb.checkExternalUnchanged(); err != nil {
		t.Fatalf("stable snapshot invalid: %v", err)
	}
	if err = os.WriteFile(walPath, []byte("WAL-two-with-more-rows"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = wb.checkExternalUnchanged(); err == nil {
		t.Fatal("WAL changes were ignored")
	}
	newFP, _, err := fingerprintFile(path, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == newFP {
		t.Fatal("WAL modification did not invalidate content fingerprint")
	}
	if err = os.Remove(walPath); err != nil {
		t.Fatal(err)
	}
	if err = wb.checkExternalUnchanged(); err == nil {
		t.Fatal("removed WAL was ignored")
	}
}
