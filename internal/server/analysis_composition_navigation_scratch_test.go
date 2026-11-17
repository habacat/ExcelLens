package server

import (
	"strings"
	"testing"

	"excellens/internal/analysis"
)

func TestCompositionNavigationRejectsChangedAnalysisSheetInsteadOfJumpingIntoNewValues(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "jump-source", Name: "分析来源", Columns: []string{"编号", "金额"}, Rows: [][]string{{"0001", "0.10"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	definition.Inputs[1].Ref = compositionRef(t, a, sourceBookID(wb), "scratch:jump-source", 0)
	definition.Inputs[1].Columns = []int{1, 2}
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2"}, "")
	waitCompositionAnalysis(t, job, "done")
	request := map[string]any{"jobId": job.ID, "input": 1, "book": definition.Inputs[1].Ref.Book, "row": 1, "col": 2}
	target := decodeResponse[struct {
		SameBook            bool
		Kind, AnalysisSheet string
		Row, Col            int
	}](t, reconcilePost(t, a, "/api/analysis/composition/navigate", request))
	if !target.SameBook || target.Kind != "analysis" || target.AnalysisSheet != "jump-source" || target.Row != 1 || target.Col != 2 {
		t.Fatal(target)
	}
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error { ws.Sheet("jump-source").Rows[0][1] = "999"; return nil }); err != nil {
		t.Fatal(err)
	}
	if response := reconcilePost(t, a, "/api/analysis/composition/navigate", request); response.Code != 409 || !strings.Contains(response.Body.String(), "已修改") {
		t.Fatal("changed scratch values masqueraded as captured original", response.Code, response.Body.String())
	}
}
