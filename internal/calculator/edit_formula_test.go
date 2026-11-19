package calculator

import (
	"strings"
	"testing"
)

func TestEditableFormulaPreservesQualifiedAndAbsoluteReferences(t *testing.T) {
	sheets := func(name string) bool { return name == "财务'账!册" || name == "Q4" }
	source := `=IF(SUM('财务''账!册'!$A1:B$4,Q4!C3)>0,$D$6,"A1""B2")`
	parsed, err := ParseEditableFormula(t.Context(), source, sheets)
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := parsed.Shift(2, 1)
	want := `=IF(SUM('财务''账!册'!$A3:C$4,Q4!D5)>0,$D$6,"A1""B2")`
	if err != nil || shifted != want {
		t.Fatalf("shifted=%q want=%q error=%v", shifted, want, err)
	}
	if _, err := ParseEditableFormula(t.Context(), shifted, sheets); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Shift(-2, 0); err == nil || !strings.Contains(err.Error(), "行列范围") {
		t.Fatalf("out-of-sheet shift got %v", err)
	}
	for _, source := range []string{`=ROUND(1e-2+A1,2)`, `=TRUE()`, `=LEN("#CALC!")`, `=COUNTIF(A1:A9,">0")`,
		`=SUM(A:A)`, `=SUM(1:4)`, `=SUM({1,2})`, `=sum(,A1)`, `=VLOOKUP(A1,A:B,2,FALSE)`,
		`=MATCH(1,B:B,0)`, `=COUNTIFS(A:A,1,B:B,2)`, `=SUMPRODUCT(A1:A4,B1:B4)`,
		`=INDEX(A:B,MATCH(1,A:A,0),2)`, `=INDEX($1:$1,3)`, `=INDEX(A:A,2)`, `=INDEX(A1:B1,0,2)`,
		`=INDEX({1,2;3,4},2,1)`, `=ROW()+COLUMN()+ROWS(A:A)+COLUMNS(1:2)`, `=ROW(2:2)+COLUMN(B:B)`, `=ISNA(NA())`} {
		if _, err := ParseEditableFormula(t.Context(), source, sheets); err != nil {
			t.Fatalf("ordinary formula %s: %v", source, err)
		}
	}
}

func TestEditableFormulaRejectsUnverifiedScalarAndReferenceSyntax(t *testing.T) {
	sheets := func(name string) bool { return name == "Data" }
	for _, formula := range []string{
		`=A1:A3`, `=A1:A3+1`, `=IF(TRUE,A1:A3,0)`, `=COUNTIFS(A:A,B1:B2)`, `=VLOOKUP(A1,A:B,{1,2},FALSE)`, `=SEQUENCE(4)`, `=_xlfn.SUM(A1)`,
		`=Data!XFE1`, `=Data!A1048577`, `=Data!A01`, `='[book.xlsx]Data'!A1`, `=Data:Other!A1`, `=Other!A1`, `=cmd|'data'!A1`,
		`=Table1[Amount]`, `=MOD(1)`, `=stored_name`, `=1 +1`, `= A1`, `=1＋1`, `=SUM(A1)&`,
		`=INDEX(A:B,0,2)`, `=INDEX(A:B,1,)`, `=INDEX(A1:B3,1)`, `=INDEX(A:B,{1;2},1)`,
		`=INDEX(A:B,MATCH({1;2},A:A,0),2)`, `=ROW(A:A)`, `=COLUMN(1:1)`, `=ROW(A1,B1)`, `=ROWS()`,
	} {
		if _, err := ParseEditableFormula(t.Context(), formula, sheets); err == nil {
			t.Fatalf("accepted unverified expression %q", formula)
		}
	}
}

func TestSharedFormulaTranslationKeepsAxisAndNameBoundaries(t *testing.T) {
	body := `SUMIFS('Q1'!$A:$B,B:B,"B1",C2:C9,Rate)+SUM($1:2)+$C3+D$4`
	f, err := ParseLocalFormula(t.Context(), body, func(s string) bool { return s == "Q1" }, func(s string) (bool, bool) { return true, s == "RATE" })
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.Shift(2, 3)
	if err != nil || raw != `=SUMIFS('Q1'!$A:$B,E:E,"B1",F4:F11,Rate)+SUM($1:4)+$C5+G$4` {
		t.Fatal(raw, err)
	}
}
