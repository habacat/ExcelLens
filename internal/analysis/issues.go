package analysis

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxIssueSamples = 100
const IssueSampleChars = 512

// Issue describes an exclusion/failed conversion, not a conclusion that the
// source is wrong. A row can have several issues; totals count field events.
type Issue struct {
	Origin        *SourceOrigin `json:"origin,omitempty"`
	Sequence      int64         `json:"sequence,omitempty"`
	Kind          string        `json:"kind"`
	Row           int           `json:"row"`
	Field         string        `json:"field"`
	FieldName     string        `json:"fieldName"`
	SourceField   string        `json:"sourceField"`
	SourceColumn  int           `json:"sourceColumn"`
	Original      string        `json:"original"`
	Input         string        `json:"input"`
	Value         string        `json:"value"`
	Reason        string        `json:"reason"`
	TextTruncated bool          `json:"textTruncated,omitempty"`
}

type issueField struct {
	id, name, source, input string
	column                  int
	step                    Transform
}

type issuePlan struct {
	metrics metricPlan
	fields  map[string]issueField
	steps   []Transform
}

func compileIssues(q Query, fields []Field, metrics metricPlan) issuePlan {
	p := issuePlan{metrics: metrics, fields: make(map[string]issueField, len(fields)+len(q.Transforms)), steps: q.Transforms}
	for _, f := range fields {
		p.fields[f.ID] = issueField{id: f.ID, name: f.Name, source: f.ID, input: f.ID, column: f.Column}
	}
	for _, step := range q.Transforms {
		src := p.fields[step.Source]
		p.fields[step.ID] = issueField{id: step.ID, name: step.Name, source: src.source, input: step.Source, column: src.column, step: step}
	}
	return p
}

// Both aggregation and extraction use this exact scope: conversion events
// cover all scanned rows; number/cache events cover included metric fields.
// Materializing strings is deferred so rows outside the bounded sample do not
// allocate diagnostic records during a large-file aggregation.
func (p issuePlan) visit(values map[string]Value, inputs []metricInput, included bool, emit func(kind string, field issueField, reason string) error) error {
	for _, step := range p.steps {
		if !values[step.ID].ConversionFailed {
			continue
		}
		reason := "显式转换失败，转换列保留原文"
		if values[step.Source].ConversionFailed {
			reason = "前序转换失败，请先核对上游计算列"
		} else {
			switch step.Op {
			case "number":
				reason = "无法按显式数值规则转换，保留原文"
			case "date", "time", "datetime", "year", "month", "yearmonth":
				reason = "日期或时间与所选输入格式不匹配，保留原文"
			case "divide":
				reason = "除数为零、参数无效或输入不能参与除法，保留原文"
			}
		}
		if err := emit("conversion", p.fields[step.ID], reason); err != nil {
			return err
		}
	}
	if !included {
		return nil
	}
	for i, field := range p.metrics.fields {
		if field.id == "" {
			continue
		}
		v := inputs[i].value
		if v.MissingCache {
			if err := emit("missing_cache", p.fields[field.id], "公式没有缓存结果，未参与该字段的统计"); err != nil {
				return err
			}
		} else if field.needs.number && v.Text != "" && inputs[i].number == nil {
			reason := "数值无法按精确数值规则解析，未计入数值聚合"
			if v.ConversionFailed {
				reason = "显式转换失败，未计入数值聚合"
			} else if !v.Numeric {
				reason = "源值为文本或其他非数值类型，未显式转为数值"
			}
			if err := emit("number", p.fields[field.id], reason); err != nil {
				return err
			}
		}
	}
	return nil
}

func issueText(text string, limit int) (string, bool) {
	if limit <= 0 {
		return text, false
	}
	count := 0
	for i := range text {
		if count == limit {
			// A substring alone could retain a multi-megabyte source value.
			return strings.Clone(text[:i]), true
		}
		count++
	}
	return strings.Clone(text), false
}

