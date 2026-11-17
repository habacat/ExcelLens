package server

import (
	"testing"

	"excellens/internal/workbook"
)

func TestSourceNumbersRespectNativeTypes(t *testing.T) {
	for _, format := range []string{"xlsx", "xlsm", "xls", "xls-html", "csv", "tsv", "txt", "sqlite", "unknown"} {
		textAmounts := format == "xls-html" || format == "csv" || format == "tsv" || format == "txt" || format == "sqlite"
		for _, tc := range []struct {
			name string
			cell workbook.CellData
			want bool
		}{
			{"number", workbook.CellData{Raw: "1234.50", Type: "n"}, true},
			{"implicit-number", workbook.CellData{Raw: "-0.01"}, true},
			{"numeric-text", workbook.CellData{Raw: "1234.50", Type: "inlineStr"}, textAmounts},
			{"shared-string", workbook.CellData{Raw: "1234.50", Type: "s"}, textAmounts},
			{"formula-text", workbook.CellData{Raw: "1234.50", Type: "str", HasFormula: true}, textAmounts},
			{"accounting", workbook.CellData{Raw: "(1,234.50)", Type: "inlineStr"}, textAmounts},
			{"identifier", workbook.CellData{Raw: "00001234", Type: "inlineStr"}, false},
			{"boolean", workbook.CellData{Raw: "1", Type: "b"}, false},
			{"error", workbook.CellData{Raw: "1", Type: "e"}, false},
			{"missing-cache", workbook.CellData{Raw: "1234.50", Type: "n", HasFormula: true, CacheMissing: true}, false},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				if got := sourceCellNumeric(&workbook.Workbook{Format: format}, tc.cell); got != tc.want {
					t.Fatalf("number=%t, want %t for %+v", got, tc.want, tc.cell)
				}
			})
		}
	}
}
