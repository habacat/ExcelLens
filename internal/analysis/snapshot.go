package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
)

// ComparisonSnapshot is independent of source caches and mutable report
// definitions. Result contains only the explicitly chosen metric, at the
// completed root level; it does not imply that Top-N contains all categories.
type ComparisonSnapshot struct {
	Version     int     `json:"version"`
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Created     string  `json:"created"`
	AnalysisAt  string  `json:"analysisAt"`
	WorkbookKey string  `json:"workbookKey"`
	Book        string  `json:"book"`
	File        string  `json:"file"`
	SourceStamp string  `json:"sourceStamp"`
	Query       Query   `json:"query"`
	Fields      []Field `json:"fields"`
	MetricIndex int     `json:"metricIndex"`
	Metric      Metric  `json:"metric"`
	Result      Result  `json:"result"`
}

func snapshotMetric(q Query, index int) (Metric, bool) {
	metrics := q.Metrics
	if len(metrics) == 0 {
		metrics = []Metric{{Field: q.Value, Aggregate: q.Aggregate}}
	}
	if index < 0 || index >= len(metrics) {
		return Metric{}, false
	}
	return metrics[index], true
}

// ProjectSnapshotMetric never slices a large result down to a chart preview.
// All captured rows, series, totals and raw/comparison states remain exact.
func ProjectSnapshotMetric(q Query, source Result, index int) (Metric, Result, error) {
	metric, ok := snapshotMetric(q, index)
	if !ok {
		return Metric{}, Result{}, errors.New("快照指标不存在")
	}
	if len(source.Rows) > formats.SnapshotRows || len(source.ColumnKeys) > formats.SnapshotSeries || len(source.Rows)*len(source.ColumnKeys) > formats.SnapshotCells {
		return Metric{}, Result{}, fmt.Errorf("快照最多%d个分类、%d个系列，请缩小分析范围", formats.SnapshotRows, formats.SnapshotSeries)
	}
	result := source
	result.Issues, result.Metrics, result.MetricGrand, result.MetricColumnTotals = nil, nil, nil, nil
	result.Rows = make([]ResultRow, len(source.Rows))
	cell := func(cells []Cell, fallback Cell) (Cell, error) {
		if index < len(cells) {
			return cells[index], nil
		}
		if index == 0 {
			return fallback, nil
		}
		return Cell{}, errors.New("分析结果缺少指标，请重新运行")
	}
	var err error
	if result.Grand, err = cell(source.MetricGrand, source.Grand); err != nil {
		return Metric{}, Result{}, err
	}
	result.Totals = make([]Cell, len(source.ColumnKeys))
	for j := range source.ColumnKeys {
		if j >= len(source.Totals) || index > 0 && j >= len(source.MetricColumnTotals) {
			return Metric{}, Result{}, errors.New("分析系列合计不完整")
		}
		var metrics []Cell
		if j < len(source.MetricColumnTotals) {
			metrics = source.MetricColumnTotals[j]
		}
		if result.Totals[j], err = cell(metrics, source.Totals[j]); err != nil {
			return Metric{}, Result{}, err
		}
	}
	for i, row := range source.Rows {
		r := ResultRow{Keys: row.Keys, Count: row.Count, Values: make([]Cell, len(source.ColumnKeys))}
		if len(row.Values) != len(source.ColumnKeys) {
			return Metric{}, Result{}, errors.New("分析结果行不完整")
		}
		if r.Total, err = cell(row.MetricTotals, row.Total); err != nil {
			return Metric{}, Result{}, err
		}
		for j := range source.ColumnKeys {
			var metrics []Cell
			if j < len(row.MetricValues) {
				metrics = row.MetricValues[j]
			}
			if r.Values[j], err = cell(metrics, row.Values[j]); err != nil {
				return Metric{}, Result{}, err
			}
		}
		result.Rows[i] = r
	}
	return metric, result, nil
}

var snapshotNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$|^[+-]?[0-9]+/[1-9][0-9]*$`)

func snapshotExact(s string) bool {
	return s == "" || len(s) <= formats.SnapshotExactChars && snapshotNumber.MatchString(s)
}

func validSnapshotCell(c Cell) bool {
	if c.Count < 0 || c.Count > 1<<53-1 || !snapshotExact(c.Exact) || len(c.Display) > formats.SnapshotExactChars {
		return false
	}
	if v := c.Comparison; v != nil {
		if !snapshotExact(v.Exact) || !snapshotExact(v.Numerator) || !snapshotExact(v.Denominator) || len(v.Display) > formats.SnapshotExactChars {
			return false
		}
		switch v.Status {
		case "":
			if v.Exact == "" {
				return false
			}
		case "empty", "empty_denominator", "zero_denominator":
			if v.Exact != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func ValidateComparisonSnapshot(s ComparisonSnapshot) error {
	invalid := errors.New("比较快照结构或容量无效，原文件已保留")
	if s.Version != 1 || len(s.ID) < 16 || len(s.ID) > 64 || !NameValid(s.Name) || len(s.Name) > 240 || s.WorkbookKey == "" || s.Book == "" || s.File == "" || !utf8.ValidString(s.Name) {
		return invalid
	}
	for _, stamp := range []string{s.Created, s.AnalysisAt} {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return invalid
		}
	}
	if len(s.Fields) > MaxSourceColumns || Validate(s.Query, s.Fields) != nil {
		return invalid
	}
	metric, ok := snapshotMetric(s.Query, s.MetricIndex)
	if !ok || !reflect.DeepEqual(metric, s.Metric) {
		return invalid
	}
	r := s.Result
	if len(r.Rows) > formats.SnapshotRows || len(r.ColumnKeys) > formats.SnapshotSeries || len(r.Rows)*len(r.ColumnKeys) > formats.SnapshotCells || len(r.Totals) != len(r.ColumnKeys) || !validSnapshotCell(r.Grand) || r.Scanned < 0 || r.Included < 0 || r.Included > r.Scanned || len(r.Metrics) != 0 || len(r.MetricGrand) != 0 || len(r.MetricColumnTotals) != 0 {
		return invalid
	}
	keys := make(map[string]bool)
	for _, row := range r.Rows {
		key, _ := json.Marshal(row.Keys)
		if keys[string(key)] || len(row.Keys) != len(r.RowFields) || len(row.Values) != len(r.ColumnKeys) || len(row.MetricValues) != 0 || len(row.MetricTotals) != 0 || !validSnapshotCell(row.Total) || row.Count < 0 {
			return invalid
		}
		keys[string(key)] = true
		for _, cell := range row.Values {
			if !validSnapshotCell(cell) {
				return invalid
			}
		}
	}
	series := make(map[string]bool)
	for i, key := range r.ColumnKeys {
		if series[key] || !validSnapshotCell(r.Totals[i]) {
			return invalid
		}
		series[key] = true
	}
	data, err := json.Marshal(s)
	if err != nil || len(data) > formats.SnapshotBytes {
		return invalid
	}
	return nil
}

// Field names and coordinates are resolved before comparing definitions, so
// an unrelated C3 in another file cannot silently become the same measure.
func snapshotDefinition(s ComparisonSnapshot) string {
	var field func(string, int) any
	field = func(id string, depth int) any {
		if depth > 8 {
			return "invalid-depth"
		}
		for _, t := range s.Query.Transforms {
			if t.ID == id {
				return []any{t.Name, t.Op, t.Arg, t.Mapping, field(t.Source, depth+1)}
			}
		}
		for _, f := range s.Fields {
			if f.ID == id {
				return []any{f.Name, f.Column}
			}
		}
		return id
	}
	rows, columns := []any{}, []any{}
	for _, id := range s.Query.Rows {
		rows = append(rows, field(id, 0))
	}
	for _, id := range s.Query.Columns {
		columns = append(columns, field(id, 0))
	}
	sort := ""
	if running(s.Metric) {
		sort = s.Query.Sort
	}
	data, _ := json.Marshal([]any{rows, columns, field(s.Metric.Field, 0), s.Metric.Aggregate, s.Metric.Comparison, s.Metric.ComparisonAxis, s.Query.Layout, sort})
	return string(data)
}

type SnapshotDelta struct {
	Left       *Cell         `json:"left"`
	Right      *Cell         `json:"right"`
	Status     string        `json:"status,omitempty"`
	Difference Cell          `json:"difference"`
	Change     ComparedValue `json:"change"`
}

type SnapshotComparisonRow struct {
	Keys   []string        `json:"keys"`
	Values []SnapshotDelta `json:"values"`
	Total  SnapshotDelta   `json:"total"`
}

type SnapshotComparison struct {
	LeftID     string                  `json:"leftId"`
	RightID    string                  `json:"rightId"`
	ColumnKeys []string                `json:"columnKeys"`
	Totals     []SnapshotDelta         `json:"totals"`
	Rows       []SnapshotComparisonRow `json:"rows"`
	Grand      SnapshotDelta           `json:"grand"`
}

func snapshotRat(c *Cell) (*big.Rat, string) {
	if c == nil {
		return nil, "missing"
	}
	s := c.Exact
	if c.Comparison != nil {
		if c.Comparison.Status != "" {
			return nil, c.Comparison.Status
		}
		s = c.Comparison.Exact
	}
	if s == "" {
		return nil, "empty"
	}
	if len(s) > 4096 || !snapshotExact(s) {
		return nil, "range"
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e < -4096 || e > 4096 {
			return nil, "range"
		}
	}
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, "range"
	}
	return v, ""
}

func snapshotDelta(left, right *Cell) SnapshotDelta {
	d := SnapshotDelta{Left: left, Right: right}
	l, ls := snapshotRat(left)
	r, rs := snapshotRat(right)
	if ls != "" || rs != "" {
		switch {
		case left == nil && right == nil:
			d.Status = "missing"
		case left == nil:
			d.Status = "only_right"
		case right == nil:
			d.Status = "only_left"
		case ls == "empty" || rs == "empty":
			d.Status = "empty"
		default:
			d.Status = "unavailable"
		}
		d.Change.Status = d.Status
		return d
	}
	diff := new(big.Rat).Sub(r, l)
	d.Difference = Cell{Exact: ExactDecimalString(diff), Display: diff.FloatString(2)}
	if left.Comparison != nil && left.Comparison.Percent {
		d.Difference.Comparison = &ComparedValue{Exact: d.Difference.Exact, Display: new(big.Rat).Mul(diff, big.NewRat(100, 1)).FloatString(2) + "%", Percent: true}
	}
	d.Change.Percent = true
	if l.Sign() == 0 {
		d.Change.Status = "zero_base"
	} else {
		change := new(big.Rat).Quo(diff, new(big.Rat).Abs(l))
		d.Change.Exact = ExactDecimalString(change)
		d.Change.Display = new(big.Rat).Mul(change, big.NewRat(100, 1)).FloatString(2) + "%"
	}
	return d
}

func CompareSnapshots(left, right ComparisonSnapshot) (SnapshotComparison, error) {
	return CompareSnapshotsContext(context.Background(), left, right)
}

func CompareSnapshotsContext(ctx context.Context, left, right ComparisonSnapshot) (SnapshotComparison, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotComparison{}, err
	}
	if err := ValidateComparisonSnapshot(left); err != nil {
		return SnapshotComparison{}, err
	}
	if err := ValidateComparisonSnapshot(right); err != nil {
		return SnapshotComparison{}, err
	}
	if snapshotDefinition(left) != snapshotDefinition(right) {
		return SnapshotComparison{}, errors.New("分类、系列或指标定义不同，请选择可比较的快照")
	}
	out := SnapshotComparison{LeftID: left.ID, RightID: right.ID, ColumnKeys: []string{}, Rows: []SnapshotComparisonRow{}, Grand: snapshotDelta(&left.Result.Grand, &right.Result.Grand)}
	seriesIndex := func(keys []string) map[string]int {
		index := make(map[string]int)
		for i, key := range keys {
			index[key] = i
		}
		return index
	}
	lc, rc := seriesIndex(left.Result.ColumnKeys), seriesIndex(right.Result.ColumnKeys)
	out.ColumnKeys = append(out.ColumnKeys, left.Result.ColumnKeys...)
	for _, key := range right.Result.ColumnKeys {
		if _, found := lc[key]; !found {
			out.ColumnKeys = append(out.ColumnKeys, key)
		}
	}
	for _, key := range out.ColumnKeys {
		var l, r *Cell
		if i, ok := lc[key]; ok {
			l = &left.Result.Totals[i]
		}
		if i, ok := rc[key]; ok {
			r = &right.Result.Totals[i]
		}
		out.Totals = append(out.Totals, snapshotDelta(l, r))
	}
	lr, rr := make(map[string]*ResultRow), make(map[string]*ResultRow)
	var order []string
	for i := range left.Result.Rows {
		r := &left.Result.Rows[i]
		key, _ := json.Marshal(r.Keys)
		lr[string(key)] = r
		order = append(order, string(key))
	}
	for i := range right.Result.Rows {
		r := &right.Result.Rows[i]
		key, _ := json.Marshal(r.Keys)
		rr[string(key)] = r
		if lr[string(key)] == nil {
			order = append(order, string(key))
		}
	}
	for _, key := range order {
		if err := ctx.Err(); err != nil {
			return SnapshotComparison{}, err
		}
		l, r := lr[key], rr[key]
		row := SnapshotComparisonRow{Values: make([]SnapshotDelta, len(out.ColumnKeys))}
		var lt, rt *Cell
		if l != nil {
			row.Keys, lt = l.Keys, &l.Total
		}
		if r != nil {
			row.Keys, rt = r.Keys, &r.Total
		}
		row.Total = snapshotDelta(lt, rt)
		for j, column := range out.ColumnKeys {
			var lv, rv *Cell
			if i, ok := lc[column]; ok && l != nil {
				lv = &l.Values[i]
			}
			if i, ok := rc[column]; ok && r != nil {
				rv = &r.Values[i]
			}
			row.Values[j] = snapshotDelta(lv, rv)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
