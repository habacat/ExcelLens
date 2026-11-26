package conditional

import (
	"math"
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

// CellOperand is the minimal source cell snapshot required to evaluate a
// conditional formatting expression without coupling to presentation styles.
type CellOperand struct {
	Raw          string
	Type         string
	CacheMissing bool
}

// Decimal represents a canonical base-10 value without float rounding or
// expanding hostile scientific exponents.
type Decimal = excelformula.Decimal

// EvalValue is a typed runtime value produced by conditional formula evaluation.
type EvalValue struct {
	CalculatedFloat bool
	Kind            string
	Text, Folded    string
	Number          Decimal
	Rat             *big.Rat
	Boolean         bool
}

// Area represents a 1-based inclusive worksheet rectangle.
type Area struct {
	Row, Col       int
	EndRow, EndCol int
}

// ParseDecimal parses an exact decimal or scientific literal without floating-point loss.
func ParseDecimal(raw string) (Decimal, bool) {
	return excelformula.ParseDecimal(raw)
}

// CompareDecimal compares two canonical decimals without constructing large powers of ten.
func CompareDecimal(a, b Decimal) int {
	return excelformula.CompareDecimal(a, b)
}

func TextValue(value string) EvalValue {
	return EvalValue{Kind: "text", Text: value, Folded: excelformula.FoldString(value)}
}

func NumberValue(d Decimal) EvalValue {
	return EvalValue{Kind: "number", Number: d}
}

func IntValue(n int64) EvalValue {
	d, _ := ParseDecimal(strconv.FormatInt(n, 10))
	return EvalValue{Kind: "number", Number: d}
}

func BoolValue(b bool) EvalValue {
	return EvalValue{Kind: "boolean", Boolean: b}
}

func ErrorValue(code string) EvalValue {
	return EvalValue{Kind: "error", Text: code}
}

func BlankValue() EvalValue {
	return EvalValue{Kind: "blank"}
}

// CompileExpression parses a conditional formatting expression using EvalValue literals.
func CompileExpression(raw, sheet string, lookupName func(string) (Reference, string)) (*Expression[EvalValue], int, string) {
	return Compile(raw, sheet, Bindings[EvalValue]{
		Text:  TextValue,
		Bool:  BoolValue,
		Error: ErrorValue,
		Blank: BlankValue(),
		Number: func(raw string) (EvalValue, bool) {
			d, ok := ParseDecimal(raw)
			return NumberValue(d), ok
		},
		Fold: func(v *EvalValue, op string) bool {
			if v.Kind != "number" {
				return false
			}
			switch op {
			case "%":
				if v.Number.Digits != "0" {
					v.Number.Exponent -= 2
				}
			case "-":
				if v.Number.Digits != "0" {
					v.Number.Negative = !v.Number.Negative
				}
			}
			return true
		},
		Name: lookupName,
	})
}

func DecimalToRat(d Decimal) (*big.Rat, bool) {
	if d.Digits == "" || d.Digits == "0" {
		return new(big.Rat), true
	}
	if len(d.Digits) > 128 || d.Exponent > formats.ConditionalArithExponent || d.Exponent < -formats.ConditionalArithExponent {
		return nil, false
	}
	num, ok := new(big.Int).SetString(d.Digits, 10)
	if !ok {
		return nil, false
	}
	if d.Negative {
		num.Neg(num)
	}
	den := big.NewInt(1)
	if d.Exponent > 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(d.Exponent), nil)
		num.Mul(num, scale)
	} else if d.Exponent < 0 {
		den.Exp(big.NewInt(10), big.NewInt(-d.Exponent), nil)
	}
	return new(big.Rat).SetFrac(num, den), true
}

func ValueToRat(v EvalValue) (*big.Rat, bool) {
	if v.Rat != nil {
		return new(big.Rat).Set(v.Rat), true
	}
	return DecimalToRat(v.Number)
}

// FloatResult converts a binary calculation result keeping both the full
// intermediate rational and Excel's 15-significant-digit comparison value.
func FloatResult(f float64) EvalValue {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return ErrorValue("#NUM!")
	}
	d, ok := ParseDecimal(strconv.FormatFloat(f, 'g', 15, 64))
	if !ok {
		return ErrorValue("#NUM!")
	}
	return EvalValue{Kind: "number", Number: d, Rat: new(big.Rat).SetFloat64(f), CalculatedFloat: true}
}

func SumResult(r *big.Rat) EvalValue {
	if r == nil {
		return FloatResult(0)
	}
	f, _ := r.Float64()
	return FloatResult(f)
}

