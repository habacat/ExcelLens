package server

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/recipe"
	"excellens/internal/reconcile"
)

type recipePreviewResponse struct {
	Recipe     recipe.Recipe        `json:"recipe"`
	Digest     string               `json:"digest"`
	Target     reconcile.SourceInfo `json:"target"`
	Stamp      string               `json:"stamp"`
	Fields     []recipe.Field       `json:"fields"`
	Matches    []recipe.Match       `json:"matches"`
	Sampled    int                  `json:"sampled"`
	SampleOnly bool                 `json:"sampleOnly"`
}

func saveTestRecipe(t *testing.T, a *App, q analysis.Query) recipe.Recipe {
	t.Helper()
	job := completedRecipeAnalysis(t, a, q)
	captured := captureRecipe(t, a, job.ID)
	if captured.Status != "done" {
		t.Fatal(captured.Status, captured.Error)
	}
	path, _ := recipesPath()
	store, err := readRecipes(path)
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/recipes/capture/commit", commitRecipeRequest(a, captured.ID, "分析方案", store.Revision)))
	value, err := findRecipe(captured.Draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func previewTestRecipe(t *testing.T, a *App, id, source string) recipePreviewResponse {
	t.Helper()
	wb := a.currentWorkbook()
	return decodeResponse[recipePreviewResponse](t, reconcilePost(t, a, "/api/recipes/preview", map[string]any{"id": id, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "target": reconcileSourceRef{Book: sourceBookID(wb), Source: source, HeaderRow: 1}}))
}

func recipeRunRequest(a *App, preview recipePreviewResponse, source string, mapping map[string]string) map[string]any {
	wb := a.currentWorkbook()
	return map[string]any{"id": preview.Recipe.ID, "digest": preview.Digest, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "target": reconcileSourceRef{Book: sourceBookID(wb), Source: source, HeaderRow: 1, Stamp: preview.Target.Stamp}, "stamp": preview.Stamp, "mapping": mapping}
}

func runTestRecipe(t *testing.T, a *App, request map[string]any) *analysisTask {
	t.Helper()
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/run/start", request))
	job := a.getAnalysisJob(started.JobID)
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("replay timed out")
	}
	return job
}

func openRecipeCSV(t *testing.T, a *App, contents string) (string, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "target.csv")
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	return "wb:" + a.currentWorkbook().Sheets[0].Name, file
}

func TestRecipeReplayReordersAllReferencesAndPreservesExactResultsAndCoordinates(t *testing.T) {
	a, original := namedViewFixture(t)
	before, _ := os.ReadFile(original)
	q := analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "D_scaled", Aggregate: "sum", Transforms: []analysis.Transform{{ID: "D_double", Name: "倍数", Source: "C2", Op: "multiply", Arg: "2"}, {ID: "D_scaled", Name: "比例", Source: "D_double", Op: "divide", Arg: "10"}}, Filters: []analysis.Filter{{Field: "C3", Op: "notblank"}}, Metrics: []analysis.Metric{{Field: "D_scaled", Aggregate: "sum"}, {Field: "C2", Aggregate: "sum"}, {Field: "C3", Aggregate: "distinct"}}, Precision: 3}
	value := saveTestRecipe(t, a, q)
	source, target := openRecipeCSV(t, a, "编号,金额,项目\n001,9007199254740993.31,甲\n002,9007199254740993.30,甲\n")
	targetBefore, _ := os.ReadFile(target)
	preview := previewTestRecipe(t, a, value.ID, source)
	mapping := map[string]string{}
	for _, match := range preview.Matches {
		if match.Status != "match" {
			t.Fatal(match)
		}
		mapping[match.Field] = match.Target
	}
	if !reflect.DeepEqual(mapping, map[string]string{"C1": "C3", "C2": "C2", "C3": "C1"}) {
		t.Fatal(mapping)
	}
	job := runTestRecipe(t, a, recipeRunRequest(a, preview, source, mapping))
	if job.Status != "done" || job.Result.Included != 2 || len(job.Result.Rows) != 1 || job.Result.MetricGrand[0].Exact != "3602879701896397.322" || job.Result.MetricGrand[1].Exact != "18014398509481986.61" || job.Result.MetricGrand[2].Exact != "2" {
		t.Fatal(job.Status, job.Error, job.Result)
	}
	if job.Query.Transforms[0].Source != "C2" || job.Query.Transforms[1].Source != "D_double" || job.Query.Filters[0].Field != "C1" || job.Query.Rows[0] != "C3" {
		t.Fatal(job.Query)
	}
	status := decodeResponse[struct {
		Query        analysis.Query
		ExportFormat string
		SourceFields []analysis.Field
	}](t, analysisRequest(t, a, "GET", "/api/recipes/run/status?id="+job.ID, ""))
	actualQuery, _ := json.Marshal(status.Query)
	expectedQuery, _ := json.Marshal(job.Query)
	if status.ExportFormat != "csv" || string(actualQuery) != string(expectedQuery) || len(status.SourceFields) != 3 {
		t.Fatal(status)
	}
	drill := decodeResponse[struct{ Rows []analysis.DrillRow }](t, reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": job.Query, "keys": []string{"甲"}, "limit": 10}))
	if len(drill.Rows) != 2 || drill.Rows[0].Row != 2 || drill.Rows[1].Row != 3 {
		t.Fatal(drill)
	}
	after, _ := os.ReadFile(original)
	targetAfter, _ := os.ReadFile(target)
	if sha256.Sum256(before) != sha256.Sum256(after) || sha256.Sum256(targetBefore) != sha256.Sum256(targetAfter) {
		t.Fatal("recipe changed source bytes")
	}
}

