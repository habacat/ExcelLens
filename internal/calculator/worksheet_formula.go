package calculator

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// WorksheetReference is a closed rectangle in Excel's coordinate system.
// Empty Sheet denotes the sheet containing the formula.
type WorksheetReference struct {
	Sheet                    string
	Top, Left, Bottom, Right int
}

func WorksheetBounds(sheet, start, end string) WorksheetReference {
	c, r := formulaCoordinates(start)
	if end == "" {
		end = start
	}
	d, s := formulaCoordinates(end)
	area := WorksheetReference{sheet, min(r, s), min(c, d), max(r, s), max(c, d)}
	if r == 0 {
		area.Top, area.Bottom = 1, formats.ExcelRows
	}
	if c == 0 {
		area.Left, area.Right = 1, formats.ExcelColumns
	}
	return area
}

type WorksheetName struct {
	Reference *WorksheetReference
	Constant  FormulaValue
}

type WorksheetFormula struct {
	Subtotal   bool
	root       *formulaNode
	References []WorksheetReference
}

func (f WorksheetFormula) Bound() bool { return f.root != nil }

var ErrWorksheetPending = errors.New("公式需在 Excel 中重算")

// XLSX arithmetic rounds each operation in binary64. Keep the shortest
// round-tripping decimal for the existing scalar functions (including ROUND).
// The standalone audit calculator retains its exact rational arithmetic.
func worksheetNumber(n float64) (FormulaValue, error) {
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return FormulaValue{}, errors.New("#NUM!")
	}
	if n == 0 || math.Abs(n) < 0x1p-1022 {
		return NumberValue(new(big.Rat)), nil
	}
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(n, 'g', -1, 64))
	return NumberValue(r), nil
}

func worksheetArithmetic(op string, x, y *big.Rat) (FormulaValue, error) {
	a, _ := x.Float64()
	b, _ := y.Float64()
	switch op {
	case "+":
		return worksheetNumber(a + b)
	case "-":
		return worksheetNumber(a - b)
	case "*":
		return worksheetNumber(a * b)
	case "/":
		if b == 0 {
			return FormulaValue{}, errors.New("#DIV/0!")
		}
		return worksheetNumber(a / b)
	case "^":
		if a == 0 && b == 0 {
			return FormulaValue{}, errors.New("#NUM!")
		}
		if a == 0 && b < 0 {
			return FormulaValue{}, errors.New("#DIV/0!")
		}
		return worksheetNumber(math.Pow(a, b))
	}
	return FormulaValue{}, ErrWorksheetPending
}

func (e *formulaEval) operandText(v FormulaValue) (string, error) {
	if e.worksheet && v.Kind == "number" {
		n, _ := v.Number.Float64()
		return strings.ToUpper(strconv.FormatFloat(n, 'g', 15, 64)), nil
	}
	return formulaText(v)
}

