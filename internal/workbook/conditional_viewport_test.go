package workbook

import (
	"context"
	"slices"
	"testing"

	"excellens/internal/conditional"
	"excellens/internal/formats"
)

func TestWideConditionalViewportChargesOnlyRuleCandidates(t *testing.T) {
	isolateTestStorage(t)
	xml := `<worksheet><sheetData><row r="1"><c r="B1"><v>7</v></c></row><row r="2"><c r="B2"><v>8</v></c></row></sheetData><conditionalFormatting sqref="A1:A2 XFD1:XFD2"><cfRule type="expression" priority="1" dxfId="0"><formula>$B1&gt;0</formula></cfRule></conditionalFormatting></worksheet>`
	w, err := OpenWorkbook(sourceMetadataWorkbook(t, xml, conditionalStylesFixture))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{1, 2}, 1, formats.ExcelColumns, RowReadOptions{ConditionalFormats: true})
	if err != nil {
		t.Fatal(err)
	}
	check := func(row RowData) {
		if row.PresentationNotice != "" || len(row.Cells) != 3 || row.Cells[1].Col != 2 || row.Cells[1].Conditional != nil {
			t.Fatalf("row %d: %d cells, notice %q", row.Number, len(row.Cells), row.PresentationNotice)
		}
		for i, col := range []int{1, formats.ExcelColumns} {
			c := row.Cells[2*i]
			if c.Col != col || !c.PresentationOnly || c.Conditional == nil || c.Conditional.Matched != 1 {
				t.Fatalf("row %d missed sparse rule at column %d", row.Number, c.Col)
			}
		}
	}
	for _, row := range rows {
		check(row)
	}
	columns := []int{formats.ExcelColumns, 2, 1, formats.ExcelColumns}
	err = w.VisitRowsWithOptionsContext(context.Background(), idx, slices.Values([]int{0, 1}), 1, formats.ExcelColumns,
		RowReadOptions{ConditionalFormats: true, Columns: columns}, func(row RowData) error {
			check(row)
			return nil
		})
	if err != nil || !slices.Equal(columns, []int{formats.ExcelColumns, 2, 1, formats.ExcelColumns}) {
		t.Fatal(columns, err)
	}
}

func TestConditionalGapSkipsAgreeWithSourceRectangles(t *testing.T) {
	groups := [][]CellMerge{
		{{Row: 1, Col: 2, EndRow: 3, EndCol: 4}, {Row: 5, Col: 90, EndRow: 10, EndCol: 100}},
		{{Row: 4, Col: 8, EndRow: 17, EndCol: 9}, {Row: 7, Col: 4, EndRow: 11, EndCol: 54}},
		{{Row: 4, Col: 8, EndRow: 9, EndCol: 9}, {Row: 15, Col: 99, EndRow: 18, EndCol: 99}},
	}
	idx := SourceConditionalFormats{}
	var entries []sourceRangeEntry
	for id, group := range groups {
		idx.groups = append(idx.groups, conditionalGroup{ruleIndices: []int{id}})
		for _, area := range group {
			entries = append(entries, sourceRangeEntry{area, id})
		}
	}
	idx.nodes, idx.root = buildSourceRangeTree(entries)
	e := idx.NewEvaluator(context.Background())
	for row := 1; row <= 19; row++ {
		next := 1
		for col := 1; col <= 101; col++ {
			var want []int
			for id, group := range groups {
				for _, a := range group {
					if row >= a.Row && row <= a.EndRow && col >= a.Col && col <= a.EndCol {
						want = append(want, id)
						break
					}
				}
			}
			if col < next && len(want) != 0 {
				t.Fatalf("skipped rule at (%d,%d)", row, col)
			}
			if msg := e.prepare(row, col); msg != "" || !slices.Equal(e.rules, want) {
				t.Fatalf("(%d,%d): rules %v want %v, %s", row, col, e.rules, want, msg)
			}
			if len(want) == 0 && col >= next {
				next = e.nextCol
			}
		}
	}
}

func TestConditionalSourceRowBorrowingAndNonDataCells(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cells []CellData
		want  []int
	}{
		{"dense", []CellData{{Col: 3, Raw: "3"}, {Col: 4, Raw: "4"}}, []int{3, 4}},
		{"sparse", []CellData{{Col: 1, Raw: "001", Type: "s"}, {Col: 4, Raw: "4"}}, []int{1, 4}},
		{"irregular", []CellData{{Col: 4, Raw: "old"}, {Col: 1, Raw: "001", Type: "s"}, {Col: 4, Raw: "4"}}, []int{1, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := indexConditionalRow(tc.cells)
			if src.position(2) != -1 || src.cell(4).Raw != "4" {
				t.Fatal("lookup lost sparse or duplicate coordinates")
			}
			out := conditional.RangeData{}
			src.appendRange(&out, 7, 1, 4)
			var got []int
			for _, c := range out.Cells {
				got = append(got, c.Col)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatal(got)
			}
			view := src.withExtra([]CellData{{Col: 2, PresentationOnly: true}})
			if len(view) != len(tc.cells)+1 || src.position(2) != -1 || src.cell(4).Raw != "4" {
				t.Fatal("presentation changed source")
			}
			for i := 1; i < len(view); i++ {
				if view[i].Col < view[i-1].Col {
					t.Fatal("unsorted presented row")
				}
			}
		})
	}
}