func TestRecipePreviewRequiresExplicitBindingForDuplicateAndMissingHeaders(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	source, _ := openRecipeCSV(t, a, "项目,项目,金额,\n甲,乙,2,备注\n")
	preview := previewTestRecipe(t, a, value.ID, source)
	if preview.Matches[0].Status != "ambiguous" || preview.Matches[0].Target != "" || preview.Fields[3].HeaderLoaded {
		t.Fatal(preview)
	}
	if out := reconcilePost(t, a, "/api/recipes/run/start", recipeRunRequest(a, preview, source, map[string]string{"C2": "C3"})); out.Code != 409 {
		t.Fatal("missing mapping guessed", out.Code, out.Body.String())
	}
	job := runTestRecipe(t, a, recipeRunRequest(a, preview, source, map[string]string{"C1": "C2", "C2": "C3"}))
	if job.Status != "done" || job.Result.Rows[0].Keys[0] != "乙" || job.Result.Grand.Exact != "2" {
		t.Fatal(job.Status, job.Error, job.Result)
	}
}

func TestRecipeEmptyPreviewBindingsStillGuardAllLaterInput(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	for _, late := range []string{"2", "待核对"} {
		source, _ := openRecipeCSV(t, a, "项目,金额\n"+strings.Repeat("甲,\n", 8)+"甲,"+late+"\n")
		preview := previewTestRecipe(t, a, value.ID, source)
		if preview.Sampled != 8 || !preview.SampleOnly || preview.Matches[1].Status != "untyped" || preview.Matches[1].Target != "" {
			t.Fatal(preview)
		}
		job := runTestRecipe(t, a, recipeRunRequest(a, preview, source, map[string]string{"C1": "C1", "C2": "C2"}))
		status := decodeResponse[map[string]json.RawMessage](t, analysisRequest(t, a, "GET", "/api/recipes/run/status?id="+job.ID, ""))
		if late == "2" {
			if job.Status != "done" || job.Result.Grand.Exact != "2" {
				t.Fatal(job.Status, job.Error)
			}
		} else if job.Status != "error" || !strings.Contains(job.Error, "B10") || status["result"] != nil {
			t.Fatal("late incompatible value published", job.Status, job.Error, status)
		}
	}
}

