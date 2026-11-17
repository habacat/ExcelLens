package calculator

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"

	"excellens/internal/analysis"
)

func registerStatFunctions(add functionRegistrar) {
	add("PRODUCT,SUMSQ,MEDIAN,MODE,MODE.SNGL,AVEDEV,DEVSQ,STDEV,STDEV.S,STDEVP,STDEV.P,VAR,VAR.S,VARP,VAR.P,GEOMEAN,HARMEAN", "数值1, …", 1, 128, statFunction)
	add("LARGE,SMALL,PERCENTILE,PERCENTILE.INC,PERCENTILE.EXC,QUARTILE,QUARTILE.INC,QUARTILE.EXC", "区域, 参数", 2, 2, statFunction)
	add("RANK,RANK.EQ,RANK.AVG", "数值, 区域, [顺序]", 2, 3, statFunction)
	add("SUMPRODUCT", "区域1, [区域2], …", 1, 128, sumProductFunction)
	add("NORM.DIST", "数值, 均值, 标准差, 累计", 4, 4, distributionFunction)
	add("NORM.INV", "概率, 均值, 标准差", 3, 3, distributionFunction)
	add("NORM.S.DIST", "数值, 累计", 2, 2, distributionFunction)
	add("NORM.S.INV", "概率", 1, 1, distributionFunction)
	add("CORREL,PEARSON,RSQ,SLOPE,INTERCEPT,STEYX,COVARIANCE.P,COVARIANCE.S,COVAR,SUMX2MY2,SUMX2PY2,SUMXMY2", "数组1, 数组2", 2, 2, pairedStatFunction)
	add("FORECAST,FORECAST.LINEAR", "预测点, 已知Y, 已知X", 3, 3, pairedStatFunction)
}
func collectNumbers(e *formulaEval, args []*formulaNode) ([]*big.Rat, error) {
	out := []*big.Rat{}
	for _, arg := range args {
		v, err := rangeArg(e, arg)
		if err != nil {
			return nil, err
		}
		err = visitSparse(e, v, func(_ int, x FormulaValue) error {
			if err := valueFault(x); err != nil {
				return err
			}
			number, ok := dataNumber(x)
			if !ok && v.Kind != "range" {
				number, err = formulaNumber(x)
				ok = err == nil
			}
			if ok {
				if len(out) >= maxArrayCells {
					return errors.New("#NUM! 统计数组超过20万格")
				}
				out = append(out, new(big.Rat).Set(number))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func statFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	args := node.args
	if strings.HasPrefix(node.op, "RANK") {
		args = node.args[1:2]
	} else if node.op == "LARGE" || node.op == "SMALL" || strings.HasPrefix(node.op, "PERCENTILE") || strings.HasPrefix(node.op, "QUARTILE") {
		args = node.args[:1]
	}
	nums, err := collectNumbers(e, args)
	if err != nil {
		return FormulaValue{}, err
	}
	count := len(nums)
	if count == 0 {
		if node.op == "PRODUCT" || node.op == "SUMSQ" {
			return integerResult(0)
		}
		return FormulaValue{}, errors.New("#NUM! 没有可用数值")
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i].Cmp(nums[j]) < 0 })
	sum := new(big.Rat)
	for _, n := range nums {
		sum.Add(sum, n)
	}
	mean := new(big.Rat).Quo(sum, big.NewRat(int64(count), 1))
	switch node.op {
	case "PRODUCT":
		r := big.NewRat(1, 1)
		for _, n := range nums {
			r.Mul(r, n)
			if err = bounded(r); err != nil {
				return FormulaValue{}, err
			}
		}
		return numberResult(r)
	case "SUMSQ":
		r := new(big.Rat)
		for _, n := range nums {
			r.Add(r, new(big.Rat).Mul(n, n))
		}
		return numberResult(r)
	case "MEDIAN":
		if count%2 == 1 {
			return numberResult(nums[count/2])
		}
		return numberResult(new(big.Rat).Quo(new(big.Rat).Add(nums[count/2-1], nums[count/2]), big.NewRat(2, 1)))
	case "LARGE", "SMALL":
		v, err := e.eval(node.args[1])
		if err != nil {
			return FormulaValue{}, err
		}
		k, err := formulaInteger(v)
		if err != nil || k < 1 || k > int64(count) {
			return FormulaValue{}, errors.New("#NUM! 排名超界")
		}
		i := int(k) - 1
		if node.op == "LARGE" {
			i = count - int(k)
		}
		return numberResult(nums[i])
	case "MODE", "MODE.SNGL":
		best, run := 1, 1
		var value *big.Rat
		for i := 1; i < count; i++ {
			if nums[i].Cmp(nums[i-1]) == 0 {
				run++
			} else {
				run = 1
			}
			if run > best {
				best = run
				value = nums[i]
			}
		}
		if value == nil {
			return FormulaValue{}, errors.New("#N/A 没有重复数值")
		}
		return numberResult(value)
	case "GEOMEAN", "HARMEAN":
		total := 0.0
		for _, n := range nums {
			if n.Sign() <= 0 {
				return FormulaValue{}, errors.New("#NUM! 需要正数")
			}
			f, _ := n.Float64()
			if node.op == "GEOMEAN" {
				total += math.Log(f)
			} else {
				total += 1 / f
			}
		}
		if node.op == "GEOMEAN" {
			return floatingResult(e, math.Exp(total/float64(count)))
		}
		return floatingResult(e, float64(count)/total)
	}
	if strings.HasPrefix(node.op, "RANK") {
		v, err := e.eval(node.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		number, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		ascending := false
		if len(node.args) > 2 {
			v, err = e.eval(node.args[2])
			if err != nil {
				return FormulaValue{}, err
			}
			order, err := formulaNumber(v)
			if err != nil {
				return FormulaValue{}, err
			}
			ascending = order.Sign() != 0
		}
		before, equal := 0, 0
		for _, n := range nums {
			cmp := n.Cmp(number)
			if cmp == 0 {
				equal++
			} else if ascending && cmp < 0 || !ascending && cmp > 0 {
				before++
			}
		}
		if equal == 0 {
			return FormulaValue{}, errors.New("#N/A 数值不在区域中")
		}
		rank := big.NewRat(int64(before+1), 1)
		if node.op == "RANK.AVG" {
			rank.Add(rank, big.NewRat(int64(equal-1), 2))
		}
		return numberResult(rank)
	}
	if strings.HasPrefix(node.op, "PERCENTILE") || strings.HasPrefix(node.op, "QUARTILE") {
		v, err := e.eval(node.args[1])
		if err != nil {
			return FormulaValue{}, err
		}
		p, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		if strings.HasPrefix(node.op, "QUARTILE") {
			if !p.IsInt() {
				p = new(big.Rat).SetInt(truncInteger(p))
			}
			p.Quo(p, big.NewRat(4, 1))
		}
		if p.Sign() < 0 || p.Cmp(big.NewRat(1, 1)) > 0 {
			return FormulaValue{}, errors.New("#NUM! 分位参数越界")
		}
		lower, upper, fraction, rankErr := analysis.QuantileRanks(int64(count), p, strings.HasSuffix(node.op, ".EXC"))
		if rankErr != nil {
			return FormulaValue{}, errors.New("#NUM! 样本不足以计算分位数")
		}
		return numberResult(analysis.InterpolateQuantile(nums[lower], nums[upper], fraction))
	}
	squares, absolute := new(big.Rat), new(big.Rat)
	for _, n := range nums {
		delta := new(big.Rat).Sub(n, mean)
		squares.Add(squares, new(big.Rat).Mul(delta, delta))
		absolute.Add(absolute, new(big.Rat).Abs(delta))
	}
	if node.op == "AVEDEV" {
		return numberResult(absolute.Quo(absolute, big.NewRat(int64(count), 1)))
	}
	if node.op == "DEVSQ" {
		return numberResult(squares)
	}
	sample := node.op == "VAR" || node.op == "VAR.S" || node.op == "STDEV" || node.op == "STDEV.S"
	divisor := count
	if sample {
		divisor--
	}
	if divisor < 1 {
		return FormulaValue{}, errors.New("#DIV/0! 样本数量不足")
	}
	variance := squares.Quo(squares, big.NewRat(int64(divisor), 1))
	if strings.HasPrefix(node.op, "VAR") {
		return numberResult(variance)
	}
	f, _ := variance.Float64()
	return floatingResult(e, math.Sqrt(f))
}
func sumProductFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	var products []*big.Rat
	rows, cols := 0, 0
	for _, arg := range node.args {
		v, err := rangeArg(e, arg)
		if err != nil {
			return FormulaValue{}, err
		}
		values, nr, nc, err := materialize(e, v, maxArrayCells)
		if err != nil {
			return FormulaValue{}, err
		}
		if products == nil {
			rows, cols = nr, nc
			products = make([]*big.Rat, len(values))
			for i := range products {
				products[i] = big.NewRat(1, 1)
			}
		} else if nr != rows || nc != cols {
			return FormulaValue{}, errors.New("#VALUE! SUMPRODUCT 区域尺寸不一致")
		}
		for i, x := range values {
			if err := valueFault(x); err != nil {
				return FormulaValue{}, err
			}
			if number, ok := dataNumber(x); ok {
				products[i].Mul(products[i], number)
			} else {
				products[i].SetInt64(0)
			}
			if err = bounded(products[i]); err != nil {
				return FormulaValue{}, err
			}
		}
	}
	sum := new(big.Rat)
	for _, n := range products {
		sum.Add(sum, n)
	}
	return numberResult(sum)
}
func distributionFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	v, err := evaluatedArgs(e, node)
	if err != nil {
		return FormulaValue{}, err
	}
	x, err := formulaNumber(v[0])
	if err != nil {
		return FormulaValue{}, err
	}
	xf, _ := x.Float64()
	mean, sd := 0.0, 1.0
	standard := strings.HasPrefix(node.op, "NORM.S.")
	if !standard {
		m, err := formulaNumber(v[1])
		if err != nil {
			return FormulaValue{}, err
		}
		s, err := formulaNumber(v[2])
		if err != nil {
			return FormulaValue{}, err
		}
		mean, _ = m.Float64()
		sd, _ = s.Float64()
		if sd <= 0 {
			return FormulaValue{}, errors.New("#NUM! 标准差必须为正")
		}
	}
	if strings.HasSuffix(node.op, "INV") {
		if xf <= 0 || xf >= 1 {
			return FormulaValue{}, errors.New("#NUM! 概率必须在0和1之间")
		}
		return floatingResult(e, mean+sd*math.Sqrt2*math.Erfinv(2*xf-1))
	}
	cumulative, err := formulaBool(v[len(v)-1])
	if err != nil {
		return FormulaValue{}, err
	}
	z := (xf - mean) / sd
	if cumulative {
		return floatingResult(e, .5*(1+math.Erf(z/math.Sqrt2)))
	}
	return floatingResult(e, math.Exp(-z*z/2)/(sd*math.Sqrt(2*math.Pi)))
}
