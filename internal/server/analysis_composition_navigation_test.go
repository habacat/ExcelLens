package server

import (
	"net/url"
	"strings"
	"testing"

	"excellens/internal/analysis"
)

func TestCompositionNavigationBindsSourceSlotBookAndActualMappedColumn(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2"}, "")
	waitCompositionAnalysis(t, job, "done")
	local := decodeResponse[struct {
		SameBook bool
		Sheet    string
		Row, Col int
	}](t, reconcilePost(t, a, "/api/analysis/composition/navigate", map[string]any{"jobId": job.ID, "input": 0, "book": definition.Inputs[0].Ref.Book, "row": 4, "col": 2}))
	if !local.SameBook || local.Sheet != "First" || local.Row != 4 || local.Col != 2 {
		t.Fatal("navigation used union sequence", local)
	}
	foreign := decodeResponse[struct {
		SameBook bool
		URL      string
	}](t, reconcilePost(t, a, "/api/analysis/composition/navigate", map[string]any{"jobId": job.ID, "input": 1, "book": definition.Inputs[1].Ref.Book, "row": 2, "col": 2}))
	target, err := url.Parse(foreign.URL)
	if err != nil || foreign.SameBook || target.Query().Get("sourceRef") != "B2" || target.Query().Get("sourceSheet") == "" || target.Query().Get("session") == "" {
		t.Fatal("foreign original column/sheet was lost", foreign, err)
	}
	for _, payload := range []map[string]any{
		{"jobId": job.ID, "input": 0, "book": definition.Inputs[1].Ref.Book, "row": 2, "col": 2},
		{"jobId": job.ID, "input": 1, "book": definition.Inputs[1].Ref.Book, "row": 2, "col": 3},
		{"jobId": job.ID, "input": 1, "book": definition.Inputs[1].Ref.Book, "row": 100, "col": 2},
		{"jobId": job.ID, "input": -1, "book": definition.Inputs[0].Ref.Book, "row": 2, "col": 2},
	} {
		if response := reconcilePost(t, a, "/api/analysis/composition/navigate", payload); response.Code == 200 {
			t.Fatal("invalid provenance silently navigated", payload)
		}
	}
	a.clearAnalysisTasks()
	if response := reconcilePost(t, a, "/api/analysis/composition/navigate", map[string]any{"jobId": job.ID, "input": 0, "book": definition.Inputs[0].Ref.Book, "row": 2, "col": 2}); response.Code != 409 || !strings.Contains(response.Body.String(), "过期") {
		t.Fatal(response.Code, response.Body.String())
	}
}