func RatToValue(r *big.Rat) EvalValue {
	if r == nil || r.Sign() == 0 {
		return EvalValue{Kind: "number", Number: Decimal{Digits: "0"}}
	}
	// Guard bit length and Excel numeric range (~1e308).
	if f, _ := r.Float64(); math.IsInf(f, 0) || math.IsNaN(f) {
		return ErrorValue("#NUM!")
	}
	if r.Num().BitLen() > formats.ConditionalArithBits || r.Denom().BitLen() > formats.ConditionalArithBits {
		f, _ := r.Float64()
		d, ok := ParseDecimal(strconv.FormatFloat(f, 'g', 15, 64))
		if !ok {
			return ErrorValue("#NUM!")
		}
		return EvalValue{Kind: "number", Number: d}
	}
	if s, ok := ExactRatDecimalString(r, 30); ok {
		if d, dok := ParseDecimal(s); dok {
			return EvalValue{Kind: "number", Number: d}
		}
	}
	f, _ := r.Float64()
	d, ok := ParseDecimal(strconv.FormatFloat(f, 'g', 15, 64))
	if !ok {
		return ErrorValue("#NUM!")
	}
	return EvalValue{Kind: "number", Number: d, Rat: new(big.Rat).Set(r)}
}

func ExactRatDecimalString(r *big.Rat, maxFrac int) (string, bool) {
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

func compareNumbersExact(a, b EvalValue) int {
	ar, aok := ValueToRat(a)
	br, bok := ValueToRat(b)
	if aok && bok {
		return ar.Cmp(br)
	}
	return CompareDecimal(a.Number, b.Number)
}

func IsNonZero(v EvalValue) bool {
	if v.Rat != nil {
		return v.Rat.Sign() != 0
	}
	return v.Number.Digits != "0"
}

func CompareValues(a, b EvalValue) (int, bool) {
	if a.Kind == "error" || b.Kind == "error" {
		return 0, false
	}
	if a.Kind == "blank" {
		a = blankAs(b.Kind)
	}
	if b.Kind == "blank" {
		b = blankAs(a.Kind)
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
	if a.Kind != b.Kind {
		return rank(a.Kind) - rank(b.Kind), true
	}
	switch a.Kind {
	case "number":
		if a.CalculatedFloat || b.CalculatedFloat {
			ar, ok1 := ValueToRat(a)
			br, ok2 := ValueToRat(b)
			if ok1 && ok2 {
				af, _ := ar.Float64()
				bf, _ := br.Float64()
				da, oka := ParseDecimal(strconv.FormatFloat(af, 'g', 15, 64))
				db, okb := ParseDecimal(strconv.FormatFloat(bf, 'g', 15, 64))
				if oka && okb {
					return CompareDecimal(da, db), true
				}
			}
		}
		if a.Rat != nil || b.Rat != nil {
			return compareNumbersExact(a, b), true
		}
		return CompareDecimal(a.Number, b.Number), true
	case "text":
		return strings.Compare(a.Folded, b.Folded), true
	case "boolean":
		if a.Boolean == b.Boolean {
			return 0, true
		}
		if a.Boolean {
			return 1, true
		}
		return -1, true
	}
	return 0, false
}

func blankAs(kind string) EvalValue {
	switch kind {
	case "text":
		return TextValue("")
	case "boolean":
		return EvalValue{Kind: "boolean"}
	}
	return EvalValue{Kind: "number", Number: Decimal{Digits: "0"}}
}

func AsRat(v EvalValue) (*big.Rat, *EvalValue) {
	switch v.Kind {
	case "error":
		return nil, &v
	case "blank":
		return new(big.Rat), nil
	case "boolean":
		if v.Boolean {
			return big.NewRat(1, 1), nil
		}
		return new(big.Rat), nil
	case "number":
		r, ok := ValueToRat(v)
		if !ok {
			e := ErrorValue("#NUM!")
			return nil, &e
		}
		return r, nil
	case "text":
		s := strings.TrimSpace(v.Text)
		percent := strings.HasSuffix(s, "%")
		if percent {
			s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
		}
		d, ok := ParseDecimal(s)
		if !ok {
			e := ErrorValue("#VALUE!")
			return nil, &e
		}
		if percent {
			d.Exponent -= 2
		}
		r, ok := DecimalToRat(d)
		if !ok {
			e := ErrorValue("#NUM!")
			return nil, &e
		}
		return r, nil
	}
	e := ErrorValue("#VALUE!")
	return nil, &e
}

func AsBool(v EvalValue) (bool, *EvalValue) {
	switch v.Kind {
	case "error":
		return false, &v
	case "boolean":
		return v.Boolean, nil
	case "blank":
		return false, nil
	case "text":
		if b, ok := excelformula.BooleanText(v.Text); ok {
			return b, nil
		}
	case "number":
		return IsNonZero(v), nil
	}
	e := ErrorValue("#VALUE!")
	return false, &e
}

func FormatDecimalText(v EvalValue) string {
	if v.Rat != nil {
		if s, ok := ExactRatDecimalString(v.Rat, 15); ok {
			return trimTrailingDecimalZeros(s)
		}
		f, _ := v.Rat.Float64()
		return strconv.FormatFloat(f, 'G', 15, 64)
	}
	d := v.Number
	if d.Digits == "" || d.Digits == "0" {
		return "0"
	}
	if r, ok := DecimalToRat(d); ok {
		if s, ok := ExactRatDecimalString(r, 15); ok {
			return trimTrailingDecimalZeros(s)
		}
	}
	sign := ""
	if d.Negative {
		sign = "-"
	}
	if d.Exponent == 0 {
		return sign + d.Digits
	}
	if len(d.Digits) == 1 {
		return sign + d.Digits + "E" + strconv.FormatInt(d.Exponent, 10)
	}
	exp := d.Exponent + int64(len(d.Digits)-1)
	return sign + d.Digits[:1] + "." + d.Digits[1:] + "E" + strconv.FormatInt(exp, 10)
}

func trimTrailingDecimalZeros(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

func Power(a, b *big.Rat) EvalValue {
	if a.Sign() == 0 && b.Sign() == 0 {
		return ErrorValue("#NUM!")
	}
	if a.Sign() == 0 && b.Sign() < 0 {
		return ErrorValue("#DIV/0!")
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
					return ErrorValue("#DIV/0!")
				}
				res.Inv(res)
			}
			return RatToValue(res)
		}
	}
floatPow:
	af, _ := a.Float64()
	bf, _ := b.Float64()
	res := math.Pow(af, bf)
	if math.IsNaN(res) || math.IsInf(res, 0) {
		return ErrorValue("#NUM!")
	}
	d, ok := ParseDecimal(strconv.FormatFloat(res, 'g', 15, 64))
	if !ok {
		return ErrorValue("#NUM!")
	}
	return NumberValue(d)
}

