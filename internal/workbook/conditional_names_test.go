package workbook

import (
	"context"
	"encoding/xml"
	"errors"
	"excellens/internal/conditional"
	"strings"
	"testing"
)

func TestConditionalNameScopeAndUnavailableSource(t *testing.T) {
	w := &Workbook{Sheets: []SheetMeta{{Name: "Ledger"}, {Name: "O'Brien"}}, NamedRanges: map[string]string{"limit": "'O''Brien'!$B$1", "bad": "[absent.xlsx]Other!$A$1", "partial": "O'Brien!A1"}, LocalNamedRanges: map[string]map[string]string{"ledger": {"blocked": ""}}}
	w.NamedRanges["blocked"] = "'O''Brien'!$B$1"
	for _, name := range []string{"bad", "partial", "blocked"} {
		if _, _, why := w.compileConditionalExpr("IFERROR("+name+",0)", "Ledger"); why == "" {
			t.Error("unavailable name accepted", name)
		}
	}
	expr, _, why := w.compileConditionalExpr("IFERROR(LiMiT,0)", "Ledger")
	if why != "" {
		t.Fatal(why)
	}
	e := (&SourceConditionalFormats{}).NewEvaluator(context.Background())
	e.engine.Row, e.engine.Col = 7, 4
	e.engine.ForeignCell = func(sheet string, row, col int) (conditional.CellOperand, string, error) {
		if sheet != "O'Brien" || row != 1 || col != 2 {
			t.Fatalf("bound %s %d %d", sheet, row, col)
		}
		return conditional.CellOperand{CacheMissing: true}, "", nil
	}
	if _, why, err := e.engine.Eval(expr, 1, 1, nil); err != nil || !strings.Contains(why, "没有保存") {
		t.Fatalf("missing formula cache swallowed: %q %v", why, err)
	}
}
func TestForeignConditionalCellBudgetIsNotBlank(t *testing.T) {
	w := &Workbook{}
	raw := `<worksheet><sheetData><row r="1"><c r="A1"><v>2</v></c></row><row r="5"><c r="B5"><v>8</v></c></row></sheetData></worksheet>`
	for _, tc := range []struct {
		row, col int
		want     string
	}{{1, 1, "2"}, {3, 1, ""}, {5, 1, ""}, {5, 2, "8"}, {9, 1, ""}} {
		c, e := w.readConditionalXMLCell(xml.NewDecoder(strings.NewReader(raw)), tc.row, tc.col)
		if e != nil || c.Raw != tc.want {
			t.Fatal(tc, c, e)
		}
	}
	budget := 20
	_, err := w.readConditionalXMLCell(xml.NewDecoder(conditionalInput{strings.NewReader(raw), context.Background(), &budget}), 5, 2)
	if !errors.Is(err, ErrPreviewUnavailable) {
		t.Fatalf("byte limit became a blank: %v", err)
	}
}
