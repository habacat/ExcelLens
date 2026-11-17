package analysis

import (
	"math/big"
	"math/rand"
	"sort"
	"testing"
)

func TestSortedDistributionMatchesIndependentFullDataQuantilesAndExactBins(t *testing.T) {
	rng := rand.New(rand.NewSource(20261116))
	for sample := 0; sample < 80; sample++ {
		count := 1 + rng.Intn(401)
		values := make([]*big.Rat, count)
		for at := range values {
			values[at] = big.NewRat(int64(rng.Intn(2001)-1000), 100)
			if sample%7 == 0 {
				values[at].Add(values[at], big.NewRat(9007199254740993, 1))
			}
		}
		sort.Slice(values, func(i, j int) bool { return values[i].Cmp(values[j]) < 0 })
		d, err := NewSortedDistribution(int64(count), values[0], values[count-1], 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if err := d.AddSorted(value); err != nil {
				t.Fatal(err)
			}
		}
		r, err := d.Finish()
		if err != nil || r.Method != "exact" || r.Count != int64(count) || len(r.Quantiles) != 5 {
			t.Fatal(r, err)
		}
		for _, q := range r.Quantiles {
			position := (count - 1) * q.Percent
			lo, rem := position/100, position%100
			hi := lo
			if rem != 0 {
				hi++
			}
			want := new(big.Rat).Add(new(big.Rat).Mul(values[lo], big.NewRat(int64(100-rem), 100)), new(big.Rat).Mul(values[hi], big.NewRat(int64(rem), 100)))
			got, ok := new(big.Rat).SetString(q.Value)
			if !ok || got.Cmp(want) != 0 {
				t.Fatal("quantile mismatch", sample, q, want)
			}
		}
		var total int64
		for at, bin := range r.Bins {
			lower, _ := new(big.Rat).SetString(bin.Lower)
			upper, _ := new(big.Rat).SetString(bin.Upper)
			want := int64(0)
			for _, value := range values {
				if value.Cmp(lower) >= 0 && (value.Cmp(upper) < 0 || bin.UpperInclusive && value.Cmp(upper) == 0) {
					want++
				}
			}
			if bin.Count != want || bin.UpperInclusive != (at == len(r.Bins)-1) {
				t.Fatal("histogram mismatch", sample, at, bin, want)
			}
			total += bin.Count
		}
		if total != int64(count) {
			t.Fatal("histogram lost rows", total, count)
		}
	}
}

func TestSortedDistributionEmptyConstantExactFractionsAndPartialFailure(t *testing.T) {
	d, err := NewSortedDistribution(0, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.Finish()
	if err != nil || r.Count != 0 || len(r.Quantiles) != 0 || len(r.Bins) != 0 {
		t.Fatal(r, err)
	}
	value := big.NewRat(1, 3)
	d, err = NewSortedDistribution(9, value, value, 10)
	if err != nil {
		t.Fatal(err)
	}
	for range 9 {
		if err := d.AddSorted(value); err != nil {
			t.Fatal(err)
		}
	}
	r, err = d.Finish()
	if err != nil || len(r.Bins) != 1 || r.Bins[0].Count != 9 || r.Bins[0].Lower != "1/3" || r.Bins[0].Upper != "1/3" {
		t.Fatal(r, err)
	}
	for _, q := range r.Quantiles {
		if q.Value != "1/3" {
			t.Fatal(q)
		}
	}
	if err := d.AddSorted(value); err == nil {
		t.Fatal("extra row accepted")
	}
	d, _ = NewSortedDistribution(2, big.NewRat(1, 1), big.NewRat(2, 1), 10)
	if err := d.AddSorted(big.NewRat(2, 1)); err == nil {
		t.Fatal("missing minimum accepted")
	}
	if _, err := d.Finish(); err == nil {
		t.Fatal("partial distribution published")
	}
	d, _ = NewSortedDistribution(3, big.NewRat(1, 1), big.NewRat(3, 1), 10)
	_ = d.AddSorted(big.NewRat(1, 1))
	_ = d.AddSorted(big.NewRat(3, 1))
	if err := d.AddSorted(big.NewRat(2, 1)); err == nil {
		t.Fatal("unsorted input accepted")
	}
}