func makeIssue(rec Record, values map[string]Value, kind string, f issueField, reason string, textLimit int) Issue {
	original, a := issueText(rec.Values[f.source].Text, textLimit)
	input, b := issueText(values[f.input].Text, textLimit)
	value, c := issueText(values[f.id].Text, textLimit)
	nameLimit := 0
	if textLimit > 0 {
		nameLimit = 160
	}
	name, d := issueText(f.name, nameLimit)
	column, sourceField := f.column, f.source
	if rec.Origin != nil && column >= 1 && column <= len(rec.Origin.Columns) {
		column = rec.Origin.Columns[column-1]
		sourceField = ""
		if column > 0 {
			sourceField = "C" + strconv.Itoa(column)
		}
	}
	return Issue{Kind: kind, Row: rec.Number, Field: f.id, FieldName: name, SourceField: sourceField, SourceColumn: column, Origin: rec.Origin, Sequence: rec.Sequence,
		Original: original, Input: input, Value: value, Reason: reason, TextTruncated: a || b || c || d}
}

func (r *Result) countIssue(kind string) {
	switch kind {
	case "conversion":
		r.InvalidConversions++
	case "missing_cache":
		r.MissingFormulaCaches++
	case "number":
		r.InvalidNumbers++
	}
}

// VisitIssues scans without aggregation state or materializing a row list.
// textLimit=0 streams complete text; positive limits produce explicit snippets.
// A visitor that retains full source text owns that additional memory budget.
func VisitIssues(ctx context.Context, q Query, fields []Field, scan Scan, textLimit int, visit func(Issue) error) (Result, error) {
	var result Result
	if err := Validate(q, fields); err != nil {
		return result, err
	}
	metrics := compileMetrics(queryMetrics(q))
	p := compileIssues(q, fields, metrics)
	filters, err := compileFilters(q.Filters)
	if err != nil {
		return result, err
	}
	inputs := make([]metricInput, len(metrics.fields))
	err = scan(ctx, required(q), func(rec Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Scanned++
		values := withTransforms(rec, q.Transforms)
		included := matchCompiled(values, filters)
		if included {
			result.Included++
			metrics.prepare(values, inputs)
		}
		return p.visit(values, inputs, included, func(kind string, field issueField, reason string) error {
			result.countIssue(kind)
			return visit(makeIssue(rec, values, kind, field, reason, textLimit))
		})
	})
	if err == nil {
		err = ctx.Err()
	}
	return result, err
}

func IssueKindName(kind string) string {
	switch kind {
	case "conversion":
		return "转换失败"
	case "missing_cache":
		return "公式缺缓存"
	default:
		return "未计入数值聚合"
	}
}

func IssueColumns() []string {
	return []string{"源行号", "源列号", "源字段", "参与字段", "类别", "原文", "步骤输入", "当前值", "原因", "内容截取"}
}

func IssueCells(issue Issue) []string {
	truncated := "否"
	if issue.TextTruncated {
		truncated = "是"
	}
	return []string{strconv.Itoa(issue.Row), strconv.Itoa(issue.SourceColumn), issue.SourceField, issue.FieldName + " (" + issue.Field + ")",
		IssueKindName(issue.Kind), issue.Original, issue.Input, issue.Value, issue.Reason, truncated}
}

// CollectIssues is for the bounded editable copy, not the complete CSV export.
// It respects both the edit-sheet text limit and a 16 MiB retained-text budget.
func CollectIssues(ctx context.Context, q Query, fields []Field, scan Scan, limit int) ([][]string, bool, error) {
	if limit < 1 || limit > MaxSheetRows {
		return nil, false, errors.New("异常明细最多提取10,000项")
	}
	rows := make([][]string, 0, min(limit, 256))
	used, truncated := 0, false
	_, err := VisitIssues(ctx, q, fields, scan, MaxCellChars, func(issue Issue) error {
		row := IssueCellsForQuery(q, issue)
		size := 128
		for _, s := range row {
			// Code points are capped above; count retained UTF-8 bytes as well.
			if !utf8.ValidString(s) {
				return errors.New("异常明细含无效文本")
			}
			size += len(s) + 16
		}
		if len(rows) >= limit || used+size > 16<<20 {
			truncated = true
			return errDrillStop
		}
		used += size
		rows = append(rows, row)
		return nil
	})
	if errors.Is(err, errDrillStop) {
		err = nil
	}
	return rows, truncated, err
}
