package workbook

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestFastSearchMatchesStandardDecoder(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "fixture.xlsx")
	writeTinyWorkbook(t, file)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	sheet, _ := wb.FindSheet("Data")
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ q, mode string }{
		{"A2+B2", "formula"}, {"A2", "reference"}, {"'Data'!A2", "reference"},
		{"A3+B3", "formula"}, {"1234", "value"}, {"2025-09-30", "value"},
		{"Hello", "value"}, {"Text", "value"}, {"unlikely-needle", "formula"},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"-"+tc.q, func(t *testing.T) {
			got, gt, err := wb.SearchSheet(idx, sheet.Name, tc.q, tc.mode, 100, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, wt, err := wb.searchSheetLegacy(idx, sheet.Name, tc.q, tc.mode, 100, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if gt != wt || !reflect.DeepEqual(got, want) {
				t.Fatalf("query=%q\nfast: %+v\nstandard: %+v", tc.q, got, want)
			}
		})
	}
}

func TestCoarseReferenceCandidateAlwaysIncludesPreciseReference(t *testing.T) {
	anchor := SharedFormulaAnchor{Ref: "C2", Formula: `SUM(A1,$B1,C$1,$D$1,'Data'!A1,"A1")`}
	candidates := newSharedRefCandidates(anchor)
	cases := []struct {
		cell     string
		col, row int
		possible bool
	}{
		{"C2", 1, 1, true}, {"D3", 2, 2, true},
		{"D3", 2, 1, false}, {"D3", 4, 1, true}, {"D3", 1, 2, false},
	}
	for _, tc := range cases {
		if got := candidates.potentiallyReferences(tc.cell, tc.col, tc.row); got != tc.possible {
			t.Fatalf("%s to %d,%d: got=%v want=%v", tc.cell, tc.col, tc.row, got, tc.possible)
		}
	}
}
