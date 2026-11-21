package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/sourceview"
)

func compositionAnalysisStart(t *testing.T, a *App, q analysis.Query, id string) *analysisTask {
	t.Helper()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if id != "" {
		payload["sourceId"] = id
	}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/query/start", payload))
	job := a.getAnalysisJob(started.JobID)
	if job == nil {
		t.Fatal("analysis task missing")
	}
	return job
}

func waitCompositionAnalysis(t *testing.T, job *analysisTask, status string) analysis.Result {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(20 * time.Second):
		t.Fatal("append analysis did not finish")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != status {
		t.Fatalf("append analysis %s: %s", job.Status, job.Error)
	}
	return job.Result
}

func TestCompositionAnalysisMixedFormatsTraceDetailsAndRetainAfterTaskClear(t *testing.T) {
	a, file := recordFixture(t, 12, "")
	before := printScaleHash(t, file)
	definition := compositionDefinition(t, a)
	definition.Fields = append(definition.Fields, "源文本金额", "缺缓存")
	definition.Inputs[0].Columns = append(definition.Inputs[0].Columns, 3, 6)
	other := attachReconcileCSV(t, a, "amounts.csv", "金额,编号,文本金额\n0.1,0002,0.1\n-2.25,0003,-2.25\n")
	otherBook := a.resolveReconcileBook(other)
	definition.Inputs[1] = sourceview.AppendInput{Ref: compositionRef(t, a, other, "wb:"+otherBook.Sheets[0].Name, 1), Columns: []int{2, 1, 3, 0}}
	prepared := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, prepared, "done")
	source := "append:" + definition.Digest()
	fields := decodeResponse[struct {
		Fields               []analysis.Field
		Preview              []analysis.DrillRow
		HeaderRow, TotalRows int
		Composition          *sourceview.Append
	}](t, analysisRequest(t, a, "GET", "/api/analysis/fields?source="+source+"&sourceId="+prepared.ID, ""))
	if fields.HeaderRow != 0 || fields.TotalRows != 4 || fields.Composition == nil || !fields.Fields[1].Numeric || fields.Fields[2].Numeric || len(fields.Preview) != 4 || fields.Preview[2].Origin.Book != definition.Inputs[1].Ref.Book || fields.Preview[2].Row != 2 || fields.Preview[2].Sequence != 3 {
		t.Fatal("field preview changed source identity or numeric qualification", fields)
	}
	profile := decodeResponse[struct{ Rows, Numeric int }](t, analysisRequest(t, a, "GET", "/api/analysis/profile?source="+source+"&sourceId="+prepared.ID+"&field=C3", ""))
	if profile.Rows != 4 || profile.Numeric != 2 {
		t.Fatal("native Excel text became an amount or CSV amounts were lost", profile)
	}
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := 0; i < cap(a.analysisSem); i++ {
				<-a.analysisSem
			}
		})
	}
	t.Cleanup(release)
	q := analysis.Query{Source: source, Aggregate: "sum", Value: "C2", Precision: 2, Metrics: []analysis.Metric{{Aggregate: "sum", Field: "C2"}, {Aggregate: "sum", Field: "C3"}, {Aggregate: "sum", Field: "C4"}}}
	job := compositionAnalysisStart(t, a, q, prepared.ID)
	clearComposition(t, a, prepared.ID)
	release()
	result := waitCompositionAnalysis(t, job, "done")
	if result.Scanned != 4 || result.Included != 4 || result.Grand.Exact != "9007199254740993.41" || result.MetricGrand[1].Exact != "-2.15" || result.InvalidNumbers != 1 || result.MissingFormulaCaches != 1 {
		t.Fatal("mixed format exact aggregation changed", result)
	}
	q = analysisStatus(t, a, job.ID)
	if q.Composition == nil || q.HeaderRow != 0 || q.Composition.Digest() != definition.Digest() {
		t.Fatal("completed query lost its descriptor", q)
	}
	drill := decodeResponse[analysis.DrillResult](t, reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": q, "jobId": job.ID, "limit": 100}))
	if len(drill.Rows) != 4 || drill.Rows[1].Row != 4 || drill.Rows[2].Row != 2 || drill.Rows[2].Sequence != 3 || drill.Rows[2].Cells[0] != "0002" || drill.Rows[2].Origin.Columns[0] != 2 || drill.Rows[0].Origin.File == drill.Rows[2].Origin.File {
		t.Fatal("union sequence was mistaken for physical source coordinates", drill)
	}
	detail := decodeResponse[struct{ ID string }](t, reconcilePost(t, a, "/api/analysis/detail/materialize", map[string]any{"name": "联合明细", "query": q, "jobId": job.ID, "limit": 100}))
	ws, err := a.loadAnalysis(a.currentWorkbook())
	if err != nil {
		t.Fatal(err)
	}
	sheet := ws.Sheet(detail.ID)
	if sheet == nil || len(sheet.Rows) != 4 || sheet.Columns[0] != "联合序号" || sheet.Rows[2][0] != "3" || sheet.Rows[2][4] != "2" || sheet.Rows[2][5] != "[2,1,3,0]" || sheet.Rows[2][6] != "0002" {
		t.Fatal("materialized provenance lost source mapping", sheet)
	}
	if len(result.Issues) != 2 || result.Issues[0].Origin == nil || result.Issues[0].SourceColumn != 3 || result.Issues[1].SourceColumn != 6 {
		t.Fatal("issue points to output column instead of native source column", result.Issues)
	}
	issues := decodeResponse[struct{ ID string }](t, reconcilePost(t, a, "/api/analysis/issues/materialize", map[string]any{"jobId": job.ID, "name": "联合异常", "limit": 100}))
	ws, err = a.loadAnalysis(a.currentWorkbook())
	if err != nil || len(ws.Sheet(issues.ID).Rows) != 2 || ws.Sheet(issues.ID).Rows[0][0] != drill.Rows[0].Origin.File || ws.Sheet(issues.ID).Rows[0][5] != "3" {
		t.Fatal("issues lost native file/column", ws.Sheet(issues.ID), err)
	}
	export := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/issues/export", map[string]any{"jobId": job.ID}))
	a.mu.RLock()
	exportJob := a.exports[export.JobID]
	a.mu.RUnlock()
	if exportJob == nil {
		t.Fatal("issue export task missing")
	}
	select {
	case <-exportJob.done:
	case <-time.After(10 * time.Second):
		t.Fatal("issue export did not finish")
	}
	exportJob.mu.RLock()
	path, state, message := exportJob.FilePath, exportJob.Status, exportJob.Error
	exportJob.mu.RUnlock()
	if state != "done" {
		t.Fatal(state, message)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][0] != drill.Rows[0].Origin.File || rows[1][5] != "3" || rows[1][14] != "1" {
		t.Fatal("exported exception provenance differs", rows, err)
	}
	if response := reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": q, "limit": 10}); response.Code == 200 {
		t.Fatal("union details silently rebuilt without a completed source")
	}
	a.clearAnalysisTasks()
	if compositionUsed(a) != 0 || printScaleHash(t, file) != before {
		t.Fatal("source budget leaked or original file changed", compositionUsed(a))
	}
}

