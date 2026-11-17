package analysis

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func sampleFields() []Field {
	return []Field{{ID: "C1", Name: "机构"}, {ID: "C2", Name: "品种"}, {ID: "C3", Name: "金额", Numeric: true}, {ID: "C4", Name: "日期"}}
}

func TestAnalysisTextBudgetRejectsBeforeRetainingAnotherDistinctValue(t *testing.T) {
	memory := &queryMemory{used: (256 << 20) - 100}
	var count int
	var accumulator accumulator
	err := accumulator.add(metricInput{value: Value{Text: strings.Repeat("x", 128)}}, metricNeeds{distinct: true}, &count, memory)
	if err == nil || len(accumulator.distinct) != 0 || count != 0 {
		t.Fatal("over-budget value retained", err, count)
	}
	if memory.used != (256<<20)-100 {
		t.Fatal("failed reservation changed accounting")
	}
}
func sampleRows() []Record {
	return []Record{
		{Number: 2, Values: map[string]Value{"C1": {Text: "北京"}, "C2": {Text: "债券"}, "C3": {Text: "0.1", Numeric: true}, "C4": {Text: "2026-01-03"}}},
		{Number: 4, Values: map[string]Value{"C1": {Text: "北京"}, "C2": {Text: "债券"}, "C3": {Text: "0.2", Numeric: true}, "C4": {Text: "2026-01-04"}}},
		{Number: 7, Values: map[string]Value{"C1": {Text: "北京"}, "C2": {Text: "基金"}, "C3": {Text: "9007199254740993", Numeric: true}, "C4": {Text: "2026-02-01"}}},
		{Number: 9, Values: map[string]Value{"C1": {Text: "上海"}, "C2": {Text: "债券"}, "C3": {Text: "1e-2", Numeric: true}, "C4": {Text: "2026-03-02"}}},
		{Number: 12, Values: map[string]Value{"C1": {Text: "上海"}, "C2": {Text: "基金"}, "C3": {Text: "拒绝数值", Numeric: false}, "C4": {Text: "2026-03-05"}}},
		{Number: 13, Values: map[string]Value{"C1": {Text: "上海"}, "C2": {Text: "基金"}, "C3": {Text: "", Numeric: true, MissingCache: true}, "C4": {Text: "2026-03-06"}}},
	}
}
func testScan(rows []Record) Scan {
	return func(ctx context.Context, needed []string, visit func(Record) error) error {
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			vals := make(map[string]Value, len(needed))
			for _, id := range needed {
				if v, ok := r.Values[id]; ok {
					vals[id] = v
				}
			}
			if err := visit(Record{Number: r.Number, Values: vals}); err != nil {
				return err
			}
		}
		return nil
	}
}
func TestPivotExactDecimalWithColumnAndTotal(t *testing.T) {
	q := Query{Source: "wb:Sheet1", Rows: []string{"C1"}, Columns: []string{"C2"}, Value: "C3", Aggregate: "sum", Precision: 2, Sort: "label_asc"}
	r, err := Run(context.Background(), q, sampleFields(), testScan(sampleRows()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Scanned != 6 || r.Included != 6 || r.InvalidNumbers != 1 || r.MissingFormulaCaches != 1 {
		t.Fatalf("count error: %+v", r)
	}
	if !reflect.DeepEqual(r.ColumnKeys, []string{"债券", "基金"}) {
		t.Fatalf("column order %v", r.ColumnKeys)
	}
	if r.Grand.Exact != "9007199254740993.31" {
		t.Fatalf("decimal rounded or inaccurate: %+v", r.Grand)
	}
	if len(r.Rows) != 2 || r.Rows[0].Keys[0] != "上海" && r.Rows[0].Keys[0] != "北京" {
		t.Fatalf("groups unexpected: %+v", r.Rows)
	}
	var beijing *ResultRow
	for i := range r.Rows {
		if r.Rows[i].Keys[0] == "北京" {
			beijing = &r.Rows[i]
			break
		}
	}
	if beijing == nil || beijing.Values[0].Exact != "0.3" || beijing.Values[1].Exact != "9007199254740993" {
		t.Fatalf("precise cells %+v", beijing)
	}
	if r.Grand.Display != "9007199254740993.31" {
		t.Fatalf("display precision %q", r.Grand.Display)
	}
}
func TestPivotCountValueSemantics(t *testing.T) {
	rows := sampleRows()
	for _, tc := range []struct {
		value  string
		expect string
	}{{"", "6"}, {"C3", "5"}} {
		q := Query{Source: "wb:s", Rows: []string{"C1"}, Aggregate: "count", Value: tc.value}
		res, err := Run(context.Background(), q, sampleFields(), testScan(rows), nil)
		if err != nil || res.Grand.Exact != tc.expect {
			t.Errorf("value=%q got=%s err=%v", tc.value, res.Grand.Exact, err)
		}
	}
}
func TestPivotTransformsFiltersAndDrill(t *testing.T) {
	rows := sampleRows()
	q := Query{Source: "wb:s", Rows: []string{"D_year"}, Value: "D_amount", Aggregate: "sum", Precision: 3,
		Filters:    []Filter{{Field: "C1", Op: "contains", Value: "京"}},
		Transforms: []Transform{{ID: "D_year", Name: "年份", Source: "C4", Op: "year"}, {ID: "D_amount", Name: "金额×3", Source: "C3", Op: "multiply", Arg: "3"}}}
	res, err := Run(context.Background(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Included != 3 || len(res.Rows) != 1 || res.Rows[0].Keys[0] != "2026" || res.Grand.Exact != "27021597764222979.9" {
		t.Fatalf("wrong calculated sum: %+v", res)
	}
	dr, err := Drill(context.Background(), q, sampleFields(), []string{"2026"}, "", 0, 2, testScan(rows))
	if err != nil {
		t.Fatal(err)
	}
	if !dr.HasMore || len(dr.Rows) != 2 || dr.Rows[0].Row != 2 || dr.Rows[1].Row != 4 {
		t.Fatalf("wrong drill page1 %+v", dr)
	}
	dr, err = Drill(context.Background(), q, sampleFields(), []string{"2026"}, "", 2, 2, testScan(rows))
	if err != nil || dr.HasMore || len(dr.Rows) != 1 || dr.Rows[0].Row != 7 {
		t.Fatalf("wrong drill page2 %+v err %v", dr, err)
	}
}
func TestPivotTopRetainsFullGrandTotal(t *testing.T) {
	q := Query{Source: "wb:s", Rows: []string{"C1", "C2"}, Value: "C3", Aggregate: "sum", Top: 1, Sort: "value_desc"}
	r, err := Run(context.Background(), q, sampleFields(), testScan(sampleRows()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 || !r.Truncated || r.Grand.Exact != "9007199254740993.31" {
		t.Fatalf("top incorrectly changes grand %+v", r)
	}
}
func TestPivotPreventsHighCardinalityAllocation(t *testing.T) {
	fields := []Field{{ID: "C1"}}
	rows := make([]Record, MaxColumns+2)
	for i := range rows {
		rows[i] = Record{Number: i + 1, Values: map[string]Value{"C1": {Text: fmt.Sprint(i)}}}
	}
	q := Query{Source: "wb:s", Columns: []string{"C1"}, Aggregate: "count"}
	_, err := Run(context.Background(), q, fields, testScan(rows), nil)
	if err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("expected cardinality guard: %v", err)
	}
}
func TestPivotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Query{Source: "wb:s", Aggregate: "count"}, sampleFields(), testScan(sampleRows()), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated %v", err)
	}
}
func TestQueryValidationRejectsUnsafeAndUnknown(t *testing.T) {
	valid := Query{Source: "wb:s", Rows: []string{"C1"}, Aggregate: "count"}
	bad := []Query{
		{Source: "wb:s", Rows: []string{"NOPE"}, Aggregate: "count"},
		{Source: "wb:s", Rows: []string{"C1"}, Aggregate: "anything"},
		{Source: "wb:s", Rows: []string{"C1"}, Aggregate: "sum"},
		{Source: "wb:s", Aggregate: "count", Top: 5000},
		{Source: "wb:s", Aggregate: "count", Filters: []Filter{{Field: "C1", Op: "regex", Value: ".*"}}},
		{Source: "wb:s", Aggregate: "count", Transforms: []Transform{{ID: "D_X", Source: "D_X", Op: "number"}}},
	}
	if err := Validate(valid, sampleFields()); err != nil {
		t.Fatalf("valid rejected %v", err)
	}
	for i, q := range bad {
		if err := Validate(q, sampleFields()); err == nil {
			t.Errorf("invalid query %d accepted", i)
		}
	}
}
func TestExactDecimalParser(t *testing.T) {
	for _, tc := range []struct {
		in, exact string
		valid     bool
	}{
		{"9007199254740993", "9007199254740993", true},
		{"-0", "0", true},
		{".25", "0.25", true},
		{"1.2e-3", "0.0012", true},
		{"1234567890123456789.12345678901234567890", "1234567890123456789.1234567890123456789", true},
		{"1/3", "1/3", true},
		{"NaN", "", false}, {"Infinity", "", false}, {"1,234", "", false}, {"1/0", "", false},
	} {
		v, ok := Decimal(tc.in)
		if ok != tc.valid {
			t.Errorf("%q expected valid=%v got=%v", tc.in, tc.valid, ok)
			continue
		}
		if ok && decimalExact(v) != tc.exact {
			t.Errorf("%q expected %q got %q", tc.in, tc.exact, decimalExact(v))
		}
	}
	for _, tc := range []struct {
		in    string
		valid bool
	}{{"01234", false}, {"0001", false}, {"0.25", true}, {"-003", false}, {"18.2", true}, {"13/7", true}, {"", false}} {
		if got := IsNumericLiteral(tc.in); got != tc.valid {
			t.Errorf("%q numeric=%v wanted=%v", tc.in, got, tc.valid)
		}
	}
}

func TestMaterializeCleanDetailsKeepsProvenanceAndPrecision(t *testing.T) {
	q := Query{Source: "wb:s", Rows: []string{"C1"}, Columns: []string{"C2"}, Value: "C3", Aggregate: "sum",
		Filters:    []Filter{{Field: "C4", Op: "contains", Value: "2026"}},
		Transforms: []Transform{{ID: "D_month", Name: "月份", Source: "C4", Op: "yearmonth"}}}
	cols, rows, truncated, err := CollectDetails(context.Background(), q, sampleFields(), []string{"北京"}, "债券", 100, testScan(sampleRows()))
	if err != nil {
		t.Fatal(err)
	}
	wantCols := []string{"源行号", "机构", "品种", "金额", "日期", "月份"}
	if !reflect.DeepEqual(cols, wantCols) {
		t.Fatalf("detail columns got %v", cols)
	}
	if truncated || len(rows) != 2 || rows[0][0] != "2" || rows[0][3] != "0.1" || rows[1][0] != "4" || rows[1][5] != "2026-01" {
		t.Fatalf("detail wrong: %v truncated=%v", rows, truncated)
	}
	_, limited, cut, err := CollectDetails(context.Background(), q, sampleFields(), nil, "", 1, testScan(sampleRows()))
	if err != nil || !cut || len(limited) != 1 {
		t.Fatalf("limit/truncation wrong: %v %v %v", limited, cut, err)
	}
	_, _, _, err = CollectDetails(context.Background(), q, sampleFields(), []string{"北京", "债券"}, "债券", 12, testScan(sampleRows()))
	if err == nil {
		t.Fatal("invalid drill group accepted")
	}
}

// A pivot dimension beyond the first 40 visible detail columns must still
// participate in the drill predicate. No high-column value may be dropped.
func TestDrillWideSheetDimensions(t *testing.T) {
	fields := make([]Field, 70)
	for i := range fields {
		fields[i] = Field{ID: fmt.Sprintf("C%d", i+1), Name: fmt.Sprintf("字段%d", i+1), Column: i + 1}
	}
	values := make(map[string]Value, len(fields))
	for i := range fields {
		values[fields[i].ID] = Value{Text: fmt.Sprint(i + 1)}
	}
	values["C65"] = Value{Text: "投资部"}
	values["C66"] = Value{Text: "债券"}
	rows := []Record{{Number: 27, Values: values}}
	q := Query{Source: "wb:BigSheet", Rows: []string{"C65"}, Columns: []string{"C66"}, Aggregate: "count", Value: "C63"}
	res, err := Drill(context.Background(), q, fields, []string{"投资部"}, "债券", 0, 20, testScan(rows))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fields) != 40 || len(res.Rows) != 1 || len(res.Rows[0].Cells) != 40 || res.Rows[0].Row != 27 {
		t.Fatalf("wide drill lost source: %+v", res)
	}
	wrong, err := Drill(context.Background(), q, fields, []string{"其他"}, "债券", 0, 20, testScan(rows))
	if err != nil || len(wrong.Rows) != 0 {
		t.Fatalf("wide filter mismatch: %+v %v", wrong, err)
	}
}
