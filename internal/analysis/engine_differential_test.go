package analysis

import (
	"context"
	"errors"
	"math/big"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRollupDistinctBudgetIncludesAllTotals(t *testing.T) {
	q := Query{Source: "wb:limit", Rows: []string{"C1"}, Columns: []string{"C2"}, Value: "C3", Aggregate: "distinct"}
	visited := 0
	scan := func(ctx context.Context, _ []string, visit func(Record) error) error {
		record := Record{Values: map[string]Value{"C1": {Text: "甲"}, "C2": {Text: "乙"}}}
		// The bucket, row total, column total and grand total each need the set.
		for i := range MaxDistinct/4 + 200 {
			visited++
			record.Number = i + 1
			record.Values["C3"] = Value{Text: strconv.Itoa(i)}
			if err := visit(record); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if _, err := Run(t.Context(), q, sampleFields(), scan, nil); !errors.Is(err, errDistinctLimit) {
		t.Fatalf("distinct state budget: got %v want %v", err, errDistinctLimit)
	}
	if visited != MaxDistinct/4+1 {
		t.Fatalf("read %d records before enforcing the limit", visited)
	}
}

func TestDecimalCoefficientAndScaleAgainstBigRat(t *testing.T) {
	inputs := []string{"0", "+0", "-0.", ".001", "9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "18446744073709551615", "000001.00", "9007199254740993.125",
		"-1e308", "1e-308", "1/7", "1/-3"}
	rng := rand.New(rand.NewSource(27))
	for range 1500 {
		n := strconv.FormatUint(rng.Uint64(), 10)
		pos := rng.Intn(len(n) + 1)
		inputs = append(inputs, "-"+n[:pos]+"."+n[pos:]+"e"+strconv.Itoa(rng.Intn(49)-24))
	}
	for _, s := range inputs {
		got, ok := Decimal(s)
		// big.Rat's fraction syntax rejects a signed denominator; division is
		// explicit here to match the existing ExcelLens rational grammar.
		var ref *big.Rat
		if a, b, found := strings.Cut(s, "/"); found {
			n, _ := new(big.Int).SetString(a, 10)
			d, _ := new(big.Int).SetString(b, 10)
			ref = new(big.Rat).SetFrac(n, d)
		} else {
			var valid bool
			ref, valid = new(big.Rat).SetString(s)
			if !valid {
				t.Fatal("reference", s)
			}
		}
		if !ok || got.Cmp(ref) != 0 {
			t.Fatalf("%s got %v want %v", s, got, ref)
		}
	}
	for _, s := range []string{"", ".", "+", "-e2", "1.2.3", "NaN", "1e309", "1e-309", "1/0", "2_0", "12元", strings.Repeat("0", 513)} {
		if _, ok := Decimal(s); ok {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestPivotRollupMatchesRecordAtATime(t *testing.T) {
	metrics := []Metric{{Field: "C3", Aggregate: "sum"}, {Field: "C3", Aggregate: "average"}, {Field: "C3", Aggregate: "min"},
		{Field: "C3", Aggregate: "max"}, {Field: "C3", Aggregate: "count"}, {Field: "C3", Aggregate: "distinct"}}
	q := Query{Source: "wb:rollup", Rows: []string{"C1"}, Columns: []string{"C2"}, Metrics: metrics, Precision: 6}
	plan := compileMetrics(metrics)
	type key struct{ row, col string }
	// "*" is used only as the key of the independent scalar totals.
	scalar := make(map[key]*accumulator)
	mem := new(queryMemory)
	used := 0
	values := []Value{{Text: "9007199254740993.125", Numeric: true}, {Text: "1/3", Numeric: true},
		{Text: "-12e-7", Numeric: true}, {Text: "(1,234.50)", Numeric: true}, {Text: "-9007199254740993.125", Numeric: true},
		{Text: "000123"}, {Text: "12.3", MissingCache: true}, {Text: "1/7", Numeric: true}, {Text: "bad", Numeric: true}, {}}
	var rows []Record
	rng := rand.New(rand.NewSource(20261128))
	for i := range 2500 {
		r, c := strconv.Itoa(rng.Intn(7)), strconv.Itoa(rng.Intn(5))
		v := values[rng.Intn(len(values))]
		record := Record{Number: 2*i + 1, Values: map[string]Value{"C1": {Text: r}, "C2": {Text: c}, "C3": v}}
		rows = append(rows, record)
		var input [1]metricInput
		plan.prepare(record.Values, input[:])
		for _, k := range []key{{r, c}, {r, "*"}, {"*", c}, {"*", "*"}} {
			a := scalar[k]
			if a == nil {
				a = new(accumulator)
				scalar[k] = a
			}
			if err := a.add(input[0], plan.fields[0].needs, &used, mem); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(k key, cells []Cell) {
		t.Helper()
		if len(cells) != len(metrics) {
			t.Fatal(k, len(cells))
		}
		for i, m := range metrics {
			want := scalar[k].result(m.Aggregate, q.Precision)
			if !reflect.DeepEqual(cells[i], want) {
				t.Fatalf("%v %s: got %+v want %+v", k, m.Aggregate, cells[i], want)
			}
		}
	}
	check(key{"*", "*"}, got.MetricGrand)
	for j, c := range got.ColumnKeys {
		check(key{"*", c}, got.MetricColumnTotals[j])
	}
	for _, r := range got.Rows {
		check(key{r.Keys[0], "*"}, r.MetricTotals)
		for j, c := range got.ColumnKeys {
			check(key{r.Keys[0], c}, r.MetricValues[j])
		}
	}
}
