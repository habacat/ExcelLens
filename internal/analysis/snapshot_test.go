package analysis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func snapshotFixture(t *testing.T, records [][]string) ComparisonSnapshot {
	t.Helper()
	fields := []Field{{ID: "C1", Name: "分类", Column: 1}, {ID: "C2", Name: "子类", Column: 2}, {ID: "C3", Name: "期间", Column: 3}, {ID: "C4", Name: "金额", Column: 4, Numeric: true}}
	q := Query{Source: "wb:原表", Rows: []string{"C1", "C2"}, Columns: []string{"C3"}, Value: "C4", Aggregate: "sum", Sort: "label_asc", Top: 200, Precision: 2}
	r, err := Run(context.Background(), q, fields, func(ctx context.Context, needed []string, visit func(Record) error) error {
		for i, values := range records {
			row := Record{Number: i + 2, Values: map[string]Value{}}
			for col, value := range values {
				row.Values[fields[col].ID] = Value{Text: value, Numeric: col == 3}
			}
			if err := visit(row); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	metric, projected, err := ProjectSnapshotMetric(q, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := ComparisonSnapshot{Version: 1, ID: strings.Repeat("a", 32), Name: "基准", Created: "2026-11-17T00:00:00Z", AnalysisAt: "2026-11-16T23:00:00Z", WorkbookKey: "content", Book: "path", File: "原文件.xlsx", Query: q, Fields: fields, Metric: metric, Result: projected}
	if err := ValidateComparisonSnapshot(s); err != nil {
		t.Fatalf("%v: %+v", err, s.Result)
	}
	return s
}

func TestSnapshotExactAlignmentPreservesKeysMissingAndNegativeBase(t *testing.T) {
	left := snapshotFixture(t, [][]string{{"a / b", "c", "一", "9007199254740993.31"}, {"a", "b / c", "一", "-0.3"}, {"001", "", "旧", "0"}, {"空", "", "一", ""}})
	right := snapshotFixture(t, [][]string{{"a", "b / c", "一", "-0.1"}, {"a / b", "c", "一", "9007199254740993.33"}, {"新", "", "新", "1"}, {"001", "", "旧", "2"}, {"空", "", "一", ""}})
	right.ID, right.Name = strings.Repeat("b", 32), "对比"
	c, err := CompareSnapshots(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rows) != 5 || len(c.ColumnKeys) != 3 {
		t.Fatal("keys/series were joined with lossy separators or omitted", c)
	}
	for _, row := range c.Rows {
		switch strings.Join(row.Keys, "|") {
		case "a / b|c":
			if row.Total.Difference.Exact != "0.02" {
				t.Fatal("large exact amount was rounded", row)
			}
		case "a|b / c":
			if row.Total.Difference.Exact != "0.2" || row.Total.Change.Exact != "2/3" {
				t.Fatal("negative baseline change must divide by absolute baseline", row)
			}
		case "001|":
			if row.Total.Change.Status != "zero_base" || row.Total.Difference.Exact != "2" {
				t.Fatal(row)
			}
		case "空|":
			if row.Total.Left.Count != 0 || row.Total.Right.Count != 0 || row.Total.Difference.Exact != "0" {
				t.Fatal("the existing empty-sum aggregate semantics changed", row)
			}
		case "新|":
			if row.Total.Status != "only_right" || row.Total.Left != nil || row.Total.Difference.Exact != "" {
				t.Fatal("absent category was silently made zero", row)
			}
		}
	}
}

func TestSnapshotMetricProjectionAndSemanticMismatch(t *testing.T) {
	s := snapshotFixture(t, [][]string{{"甲", "乙", "期", "1/3"}})
	s.Query.Metrics = []Metric{{Field: "C4", Aggregate: "sum"}, {Field: "C4", Aggregate: "average"}}
	metric, r, err := ProjectSnapshotMetric(s.Query, Result{ColumnKeys: []string{"期"}, Totals: []Cell{{}}, MetricColumnTotals: [][]Cell{{{}, {Exact: "1/3", Display: "0.33"}}}, MetricGrand: []Cell{{}, {Exact: "1/3", Display: "0.33"}}, Rows: []ResultRow{{Keys: []string{"甲", "乙"}, Values: []Cell{{}}, MetricValues: [][]Cell{{{}, {Exact: "1/3", Display: "0.33"}}}, MetricTotals: []Cell{{}, {Exact: "1/3", Display: "0.33"}}}}}, 1)
	if err != nil || metric.Aggregate != "average" || r.Grand.Exact != "1/3" || r.Rows[0].Values[0].Exact != "1/3" || len(r.Rows[0].MetricValues) != 0 {
		t.Fatal(metric, r, err)
	}
	if _, _, err := ProjectSnapshotMetric(s.Query, Result{}, 2); err == nil {
		t.Fatal("nonexistent metric accepted")
	}
	other := s
	other.Fields = append([]Field(nil), s.Fields...)
	other.Fields[3].Name = "比例"
	if _, err := CompareSnapshots(s, other); err == nil {
		t.Fatal("C4 from an unrelated measure was treated as compatible")
	}
}

func TestSnapshotCapacitiesAndUnavailableComparisons(t *testing.T) {
	s := snapshotFixture(t, [][]string{{"甲", "乙", "期", "0.1"}})
	for _, update := range []func(*ComparisonSnapshot){
		func(s *ComparisonSnapshot) { s.Result.Rows = append(s.Result.Rows, s.Result.Rows[0]) },
		func(s *ComparisonSnapshot) { s.Result.Rows[0].Values = nil },
		func(s *ComparisonSnapshot) { s.Result.Grand.Exact = "NaN" },
		func(s *ComparisonSnapshot) { s.Version = 2 },
	} {
		data, _ := json.Marshal(s)
		var invalid ComparisonSnapshot
		_ = json.Unmarshal(data, &invalid)
		update(&invalid)
		if ValidateComparisonSnapshot(invalid) == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	if _, _, err := ProjectSnapshotMetric(s.Query, Result{Rows: make([]ResultRow, formats.SnapshotRows+1)}, 0); err == nil {
		t.Fatal("oversized result was silently cut to a preview")
	}
	l := Cell{Exact: "1/3", Display: "0.33"}
	r := Cell{Exact: "2/3", Display: "0.67"}
	if d := snapshotDelta(&l, &r); d.Difference.Exact != "1/3" || d.Change.Exact != "1" {
		t.Fatal(d)
	}
	l.Exact = "1e999999999"
	if d := snapshotDelta(&l, &r); d.Status != "unavailable" || d.Difference.Exact != "" {
		t.Fatal("arithmetic capacity failure invented a value", d)
	}
	l.Comparison = &ComparedValue{Status: "zero_denominator", Display: "分母为0"}
	if d := snapshotDelta(&l, &r); d.Status != "unavailable" {
		t.Fatal(d)
	}
	if d := snapshotDelta(&Cell{}, &r); d.Status != "empty" || d.Difference.Exact != "" {
		t.Fatal("unavailable aggregate was silently made zero", d)
	}
	if d := snapshotDelta(nil, nil); d.Status != "missing" || d.Left != nil || d.Right != nil {
		t.Fatal("a category/series missing from both sides was attributed to one side", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareSnapshotsContext(ctx, s, s); err == nil {
		t.Fatal("cancelled comparison continued")
	}
}
