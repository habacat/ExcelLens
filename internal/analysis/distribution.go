package analysis

import (
	"errors"
	"math/big"
)

type DistributionQuantile struct {
	Percent int    `json:"percent"`
	Value   string `json:"value"`
}

type DistributionBin struct {
	Lower          string `json:"lower"`
	Upper          string `json:"upper"`
	UpperInclusive bool   `json:"upperInclusive"`
	Count          int64  `json:"count"`
}

type NumericDistribution struct {
	Method     string                 `json:"method"`
	Definition string                 `json:"definition"`
	Count      int64                  `json:"count"`
	Quantiles  []DistributionQuantile `json:"quantiles"`
	Bins       []DistributionBin      `json:"bins"`
}

type distributionRank struct {
	percent      int
	lower, upper int64
	fraction     *big.Rat
	lo, hi       *big.Rat
}

// SortedDistribution retains only fixed percentile boundaries and bins. It
// consumes the whole exact sorted stream, never an implicit sample or sketch.
type SortedDistribution struct {
	count, seen int64
	min, max    *big.Rat
	span        *big.Rat
	previous    *big.Rat
	ranks       []distributionRank
	bins        []DistributionBin
}

func NewSortedDistribution(count int64, minimum, maximum *big.Rat, bins int) (*SortedDistribution, error) {
	if count < 0 || bins < 1 || bins > 100 || count > 0 && (minimum == nil || maximum == nil || minimum.Cmp(maximum) > 0) {
		return nil, errors.New("数值分布参数无效")
	}
	d := &SortedDistribution{count: count, bins: []DistributionBin{}}
	if count == 0 {
		return d, nil
	}
	d.min, d.max = new(big.Rat).Set(minimum), new(big.Rat).Set(maximum)
	d.span = new(big.Rat).Sub(d.max, d.min)
	if d.span.Sign() == 0 {
		bins = 1
	}
	for at := 0; at < bins; at++ {
		lower := new(big.Rat).Add(d.min, new(big.Rat).Mul(d.span, big.NewRat(int64(at), int64(bins))))
		upper := new(big.Rat).Add(d.min, new(big.Rat).Mul(d.span, big.NewRat(int64(at+1), int64(bins))))
		d.bins = append(d.bins, DistributionBin{Lower: decimalExact(lower), Upper: decimalExact(upper), UpperInclusive: at == bins-1})
	}
	for _, percent := range []int{5, 25, 50, 75, 95} {
		lo, hi, fraction, err := QuantileRanks(count, big.NewRat(int64(percent), 100), false)
		if err != nil {
			return nil, err
		}
		d.ranks = append(d.ranks, distributionRank{percent: percent, lower: lo, upper: hi, fraction: fraction})
	}
	return d, nil
}

func (d *SortedDistribution) AddSorted(value *big.Rat) error {
	if d.seen >= d.count || value == nil || value.Cmp(d.min) < 0 || value.Cmp(d.max) > 0 || d.previous != nil && d.previous.Cmp(value) > 0 || d.seen == 0 && value.Cmp(d.min) != 0 {
		return errors.New("数值分布的排序、数量或边界不完整")
	}
	for at := range d.ranks {
		rank := &d.ranks[at]
		if d.seen == rank.lower {
			rank.lo = new(big.Rat).Set(value)
		}
		if d.seen == rank.upper {
			rank.hi = new(big.Rat).Set(value)
		}
	}
	bin := 0
	if d.span.Sign() != 0 {
		position := new(big.Rat).Sub(value, d.min)
		position.Quo(position, d.span).Mul(position, new(big.Rat).SetInt64(int64(len(d.bins))))
		bin = int(new(big.Int).Quo(position.Num(), position.Denom()).Int64())
		bin = min(bin, len(d.bins)-1)
	}
	d.bins[bin].Count++
	if d.previous == nil {
		d.previous = new(big.Rat)
	}
	d.previous.Set(value)
	d.seen++
	return nil
}

func (d *SortedDistribution) Finish() (*NumericDistribution, error) {
	if d.seen != d.count || d.count > 0 && d.previous.Cmp(d.max) != 0 {
		return nil, errors.New("数值分布的数列不完整")
	}
	result := &NumericDistribution{Method: "exact", Definition: "inclusive-linear", Count: d.count, Quantiles: []DistributionQuantile{}, Bins: append([]DistributionBin{}, d.bins...)}
	for _, rank := range d.ranks {
		if rank.lo == nil || rank.hi == nil {
			return nil, errors.New("数值分布缺少精确分位边界")
		}
		result.Quantiles = append(result.Quantiles, DistributionQuantile{Percent: rank.percent, Value: decimalExact(InterpolateQuantile(rank.lo, rank.hi, rank.fraction))})
	}
	return result, nil
}
