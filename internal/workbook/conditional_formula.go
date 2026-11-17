package workbook

import (
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type conditionalDecimal struct {
	digits   string
	exponent int64
	negative bool
}
type conditionalValue struct {
	kind         string
	text, folded string
	number       conditionalDecimal
	boolean      bool
}
type conditionalReference struct {
	row, col                 int
	absoluteRow, absoluteCol bool
}
type conditionalOperand struct {
	value     conditionalValue
	reference *conditionalReference
}
type conditionalPredicate struct {
	left, right conditionalOperand
	operator    string
	comparison  bool
}

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

func compileConditionalOperand(raw string) (conditionalOperand, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > formats.ConditionalFormulaBytes {
		return conditionalOperand{}, false
	}
	if s[0] == '"' {
		var value strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					value.WriteByte('"')
					i++
					continue
				}
				if i == len(s)-1 {
					return conditionalOperand{value: conditionalText(value.String())}, true
				}
				return conditionalOperand{}, false
			}
			value.WriteByte(s[i])
		}
		return conditionalOperand{}, false
	}
	switch strings.ToUpper(s) {
	case "TRUE", "TRUE()":
		return conditionalOperand{value: conditionalValue{kind: "boolean", boolean: true}}, true
	case "FALSE", "FALSE()":
		return conditionalOperand{value: conditionalValue{kind: "boolean"}}, true
	}
	percent := strings.HasSuffix(s, "%")
	numeric := s
	if percent {
		numeric = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	}
	if number, ok := parseConditionalDecimal(numeric); ok {
		if percent {
			number.exponent -= 2
		}
		return conditionalOperand{value: conditionalValue{kind: "number", number: number}}, true
	}
	i := 0
	absoluteCol := s[0] == '$'
	if absoluteCol {
		i++
	}
	letters := i
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i == letters || i-letters > 3 {
		return conditionalOperand{}, false
	}
	absoluteRow := i < len(s) && s[i] == '$'
	if absoluteRow {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i != len(s) || digits == i {
		return conditionalOperand{}, false
	}
	col, row, err := ParseCellRef(s)
	if err != nil || row > formats.ExcelRows {
		return conditionalOperand{}, false
	}
	return conditionalOperand{reference: &conditionalReference{row: row, col: col, absoluteCol: absoluteCol, absoluteRow: absoluteRow}}, true
}

func compileConditionalPredicate(raw string) (conditionalPredicate, bool) {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "="))
	quoted := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			if quoted && i+1 < len(s) && s[i+1] == '"' {
				i++
				continue
			}
			quoted = !quoted
			continue
		}
		if quoted || s[i] != '<' && s[i] != '>' && s[i] != '=' {
			continue
		}
		end := i + 1
		if end < len(s) && (s[end] == '=' || s[i] == '<' && s[end] == '>') {
			end++
		}
		op := s[i:end]
		switch op {
		case "=", "<>", "<", ">", "<=", ">=":
		default:
			return conditionalPredicate{}, false
		}
		left, ok := compileConditionalOperand(s[:i])
		if !ok {
			return conditionalPredicate{}, false
		}
		right, ok := compileConditionalOperand(s[end:])
		if !ok {
			return conditionalPredicate{}, false
		}
		return conditionalPredicate{left: left, right: right, operator: op, comparison: true}, true
	}
	value, ok := compileConditionalOperand(s)
	return conditionalPredicate{left: value}, ok
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
	switch operator {
	case "=", "equal":
		return cmp == 0
	case "<>", "notEqual":
		return cmp != 0
	case ">", "greaterThan":
		return cmp > 0
	case "<", "lessThan":
		return cmp < 0
	case ">=", "greaterThanOrEqual":
		return cmp >= 0
	case "<=", "lessThanOrEqual":
		return cmp <= 0
	}
	return false
}
