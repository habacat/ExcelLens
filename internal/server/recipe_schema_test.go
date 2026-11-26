package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/workbook"
)

func TestRecipeTypeGuardDetectsLateNumericInterpretationChangeBeforePublishingAnalysis(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "late-type.csv")
	data := "类别,金额\n" + strings.Repeat("甲,2\n", 9) + "甲,金额待核对\n"
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	q := analysis.Query{Source: "wb:" + meta.Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}
	job, err := a.startAnalysisTask(wb, q, &recipeRunPlan{Stamp: wb.Fingerprint, Masks: map[int]recipeTypeMask{1: recipeText, 2: recipeAmountText}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("guarded analysis timed out")
	}
	status := decodeResponse[map[string]any](t, analysisRequest(t, a, "GET", "/api/analysis/query/status?id="+job.ID, ""))
	if status["status"] != "error" || status["result"] != nil || job.Result.Included != 0 || !strings.Contains(job.Error, "B11") || !strings.Contains(job.Error, "未发布部分结果") {
		t.Fatal("failed recipe exposed a partial successful result", status)
	}
	after, _ := os.ReadFile(file)
	if string(after) != data {
		t.Fatal("guard rewrote the source")
	}
}

func TestAnalysisResultKeepsItsFieldCaptionsAfterSavedEditorChanges(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "caption", Name: "源编辑表", Columns: []string{"原分类", "原金额"}, Rows: [][]string{{"甲", "2"}}, Revision: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q := analysis.Query{Source: "scratch:caption", HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}
	job, err := a.startAnalysisTask(wb, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("analysis timed out")
	}
	if job.Status != "done" {
		t.Fatal(job.Error)
	}
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		sheet := ws.Sheet("caption")
		sheet.Columns = []string{"新分类", "新金额"}
		sheet.Rows[0][1] = "99"
		sheet.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	exported := analysisRequest(t, a, "GET", "/api/analysis/export?id="+job.ID+"&format=csv", "")
	if exported.Code != 200 || !strings.Contains(exported.Body.String(), "原分类") || !strings.Contains(exported.Body.String(), "原金额") || strings.Contains(exported.Body.String(), "新金额") || !strings.Contains(exported.Body.String(), "\n甲,2\n") {
		t.Fatal("old result was relabelled or recalculated from current source", exported.Body.String())
	}
	created := decodeResponse[struct{ ID string }](t, reconcilePost(t, a, "/api/analysis/materialize", map[string]string{"jobId": job.ID, "name": "完成结果"}))
	workspace, err := a.loadAnalysis(wb)
	if err != nil {
		t.Fatal(err)
	}
	sheet := workspace.Sheet(created.ID)
	if sheet == nil || sheet.Columns[0] != "原分类" || !strings.Contains(sheet.Columns[1], "原金额") {
		t.Fatal("result sheet used a later source header", sheet)
	}
}

func TestAnalysisTaskCapacityAndCloseWaitForCancelledSourcePins(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Aggregate: "count"}
	a.analysisSem <- struct{}{}
	for i := 0; i < 12; i++ {
		if _, err := a.startAnalysisTask(wb, q, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.startAnalysisTask(wb, q, nil); err == nil {
		t.Fatal("untracked active analysis tasks were evicted to bypass capacity")
	}
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("close did not unwind queued analysis tasks")
	}
	<-a.analysisSem
	pins := a.sourcePins.Len()
	if pins != 0 || len(a.analysisJobs) != 0 {
		t.Fatal("close returned before its source references were released", pins, len(a.analysisJobs))
	}
}

func TestRecipeObserverPreservesOrdinaryAnalysisAndDistinguishesNativeTextFromCSVAmountText(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}
	plain, _ := a.source(wb, q.Source, 1)
	var mask recipeTypeMask
	observed, err := a.sourceObserved(wb, q.Source, 1, func(row int, cell workbook.CellData, numeric bool) error {
		if cell.Col == 2 {
			mask |= recipeCellMask(wb.Format, cell, numeric)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	aResult, ae := analysis.Run(context.Background(), q, plain.fields, plain.scan, nil)
	bResult, be := analysis.Run(context.Background(), q, observed.fields, observed.scan, nil)
	if ae != nil || be != nil || !reflect.DeepEqual(aResult, bResult) || mask != recipeAmountText {
		t.Fatal("opt-in observation changed the actual analytics result", mask, ae, be)
	}
	text := workbook.CellData{Col: 1, Type: "inlineStr", Raw: "1234.50"}
	native := &workbook.Workbook{Format: "xlsx"}
	csv := &workbook.Workbook{Format: "csv"}
	if recipeCellMask("xlsx", text, sourceCellNumeric(native, text)) != recipeText || recipeCellMask("csv", text, sourceCellNumeric(csv, text)) != recipeAmountText {
		t.Fatal("same text bytes were promoted across different numeric source semantics")
	}
	text.Raw = "001234"
	if recipeCellMask("csv", text, sourceCellNumeric(csv, text)) != recipeText {
		t.Fatal("leading-zero identifier was promoted to a monetary type")
	}
	text.Type, text.Raw = "n", ""
	text.CacheMissing = true
	if !recipeCellMask("xlsx", text, false).uncertain() {
		t.Fatal("missing formula cache disguised as a blank typed value")
	}
}
