package analysis

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"
)

func comparisonRecords() []Record {
	var records []Record
	for i, item := range []struct{ row, col, amount, code string }{{"A", "X", "10", "001"}, {"A", "Y", "30", "001"}, {"B", "X", "20", "001"}, {"B", "X", "40", "002"}, {"B", "Y", "60", "002"}} {
		records = append(records, Record{Number: i*2 + 2, Values: map[string]Value{"C1": {Text: item.row}, "C2": {Text: item.col}, "C3": {Text: item.amount, Numeric: true}, "C4": {Text: item.code}}})
	}
	return records
}

func assertComparison(t *testing.T, cell Cell, exact string) {
	t.Helper()
	if cell.Comparison == nil || cell.Comparison.Exact != exact || cell.Comparison.Status != "" {
		t.Fatalf("comparison %+v, want %s; original %+v", cell.Comparison, exact, cell)
	}
}

func TestComparisonRatiosUseIndependentWholeScopeTotalsInOneScan(t *testing.T) {
	q := Query{Source: "wb:source", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_asc", Precision: 2,
		Metrics: []Metric{{Field: "C3", Aggregate: "average", Comparison: "percent_grand"}, {Field: "C4", Aggregate: "distinct", Comparison: "percent_row"}, {Field: "C3", Aggregate: "sum", Comparison: "percent_column"}}}
	passes := 0
	scan := func(ctx context.Context, needed []string, visit func(Record) error) error {
		passes++
		return testScan(comparisonRecords())(ctx, needed, visit)
	}
	r, err := Run(t.Context(), q, sampleFields(), scan, nil)
	if err != nil || passes != 1 {
		t.Fatal(r, passes, err)
	}
	// Raw average A=20, B=40, complete average=32, not 20+40 or (20+40)/2.
	assertComparison(t, r.Rows[0].MetricTotals[0], "0.625")
	assertComparison(t, r.Rows[0].MetricValues[0][0], "0.3125")
	assertComparison(t, r.Rows[1].MetricTotals[0], "1.25")
	assertComparison(t, r.Rows[0].MetricValues[0][1], "1")
	assertComparison(t, r.Rows[1].MetricValues[1][1], "0.5")
	assertComparison(t, r.Rows[0].MetricValues[0][2], "1/7")
	assertComparison(t, r.MetricColumnTotals[0][2], "1")
	assertComparison(t, r.MetricGrand[0], "1")
	if r.Rows[0].MetricTotals[0].Exact != "20" || r.MetricGrand[0].Exact != "32" || r.Rows[0].MetricTotals[0].Comparison.Display != "62.50%" || r.Rows[0].MetricValues[0][2].Comparison.Denominator != "70" {
		t.Fatal("original aggregate or exact comparison scope lost", r)
	}
	q.Top = 1
	limited, err := Run(t.Context(), q, sampleFields(), testScan(comparisonRecords()), nil)
	if err != nil || !limited.Truncated || len(limited.Rows) != 1 || !reflect.DeepEqual(limited.Rows[0], r.Rows[0]) || !reflect.DeepEqual(limited.MetricGrand, r.MetricGrand) {
		t.Fatal("Top-N changed the complete denominator", limited, err)
	}
}

