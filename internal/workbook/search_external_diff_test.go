package workbook

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestLiteralSearchAgreesWithUnicodeLower(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if lower := unicode.ToLower(r); r != lower && (literalSearchRune(lower) || literalSearchRune(r)) {
			t.Fatalf("case mapping %U -> %U crosses a literal search rune", r, lower)
		}
	}
	for _, value := range []string{"审计 00012.30 Kİ", "审核编號２０５", "ΑΣΣς", "\xff原文\xfe", "AZ[\\]az"} {
		for _, input := range []string{"", "审计", "00012.30", " 0", "K", "i", "ς", "原文", "�", "[\\]", "２０５"} {
			needle := strings.ToLower(input)
			if newSearchTextQuery(needle).contains(value) != strings.Contains(strings.ToLower(value), needle) {
				t.Fatalf("query %q value %q", input, value)
			}
		}
	}
}

func TestExternalValueSearchMatchesDecodedRows(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "search.rows"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := newExternalRowWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.offsets.Close()
	for i := range 9 {
		cells := []externalCell{{Col: 1, Type: 's', Raw: "κ K İ 数值"},
			{Col: 2, Type: 'n', Raw: "9007199254740993"}, {Col: 3, Type: 'b', Raw: "1"},
			{Col: 4, Type: '0'}, {Col: 5, Type: 's', Raw: "00007", HasFormula: true, Formula: "=A1"},
			{Col: 6, Type: 'F', Raw: "Σςσ", HasFormula: true, Formula: "=B2"}}
		if i%2 == 0 {
			for col := 7; col <= 72; col++ {
				cells = append(cells, externalCell{Col: col, Type: 's', Raw: "MiXeD文本"})
			}
		}
		if err := writer.cells(cells, 72); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.finish(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	w := &Workbook{}
	idx := &SheetIndex{XMLPath: file.Name(), XMLSize: writer.count, RowIndex: NewRowIndex(writer.offsets.Rows()), MaxRow: 9, MaxCol: 72}
	rows, err := w.ReadRowsContext(t.Context(), idx, 0, 9, 1, 72)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"K", "İ", "σ", "ς", "0000", "true", "NULL", "1993", "xEd", "文本", "no hits"} {
		var want []SearchResult
		needle := strings.ToLower(q)
		for _, row := range rows {
			for _, c := range row.Cells {
				if strings.Contains(strings.ToLower(c.Raw), needle) || strings.Contains(strings.ToLower(c.Display), needle) {
					want = append(want, SearchResult{Sheet: "Data", Row: row.Number, Col: c.Col, Ref: c.Ref, Value: c.Display})
				}
			}
		}
		got, truncated, err := w.SearchSheet(idx, "Data", q, "value", 5000, nil, nil)
		if err != nil || truncated || !slices.Equal(got, want) {
			t.Fatalf("%q results %d/%d truncated %t err %v", q, len(got), len(want), truncated, err)
		}
	}
	// An early text miss must not hide an invalid field later in the row.
	bad := []byte{2, 1, 's', 0, 0, 1, 'x', 2, 's', 0, 0}
	bad = binary.AppendUvarint(bad, maxExternalCell+1)
	query := newSearchTextQuery("absent")
	for _, opts := range []RowReadOptions{{}, {nativeValueQuery: &query}} {
		if _, err := w.decodeExternalRow(bad, 1, 1, 72, nil, opts); err == nil {
			t.Fatal("accepted corrupt row")
		}
	}
}
