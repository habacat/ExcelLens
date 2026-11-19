package calculator

import (
	"context"
	"strings"
)

// Future function prefixes are part of the XLSX formula language. The saved
// expression uses them even though Excel's formula bar omits them.
func worksheetArrayName(name string) (prefix, bare string, ok bool) {
	bare = strings.TrimPrefix(strings.TrimPrefix(name, "_XLFN."), "_XLWS.")
	switch bare {
	case "FILTER", "SORT":
		return "_XLFN._XLWS.", bare, true
	case "ANCHORARRAY", "SEQUENCE", "UNIQUE", "TAKE", "DROP", "CHOOSECOLS", "CHOOSEROWS", "HSTACK", "VSTACK", "TOCOL", "TOROW", "TEXTSPLIT":
		return "_XLFN.", bare, true
	}
	return "", "", false
}

func (f EditableFormula) ArrayStorageFormula() string {
	var b strings.Builder
	at := 0
	var visit func(*formulaNode)
	visit = func(n *formulaNode) {
		if n.kind == "function" {
			if n.op == "ANCHORARRAY" && n.refOffset == n.args[0].refOffset {
				start := n.refOffset + 1
				end := start + n.args[0].ref.length
				b.WriteString(f.expression[at:start])
				b.WriteString("_xlfn.ANCHORARRAY(")
				b.WriteString(f.expression[start:end])
				b.WriteByte(')')
				at = end + 1
				return
			}
			prefix, _, ok := worksheetArrayName(n.op)
			offset := n.refOffset + 1
			if ok && !strings.HasPrefix(strings.ToUpper(f.expression[offset:]), "_XLFN.") {
				b.WriteString(f.expression[at:offset])
				b.WriteString(strings.ToLower(prefix))
				at = offset
			}
		}
		for _, arg := range n.args {
			visit(arg)
		}
	}
	visit(f.root)
	b.WriteString(f.expression[at:])
	return b.String()
}

func (f WorksheetFormula) EvaluateArray(ctx context.Context, resolve FormulaResolver, limit int) ([]FormulaValue, int, int, error) {
	v, err := f.evaluate(ctx, resolve, true)
	if err != nil {
		return nil, 0, 0, err
	}
	if v.Kind != "range" {
		return []FormulaValue{v}, 1, 1, nil
	}
	e := formulaEval{ctx: ctx, worksheet: true, date1904: date1904FromContext(ctx)}
	return materialize(&e, v, limit)
}
