// Package analysis implements a bounded streaming analytics engine. Source data
// is immutable; the only writable objects are separately persisted work sheets
// and report definitions. No XLSX file is ever opened for writing.
package analysis

import (
	"context"
	"fmt"
	"strings"

	"excellens/internal/sourceview"
	"excellens/internal/valueconv"
)

const MaxSourceColumns = 256
const MaxSourceFields = 80

type Value struct {
	Text             string `json:"text"`
	Display          string `json:"display,omitempty"`
	Numeric          bool   `json:"numeric,omitempty"`
	MissingCache     bool   `json:"missingCache,omitempty"`
	ConversionFailed bool   `json:"conversionFailed,omitempty"`
	Textual          bool   `json:"textual,omitempty"` // trusted text eligibility; not type inference
}
type Record struct {
	Number   int              `json:"number"`
	Values   map[string]Value `json:"values"`
	Sequence int64            `json:"sequence,omitempty"`
	Origin   *SourceOrigin    `json:"origin,omitempty"`
}
type Field struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Column  int    `json:"column"`
	Numeric bool   `json:"numeric,omitempty"`
}
type Filter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
}
type Transform struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Source  string        `json:"source"`
	Op      string        `json:"op"`
	Arg     string        `json:"arg,omitempty"`
	Mapping []TextMapping `json:"mapping,omitempty"`
}

// Metric describes an independent measure; metrics are aggregated in the same
// source scan. Empty Metrics preserves compatibility with 1.2.0 reports.
type Metric struct {
	Field          string `json:"field,omitempty"`
	Aggregate      string `json:"aggregate"`
	Name           string `json:"name,omitempty"`
	Comparison     string `json:"comparison,omitempty"`
	ComparisonAxis string `json:"comparisonAxis,omitempty"`
}
type Query struct {
	Source      string             `json:"source"`
	SourceScope *sourceview.Scope  `json:"sourceScope,omitempty"`
	Composition *sourceview.Append `json:"composition,omitempty"`
	HeaderRow   int                `json:"headerRow,omitempty"`
	Rows        []string           `json:"rows"`
	Columns     []string           `json:"columns,omitempty"`
	Value       string             `json:"value,omitempty"`
	Aggregate   string             `json:"aggregate"`
	Metrics     []Metric           `json:"metrics,omitempty"`
	Filters     []Filter           `json:"filters,omitempty"`
	Transforms  []Transform        `json:"transforms,omitempty"`
	Sort        string             `json:"sort,omitempty"`
	Top         int                `json:"top,omitempty"`
	Precision   int                `json:"precision,omitempty"`
	Chart       string             `json:"chart,omitempty"`
	ChartMetric int                `json:"chartMetric,omitempty"`
	Layout      string             `json:"layout,omitempty"`
}
type Cell struct {
	Display    string         `json:"display"`
	Exact      string         `json:"exact"`
	Count      int64          `json:"count"`
	Comparison *ComparedValue `json:"comparison,omitempty"`
}
type ResultRow struct {
	Keys         []string `json:"keys"`
	Values       []Cell   `json:"values"`
	Total        Cell     `json:"total"`
	MetricValues [][]Cell `json:"metricValues,omitempty"`
	MetricTotals []Cell   `json:"metricTotals,omitempty"`
	Count        int64    `json:"count"`
}
type Result struct {
	RowFields            []string    `json:"rowFields"`
	ColumnField          string      `json:"columnField,omitempty"`
	ColumnKeys           []string    `json:"columnKeys"`
	Rows                 []ResultRow `json:"rows"`
	Totals               []Cell      `json:"totals"`
	Metrics              []Metric    `json:"metrics,omitempty"`
	MetricGrand          []Cell      `json:"metricGrand,omitempty"`
	MetricColumnTotals   [][]Cell    `json:"metricColumnTotals,omitempty"`
	Grand                Cell        `json:"grand"`
	Scanned              int64       `json:"scanned"`
	Included             int64       `json:"included"`
	InvalidNumbers       int64       `json:"invalidNumbers"`
	InvalidConversions   int64       `json:"invalidConversions,omitempty"`
	MissingFormulaCaches int64       `json:"missingFormulaCaches"`
	Issues               []Issue     `json:"issues,omitempty"`
	IssuesTruncated      bool        `json:"issuesTruncated,omitempty"`
	Aggregation          string      `json:"aggregation"`
	Truncated            bool        `json:"truncated,omitempty"`
	Tree                 *TreeLevel  `json:"tree,omitempty"`
}
type Scan func(ctx context.Context, needed []string, visit func(Record) error) error

