package workbook

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func syntheticIndex(t *testing.T, rows ...string) *SheetIndex {
	t.Helper()
	f := filepath.Join(t.TempDir(), "indexed.xml")
	var b strings.Builder
	idx := &SheetIndex{XMLPath: f, SharedAnchors: make(map[string]SharedFormulaAnchor)}
	for i, row := range rows {
		from := int64(b.Len())
		b.WriteString(row)
		idx.Rows = append(idx.Rows, RowOffset{Number: i + 1, Start: from, End: int64(b.Len())})
	}
	idx.XMLSize = int64(b.Len())
	if err := os.WriteFile(f, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestColumnCacheWholeViewAndHeaderAccuracy(t *testing.T) {
	idx := syntheticIndex(t,
		`<row r="1"><c r="A1" t="inlineStr"><is><t>标识</t></is></c></row>`,
		`<row r="2"><c r="A2" t="inlineStr"><is><t>ABC</t></is></c></row>`,
		`<row r="3"><c r="A3" t="inlineStr"><is><t>abc</t></is></c></row>`,
		`<row r="4"><c r="A4" t="inlineStr"><is><t>XYZ</t></is></c></row>`,
	)
	w := &Workbook{Styles: make([]StyleInfo, 2), NumFmts: make(map[int]string)}
	first, err := w.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "in", Values: []string{"abc"}}}, FilterOptions{HeaderRow: 1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, []int{0, 1, 2}) {
		t.Fatalf("unexpected first filter: %v", first)
	}
	if _, ok := w.cachedColumns(idx, []int{1}); !ok {
		t.Fatal("first full pass did not populate column cache")
	}
	// Removing the XML proves the subsequent lookup uses immutable column
	// data without silently reading a stale or different physical file.
	if err := os.Remove(idx.XMLPath); err != nil {
		t.Fatal(err)
	}
	second, err := w.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "equals", Value: "标识"}}, FilterOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, []int{0}) {
		t.Fatalf("header was missing from cache: %v", second)
	}
	sorted, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 1, Descending: true}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sorted, []int{0, 3, 1, 2}) {
		t.Fatalf("stable sort mismatch %v", sorted)
	}
	distinct, truncated, err := w.DistinctColumnValues(idx, 1, 1, 20, nil)
	if err != nil || truncated || len(distinct) != 3 {
		t.Fatalf("cached distinct failed: %+v %v %v", distinct, truncated, err)
	}
	w.ClearIndexes()
	if _, ok := w.cachedColumns(idx, []int{1}); ok {
		t.Fatal("stale column cache survived ClearIndexes")
	}
}

func TestFilterExactLongNumericAndStringIDs(t *testing.T) {
	idx := syntheticIndex(t,
		`<row r="1"><c r="A1"><v>9007199254740993</v></c></row>`,
		`<row r="2"><c r="A2"><v>9007199254740992</v></c></row>`,
		`<row r="3"><c r="A3" t="inlineStr"><is><t>001234567890123456789</t></is></c></row>`,
		`<row r="4"><c r="A4" t="inlineStr"><is><t>001234567890123456788</t></is></c></row>`,
	)
	w := &Workbook{NumFmts: make(map[int]string)}
	selected, err := w.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "equals", Value: "9007199254740993"}}, FilterOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []int{0}) {
		t.Fatalf("rounded numeric comparison returned wrong rows %v", selected)
	}
	selected, err = w.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "equals", Value: "001234567890123456788"}}, FilterOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []int{3}) {
		t.Fatalf("text id comparison lost digits %v", selected)
	}
	sorted, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sorted, []int{1, 0, 3, 2}) {
		t.Fatalf("numeric precision or text order wrong %v", sorted)
	}
}

func TestFilterCancellationDoesNotPublishPartialCache(t *testing.T) {
	idx := syntheticIndex(t, `<row r="1"><c r="A1"><v>1</v></c></row>`)
	cancel := make(chan struct{})
	close(cancel)
	w := &Workbook{}
	_, err := w.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "blank"}}, FilterOptions{}, cancel, nil)
	if err == nil {
		t.Fatal("expected cancelled query")
	}
	if _, ok := w.cachedColumns(idx, []int{1}); ok {
		t.Fatal("cancelled scan published incomplete cache")
	}
}

