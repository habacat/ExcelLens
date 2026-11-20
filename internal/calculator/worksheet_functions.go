package calculator

import (
	"cmp"
	"slices"
)

// The editor advertises the same builtins accepted by checkLocalFormula.
// Worksheet-only selectors and aggregates have different argument contracts.
func worksheetBuiltin(name string) bool {
	_, ok := formulaFunctions[name]
	return ok && name != "AVG"
}

// WorksheetFunctions lists functions offered while editing a workbook cell.
func WorksheetFunctions() []FunctionSpec {
	var items []FunctionSpec
	for _, spec := range formulaFunctions {
		f := spec.FunctionSpec
		switch {
		case f.Name == "ROW" || f.Name == "COLUMN":
			f.Signature = f.Name + "([引用])"
		case !worksheetBuiltin(f.Name):
			continue
		}
		items = append(items, f)
	}
	items = append(items, FunctionSpec{Name: "SUBTOTAL", Signature: "SUBTOTAL(汇总方式, 引用1, …)"})
	slices.SortFunc(items, func(a, b FunctionSpec) int { return cmp.Compare(a.Name, b.Name) })
	return items
}
