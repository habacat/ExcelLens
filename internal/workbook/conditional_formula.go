package workbook

import (
	"excellens/internal/conditional"
)

type conditionalDecimal = conditional.Decimal
type conditionalValue = conditional.EvalValue
type conditionalReference = conditional.Reference
type conditionalExpr = conditional.Expression[conditional.EvalValue]

func parseConditionalDecimal(raw string) (conditionalDecimal, bool) {
	return conditional.ParseDecimal(raw)
}

func compareConditionalDecimal(a, b conditionalDecimal) int {
	return conditional.CompareDecimal(a, b)
}

func (w *Workbook) compileConditionalExpr(raw, sheet string) (*conditionalExpr, int, string) {
	return conditional.CompileExpression(raw, sheet, func(name string) (conditional.Reference, string) {
		return w.conditionalDefinedName(sheet, name)
	})
}
