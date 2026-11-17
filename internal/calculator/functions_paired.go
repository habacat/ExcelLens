package calculator

import (
	"errors"
	"math"
	"math/big"
)

func pairedStatFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	start := 0
	if node.op == "FORECAST" || node.op == "FORECAST.LINEAR" {
		start = 1
	}
	arrays := [2][]FormulaValue{}
	for i := range arrays {
		v, err := rangeArg(e, node.args[start+i])
		if err != nil {
			return FormulaValue{}, err
		}
		arrays[i], _, _, err = materialize(e, v, maxArrayCells)
		if err != nil {
			return FormulaValue{}, err
		}
	}
	if len(arrays[0]) != len(arrays[1]) {
		return FormulaValue{}, errors.New("#N/A 两个数组的长度不同")
	}
	// Keep pairs aligned before discarding text, logical values and blanks.
	// The first argument is Y for regression functions, X otherwise.
	type pair struct{ x, y *big.Rat }
	pairs := make([]pair, 0, len(arrays[0]))
	sx, sy := new(big.Rat), new(big.Rat)
	sumDifferences := new(big.Rat)
	for i, first := range arrays[0] {
		if err := e.ctx.Err(); err != nil {
			return FormulaValue{}, err
		}
		second := arrays[1][i]
		if err := valueFault(first); err != nil {
			return FormulaValue{}, err
		}
		if err := valueFault(second); err != nil {
			return FormulaValue{}, err
		}
		y, okY := dataNumber(first)
		x, okX := dataNumber(second)
		if !okX || !okY {
			continue
		}
		pairs = append(pairs, pair{x, y})
		sx.Add(sx, x)
		sy.Add(sy, y)
		var term *big.Rat
		switch node.op {
		case "SUMX2MY2":
			term = new(big.Rat).Sub(new(big.Rat).Mul(y, y), new(big.Rat).Mul(x, x))
		case "SUMX2PY2":
			term = new(big.Rat).Add(new(big.Rat).Mul(y, y), new(big.Rat).Mul(x, x))
		case "SUMXMY2":
			term = new(big.Rat).Sub(y, x)
			term.Mul(term, term)
		}
		if term != nil {
			sumDifferences.Add(sumDifferences, term)
			if err := bounded(sumDifferences); err != nil {
				return FormulaValue{}, err
			}
		}
	}
	if node.op == "SUMX2MY2" || node.op == "SUMX2PY2" || node.op == "SUMXMY2" {
		return numberResult(sumDifferences)
	}
	count := int64(len(pairs))
	if count == 0 {
		return FormulaValue{}, errors.New("#DIV/0! 没有成对数值")
	}
	mx := new(big.Rat).Quo(sx, big.NewRat(count, 1))
	my := new(big.Rat).Quo(sy, big.NewRat(count, 1))
	sxx, syy, sxy := new(big.Rat), new(big.Rat), new(big.Rat)
	for _, p := range pairs {
		if err := e.ctx.Err(); err != nil {
			return FormulaValue{}, err
		}
		dx, dy := new(big.Rat).Sub(p.x, mx), new(big.Rat).Sub(p.y, my)
		sxx.Add(sxx, new(big.Rat).Mul(dx, dx))
		syy.Add(syy, new(big.Rat).Mul(dy, dy))
		sxy.Add(sxy, new(big.Rat).Mul(dx, dy))
		for _, value := range []*big.Rat{sxx, syy, sxy} {
			if err := bounded(value); err != nil {
				return FormulaValue{}, err
			}
		}
	}
	switch node.op {
	case "COVAR", "COVARIANCE.P", "COVARIANCE.S":
		if node.op == "COVARIANCE.S" {
			count--
		}
		if count == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 成对数值不足")
		}
		return numberResult(new(big.Rat).Quo(sxy, big.NewRat(count, 1)))
	case "CORREL", "PEARSON", "RSQ":
		if sxx.Sign() == 0 || syy.Sign() == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 数组方差为0")
		}
		rSquared := new(big.Rat).Quo(new(big.Rat).Mul(sxy, sxy), new(big.Rat).Mul(sxx, syy))
		if node.op == "RSQ" {
			return numberResult(rSquared)
		}
		r, _ := rSquared.Float64()
		return floatingResult(e, math.Copysign(math.Sqrt(r), float64(sxy.Sign())))
	}
	if sxx.Sign() == 0 {
		return FormulaValue{}, errors.New("#DIV/0! 已知X的方差为0")
	}
	slope := new(big.Rat).Quo(sxy, sxx)
	intercept := new(big.Rat).Sub(my, new(big.Rat).Mul(slope, mx))
	switch node.op {
	case "SLOPE":
		return numberResult(slope)
	case "INTERCEPT":
		return numberResult(intercept)
	case "FORECAST", "FORECAST.LINEAR":
		v, err := e.eval(node.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		x, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		return numberResult(new(big.Rat).Add(intercept, new(big.Rat).Mul(slope, x)))
	case "STEYX":
		if count <= 2 {
			return FormulaValue{}, errors.New("#DIV/0! 至少需要三组成对数值")
		}
		residual := new(big.Rat).Sub(syy, new(big.Rat).Mul(slope, sxy))
		residual.Quo(residual, big.NewRat(count-2, 1))
		f, _ := residual.Float64()
		return floatingResult(e, math.Sqrt(f))
	}
	return FormulaValue{}, errors.New("#NAME? 未注册成对统计函数")
}
