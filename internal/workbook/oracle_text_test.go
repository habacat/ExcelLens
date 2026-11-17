package workbook

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func oracleJournalFixture() (string, []string) {
	header := "凭证来源@批名@凭证编号@凭证名@日期@凭证类别@过帐人@创建人@审批人@原币@原币借@原币贷@本位币@本位借@本位贷"
	for i := 1; i <= 12; i++ {
		header += fmt.Sprintf("@segment%d@seg%d_desc", i, i)
	}
	var records []string
	for i, amount := range []string{"7.28", "6636.46", "0.000001"} {
		fields := []string{"费控系统", "批次,原文", fmt.Sprintf("00000000000000000%d", i+1), "记账", "23-09-21", "记账", "过帐人,", "创建人,", "不需要审批", "CNY", amount, "", "CNY", amount, ""}
		for segment := 1; segment <= 12; segment++ {
			fields = append(fields, "0001", "缺省")
		}
		fields[20] = `"原文引号，不属于 CSV`
		fields[38] = " 最后一列含空格 "
		records = append(records, strings.Join(fields, "@"))
	}
	return header, records
}

func TestOracleAtJournalJoinedHeaderAndLiteralFields(t *testing.T) {
	isolateTestStorage(t)
	header, records := oracleJournalFixture()
	for _, joined := range []bool{false, true} {
		for _, encoding := range []string{"utf8", "utf16le"} {
			t.Run(fmt.Sprintf("joined=%t/%s", joined, encoding), func(t *testing.T) {
				separator := "\n"
				if joined {
					separator = ""
				}
				original := header + separator + strings.Join(records, "\n") + "\n"
				data := []byte(original)
				if encoding == "utf16le" {
					data = []byte{0xff, 0xfe}
					for _, unit := range utf16.Encode([]rune(original)) {
						data = binary.LittleEndian.AppendUint16(data, unit)
					}
				}
				wb, idx := openIndexText(t, "journal.txt", data)
				if wb.SourceInfo.Delimiter != "@" || wb.source.oracle == nil || wb.source.oracle.joinedHeader != joined {
					t.Fatalf("wrong detection: %+v %+v", wb.SourceInfo, wb.source.oracle)
				}
				if idx.MaxRow != 4 || idx.MaxCol != 39 {
					t.Fatalf("shape %d x %d, want 4 x 39", idx.MaxRow, idx.MaxCol)
				}
				for row, record := range append([]string{header}, records...) {
					for col, want := range strings.Split(record, "@") {
						if got := cellValue(t, wb, idx, row+1, col+1); got != want {
							t.Fatalf("R%dC%d = %q, want %q", row+1, col+1, got, want)
						}
					}
				}
				reopened, err := OpenWorkbook(wb.Path)
				if err != nil || reopened.GetCachedIndex(reopened.Sheets[0].Path) == nil {
					t.Fatalf("cache reopen: %v", err)
				}
				if got := cellValue(t, reopened, idx, 4, 14); got != "0.000001" {
					t.Fatalf("cached financial value %q", got)
				}
			})
		}
	}
}

func TestOracleJoinedHeaderRequiresCompleteSchemaEvidence(t *testing.T) {
	header, records := oracleJournalFixture()
	for _, text := range []string{
		strings.Replace(header, "segment7", "other7", 1) + strings.Join(records, "\n") + "\n",
		header + records[0] + "\n" + records[1] + "\n",
	} {
		if got := detectOracleTextLayout(text, '@'); got != nil {
			t.Fatalf("unproven header repair: %+v", got)
		}
	}
}

func TestOracleAtJournalRejectsShiftedColumns(t *testing.T) {
	isolateTestStorage(t)
	header, records := oracleJournalFixture()
	path := filepath.Join(t.TempDir(), "broken.txt")
	data := header + "\n" + strings.Join(records, "\n") + "\n" + records[0] + "@unexpected\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err == nil || !strings.Contains(err.Error(), "40 列") {
		t.Fatalf("shifted columns accepted: %v", err)
	}
	if wb.GetCachedIndex(wb.Sheets[0].Path) != nil {
		t.Fatal("partial index was cached")
	}
}

func TestAtDelimiterAndUTF8SampleBoundaries(t *testing.T) {
	isolateTestStorage(t)
	wb, idx := openIndexText(t, "other.txt", []byte("账户@原币借@原币贷\n000123@1,234.50@\n000456@@0.01\n"))
	if wb.SourceInfo.Delimiter != "@" || idx.MaxCol != 3 || cellValue(t, wb, idx, 3, 3) != "0.01" {
		t.Fatal("ordinary @ delimiter was not recognized")
	}
	_, csvIdx := openIndexText(t, "emails.csv", []byte("ID,邮箱,金额\n0001,user@example.com,1.23\n0002,next@example.com,4.56\n"))
	if csvIdx.MaxCol != 3 {
		t.Fatal("email address was mistaken for a delimiter")
	}
	for _, value := range []string{"中", "🙂"} {
		encoded := []byte("ASCII," + value)
		for trim := 1; trim < len(value); trim++ {
			if got := detectTextEncoding(encoded[:len(encoded)-trim]); got != "UTF-8" {
				t.Fatalf("partial UTF-8 rune misidentified: %s", got)
			}
		}
	}
}
