package analysis

import (
	"context"
	"reflect"
	"testing"
)

func TestSharedFieldMeasuresKeepDifferentCountsAndExactValues(t *testing.T) {
	values := []Value{{Text: "9007199254740993", Numeric: true}, {Text: "0.25", Numeric: true}, {Text: "-9007199254740993", Numeric: true}, {Text: "1/3", Numeric: true}, {Text: "0001"}, {Text: "invalid", Numeric: true}, {Text: "12"}, {MissingCache: true}, {Numeric: true}, {Text: "failed", ConversionFailed: true}}
	rows := make([]Record, len(values))
	for i, value := range values {
		rows[i] = Record{Number: 2*i + 2, Values: map[string]Value{"C1": {Text: []string{"甲", "乙"}[i%2]}, "C2": {Text: []string{"一", "二"}[i%2]}, "C3": value}}
	}
	metrics := []Metric{{Field: "C3", Aggregate: "sum"}, {Field: "C3", Aggregate: "average"}, {Field: "C3", Aggregate: "min"}, {Field: "C3", Aggregate: "max"}, {Field: "C3", Aggregate: "count"}, {Field: "C3", Aggregate: "distinct"}}
	want := []Cell{{Exact: "7/12", Count: 4}, {Exact: "7/48", Count: 4}, {Exact: "-9007199254740993", Count: 4}, {Exact: "9007199254740993", Count: 4}, {Exact: "8", Count: 8}, {Exact: "8", Count: 8}}
	for _, reverse := range []bool{false, true} {
		q := Query{Source: "wb:test", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_asc", Precision: 4, Metrics: metrics}
		if reverse {
			q.Metrics = append([]Metric(nil), metrics...)
			for i, j := 0, len(metrics)-1; i < j; i, j = i+1, j-1 {
				q.Metrics[i], q.Metrics[j] = q.Metrics[j], q.Metrics[i]
			}
		}
		got, err := Run(context.Background(), q, sampleFields(), testScan(rows), nil)
		if err != nil || got.InvalidNumbers != 4 || got.MissingFormulaCaches != 1 {
			t.Fatalf("diagnostics: %+v %v", got, err)
		}
		for i, metric := range q.Metrics {
			gold := i
			if reverse {
				gold = len(metrics) - 1 - i
			}
			if got.MetricGrand[i].Exact != want[gold].Exact || got.MetricGrand[i].Count != want[gold].Count {
				t.Fatalf("%s exact/count: %+v want %+v", metric.Aggregate, got.MetricGrand[i], want[gold])
			}
			single := q
			single.Metrics = []Metric{metric}
			separate, err := Run(context.Background(), single, sampleFields(), testScan(rows), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.MetricGrand[i], separate.Grand) {
				t.Fatalf("%s grand changed in multi-measure query", metric.Aggregate)
			}
			for j, row := range got.Rows {
				if !reflect.DeepEqual(row.MetricTotals[i], separate.Rows[j].Total) {
					t.Fatalf("%s row total changed", metric.Aggregate)
				}
				for k := range got.ColumnKeys {
					if !reflect.DeepEqual(row.MetricValues[k][i], separate.Rows[j].Values[k]) || !reflect.DeepEqual(got.MetricColumnTotals[k][i], separate.Totals[k]) {
						t.Fatalf("%s cross-tab value changed", metric.Aggregate)
					}
				}
			}
		}
	}
}
