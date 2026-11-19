package workbook

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Some OOXML producers omit c@r. Inferring those cells is part of preserving
// source fidelity: viewport, filters and exports must agree on the address.
func TestImplicitCellReferencesStrictAndFastParity(t *testing.T) {
	row := `<row r="7"><c r="A7"><v>100</v></c><c t="inlineStr"><is><t>00123</t></is></c><c r="D7"><f>SUM(A7,B7)</f><v>100</v></c><c t="inlineStr"><is><t>&amp;x</t></is></c><c r="G7"><v>3</v></c><c><v>4</v></c></row>`
	idx := syntheticIndex(t, row)
	idx.ResidentRows[0].Number = 7
	w := &Workbook{NumFmts: map[int]string{}}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var scratch []byte
	for _, tc := range []struct {
		first, last int
		selected    map[int]struct{}
	}{
		{1, 8, nil},
		{2, 2, map[int]struct{}{2: {}}},
		{4, 5, map[int]struct{}{4: {}, 5: {}}},
		{8, 8, nil},
	} {
		want, err := w.readRowFromFileSelected(f, idx, 0, tc.first, tc.last, tc.selected, true, true)
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.fastReadRowWithOptions(f, idx, 0, tc.first, tc.last, tc.selected, true, true, &scratch)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("range %d:%d got=%+v want=%+v", tc.first, tc.last, got, want)
		}
	}
	got, err := w.ReadRow(idx, 0, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 0, len(got.Cells))
	for _, cell := range got.Cells {
		refs = append(refs, cell.Ref)
	}
	want := []string{"A7", "B7", "D7", "E7", "G7", "H7"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("bad implicit cell positions: got=%v want=%v", refs, want)
	}
	filtered, err := w.FilterSheetWithOptions(idx, []FilterRule{{Column: 2, Operator: "equals", Value: "00123"}}, FilterOptions{}, nil, nil)
	if err != nil || !reflect.DeepEqual(filtered, []int{0}) {
		t.Fatalf("implicit-cell filter: %v %v", filtered, err)
	}
}

func TestImplicitCellExtentInScan(t *testing.T) {
	xml := `<worksheet><sheetData><row r="7"><c r="A7"><v>1</v></c><c><v>2</v></c><c r="F7"/><c><v>3</v></c></row></sheetData></worksheet>`
	rows, _, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(xml), int64(len(xml)), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Number != 7 || maxRow != 7 || maxCol != 7 {
		t.Fatalf("content extent failed: rows=%+v maxRow=%d maxCol=%d", rows, maxRow, maxCol)
	}
}

func TestBrokenExplicitCellReferenceFailsClosed(t *testing.T) {
	idx := syntheticIndex(t, `<row r="1"><c r="Definitely-not-an-address"><v>1</v></c></row>`)
	w := &Workbook{}
	_, err := w.ReadRow(idx, 0, 1, 16384)
	if err == nil {
		t.Fatal("malformed coordinate silently ignored")
	}
	if !strings.Contains(err.Error(), "无效单元格地址") {
		t.Fatalf("unexpected error: %v", err)
	}
}