func TestCompositionAnalysisSavedReportRestoresOnlyOpenSourcesAndTreeUsesIndependentLease(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	q := analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2", Rows: []string{"C1", "C2"}, Layout: "tree", Precision: 2}
	root := compositionAnalysisStart(t, a, q, "")
	result := waitCompositionAnalysis(t, root, "done")
	if result.Tree == nil || result.Grand.Exact != "9007199254740993.41" {
		t.Fatal(result)
	}
	branch := treeStart(t, a, root.ID, []string{"0002"})
	if child := treeResult(t, a, branch, "done"); child.Grand.Exact != "0.1" {
		t.Fatal(child)
	}
	saved := reconcilePost(t, a, "/api/analysis/report", map[string]any{"name": "联合报表", "query": analysisStatus(t, a, root.ID)})
	if saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	ws, err := a.loadAnalysis(a.currentWorkbook())
	if err != nil || ws.Version != 6 || len(ws.Reports) != 1 || ws.Reports[0].Query.Composition == nil {
		t.Fatal(ws, err)
	}
	a.clearAnalysisTasks()
	if compositionUsed(a) != 0 {
		t.Fatal("clear leaked source", compositionUsed(a))
	}
	root = compositionAnalysisStart(t, a, ws.Reports[0].Query, "")
	waitCompositionAnalysis(t, root, "done")
	bad := ws.Reports[0].Query
	copy, _ := bad.Composition.Normalized()
	copy.Inputs[1].Ref.Book = strings.Repeat("f", 64)
	bad.Composition, bad.Source = &copy, "append:"+copy.Digest()
	response := reconcilePost(t, a, "/api/analysis/query/start", bad)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "未打开") {
		t.Fatal("missing source silently rebound", response.Code, response.Body.String())
	}
	if response := reconcilePost(t, a, "/api/recipes/capture/start", map[string]string{"jobId": root.ID, "format": "csv", "book": sourceBookID(a.currentWorkbook()), "workbookKey": a.currentWorkbook().Fingerprint}); response.Code == 200 {
		t.Fatal("single-source recipe silently accepted append semantics")
	}
}

