package workbook

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"excellens/internal/rowindex"
)

func TestTextAmountSortModesExactAndStable(t *testing.T) {
	values := []string{"金额", "2", "10", "9007199254740993.31", "9007199254740993.30", "(2.00)", "￥1,234.50", "25%", "0.25", "abc", "", "0002"}
	lines := make([]string, len(values))
	for i, v := range values {
		lines[i] = fmt.Sprintf(`<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i+1, v)
	}
	idx := syntheticIndex(t, lines...)
	w := &Workbook{}
	cases := []struct {
		mode       string
		descending bool
		want       []int
	}{
		{"", false, []int{0, 5, 8, 11, 2, 1, 7, 4, 3, 9, 6, 10}},
		{"number", false, []int{0, 5, 7, 8, 1, 11, 2, 6, 4, 3, 9, 10}},
		{"number", true, []int{0, 3, 4, 6, 2, 1, 11, 7, 8, 5, 9, 10}},
		{"text", false, []int{0, 5, 8, 11, 2, 1, 7, 4, 3, 9, 6, 10}},
	}
	for pass := 0; pass < 2; pass++ {
		for _, tc := range cases {
			got, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 1, Mode: tc.mode, Descending: tc.descending}}}, nil, nil)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mode=%q desc=%v pass=%d: %v %v", tc.mode, tc.descending, pass, got, err)
			}
		}
		if pass == 0 {
			if err := os.Remove(idx.XMLPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1, Mode: "invalid"}}}, nil, nil); err == nil {
		t.Fatal("unknown conversion mode accepted")
	}
}

func TestSortUsesUnroundedRawAndDateSerialOnColdAndWarmQueries(t *testing.T) {
	idx := syntheticIndex(t,
		`<row r="1"><c r="A1" s="1"><v>10.004</v></c><c r="B1" s="2"><v>45323</v></c></row>`,
		`<row r="2"><c r="A2" s="1"><v>10.003</v></c><c r="B2" s="2"><v>45322</v></c></row>`,
	)
	w := &Workbook{Styles: []StyleInfo{{}, {NumFmtID: 164}, {NumFmtID: 165}}, NumFmts: map[int]string{164: "0.00", 165: "dd/mm/yyyy"}}
	for col := 1; col <= 2; col++ {
		cold := &Workbook{Styles: w.Styles, NumFmts: w.NumFmts}
		got, err := cold.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: col}}}, nil, nil)
		if err != nil || !reflect.DeepEqual(got, []int{1, 0}) {
			t.Fatalf("cold col=%d: %v %v", col, got, err)
		}
	}
	// Populate through distinct-value reads first: sorting must work even if
	// a different query created the column cache from visible values.
	for col := 1; col <= 2; col++ {
		if _, _, err := w.DistinctColumnValues(idx, col, 0, 20, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(idx.XMLPath); err != nil {
		t.Fatal(err)
	}
	for col := 1; col <= 2; col++ {
		for _, mode := range []string{"", "number", "text"} {
			got, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: col, Mode: mode}}}, nil, nil)
			want := []int{1, 0}
			if mode == "text" {
				want = []int{0, 1}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("col=%d mode=%q got %v err %v", col, mode, got, err)
			}
		}
	}
}

func TestPercentageSortExactTies(t *testing.T) {
	for _, pair := range [][2]string{{"57%", "0.57"}, {"28.5%", "0.285"}, {"9007199254740993%", "90071992547409.93"}} {
		a, b := makeSortValue(CellData{Type: "s", Raw: pair[0]}, "number"), makeSortValue(CellData{Type: "s", Raw: pair[1]}, "number")
		if got := rowindex.CompareSortNumeric(a.Text, b.Text, a.Number, b.Number); got != 0 {
			t.Fatalf("%v compared %d", pair, got)
		}
	}
}

func TestNumericRawCacheSurvivesDictionaryFallbackWithinBudget(t *testing.T) {
	const rows = 10000
	v := beginColumnCache(rows)
	for i := 0; i < rows; i++ {
		v = writeColumnCache(v, i, CellData{Type: "n", Display: fmt.Sprintf("%d.00", i), Raw: fmt.Sprintf("%d.000001", i)}, true)
	}
	if v == nil || v.codes != nil || v.bytes > columnCachePerColumnLimit || v.bytes < int64(rows)*81 {
		t.Fatal("raw cache budget/fallback invalid")
	}
	for _, row := range []int{0, 4095, 4096, rows - 1} {
		cell, ok := cacheCell(v, row)
		if !ok || cell.Raw != fmt.Sprintf("%d.000001", row) || cell.Display != fmt.Sprintf("%d.00", row) {
			t.Fatal("fallback lost raw value", cell)
		}
	}
}
