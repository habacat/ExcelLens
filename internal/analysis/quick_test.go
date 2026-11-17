package analysis

import (
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

func TestQuickSeriesExactSegmentsMissingAndInputOwnership(t *testing.T) {
	s, err := NewQuickSeries(7, 3)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{"9007199254740993.31", "-9007199254740993.30", "", "-0.01", "1/3", "", "0"}
	rows := []int{19, 13, 11, 9, 7, 5, 2}
	for i, text := range values {
		value, _ := new(big.Rat).SetString(text)
		if err := s.Add(rows[i], value); err != nil {
			t.Fatal(err)
		}
		if value != nil {
			value.SetInt64(999) // accumulators must own their operands
		}
	}
	total, parts, err := s.Result()
	if err != nil || total.Count != 5 || total.Sum != "1/3" || total.Average != "1/15" || total.Minimum != "-9007199254740993.3" || total.MinimumRow != 13 || total.MaximumRow != 19 {
		t.Fatal(total, err)
	}
	if len(parts) != 3 || parts[0].First != 1 || parts[0].Last != 3 || parts[0].FirstRow != 19 || parts[0].LastRow != 11 || parts[0].Summary.Sum != "0.01" || parts[1].Summary.Sum != "97/300" || parts[2].Summary.Count != 1 {
		t.Fatal(parts)
	}
	parts[0].First = 99
	_, fresh, _ := s.Result()
	if fresh[0].First != 1 {
		t.Fatal("result mutated accumulator")
	}
}

func TestQuickSeriesCoverageAgainstIndependentRationals(t *testing.T) {
	rng := rand.New(rand.NewSource(20261116))
	for run := 0; run < 200; run++ {
		rows, segments := rng.Intn(500), rng.Intn(60)+1
		s, err := NewQuickSeries(rows, segments)
		if err != nil {
			t.Fatal(err)
		}
		values := make([]*big.Rat, rows)
		for i := range values {
			if rng.Intn(3) != 0 {
				values[i] = big.NewRat(int64(rng.Intn(20000)-10000), int64(rng.Intn(19)+1))
			}
			if err := s.Add(10000-i, values[i]); err != nil {
				t.Fatal(err)
			}
		}
		total, parts, err := s.Result()
		if err != nil {
			t.Fatal(err)
		}
		next, count, all := 1, int64(0), new(big.Rat)
		for _, part := range parts {
			if part.First != next || part.Last < part.First || part.FirstRow != 10001-part.First || part.LastRow != 10001-part.Last {
				t.Fatal("segment coverage", parts)
			}
			sum, n := new(big.Rat), int64(0)
			for _, value := range values[part.First-1 : part.Last] {
				if value != nil {
					sum.Add(sum, value)
					n++
				}
			}
			got, ok := new(big.Rat).SetString(part.Summary.Sum)
			if !ok || got.Cmp(sum) != 0 || n != part.Summary.Count {
				t.Fatal(part, sum, n)
			}
			if n == 0 && (part.Summary.Average != "" || part.Summary.Minimum != "" || part.Summary.MinimumRow != 0) {
				t.Fatal("empty segment became zero data", part)
			}
			all.Add(all, sum)
			count += n
			next = part.Last + 1
		}
		got, ok := new(big.Rat).SetString(total.Sum)
		if !ok || next != rows+1 || total.Count != count || got.Cmp(all) != 0 {
			t.Fatal(total, rows, next, all)
		}
	}
}

func TestQuickSeriesRejectsIncompleteAndExcessInput(t *testing.T) {
	for _, spec := range [][2]int{{-1, 1}, {400001, 1}, {1, 0}, {1, 61}} {
		if _, err := NewQuickSeries(spec[0], spec[1]); err == nil {
			t.Fatal("invalid capacity accepted", spec)
		}
	}
	s, _ := NewQuickSeries(1, 1)
	if _, _, err := s.Result(); err == nil {
		t.Fatal("partial result accepted")
	}
	if err := s.Add(0, nil); err == nil {
		t.Fatal("bad source row accepted")
	}
	if err := s.Add(2, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(3, nil); err == nil {
		t.Fatal("extra row accepted")
	}
	total, _, _ := s.Result()
	if total.Sum != "0" || total.Average != "" || total.Count != 0 {
		t.Fatal(total)
	}
}

func TestAccountingNumericValueMatchesExistingLiteralRules(t *testing.T) {
	for _, input := range []string{"0", "-0", "0001", "+01", " (01) ", "(0.01)", "1,234.56", "1,23", "0,123", "1e-308", "1e309", "1/3", "  ", "0.00000000000000001", "-9007199254740993.31"} {
		got, ok := AccountingNumericValue(input)
		want := false
		if n, valid := normalizeAccountingNumber(strings.TrimSpace(input)); valid {
			want = IsNumericLiteral(n)
		}
		if ok != want || (got != nil) != want {
			t.Fatal(input, got, ok, want)
		}
	}
}
