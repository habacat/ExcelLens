package analysis

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxGroupKeys = 5000
	MaxColumns   = 32
	MaxCells     = 20000
	MaxDistinct  = 300000
)

type queryMemory struct{ used int64 }

func (m *queryMemory) reserve(size int64) error {
	if size < 0 || size > (256<<20)-m.used {
		return errors.New("分析分类或去重文本超过256 MiB内存预算，请缩小范围或选用更短的分类字段")
	}
	m.used += size
	return nil
}

func groupKey(fields []string) string {
	var b strings.Builder
	for _, v := range fields {
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
	}
	return b.String()
}
func required(q Query) []string {
	m := map[string]struct{}{}
	add := func(id string) {
		if id != "" && !strings.HasPrefix(id, "D_") {
			m[id] = struct{}{}
		}
	}
	for _, id := range q.Rows {
		add(id)
	}
	for _, id := range q.Columns {
		add(id)
	}
	add(q.Value)
	for _, m := range q.Metrics {
		add(m.Field)
	}
	for _, f := range q.Filters {
		add(f.Field)
	}
	for _, t := range q.Transforms {
		add(t.Source)
	}
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RequiredFields returns the original inputs needed by the existing transform,
// filter and aggregation pipeline. Derived IDs are deliberately not source IDs.
func RequiredFields(q Query) []string { return required(q) }

func matches(vals map[string]Value, filters []Filter) bool {
	for _, f := range filters {
		v := vals[f.Field]
		a := v.Text
		b := f.Value
		var ok bool
		switch f.Op {
		case "eq":
			ok = a == b
		case "neq":
			ok = a != b
		case "blank":
			ok = a == ""
		case "notblank":
			ok = a != ""
		case "contains":
			ok = strings.Contains(strings.ToLower(a), strings.ToLower(b))
		case "notcontains":
			ok = !strings.Contains(strings.ToLower(a), strings.ToLower(b))
		case "startswith":
			ok = strings.HasPrefix(strings.ToLower(a), strings.ToLower(b))
		case "endswith":
			ok = strings.HasSuffix(strings.ToLower(a), strings.ToLower(b))
		case "gt", "gte", "lt", "lte":
			ad, ao := Decimal(a)
			bd, bo := Decimal(b)
			if ao && bo {
				cmp := ad.Cmp(bd)
				switch f.Op {
				case "gt":
					ok = cmp > 0
				case "gte":
					ok = cmp >= 0
				case "lt":
					ok = cmp < 0
				case "lte":
					ok = cmp <= 0
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
func withTransforms(rec Record, steps []Transform) map[string]Value {
	v := rec.Values
	if len(steps) > 0 {
		copied := make(map[string]Value, len(v)+len(steps))
		for k, val := range v {
			copied[k] = val
		}
		v = copied
		ApplyTransforms(v, steps)
	}
	return v
}

// metricGroup holds one accumulator per measure and per cross-tab column.
// Querying many measures never rescans the original (possibly multi-GB) sheet.
type metricGroup struct {
	keys    []string
	count   int64
	buckets map[string][]accumulator
	total   []accumulator
}

func Run(ctx context.Context, q Query, fields []Field, scan Scan, progress func(int64)) (Result, error) {
	if q.Layout == "tree" {
		return RunTreeLevel(ctx, q, fields, nil, scan, progress)
	}
	return runLevel(ctx, q, fields, q.Rows, nil, scan, progress)
}

func runLevel(ctx context.Context, q Query, fields []Field, rowFields, path []string, scan Scan, progress func(int64)) (Result, error) {
	var result Result
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := Validate(q, fields); err != nil {
		return result, err
	}
	metrics := queryMetrics(q)
	count := len(metrics)
	plan := compileMetrics(metrics)
	issues := compileIssues(q, fields, plan)
	stateCount := len(plan.fields)
	inputs := make([]metricInput, stateCount)
	result.Aggregation = metrics[0].Aggregate
	result.RowFields = append([]string(nil), rowFields...)
	if len(q.Columns) > 0 {
		result.ColumnField = q.Columns[0]
	}
	if len(q.Metrics) > 0 {
		result.Metrics = append([]Metric(nil), metrics...)
	}
	prepared, err := compileFilters(q.Filters)
	if err != nil {
		return result, err
	}
	groups := make(map[string]*metricGroup)
	columnNames := make(map[string]struct{})
	grand := make([]accumulator, stateCount)
	colTotals := make(map[string][]accumulator)
	var distinctUsed int
	memory := &queryMemory{}
	lastReport := time.Now()
	var scanned int64
	readQuery := q
	readQuery.Rows = rowFields
	need := required(readQuery)
	addMetrics := func(target []accumulator) error {
		for i, field := range plan.fields {
			if err := target[i].add(inputs[i], field.needs, &distinctUsed, memory); err != nil {
				return err
			}
		}
		return nil
	}
	err = scan(ctx, need, func(rec Record) error {
		scanned++
		if scanned%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if progress != nil && time.Since(lastReport) >= 250*time.Millisecond {
				progress(scanned)
				lastReport = time.Now()
			}
		}
		values := withTransforms(rec, q.Transforms)
		if !matchTreePath(values, q.Rows, path) {
			return nil
		}
		included := matchCompiled(values, prepared)
		if included {
			plan.prepare(values, inputs)
		}
		if err := issues.visit(values, inputs, included, func(kind string, field issueField, reason string) error {
			result.countIssue(kind)
			if len(result.Issues) < MaxIssueSamples {
				result.Issues = append(result.Issues, makeIssue(rec, values, kind, field, reason, IssueSampleChars))
			} else {
				result.IssuesTruncated = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !included {
			return nil
		}
		result.Included++
		rowKeys := make([]string, len(rowFields))
		for i, id := range rowFields {
			rowKeys[i] = values[id].Text
		}
		column := ""
		if len(q.Columns) > 0 {
			column = values[q.Columns[0]].Text
		}
		if _, ok := columnNames[column]; !ok {
			if err := memory.reserve(int64(len(column)) + 128); err != nil {
				return err
			}
			if len(columnNames) >= MaxColumns {
				return fmt.Errorf("列维度的分类超过 %d 个，请增加筛选条件", MaxColumns)
			}
			columnNames[column] = struct{}{}
		}
		key := groupKey(rowKeys)
		g := groups[key]
		if g == nil {
			if err := memory.reserve(int64(len(key))*2 + 256 + int64(stateCount)*128); err != nil {
				return err
			}
			if len(groups) >= MaxGroupKeys {
				return fmt.Errorf("行维度分类超过 %d 个，请先筛选或选用低基数字段", MaxGroupKeys)
			}
			g = &metricGroup{keys: rowKeys, buckets: make(map[string][]accumulator), total: make([]accumulator, stateCount)}
			groups[key] = g
		}
		bucket, exists := g.buckets[column]
		if !exists {
			if err := memory.reserve(int64(stateCount)*128 + 64); err != nil {
				return err
			}
			if len(groups)*len(columnNames)*count > MaxCells {
				return errors.New("透视表维度组合过大，请先筛选数据")
			}
			bucket = make([]accumulator, stateCount)
			g.buckets[column] = bucket
		}
		if err := addMetrics(bucket); err != nil {
			return err
		}
		if err := addMetrics(g.total); err != nil {
			return err
		}
		if err := addMetrics(grand); err != nil {
			return err
		}
		if colTotals[column] == nil {
			colTotals[column] = make([]accumulator, stateCount)
		}
		if err := addMetrics(colTotals[column]); err != nil {
			return err
		}
		g.count++
		return nil
	})
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Scanned = scanned
	result.ColumnKeys = make([]string, 0, len(columnNames))
	for key := range columnNames {
		result.ColumnKeys = append(result.ColumnKeys, key)
	}
	sort.Strings(result.ColumnKeys)
	result.Rows = make([]ResultRow, 0, len(groups))
	for _, g := range groups {
		rr := ResultRow{Keys: g.keys, Count: g.count, Values: make([]Cell, len(result.ColumnKeys)),
			MetricValues: make([][]Cell, len(result.ColumnKeys)), MetricTotals: make([]Cell, count)}
		for i, m := range metrics {
			rr.MetricTotals[i] = g.total[plan.slots[i]].result(m.Aggregate, q.Precision)
		}
		rr.Total = rr.MetricTotals[0]
		for j, col := range result.ColumnKeys {
			rr.MetricValues[j] = make([]Cell, count)
			if a, ok := g.buckets[col]; ok {
				for i, m := range metrics {
					rr.MetricValues[j][i] = a[plan.slots[i]].result(m.Aggregate, q.Precision)
				}
			}
			rr.Values[j] = rr.MetricValues[j][0]
		}
		result.Rows = append(result.Rows, rr)
	}
	result.MetricGrand = make([]Cell, count)
	for i, m := range metrics {
		result.MetricGrand[i] = grand[plan.slots[i]].result(m.Aggregate, q.Precision)
	}
	result.Grand = result.MetricGrand[0]
	result.Totals = make([]Cell, len(result.ColumnKeys))
	result.MetricColumnTotals = make([][]Cell, len(result.ColumnKeys))
	for j, col := range result.ColumnKeys {
		result.MetricColumnTotals[j] = make([]Cell, count)
		if a, ok := colTotals[col]; ok {
			for i, m := range metrics {
				result.MetricColumnTotals[j][i] = a[plan.slots[i]].result(m.Aggregate, q.Precision)
			}
		}
		result.Totals[j] = result.MetricColumnTotals[j][0]
	}
	// Compare precomputed exact numeric totals. Never sort by the rounded
	// display string or float64 (audit amounts can exceed 2^53).
	order := make(map[string]*big.Rat, len(result.Rows))
	for _, row := range result.Rows {
		v, _ := new(big.Rat).SetString(row.Total.Exact)
		order[groupKey(row.Keys)] = v
	}
	sort.SliceStable(result.Rows, func(i, j int) bool {
		a, b := result.Rows[i], result.Rows[j]
		if q.Sort == "label_asc" || q.Sort == "label_desc" {
			cmp := slices.Compare(a.Keys, b.Keys)
			if q.Sort == "label_desc" {
				return cmp > 0
			}
			return cmp < 0
		}
		x, y := order[groupKey(a.Keys)], order[groupKey(b.Keys)]
		if (x == nil) != (y == nil) {
			return x != nil // Missing aggregates sort last in either direction.
		}
		if x != nil && y != nil && x.Cmp(y) != 0 {
			if q.Sort == "value_asc" {
				return x.Cmp(y) < 0
			}
			return x.Cmp(y) > 0
		}
		return slices.Compare(a.Keys, b.Keys) < 0
	})
	if err := applyComparisons(ctx, q, &result, groups, colTotals, grand, plan, &distinctUsed, memory); err != nil {
		return Result{}, err
	}
	if q.Top > 0 && len(result.Rows) > q.Top {
		clear(result.Rows[q.Top:])
		result.Rows = result.Rows[:q.Top:q.Top]
		result.Truncated = true
	}
	if progress != nil {
		progress(scanned)
	}
	return result, nil
}

type DrillRow struct {
	Row      int           `json:"row"`
	Cells    []string      `json:"cells"`
	Sequence int64         `json:"sequence,omitempty"`
	Origin   *SourceOrigin `json:"origin,omitempty"`
}
type DrillResult struct {
	Fields  []Field    `json:"fields"`
	Rows    []DrillRow `json:"rows"`
	HasMore bool       `json:"hasMore"`
	Scanned int64      `json:"scanned"`
}

// Drill repeats the *same filters and transforms* as the pivot, returning
// physical Excel row identifiers. It never reconstructs detail from rounded
// aggregates (which would be lossy).
func Drill(ctx context.Context, q Query, fields []Field, keys []string, colKey string, offset, limit int, scan Scan) (DrillResult, error) {
	var column *string
	if len(q.Columns) > 0 {
		column = &colKey
	}
	return DrillScope(ctx, q, fields, keys, column, offset, limit, scan)
}

// A nil column means the row subtotal across every column; a pointer to ""
// selects the actual blank column category. Legacy Drill keeps its old rule.
func DrillScope(ctx context.Context, q Query, fields []Field, keys []string, column *string, offset, limit int, scan Scan) (DrillResult, error) {
	var res DrillResult
	if err := Validate(q, fields); err != nil {
		return res, err
	}
	if validateDetailKeys(q, keys) != nil || column != nil && len(q.Columns) == 0 || offset < 0 || offset > 1_048_576 || limit < 1 || limit > 200 {
		return res, errors.New("下钻范围无效")
	}
	// The UI displays a bounded number of columns, but filters and pivot
	// dimensions may reference any of the 256 available source columns.
	// Validating only the displayed prefix rejects valid drill-through on
	// wide investment ledgers. Load every required predicate column while
	// transferring at most 40 detail columns to the browser.
	prepared, err := compileFilters(q.Filters)
	if err != nil {
		return res, err
	}
	res.Fields = fields[:min(40, len(fields))]
	need := required(q)
	for _, f := range res.Fields {
		need = append(need, f.ID)
	}
	sort.Strings(need)
	need = unique(need)
	matched := 0
	err = scan(ctx, need, func(rec Record) error {
		res.Scanned++
		if res.Scanned%256 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		vals := withTransforms(rec, q.Transforms)
		if !matchCompiled(vals, prepared) {
			return nil
		}
		for i, id := range q.Rows[:len(keys)] {
			if vals[id].Text != keys[i] {
				return nil
			}
		}
		if column != nil && vals[q.Columns[0]].Text != *column {
			return nil
		}
		matched++
		if matched <= offset {
			return nil
		}
		if len(res.Rows) >= limit {
			res.HasMore = true
			return errDrillStop
		}
		cells := make([]string, len(res.Fields))
		for i, f := range res.Fields {
			cells[i] = vals[f.ID].Text
		}
		res.Rows = append(res.Rows, DrillRow{Row: rec.Number, Cells: cells, Sequence: rec.Sequence, Origin: rec.Origin})
		return nil
	})
	if err != nil && !errors.Is(err, errDrillStop) {
		return res, err
	}
	return res, nil
}

var errDrillStop = errors.New("drill page complete")

func unique(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// CollectDetails materializes a *bounded*, explicitly requested set of source
// rows into an editable snapshot. It does not mutate source data. Unlike paged
// Drill, it makes just one pass, preserving physical source-row provenance and
// the output of non-destructive calculated columns. A nil keys slice means
// "all records passing the filters"; non-nil keys select a particular group.
//
// Returned columns are: source row number, the selected original fields, then
// computed columns. It stops after the first additional matching row so the
// caller can explain truncation without ever claiming a complete extract.
func CollectDetails(ctx context.Context, q Query, fields []Field, keys []string, colKey string, limit int, scan Scan) ([]string, [][]string, bool, error) {
	var column *string
	if keys != nil && len(q.Columns) > 0 {
		column = &colKey
	}
	return CollectDetailsScope(ctx, q, fields, keys, column, limit, scan)
}

func CollectDetailsScope(ctx context.Context, q Query, fields []Field, keys []string, column *string, limit int, scan Scan) ([]string, [][]string, bool, error) {
	if err := Validate(q, fields); err != nil {
		return nil, nil, false, err
	}
	if keys != nil && validateDetailKeys(q, keys) != nil || column != nil && len(q.Columns) == 0 {
		return nil, nil, false, errors.New("明细分组条件与透视维度不匹配")
	}
	if limit < 1 || limit > 10000 {
		return nil, nil, false, errors.New("明细提取行数超出限制")
	}
	prepared, err := compileFilters(q.Filters)
	if err != nil {
		return nil, nil, false, err
	}
	// Reserve one column for the immutable original row number and up to
	// eight additional transform fields. This respects the edit-sheet width.
	provenance := detailOriginColumns(q)
	maxOriginal := min(len(fields), MaxSheetCols-len(provenance)-len(q.Transforms))
	maxOriginal = min(maxOriginal, 80-len(required(q)))
	if maxOriginal > 60 {
		maxOriginal = 60
	}
	outputFields := fields[:maxOriginal]
	columns := make([]string, 0, 1+len(outputFields)+len(q.Transforms))
	columns = append(columns, provenance...)
	need := required(q)
	for _, f := range outputFields {
		columns = append(columns, f.Name)
		need = append(need, f.ID)
	}
	for _, step := range q.Transforms {
		columns = append(columns, step.Name)
	}
	sort.Strings(need)
	need = unique(need)
	rows := make([][]string, 0, min(limit, 256))
	truncated := false
	err = scan(ctx, need, func(rec Record) error {
		vals := withTransforms(rec, q.Transforms)
		if !matchCompiled(vals, prepared) {
			return nil
		}
		if keys != nil {
			for i, field := range q.Rows[:len(keys)] {
				if vals[field].Text != keys[i] {
					return nil
				}
			}
		}
		if column != nil && vals[q.Columns[0]].Text != *column {
			return nil
		}
		if len(rows) >= limit {
			truncated = true
			return errDrillStop
		}
		row := make([]string, 0, len(columns))
		row = append(row, detailOriginCells(rec, outputFields)...)
		for _, f := range outputFields {
			row = append(row, vals[f.ID].Text)
		}
		for _, step := range q.Transforms {
			row = append(row, vals[step.ID].Text)
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil && !errors.Is(err, errDrillStop) {
		return nil, nil, false, err
	}
	return columns, rows, truncated, nil
}
