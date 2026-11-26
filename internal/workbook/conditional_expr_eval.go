package workbook

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type conditionalRangeCell struct {
	row, col int
	cell     CellData
}

type conditionalRangeData struct {
	r1, c1, r2, c2 int
	cells          []conditionalRangeCell
	blankCount     int64
}

type ConditionalRangeResolver func(r1, c1, r2, c2 int) (conditionalRangeData, string, error)

func decimalToRat(d conditionalDecimal) (*big.Rat, bool) {
	if d.digits == "" || d.digits == "0" {
		return new(big.Rat), true
	}
	if len(d.digits) > 128 || d.exponent > formats.ConditionalArithExponent || d.exponent < -formats.ConditionalArithExponent {
		return nil, false
	}
	num, ok := new(big.Int).SetString(d.digits, 10)
	if !ok {
		return nil, false
	}
	if d.negative {
		num.Neg(num)
	}
	den := big.NewInt(1)
	if d.exponent > 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(d.exponent), nil)
		num.Mul(num, scale)
	} else if d.exponent < 0 {
		den.Exp(big.NewInt(10), big.NewInt(-d.exponent), nil)
	}
	return new(big.Rat).SetFrac(num, den), true
}

func valueToRat(v conditionalValue) (*big.Rat, bool) {
	if v.rat != nil {
		return new(big.Rat).Set(v.rat), true
	}
	return decimalToRat(v.number)
}

func ratToConditionalValue(r *big.Rat) conditionalValue {
	if r == nil || r.Sign() == 0 {
		return conditionalValue{kind: "number", number: conditionalDecimal{digits: "0"}}
	}
	// Guard bit length and Excel numeric range (~1e308).
	if f, _ := r.Float64(); math.IsInf(f, 0) || math.IsNaN(f) {
		return conditionalErrorVal("#NUM!")
	}
	if r.Num().BitLen() > formats.ConditionalArithBits || r.Denom().BitLen() > formats.ConditionalArithBits {
		f, _ := r.Float64()
		d, ok := parseConditionalDecimal(strconv.FormatFloat(f, 'g', 15, 64))
		if !ok {
			return conditionalErrorVal("#NUM!")
		}
		return conditionalValue{kind: "number", number: d}
	}
	if s, ok := exactRatDecimalString(r, 30); ok {
		if d, dok := parseConditionalDecimal(s); dok {
			return conditionalValue{kind: "number", number: d}
		}
	}
	f, _ := r.Float64()
	d, ok := parseConditionalDecimal(strconv.FormatFloat(f, 'g', 15, 64))
	if !ok {
		return conditionalErrorVal("#NUM!")
	}
	return conditionalValue{kind: "number", number: d, rat: new(big.Rat).Set(r)}
}

