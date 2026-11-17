package analysis

import (
	"context"
	"reflect"
	"testing"
)

func TestOnePassMultiMetricExactAndFilterDrill(t *testing.T) {
	fields := sampleFields()
	rows := sampleRows()
	q := Query{Source: "wb:Invest", Rows: []string{"C1"}, Columns: []string{"C2"}, Aggregate: "sum", Value: "C3", Metrics: []Metric{{Field: "C3", Aggregate: "sum", Name: "金额"}, {Field: "", Aggregate: "count", Name: "笔数"}, {Field: "C3", Aggregate: "max", Name: "最大金额"}}, Filters: []Filter{{Field: "C2", Op: "in", Value: "债券|基金"}, {Field: "C4", Op: "between", Value: "2026-01-01..2026-12-31"}}, Precision: 2}
	scans := 0
	scan := func(ctx context.Context, needed []string, visit func(Record) error) error {
		scans++
		return testScan(rows)(ctx, needed, visit)
	}
	got, err := Run(context.Background(), q, fields, scan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("must scan once, got %d", scans)
	}
	if len(got.MetricGrand) != 3 || got.MetricGrand[0].Exact != "9007199254740993.31" || got.MetricGrand[1].Exact != "6" || got.MetricGrand[2].Exact != "9007199254740993" {
		t.Fatalf("incorrect metrics %+v", got.MetricGrand)
	}
	if len(got.Rows) != 2 || len(got.Rows[0].MetricTotals) != 3 || len(got.Rows[0].MetricValues[0]) != 3 {
		t.Fatalf("missing metrics in table %+v", got.Rows)
	}
	table := BuildPivotTable(q, got, fields)
	if len(table.Headers) != 10 || len(table.Rows) != 3 || len(table.Rows[0]) != 10 {
		t.Fatalf("flattened table inconsistent: %+v", table)
	}
	dr, err := Drill(context.Background(), q, fields, []string{"北京"}, "债券", 0, 100, testScan(rows))
	if err != nil || len(dr.Rows) != 2 {
		t.Fatalf("multi filter drill mismatch %+v %v", dr, err)
	}
	cols, detail, truncated, err := CollectDetails(context.Background(), q, fields, []string{"北京"}, "债券", 100, testScan(rows))
	if err != nil || truncated || len(detail) != 2 || len(cols) < 4 || detail[0][0] != "2" || detail[1][0] != "4" {
		t.Fatalf("details %+v %+v %v %v", cols, detail, truncated, err)
	}
}
func TestBatchRevisionAtomicAndUndoValues(t *testing.T) {
	s := Sheet{ID: "s", Name: "测试", Columns: []string{"A", "B"}, Rows: [][]string{{"old", "1"}}}
	before, err := s.ApplyEdits([]CellChange{{Row: 0, Col: 0, Value: "new"}, {Row: 2, Col: 1, Value: "100"}})
	if err != nil || s.Revision != 1 || !reflect.DeepEqual(before, []CellChange{{Row: 0, Col: 0, Value: "old"}, {Row: 2, Col: 1, Value: ""}}) {
		t.Fatalf("batch %+v %v", before, err)
	}
	if _, err = s.ApplyEdits([]CellChange{{Row: 0, Col: 0, Value: "bad"}, {Row: MaxSheetRows, Col: 1, Value: "1"}}); err == nil {
		t.Fatal("invalid transaction should fail")
	}
	if s.Revision != 1 || s.Rows[0][0] != "new" {
		t.Fatalf("partial transaction committed %+v", s)
	}
	_, err = s.ApplyEdits(before)
	if err != nil || s.Rows[0][0] != "old" || s.Rows[2][1] != "" || s.Revision != 2 {
		t.Fatalf("undo failed %+v %v", s, err)
	}
}
func TestPivotWithoutColumnDoesNotDuplicateTotal(t *testing.T) {
	q := Query{Source: "wb:Invest", Rows: []string{"C1"}, Aggregate: "sum", Value: "C3", Metrics: []Metric{{Field: "C3", Aggregate: "sum"}, {Aggregate: "count"}}}
	got, err := Run(context.Background(), q, sampleFields(), testScan(sampleRows()), nil)
	if err != nil {
		t.Fatal(err)
	}
	tab := BuildPivotTable(q, got, sampleFields())
	if len(tab.Headers) != 3 || len(tab.Rows[0]) != 3 || tab.Rows[len(tab.Rows)-1][1] != "9007199254740993.31" {
		t.Fatalf("spurious total column or wrong data %+v", tab)
	}
}
