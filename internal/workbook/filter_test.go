package workbook

import "testing"

func TestMatchFilterNumericDisplayValues(t *testing.T) {
	cases := []struct {
		name string
		cell CellData
		rule FilterRule
		want bool
	}{
		{"thousands greater", CellData{Display: "1,234.50"}, FilterRule{Operator: "gt", Value: "1200"}, true},
		{"currency equality", CellData{Display: "¥1,234.50"}, FilterRule{Operator: "equals", Value: "1234.5"}, true},
		{"percent equality", CellData{Display: "12.50%"}, FilterRule{Operator: "equals", Value: "12.5%"}, true},
		{"accounting negative", CellData{Display: "(1,234.50)"}, FilterRule{Operator: "lt", Value: "-1000"}, true},
		{"text equality", CellData{Display: "债券"}, FilterRule{Operator: "equals", Value: "债券"}, true},
		{"text mismatch", CellData{Display: "债券"}, FilterRule{Operator: "equals", Value: "股票"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchFilter(tc.cell, true, tc.rule); got != tc.want {
				t.Fatalf("matchFilter() = %v, want %v", got, tc.want)
			}
		})
	}
}