func TestCompositionAnalysisCancellationWhileQuerySlotBlockedReleasesSource(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	prepared := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, prepared, "done")
	for range cap(a.querySem) {
		a.querySem <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(a.querySem) {
			<-a.querySem
		}
	})
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Aggregate: "count"}, prepared.ID)
	clearComposition(t, a, prepared.ID)
	if response := reconcilePost(t, a, "/api/analysis/query/cancel", map[string]string{"id": job.ID}); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	waitCompositionAnalysis(t, job, "cancelled")
	if compositionUsed(a) != 0 || job.SourceRows != nil {
		t.Fatal("cancelled scan retained a prepared source", compositionUsed(a))
	}
}

func TestCompositionAnalysisIssueExportSurvivesClearedOwnersAndRemovedAttachment(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	definition.Inputs[0].Columns[1] = 3 // native text amount: deliberately excluded
	prepared := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, prepared, "done")
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Aggregate: "sum", Value: "C2"}, prepared.ID)
	if result := waitCompositionAnalysis(t, job, "done"); result.InvalidNumbers != 1 {
		t.Fatal(result)
	}
	for range cap(a.exportSem) {
		a.exportSem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range cap(a.exportSem) {
				<-a.exportSem
			}
		})
	}
	t.Cleanup(release)
	response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/issues/export", map[string]string{"jobId": job.ID}))
	clearComposition(t, a, prepared.ID)
	a.clearAnalysisTasks()
	removed := reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": definition.Inputs[1].Ref.Book})
	if removed.Code != 200 || compositionUsed(a) == 0 {
		t.Fatal("export did not retain its own source", removed.Code, compositionUsed(a))
	}
	release()
	a.mu.RLock()
	export := a.exports[response.JobID]
	a.mu.RUnlock()
	select {
	case <-export.done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued export lost source or deadlocked")
	}
	if export.Status != "done" || compositionUsed(a) != 0 || a.resolveReconcileBook(definition.Inputs[1].Ref.Book) != nil {
		t.Fatal("export failed or leaked an independent source", export.Status, export.Error, compositionUsed(a))
	}
}

