package workbook

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestTextFragmentOnlyCompletePhysicalRecordsAndSeparateCache(t *testing.T) {
	isolateTestStorage(t)
	header, records := oracleJournalFixture()
	for _, encoding := range []string{"utf8", "gb18030", "utf16le", "utf16be"} {
		t.Run(encoding, func(t *testing.T) {
			text := header + records[0] + "\n" + records[1] + "\n" + records[2] + "\n" + "末尾@未结束的@记录"
			data := []byte(text)
			switch encoding {
			case "gb18030":
				var err error
				data, err = simplifiedchinese.GB18030.NewEncoder().Bytes(data)
				if err != nil {
					t.Fatal(err)
				}
			case "utf16le", "utf16be":
				data = []byte{0xff, 0xfe}
				appendUnit := binary.LittleEndian.AppendUint16
				if encoding == "utf16be" {
					data = []byte{0xfe, 0xff}
					appendUnit = binary.BigEndian.AppendUint16
				}
				for _, unit := range utf16.Encode([]rune(text)) {
					data = appendUnit(data, unit)
				}
				data = append(data, 0x42) // incomplete trailing UTF-16 code unit
			}
			path := filepath.Join(t.TempDir(), "fragment.txt")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			wb, err := OpenTextFragment(path)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if idx.MaxRow != 4 || idx.MaxCol != 39 || !wb.SourceInfo.Incomplete {
				t.Fatal("incomplete record was exposed", idx.MaxRow, idx.MaxCol)
			}
			for row, record := range append([]string{header}, records...) {
				for col, want := range strings.Split(record, "@") {
					if got := cellValue(t, wb, idx, row+1, col+1); got != want {
						t.Fatalf("R%dC%d = %q, want %q", row+1, col+1, got, want)
					}
				}
			}
			regular, err := OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			if regular.Fingerprint == wb.Fingerprint || regular.GetCachedIndex(regular.Sheets[0].Path) != nil {
				t.Fatal("partial index leaked into normal import")
			}
			if _, err := regular.BuildIndex(regular.Sheets[0], nil, nil); err == nil {
				t.Fatal("normal import stopped validating EOF")
			}
			if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
				t.Fatal("raw fragment changed")
			}
		})
	}
}

func TestTextFragmentRefusesAmbiguousQuotesAndInternalDamage(t *testing.T) {
	isolateTestStorage(t)
	for _, text := range []string{"账户,金额\n0001,2\n\"多行\n被切断", "没有换行的片段"} {
		path := filepath.Join(t.TempDir(), "fragment.txt")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenTextFragment(path); err == nil {
			t.Fatal("ambiguous fragment accepted")
		}
	}
	header, records := oracleJournalFixture()
	text := header + "\n" + strings.Join(records, "\n") + "\n" + records[0] + "@bad\n末条片段"
	path := filepath.Join(t.TempDir(), "fragment.txt")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenTextFragment(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err == nil || !strings.Contains(err.Error(), "40 列") {
		t.Fatal("internal corruption was hidden", err)
	}
}