// BindWorksheet resolves names and legacy implicit intersection before
// collecting dependencies. The evaluator and the dependency index consequently
// refer to the same cells, including positional criteria such as B:B in SUMIFS.
func (f EditableFormula) BindWorksheet(row, col int, array bool, name func(string) WorksheetName) WorksheetFormula {
	var result WorksheetFormula
	var bind func(*formulaNode, bool) *formulaNode
	bind = func(n *formulaNode, scalar bool) *formulaNode {
		if n == nil {
			return nil
		}
		switch n.kind {
		case "name", "namedRange":
			binding := name(n.op)
			if binding.Reference == nil {
				if binding.Constant.Kind == "" {
					return nil
				}
				return &formulaNode{kind: "value", value: binding.Constant}
			}
			a := binding.Reference
			start, end := formulaAddress(a.Left, a.Top), formulaAddress(a.Right, a.Bottom)
			if start == end {
				end = ""
			}
			return bind(&formulaNode{kind: "ref", ref: formulaReference{sheet: a.Sheet, start: start, end: end}}, scalar)
		case "ref":
			a := WorksheetBounds(n.ref.sheet, n.ref.start, n.ref.end)
			if scalar && n.ref.end != "" {
				switch {
				case a.Top == a.Bottom && a.Left == a.Right:
				case a.Left == a.Right && a.Top <= row && row <= a.Bottom:
					a.Top, a.Bottom = row, row
				case a.Top == a.Bottom && a.Left <= col && col <= a.Right:
					a.Left, a.Right = col, col
				default:
					return &formulaNode{kind: "value", value: errorValue(errors.New("#VALUE!"))}
				}
				n = &formulaNode{kind: "ref", ref: formulaReference{sheet: a.Sheet, start: formulaAddress(a.Left, a.Top)}}
			}
			result.References = append(result.References, a)
			return n
		case "function":
			if n.op == "SUBTOTAL" {
				result.Subtotal = true
			}
			if n.op == "_XLFN.SINGLE" {
				return bind(n.args[0], true)
			}
			if (n.op == "ROW" || n.op == "COLUMN") && len(n.args) == 0 {
				pos := row
				if n.op == "COLUMN" {
					pos = col
				}
				return &formulaNode{kind: "value", value: NumberValue(big.NewRat(int64(pos), 1))}
			}
			if len(n.args) == 1 && n.args[0].kind == "ref" {
				ref := n.args[0].ref
				bounds := WorksheetBounds(ref.sheet, ref.start, ref.end)
				var size int
				switch n.op {
				case "ROWS":
					size = bounds.Bottom - bounds.Top + 1
				case "COLUMNS":
					size = bounds.Right - bounds.Left + 1
				case "ROW":
					if scalar || bounds.Top == bounds.Bottom {
						size = bounds.Top
					}
				case "COLUMN":
					if scalar || bounds.Left == bounds.Right {
						size = bounds.Left
					}
				}
				if size > 0 {
					return &formulaNode{kind: "value", value: NumberValue(big.NewRat(int64(size), 1))}
				}
			}
		}
		if len(n.args) == 0 {
			return n
		}
		bound := *n
		bound.args = make([]*formulaNode, len(n.args))
		for i, arg := range n.args {
			wantScalar := scalar
			if n.kind == "function" {
				wantScalar = !array && !worksheetRangeArgument(n.op, i)
			}
			bound.args[i] = bind(arg, wantScalar)
			if bound.args[i] == nil {
				return nil
			}
		}
		return &bound
	}
	result.root = bind(f.root, !array)
	return result
}

func worksheetRangeArgument(op string, i int) bool {
	switch op {
	case "SUM", "AVERAGE", "MIN", "MAX", "COUNT", "COUNTA", "PRODUCT", "SUMPRODUCT", "AND", "OR":
		return true
	case "SUBTOTAL":
		return i > 0
	case "ROW", "COLUMN", "ROWS", "COLUMNS", "INDEX":
		return i == 0
	case "MATCH", "VLOOKUP", "HLOOKUP":
		return i == 1
	case "SUMIF", "COUNTIF", "AVERAGEIF":
		return i != 1
	case "SUMIFS", "AVERAGEIFS":
		return i == 0 || i%2 == 1
	case "COUNTIFS":
		return i%2 == 0
	}
	return false
}

func (f WorksheetFormula) Evaluate(ctx context.Context, resolve FormulaResolver) (FormulaValue, error) {
	if f.root == nil {
		return FormulaValue{}, ErrWorksheetPending
	}
	e := formulaEval{ctx: ctx, resolve: resolve, cache: map[string]RangeValue{}, date1904: date1904FromContext(ctx), worksheet: true}
	v, err := e.eval(f.root)
	if err == nil && v.Kind == "range" && v.Range.Rows == 1 && v.Range.Cols == 1 {
		v, err = valueAt(&e, v, 0)
	}
	if err != nil {
		if ctx.Err() != nil {
			return FormulaValue{}, ctx.Err()
		}
		// Resource limits and unavailable inputs must not become a fabricated
		// Excel error. Excel error codes travel as ordinary typed cell results.
		if errors.Is(err, ErrWorksheetPending) || strings.Contains(err.Error(), "超过") || strings.Contains(err.Error(), "过于复杂") || strings.Contains(err.Error(), "过多") {
			return FormulaValue{}, ErrWorksheetPending
		}
		for _, code := range []string{"#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#NULL!"} {
			if strings.HasPrefix(err.Error(), code) {
				return FormulaValue{Kind: "error", Text: code}, nil
			}
		}
		return FormulaValue{}, ErrWorksheetPending
	}
	if v.Kind == "range" {
		return FormulaValue{}, ErrWorksheetPending
	}
	if v.Kind == "blank" {
		v = NumberValue(new(big.Rat))
	}
	return v, nil
}
