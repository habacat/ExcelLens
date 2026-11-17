package analysis

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUnicodeCellLimitAcrossEditsPersistenceAndBackup(t *testing.T) {
	for _, symbol := range []string{"文", "😀"} {
		t.Run(symbol, func(t *testing.T) {
			value := strings.Repeat(symbol, MaxCellChars)
			w := EmptyWorkspace()
			w.Sheets = []Sheet{{ID: "edit", Name: "中文长文本", Columns: []string{"原文"}}}
			if _, err := w.Sheets[0].ApplyEdits([]CellChange{{Row: 0, Col: 0, Value: value}}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "workspace.json")
			if err := SaveWorkspace(path, w); err != nil {
				t.Fatal(err)
			}
			w.Sheets[0].Rows[0][0] = value + symbol
			if err := ValidateWorkspace(w); err == nil {
				t.Fatal("overlong backup accepted")
			}
			if err := SaveWorkspace(path, w); err == nil {
				t.Fatal("overlong save accepted")
			}
			loaded, err := LoadWorkspace(path)
			if err != nil || loaded.Sheets[0].Rows[0][0] != value {
				t.Fatalf("original Unicode value changed: %v", err)
			}
			if err := ValidateWorkspace(loaded); err != nil {
				t.Fatal(err)
			}
			if err := SaveWorkspace(path, loaded); err != nil {
				t.Fatal(err)
			}
			backup, err := LoadWorkspace(path + ".bak")
			if err != nil || backup.Sheets[0].Rows[0][0] != value {
				t.Fatalf("Unicode checkpoint changed: %v", err)
			}
		})
	}
}

func TestInvalidCellTextRejectsEntireTransaction(t *testing.T) {
	for _, value := range []string{strings.Repeat("😀", MaxCellChars+1), string([]byte{0xff})} {
		s := Sheet{Columns: []string{"值"}, Rows: [][]string{{"保留"}}}
		if _, err := s.ApplyEdits([]CellChange{{Row: 0, Col: 0, Value: "覆盖"}, {Row: 1, Col: 0, Value: value}}); err == nil {
			t.Fatal("invalid Unicode text accepted")
		}
		if len(s.Rows) != 1 || s.Rows[0][0] != "保留" || s.Revision != 0 {
			t.Fatal("invalid transaction partially committed")
		}
	}
}
