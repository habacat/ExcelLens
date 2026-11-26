package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func completedRecipeAnalysis(t *testing.T, a *App, q analysis.Query) *analysisTask {
	t.Helper()
	job, err := a.startAnalysisTask(a.currentWorkbook(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("analysis timed out")
	}
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	return job
}
func captureRecipe(t *testing.T, a *App, original string) *recipeCaptureTask {
	t.Helper()
	wb := a.currentWorkbook()
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/capture/start", map[string]string{"jobId": original, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "format": "csv"}))
	job := a.getRecipeCapture(started.JobID)
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("recipe capture timed out")
	}
	return job
}
func commitRecipeRequest(a *App, id, name string, revision int64) map[string]any {
	wb := a.currentWorkbook()
	return map[string]any{"id": id, "name": name, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "expectedRevision": revision}
}

func TestRecipeCaptureUsesFullInputTypesAndCommitRetriesAreIdempotent(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}
	original := completedRecipeAnalysis(t, a, q)
	capture := captureRecipe(t, a, original.ID)
	if capture.Status != "done" {
		t.Fatal(capture.Error)
	}
	path, _ := recipesPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("type capture wrote the recipe catalog before commit", err)
	}
	if len(capture.Draft.Fields) != 2 || capture.Draft.Fields[0].Mask != uint16(recipeText) || capture.Draft.Fields[1].Mask != uint16(recipeAmountText) || capture.Draft.Fields[1].HeaderHash == "" {
		t.Fatal("raw field binding or full read semantics lost", capture.Draft.Fields)
	}
	request := commitRecipeRequest(a, capture.ID, "金额核对", 0)
	first := decodeResponse[struct {
		ID       string
		Revision int64
	}](t, reconcilePost(t, a, "/api/recipes/capture/commit", request))
	again := decodeResponse[struct {
		ID       string
		Revision int64
	}](t, reconcilePost(t, a, "/api/recipes/capture/commit", request))
	if first != again || first.Revision != 1 {
		t.Fatal("lost confirmation retry duplicated or changed a recipe", first, again)
	}
	store, err := readRecipes(path)
	if err != nil || len(store.Items) != 1 || store.Items[0].Name != "金额核对" {
		t.Fatal(store, err)
	}
	request["name"] = "不同重试"
	if out := reconcilePost(t, a, "/api/recipes/capture/commit", request); out.Code == 200 {
		t.Fatal("same capture token silently changed an already committed definition")
	}
	pins := a.sourcePins.Len()
	if pins != 0 {
		t.Fatal("finished type capture retained source pins", pins)
	}
}

func TestRecipeCaptureRejectsChangedSavedTableAndUncertainFormulaCache(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "recipe-source", Name: "捕获来源", Columns: []string{"类别", "金额"}, Rows: [][]string{{"甲", "2"}}, Revision: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original := completedRecipeAnalysis(t, a, analysis.Query{Source: "scratch:recipe-source", HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheet("recipe-source").Columns[0] = "已更名"
		ws.Sheet("recipe-source").Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	capture := captureRecipe(t, a, original.ID)
	if capture.Status != "error" || !strings.Contains(capture.Error, "已修改") {
		t.Fatal("changed scratch headers rebound to an old analysis", capture.Status, capture.Error)
	}
	file := filepath.Join(t.TempDir(), "missing.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>类别</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>甲</t></is></c><c r="B2"><f>1+1</f></c></row></sheetData></worksheet>`,
	})
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb = a.currentWorkbook()
	meta := wb.Sheets[0]
	original = completedRecipeAnalysis(t, a, analysis.Query{Source: "wb:" + meta.Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	capture = captureRecipe(t, a, original.ID)
	if capture.Status != "error" || !strings.Contains(capture.Error, "缺缓存") {
		t.Fatal("uncertain source became a portable trusted schema", capture.Status, capture.Error)
	}
}

func TestRecipeCaptureCancellationAndCatalogConflictReuseValidatedDraft(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	original := completedRecipeAnalysis(t, a, analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/capture/start", map[string]string{"jobId": original.ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "format": "csv"}))
	job := a.getRecipeCapture(started.JobID)
	reconcilePost(t, a, "/api/recipes/capture/cancel", map[string]string{"id": job.ID})
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued capture cancellation blocked")
	}
	for len(a.querySem) > 0 {
		<-a.querySem
	}
	if job.Status != "cancelled" {
		t.Fatal(job.Status, job.Error)
	}
	if out := reconcilePost(t, a, "/api/recipes/capture/commit", commitRecipeRequest(a, job.ID, "不能保存", 0)); out.Code != 409 {
		t.Fatal("cancelled capture draft committed", out.Code)
	}
	first, second := captureRecipe(t, a, original.ID), captureRecipe(t, a, original.ID)
	if first.Status != "done" || second.Status != "done" {
		t.Fatal(first.Error, second.Error)
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/recipes/capture/commit", commitRecipeRequest(a, first.ID, "甲分析方案", 0)))
	request := commitRecipeRequest(a, second.ID, "乙分析方案", 0)
	if out := reconcilePost(t, a, "/api/recipes/capture/commit", request); out.Code != 409 {
		t.Fatal("stale global revision overwrote another saved recipe", out.Code)
	}
	request["expectedRevision"] = 1
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/recipes/capture/commit", request))
	path, _ := recipesPath()
	store, err := readRecipes(path)
	if err != nil || store.Revision != 2 || len(store.Items) != 2 {
		t.Fatal("retry did not reuse validated draft without rescanning", store, err)
	}
	before, _ := os.ReadFile(path)
	tampered := strings.Replace(string(before), "甲分析方案", "坏分析方案", 1)
	if err := os.WriteFile(path, []byte(tampered), 0600); err != nil {
		t.Fatal(err)
	}
	if out := analysisRequest(t, a, "GET", "/api/recipes", ""); out.Code != 422 {
		t.Fatal("corrupt catalog reset or accepted", out.Code)
	}
	after, _ := os.ReadFile(path)
	if string(after) != tampered {
		t.Fatal("catalog error destroyed evidence")
	}
}

func TestRecipeProfileCapturesLateMixedInputAndZeroFieldCount(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		rows := make([][]string, 10)
		for i := range rows {
			rows[i] = []string{"甲", "2"}
		}
		rows[9][1] = "待核对"
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "mixed", Name: "混合类型", Columns: []string{"类别", "金额"}, Rows: rows, Revision: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original := completedRecipeAnalysis(t, a, analysis.Query{Source: "scratch:mixed", HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	captured := captureRecipe(t, a, original.ID)
	if captured.Status != "done" {
		t.Fatal(captured.Error)
	}
	if captured.Draft.Fields[1].Mask != uint16(recipeText|recipeAmountText) {
		t.Fatal("captured only preview rows instead of all input types", captured.Draft.Fields)
	}
	count := completedRecipeAnalysis(t, a, analysis.Query{Source: "scratch:mixed", HeaderRow: 1, Aggregate: "count"})
	captured = captureRecipe(t, a, count.ID)
	if captured.Status != "done" || len(captured.Draft.Fields) != 0 {
		t.Fatal("field-free count captured a dummy reader field", captured.Status, captured.Error, captured.Draft.Fields)
	}
	// Reading a catalog on an unopened application is independent from data.
	b, _ := New()
	defer b.Close()
	out := analysisRequest(t, b, "GET", "/api/recipes", "")
	var value map[string]any
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &value) != nil {
		t.Fatal(out.Code, out.Body.String())
	}
}
