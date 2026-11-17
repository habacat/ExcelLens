// Package recipe describes versioned, declarative analysis steps and explicit
// field bindings. Execution remains in the existing analysis engine; recipes
// contain no scripts, SQL, macros, source data or transient task results.
package recipe

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"

	"excellens/internal/analysis"
	"excellens/internal/sourceview"
)

const Version = 1
const TreeVersion = 2
const ComparisonVersion = 3
const TextMappingVersion = 4
const MaxFields = analysis.MaxSourceFields

type Field struct {
	ID           string `json:"id"`
	Column       int    `json:"column"`
	Name         string `json:"name"`
	HeaderHash   string `json:"headerHash"`
	HeaderLoaded bool   `json:"headerLoaded"`
	HeaderUnique bool   `json:"headerUnique"`
	Truncated    bool   `json:"truncated"`
	Mask         uint16 `json:"mask"`
	Uncertain    bool   `json:"uncertain"`
}

type Origin struct {
	Name        string            `json:"name"`
	Source      string            `json:"source"`
	Format      string            `json:"format"`
	Fingerprint string            `json:"fingerprint"`
	HeaderRow   int               `json:"headerRow"`
	SourceScope *sourceview.Scope `json:"sourceScope,omitempty"`
}

type Group struct {
	Rows        []string          `json:"rows"`
	Columns     []string          `json:"columns"`
	Value       string            `json:"value"`
	Aggregate   string            `json:"aggregate"`
	Metrics     []analysis.Metric `json:"metrics"`
	Sort        string            `json:"sort"`
	Top         int               `json:"top"`
	Precision   int               `json:"precision"`
	Chart       string            `json:"chart"`
	ChartMetric int               `json:"chartMetric"`
	Layout      string            `json:"layout,omitempty"`
}

type Export struct {
	Format string `json:"format"`
}

type Step struct {
	Kind      string              `json:"kind"`
	Transform *analysis.Transform `json:"transform,omitempty"`
	Filter    *analysis.Filter    `json:"filter,omitempty"`
	Group     *Group              `json:"group,omitempty"`
	Export    *Export             `json:"export,omitempty"`
}

type Recipe struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Origin  Origin  `json:"origin"`
	Fields  []Field `json:"fields"`
	Steps   []Step  `json:"steps"`
	Created string  `json:"created"`
	Updated string  `json:"updated"`
}

func StepsFromQuery(q analysis.Query, format string) []Step {
	steps := make([]Step, 0, len(q.Transforms)+len(q.Filters)+2)
	for _, transform := range q.Transforms {
		value := analysis.CloneTransform(transform)
		steps = append(steps, Step{Kind: "transform", Transform: &value})
	}
	for _, filter := range q.Filters {
		value := filter
		steps = append(steps, Step{Kind: "filter", Filter: &value})
	}
	group := &Group{Rows: append([]string{}, q.Rows...), Columns: append([]string{}, q.Columns...), Value: q.Value, Aggregate: q.Aggregate, Metrics: append([]analysis.Metric{}, q.Metrics...), Sort: q.Sort, Top: q.Top, Precision: q.Precision, Chart: q.Chart, ChartMetric: q.ChartMetric, Layout: q.Layout}
	steps = append(steps, Step{Kind: "group", Group: group}, Step{Kind: "export", Export: &Export{Format: format}})
	return steps
}

func VersionForQuery(q analysis.Query) int {
	if analysis.HasTextMappings(q) {
		return TextMappingVersion
	}
	if analysis.HasComparisons(q) {
		return ComparisonVersion
	}
	if q.Layout == "tree" {
		return TreeVersion
	}
	return Version
}

