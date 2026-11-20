package calculator

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

func registerConditionalFunctions(add functionRegistrar) {
	add("SUMIF,AVERAGEIF", "条件区域, 条件, [数值区域]", 2, 3, conditionalFunction)
	add("COUNTIF", "条件区域, 条件", 2, 2, conditionalFunction)
	add("SUMIFS,AVERAGEIFS,MINIFS,MAXIFS", "数值区域, 条件区域1, 条件1, …", 3, 127, conditionalFunction)
	add("COUNTIFS", "条件区域1, 条件1, …", 2, 128, conditionalFunction)
	add("COUNTBLANK", "区域", 1, 1, conditionalFunction)
}
func wildcardPattern(s string, whole bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if whole {
		b.WriteByte('^')
	}
	escaped := false
	for _, r := range s {
		if escaped {
			b.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
			continue
		}
		switch r {
		case '~':
			escaped = true
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if escaped {
		b.WriteByte('~')
	}
	if whole {
		b.WriteByte('$')
	}
	return regexp.Compile("(?is)" + b.String())
}

// FilterCriterion compiles a customFilter operator and its operand once for
// row visibility changes. Excel wildcards use the same escaping as COUNTIF.
func FilterCriterion(operator, operand string) (func(FormulaValue) bool, error) {
	prefix, ok := map[string]string{"": "=", "equal": "=", "notEqual": "<>", "lessThan": "<", "lessThanOrEqual": "<=", "greaterThan": ">", "greaterThanOrEqual": ">="}[operator]
	if !ok {
		return nil, errors.New("无法识别筛选运算符")
	}
	return compileCriterion(TextValue(prefix + operand))
}

type criterion func(FormulaValue) bool

func compileCriterion(v FormulaValue) (criterion, error) {
	if err := valueFault(v); err != nil {
		return nil, err
	}
	op := "="
	rhs := v
	if v.Kind == "text" {
		s := v.Text
		for _, prefix := range []string{"<=", ">=", "<>", "<", ">", "="} {
			if strings.HasPrefix(s, prefix) {
				op = prefix
				s = s[len(prefix):]
				break
			}
		}
		rhs = TextValue(s)
		if n, err := formulaNumber(rhs); err == nil && s != "" {
			rhs = NumberValue(n)
		}
		if rhs.Kind == "text" && strings.ContainsAny(s, "*?~") && (op == "=" || op == "<>") {
			re, err := wildcardPattern(s, true)
			if err != nil {
				return nil, err
			}
			return func(x FormulaValue) bool {
				matched := x.Kind == "text" && re.MatchString(x.Text)
				if op == "<>" {
					return !matched
				}
				return matched
			}, nil
		}
	}
	return func(x FormulaValue) bool {
		if x.Kind == "error" {
			// Quoted error criteria match the error code, including errors
			// whose diagnostic includes a source address or explanation.
			matched := rhs.Kind == "text" && strings.EqualFold(formulaErrorCode(x.Text), rhs.Text)
			return op == "=" && matched || op == "<>" && !matched
		}
		if rhs.Kind == "number" {
			if x.Kind == "text" {
				if number, err := formulaNumber(x); err == nil {
					x = NumberValue(number)
				} else {
					return op == "<>"
				}
			}
			if x.Kind != "number" && x.Kind != "blank" {
				return op == "<>"
			}
		} else if rhs.Kind == "text" && x.Kind == "blank" {
			x = TextValue("")
		}
		if op != "=" && op != "<>" && x.Kind != rhs.Kind && x.Kind != "blank" {
			return false
		}
		cmp, err := compareFormulaValues(x, rhs)
		if err != nil {
			return false
		}
		switch op {
		case "=":
			return cmp == 0
		case "<>":
			return cmp != 0
		case "<":
			return cmp < 0
		case ">":
			return cmp > 0
		case "<=":
			return cmp <= 0
		case ">=":
			return cmp >= 0
		}
		return false
	}, nil
}
func conditionalFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	if n.op == "COUNTBLANK" {
		v, err := rangeArg(e, n.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		rows, cols, err := rangeShape(v)
		if err != nil {
			return FormulaValue{}, err
		}
		count := int64(rows * cols)
		err = visitSparse(e, v, func(_ int, x FormulaValue) error {
			if x.Kind != "blank" && !(x.Kind == "text" && x.Text == "") {
				count--
			}
			return nil
		})
		if err != nil {
			return FormulaValue{}, err
		}
		return integerResult(count)
	}
	countOnly := n.op == "COUNTIF" || n.op == "COUNTIFS"
	single := n.op == "SUMIF" || n.op == "COUNTIF" || n.op == "AVERAGEIF"
	var target FormulaValue
	var ranges []FormulaValue
	var tests []criterion
	addCondition := func(rn, cn *formulaNode) error {
		v, err := rangeArg(e, rn)
		if err != nil {
			return err
		}
		c, err := e.eval(cn)
		if err != nil {
			return err
		}
		test, err := compileCriterion(c)
		if err != nil {
			return err
		}
		ranges = append(ranges, v)
		tests = append(tests, test)
		return nil
	}
	if single {
		if err := addCondition(n.args[0], n.args[1]); err != nil {
			return FormulaValue{}, err
		}
		target = ranges[0]
		if len(n.args) == 3 {
			var err error
			target, err = rangeArg(e, n.args[2])
			if err != nil {
				return FormulaValue{}, err
			}
		}
	} else {
		start := 0
		if !countOnly {
			var err error
			target, err = rangeArg(e, n.args[0])
			if err != nil {
				return FormulaValue{}, err
			}
			start = 1
		}
		if (len(n.args)-start)%2 != 0 {
			return FormulaValue{}, errors.New("#VALUE! 条件区域与条件必须成对")
		}
		for i := start; i < len(n.args); i += 2 {
			if err := addCondition(n.args[i], n.args[i+1]); err != nil {
				return FormulaValue{}, err
			}
		}
	}
	rows, cols, err := rangeShape(ranges[0])
	if err != nil {
		return FormulaValue{}, err
	}
	size := rows * cols
	// Unallocated cells have the same answer for a given criterion. Keep
	// only deviations from that answer, even for a full Excel column.
	blankFailures := 0
	delta := make(map[int]int)
	for i, v := range ranges {
		nr, nc, err := rangeShape(v)
		if err != nil {
			return FormulaValue{}, err
		}
		if nr != rows || nc != cols {
			return FormulaValue{}, errors.New("#VALUE! 条件区域尺寸不一致")
		}
		blankMatches := tests[i](BlankValue())
		if !blankMatches {
			blankFailures++
		}
		err = visitSparse(e, v, func(j int, x FormulaValue) error {
			if j < 0 || j >= size {
				return errors.New("#REF! 条件坐标越界")
			}
			if matches := tests[i](x); matches != blankMatches {
				if matches {
					delta[j]--
				} else {
					delta[j]++
				}
			}
			return nil
		})
		if err != nil {
			return FormulaValue{}, err
		}
	}
	if countOnly {
		count := int64(0)
		if blankFailures == 0 {
			count = int64(size)
		}
		for _, d := range delta {
			if blankFailures == 0 && d != 0 {
				count--
			}
			if blankFailures != 0 && blankFailures+d == 0 {
				count++
			}
		}
		return integerResult(count)
	}
	nr, nc, err := rangeShape(target)
	if err != nil {
		return FormulaValue{}, err
	}
	if nr != rows || nc != cols {
		return FormulaValue{}, errors.New("#VALUE! 数值区域与条件区域尺寸不一致")
	}
	sum := new(big.Rat)
	var worksheetSum float64
	var extremum *big.Rat
	count := int64(0)
	err = visitSparse(e, target, func(i int, x FormulaValue) error {
		if blankFailures+delta[i] != 0 {
			return nil
		}
		if err := valueFault(x); err != nil {
			return err
		}
		if number, ok := dataNumber(x); ok {
			if e.worksheet {
				f, _ := number.Float64()
				worksheetSum += f
			} else {
				sum.Add(sum, number)
			}
			count++
			if extremum == nil || n.op == "MINIFS" && number.Cmp(extremum) < 0 || n.op == "MAXIFS" && number.Cmp(extremum) > 0 {
				extremum = new(big.Rat).Set(number)
			}
		}
		return nil
	})
	if err != nil {
		return FormulaValue{}, err
	}
	if n.op == "MINIFS" || n.op == "MAXIFS" {
		if extremum == nil {
			extremum = new(big.Rat)
		}
		return numberResult(extremum)
	}
	if n.op == "AVERAGEIF" || n.op == "AVERAGEIFS" {
		if count == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 没有符合条件的数值")
		}
		sum.Quo(sum, big.NewRat(count, 1))
		worksheetSum /= float64(count)
	}
	if e.worksheet {
		return worksheetNumber(worksheetSum)
	}
	return numberResult(sum)
}