func TestCompositionAnalysisRejectsOtherSourceChangesEvenAfterEarlyStop(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	prepared := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, prepared, "done")
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Aggregate: "count"}, prepared.ID)
	waitCompositionAnalysis(t, job, "done")
	other := a.resolveReconcileBook(definition.Inputs[1].Ref.Book)
	q := analysisStatus(t, a, job.ID)
	a.cacheMu.RLock()
	src, err := a.sourceResult(context.Background(), a.currentWorkbook(), q, job.ID)
	if err != nil {
		a.cacheMu.RUnlock()
		t.Fatal(err)
	}
	stop := errors.New("bounded consumer ended")
	err = src.scan(context.Background(), []string{"C1"}, func(rec analysis.Record) error {
		if err := os.WriteFile(other.Path, []byte("金额,编号\n0.3,changed\n"), 0600); err != nil {
			return err
		}
		return stop
	})
	src.close()
	a.cacheMu.RUnlock()
	if err == nil || errors.Is(err, stop) {
		t.Fatal("partial output ignored a changed unvisited source", err)
	}
	if response := reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": q, "jobId": job.ID, "limit": 1}); response.Code == 200 {
		t.Fatal("changed source published drill details")
	}
	job = compositionAnalysisStart(t, a, q, prepared.ID)
	waitCompositionAnalysis(t, job, "error")
	clearComposition(t, a, prepared.ID)
	a.clearAnalysisTasks()
	if compositionUsed(a) != 0 {
		t.Fatal("failed source retained budget", compositionUsed(a))
	}
}

func TestCompositionAnalysisUsesNativeRulesWhenPrimaryWorkbookIsCSV(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "primary.csv")
	if err := os.WriteFile(file, []byte("类别,金额\n甲,0.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := recordOpen(t, file)
	otherPath := filepath.Join(dir, "native.xlsx")
	writeZipArchive(t, otherPath, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Native" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>类别</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>甲</t></is></c><c r="B2" t="inlineStr"><is><t>1234.50</t></is></c></row></sheetData></worksheet>`,
	})
	book, unpin, err := a.openPinnedWorkbook(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unpin()
	before := printScaleHash(t, otherPath)
	first := a.currentWorkbook()
	definition := sourceview.Append{Version: 1, Name: "反向混合", Fields: []string{"类别", "金额"}, Inputs: []sourceview.AppendInput{
		{Ref: compositionRef(t, a, sourceBookID(first), "wb:"+first.Sheets[0].Name, 1), Columns: []int{1, 2}},
		{Ref: compositionRef(t, a, sourceBookID(book), "wb:Native", 1), Columns: []int{1, 2}},
	}}
	job := compositionAnalysisStart(t, a, analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2"}, "")
	if result := waitCompositionAnalysis(t, job, "done"); result.Grand.Exact != "0.1" || result.InvalidNumbers != 1 {
		t.Fatal("native secondary text was parsed using the primary CSV format", result)
	}
	if printScaleHash(t, otherPath) != before {
		t.Fatal("native secondary source changed")
	}
}

func TestCompositionAnalysisRestoresMultipleFiltersWithSingleQuerySlot(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	a.querySem = make(chan struct{}, 1)
	definition := compositionDefinition(t, a)
	for i := range definition.Inputs {
		ref := &definition.Inputs[i].Ref
		column := 2
		if i == 1 {
			column = 1
		}
		ref.Scope = &sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: ref.Fingerprint, Mode: "filtered", Filter: &sourceview.FilterSpec{HeaderRow: 1, SortColumn: column, Descending: true}}
	}
	canonical, err := definition.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	definition = canonical
	q := analysis.Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C2", Precision: 2}
	first := compositionAnalysisStart(t, a, q, "")
	second := compositionAnalysisStart(t, a, q, "")
	for _, job := range []*analysisTask{first, second} {
		if result := waitCompositionAnalysis(t, job, "done"); result.Scanned != 4 || result.Grand.Exact != "9007199254740993.41" {
			t.Fatal("filtered append restore changed source set", result)
		}
	}
	a.clearAnalysisTasks()
	if compositionUsed(a) != 0 {
		t.Fatal("filtered jobs leaked independent scopes", compositionUsed(a))
	}
}
