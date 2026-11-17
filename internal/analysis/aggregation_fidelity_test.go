package analysis

import (
	"context"
	"reflect"
	"testing"
)

func TestPivotLabelsCompareValuesRatherThanEncodedKeyLengths(t *testing.T) {
	fields := []Field{{ID: "C1"}, {ID: "C2"}}
	rows := []Record{
		{Values: map[string]Value{"C1": {Text: "b"}, "C2": {Text: "x"}}},
		{Values: map[string]Value{"C1": {Text: "aa"}, "C2": {Text: "long"}}},
		{Values: map[string]Value{"C1": {Text: "aa"}, "C2": {Text: "z"}}},
		{Values: map[string]Value{"C1": {Text: ""}, "C2": {Text: "a"}}},
	}
	for _, order := range []string{"label_asc", "label_desc", "value_asc"} {
		q := Query{Source: "wb:s", Rows: []string{"C1", "C2"}, Aggregate: "count", Sort: order}
		got, err := Run(context.Background(), q, fields, testScan(rows), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"", "a"}, {"aa", "long"}, {"aa", "z"}, {"b", "x"}}
		if order == "label_desc" {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		for i, row := range got.Rows {
			if !reflect.DeepEqual(row.Keys, want[i]) {
				t.Fatalf("%s: row %d got %v want %v", order, i, row.Keys, want[i])
			}
		}
	}
}

func TestMissingFormulaCacheCannotBecomeZeroOrDisappearInTransforms(t *testing.T) {
	fields := []Field{{ID: "C1"}}
	for _, op := range []string{"length", "abs", "multiply", "divide", "year", "number", "prefix"} {
		t.Run(op, func(t *testing.T) {
			rows := []Record{
				{Values: map[string]Value{"C1": {MissingCache: true}}},
				{Values: map[string]Value{"C1": {Text: "123", Numeric: true}}},
			}
			q := Query{Source: "wb:s", Aggregate: "count", Value: "D_one", Transforms: []Transform{{ID: "D_one", Source: "C1", Op: op, Arg: "2"}}}
			if op == "length" {
				q.Aggregate = "average"
			}
			if op == "year" {
				q.Transforms[0].Arg = ""
			}
			got, err := Run(context.Background(), q, fields, testScan(rows), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.MissingFormulaCaches != 1 {
				t.Fatalf("missing-cache evidence lost: %+v", got)
			}
			if op == "length" && got.Grand.Exact != "3" {
				t.Fatalf("unknown cache entered average: %+v", got.Grand)
			}
			values := map[string]Value{"C1": {MissingCache: true}}
			ApplyTransforms(values, q.Transforms)
			if !values["D_one"].MissingCache || values["D_one"].Text != "" {
				t.Fatal("manufactured a source value", values)
			}
		})
	}
}

func TestFailedNumericAndDatePartsTransformsRetainSourceEvidence(t *testing.T) {
	for _, tc := range []struct {
		op, arg, raw string
		numeric      bool
	}{
		{"multiply", "2", "not-a-number", false},
		{"divide", "0", "12.5", true},
		{"abs", "", "invalid", false},
		{"year", "", "2026-02-30", false},
	} {
		values := map[string]Value{"C1": {Text: tc.raw, Numeric: tc.numeric}}
		ApplyTransforms(values, []Transform{{ID: "D_one", Source: "C1", Op: tc.op, Arg: tc.arg}})
		out := values["D_one"]
		if out.Text != tc.raw || out.Numeric || !out.ConversionFailed {
			t.Fatalf("failure erased evidence: %+v -> %+v", tc, out)
		}
	}
}

func TestMissingCacheDiagnosticsCountSourceFieldsOnceAcrossMetrics(t *testing.T) {
	q := Query{Source: "wb:s", Metrics: []Metric{{Field: "C1", Aggregate: "count"}, {Field: "C1", Aggregate: "sum"}, {Field: "C1", Aggregate: "max"}}}
	got, err := Run(context.Background(), q, []Field{{ID: "C1"}}, testScan([]Record{{Values: map[string]Value{"C1": {MissingCache: true}}}}), nil)
	if err != nil || got.MissingFormulaCaches != 1 {
		t.Fatal(got, err)
	}
}
