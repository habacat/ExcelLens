package analysis

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTreeQueriesOnlyRequestedLevelWithoutMaterializingDescendants(t *testing.T) {
	fields := []Field{{ID: "C1"}, {ID: "C2"}, {ID: "C3"}, {ID: "C4", Numeric: true}}
	q := Query{Source: "wb:generated", Rows: []string{"C1", "C2", "C3"}, Value: "C4", Aggregate: "sum", Layout: "tree", Sort: "label_asc", Precision: 2}
	var requested []string
	scan := func(ctx context.Context, need []string, visit func(Record) error) error {
		requested = slices.Clone(need)
		for i := 0; i < 6000; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			all := map[string]Value{"C1": {Text: fmt.Sprint(i / 3000)}, "C2": {Text: fmt.Sprint(i % 3000 / 1000)}, "C3": {Text: fmt.Sprint(i % 1000)}, "C4": {Text: "0.01", Numeric: true}}
			values := make(map[string]Value, len(need))
			for _, id := range need {
				values[id] = all[id]
			}
			if err := visit(Record{Number: i + 2, Values: values}); err != nil {
				return err
			}
		}
		return nil
	}
	root, err := Run(t.Context(), q, fields, scan, nil)
	if err != nil || len(root.Rows) != 2 || root.Grand.Exact != "60" || root.Scanned != 6000 || !reflect.DeepEqual(requested, []string{"C1", "C4"}) || root.Tree == nil || !root.Tree.HasChildren || len(root.RowFields) != 1 {
		t.Fatal("root read/retained future grouping fields or lost exact totals", root, requested, err)
	}
	middle, err := RunTreeLevel(t.Context(), q, fields, []string{"0"}, scan, nil)
	if err != nil || len(middle.Rows) != 3 || middle.Included != 3000 || middle.Grand.Exact != "30" || middle.Tree.Path[0] != "0" || slices.Contains(requested, "C3") {
		t.Fatal(middle, requested, err)
	}
	leaf, err := RunTreeLevel(t.Context(), q, fields, []string{"0", "1"}, scan, nil)
	if err != nil || len(leaf.Rows) != 1000 || leaf.Included != 1000 || leaf.Grand.Exact != "10" || leaf.Tree.HasChildren || !reflect.DeepEqual(leaf.Rows[0].Keys, []string{"0", "1", "0"}) {
		t.Fatal(leaf, err)
	}
	flat := q
	flat.Layout = ""
	if _, err := Run(t.Context(), flat, fields, scan, nil); err == nil || !strings.Contains(err.Error(), "5000") {
		t.Fatal("test did not exceed the old total combination guard", err)
	}
}

