package excelformula

import "math/big"

func Round(r *big.Rat, digits int64, mode string) *big.Rat {
	scale := new(big.Rat)
	if digits >= 0 {
		scale.SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(digits), nil))
	} else {
		scale.SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(-digits), nil))
	}
	scaled := new(big.Rat).Mul(r, scale)
	neg := scaled.Sign() < 0
	abs := new(big.Rat).Abs(scaled)
	q, rem := new(big.Int).QuoRem(abs.Num(), abs.Denom(), new(big.Int))
	switch mode {
	case "half":
		// Excel rounds ties away from zero.
		twice := new(big.Int).Lsh(rem, 1)
		if twice.Cmp(abs.Denom()) >= 0 {
			q.Add(q, big.NewInt(1))
		}
	case "up":
		if rem.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		}
	case "down":
		// already truncated toward zero
	}
	if neg {
		q.Neg(q)
	}
	out := new(big.Rat).Quo(new(big.Rat).SetInt(q), scale)
	return out
}

// AddFloat reproduces Excel's ordered binary64 accumulation. Explicit audit
// calculations retain their exact rational accumulator.
func AddFloat(sum, term *big.Rat) bool {
	a, _ := sum.Float64()
	b, _ := term.Float64()
	return sum.SetFloat64(a+b) != nil
}
