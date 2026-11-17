package analysis

import (
	"context"
	"testing"
)

func TestConversionsOnlyRunWhenRequestedAndKeepRejectedText(t *testing.T) {
	fields := []Field{{ID: "C1", Name: "原日期"}}
	records := []Record{
		{Number: 2, Values: map[string]Value{"C1": {Text: "2026/1/2"}}},
		{Number: 3, Values: map[string]Value{"C1": {Text: "01/02/2026"}}},
		{Number: 4, Values: map[string]Value{"C1": {Text: "2026-02-30"}}},
		{Number: 5, Values: map[string]Value{"C1": {Text: ""}}},
	}
	q := Query{Source: "wb:Data", Rows: []string{"C1"}, Aggregate: "count", Sort: "label_asc"}
	plain, err := Run(context.Background(), q, fields, testScan(records), nil)
	if err != nil || plain.InvalidConversions != 0 {
		t.Fatal(plain, err)
	}
	q.Transforms = []Transform{{ID: "D_date", Name: "转换日期", Source: "C1", Op: "date", Arg: "auto"}}
	q.Rows = []string{"D_date"}
	converted, err := Run(context.Background(), q, fields, testScan(records), nil)
	if err != nil || converted.InvalidConversions != 2 || converted.Scanned != 4 {
		t.Fatal(converted, err)
	}
	_, rows, _, err := CollectDetails(context.Background(), q, fields, nil, "", 10, testScan(records))
	if err != nil || len(rows) != 4 {
		t.Fatal(rows, err)
	}
	want := []string{"2026-01-02", "01/02/2026", "2026-02-30", ""}
	for i, row := range rows {
		if row[1] != records[i].Values["C1"].Text || row[2] != want[i] {
			t.Fatalf("original/converted values differ at row %d: %v", i, row)
		}
		if _, mutated := records[i].Values["D_date"]; mutated {
			t.Fatal("query changed its source records")
		}
	}
	q.Transforms[0].Arg = "guess-month-day"
	if err := Validate(q, fields); err == nil {
		t.Fatal("unknown date-order rule accepted")
	}
}

func TestConversionKeepsTimePrecisionAndExplicitTextType(t *testing.T) {
	vals := map[string]Value{"C1": {Text: "2026/1/2 09:05:12.5"}, "C2": {Text: "000123"}, "C3": {Text: "12.50", Numeric: true}, "C4": {Text: "bad-number"}}
	ApplyTransforms(vals, []Transform{
		{ID: "D_time", Source: "C1", Op: "time"},
		{ID: "D_datetime", Source: "C1", Op: "datetime"},
		{ID: "D_identifier", Source: "C2", Op: "text"},
		{ID: "D_text", Source: "C3", Op: "text"},
		{ID: "D_bad", Source: "C4", Op: "number"},
	})
	if vals["D_time"].Text != "09:05:12.5" || vals["D_datetime"].Text != "2026-01-02 09:05:12.5" || vals["D_identifier"].Text != "000123" || vals["D_text"].Numeric {
		t.Fatal(vals)
	}
	if vals["D_bad"].Text != "bad-number" || !vals["D_bad"].ConversionFailed {
		t.Fatal("failed number conversion must keep the original text", vals["D_bad"])
	}
}

func TestConvertedTimesFilterAsTimes(t *testing.T) {
	fields := []Field{{ID: "C1", Name: "时间"}}
	records := []Record{
		{Number: 2, Values: map[string]Value{"C1": {Text: "09:05:12.5"}}},
		{Number: 3, Values: map[string]Value{"C1": {Text: "14:30"}}},
		{Number: 4, Values: map[string]Value{"C1": {Text: "bad"}}},
	}
	q := Query{Source: "wb:Data", Rows: []string{"D_time"}, Aggregate: "count", Transforms: []Transform{{ID: "D_time", Source: "C1", Op: "time"}}, Filters: []Filter{{Field: "D_time", Op: "between", Value: "09:00..12:00"}}}
	got, err := Run(context.Background(), q, fields, testScan(records), nil)
	if err != nil || got.Included != 1 || got.InvalidConversions != 1 || len(got.Rows) != 1 || got.Rows[0].Keys[0] != "09:05:12.5" {
		t.Fatal(got, err)
	}
	q.Filters[0].Value = "2026-01-01..2026-12-31"
	got, err = Run(context.Background(), q, fields, testScan(records), nil)
	if err != nil || got.Included != 0 {
		t.Fatal("time-only values must not compare as calendar dates", got, err)
	}
}
