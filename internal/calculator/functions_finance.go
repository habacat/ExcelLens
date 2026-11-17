package calculator

import (
	"errors"
	"math"
	"strings"
)

func registerFinanceFunctions(add functionRegistrar) {
	add("PV,FV,PMT", "利率, 期数, 金额, [末值], [期初期末]", 3, 5, financeFunction)
	add("NPER", "利率, 每期支付, 现值, [末值], [期初期末]", 3, 5, financeFunction)
	add("RATE", "期数, 每期支付, 现值, [末值], [期初期末], [猜测值]", 3, 6, financeFunction)
	add("IPMT,PPMT", "利率, 期间, 总期数, 现值, [末值], [期初期末]", 4, 6, financeFunction)
	add("NPV", "贴现率, 现金流1, …", 2, 128, financeFunction)
	add("IRR", "现金流区域, [猜测值]", 1, 2, financeFunction)
	add("XNPV", "贴现率, 现金流区域, 日期区域", 3, 3, financeFunction)
	add("XIRR", "现金流区域, 日期区域, [猜测值]", 2, 3, financeFunction)
	add("SLN", "原值, 残值, 使用年限", 3, 3, financeFunction)
	add("SYD", "原值, 残值, 使用年限, 期间", 4, 4, financeFunction)
	add("DDB", "原值, 残值, 使用年限, 期间, [倍率]", 4, 5, financeFunction)
	add("EFFECT,NOMINAL", "利率, 每年复利次数", 2, 2, extraFinanceFunction)
	add("RRI,PDURATION", "期数或利率, 现值, 终值", 3, 3, extraFinanceFunction)
	add("MIRR", "现金流区域, 融资利率, 再投资利率", 3, 3, extraFinanceFunction)
	add("DOLLARDE,DOLLARFR", "金额, 分母", 2, 2, extraFinanceFunction)
}

func extraFinanceFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	if node.op == "MIRR" {
		flows, err := collectNumbers(e, node.args[:1])
		if err != nil {
			return FormulaValue{}, err
		}
		if len(flows) < 2 {
			return FormulaValue{}, errors.New("#DIV/0! 现金流不足")
		}
		rates := [2]float64{}
		for i := range rates {
			v, err := e.eval(node.args[i+1])
			if err != nil {
				return FormulaValue{}, err
			}
			x, err := formulaNumber(v)
			if err != nil {
				return FormulaValue{}, err
			}
			rates[i], _ = x.Float64()
			if rates[i] <= -1 {
				return FormulaValue{}, errors.New("#NUM! 利率必须大于-1")
			}
		}
		negative, positive := 0.0, 0.0
		last := len(flows) - 1
		for i, flow := range flows {
			if err := e.ctx.Err(); err != nil {
				return FormulaValue{}, err
			}
			value, _ := flow.Float64()
			if value < 0 {
				negative += value / math.Pow(1+rates[0], float64(i))
			} else {
				positive += value * math.Pow(1+rates[1], float64(last-i))
			}
		}
		if negative == 0 || positive == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 现金流必须包含正数和负数")
		}
		return floatingResult(e, math.Pow(positive/-negative, 1/float64(last))-1)
	}
	vals, err := evaluatedArgs(e, node)
	if err != nil {
		return FormulaValue{}, err
	}
	a := make([]float64, len(vals))
	for i, v := range vals {
		x, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		a[i], _ = x.Float64()
	}
	switch node.op {
	case "EFFECT", "NOMINAL":
		periods := math.Trunc(a[1])
		if a[0] <= 0 || periods < 1 {
			return FormulaValue{}, errors.New("#NUM! 利率与复利次数必须为正")
		}
		if node.op == "EFFECT" {
			return floatingResult(e, math.Expm1(periods*math.Log1p(a[0]/periods)))
		}
		return floatingResult(e, periods*math.Expm1(math.Log1p(a[0])/periods))
	case "RRI", "PDURATION":
		if a[0] <= 0 || a[1] <= 0 || a[2] <= 0 {
			return FormulaValue{}, errors.New("#NUM! 参数必须为正")
		}
		if node.op == "RRI" {
			return floatingResult(e, math.Expm1(math.Log(a[2]/a[1])/a[0]))
		}
		return floatingResult(e, math.Log(a[2]/a[1])/math.Log1p(a[0]))
	case "DOLLARDE", "DOLLARFR":
		denominator := math.Trunc(a[1])
		if denominator < 0 {
			return FormulaValue{}, errors.New("#NUM! 分母不能为负")
		}
		if denominator == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 分母不能为0")
		}
		digits := math.Pow10(int(math.Ceil(math.Log10(denominator))))
		whole, fraction := math.Modf(a[0])
		if node.op == "DOLLARDE" {
			return floatingResult(e, whole+fraction*digits/denominator)
		}
		return floatingResult(e, whole+fraction*denominator/digits)
	}
	return FormulaValue{}, errors.New("#NAME? 未注册财务函数")
}
func annuityPayment(rate, nper, pv, fv, kind float64) float64 {
	if rate == 0 {
		return -(pv + fv) / nper
	}
	power := math.Pow(1+rate, nper)
	return -(pv*power + fv) * rate / ((1 + rate*kind) * (power - 1))
}
func solveRate(e *formulaEval, guess float64, fn func(float64) float64) (float64, error) {
	x := guess
	for i := 0; i < 150; i++ {
		if err := e.ctx.Err(); err != nil {
			return 0, err
		}
		if x <= -1 || math.IsNaN(x) || math.IsInf(x, 0) {
			x = .1
		}
		y := fn(x)
		step := math.Max(1e-7, math.Abs(x)*1e-5)
		slope := (fn(x+step) - fn(x-step)) / (2 * step)
		if math.IsNaN(y) || math.IsInf(y, 0) || slope == 0 || math.IsNaN(slope) {
			return 0, errors.New("#NUM! 利率迭代未收敛")
		}
		next := x - y/slope
		if next <= -1 {
			next = (x - 1) / 2
		}
		if math.Abs(next-x) < 1e-11*(1+math.Abs(x)) {
			if math.Abs(fn(next)) < 1e-5 {
				return next, nil
			}
		}
		x = next
	}
	return 0, errors.New("#NUM! 利率迭代未收敛")
}
func financeFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	if n.op == "NPV" || n.op == "IRR" || n.op == "XNPV" || n.op == "XIRR" {
		rate := .1
		start := 1
		if n.op == "IRR" || n.op == "XIRR" {
			start = 0
		}
		if start == 1 {
			v, err := e.eval(n.args[0])
			if err != nil {
				return FormulaValue{}, err
			}
			r, err := formulaNumber(v)
			if err != nil {
				return FormulaValue{}, err
			}
			rate, _ = r.Float64()
		}
		cashArgs := n.args[start:]
		if n.op != "NPV" {
			cashArgs = n.args[start : start+1]
		}
		numbers, err := collectNumbers(e, cashArgs)
		if err != nil {
			return FormulaValue{}, err
		}
		if len(numbers) == 0 {
			return FormulaValue{}, errors.New("#NUM! 没有现金流")
		}
		flows := make([]float64, len(numbers))
		positive, negative := false, false
		for i, x := range numbers {
			flows[i], _ = x.Float64()
			positive = positive || x.Sign() > 0
			negative = negative || x.Sign() < 0
		}
		times := make([]float64, len(flows))
		for i := range times {
			times[i] = float64(i + 1)
			if n.op == "IRR" || n.op == "XIRR" {
				times[i] = float64(i)
			}
		}
		if strings.HasPrefix(n.op, "X") {
			dateIndex := 2
			if n.op == "XIRR" {
				dateIndex = 1
			}
			v, err := rangeArg(e, n.args[dateIndex])
			if err != nil {
				return FormulaValue{}, err
			}
			dates, _, _, err := materialize(e, v, maxArrayCells)
			if err != nil {
				return FormulaValue{}, err
			}
			if len(dates) != len(flows) {
				return FormulaValue{}, errors.New("#NUM! 日期与现金流数量不同")
			}
			first := int64(0)
			for i, date := range dates {
				_, day, err := e.serialTime(date)
				if err != nil {
					return FormulaValue{}, err
				}
				if i == 0 {
					first = day
				}
				if day < first {
					return FormulaValue{}, errors.New("#NUM! 日期早于初始日期")
				}
				times[i] = float64(day-first) / 365
			}
		}
		value := func(rate float64) float64 {
			if rate <= -1 {
				return math.NaN()
			}
			sum := 0.0
			for i, x := range flows {
				sum += x / math.Pow(1+rate, times[i])
			}
			return sum
		}
		if n.op == "IRR" || n.op == "XIRR" {
			if !positive || !negative {
				return FormulaValue{}, errors.New("#NUM! 现金流必须同时包含正数和负数")
			}
			guessIndex := 1
			if n.op == "XIRR" {
				guessIndex = 2
			}
			if len(n.args) > guessIndex {
				v, err := e.eval(n.args[guessIndex])
				if err != nil {
					return FormulaValue{}, err
				}
				number, err := formulaNumber(v)
				if err != nil {
					return FormulaValue{}, err
				}
				rate, _ = number.Float64()
			}
			r, err := solveRate(e, rate, value)
			if err != nil {
				return FormulaValue{}, err
			}
			return floatingResult(e, r)
		}
		return floatingResult(e, value(rate))
	}
	vals, err := evaluatedArgs(e, n)
	if err != nil {
		return FormulaValue{}, err
	}
	a := make([]float64, len(vals))
	for i, v := range vals {
		number, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		a[i], _ = number.Float64()
	}
	arg := func(i int, defaultValue float64) float64 {
		if i >= len(a) {
			return defaultValue
		}
		return a[i]
	}
	if n.op == "SLN" || n.op == "SYD" || n.op == "DDB" {
		cost, salvage, life := a[0], a[1], a[2]
		if cost < 0 || salvage < 0 || life <= 0 {
			return FormulaValue{}, errors.New("#NUM! 折旧参数无效")
		}
		if n.op == "SLN" {
			return floatingResult(e, (cost-salvage)/life)
		}
		period := a[3]
		if period < 1 || period > life {
			return FormulaValue{}, errors.New("#NUM! 折旧期间超界")
		}
		if n.op == "SYD" {
			return floatingResult(e, (cost-salvage)*(life-period+1)*2/(life*(life+1)))
		}
		factor := arg(4, 2)
		if factor <= 0 {
			return FormulaValue{}, errors.New("#NUM! 折旧倍率无效")
		}
		before := cost * math.Pow(math.Max(0, 1-factor/life), period-1)
		return floatingResult(e, math.Max(0, math.Min(before-salvage, before*factor/life)))
	}
	if n.op == "RATE" {
		periods, pmt, pv := a[0], a[1], a[2]
		fv, kind, guess := arg(3, 0), arg(4, 0), arg(5, .1)
		if periods <= 0 || kind != 0 && kind != 1 {
			return FormulaValue{}, errors.New("#NUM! 期间或支付类型无效")
		}
		fn := func(rate float64) float64 {
			if math.Abs(rate) < 1e-12 {
				return pv + pmt*periods + fv
			}
			power := math.Pow(1+rate, periods)
			return pv*power + pmt*(1+rate*kind)*(power-1)/rate + fv
		}
		r, err := solveRate(e, guess, fn)
		if err != nil {
			return FormulaValue{}, err
		}
		return floatingResult(e, r)
	}
	rate := a[0]
	if rate <= -1 {
		return FormulaValue{}, errors.New("#NUM! 利率必须大于-1")
	}
	if n.op == "NPER" {
		pmt, pv, fv, kind := a[1], a[2], arg(3, 0), arg(4, 0)
		if kind != 0 && kind != 1 {
			return FormulaValue{}, errors.New("#NUM! 支付类型无效")
		}
		if rate == 0 {
			return floatingResult(e, -(pv+fv)/pmt)
		}
		adjusted := pmt * (1 + rate*kind) / rate
		return floatingResult(e, math.Log((adjusted-fv)/(pv+adjusted))/math.Log(1+rate))
	}
	if n.op == "IPMT" || n.op == "PPMT" {
		period, periods, pv, fv, kind := a[1], a[2], a[3], arg(4, 0), arg(5, 0)
		if periods <= 0 || period < 1 || period > periods || kind != 0 && kind != 1 {
			return FormulaValue{}, errors.New("#NUM! 支付期间无效")
		}
		pmt := annuityPayment(rate, periods, pv, fv, kind)
		interest := 0.0
		if rate != 0 && !(kind == 1 && period == 1) {
			power := math.Pow(1+rate, period-1)
			balance := pv*power + pmt*(1+rate*kind)*(power-1)/rate
			interest = -balance * rate
			if kind == 1 {
				interest /= 1 + rate
			}
		}
		if n.op == "IPMT" {
			return floatingResult(e, interest)
		}
		return floatingResult(e, pmt-interest)
	}
	periods, amount, fv, kind := a[1], a[2], arg(3, 0), arg(4, 0)
	if periods <= 0 || kind != 0 && kind != 1 {
		return FormulaValue{}, errors.New("#NUM! 期间或支付类型无效")
	}
	if n.op == "PMT" {
		return floatingResult(e, annuityPayment(rate, periods, amount, fv, kind))
	}
	if rate == 0 {
		if n.op == "FV" {
			return floatingResult(e, -(fv + amount*periods))
		}
		return floatingResult(e, -fv-amount*periods)
	}
	power := math.Pow(1+rate, periods)
	if n.op == "FV" {
		return floatingResult(e, -(fv*power + amount*(1+rate*kind)*(power-1)/rate))
	}
	return floatingResult(e, -(fv+amount*(1+rate*kind)*(power-1)/rate)/power)
}
