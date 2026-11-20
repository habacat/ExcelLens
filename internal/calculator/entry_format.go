package calculator

import "strings"

// EntryFormatOperand is either the top-left cell of a worksheet reference or an
// intrinsic Excel number format. Order and repeated references are significant.
type EntryFormatOperand struct {
	// Empty means the current sheet. An explicit reference to the current
	// sheet is equivalent to an unqualified reference.
	Sheet    string
	Row, Col int
	Builtin  int
	// Excel clears an ordinary choice across a non-inheriting function
	// containing multiplication, comparison or another non-additive operator.
	AfterArithmetic bool
	SumTime         bool
}

// EntryFormatOperands inspects the parsed expression without evaluating cells
// or enumerating ranges. A reference in another sheet still calculates normally.
func (f EditableFormula) EntryFormatOperands(sheet string) []EntryFormatOperand {
	var operands []EntryFormatOperand
	var visit func(*formulaNode, bool, bool) bool
	visit = func(n *formulaNode, inherit, sumTime bool) bool {
		if n == nil {
			return false
		}
		switch n.kind {
		case "union":
			return false
		case "ref":
			if inherit {
				r := WorksheetBounds(sheet, n.ref.start, n.ref.end)
				other := n.ref.sheet
				if strings.EqualFold(other, sheet) {
					other = ""
				}
				operands = append(operands, EntryFormatOperand{Sheet: other, Row: r.Top, Col: r.Left, SumTime: sumTime})
			}
		case "binary", "unary":
			if n.op != "+" && n.op != "-" {
				return false
			}
			for _, arg := range n.args {
				if !visit(arg, inherit, sumTime) {
					return false
				}
			}
		case "function":
			builtin, pass := functionEntryFormat(n.op)
			afterArithmetic := false
			for _, arg := range n.args {
				if !visit(arg, inherit && pass, sumTime || n.op == "SUM") {
					if !inherit {
						return false
					}
					if pass {
						operands = nil
					} else {
						afterArithmetic = true
					}
					break
				}
			}
			if !inherit {
				builtin = 0
			}
			if afterArithmetic || builtin != 0 {
				operands = append(operands, EntryFormatOperand{Builtin: builtin, AfterArithmetic: afterArithmetic})
			}
		}
		return true
	}
	if !visit(f.root, true, false) {
		return nil
	}
	return operands
}

func functionEntryFormat(name string) (builtin int, inherit bool) {
	switch name {
	case "SUM", "AVERAGE", "MIN", "MAX", "ROUND", "ROUNDUP", "ROUNDDOWN", "INT", "TRUNC", "MOD":
		return 0, true
	case "DATE":
		return 14, false
	case "TIME":
		return 18, false
	}
	return 0, false
}