func exactRatDecimalString(r *big.Rat, maxFrac int) (string, bool) {
	if r.IsInt() {
		return r.Num().String(), true
	}
	den := new(big.Int).Set(r.Denom())
	twos, fives := 0, 0
	rem := new(big.Int)
	five := big.NewInt(5)
	for den.Bit(0) == 0 {
		den.Rsh(den, 1)
		twos++
	}
	for {
		q, _ := new(big.Int).QuoRem(den, five, rem)
		if rem.Sign() != 0 {
			break
		}
		den = q
		fives++
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	frac := max(twos, fives)
	if frac > maxFrac {
		return "", false
	}
	return r.FloatString(frac), true
}

func compareConditionalNumbersExact(a, b conditionalValue) int {
	ar, aok := valueToRat(a)
	br, bok := valueToRat(b)
	if aok && bok {
		return ar.Cmp(br)
	}
	return compareConditionalDecimal(a.number, b.number)
}

func conditionalValueIsNonZero(v conditionalValue) bool {
	if v.rat != nil {
		return v.rat.Sign() != 0
	}
	return v.number.digits != "0"
}

func (e *ConditionalEvaluator) evalExpr(node *conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	if err := e.ctx.Err(); err != nil {
		return conditionalValue{}, "", err
	}
	if !e.spend(1) {
		return conditionalValue{}, "条件格式超过本次求值预算", nil
	}
	switch node.kind {
	case cfExprLiteral:
		return node.value, "", nil
	case cfExprRef:
		return e.evalRef(node.ref, group)
	case cfExprRange:
		// A range in scalar context is invalid unless used inside a range-accepting function.
		return conditionalErrorVal("#VALUE!"), "", nil
	case cfExprUnary:
		v, notice, err := e.evalExpr(node.args[0], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if v.kind == "error" {
			return v, "", nil
		}
		if node.op == "+" {
			// Excel unary + preserves text/value or returns number; for non-number text it's a no-op.
			return v, "", nil
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		return ratToConditionalValue(r.Neg(r)), "", nil
	case cfExprPercent:
		v, notice, err := e.evalExpr(node.args[0], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		r.Quo(r, big.NewRat(100, 1))
		return ratToConditionalValue(r), "", nil
	case cfExprBinary:
		return e.evalBinary(node, group)
	case cfExprCall:
		return e.evalCall(node, group)
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func (e *ConditionalEvaluator) resolveRefCoords(ref conditionalReference, group *conditionalGroup) (int, int, bool) {
	row, col := ref.row, ref.col
	if !ref.absoluteRow {
		row += e.row - group.anchorRow
	}
	if !ref.absoluteCol {
		col += e.col - group.anchorCol
	}
	if row < 1 || row > formats.ExcelRows || col < 1 || col > formats.ExcelColumns {
		return 0, 0, false
	}
	return row, col, true
}

func (e *ConditionalEvaluator) resolveRangeBounds(node *conditionalExpr, group *conditionalGroup) (r1, c1, r2, c2 int, ok bool) {
	if node.kind == cfExprRef {
		r, c, valid := e.resolveRefCoords(node.ref, group)
		return r, c, r, c, valid
	}
	if node.kind != cfExprRange {
		return 0, 0, 0, 0, false
	}
	a, b := node.ref, node.endRef
	if a.row == 0 && b.row == 0 {
		// Whole-column range: A:C
		c1, c2 = a.col, b.col
		if !a.absoluteCol {
			c1 += e.col - group.anchorCol
		}
		if !b.absoluteCol {
			c2 += e.col - group.anchorCol
		}
		if c1 < 1 || c1 > formats.ExcelColumns || c2 < 1 || c2 > formats.ExcelColumns {
			return 0, 0, 0, 0, false
		}
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		return 1, c1, formats.ExcelRows, c2, true
	}
	if a.col == 0 && b.col == 0 {
		// Whole-row range: 2:5
		r1, r2 = a.row, b.row
		if !a.absoluteRow {
			r1 += e.row - group.anchorRow
		}
		if !b.absoluteRow {
			r2 += e.row - group.anchorRow
		}
		if r1 < 1 || r1 > formats.ExcelRows || r2 < 1 || r2 > formats.ExcelRows {
			return 0, 0, 0, 0, false
		}
		if r1 > r2 {
			r1, r2 = r2, r1
		}
		return r1, 1, r2, formats.ExcelColumns, true
	}
	var ok1, ok2 bool
	r1, c1, ok1 = e.resolveRefCoords(a, group)
	r2, c2, ok2 = e.resolveRefCoords(b, group)
	if !ok1 || !ok2 {
		return 0, 0, 0, 0, false
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	return r1, c1, r2, c2, true
}

func (e *ConditionalEvaluator) evalRef(ref conditionalReference, group *conditionalGroup) (conditionalValue, string, error) {
	row, col, ok := e.resolveRefCoords(ref, group)
	if !ok {
		return conditionalErrorVal("#REF!"), "", nil
	}
	if row == e.row && col == e.col {
		v, notice := e.currentValue()
		return v, notice, nil
	}
	if e.resolver == nil {
		return conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	cell, available, err := e.resolver(row, col)
	if err != nil {
		var unavailable *conditionalDependencyUnavailable
		if errors.As(err, &unavailable) {
			return conditionalValue{}, unavailable.reason, nil
		}
		return conditionalValue{}, "", err
	}
	if !available {
		return conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	v, notice := e.value(cell)
	return v, notice, nil
}

func (e *ConditionalEvaluator) loadRange(node *conditionalExpr, group *conditionalGroup) (conditionalRangeData, conditionalValue, string, error) {
	if node.kind != cfExprRef && node.kind != cfExprRange {
		return conditionalRangeData{}, conditionalErrorVal("#VALUE!"), "", nil
	}
	r1, c1, r2, c2, ok := e.resolveRangeBounds(node, group)
	if !ok {
		return conditionalRangeData{}, conditionalErrorVal("#REF!"), "", nil
	}
	if e.rangeResolver != nil {
		data, notice, err := e.rangeResolver(r1, c1, r2, c2)
		if err != nil {
			var unavailable *conditionalDependencyUnavailable
			if errors.As(err, &unavailable) {
				return conditionalRangeData{}, conditionalValue{}, unavailable.reason, nil
			}
			return conditionalRangeData{}, conditionalValue{}, "", err
		}
		return data, conditionalValue{}, notice, nil
	}
	total := int64(r2-r1+1) * int64(c2-c1+1)
	if total > int64(formats.ConditionalDependencyReads) {
		return conditionalRangeData{}, conditionalValue{}, "条件格式引用范围超过本次读取容量，未扫描整表", nil
	}
	out := conditionalRangeData{r1: r1, c1: c1, r2: r2, c2: c2}
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			if !e.spend(1) {
				return conditionalRangeData{}, conditionalValue{}, "条件格式超过本次求值预算", nil
			}
			var cell CellData
			if r == e.row && c == e.col {
				cell = e.cell
			} else {
				if e.resolver == nil {
					return conditionalRangeData{}, conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
				}
				var available bool
				var err error
				cell, available, err = e.resolver(r, c)
				if err != nil {
					var unavailable *conditionalDependencyUnavailable
					if errors.As(err, &unavailable) {
						return conditionalRangeData{}, conditionalValue{}, unavailable.reason, nil
					}
					return conditionalRangeData{}, conditionalValue{}, "", err
				}
				if !available {
					return conditionalRangeData{}, conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
				}
			}
			if !cell.CacheMissing && cell.Type == "" && cell.Raw == "" {
				out.blankCount++
				continue
			}
			out.cells = append(out.cells, conditionalRangeCell{row: r, col: c, cell: cell})
		}
	}
	return out, conditionalValue{}, "", nil
}

func (e *ConditionalEvaluator) evalBinary(node *conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	left, notice, err := e.evalExpr(node.args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	right, notice, err := e.evalExpr(node.args[1], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if left.kind == "error" {
		return left, "", nil
	}
	if right.kind == "error" {
		return right, "", nil
	}
	switch node.op {
	case "=", "<>", "<", ">", "<=", ">=":
		cmp, valid := compareConditionalValues(left, right)
		if !valid {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		return conditionalBoolVal(matchConditionalComparison(cmp, node.op)), "", nil
	case "&":
		a, okA := e.asText(left)
		if !okA {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		b, okB := e.asText(right)
		if !okB {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		if len(a)+len(b) > formats.ConditionalOperandBytes || len(a)+len(b) > e.textBytes {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.textBytes -= len(a) + len(b)
		return conditionalText(a + b), "", nil
	case "+", "-", "*", "/", "^":
		ar, errA := conditionalAsRat(left)
		if errA != nil {
			return *errA, "", nil
		}
		br, errB := conditionalAsRat(right)
		if errB != nil {
			return *errB, "", nil
		}
		switch node.op {
		case "+":
			return ratToConditionalValue(ar.Add(ar, br)), "", nil
		case "-":
			return ratToConditionalValue(ar.Sub(ar, br)), "", nil
		case "*":
			return ratToConditionalValue(ar.Mul(ar, br)), "", nil
		case "/":
			if br.Sign() == 0 {
				return conditionalErrorVal("#DIV/0!"), "", nil
			}
			return ratToConditionalValue(ar.Quo(ar, br)), "", nil
		case "^":
			return conditionalPower(ar, br), "", nil
		}
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func conditionalAsRat(v conditionalValue) (*big.Rat, *conditionalValue) {
	switch v.kind {
	case "error":
		return nil, &v
	case "blank":
		return new(big.Rat), nil
	case "boolean":
		if v.boolean {
			return big.NewRat(1, 1), nil
		}
		return new(big.Rat), nil
	case "number":
		r, ok := valueToRat(v)
		if !ok {
			e := conditionalErrorVal("#NUM!")
			return nil, &e
		}
		return r, nil
	case "text":
		s := strings.TrimSpace(v.text)
		percent := strings.HasSuffix(s, "%")
		if percent {
			s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
		}
		d, ok := parseConditionalDecimal(s)
		if !ok {
			e := conditionalErrorVal("#VALUE!")
			return nil, &e
		}
		if percent {
			d.exponent -= 2
		}
		r, ok := decimalToRat(d)
		if !ok {
			e := conditionalErrorVal("#NUM!")
			return nil, &e
		}
		return r, nil
	}
	e := conditionalErrorVal("#VALUE!")
	return nil, &e
}

func conditionalAsBool(v conditionalValue) (bool, *conditionalValue) {
	switch v.kind {
	case "error":
		return false, &v
	case "boolean":
		return v.boolean, nil
	case "blank":
		return false, nil
	case "text":
		if strings.EqualFold(v.text, "TRUE") || strings.EqualFold(v.text, "FALSE") {
			return strings.EqualFold(v.text, "TRUE"), nil
		}
	case "number":
		return conditionalValueIsNonZero(v), nil
	}
	e := conditionalErrorVal("#VALUE!")
	return false, &e
}

func (e *ConditionalEvaluator) asText(v conditionalValue) (string, bool) {
	var s string
	switch v.kind {
	case "text":
		s = v.text
	case "blank":
		s = ""
	case "boolean":
		if v.boolean {
			s = "TRUE"
		} else {
			s = "FALSE"
		}
	case "number":
		s = formatConditionalDecimalText(v)
	case "error":
		s = v.text
	}
	if len(s) > formats.ConditionalOperandBytes || len(s) > e.textBytes {
		return "", false
	}
	return s, true
}

func formatConditionalDecimalText(v conditionalValue) string {
	if v.rat != nil {
		if s, ok := exactRatDecimalString(v.rat, 15); ok {
			return trimTrailingDecimalZeros(s)
		}
		f, _ := v.rat.Float64()
		return strconv.FormatFloat(f, 'G', 15, 64)
	}
	d := v.number
	if d.digits == "" || d.digits == "0" {
		return "0"
	}
	if r, ok := decimalToRat(d); ok {
		if s, ok := exactRatDecimalString(r, 15); ok {
			return trimTrailingDecimalZeros(s)
		}
	}
	sign := ""
	if d.negative {
		sign = "-"
	}
	if d.exponent == 0 {
		return sign + d.digits
	}
	if len(d.digits) == 1 {
		return sign + d.digits + "E" + strconv.FormatInt(d.exponent, 10)
	}
	exp := d.exponent + int64(len(d.digits)-1)
	return sign + d.digits[:1] + "." + d.digits[1:] + "E" + strconv.FormatInt(exp, 10)
}

func trimTrailingDecimalZeros(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

func conditionalPower(a, b *big.Rat) conditionalValue {
	if a.Sign() == 0 && b.Sign() == 0 {
		return conditionalErrorVal("#NUM!")
	}
	if a.Sign() == 0 && b.Sign() < 0 {
		return conditionalErrorVal("#DIV/0!")
	}
	if b.IsInt() && b.Num().IsInt64() {
		exp := b.Num().Int64()
		if exp >= -64 && exp <= 64 {
			res := big.NewRat(1, 1)
			base := new(big.Rat).Set(a)
			neg := exp < 0
			if neg {
				exp = -exp
			}
			for exp > 0 {
				if exp&1 != 0 {
					res.Mul(res, base)
					if res.Num().BitLen() > formats.ConditionalArithBits || res.Denom().BitLen() > formats.ConditionalArithBits {
						goto floatPow
					}
				}
				exp >>= 1
				if exp > 0 {
					base.Mul(base, base)
					if base.Num().BitLen() > formats.ConditionalArithBits || base.Denom().BitLen() > formats.ConditionalArithBits {
						goto floatPow
					}
				}
			}
			if neg {
				if res.Sign() == 0 {
					return conditionalErrorVal("#DIV/0!")
				}
				res.Inv(res)
			}
			return ratToConditionalValue(res)
		}
	}
floatPow:
	af, _ := a.Float64()
	bf, _ := b.Float64()
	res := math.Pow(af, bf)
	if math.IsNaN(res) || math.IsInf(res, 0) {
		return conditionalErrorVal("#NUM!")
	}
	d, ok := parseConditionalDecimal(strconv.FormatFloat(res, 'g', 15, 64))
	if !ok {
		return conditionalErrorVal("#NUM!")
	}
	return conditionalNumberVal(d)
}