// Query rewrites every original reference, including transform chains and
// secondary metrics. Derived IDs retain their explicitly declared semantics.
func (recipe Recipe) Query(source string, header int, mapping map[string]string) (analysis.Query, Export, error) {
	q := analysis.Query{Source: source, HeaderRow: header, Rows: []string{}, Columns: []string{}, Metrics: []analysis.Metric{}, Filters: []analysis.Filter{}, Transforms: []analysis.Transform{}}
	if recipe.Version < Version || recipe.Version > TextMappingVersion || len(recipe.Fields) > MaxFields || len(recipe.Steps) < 2 || len(recipe.Steps) > 18 {
		return q, Export{}, errors.New("分析方案版本或步骤数量无效")
	}
	if !ValidName(recipe.Name) || len(recipe.Origin.Name) > 2048 || len(recipe.Origin.Source) > 512 || len(recipe.Origin.Fingerprint) > 128 || len(recipe.Origin.Format) > 32 {
		return q, Export{}, errors.New("分析方案名称或来源说明无效")
	}
	if scope := recipe.Origin.SourceScope; scope != nil {
		if scope.Fingerprint != recipe.Origin.Fingerprint || !strings.HasPrefix(recipe.Origin.Source, "wb:") {
			return q, Export{}, errors.New("分析方案来源范围不一致")
		}
		if err := scope.Validate(); err != nil {
			return q, Export{}, err
		}
	}
	original, target := make(map[string]bool), make(map[string]bool)
	fields := make([]analysis.Field, 0, len(recipe.Fields))
	for _, field := range recipe.Fields {
		if field.Column < 1 || field.Column > analysis.MaxSourceColumns || field.ID != "C"+strconv.Itoa(field.Column) || original[field.ID] || len(field.Name) > 1024 || len(field.HeaderHash) > 64 {
			return q, Export{}, errors.New("分析方案字段身份无效或重复")
		}
		id := mapping[field.ID]
		column, err := strconv.Atoi(strings.TrimPrefix(id, "C"))
		if err != nil || id != "C"+strconv.Itoa(column) || column < 1 || column > analysis.MaxSourceColumns || target[id] {
			return q, Export{}, fmt.Errorf("字段%s未绑定或目标列重复", field.Name)
		}
		original[field.ID], target[id] = true, true
		fields = append(fields, analysis.Field{ID: id, Name: field.Name, Column: column})
	}
	if len(mapping) != len(original) {
		return q, Export{}, errors.New("字段映射包含多余或缺少的绑定")
	}
	remap := func(id string) string {
		if id == "" || strings.HasPrefix(id, "D_") {
			return id
		}
		return mapping[id]
	}
	// First validate original references as well: dropping an absent source
	// reference to an empty string must never turn a broken metric into count.
	references := make(map[string]bool, len(original)+8)
	for id := range original {
		references[id] = true
	}
	check := func(id string) error {
		if id != "" && !references[id] {
			return fmt.Errorf("分析方案引用未声明字段%s", id)
		}
		return nil
	}
	phase, groups, exports := 0, 0, 0
	var output Export
	for _, step := range recipe.Steps {
		members := 0
		if step.Transform != nil {
			members++
		}
		if step.Filter != nil {
			members++
		}
		if step.Group != nil {
			members++
		}
		if step.Export != nil {
			members++
		}
		if members != 1 {
			return q, output, errors.New("分析方案步骤必须只有一个明确操作")
		}
		switch step.Kind {
		case "transform":
			if phase != 0 || step.Transform == nil {
				return q, output, errors.New("显式转换必须在筛选及分组之前")
			}
			value := analysis.CloneTransform(*step.Transform)
			if err := check(value.Source); err != nil {
				return q, output, err
			}
			if references[value.ID] {
				return q, output, errors.New("派生字段重复")
			}
			references[value.ID] = true
			value.Source = remap(value.Source)
			q.Transforms = append(q.Transforms, value)
		case "filter":
			if phase > 1 || step.Filter == nil {
				return q, output, errors.New("筛选步骤必须在分组之前")
			}
			phase = 1
			value := *step.Filter
			if value.Field == "" {
				return q, output, errors.New("筛选步骤缺少明确字段")
			}
			if err := check(value.Field); err != nil {
				return q, output, err
			}
			value.Field = remap(value.Field)
			q.Filters = append(q.Filters, value)
		case "group":
			if phase > 1 || step.Group == nil || groups != 0 {
				return q, output, errors.New("分析方案必须有且只有一个分组步骤")
			}
			phase = 2
			groups++
			group := step.Group
			if group.Layout != "" && recipe.Version < TreeVersion {
				return q, output, errors.New("层级透视分析方案要求步骤版本2")
			}
			q.Layout = group.Layout
			for _, id := range append(append([]string{}, group.Rows...), group.Columns...) {
				if id == "" {
					return q, output, errors.New("分组维度缺少明确字段")
				}
			}
			for _, id := range append(append(append([]string{}, group.Rows...), group.Columns...), group.Value) {
				if err := check(id); err != nil {
					return q, output, err
				}
			}
			for _, id := range group.Rows {
				q.Rows = append(q.Rows, remap(id))
			}
			for _, id := range group.Columns {
				q.Columns = append(q.Columns, remap(id))
			}
			q.Value, q.Aggregate, q.Sort, q.Top, q.Precision, q.Chart, q.ChartMetric = remap(group.Value), group.Aggregate, group.Sort, group.Top, group.Precision, group.Chart, group.ChartMetric
			for _, metric := range group.Metrics {
				if err := check(metric.Field); err != nil {
					return q, output, err
				}
				metric.Field = remap(metric.Field)
				q.Metrics = append(q.Metrics, metric)
			}
		case "export":
			if phase != 2 || step.Export == nil || exports != 0 {
				return q, output, errors.New("导出设置必须位于分析方案末尾")
			}
			phase = 3
			exports++
			output = *step.Export
			if output.Format != "csv" && output.Format != "xlsx" {
				return q, output, errors.New("分析方案导出格式无效")
			}
		default:
			return q, output, errors.New("分析方案包含未支持的步骤")
		}
	}
	if groups != 1 || exports != 1 || header < 1 || len(source) == 0 {
		return q, output, errors.New("分析方案分组、导出或来源设置不完整")
	}
	if recipe.Version == TreeVersion && q.Layout != "tree" {
		return q, output, errors.New("分析方案步骤版本2需要明确的层级透视布局")
	}
	if analysis.HasComparisons(q) && recipe.Version < ComparisonVersion {
		return q, output, errors.New("占比或累计分析方案要求步骤版本3")
	}
	if recipe.Version == ComparisonVersion && !analysis.HasComparisons(q) {
		return q, output, errors.New("分析方案步骤版本3需要明确的占比或累计指标")
	}
	if analysis.HasTextMappings(q) && recipe.Version < TextMappingVersion {
		return q, output, errors.New("文本映射分析方案要求步骤版本4")
	}
	if recipe.Version == TextMappingVersion && !analysis.HasTextMappings(q) {
		return q, output, errors.New("分析方案步骤版本4需要明确的文本映射")
	}
	if err := analysis.Validate(q, fields); err != nil {
		return q, output, err
	}
	needed := analysis.RequiredFields(q)
	if len(needed) != len(fields) {
		return q, output, errors.New("分析方案字段绑定与实际步骤输入不一致")
	}
	for _, id := range needed {
		if !target[id] {
			return q, output, errors.New("分析方案步骤读取了未绑定原始字段")
		}
	}
	return q, output, nil
}