func TypedKey(v EvalValue) string {
	switch v.Kind {
	case "number":
		d := v.Number
		return "n" + strconv.FormatBool(d.Negative) + d.Digits + "e" + strconv.FormatInt(d.Exponent, 10)
	case "text":
		return "t" + v.Folded
	case "boolean":
		if v.Boolean {
			return "b1"
		}
		return "b0"
	case "error":
		return "e" + v.Text
	default:
		return "z"
	}
}

func GroupKey(v EvalValue) string {
	if v.Kind == "text" {
		if v.Text == "" {
			return "z"
		}
		if d, ok := ParseDecimal(strings.TrimSpace(v.Text)); ok {
			return TypedKey(NumberValue(d))
		}
	}
	return TypedKey(v)
}

func LookupKey(v EvalValue, match bool) (string, bool) {
	// A rounded calculated criterion may compare equal to several different
	// exact source keys. Only use the direct lookup if equality is exact.
	if v.CalculatedFloat || v.Rat != nil {
		return "", false
	}
	if match {
		if v.Kind == "blank" {
			v = IntValue(0)
		}
		if v.Kind == "text" && strings.ContainsAny(v.Text, "*?~") {
			return "", false
		}
		return TypedKey(v), true
	}
	if v.Kind == "blank" {
		v = IntValue(0)
	}
	if v.Kind == "text" {
		text := v.Text
		if strings.HasPrefix(text, "<") || strings.HasPrefix(text, ">") {
			return "", false
		}
		text = strings.TrimPrefix(text, "=")
		if strings.ContainsAny(text, "*?~") || strings.HasPrefix(text, "#") {
			return "", false
		}
		if strings.EqualFold(text, "TRUE") || strings.EqualFold(text, "FALSE") {
			return TypedKey(BoolValue(strings.EqualFold(text, "TRUE"))), true
		}
		v = TextValue(text)
	}
	return GroupKey(v), true
}

// DecodeOperand converts a source cell operand into a typed conditional value
// while enforcing operand and per-request text budgets.
func DecodeOperand(cell CellOperand, textBytes *int) (EvalValue, string) {
	if cell.CacheMissing {
		return EvalValue{}, "所引用公式没有保存计算结果"
	}
	if len(cell.Raw) > formats.ConditionalOperandBytes || len(cell.Raw) > *textBytes {
		return EvalValue{}, "条件操作数超过本次文本容量"
	}
	*textBytes -= len(cell.Raw)
	switch cell.Type {
	case "e":
		return EvalValue{Kind: "error", Text: cell.Raw}, ""
	case "s", "str", "inlineStr", "text", "string":
		return TextValue(cell.Raw), ""
	case "b":
		switch strings.ToUpper(cell.Raw) {
		case "TRUE", "1":
			return EvalValue{Kind: "boolean", Boolean: true}, ""
		case "FALSE", "0":
			return EvalValue{Kind: "boolean"}, ""
		}
		return EvalValue{}, "布尔源值无效"
	case "", "n":
		if cell.Raw == "" {
			return EvalValue{Kind: "blank"}, ""
		}
		number, ok := ParseDecimal(cell.Raw)
		if !ok {
			return EvalValue{}, "数值源值无效，未按文本或零替代"
		}
		return EvalValue{Kind: "number", Number: number}, ""
	default:
		return EvalValue{}, "条件规则遇到尚未支持的源类型"
	}
}