func TestTreeParentTotalsAreRecomputedWithExactTypesAndFullFilterBudget(t *testing.T) {
	fields := []Field{{ID: "C1"}, {ID: "C2"}, {ID: "C3", Numeric: true}, {ID: "C4"}, {ID: "C5"}}
	q := Query{Source: "wb:typed", Layout: "tree", Rows: []string{"D_group", "C2"}, Columns: []string{"C5"}, Aggregate: "sum", Value: "C3", Precision: 2, Sort: "label_asc", Transforms: []Transform{{ID: "D_group", Source: "C1", Op: "upper"}}, Metrics: []Metric{{Field: "C3", Aggregate: "sum"}, {Field: "C3", Aggregate: "average"}, {Field: "C4", Aggregate: "distinct"}, {Aggregate: "count"}, {Field: "C3", Aggregate: "min"}, {Field: "C3", Aggregate: "max"}}}
	for range 8 {
		q.Filters = append(q.Filters, Filter{Field: "C1", Op: "contains", Value: ""})
	}
	var records []Record
	for i, entry := range []struct{ parent, child, amount, code, column string }{{"a*?:", "001", "1", "X", ""}, {"a*?:", "1", "10", "X", "非空"}, {"a*?:", "1", "10", "Y", ""}, {"a*?:", "1", "10", "Y", "非空"}, {"other", "001", "9007199254740993.31", "Z", ""}} {
		records = append(records, Record{Number: i + 2, Values: map[string]Value{"C1": {Text: entry.parent}, "C2": {Text: entry.child}, "C3": {Text: entry.amount, Numeric: true}, "C4": {Text: entry.code}, "C5": {Text: entry.column}}})
	}
	r, err := RunTreeLevel(t.Context(), q, fields, []string{"A*?:"}, testScan(records), nil)
	if err != nil || r.Included != 4 || len(r.Rows) != 2 || r.MetricGrand[0].Exact != "31" || r.MetricGrand[1].Exact != "7.75" || r.MetricGrand[2].Exact != "2" || r.MetricGrand[3].Exact != "4" || r.MetricGrand[4].Exact != "1" || r.MetricGrand[5].Exact != "10" || r.Rows[0].Keys[1] != "001" {
		t.Fatal("parent average/distinct/type/path changed", r, err)
	}
	// Subtotals and the actual blank column are distinct, also for parent paths.
	detail, err := DrillScope(t.Context(), q, fields, []string{"A*?:"}, nil, 0, 100, testScan(records))
	if err != nil || len(detail.Rows) != 4 || detail.Rows[0].Row != 2 {
		t.Fatal(detail, err)
	}
	blank := ""
	detail, err = DrillScope(t.Context(), q, fields, []string{"A*?:"}, &blank, 0, 100, testScan(records))
	if err != nil || len(detail.Rows) != 2 || detail.Rows[1].Row != 4 {
		t.Fatal("blank column was treated as a subtotal", detail, err)
	}
	_, rows, truncated, err := CollectDetailsScope(t.Context(), q, fields, []string{"A*?:"}, nil, 100, testScan(records))
	if err != nil || truncated || len(rows) != 4 || rows[0][0] != "2" || rows[3][0] != "5" {
		t.Fatal(rows, truncated, err)
	}
	q.Layout = ""
	if _, err := DrillScope(t.Context(), q, fields, []string{"A*?:"}, nil, 0, 100, testScan(records)); err == nil {
		t.Fatal("partial keys silently changed legacy flat drill semantics")
	}
}

func TestTreeDiagnosticsTopAndCancellationDoNotPublishPartialSuccess(t *testing.T) {
	q := Query{Source: "wb:types", Layout: "tree", Rows: []string{"C1", "C2"}, Value: "C3", Aggregate: "sum", Top: 1, Precision: 2}
	rows := sampleRows()
	for i := range rows {
		rows[i].Values["C2"] = Value{Text: fmt.Sprint(i)}
	}
	r, err := RunTreeLevel(t.Context(), q, sampleFields(), []string{"上海"}, testScan(rows), nil)
	if err != nil || !r.Truncated || len(r.Rows) != 1 || r.Included != 3 || r.InvalidNumbers != 1 || r.MissingFormulaCaches != 1 || r.Grand.Exact != "0.01" {
		t.Fatal(r, err)
	}
	q.Transforms = []Transform{{ID: "D_date", Source: "C4", Op: "date", Arg: "auto"}}
	rows[0].Values["C4"] = Value{Text: "bad-date-outside-parent"}
	r, err = RunTreeLevel(t.Context(), q, sampleFields(), []string{"上海"}, testScan(rows), nil)
	if err != nil || r.InvalidConversions != 0 {
		t.Fatal("child diagnostics escaped its parent path", r, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r, err := RunTreeLevel(ctx, q, sampleFields(), []string{"上海"}, testScan(rows), nil); !errors.Is(err, context.Canceled) || r.Tree != nil || len(r.Rows) != 0 {
		t.Fatal("cancelled level published partial data", r, err)
	}
	for _, path := range [][]string{{"a", "b"}, {string([]byte{0xff})}} {
		if _, err := RunTreeLevel(t.Context(), q, sampleFields(), path, testScan(rows), nil); err == nil {
			t.Fatal("invalid path accepted", path)
		}
	}
}

func TestTreeExportUsesActualLoadedLevelAndKeepsFullTotals(t *testing.T) {
	q := Query{Source: "wb:source", Layout: "tree", Rows: []string{"C1", "C2"}, Value: "C3", Aggregate: "sum", Precision: 2, Top: 1}
	r, err := Run(t.Context(), q, sampleFields(), testScan(sampleRows()), nil)
	if err != nil {
		t.Fatal(err)
	}
	table := BuildPivotTable(q, r, sampleFields())
	if len(table.Headers) != 2 || table.Headers[0] != "机构" || len(table.Rows) != 2 || table.Rows[1][1] != "9007199254740993.31" || !strings.Contains(table.Rows[1][0], "未显示") {
		t.Fatal("tree root export invented empty descendant columns or lost total", table)
	}
}
