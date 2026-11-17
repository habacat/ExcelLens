package server

import (
	"fmt"
	"testing"

	"excellens/internal/analysis"
)

func TestRecipeTargetSelectionPreservesColumnIDsPreviewStampAndQueuedSource(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:First", HeaderRow: 1, Value: "C2", Aggregate: "sum", Precision: 2})
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "selection", ViewID: view, Start: "B4", End: "B6"})
	wb := a.currentWorkbook()
	preview := decodeResponse[recipePreviewResponse](t, reconcilePost(t, a, "/api/recipes/preview", map[string]any{"id": value.ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "target": reconcileSourceRef{Book: sourceBookID(wb), Source: "wb:First", HeaderRow: 1}, "sourceId": captured.ID}))
	if preview.Sampled != 2 || preview.Target.Total != 2 || len(preview.Fields) != 1 || preview.Fields[0].ID != "C2" || preview.Fields[0].Mask != uint16(recipeNumber) || preview.Target.Fields[0].Column != 2 || preview.Stamp != captured.SourceScope.Digest() {
		t.Fatal("target preview widened, renumbered or sampled outside selection", preview)
	}
	request := recipeRunRequest(a, preview, "wb:First", map[string]string{"C2": "C2"})
	// A preview cannot become a full-source or a different-scope replay.
	if got := reconcilePost(t, a, "/api/recipes/run/start", request); got.Code != 409 {
		t.Fatal("missing selected owner silently widened", got.Code, got.Body.String())
	}
	other := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "selection", ViewID: view, Start: "B7", End: "B7"})
	request["sourceId"] = other.ID
	if got := reconcilePost(t, a, "/api/recipes/run/start", request); got.Code != 409 {
		t.Fatal("substituted source accepted", got.Code, got.Body.String())
	}
	request["sourceId"] = captured.ID
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	released := false
	release := func() {
		if !released {
			released = true
			for i := 0; i < cap(a.analysisSem); i++ {
				<-a.analysisSem
			}
		}
	}
	t.Cleanup(release)
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/run/start", request))
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	release()
	result := awaitAnalysis(t, a, started.JobID)
	q := analysisStatus(t, a, started.JobID)
	if result.Scanned != 2 || result.Grand.Exact != "9007199254740993.31" || q.SourceScope == nil || q.SourceScope.Digest() != captured.SourceScope.Digest() {
		t.Fatal("queued replay lost selected rows or types", result, q)
	}
	detail := decodeResponse[analysis.DrillResult](t, reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": q, "jobId": started.JobID, "limit": 100}))
	if len(detail.Rows) != 2 || detail.Rows[0].Row != 4 || detail.Rows[1].Row != 6 {
		t.Fatal("recipe result drill changed source", detail)
	}
	if got := reconcilePost(t, a, "/api/recipes/preview", map[string]any{"id": value.ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "target": reconcileSourceRef{Book: sourceBookID(wb), Source: "wb:First", HeaderRow: 1}, "sourceId": captured.ID}); got.Code != 409 {
		t.Fatal("expired selection owner accepted", got.Code)
	}
}

func TestRecipeTargetBlankSelectionBeyondUsedColumnsKeepsOriginalAddresses(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "selection", ViewID: view, Start: "B8", End: "C10"})
	wb := a.currentWorkbook()
	snapshot, err := a.analysisSourceSnapshot(captured.ID, wb)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	a.cacheMu.RLock()
	schema, err := a.recipeTargetSchema(t.Context(), wb, reconcileSourceRef{Book: sourceBookID(wb), Source: "wb:First", HeaderRow: 1}, snapshot)
	a.cacheMu.RUnlock()
	if err != nil || len(schema.Fields) != 2 || schema.Fields[0].ID != "C2" || schema.Fields[1].ID != "C3" || schema.Fields[0].Mask != 0 || schema.Fields[1].HeaderLoaded || schema.Sampled != 3 || schema.Info.Total != 3 || schema.Scope.Mode != "selection" {
		t.Fatal("blank range was truncated or moved", schema, err)
	}
}
