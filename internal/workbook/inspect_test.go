package workbook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInspectWorksheet(t *testing.T) (*Workbook, *SheetIndex) {
	t.Helper()
	xmlData := `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
		`<row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>数量</t></is></c></row>` +
		`<row r="2"><c r="A2" t="inlineStr"><is><t>abc</t></is></c><c r="B2"><v>1.00</v></c><c r="C2"><f>B2*2</f><v>2</v></c></row>` +
		`<row r="3"><c r="A3" t="inlineStr"><is><t>ABC</t></is></c><c r="B3"><v>1</v></c><c r="C3"><f>B3*2</f></c></row>` +
		`<row r="4"><c r="A4" t="inlineStr"><is><t>   </t></is></c><c r="B4" t="e"><v>#DIV/0!</v></c><c r="D4" s="0"/></row>` +
		`<row r="6"><c r="B6"><v>0</v></c></row>` +
		`</sheetData></worksheet>`
	rows, anchors, dim, maxR, maxC, err := scanWorksheet(strings.NewReader(xmlData), int64(len(xmlData)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sheet.xml")
	if err := os.WriteFile(file, []byte(xmlData), 0600); err != nil {
		t.Fatal(err)
	}
	return &Workbook{}, &SheetIndex{XMLPath: file, XMLSize: int64(len(xmlData)), RowIndex: NewRowIndex(rows), SharedAnchors: anchors, Dimension: dim, MaxRow: maxR, MaxCol: maxC}
}
func TestInspectSpecialMatchesAndBlankRows(t *testing.T) {
	wb, idx := testInspectWorksheet(t)
	tests := []struct {
		mode string
		want int
		refs []string
	}{
		{"formulas", 2, []string{"C2", "C3"}},
		{"constants", 9, []string{"A1", "B1", "A2", "B2", "A3", "B3", "A4", "B4"}},
		{"errors", 1, []string{"B4"}},
		{"missing_cache", 1, []string{"C3"}},
		{"blanks", 13, nil},
	}
	// A1:D6 has 24 slots, including a truly absent fifth row and a style-only D4.
	for _, tt := range tests {
		got, err := wb.InspectSelection(context.Background(), idx, InspectOptions{Mode: tt.mode, FirstRow: 1, LastRow: 6, FirstCol: 1, LastCol: 4, Limit: 100})
		if err != nil {
			t.Fatalf("%s %v", tt.mode, err)
		}
		if got.Matched != tt.want {
			t.Errorf("%s matched=%d want=%d refs=%+v", tt.mode, got.Matched, tt.want, got.Matches)
		}
		if len(tt.refs) > 0 {
			for _, ref := range tt.refs {
				has := false
				for _, x := range got.Matches {
					if x.Ref == ref {
						has = true
					}
				}
				if !has {
					t.Errorf("%s missing %s", tt.mode, ref)
				}
			}
		}
	}
}
func TestInspectDuplicatesCaseNumericAndTruncation(t *testing.T) {
	wb, idx := testInspectWorksheet(t)
	cases := []struct {
		column int
		want   int
		groups int
		refs   []string
	}{
		{1, 2, 1, []string{"A2", "A3"}},
		{2, 2, 1, []string{"B2", "B3"}},
		{3, 0, 0, nil},
	}
	for _, tc := range cases {
		got, err := wb.InspectSelection(context.Background(), idx, InspectOptions{Mode: "duplicates", FirstRow: 2, LastRow: 6, FirstCol: tc.column, LastCol: tc.column, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got.Matched != tc.want || got.DuplicateGroups != tc.groups {
			t.Errorf("column%d got %+v", tc.column, got)
		}
		if tc.want > 1 && !got.Truncated {
			t.Errorf("column%d should be truncated", tc.column)
		}
	}
}
func TestInspectRejectsHugeRangeAndCancelledContext(t *testing.T) {
	wb, idx := testInspectWorksheet(t)
	_, err := wb.InspectSelection(context.Background(), idx, InspectOptions{Mode: "blanks", FirstRow: 1, LastRow: 1048576, FirstCol: 1, LastCol: 100, Limit: 10})
	if err == nil {
		t.Fatal("expected bounded range")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = wb.InspectSelection(ctx, idx, InspectOptions{Mode: "formulas", FirstRow: 1, LastRow: 6, FirstCol: 1, LastCol: 4, Limit: 10})
	if err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
func TestInspectFilteredRowsKeepOriginalCoordinates(t *testing.T) {
	wb, idx := testInspectWorksheet(t)
	allowed := map[int]struct{}{3: {}}
	got, err := wb.InspectSelection(context.Background(), idx, InspectOptions{Mode: "formulas", FirstRow: 1, LastRow: 6, FirstCol: 1, LastCol: 4, Limit: 10, AllowedRows: allowed})
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched != 1 || got.Matches[0].Ref != "C3" {
		t.Fatalf("got %+v", got)
	}
}

func TestInspectDuplicateKeysPreserveExactValuesAndTypes(t *testing.T) {
	key := func(raw, kind string) string { return inspectValueKey(CellData{Raw: raw, Type: kind}) }
	for _, pair := range [][2]string{{"9007199254740992", "9007199254740993"}, {"1.00000000000000001", "1.00000000000000002"}, {"1e-400", "0"}} {
		if key(pair[0], "n") == key(pair[1], "n") {
			t.Fatalf("different source numbers collided: %v", pair)
		}
	}
	for _, pair := range [][2]string{{"1.00", "1e0"}, {"-0.000", "0"}, {"+00120.0", "12e1"}, {"1e1000000000", "10e999999999"}} {
		if key(pair[0], "n") != key(pair[1], "n") {
			t.Fatalf("equal decimal representations differ: %v", pair)
		}
	}
	for _, pair := range [][2]CellData{
		{{Raw: "1", Type: "b"}, {Raw: "1", Type: "str"}},
		{{Raw: "#N/A", Type: "e"}, {Raw: "#N/A", Type: "str"}},
		{{Raw: "001", Type: "inlineStr"}, {Raw: "1", Type: "n"}},
	} {
		if inspectValueKey(pair[0]) == inspectValueKey(pair[1]) {
			t.Fatalf("different source types collided: %+v", pair)
		}
	}
	if key("ABC", "str") != key("abc", "inlineStr") {
		t.Fatal("text representation should not change case-insensitive identity")
	}
}

func TestInspectExplicitSparseRowsIncludeSelectedBlankRowsOnly(t *testing.T) {
	wb, idx := testInspectWorksheet(t)
	for _, mode := range []string{"blanks", "formulas"} {
		got, err := wb.InspectSelection(context.Background(), idx, InspectOptions{Mode: mode, FirstRow: 2, LastRow: 1048576, FirstCol: 1, LastCol: 3, Limit: 20, RowNumbers: []int{2, 1048576}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Scanned != 6 {
			t.Fatalf("scanned unselected rows: %+v", got)
		}
		if mode == "blanks" && (got.Matched != 3 || got.Matches[0].Ref != "A1048576") || mode == "formulas" && (got.Matched != 1 || got.Matches[0].Ref != "C2") {
			t.Fatalf("wrong sparse inspection: %+v", got)
		}
	}
}
