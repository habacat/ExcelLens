package workbook

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Production source-number viewport reads, including XML decoding, sparse
// references, conditional application and the returned original cells.
func BenchmarkConditionalViewportRead(b *testing.B) {
	for _, step := range []int{1, 7} {
		b.Run(fmt.Sprintf("column_step_%d", step), func(b *testing.B) {
			dir := b.TempDir()
			for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
				b.Setenv(key, dir)
			}
			var xml strings.Builder
			xml.WriteString("<worksheet><sheetData>")
			for r := 1; r <= 320; r++ {
				fmt.Fprintf(&xml, "<row r=\"%d\">", r)
				for c := 1; c <= 48; c += step {
					fmt.Fprintf(&xml, "<c r=\"%s%d\"><v>%d</v></c>", ColumnName(c), r, (r-1)%3+1)
				}
				xml.WriteString("</row>")
			}
			xml.WriteString(`</sheetData><conditionalFormatting sqref="B1:B318"><cfRule type="expression" priority="1" dxfId="0"><formula>SUM($A1:$A3)=6</formula></cfRule></conditionalFormatting></worksheet>`)
			w, err := OpenWorkbook(sourceMetadataWorkbook(b, xml.String(), conditionalStylesFixture))
			if err != nil {
				b.Fatal(err)
			}
			idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
			if err != nil {
				b.Fatal(err)
			}
			rows := make([]int, 80)
			for i := range rows {
				rows[i] = 100 + i
			}
			opts := RowReadOptions{ConditionalFormats: true, ConditionalStatsReadOnly: true}
			b.ReportAllocs()
			for b.Loop() {
				got, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, rows, 1, 48, opts)
				if err != nil || len(got) != 80 {
					b.Fatal("viewport incomplete", err)
				}
				for i, row := range got {
					if row.Number != rows[i] || row.Cells[1].Col != 2 || row.Cells[1].Conditional == nil || row.Cells[1].Conditional.Matched != 1 || row.Cells[1].PresentationOnly != (step > 1) {
						b.Fatal("source or conditional style mismatch", row.Number)
					}
				}
			}
		})
	}
}

// Exercise each source order through the same public row-number API. Inline
// text makes the indexed input larger than the sequential read-ahead window.
func BenchmarkSourceNumberRead(b *testing.B) {
	dir := b.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		b.Setenv(key, dir)
	}
	const rowCount = 8192
	var xml strings.Builder
	xml.WriteString("<worksheet><sheetData>")
	payload := strings.Repeat("column", 20)
	for n := 1; n <= rowCount; n++ {
		fmt.Fprintf(&xml, `<row r="%d"><c r="A%d"><v>%d</v></c><c r="B%d" t="inlineStr"><is><t>%s</t></is></c></row>`, n, n, n, n, payload)
	}
	xml.WriteString("</sheetData></worksheet>")
	w, err := OpenWorkbook(sourceMetadataWorkbook(b, xml.String(), conditionalStylesFixture))
	if err != nil {
		b.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	forward, reverse := make([]int, 80), make([]int, 80)
	for i := range forward {
		forward[i], reverse[i] = 4000+i, 4079-i
	}
	for _, p := range []struct {
		name string
		rows []int
	}{
		{"single", []int{4000}}, {"forward", forward}, {"reverse", reverse},
		{"scattered", []int{5, 8180, 1327, 5859, 2493, 7444, 3751, 4802}},
	} {
		b.Run(p.name, func(b *testing.B) {
			expected := make([]string, len(p.rows))
			for i, n := range p.rows {
				expected[i] = fmt.Sprint(n)
			}
			b.ReportAllocs()
			for b.Loop() {
				rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, p.rows, 1, 1, RowReadOptions{})
				if err != nil || len(rows) != len(p.rows) {
					b.Fatal("incomplete source rows", err)
				}
				for i, row := range rows {
					if row.Number != p.rows[i] || len(row.Cells) != 1 || row.Cells[0].Col != 1 || row.Cells[0].Raw != expected[i] {
						b.Fatal("incorrect source coordinate or raw value", row.Number)
					}
				}
			}
		})
	}
}
