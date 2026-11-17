//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompositionWindowsSQLiteAndGB18030PreserveExactCachedStates(t *testing.T) {
	for _, name := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(name, func(t *testing.T) {
			isolateTestStorage(t)
			original, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			a, _ := recordOpen(t, file)
			definition := compositionDefinition(t, a)
			if name == "ledger.db" {
				wb := a.currentWorkbook()
				for _, meta := range wb.Sheets {
					if strings.HasPrefix(meta.Name, "财务") {
						definition.Inputs[0].Ref = compositionRef(t, a, sourceBookID(wb), "wb:"+meta.Name, 1)
						break
					}
				}
				definition.Fields = append(definition.Fields, "空值状态", "二进制状态")
				definition.Inputs[0].Columns = []int{1, 2, 3, 4}
				definition.Inputs[1].Columns = []int{2, 1, 0, 0}
			}
			snapshot := compositionCapture(t, a, definition)
			columns := []int{1, 2}
			if name == "ledger.db" {
				columns = append(columns, 3, 4)
			}
			rows := compositionRows(t, a, snapshot, columns...)
			if name == "ledger.db" {
				if rows[0].Cells[0].Data.Raw != "0000000123456789" || rows[0].Cells[1].Data.Raw != "100000000000.123456" || rows[0].Cells[2].Data.Raw != "[NULL]" || rows[0].Cells[3].Data.Raw != "[BLOB 3 bytes]" {
					t.Fatal("composition reinterpreted SQLite's known cache limitations", rows[0])
				}
			} else if rows[0].Cells[0].Data.Raw != "现金" {
				t.Fatal("GB18030 source text changed", rows[0])
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(original) {
				t.Fatal("source changed", err)
			}
		})
	}
}