func TestRecipeReplayRejectsDefinitionSourceAndWorkbookChanges(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	if err := a.editAnalysis(a.currentWorkbook(), func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "target", Name: "目标", Columns: []string{"项目", "金额"}, Rows: [][]string{{"甲", "2"}}, Revision: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := "scratch:target"
	preview := previewTestRecipe(t, a, value.ID, source)
	request := recipeRunRequest(a, preview, source, map[string]string{"C1": "C1", "C2": "C2"})
	request["digest"] = "stale"
	if out := reconcilePost(t, a, "/api/recipes/run/start", request); out.Code != 409 || !strings.Contains(out.Body.String(), "定义") {
		t.Fatal(out.Code, out.Body.String())
	}
	request["digest"] = preview.Digest
	if err := a.editAnalysis(a.currentWorkbook(), func(ws *analysis.Workspace) error {
		sheet := ws.Sheet("target")
		sheet.Columns[1] = "变更表头"
		sheet.Rows[0][1] = "3"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := reconcilePost(t, a, "/api/recipes/run/start", request); out.Code != 409 {
		t.Fatal("scratch reused numeric revision", out.Code, out.Body.String())
	}
	openRecipeCSV(t, a, "项目,金额\n甲,2\n")
	if out := reconcilePost(t, a, "/api/recipes/run/start", request); out.Code != 409 {
		t.Fatal("obsolete workbook accepted", out.Code)
	}
}

func TestRecipeCrossFormatTextAmountIsNotNativeExcelText(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	file := filepath.Join(t.TempDir(), "text.xlsx")
	writeZipArchive(t, file, map[string]string{"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`, "xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>项目</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>甲</t></is></c><c r="B2" t="inlineStr"><is><t>2</t></is></c></row></sheetData></worksheet>`})
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	preview := previewTestRecipe(t, a, value.ID, "wb:Data")
	if preview.Matches[1].Status != "type_mismatch" || preview.Matches[1].Target != "" {
		t.Fatal(preview.Matches)
	}
	if out := reconcilePost(t, a, "/api/recipes/run/start", recipeRunRequest(a, preview, "wb:Data", map[string]string{"C1": "C1", "C2": "C2"})); out.Code != 409 {
		t.Fatal("manual binding bypassed source type", out.Code, out.Body.String())
	}
}

func TestRecipeRemembersDuplicateHeadersOutsideItsRequiredFields(t *testing.T) {
	a, _ := namedViewFixture(t)
	source, _ := openRecipeCSV(t, a, "项目,项目,金额\n甲,乙,2\n")
	value := saveTestRecipe(t, a, analysis.Query{Source: source, HeaderRow: 1, Rows: []string{"C1"}, Value: "C3", Aggregate: "sum"})
	if value.Fields[0].HeaderUnique {
		t.Fatal("unused duplicate header did not invalidate automatic matching", value.Fields)
	}
	source, _ = openRecipeCSV(t, a, "项目,金额\n甲,2\n")
	preview := previewTestRecipe(t, a, value.ID, source)
	if preview.Matches[0].Status != "ambiguous" || preview.Matches[0].Target != "" {
		t.Fatal("ambiguous origin became a unique binding", preview.Matches)
	}
	// A bounded read cannot prove uniqueness outside its known header scope.
	fields := make([]reconcile.Field, maxSourceHeaderColumns+1)
	fields[0] = reconcile.Field{Column: 1, Name: "项目", RawName: "项目", HeaderHash: "hash", HeaderLoaded: true}
	if recipeSourceFields(reconcile.SourceInfo{Kind: "workbook", Fields: fields})[0].HeaderUnique {
		t.Fatal("unread columns were treated as known unique names")
	}
}

func TestRecipeVeryWidePreviewBoundsTransportAndDoesNotClaimUnreadHeaderUniqueness(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	headers, values := make([]string, maxSourceHeaderColumns+1), make([]string, maxSourceHeaderColumns+1)
	for i := range headers {
		headers[i] = "其他字段"
		values[i] = "备注"
	}
	headers[0], headers[1], values[0], values[1] = "项目", "金额", "甲", "2"
	source, _ := openRecipeCSV(t, a, strings.Join(headers, ",")+"\n"+strings.Join(values, ",")+"\n")
	preview := previewTestRecipe(t, a, value.ID, source)
	if len(preview.Fields) != analysis.MaxSourceColumns || len(preview.Target.Fields) != analysis.MaxSourceColumns || preview.Matches[0].Status != "ambiguous" || preview.Matches[0].Target != "" {
		t.Fatal("wide metadata expanded or guessed", len(preview.Fields), len(preview.Target.Fields), preview.Matches)
	}
	job := runTestRecipe(t, a, recipeRunRequest(a, preview, source, map[string]string{"C1": "C1", "C2": "C2"}))
	if job.Status != "done" || job.Result.Grand.Exact != "2" {
		t.Fatal("manual bound columns did not replay", job.Status, job.Error)
	}
}

func TestRecipeQueuedReplayCancellationAndEditedSnapshotDoNotPublish(t *testing.T) {
	a, _ := namedViewFixture(t)
	value := saveTestRecipe(t, a, analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum"})
	if err := a.editAnalysis(a.currentWorkbook(), func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "queue", Name: "排队来源", Columns: []string{"项目", "金额"}, Rows: [][]string{{"甲", "2"}}, Revision: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, cancel := range []bool{true, false} {
		preview := previewTestRecipe(t, a, value.ID, "scratch:queue")
		for i := 0; i < cap(a.analysisSem); i++ {
			a.analysisSem <- struct{}{}
		}
		started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/recipes/run/start", recipeRunRequest(a, preview, "scratch:queue", map[string]string{"C1": "C1", "C2": "C2"})))
		job := a.getAnalysisJob(started.JobID)
		if cancel {
			reconcilePost(t, a, "/api/recipes/run/cancel", map[string]string{"id": job.ID})
		} else if err := a.editAnalysis(a.currentWorkbook(), func(ws *analysis.Workspace) error { ws.Sheet("queue").Rows[0][1] = "3"; return nil }); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < cap(a.analysisSem); i++ {
			<-a.analysisSem
		}
		select {
		case <-job.done:
		case <-time.After(5 * time.Second):
			t.Fatal("queued replay did not exit")
		}
		status := decodeResponse[map[string]json.RawMessage](t, analysisRequest(t, a, "GET", "/api/recipes/run/status?id="+job.ID, ""))
		if status["result"] != nil || cancel && job.Status != "cancelled" || !cancel && job.Status != "error" {
			t.Fatal(job.Status, job.Error, status)
		}
	}
}
