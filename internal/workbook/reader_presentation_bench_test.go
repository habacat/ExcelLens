package workbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Same source bytes and viewport for value-only and styled reads. This is a
// warm indexed-read microbenchmark, not an open-time or desktop measurement.
func BenchmarkInlinePresentationViewport(b *testing.B) {
	for _, complex := range []bool{false, true} {
		name, body := "simple", `<t>中😀001</t>`
		if complex {
			name = "four_runs"
			body = strings.Repeat(`<r><rPr><rFont val="Arial"/><sz val="11"/><b val="0"/><color rgb="FF1255AA"/></rPr><t>中😀001</t></r>`, 4)
		}
		var source strings.Builder
		idx := &SheetIndex{XMLPath: filepath.Join(b.TempDir(), "viewport.xml"), MaxRow: 300, MaxCol: 16}
		var offsets []RowOffset
		for row := 1; row <= 300; row++ {
			start := source.Len()
			fmt.Fprintf(&source, `<row r="%d">`, row)
			for col := 1; col <= 16; col++ {
				fmt.Fprintf(&source, `<c r="%s%d" t="inlineStr"><is>%s</is></c>`, ColumnName(col), row, body)
			}
			source.WriteString(`</row>`)
			offsets = append(offsets, RowOffset{Number: row, Start: int64(start), End: int64(source.Len())})
		}
		idx.RowIndex = NewRowIndex(offsets)
		idx.XMLSize = int64(source.Len())
		if err := os.WriteFile(idx.XMLPath, []byte(source.String()), 0600); err != nil {
			b.Fatal(err)
		}
		for _, styled := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/styled_%v", name, styled), func(b *testing.B) {
				w := &Workbook{}
				options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: styled}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					rows, err := w.ReadRowsWithOptionsContext(context.Background(), idx, 100, 80, 2, 9, options)
					if err != nil || len(rows) != 80 || len(rows[0].Cells) != 8 || rows[0].Cells[0].Raw == "" {
						b.Fatal(rows, err)
					}
				}
			})
		}
	}
}
