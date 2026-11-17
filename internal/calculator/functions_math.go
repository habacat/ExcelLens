package calculator

import (
	"errors"
	"math"
	"math/big"
	"strconv"
)

func registerMathFunctions(add functionRegistrar) {
	add("SIGN,INT,SQRT,SQRTPI,EXP,LN,LOG10,SIN,COS,TAN,ASIN,ACOS,ATAN,SINH,COSH,TANH,ASINH,ACOSH,ATANH,DEGREES,RADIANS,FACT,FACTDOUBLE,EVEN,ODD", "数值", 1, 1, mathFunction)
	add("TRUNC,LOG", "数值, [参数]", 1, 2, mathFunction)
	add("MOD,QUOTIENT,POWER,ATAN2,COMBIN,COMBINA,PERMUT,PERMUTATIONA,MROUND", "数值1, 数值2", 2, 2, mathFunction)
	add("CEILING,FLOOR", "数值, 基数", 2, 2, mathFunction)
	add("CEILING.MATH,FLOOR.MATH", "数值, [基数], [模式]", 1, 3, mathFunction)
	add("CEILING.PRECISE,FLOOR.PRECISE", "数值, [基数]", 1, 2, mathFunction)
	add("GCD,LCM", "数值1, …", 1, 128, mathFunction)
	add("PI", "", 0, 0, mathFunction)
}
func floatingResult(e *formulaEval, value float64) (FormulaValue, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return FormulaValue{}, errors.New("#NUM! 数值超出定义域")
	}
	text := strconv.FormatFloat(value, 'g', 15, 64)
	n, err := Canonical(text)
	if err != nil {
		return FormulaValue{}, err
	}
	e.approximate = true
	return NumberValue(n), nil
}
func truncInteger(x *big.Rat) *big.Int { return new(big.Int).Quo(x.Num(), x.Denom()) }
func floorInteger(x *big.Rat) *big.Int {
	q := truncInteger(x)
	if x.Sign() < 0 && !x.IsInt() {
		q.Sub(q, big.NewInt(1))
	}
	return q
}
func ceilInteger(x *big.Rat) *big.Int {
	q := truncInteger(x)
	if x.Sign() > 0 && !x.IsInt() {
		q.Add(q, big.NewInt(1))
	}
	return q
}
func formulaPower(e *formulaEval, base, exponent *big.Rat) (FormulaValue, error) {
	if exponent.IsInt() {
		r, err := binaryArithmetic('^', base, exponent)
		return NumberValue(r), err
	}
	if base.Sign() == 0 && exponent.Sign() < 0 {
		return FormulaValue{}, errors.New("#DIV/0! 0不能取负次幂")
	}
	x, _ := base.Float64()
	y, _ := exponent.Float64()
	return floatingResult(e, math.Pow(x, y))
}
func factorial(n int64, step int64) (*big.Int, error) {
	if n < 0 || n > 1000 {
		return nil, errors.New("#NUM! 阶乘参数超限")
	}
	r := big.NewInt(1)
	for i := n; i > 1; i -= step {
		r.Mul(r, big.NewInt(i))
		if r.BitLen() > maxBits {
			return nil, errors.New("#NUM! 阶乘结果过大")
		}
	}
	return r, nil
}
func mathFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	if node.op == "PI" {
		return floatingResult(e, math.Pi)
	}
	if node.op == "GCD" || node.op == "LCM" {
		nums, err := collectNumbers(e, node.args)
		if err != nil {
			return FormulaValue{}, err
		}
		result := big.NewInt(0)
		if node.op == "LCM" {
			result.SetInt64(1)
		}
		for _, n := range nums {
			if n.Sign() < 0 {
				return FormulaValue{}, errors.New("#NUM! 参数不能为负")
			}
			i := truncInteger(n)
			g := new(big.Int).GCD(nil, nil, result, i)
			if node.op == "GCD" {
				result = g
			} else if i.Sign() == 0 || result.Sign() == 0 {
				result.SetInt64(0)
			} else {
				result.Quo(result, g)
				result.Mul(result, i)
			}
			if result.BitLen() > maxBits {
				return FormulaValue{}, errors.New("#NUM! 结果过大")
			}
		}
		return numberResult(new(big.Rat).SetInt(result))
	}
	vals, err := evaluatedArgs(e, node)
	if err != nil {
		return FormulaValue{}, err
	}
	args := make([]*big.Rat, len(vals))
	for i, v := range vals {
		args[i], err = formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
	}
	x := args[0]
	xf, _ := x.Float64()
	yf := 0.0
	if len(args) > 1 {
		yf, _ = args[1].Float64()
	}
	integer := func(v *big.Int) (FormulaValue, error) { return numberResult(new(big.Rat).SetInt(v)) }
	switch node.op {
	case "SIGN":
		return integerResult(int64(x.Sign()))
	case "INT":
		return integer(floorInteger(x))
	case "TRUNC":
		places := int64(0)
		if len(vals) > 1 {
			places, err = formulaInteger(vals[1])
		}
		if err != nil || places < -100 || places > 100 {
			return FormulaValue{}, errors.New("#NUM! 截断位数超限")
		}
		factor := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(int(places)))), nil))
		scaled := new(big.Rat).Set(x)
		if places >= 0 {
			scaled.Mul(scaled, factor)
		} else {
			scaled.Quo(scaled, factor)
		}
		out := new(big.Rat).SetInt(truncInteger(scaled))
		if places >= 0 {
			out.Quo(out, factor)
		} else {
			out.Mul(out, factor)
		}
		return numberResult(out)
	case "MOD", "QUOTIENT":
		if args[1].Sign() == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 除数不能为0")
		}
		ratio := new(big.Rat).Quo(x, args[1])
		if node.op == "QUOTIENT" {
			return integer(truncInteger(ratio))
		}
		return numberResult(new(big.Rat).Sub(x, new(big.Rat).Mul(args[1], new(big.Rat).SetInt(floorInteger(ratio)))))
	case "POWER":
		return formulaPower(e, x, args[1])
	case "SQRT":
		if x.Sign() < 0 {
			return FormulaValue{}, errors.New("#NUM! 负数不能开平方")
		}
		a, b := new(big.Int).Sqrt(x.Num()), new(big.Int).Sqrt(x.Denom())
		if new(big.Int).Mul(new(big.Int).Set(a), a).Cmp(x.Num()) == 0 && new(big.Int).Mul(new(big.Int).Set(b), b).Cmp(x.Denom()) == 0 {
			return numberResult(new(big.Rat).SetFrac(a, b))
		}
		return floatingResult(e, math.Sqrt(xf))
	case "SQRTPI":
		return floatingResult(e, math.Sqrt(xf*math.Pi))
	case "EXP":
		return floatingResult(e, math.Exp(xf))
	case "LN":
		return floatingResult(e, math.Log(xf))
	case "LOG10":
		return floatingResult(e, math.Log10(xf))
	case "LOG":
		if len(args) == 1 {
			yf = 10
		}
		if xf <= 0 || yf <= 0 || yf == 1 {
			return FormulaValue{}, errors.New("#NUM! 对数参数无效")
		}
		return floatingResult(e, math.Log(xf)/math.Log(yf))
	case "SIN":
		return floatingResult(e, math.Sin(xf))
	case "COS":
		return floatingResult(e, math.Cos(xf))
	case "TAN":
		return floatingResult(e, math.Tan(xf))
	case "ASIN":
		return floatingResult(e, math.Asin(xf))
	case "ACOS":
		return floatingResult(e, math.Acos(xf))
	case "ATAN":
		return floatingResult(e, math.Atan(xf))
	case "ATAN2":
		if xf == 0 && yf == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 坐标不能同时为0")
		}
		return floatingResult(e, math.Atan2(yf, xf))
	case "SINH":
		return floatingResult(e, math.Sinh(xf))
	case "COSH":
		return floatingResult(e, math.Cosh(xf))
	case "TANH":
		return floatingResult(e, math.Tanh(xf))
	case "ASINH":
		return floatingResult(e, math.Asinh(xf))
	case "ACOSH":
		return floatingResult(e, math.Acosh(xf))
	case "ATANH":
		return floatingResult(e, math.Atanh(xf))
	case "DEGREES":
		return floatingResult(e, xf*180/math.Pi)
	case "RADIANS":
		return floatingResult(e, xf*math.Pi/180)
	case "FACT", "FACTDOUBLE":
		i, err := formulaInteger(vals[0])
		if err != nil {
			return FormulaValue{}, err
		}
		step := int64(1)
		if node.op == "FACTDOUBLE" {
			step = 2
		}
		r, err := factorial(i, step)
		if err != nil {
			return FormulaValue{}, err
		}
		return integer(r)
	case "COMBIN", "COMBINA", "PERMUT", "PERMUTATIONA":
		a, err := formulaInteger(vals[0])
		if err != nil {
			return FormulaValue{}, err
		}
		b, err := formulaInteger(vals[1])
		if err != nil || a < 0 || b < 0 || a > 1000 || b > 1000 {
			return FormulaValue{}, errors.New("#NUM! 组合参数无效")
		}
		if node.op == "COMBINA" {
			a += b - 1
			if b == 0 {
				return integerResult(1)
			}
		}
		if node.op == "PERMUTATIONA" {
			return integer(new(big.Int).Exp(big.NewInt(a), big.NewInt(b), nil))
		}
		if b > a {
			return FormulaValue{}, errors.New("#NUM! 选择数量超过总数")
		}
		if node.op == "PERMUT" {
			r := big.NewInt(1)
			for i := int64(0); i < b; i++ {
				r.Mul(r, big.NewInt(a-i))
			}
			return integer(r)
		}
		return integer(new(big.Int).Binomial(a, b))
	case "EVEN", "ODD":
		a := ceilInteger(new(big.Rat).Abs(x))
		parity := uint(0)
		if node.op == "ODD" {
			parity = 1
		}
		if a.Bit(0) != parity {
			a.Add(a, big.NewInt(1))
		}
		if x.Sign() < 0 {
			a.Neg(a)
		}
		return integer(a)
	case "MROUND", "CEILING", "FLOOR", "CEILING.MATH", "FLOOR.MATH", "CEILING.PRECISE", "FLOOR.PRECISE":
		significance := big.NewRat(1, 1)
		if len(args) > 1 {
			significance = new(big.Rat).Set(args[1])
		}
		if significance.Sign() == 0 {
			return integerResult(0)
		}
		if node.op == "MROUND" && x.Sign()*significance.Sign() < 0 {
			return FormulaValue{}, errors.New("#NUM! 数值与基数符号不同")
		}
		if (node.op == "CEILING" || node.op == "FLOOR") && x.Sign() > 0 && significance.Sign() < 0 {
			return FormulaValue{}, errors.New("#NUM! 基数符号无效")
		}
		sig := new(big.Rat).Abs(significance)
		ratio := new(big.Rat).Quo(x, sig)
		var q *big.Int
		if node.op == "MROUND" {
			abs := new(big.Rat).Abs(ratio)
			q = floorInteger(new(big.Rat).Add(abs, big.NewRat(1, 2)))
			if x.Sign() < 0 {
				q.Neg(q)
			}
		} else {
			up := node.op == "CEILING" || node.op == "CEILING.MATH" || node.op == "CEILING.PRECISE"
			mode := len(args) > 2 && args[2].Sign() != 0
			if x.Sign() < 0 && ((mode && (node.op == "CEILING.MATH" || node.op == "FLOOR.MATH")) || ((node.op == "CEILING" || node.op == "FLOOR") && significance.Sign() < 0)) {
				up = !up
			}
			if up {
				q = ceilInteger(ratio)
			} else {
				q = floorInteger(ratio)
			}
		}
		return numberResult(new(big.Rat).Mul(new(big.Rat).SetInt(q), sig))
	}
	return FormulaValue{}, errors.New("#NAME? 未注册数学函数")
}
