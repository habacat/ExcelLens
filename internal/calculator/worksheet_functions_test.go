package calculator

import (
	"strings"
	"testing"
)

func TestWorksheetFunctionSuggestionsAgreeWithFormulaParser(t *testing.T) {
	catalog := map[string]bool{}
	last := ""
	for _, spec := range WorksheetFunctions() {
		if spec.Name <= last || !strings.HasPrefix(spec.Signature, spec.Name+"(") {
			t.Fatalf("invalid or duplicate suggestion %+v", spec)
		}
		catalog[spec.Name], last = true, spec.Name
	}
	for name, spec := range formulaFunctions {
		n := max(1, spec.min)
		if spec.max == 0 {
			n = 0
		}
		args := strings.TrimSuffix(strings.Repeat("1,", n), ",")
		if name == "INDIRECT" {
			args = `"A1"`
		}
		if name == "LET" {
			args = "number,1,number"
		}
		_, err := ParseLocalFormula(t.Context(), name+"("+args+")", func(string) bool { return true }, nil)
		if (err == nil) != catalog[name] {
			t.Errorf("%s: offered=%t, parse error=%v", name, catalog[name], err)
		}
	}
	if !catalog["SUBTOTAL"] || !catalog["ROW"] || !catalog["SUMIFS"] || catalog["AVG"] {
		t.Fatal("worksheet-specific catalog entries")
	}
}