func validateSourceScope(q Query) error {
	if q.SourceScope == nil {
		return nil
	}
	if !strings.HasPrefix(q.Source, "wb:") {
		return fmt.Errorf("来源范围只适用于原始工作表")
	}
	return q.SourceScope.Validate()
}

// ValidateSource prevents an append descriptor from being ignored by a
// single-table reader. Its digest binds field order, mappings and all inputs.
func ValidateSource(q Query) error {
	if q.Composition == nil {
		if strings.HasPrefix(q.Source, "append:") {
			return fmt.Errorf("联合来源缺少定义，请重新准备来源")
		}
		return validateSourceScope(q)
	}
	definition, err := q.Composition.Normalized()
	if err != nil {
		return err
	}
	if q.Source != "append:"+definition.Digest() || q.SourceScope != nil || q.HeaderRow != 0 {
		return fmt.Errorf("联合来源定义、字段或范围与查询不一致")
	}
	return nil
}

func Validate(q Query, fields []Field) error {
	if err := validateLayout(q); err != nil {
		return err
	}
	if err := ValidateSource(q); err != nil {
		return err
	}
	if err := ValidateComparisons(q); err != nil {
		return err
	}
	if len(q.Source) > 512 || q.Source == "" {
		return fmt.Errorf("数据源无效")
	}
	if len(q.Rows) > 3 || len(q.Columns) > 1 || len(q.Filters) > 8 || len(q.Transforms) > 8 || len(q.Metrics) > 6 {
		return fmt.Errorf("维度、条件或计算列超出限制")
	}
	if q.Top < 0 || q.Top > 1000 || q.Precision < 0 || q.Precision > 10 {
		return fmt.Errorf("显示参数无效")
	}
	measures := queryMetrics(q)
	for _, metric := range measures {
		switch metric.Aggregate {
		case "count", "sum", "average", "min", "max", "distinct":
		default:
			return fmt.Errorf("聚合方式不支持：%s", metric.Aggregate)
		}
		if metric.Aggregate != "count" && metric.Field == "" {
			return fmt.Errorf("请选择数值字段")
		}
		if len(metric.Name) > 80 {
			return fmt.Errorf("指标名称过长")
		}
	}
	fieldSet := make(map[string]bool, len(fields)+len(q.Transforms))
	for _, f := range fields {
		fieldSet[f.ID] = true
	}
	for _, t := range q.Transforms {
		if err := ValidateTextMapping(t); err != nil {
			return err
		}
		if t.ID == "" || !strings.HasPrefix(t.ID, "D_") || len(t.ID) > 72 || fieldSet[t.ID] || len(t.Name) > 80 || len(t.Arg) > 200 {
			return fmt.Errorf("计算列名称或标识无效")
		}
		if !fieldSet[t.Source] {
			return fmt.Errorf("计算列引用了不存在的字段：%s", t.Source)
		}
		switch t.Op {
		case "trim", "upper", "lower", "year", "month", "yearmonth", "number", "text", "abs", "multiply", "divide", "prefix", "suffix", "replace", "length", "map_text":
		case "date", "time", "datetime":
			if !valueconv.ValidInput(t.Arg) {
				return fmt.Errorf("日期或时间输入格式无效：%s", t.Arg)
			}
		default:
			return fmt.Errorf("不支持计算步骤：%s", t.Op)
		}
		fieldSet[t.ID] = true
	}
	check := func(s string) error {
		if s != "" && !fieldSet[s] {
			return fmt.Errorf("字段 %s 不存在", s)
		}
		return nil
	}
	for _, f := range q.Rows {
		if err := check(f); err != nil {
			return err
		}
	}
	for _, f := range q.Columns {
		if err := check(f); err != nil {
			return err
		}
	}
	if err := check(q.Value); err != nil {
		return err
	}
	for _, metric := range q.Metrics {
		if err := check(metric.Field); err != nil {
			return err
		}
	}
	for _, f := range q.Filters {
		if err := check(f.Field); err != nil {
			return err
		}
		if len(f.Value) > 500 {
			return fmt.Errorf("筛选值过长")
		}
		switch f.Op {
		case "eq", "neq", "contains", "notcontains", "startswith", "endswith", "blank", "notblank", "gt", "gte", "lt", "lte", "between", "in":
		default:
			return fmt.Errorf("筛选条件不支持：%s", f.Op)
		}
	}
	switch q.Sort {
	case "", "value_desc", "value_asc", "label_asc", "label_desc":
	default:
		return fmt.Errorf("排序方式无效")
	}
	return nil
}

func queryMetrics(q Query) []Metric {
	if len(q.Metrics) > 0 {
		return q.Metrics
	}
	return []Metric{{Field: q.Value, Aggregate: q.Aggregate}}
}
