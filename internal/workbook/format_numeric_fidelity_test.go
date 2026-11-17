package workbook

import (
	"math"
	"testing"
)

func TestNumericDisplayPreservesZeroSectionsAndRawAudit(t *testing.T) {
	cases := []struct {
		name, raw, fmt, want string
		id                   int
	}{
		{"general negative zero", "-0", "", "0", 0},
		{"general negative zero decimal", "-0.000", "", "0", 0},
		{"general positive zero", "0", "", "0", 0},
		{"general positive number", "31.234", "", "31.234", 0},
		{"general negative number", "-0.01", "", "-0.01", 0},
		{"numeric fixed negative zero", "-0.0", "", "0.00", 2},
		{"numeric thousands negative zero", "-0", "", "0.00", 4},
		{"accounting negative zero", "-0", "", "-", 44},
		{"accounting positive zero", "0", "", "-", 44},
		{"accounting negative 0.0001", "-0.0001", "", "(0.00)", 44},
		{"custom literal dash negative zero", "-0", "#,##0.00;[Red](#,##0.00);\"-\"", "-", 164},
		{"custom escaped dash zero", "-0", "#,##0.00;[Red](#,##0.00);\\-", "-", 164},
		{"custom zero section", "0", "0.00;[Red](0.00);\"-\"", "-", 164},
		{"custom sign negative nonzero", "-0.01", "0.00;[Red](0.00);\"-\"", "(0.01)", 164},
		{"custom sign positive", "1.25", "0.00;[Red](0.00);\"-\"", "1.25", 164},
		{"plain text format? number still raw", "-0", "General", "0", 164},
		{"negative large", "-1234.50", "#,##0.00;[Red](#,##0.00);\"-\"", "(1,234.50)", 164},
		{"positive large", "1234.50", "#,##0.00;[Red](#,##0.00);\"-\"", "1,234.50", 164},
		{"zero with builtin decimal", "0", "", "0.00", 2},
		{"negative remains negative", "-1.99", "", "-1.99", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wb := &Workbook{Styles: []StyleInfo{{NumFmtID: tc.id}}, NumFmts: map[int]string{164: tc.fmt}}
			got := wb.formatNumeric(tc.raw, 0)
			if got != tc.want {
				t.Errorf("%q fmt=%q id=%d got %q want %q", tc.raw, tc.fmt, tc.id, got, tc.want)
			}
		})
	}
}

func TestNumericRawRemainsPreciseAndUnderflowIsNotMisrepresented(t *testing.T) {
	wb := &Workbook{Styles: []StyleInfo{{NumFmtID: 0}}, NumFmts: map[int]string{}}
	for _, value := range []string{"-0.0000001", "-1E-15", "-998877665544332211", "10000000000000001", "3.141592653589793"} {
		if got := wb.formatNumeric(value, 0); got != value {
			t.Fatalf("lossy display: %q -> %q", value, got)
		}
	}
	if got := wb.formatNumeric("-0", 0); got != "0" {
		t.Fatal(got)
	}
	if got := wb.formatNumeric("NaN", 0); got != "NaN" {
		t.Fatal(got)
	}
	if got := formatCustomNumber(math.Copysign(0, -1), `0.00;[Red](0.00);"-"`, false); got != "-" {
		t.Fatalf("negative signed zero custom = %q", got)
	}
}

func TestCachedSUMIFDisplayZeroAndFormulaUnchanged(t *testing.T) {
	wb := &Workbook{Styles: []StyleInfo{{NumFmtID: 164}}, NumFmts: map[int]string{164: `#,##0.00;[Red](#,##0.00);"-"`}}
	cell := xmlCell{Ref: "C3", Style: 0, Value: "-0", HasValue: true, Formula: &formulaXML{Text: `SUMIF(A1:A10,"日常",B1:B10)`}}
	got := wb.resolveCell(cell, 3, nil, true, true)
	if got.Display != "-" || got.Raw != "-0" || !got.HasFormula || got.Formula != `=SUMIF(A1:A10,"日常",B1:B10)` {
		t.Fatalf("cached sumif cell lost fidelity: %+v", got)
	}
}
