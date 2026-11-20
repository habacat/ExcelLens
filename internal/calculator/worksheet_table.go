package calculator

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Excel brackets can be nested and a quote escapes the next special character
// in a column caption. A bracket after a quoted string is never consumed here.
func structuredToken(s string) int {
	i := 0
	for i < len(s) && s[i] != '[' {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsLetter(r) && r != '_' && r != '\\' && (i == 0 || !unicode.IsDigit(r) && r != '.') {
			return 0
		}
		i += n
	}
	if i == len(s) {
		return 0
	}
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if i+1 < len(s) && strings.ContainsRune("[]#@'", rune(s[i+1])) {
				i++
			}
		case '[':
			depth++
			if depth > 4 {
				return 0
			}
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

// StoredTableFormula expands grid notation (e.g. [@Cost]) for the OOXML
// formula. The original spans keep quoted text and ordinary A1 refs intact.
func (f EditableFormula) StoredTableFormula(canonical func(string) string) string {
	if len(f.tableTokens) == 0 {
		return f.expression
	}
	var out strings.Builder
	pos := 0
	for _, span := range f.tableTokens {
		out.WriteString(f.expression[pos:span[0]])
		out.WriteString(canonical(f.expression[span[0]:span[1]]))
		pos = span[1]
	}
	out.WriteString(f.expression[pos:])
	return out.String()
}

func (e *formulaEval) worksheetSubtotal(n *formulaNode) (FormulaValue, error) {
	code, err := e.eval(n.args[0])
	if err != nil {
		return FormulaValue{}, err
	}
	v, err := formulaNumber(code)
	if err != nil {
		return FormulaValue{}, err
	}
	number, _ := v.Float64()
	if number < 1 || number >= 112 {
		return FormulaValue{}, errors.New("#VALUE!")
	}
	fn := int(number) // Excel truncates a fractional function number.
	hidden := fn > 100
	if hidden {
		fn -= 100
	}
	if fn < 1 || fn > 11 {
		return FormulaValue{}, errors.New("#VALUE!")
	}
	var count, nonempty int
	var sum, least, most, mean, m2 float64
	product := 1.0
	for _, arg := range n.args[1:] {
		if arg.kind != "ref" {
			if arg.kind == "value" && arg.value.Kind == "error" {
				return arg.value, nil
			}
			return FormulaValue{}, errors.New("#VALUE!")
		}
		r, err := e.get(arg.ref)
		if err != nil {
			return FormulaValue{}, err
		}
		if r.VisitSubtotal == nil {
			return FormulaValue{}, ErrWorksheetPending
		}
		err = r.VisitSubtotal(hidden, func(_ int, value FormulaValue) error {
			if value.Kind != "blank" {
				nonempty++
			}
			if value.Kind == "error" && fn != 2 && fn != 3 {
				return errors.New(value.Text)
			}
			if value.Kind != "number" {
				return nil
			}
			x, _ := value.Number.Float64()
			count++
			if count == 1 || x < least {
				least = x
			}
			if count == 1 || x > most {
				most = x
			}
			sum += x
			product *= x
			delta := x - mean
			mean += delta / float64(count)
			m2 += delta * (x - mean)
			return nil
		})
		if err != nil {
			return FormulaValue{}, err
		}
	}
	var result float64
	switch fn {
	case 1:
		if count == 0 {
			return FormulaValue{}, errors.New("#DIV/0!")
		}
		result = sum / float64(count)
	case 2:
		result = float64(count)
	case 3:
		result = float64(nonempty)
	case 4:
		result = most
	case 5:
		result = least
	case 6:
		if count > 0 {
			result = product
		}
	case 9:
		result = sum
	default:
		divisor := count
		if fn == 7 || fn == 10 {
			divisor--
		}
		if divisor <= 0 {
			return FormulaValue{}, errors.New("#DIV/0!")
		}
		result = m2 / float64(divisor)
		if fn == 7 || fn == 8 {
			result = math.Sqrt(result)
		}
	}
	return worksheetNumber(result)
}
