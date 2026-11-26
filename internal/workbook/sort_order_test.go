package workbook

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"excellens/internal/rowindex"
)

func TestNaturalSortIsExplicitStableAndNeverParsesIntegerRuns(t *testing.T) {
	values := []string{"项目10", "项目2", "项目002", "项目1", "项目2-10", "项目2-2", "", "项目" + strings.Repeat("9", 1000), "项目1" + strings.Repeat("0", 1000)}
	var lines []string
	for i, text := range values {
		lines = append(lines, fmt.Sprintf(`<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i+1, text))
	}
	idx := syntheticIndex(t, lines...)
	w := &Workbook{CacheDir: t.TempDir()}
	for _, tc := range []struct {
		desc bool
		want []int
	}{
		{false, []int{3, 1, 2, 5, 4, 0, 7, 8, 6}},
		{true, []int{8, 7, 0, 4, 5, 1, 2, 3, 6}},
	} {
		got, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1, Mode: "natural", Descending: tc.desc}}}, nil, nil)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("desc=%v: %v / %v want %v", tc.desc, got, err, tc.want)
		}
	}
	plain, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1, Mode: "text"}}}, nil, nil)
	if err != nil || slices.Index(plain, 0) >= slices.Index(plain, 1) {
		t.Fatal("ordinary text mode unexpectedly changed to natural ordering", plain, err)
	}
	for _, pair := range [][2]string{{"file000", "file0"}, {"a0002b0003", "a2b3"}, {"000", "0"}} {
		if rowindex.CompareNaturalText(pair[0], pair[1]) != 0 {
			t.Fatal("leading-zero numeric run was not stable", pair)
		}
	}
}
