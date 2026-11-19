package editfile

import (
	"slices"
	"testing"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
)

func TestPivotCacheOrderingThroughFormulaSourcesAndCycle(t *testing.T) {
	p := xlsxPackage{pivotRanges: map[string][]ownedRange{"Output": {{pivot: "table", area: cellRect{2, 1, 4, 2}}}}, pivotOutputs: map[string]string{"table": "first"}, pivotSources: map[string]pivotSource{
		"first": {sheet: "Input", area: cellRect{1, 1, 5, 2}}, "next": {sheet: "Intermediate", area: cellRect{1, 1, 7, 2}},
	}}
	b := CalculationBook{deps: newCalcDependencies()}
	helper := editdoc.Address{Sheet: "Intermediate", Row: 3, Col: 4}
	b.deps.add(helper, calculator.WorksheetReference{Sheet: "Output", Top: 1, Left: 2, Bottom: 800, Right: 2})
	b.deps.add(editdoc.Address{Sheet: "Intermediate", Row: 6, Col: 2}, calculator.WorksheetReference{Sheet: helper.Sheet, Top: 3, Bottom: 3, Left: 4, Right: 4})
	g, err := p.cacheGraph(t.Context(), &b)
	if err != nil {
		t.Fatal(err)
	}
	order, err := g.order("first")
	if err != nil || !slices.Equal(order, []string{"first", "next"}) {
		t.Fatalf("%v: %v", order, err)
	}
	g.link("next", "first")
	if _, err := g.order("first"); err == nil {
		t.Fatal("circular caches were accepted")
	}
}

func TestPivotValueRankCountKeepsTiesButCumulativeFilterStopsAtBoundary(t *testing.T) {
	groups := map[int]*pivotAggregate{}
	for i, n := range []string{"15", "4", "26", "26"} {
		g := new(pivotAggregate)
		g.add(pivotValue{"n", n})
		groups[i] = g
	}
	for _, tc := range []struct {
		kind  string
		limit float64
		want  []int
	}{{"topCount", 1, []int{2, 3}}, {"topPercent", 30, []int{3}}, {"bottomSum", 20, []int{0, 1, 2}}} {
		f := pivotFilter{spec: editdoc.PivotValueFilter{Kind: tc.kind, Value: tc.limit}}
		got := f.passing(groups, "sum")
		for i := range groups {
			if got[i] != slices.Contains(tc.want, i) {
				t.Fatalf("%s %v", tc.kind, got)
			}
		}
	}
}
