package calculator

import (
	"strings"
)

type functionRegistrar func(string, string, int, int, func(*formulaEval, *formulaNode) (FormulaValue, error))

func registerExtendedFunctions(out map[string]functionSpec) {
	add := functionRegistrar(func(names, signature string, min, max int, run func(*formulaEval, *formulaNode) (FormulaValue, error)) {
		for _, name := range strings.Split(names, ",") {
			out[name] = functionSpec{FunctionSpec: FunctionSpec{Name: name, Signature: name + "(" + signature + ")"}, min: min, max: max, run: run}
		}
	})
	registerConditionalFunctions(add)
	registerLookupFunctions(add)
	registerMathFunctions(add)
	registerStatFunctions(add)
	registerTextFunctions(add)
	registerLogicFunctions(add)
	registerDateFunctions(add)
	registerFinanceFunctions(add)
	registerArrayFunctions(add)
}
