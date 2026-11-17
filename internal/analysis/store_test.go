package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditableWorkspacePersistenceAndRecovery(t *testing.T) {
	path := WorkspacePath(t.TempDir(), "testfp")
	original := EmptyWorkspace()
	original.Sheets = append(original.Sheets, Sheet{ID: "edit1", Name: "测试", Columns: []string{"机构", "金额"}, Rows: [][]string{{"A", "0.10"}}})
	if err := SaveWorkspace(path, original); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Sheets = []Sheet{{ID: "edit1", Name: "测试", Columns: []string{"机构", "金额"}, Rows: [][]string{{"B", "100.25"}}}}
	if err := SaveWorkspace(path, changed); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(path)
	if err != nil || loaded.Sheets[0].Rows[0][1] != "100.25" {
		t.Fatalf("updated persistence %v %v", loaded, err)
	}
	checkpoint, err := LoadWorkspace(path + ".bak")
	if err != nil || checkpoint.Sheets[0].Rows[0][1] != "0.10" {
		t.Fatalf("checkpoint wrong %v err %v", checkpoint, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recovered, err := LoadWorkspace(path)
	if err != nil || recovered.Sheets[0].Rows[0][1] != "0.10" {
		t.Fatalf("backup recovery wrong %+v err=%v", recovered, err)
	}
	if filepath.Dir(path) == "" {
		t.Fatal("missing workspace root")
	}
}
func TestScratchEditsAndBounds(t *testing.T) {
	sheet := Sheet{Columns: []string{"编号", "金额"}, Rows: [][]string{}}
	if err := sheet.Edit(2, 1, "9007199254740993"); err != nil {
		t.Fatal(err)
	}
	if len(sheet.Rows) != 3 || sheet.Rows[2][1] != "9007199254740993" {
		t.Fatalf("bad sparse edit %+v", sheet)
	}
	if err := sheet.Paste(0, 0, [][]string{{"A", "0.1"}, {"B", "0.2"}}); err != nil {
		t.Fatal(err)
	}
	if sheet.Rows[1][1] != "0.2" {
		t.Fatal("paste wrong")
	}
	for _, tc := range []struct {
		row, col int
		value    string
	}{{-1, 0, ""}, {MaxSheetRows, 0, ""}, {0, 2, ""}, {0, 0, strings.Repeat("x", MaxCellChars+1)}} {
		if err := sheet.Edit(tc.row, tc.col, tc.value); err == nil {
			t.Errorf("invalid edit accepted: %+v", tc)
		}
	}
	invalid := EmptyWorkspace()
	invalid.Sheets = []Sheet{{ID: "x", Name: "bad", Columns: []string{"X"}, Rows: [][]string{{strings.Repeat("x", MaxCellChars+1)}}}}
	if err := SaveWorkspace(filepath.Join(t.TempDir(), "bad.json"), invalid); err == nil {
		t.Fatal("out-of-bound workspace accepted")
	}
}
