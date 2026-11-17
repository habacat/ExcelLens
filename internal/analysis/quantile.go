package analysis

import (
	"errors"
	"math/big"
)

// QuantileRanks uses zero-based positions and retains exact interpolation.
// It is shared by in-memory formula arrays and streaming sorted profiles.
func QuantileRanks(count int64, probability *big.Rat, exclusive bool) (lower, upper int64, fraction *big.Rat, err error) {
	if count < 1 || probability == nil || probability.Sign() < 0 || probability.Cmp(big.NewRat(1, 1)) > 0 {
		return 0, 0, nil, errors.New("分位数参数无效或没有数值")
	}
	span := new(big.Int).SetInt64(count)
	if exclusive {
		span.Add(span, big.NewInt(1))
	} else {
		span.Sub(span, big.NewInt(1))
	}
	position := new(big.Rat).Mul(probability, new(big.Rat).SetInt(span))
	if exclusive {
		position.Sub(position, big.NewRat(1, 1))
	}
	if position.Sign() < 0 || position.Cmp(new(big.Rat).SetInt64(count-1)) > 0 {
		return 0, 0, nil, errors.New("样本不足以计算该分位数")
	}
	lower = new(big.Int).Quo(position.Num(), position.Denom()).Int64()
	fraction = new(big.Rat).Sub(position, new(big.Rat).SetInt64(lower))
	upper = lower
	if fraction.Sign() != 0 {
		upper++
	}
	return lower, upper, fraction, nil
}

// InterpolateQuantile never mutates the caller's sorted boundary values.
func InterpolateQuantile(lower, upper, fraction *big.Rat) *big.Rat {
	delta := new(big.Rat).Sub(upper, lower)
	delta.Mul(delta, fraction)
	return delta.Add(lower, delta)
}
