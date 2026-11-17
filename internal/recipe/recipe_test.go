package recipe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/analysis"
)

func field(id, name string, column int, mask uint16) Field {
	return Field{ID: id, Column: column, Name: name, HeaderHash: name, HeaderLoaded: true, HeaderUnique: true, Mask: mask}
}

func fixture() (Recipe, analysis.Query) {
	q := analysis.Query{Source: "wb:原来源", HeaderRow: 1, Rows: []string{"C1"}, Columns: []string{}, Value: "D_double", Aggregate: "sum", Metrics: []analysis.Metric{{Field: "D_double", Aggregate: "sum", Name: "转换金额"}, {Field: "C2", Aggregate: "max", Name: "源金额"}},
		Transforms: []analysis.Transform{{ID: "D_trim", Name: "去空格", Source: "C2", Op: "trim"}, {ID: "D_number", Name: "显式数值", Source: "D_trim", Op: "number"}, {ID: "D_double", Name: "两倍", Source: "D_number", Op: "multiply", Arg: "2"}},
		Filters:    []analysis.Filter{{Field: "C1", Op: "eq", Value: "甲"}, {Field: "D_number", Op: "gt", Value: "0"}}, Sort: "value_desc", Top: 0, Precision: 0, Chart: "bar", ChartMetric: 1}
	r := Recipe{Version: Version, Name: "我的分析方案", Fields: []Field{field("C1", "类别", 1, 2), field("C2", "金额", 2, 4)}, Steps: StepsFromQuery(q, "xlsx")}
	return r, q
}

func TestTreeRecipeUsesExplicitVersionAndPreservesHierarchyDuringRemapping(t *testing.T) {
	r, q := fixture()
	q.Layout = "tree"
	q.Rows = []string{"C1", "C2"}
	r.Version, r.Steps = VersionForQuery(q), StepsFromQuery(q, "xlsx")
	if r.Version != TreeVersion {
		t.Fatal("tree recipe did not set a rejecting legacy boundary")
	}
	replayed, _, err := r.Query("wb:new", 1, map[string]string{"C1": "C3", "C2": "C1"})
	if err != nil || replayed.Layout != "tree" || !reflect.DeepEqual(replayed.Rows, []string{"C3", "C1"}) {
		t.Fatal(replayed, err)
	}
	r.Version = Version
	if _, _, err := r.Query("wb:new", 1, map[string]string{"C1": "C3", "C2": "C1"}); err == nil {
		t.Fatal("legacy version accepted a hierarchy it cannot explain")
	}
}

func TestComparisonRecipesUseVersionThreeAndRetainModesOnEveryMetric(t *testing.T) {
	for _, layout := range []string{"", "tree"} {
		r, q := fixture()
		q.Layout = layout
		q.Metrics[0].Comparison, q.Metrics[0].ComparisonAxis = "running_percent", "columns"
		q.Metrics[1].Comparison = "percent_grand"
		r.Version, r.Steps = VersionForQuery(q), StepsFromQuery(q, "xlsx")
		if r.Version != ComparisonVersion {
			t.Fatal("comparison did not establish a legacy rejection boundary")
		}
		got, _, err := r.Query("wb:new", 1, map[string]string{"C1": "C3", "C2": "C1"})
		if err != nil || got.Layout != layout || got.Metrics[0].Comparison != "running_percent" || got.Metrics[0].ComparisonAxis != "columns" || got.Metrics[1].Field != "C1" || got.Metrics[1].Comparison != "percent_grand" {
			t.Fatal(got, err)
		}
		for _, version := range []int{Version, TreeVersion, ComparisonVersion + 1} {
			r.Version = version
			if _, _, err := r.Query("wb:new", 1, map[string]string{"C1": "C3", "C2": "C1"}); err == nil {
				t.Fatal("wrong version silently changed comparison semantics", version)
			}
		}
	}
}

func clone(r Recipe) Recipe {
	data, _ := json.Marshal(r)
	var out Recipe
	_ = json.Unmarshal(data, &out)
	return out
}

