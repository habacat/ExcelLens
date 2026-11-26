package calculator

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"excellens/internal/excelformula"
)

type formulaEval struct {
	names       map[string]FormulaValue
	ctx         context.Context
	resolve     FormulaResolver
	cache       map[string]RangeValue
	steps       int
	textBytes   int
	approximate bool
	worksheet   bool
	now         time.Time
	date1904    bool
}

func (e *formulaEval) get(ref formulaReference) (RangeValue, error) {
	key := ref.sheet + "\x00" + ref.start + ":" + ref.end
	if cached, ok := e.cache[key]; ok {
		return cached, nil
	}
	if len(e.cache) >= MaxOperands {
		return RangeValue{}, errors.New("#NUM! 引用过多")
	}
	if e.resolve == nil {
		return RangeValue{}, errors.New("#REF! 请先打开工作表")
	}
	value, err := e.resolve(ref.sheet, ref.start, ref.end)
	if err != nil {
		return value, err
	}
	if value.Rows == 0 || value.Cols == 0 {
		c1, r1 := formulaCoordinates(ref.start)
		c2, r2 := formulaCoordinates(ref.end)
		if ref.end == "" {
			c2, r2 = c1, r1
		}
		if c1 > 0 && r1 > 0 && c2 > 0 && r2 > 0 {
			value.FirstRow = min(r1, r2)
			value.FirstCol = min(c1, c2)
			value.Rows = absInt(r2-r1) + 1
			value.Cols = absInt(c2-c1) + 1
		}
	}
	if value.Scalar != nil {
		e.textBytes += len(value.Scalar.Text)
		if e.textBytes > 32<<20 {
			return RangeValue{}, errors.New("#NUM! 引用文本过多")
		}
	}
	e.cache[key] = value
	return value, nil
}

func (e *formulaEval) eval(n *formulaNode) (FormulaValue, error) {
	if err := e.ctx.Err(); err != nil {
		return FormulaValue{}, err
	}
	e.steps++
	if e.steps > 1024 {
		return FormulaValue{}, errors.New("#NUM! 公式过于复杂")
	}
	switch n.kind {
	case "value":
		if e.worksheet && n.value.Kind == "number" {
			number, _ := n.value.Number.Float64()
			return worksheetNumber(number)
		}
		return n.value, valueFault(n.value)
	case "array":
		values := make([]FormulaValue, len(n.args))
		for i, arg := range n.args {
			v, err := e.eval(arg)
			if err != nil {
				if e.ctx.Err() != nil {
					return FormulaValue{}, e.ctx.Err()
				}
				v = errorValue(err)
			}
			values[i] = v
		}
		return arrayValue(values, n.rows, n.cols), nil
	case "name", "localName":
		if value, ok := e.names[n.op]; ok {
			return value, nil
		}
		return FormulaValue{}, fmt.Errorf("#NAME? %s", n.op)
	case "ref":
		if n.ref.start == "#REF!" {
			return FormulaValue{}, errors.New("#REF!")
		}
		rv, err := e.get(n.ref)
		if err != nil {
			return FormulaValue{}, err
		}
		if n.ref.end != "" {
			return FormulaValue{Kind: "range", Range: &rv}, nil
		}
		if rv.Fault != nil {
			return FormulaValue{}, rv.Fault
		}
		if rv.Scalar != nil {
			return *rv.Scalar, nil
		}
		if rv.Text {
			return TextValue(""), nil
		}
		if rv.Sum == nil {
			return BlankValue(), nil
		}
		return NumberValue(rv.Sum), nil
	case "function":
		if e.worksheet && n.op == "ANCHORARRAY" {
			r, err := e.get(n.args[0].ref)
			if err != nil {
				return FormulaValue{}, err
			}
			if r.Spill == nil {
				return FormulaValue{}, errors.New("#REF!")
			}
			spill, err := r.Spill()
			return FormulaValue{Kind: "range", Range: &spill}, err
		}
		v, err := e.function(n)
		if e.worksheet && err == nil && v.Kind == "error" {
			return v, valueFault(v)
		}
		if e.worksheet && err == nil && v.Kind == "number" {
			number, _ := v.Number.Float64()
			return worksheetNumber(number)
		}
		return v, err
	case "unary":
		v, err := e.eval(n.args[0])
		if err != nil {
			return v, err
		}
		if v.Kind == "range" {
			return e.arrayUnary(n.op, v)
		}
		number, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		if n.op == "-" {
			number.Neg(number)
		}
		if n.op == "%" {
			if e.worksheet {
				number, _ := number.Float64()
				return worksheetNumber(number / 100)
			}
			number.Quo(number, big.NewRat(100, 1))
		}
		if err := bounded(number); err != nil {
			return FormulaValue{}, err
		}
		return NumberValue(number), nil
	case "binary":
		a, err := e.eval(n.args[0])
		if err != nil {
			return a, err
		}
		b, err := e.eval(n.args[1])
		if err != nil {
			return b, err
		}
		return e.binaryValue(n.op, a, b)
	}
	return FormulaValue{}, errors.New("#VALUE! 公式无效")
}

func (e *formulaEval) binaryValue(op string, a, b FormulaValue) (FormulaValue, error) {
	if a.Kind == "range" || b.Kind == "range" {
		return e.arrayBinary(op, a, b)
	}
	if err := valueFault(a); err != nil {
		return FormulaValue{}, err
	}
	if err := valueFault(b); err != nil {
		return FormulaValue{}, err
	}
	if op == "&" {
		s, err := e.operandText(a)
		if err != nil {
			return FormulaValue{}, err
		}
		t, err := e.operandText(b)
		if err != nil {
			return FormulaValue{}, err
		}
		if len(s)+len(t) > maxFormulaText {
			return FormulaValue{}, errors.New("#NUM! 文本结果过长")
		}
		return TextValue(s + t), nil
	}
	if strings.Contains(" = <> < > <= >= ", " "+op+" ") {
		comparison, err := compareFormulaValues(a, b)
		if err != nil {
			return FormulaValue{}, err
		}
		return BoolValue(excelformula.MatchesComparison(comparison, op)), nil
	}
	x, err := formulaNumber(a)
	if err != nil {
		return FormulaValue{}, err
	}
	y, err := formulaNumber(b)
	if err != nil {
		return FormulaValue{}, err
	}
	if e.worksheet {
		return worksheetArithmetic(op, x, y)
	}
	if op == "^" {
		return formulaPower(e, x, y)
	}
	v, err := binaryArithmetic(op[0], x, y)
	if err != nil {
		return FormulaValue{}, err
	}
	return NumberValue(v), nil
}
