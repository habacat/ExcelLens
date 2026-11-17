package workbook

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// The optimized one-pass attribute reader must interpret every normal OOXML
// spelling exactly as the original reference helper does, including reordered
// attributes and both XML quote styles.
func TestFastCellAttributeReferenceParity(t *testing.T) {
	variants := []string{
		`<c r="A1" s="7" t="s">`,
		`<c t='inlineStr' foo='bar' s='11' r='XFD1048576' />`,
		`<c xmlns="urn:office" abc='weird' r = 'C7' s = " 5 " t = 'n' >`,
		`<c r="A12" t="str" s="0"/>`,
		`<c t="n" r="AB12" foo="x" s="8">`,
		`<c custom="r=misleading" r="D4" x="1" t="b" s="99">`,
		`<c foo='r="ABC"' r='A2' s='003' t='inlineStr'>`,
		`<c r='A1' r='B2' t='s' t='n' s='2' s='4'>`,
	}
	for i, tag := range variants {
		ref, typ, style := xmlCellAttributes([]byte(tag))
		for _, tc := range []struct {
			name string
			got  []byte
		}{{"r", ref}, {"t", typ}, {"s", style}} {
			want := xmlAttribute([]byte(tag), tc.name)
			if !reflect.DeepEqual(tc.got, want) {
				t.Fatalf("case %d %q %s got %q want %q", i, tag, tc.name, tc.got, want)
			}
		}
	}
	// Deterministic pseudo-random attribute orders reveal inadvertent last-wins.
	rng := rand.New(rand.NewSource(20261110))
	for k := 0; k < 1500; k++ {
		fields := []string{`r="Q37"`, `s='29'`, `t="inlineStr"`, `hello='World'`}
		rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
		tag := []byte("<c " + strings.Join(fields, "\n\t") + " />")
		r, y, s := xmlCellAttributes(tag)
		if string(r) != "Q37" || string(y) != "inlineStr" || string(s) != "29" {
			t.Fatalf("invalid attributes: %q", tag)
		}
	}
}

func TestSortingStableWithSameKeysAndExactNumeric(t *testing.T) {
	lines := make([]string, 0, 150)
	for i := 0; i < 150; i++ {
		v := "9007199254740993"
		if i%3 == 0 {
			v = "9007199254740992"
		}
		lines = append(lines, fmt.Sprintf(`<row r="%d"><c r="A%d"><v>%s</v></c><c r="B%d" t="inlineStr"><is><t>same</t></is></c></row>`, i+1, i+1, v, i+1))
	}
	idx := syntheticIndex(t, lines...)
	w := &Workbook{Styles: []StyleInfo{{NumFmtID: 0}}}
	got, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 2}, {Column: 1}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lines) {
		t.Fatalf("got %d rows", len(got))
	}
	var smaller, larger []int
	for i := 0; i < len(lines); i++ {
		if i%3 == 0 {
			smaller = append(smaller, i)
		} else {
			larger = append(larger, i)
		}
	}
	want := append(smaller, larger...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unstable or lossy sort: got %v want %v", got, want)
	}
}

func TestGeneralFastPathLeavesRawDigitsAndFormatsAlone(t *testing.T) {
	w := &Workbook{Styles: []StyleInfo{{NumFmtID: 0}, {NumFmtID: 164}}, NumFmts: map[int]string{164: "General"}}
	for _, raw := range []string{"9007199254740993", "00012345678901234567890", "-0.0001", "-1e-999", "NaN", "Infinity", "1.234567890123456789"} {
		if got := w.formatNumeric(raw, 0); got != raw {
			t.Fatalf("General raw %q changed to %q", raw, got)
		}
	}
	for _, raw := range []string{"-0", "-0.00", "0.000e-10", "0e999", "+0"} {
		if got := w.formatNumeric(raw, 0); got != "0" {
			t.Fatalf("General zero %q: %q", raw, got)
		}
	}
	// A custom format explicitly called General remains on the legacy code
	// path: it can deliberately canonicalize scientific exponent notation.
	if got := w.formatNumeric("1e3", 1); got != "1000" {
		t.Fatal(got)
	}
}