func TestCompiledFilterMatchesLegacyUnicodeCaseFold(t *testing.T) {
	// Go strings.EqualFold performs Unicode simple case folding. A lowercased
	// map alone is NOT equivalent (e.g. long s U+017F and ASCII s).
	values := []string{"aBc", "long ſ", "K", "中文", ""}
	allowed := []string{"ABC", "LONG S", "k", "中文", ""}
	for i, val := range values {
		rule := FilterRule{Operator: "in", Values: []string{allowed[i]}}
		legacy := false
		for _, s := range rule.Values {
			if strings.EqualFold(val, s) {
				legacy = true
			}
		}
		got := matchFilter(CellData{Display: val, Type: "inlineStr"}, true, rule)
		if got != legacy {
			t.Fatalf("Unicode fold val=%q allowed=%q got=%v want=%v", val, allowed[i], got, legacy)
		}
	}
}

// A mixed hit/miss must not change a filter's answer, even with sparse cells
// and far-apart columns. The cached column survives another sort-key request.
func TestColumnCachePartialHitIdenticalToColdScan(t *testing.T) {
	idx := syntheticIndex(t,
		`<row r="1"><c r="A1" t="inlineStr"><is><t>名称</t></is></c><c r="D1" t="inlineStr"><is><t>排序</t></is></c></row>`,
		`<row r="2"><c r="A2" t="inlineStr"><is><t>Foo</t></is></c><c r="D2"><v>20</v></c></row>`,
		`<row r="3"><c r="A3" t="inlineStr"><is><t>bar</t></is></c><c r="D3"><v>5</v></c></row>`,
		`<row r="4"><c r="A4" t="inlineStr"><is><t>FOO</t></is></c><c r="D4"><v>15</v></c></row>`,
		`<row r="5"><c r="D5"><v>1</v></c></row>`,
	)
	rules := []FilterRule{{Column: 1, Operator: "in", Values: []string{"foo", "bar"}}}
	options := FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 4}}}
	cold := &Workbook{NumFmts: make(map[int]string)}
	want, err := cold.FilterSheetWithOptions(idx, rules, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	warm := &Workbook{NumFmts: make(map[int]string)}
	_, err = warm.FilterSheetWithOptions(idx, rules, FilterOptions{HeaderRow: 1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, all := warm.cachedColumns(idx, []int{1, 4})
	if all || v[0] == nil || v[1] != nil {
		t.Fatalf("expected partial cache hit: %v %v", all, v)
	}
	got, err := warm.FilterSheetWithOptions(idx, rules, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partial cache differs: got %v want %v", got, want)
	}
	_, all = warm.cachedColumns(idx, []int{1, 4})
	if !all {
		t.Fatal("second query failed to populate missing sort column")
	}
}

func TestThreeKeySortBuildsBoundedReusableColumnCache(t *testing.T) {
	idx := syntheticIndex(t,
		`<row r="1"><c r="A1" t="inlineStr"><is><t>身份证号</t></is></c><c r="B1"><v>5</v></c><c r="D1"><v>9</v></c></row>`,
		`<row r="2"><c r="A2" t="inlineStr"><is><t>0001234567890123456789</t></is></c><c r="B2"><v>10</v></c><c r="D2"><v>3</v></c></row>`,
		`<row r="3"><c r="A3" t="inlineStr"><is><t>0001234567890123456788</t></is></c><c r="B3"><v>10</v></c><c r="D3"><v>1</v></c></row>`,
		`<row r="4"><c r="A4" t="inlineStr"><is><t>0001234567890123456788</t></is></c><c r="B4"><v>10</v></c><c r="D4"><v>2</v></c></row>`,
		`<row r="5"><c r="A5" t="inlineStr"><is><t>empty</t></is></c><c r="D5"><v>2</v></c></row>`,
	)
	options := FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 1}, {Column: 2, Descending: true}, {Column: 4}}}
	wb := &Workbook{NumFmts: make(map[int]string)}
	first, err := wb.FilterSheetWithOptions(idx, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	columns, ok := wb.cachedColumns(idx, []int{1, 2, 4})
	if !ok || columns[0] == nil || columns[1] == nil || columns[2] == nil {
		t.Fatal("three-column sort should populate all bounded column caches")
	}
	if err := os.Remove(idx.XMLPath); err != nil {
		t.Fatal(err)
	}
	second, err := wb.FilterSheetWithOptions(idx, nil, options, nil, nil)
	if err != nil {
		t.Fatalf("warm three-column sort unexpectedly read XML: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cold/warm sort mismatch: %v vs %v", first, second)
	}
}