func TestRunningAcrossRowsMergesWeightedAveragesSetsAndIndependentMeasures(t *testing.T) {
	q := Query{Source: "wb:source", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_asc", Precision: 4}
	for _, item := range []struct{ field, aggregate string }{{"C3", "sum"}, {"C3", "average"}, {"C3", "min"}, {"C3", "max"}, {"C4", "distinct"}, {"", "count"}} {
		q.Metrics = append(q.Metrics, Metric{Field: item.field, Aggregate: item.aggregate, Comparison: "running_total"})
	}
	r, err := Run(t.Context(), q, sampleFields(), testScan(comparisonRecords()), nil)
	if err != nil {
		t.Fatal(err)
	}
	for m, want := range []string{"160", "32", "10", "60", "2", "5"} {
		assertComparison(t, r.Rows[1].MetricTotals[m], want)
		assertComparison(t, r.MetricGrand[m], want)
	}
	assertComparison(t, r.Rows[1].MetricValues[0][1], "70/3")
	assertComparison(t, r.Rows[1].MetricValues[1][1], "45")
	assertComparison(t, r.Rows[1].MetricValues[0][4], "2")
	assertComparison(t, r.Rows[1].MetricValues[1][4], "2")
	if r.Rows[1].MetricTotals[1].Exact != "40" || r.Rows[1].MetricValues[0][1].Exact != "30" {
		t.Fatal("cumulative state mutated the original groups", r)
	}
	q.Metrics[1].Comparison = "running_percent"
	r, err = Run(t.Context(), q, sampleFields(), testScan(comparisonRecords()), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertComparison(t, r.Rows[0].MetricTotals[1], "0.625")
	assertComparison(t, r.Rows[1].MetricTotals[1], "1")
}

func TestRunningAcrossColumnsAndMixedAxesUseTheOriginalColumnOrder(t *testing.T) {
	q := Query{Source: "wb:source", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_desc", Precision: 2,
		Metrics: []Metric{{Field: "C3", Aggregate: "average", Comparison: "running_total", ComparisonAxis: "columns"}, {Field: "C4", Aggregate: "distinct", Comparison: "running_percent", ComparisonAxis: "columns"}, {Field: "C3", Aggregate: "sum", Comparison: "running_total"}}}
	r, err := Run(t.Context(), q, sampleFields(), testScan(comparisonRecords()), nil)
	if err != nil || r.Rows[0].Keys[0] != "B" || !reflect.DeepEqual(r.ColumnKeys, []string{"X", "Y"}) {
		t.Fatal(r, err)
	}
	assertComparison(t, r.Rows[0].MetricValues[0][0], "30")
	assertComparison(t, r.Rows[0].MetricValues[1][0], "40")
	assertComparison(t, r.Rows[0].MetricTotals[0], "40")
	assertComparison(t, r.MetricColumnTotals[1][0], "32")
	assertComparison(t, r.Rows[0].MetricValues[0][1], "1")
	assertComparison(t, r.Rows[1].MetricValues[1][2], "90")
	assertComparison(t, r.Rows[1].MetricTotals[2], "160")
}

func TestComparisonsRetainTypedBlanksErrorsZeroDenominatorsAndMissingCrossGroups(t *testing.T) {
	rows := []Record{
		{Number: 1, Values: map[string]Value{"C1": {Text: "A"}, "C2": {Text: "X"}, "C3": {Text: "5", Numeric: true}}},
		{Number: 2, Values: map[string]Value{"C1": {Text: "B"}, "C2": {Text: "X"}, "C3": {Text: "-5", Numeric: true}}},
		{Number: 3, Values: map[string]Value{"C1": {Text: "C"}, "C2": {Text: "Y"}, "C3": {Text: "9007199254740993"}}},
		{Number: 4, Values: map[string]Value{"C1": {Text: "D"}, "C2": {Text: "Y"}, "C3": {Text: "", MissingCache: true}}},
	}
	q := Query{Source: "wb:typed", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_asc", Metrics: []Metric{{Field: "C3", Aggregate: "sum", Comparison: "percent_grand"}, {Field: "C3", Aggregate: "sum", Comparison: "running_total"}}}
	r, err := Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil || r.InvalidNumbers != 1 || r.MissingFormulaCaches != 1 || r.Grand.Exact != "0" {
		t.Fatal(r, err)
	}
	if r.Rows[0].Total.Comparison.Status != "zero_denominator" || r.Rows[2].Total.Comparison.Status != "empty" || r.Rows[0].Values[1].Comparison.Status != "empty" || r.MetricColumnTotals[1][0].Comparison.Status != "empty" {
		t.Fatal("empty or zero state manufactured a valid percentage", r)
	}
	assertComparison(t, r.Rows[1].MetricValues[0][1], "0") // genuine cancelling numeric values
	if r.Rows[1].MetricValues[1][1].Comparison.Status != "empty" {
		t.Fatal("no numeric prefix fabricated a zero", r)
	}
}

func TestComparisonTreeScopeAndExactSortRemainDeterministic(t *testing.T) {
	rows := comparisonRecords()
	q := Query{Source: "wb:source", Rows: []string{"C1", "C2"}, Layout: "tree", Sort: "label_asc", Precision: 2, Top: 1, Metrics: []Metric{{Field: "C3", Aggregate: "sum", Comparison: "running_percent"}}}
	r, err := RunTreeLevel(t.Context(), q, sampleFields(), []string{"B"}, testScan(rows), nil)
	if err != nil || !r.Truncated || r.Grand.Exact != "120" || r.Tree == nil || r.Tree.Path[0] != "B" {
		t.Fatal(r, err)
	}
	assertComparison(t, r.Rows[0].Total, "0.5")
	assertComparison(t, r.Grand, "1")
	q.Layout, q.Rows, q.Top, q.Sort = "", []string{"C1"}, 0, "value_desc"
	q.Metrics[0].Aggregate = "average"
	rows = append(rows, Record{Number: 99, Values: map[string]Value{"C1": {Text: "AA-empty"}, "C3": {Text: "text"}}})
	for range 12 {
		r, err := Run(t.Context(), q, sampleFields(), testScan(rows), nil)
		if err != nil || r.Rows[0].Keys[0] != "B" || r.Rows[1].Keys[0] != "A" || r.Rows[2].Keys[0] != "AA-empty" {
			t.Fatal("missing aggregate introduced a non-transitive sort", r, err)
		}
		assertComparison(t, r.Rows[0].Total, "1.25") // 40 divided by whole-scope 32
	}
}

func TestComparisonRejectsInvalidSettingsBudgetAndCancellationWithoutPartialResult(t *testing.T) {
	for _, metric := range []Metric{{Aggregate: "count", Comparison: "unknown"}, {Aggregate: "count", ComparisonAxis: "rows"}, {Aggregate: "count", Comparison: "percent_grand", ComparisonAxis: "columns"}, {Aggregate: "count", Comparison: "running_total", ComparisonAxis: "invalid"}} {
		q := Query{Source: "wb:x", Metrics: []Metric{metric}}
		if _, err := Run(t.Context(), q, nil, testScan(nil), nil); err == nil {
			t.Fatal("invalid comparison accepted", metric)
		}
	}
	a := []accumulator{{distinct: map[string]struct{}{"key": {}}, count: 1}}
	for _, exhausted := range []bool{false, true} {
		memory, used, target := &queryMemory{}, MaxDistinct, make([]accumulator, 1)
		if exhausted {
			memory.used = 256 << 20
		}
		err := mergeMetrics(t.Context(), target, a, []metricNeeds{{count: true, distinct: true}}, &used, memory)
		if err == nil || len(target[0].distinct) != 0 {
			t.Fatal("over-budget prefix retained new keys", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	q := Query{Source: "wb:x", Metrics: []Metric{{Aggregate: "count", Comparison: "running_total"}}}
	scan := func(context.Context, []string, func(Record) error) error { cancel(); return nil }
	r, err := Run(ctx, q, nil, scan, nil)
	if !errors.Is(err, context.Canceled) || r.Grand.Comparison != nil || len(r.Rows) != 0 {
		t.Fatal("cancelled comparison published partial data", r, err)
	}
}

func TestComparisonExactFractionsAndLargeAmountsNeverUseDisplayRounding(t *testing.T) {
	rows := []Record{{Number: 1, Values: map[string]Value{"C1": {Text: "A"}, "C3": {Text: "9007199254740993.31", Numeric: true}}}, {Number: 2, Values: map[string]Value{"C1": {Text: "B"}, "C3": {Text: "1/3", Numeric: true}}}}
	q := Query{Source: "wb:x", Rows: []string{"C1"}, Sort: "label_asc", Precision: 0, Metrics: []Metric{{Field: "C3", Aggregate: "sum", Comparison: "percent_grand"}, {Field: "C3", Aggregate: "sum", Comparison: "running_total"}}}
	r, err := Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertComparison(t, r.Rows[1].MetricTotals[1], "2702159776422298093/300")
	want, _ := new(big.Rat).SetString("2702159776422297993/2702159776422298093")
	got, _ := new(big.Rat).SetString(r.Rows[0].Total.Comparison.Exact)
	if got == nil || got.Cmp(want) != 0 || r.Rows[0].Total.Comparison.Display != "100%" {
		t.Fatal("rounded displays were used to compute the exact ratio", r.Rows[0].Total)
	}
}
