package analysis

import (
	"math"
	"math/big"
	"testing"
)

func TestQuantilesExactInclusiveAndExclusiveRanksAndInterpolation(t *testing.T) {
	values := []*big.Rat{big.NewRat(-100, 1), big.NewRat(0, 1), big.NewRat(0, 1), big.NewRat(1, 1), big.NewRat(3, 1), big.NewRat(17, 1)}
	for _, tc := range []struct {
		percent   int64
		exclusive bool
		want      string
	}{{5, false, "-75"}, {25, false, "0"}, {50, false, "0.5"}, {75, false, "2.5"}, {95, false, "13.5"}, {25, true, "-25"}, {50, true, "0.5"}, {75, true, "6.5"}} {
		lo, hi, f, err := QuantileRanks(int64(len(values)), big.NewRat(tc.percent, 100), tc.exclusive)
		if err != nil {
			t.Fatal(err)
		}
		beforeLo, beforeHi := values[lo].RatString(), values[hi].RatString()
		result := InterpolateQuantile(values[lo], values[hi], f)
		if ExactDecimalString(result) != tc.want || values[lo].RatString() != beforeLo || values[hi].RatString() != beforeHi {
			t.Fatal(tc, lo, hi, f, result)
		}
	}
	for n := int64(1); n <= 137; n++ {
		for p := int64(0); p <= 100; p++ {
			lo, hi, f, err := QuantileRanks(n, big.NewRat(p, 100), false)
			if err != nil {
				t.Fatal(err)
			}
			// Independent integer arithmetic for an inclusive linear percentile.
			lower, rem := (n-1)*p/100, (n-1)*p%100
			upper := lower
			if rem != 0 {
				upper++
			}
			if lo != lower || hi != upper || f.Cmp(big.NewRat(rem, 100)) != 0 {
				t.Fatal(n, p, lo, hi, f)
			}
		}
	}
}

func TestQuantilesRejectEmptyInvalidAndUnsupportedExclusiveEndpoints(t *testing.T) {
	for _, tc := range []struct {
		count     int64
		p         *big.Rat
		exclusive bool
	}{{0, big.NewRat(1, 2), false}, {-1, big.NewRat(1, 2), false}, {1, nil, false}, {1, big.NewRat(-1, 100), false}, {2, big.NewRat(101, 100), false}, {6, big.NewRat(0, 1), true}, {6, big.NewRat(1, 1), true}, {6, big.NewRat(95, 100), true}} {
		if _, _, _, err := QuantileRanks(tc.count, tc.p, tc.exclusive); err == nil {
			t.Fatal(tc)
		}
	}
	lo, hi, f, err := QuantileRanks(math.MaxInt64, big.NewRat(1, 2), true)
	if err != nil || lo != (math.MaxInt64-1)/2 || hi != lo || f.Sign() != 0 {
		t.Fatal("large count overflow", lo, hi, f, err)
	}
	for _, exclusive := range []bool{false, true} {
		lo, hi, f, err = QuantileRanks(1, big.NewRat(1, 2), exclusive)
		if err != nil || lo != 0 || hi != 0 || f.Sign() != 0 {
			t.Fatal(lo, hi, f, err)
		}
	}
}
