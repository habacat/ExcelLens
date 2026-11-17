package calculator

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

const maxFunctionCells = 4000000
const maxArrayCells = 200000
const maxArrayText = 32 << 20

func trackArrayText(total *int, value FormulaValue) error {
	*total += len(value.Text)
	if *total > maxArrayText {
		return errors.New("#NUM! 数组文本超过32MiB")
	}
	return nil
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func formulaCoordinates(s string) (int, int) {
	s = strings.ReplaceAll(s, "$", "")
	i, c := 0, 0
	for i < len(s) && letter(s[i]) {
		c = c*26 + int(strings.ToUpper(s[i : i+1])[0]-'A'+1)
		i++
	}
	r, _ := strconv.Atoi(s[i:])
	return c, r
}
func formulaAddress(c, r int) string {
	out := ""
	for c > 0 {
		c--
		out = string(rune('A'+c%26)) + out
		c /= 26
	}
	return out + strconv.Itoa(r)
}
func errorValue(err error) FormulaValue { return FormulaValue{Kind: "error", Text: err.Error()} }
func formulaErrorCode(message string) string {
	for _, code := range []string{"#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#NULL!", "#CALC!", "#SPILL!"} {
		if strings.HasPrefix(strings.ToUpper(message), code) {
			return code
		}
	}
	return "#VALUE!"
}
func valueFault(v FormulaValue) error {
	if v.Kind == "error" {
		return errors.New(v.Text)
	}
	return nil
}
func rangeArg(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	if n.kind == "ref" {
		r, err := e.get(n.ref)
		return FormulaValue{Kind: "range", Range: &r}, err
	}
	return e.eval(n)
}
func rangeShape(v FormulaValue) (int, int, error) {
	if v.Kind != "range" {
		return 1, 1, nil
	}
	r := v.Range
	if r == nil {
		return 0, 0, errors.New("#VALUE! 无效范围")
	}
	if r.Rows > 0 && r.Cols > 0 {
		if int64(r.Rows)*int64(r.Cols) > maxFunctionCells {
			return 0, 0, errors.New("#NUM! 范围超过400万格")
		}
		return r.Rows, r.Cols, nil
	}
	if r.Scalar != nil {
		return 1, 1, nil
	}
	return 0, 0, errors.New("#VALUE! 范围缺少行列信息")
}
func visitSparse(e *formulaEval, v FormulaValue, fn func(int, FormulaValue) error) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if v.Kind != "range" {
		return fn(0, v)
	}
	r := v.Range
	wrap := func(i int, v FormulaValue) error {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		if r.Rows > 0 && r.Cols > 0 && (i < 0 || i >= r.Rows*r.Cols) {
			return errors.New("#REF! 范围坐标越界")
		}
		return fn(i, v)
	}
	if r.Visit != nil {
		return r.Visit(wrap)
	}
	if r.Scalar != nil {
		return wrap(0, *r.Scalar)
	}
	if r.Load != nil {
		values, err := r.Load()
		if err != nil {
			return err
		}
		for i, v := range values {
			if err = wrap(i, v); err != nil {
				return err
			}
		}
		return nil
	}
	if r.Fault != nil {
		return r.Fault
	}
	return errors.New("#VALUE! 范围不支持逐格读取")
}
func valueAt(e *formulaEval, v FormulaValue, index int) (FormulaValue, error) {
	rows, cols, err := rangeShape(v)
	if err != nil {
		return FormulaValue{}, err
	}
	if index < 0 || index >= rows*cols {
		return FormulaValue{}, errors.New("#REF! 范围下标越界")
	}
	if v.Kind != "range" {
		return v, valueFault(v)
	}
	r := v.Range
	if r.At != nil {
		return r.At(index)
	}
	out := BlankValue()
	err = visitSparse(e, v, func(i int, item FormulaValue) error {
		if i == index {
			out = item
		}
		return nil
	})
	if err == nil {
		err = valueFault(out)
	}
	return out, err
}
func materialize(e *formulaEval, v FormulaValue, limit int) ([]FormulaValue, int, int, error) {
	rows, cols, err := rangeShape(v)
	if err != nil {
		return nil, 0, 0, err
	}
	if rows*cols > limit {
		return nil, 0, 0, errors.New("#NUM! 数组超过允许格数")
	}
	out := make([]FormulaValue, rows*cols)
	for i := range out {
		out[i] = BlankValue()
	}
	textBytes := 0
	err = visitSparse(e, v, func(i int, x FormulaValue) error {
		if i < 0 || i >= len(out) {
			return errors.New("#REF! 范围坐标无效")
		}
		if err := trackArrayText(&textBytes, x); err != nil {
			return err
		}
		out[i] = x
		return nil
	})
	return out, rows, cols, err
}
func visitDense(e *formulaEval, v FormulaValue, fn func(int, FormulaValue) error) error {
	rows, cols, err := rangeShape(v)
	if err != nil {
		return err
	}
	next := 0
	fill := func(end int) error {
		for next < end {
			if next%1024 == 0 {
				if err := e.ctx.Err(); err != nil {
					return err
				}
			}
			if err := fn(next, BlankValue()); err != nil {
				return err
			}
			next++
		}
		return nil
	}
	err = visitSparse(e, v, func(i int, x FormulaValue) error {
		if i < next {
			return errors.New("#REF! 范围坐标未按源顺序排列")
		}
		if err := fill(i); err != nil {
			return err
		}
		next = i + 1
		return fn(i, x)
	})
	if err != nil {
		return err
	}
	return fill(rows * cols)
}
func dataNumber(v FormulaValue) (*big.Rat, bool) {
	if v.Kind == "number" && v.Number != nil {
		return v.Number, true
	}
	if v.NumericText != nil {
		return v.NumericText, true
	}
	return nil, false
}
func evaluatedArgs(e *formulaEval, n *formulaNode) ([]FormulaValue, error) {
	out := make([]FormulaValue, len(n.args))
	for i, arg := range n.args {
		v, err := e.eval(arg)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
func numberResult(n *big.Rat) (FormulaValue, error) {
	if err := bounded(n); err != nil {
		return FormulaValue{}, err
	}
	return NumberValue(n), nil
}
func integerResult(n int64) (FormulaValue, error) { return NumberValue(big.NewRat(n, 1)), nil }
func subsetRange(e *formulaEval, v FormulaValue, row, col, rows, cols int) (FormulaValue, error) {
	nr, nc, err := rangeShape(v)
	if err != nil {
		return FormulaValue{}, err
	}
	if row < 0 || col < 0 || rows < 1 || cols < 1 || row+rows > nr || col+cols > nc {
		return FormulaValue{}, errors.New("#REF! 子范围越界")
	}
	r := RangeValue{Rows: rows, Cols: cols}
	r.Ordered = v.Kind != "range" || v.Range.Ordered
	if v.Kind == "range" {
		r.FirstRow = v.Range.FirstRow + row
		r.FirstCol = v.Range.FirstCol + col
	}
	r.Visit = func(fn func(int, FormulaValue) error) error {
		return visitSparse(e, v, func(i int, x FormulaValue) error {
			rr, cc := i/nc, i%nc
			if rr >= row && rr < row+rows && cc >= col && cc < col+cols {
				return fn((rr-row)*cols+cc-col, x)
			}
			return nil
		})
	}
	r.At = func(i int) (FormulaValue, error) { return valueAt(e, v, (row+i/cols)*nc+col+i%cols) }
	r.Load = func() ([]FormulaValue, error) {
		a, _, _, err := materialize(e, FormulaValue{Kind: "range", Range: &r}, 8192)
		return a, err
	}
	return FormulaValue{Kind: "range", Range: &r}, nil
}