func ValidName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 80 && !strings.ContainsAny(name, "\r\n\x00")
}

type Match = sourceview.HeaderMatch

func Compatible(expected, actual Field) bool {
	// Empty samples give no automatic suggestion. Explicit bindings are still
	// guarded against the unchanged expected mask over the complete input.
	return expected.Mask != 0 && !expected.Uncertain && !actual.Uncertain && actual.Mask & ^expected.Mask == 0
}

// Suggestions require a unique complete raw-header identity on both sides.
// A same-position column is never used to resolve missing or duplicate names.
func Suggest(expected, actual []Field) []Match {
	identities := func(fields []Field) []sourceview.HeaderIdentity {
		out := make([]sourceview.HeaderIdentity, len(fields))
		for i, field := range fields {
			out[i] = sourceview.HeaderIdentity{ID: field.ID, Hash: field.HeaderHash, Complete: field.HeaderLoaded && !field.Truncated, Unique: field.HeaderUnique}
		}
		return out
	}
	out := sourceview.MatchHeaders(identities(expected), identities(actual))
	byID := make(map[string]Field, len(actual))
	for _, field := range actual {
		byID[field.ID] = field
	}
	for i, field := range expected {
		match := &out[i]
		if match.Status != "match" {
			continue
		}
		candidate := byID[match.Target]
		match.Target = ""
		switch {
		case field.Uncertain || candidate.Uncertain:
			match.Status = "uncertain"
		case field.Mask == 0 || candidate.Mask == 0:
			match.Status = "untyped"
		case !Compatible(field, candidate):
			match.Status = "type_mismatch"
		case bits.OnesCount16(field.Mask) > 1 || bits.OnesCount16(candidate.Mask) > 1:
			match.Status = "mixed"
		default:
			match.Status, match.Target = "match", candidate.ID
		}
	}
	return out
}

func ValidateMapping(expected, actual []Field, mapping map[string]string) error {
	targets := make(map[string]Field, len(actual))
	for _, field := range actual {
		targets[field.ID] = field
	}
	used := make(map[string]bool)
	if len(mapping) != len(expected) {
		return errors.New("请逐项完成全部字段映射")
	}
	for _, field := range expected {
		id := mapping[field.ID]
		target, ok := targets[id]
		if !ok || used[id] {
			return fmt.Errorf("字段%s未选择目标或目标列重复", field.Name)
		}
		if !Compatible(field, target) {
			return fmt.Errorf("字段%s的读取类型或数值解释不兼容，请先在普通分析中核对并显式调整", field.Name)
		}
		used[id] = true
	}
	return nil
}
