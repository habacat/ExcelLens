package recipe

import (
	"testing"

	"excellens/internal/analysis"
)

func TestTextMappingRecipeVersionRebindingAndCopyIsolation(t *testing.T) {
	q := analysis.Query{Source: "wb:A", Rows: []string{"D_map"}, Value: "C2", Aggregate: "sum",
		Transforms: []analysis.Transform{{ID: "D_map", Name: "机构", Source: "C1", Op: "map_text", Mapping: []analysis.TextMapping{{From: "a", To: "A"}}}}}
	r := Recipe{Version: VersionForQuery(q), Name: "文本核对", Fields: []Field{field("C1", "机构", 1, 2), field("C2", "金额", 2, 1)}, Steps: StepsFromQuery(q, "csv")}
	if r.Version != TextMappingVersion {
		t.Fatal(r.Version)
	}
	q.Transforms[0].Mapping[0].To = "changed"
	if r.Steps[0].Transform.Mapping[0].To != "A" {
		t.Fatal("capture retained caller's mutable mapping")
	}
	got, _, err := r.Query("wb:B", 2, map[string]string{"C1": "C3", "C2": "C1"})
	if err != nil || got.Transforms[0].Source != "C3" || got.Transforms[0].Mapping[0].To != "A" {
		t.Fatal(got, err)
	}
	got.Transforms[0].Mapping[0].To = "mutated"
	if r.Steps[0].Transform.Mapping[0].To != "A" {
		t.Fatal("replay mutated saved rule")
	}
	for _, version := range []int{Version, TreeVersion, ComparisonVersion, TextMappingVersion + 1} {
		r.Version = version
		if _, _, err := r.Query("wb:B", 2, map[string]string{"C1": "C3", "C2": "C1"}); err == nil {
			t.Fatal("unsupported recipe version lost mapping", version)
		}
	}
}