func TestRecipeRewritesEveryReferenceAndPreservesExplicitTransformChainsAndDisplayZeros(t *testing.T) {
	r, original := fixture()
	q, output, err := r.Query("wb:重排字段", 3, map[string]string{"C1": "C3", "C2": "C1"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Source != "wb:重排字段" || q.HeaderRow != 3 || q.Rows[0] != "C3" || q.Filters[0].Field != "C3" || q.Transforms[0].Source != "C1" || q.Transforms[1].Source != "D_trim" || q.Transforms[2].Source != "D_number" || q.Metrics[1].Field != "C1" || q.Value != "D_double" || q.Top != 0 || q.Precision != 0 || q.ChartMetric != 1 || output.Format != "xlsx" {
		t.Fatal("field remap changed the pipeline or omitted a secondary reference", q, output)
	}
	identity, _, err := r.Query(original.Source, original.HeaderRow, map[string]string{"C1": "C1", "C2": "C2"})
	if err != nil || !reflect.DeepEqual(identity, original) {
		t.Fatal("canonical steps changed ordinary analysis semantics", identity, original, err)
	}
	q.Rows[0] = "mutated"
	if r.Steps[len(r.Steps)-2].Group.Rows[0] != "C1" {
		t.Fatal("replay mutated the stored recipe")
	}
}

func TestRecipeRejectsInvalidStepsUnboundInputsAndDuplicateTargets(t *testing.T) {
	base, _ := fixture()
	for _, change := range []func(*Recipe){
		func(r *Recipe) { r.Version = 2 },
		func(r *Recipe) { r.Steps[0].Kind = "script" },
		func(r *Recipe) { r.Steps[0], r.Steps[3] = r.Steps[3], r.Steps[0] },
		func(r *Recipe) { r.Steps[0].Filter = &analysis.Filter{Field: "C1", Op: "eq"} },
		func(r *Recipe) { r.Steps[len(r.Steps)-2].Group.Value = "C9" },
		func(r *Recipe) { r.Steps[3].Filter.Field = "" },
		func(r *Recipe) { r.Steps[len(r.Steps)-2].Group.Rows = []string{""} },
		func(r *Recipe) { r.Steps[len(r.Steps)-1].Export.Format = "executable" },
		func(r *Recipe) { r.Fields = append(r.Fields, field("C3", "无关字段", 3, 2)) },
	} {
		r := clone(base)
		change(&r)
		mapping := map[string]string{}
		for _, f := range r.Fields {
			mapping[f.ID] = f.ID
		}
		if _, _, err := r.Query("wb:target", 1, mapping); err == nil {
			t.Fatal("invalid recipe accepted", r)
		}
	}
	for _, mapping := range []map[string]string{{"C1": "C1"}, {"C1": "C1", "C2": "C1"}, {"C1": "C1", "C2": "C2", "extra": "C3"}} {
		if _, _, err := base.Query("wb:target", 1, mapping); err == nil {
			t.Fatal("invalid mapping accepted", mapping)
		}
	}
}

func TestSuggestionsRequireUniqueCompleteNamesAndCompatibleReadSemantics(t *testing.T) {
	expected := []Field{field("C1", "类别", 1, 2), field("C2", "金额", 2, 4)}
	actual := []Field{field("C1", "金额", 1, 4), field("C2", "其他", 2, 2), field("C3", "类别", 3, 2)}
	matches := Suggest(expected, actual)
	if matches[0].Status != "match" || matches[0].Target != "C3" || matches[1].Target != "C1" {
		t.Fatal("unique names did not follow moved columns", matches)
	}
	if err := ValidateMapping(expected, actual, map[string]string{"C1": "C3", "C2": "C1"}); err != nil {
		t.Fatal(err)
	}
	actual[0].Mask = 2 // native amount-looking text is not CSV numeric amount text
	if got := Suggest(expected, actual)[1]; got.Status != "type_mismatch" || got.Target != "" {
		t.Fatal("different numerical read semantics were silently accepted", got)
	}
	if err := ValidateMapping(expected, actual, map[string]string{"C1": "C3", "C2": "C1"}); err == nil {
		t.Fatal("manual column choice silently disabled type checks")
	}
	actual[0].Mask = 4
	actual = append(actual, field("C4", "金额", 4, 4))
	if got := Suggest(expected, actual)[1]; got.Status != "ambiguous" || got.Target != "" {
		t.Fatal("duplicate names fell back to the original position", got)
	}
	if err := ValidateMapping(expected, actual, map[string]string{"C1": "C3", "C2": "C4"}); err != nil {
		t.Fatal("explicit choice did not resolve a name ambiguity", err)
	}
	for _, mutate := range []func(*Field){func(f *Field) { f.Truncated = true }, func(f *Field) { f.HeaderLoaded = false }, func(f *Field) { f.Uncertain = true }, func(f *Field) { f.Mask = 0 }, func(f *Field) { f.Mask = 6 }} {
		fields := append([]Field(nil), expected...)
		mutate(&fields[0])
		got := Suggest(fields, actual)[0]
		if got.Target != "" {
			t.Fatal("uncertain/mixed/partial field was automatically bound", got)
		}
	}
}

func TestRecordCountRecipeDoesNotManufactureAnUnusedFieldBinding(t *testing.T) {
	q := analysis.Query{Source: "wb:data", HeaderRow: 1, Aggregate: "count", Rows: []string{}, Columns: []string{}, Metrics: []analysis.Metric{}, Filters: []analysis.Filter{}, Transforms: []analysis.Transform{}}
	r := Recipe{Version: Version, Name: "全量计数", Steps: StepsFromQuery(q, "csv")}
	got, output, err := r.Query("scratch:another", 1, nil)
	if err != nil || len(analysis.RequiredFields(got)) != 0 || output.Format != "csv" {
		t.Fatal(got, output, err)
	}
	if !ValidName(strings.Repeat("😀", 80)) || ValidName(strings.Repeat("😀", 81)) {
		t.Fatal("recipe name is not bounded by Unicode code points")
	}
}
