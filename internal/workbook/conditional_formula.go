package workbook

import (
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/conditional"
	"excellens/internal/excelformula"
)

type conditionalDecimal struct {
	digits   string
	exponent int64
	negative bool
}

type conditionalValue struct {
	calculatedFloat bool
	kind            string
	text, folded    string
	number          conditionalDecimal
	rat             *big.Rat
	boolean         bool
}

type conditionalReference = conditional.Reference
type conditionalExpr = conditional.Expression[conditionalValue]

const (
	cfExprLiteral = conditional.Literal
	cfExprRef     = conditional.ReferenceCell
	cfExprRange   = conditional.Range
	cfExprUnary   = conditional.Unary
	cfExprPercent = conditional.Percent
	cfExprBinary  = conditional.Binary
	cfExprCall    = conditional.Call
)

func parseConditionalDecimal(raw string) (conditionalDecimal, bool) {
	key, ok := inspectNumericKey(raw)
	if !ok {
		return conditionalDecimal{}, false
	}
	if key == "0" {
		return conditionalDecimal{digits: "0"}, true
	}
	out := conditionalDecimal{}
	if key[0] == '-' {
		out.negative = true
		key = key[1:]
	}
	at := strings.LastIndexByte(key, 'e')
	if at < 0 {
		return conditionalDecimal{}, false
	}
	out.digits = key[:at]
	out.exponent, _ = strconv.ParseInt(key[at+1:], 10, 64)
	return out, true
}

// Decimal digits and a bounded exponent compare without float rounding or
// constructing enormous powers of ten for a hostile scientific literal.
func compareConditionalDecimal(a, b conditionalDecimal) int {
	az, bz := a.digits == "0", b.digits == "0"
	if az && bz {
		return 0
	}
	if a.negative != b.negative {
		if a.negative {
			return -1
		}
		return 1
	}
	cmp := 0
	if az {
		cmp = -1
	} else if bz {
		cmp = 1
	} else {
		am, bm := int64(len(a.digits))+a.exponent, int64(len(b.digits))+b.exponent
		if am < bm {
			cmp = -1
		} else if am > bm {
			cmp = 1
		} else {
			for i := 0; i < max(len(a.digits), len(b.digits)); i++ {
				x, y := byte('0'), byte('0')
				if i < len(a.digits) {
					x = a.digits[i]
				}
				if i < len(b.digits) {
					y = b.digits[i]
				}
				if x < y {
					cmp = -1
					break
				}
				if x > y {
					cmp = 1
					break
				}
			}
		}
	}
	if a.negative {
		return -cmp
	}
	return cmp
}

func conditionalText(value string) conditionalValue {
	return conditionalValue{kind: "text", text: value, folded: filterFoldKey(value)}
}

func conditionalNumberVal(d conditionalDecimal) conditionalValue {
	return conditionalValue{kind: "number", number: d}
}

func conditionalIntVal(n int64) conditionalValue {
	d, _ := parseConditionalDecimal(strconv.FormatInt(n, 10))
	return conditionalValue{kind: "number", number: d}
}

func conditionalBoolVal(b bool) conditionalValue {
	return conditionalValue{kind: "boolean", boolean: b}
}

func conditionalErrorVal(code string) conditionalValue {
	return conditionalValue{kind: "error", text: code}
}

func conditionalBlankVal() conditionalValue {
	return conditionalValue{kind: "blank"}
}

func (w *Workbook) compileConditionalExpr(raw, sheet string) (*conditionalExpr, int, string) {
	return conditional.Compile(raw, sheet, conditional.Bindings[conditionalValue]{
		Text: conditionalText, Bool: conditionalBoolVal, Error: conditionalErrorVal, Blank: conditionalBlankVal(),
		Number: func(raw string) (conditionalValue, bool) {
			d, ok := parseConditionalDecimal(raw)
			return conditionalNumberVal(d), ok
		},
		Fold: func(v *conditionalValue, op string) bool {
			if v.kind != "number" {
				return false
			}
			switch op {
			case "%":
				if v.number.digits != "0" {
					v.number.exponent -= 2
				}
			case "-":
				if v.number.digits != "0" {
					v.number.negative = !v.number.negative
				}
			}
			return true
		},
		Name: func(name string) (conditional.Reference, string) { return w.conditionalDefinedName(sheet, name) },
	})
}
func compareConditionalValues(a, b conditionalValue) (int, bool) {
	if a.kind == "error" || b.kind == "error" {
		return 0, false
	}
	if a.kind == "blank" {
		a = conditionalBlankAs(b.kind)
	}
	if b.kind == "blank" {
		b = conditionalBlankAs(a.kind)
	}
	rank := func(kind string) int {
		switch kind {
		case "number":
			return 0
		case "text":
			return 1
		case "boolean":
			return 2
		}
		return -1
	}
	if a.kind != b.kind {
		return rank(a.kind) - rank(b.kind), true
	}
	switch a.kind {
	case "number":
		if a.calculatedFloat || b.calculatedFloat {
			ar, ok1 := valueToRat(a)
			br, ok2 := valueToRat(b)
			if ok1 && ok2 {
				af, _ := ar.Float64()
				bf, _ := br.Float64()
				da, oka := parseConditionalDecimal(strconv.FormatFloat(af, 'g', 15, 64))
				db, okb := parseConditionalDecimal(strconv.FormatFloat(bf, 'g', 15, 64))
				if oka && okb {
					return compareConditionalDecimal(da, db), true
				}
			}
		}
		if a.rat != nil || b.rat != nil {
			return compareConditionalNumbersExact(a, b), true
		}
		return compareConditionalDecimal(a.number, b.number), true
	case "text":
		return strings.Compare(a.folded, b.folded), true
	case "boolean":
		if a.boolean == b.boolean {
			return 0, true
		}
		if a.boolean {
			return 1, true
		}
		return -1, true
	}
	return 0, false
}

func conditionalBlankAs(kind string) conditionalValue {
	switch kind {
	case "text":
		return conditionalText("")
	case "boolean":
		return conditionalValue{kind: "boolean"}
	}
	return conditionalValue{kind: "number", number: conditionalDecimal{digits: "0"}}
}

func matchConditionalComparison(cmp int, operator string) bool {
	return excelformula.MatchesComparison(cmp, operator)
}
