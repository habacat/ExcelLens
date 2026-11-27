package analysis

import (
	"context"
	"strconv"
	"testing"
)

func BenchmarkAnalysisPreparedMetrics(b *testing.B) {
	for _, multi := range []bool{false, true} {
		name := "single-sum"
		metrics := []Metric{{Field: "C3", Aggregate: "sum"}}
		if multi {
			name = "six-metrics-one-field"
			metrics = []Metric{{Field: "C3", Aggregate: "sum"}, {Field: "C3", Aggregate: "average"}, {Field: "C3", Aggregate: "min"}, {Field: "C3", Aggregate: "max"}, {Field: "C3", Aggregate: "count"}, {Field: "C3", Aggregate: "distinct"}}
		}
		b.Run(name, func(b *testing.B) {
			rows := make([]Record, 5000)
			for i := range rows {
				rows[i] = Record{Number: i + 2, Values: map[string]Value{"C1": {Text: strconv.Itoa(i % 20)}, "C2": {Text: strconv.Itoa(i % 4)}, "C3": {Text: "9007199254740993." + strconv.Itoa(1000+i%100), Numeric: true}}}
			}
			q := Query{Source: "wb:benchmark", Rows: []string{"C1"}, Columns: []string{"C2"}, Metrics: metrics, Precision: 4}
			scan := testScan(rows)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				result, err := Run(context.Background(), q, sampleFields(), scan, nil)
				if err != nil || result.Included != 5000 || result.MetricGrand[0].Exact != "45035996273704965524.75" {
					b.Fatalf("unexpected aggregate: %v %+v", err, result.MetricGrand)
				}
			}
		})
	}
}

func BenchmarkAnalysisUniqueGroups(b *testing.B) {
	const n = 1000
	rows := make([]Record, n)
	for i := range rows {
		rows[i] = Record{Number: i + 2, Values: map[string]Value{
			"C1": {Text: strconv.Itoa(i)}, "C3": {Numeric: true, Text: "1/7"},
		}}
	}
	q := Query{Source: "wb:benchmark", Rows: []string{"C1"}, Aggregate: "sum", Value: "C3", Precision: 4}
	scan := testScan(rows)
	b.ReportAllocs()
	for b.Loop() {
		r, err := Run(b.Context(), q, sampleFields(), scan, nil)
		if err != nil || len(r.Rows) != n || r.Grand.Exact != "1000/7" {
			b.Fatal(r.Grand, err)
		}
	}
}
