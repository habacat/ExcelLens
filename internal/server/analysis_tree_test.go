package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
)

func openTreeTestApp(t *testing.T, file string) *App {
	t.Helper()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	return a
}

func treeFixture(t *testing.T) (*App, string, analysis.Query) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "tree.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Tree" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>机构</t></is></c><c r="B1" t="inlineStr"><is><t>团队</t></is></c><c r="C1" t="inlineStr"><is><t>类别</t></is></c><c r="D1" t="inlineStr"><is><t>金额</t></is></c><c r="E1" t="inlineStr"><is><t>代码</t></is></c><c r="F1" t="inlineStr"><is><t>列</t></is></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>北</t></is></c><c r="B2" t="inlineStr"><is><t>001</t></is></c><c r="C2" t="inlineStr"><is><t>债</t></is></c><c r="D2"><v>0.1</v></c><c r="E2" t="inlineStr"><is><t>同</t></is></c></row>
<row r="3"><c r="A3" t="inlineStr"><is><t>北</t></is></c><c r="B3" t="inlineStr"><is><t>1</t></is></c><c r="C3" t="inlineStr"><is><t>债</t></is></c><c r="D3"><v>0.2</v></c><c r="E3" t="inlineStr"><is><t>同</t></is></c><c r="F3" t="inlineStr"><is><t>X</t></is></c></row>
<row r="4" hidden="1"><c r="A4" t="inlineStr"><is><t>北</t></is></c><c r="B4" t="inlineStr"><is><t>1</t></is></c><c r="C4" t="inlineStr"><is><t>股</t></is></c><c r="D4"><v>9007199254740993.31</v></c><c r="E4" t="inlineStr"><is><t>不同</t></is></c></row>
<row r="5"><c r="A5" t="inlineStr"><is><t>南</t></is></c><c r="B5" t="inlineStr"><is><t>001</t></is></c><c r="C5" t="inlineStr"><is><t>股</t></is></c><c r="D5"><v>11</v></c></row>
<row r="6"><c r="A6" t="inlineStr"><is><t>南</t></is></c><c r="B6" t="inlineStr"><is><t>1</t></is></c><c r="C6" t="inlineStr"><is><t>股</t></is></c><c r="D6" t="inlineStr"><is><t>0007</t></is></c></row>
<row r="7"><c r="A7" t="inlineStr"><is><t>南</t></is></c><c r="B7" t="inlineStr"><is><t>1</t></is></c><c r="C7" t="inlineStr"><is><t>股</t></is></c><c r="D7"><f>1+2</f></c></row>
</sheetData></worksheet>`,
	})
	a := openTreeTestApp(t, file)
	t.Cleanup(a.Close)
	q := analysis.Query{Source: "wb:Tree", HeaderRow: 1, Rows: []string{"C1", "C2", "C3"}, Columns: []string{"C6"}, Value: "C4", Aggregate: "sum", Metrics: []analysis.Metric{{Field: "C4", Aggregate: "sum"}, {Field: "C4", Aggregate: "average"}, {Field: "C5", Aggregate: "distinct"}, {Aggregate: "count"}}, Layout: "tree", Precision: 2, Sort: "label_asc"}
	return a, file, q
}

func treeStart(t *testing.T, a *App, rootID string, path []string) *analysisTreeTask {
	t.Helper()
	wb := a.currentWorkbook()
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/tree/start", map[string]any{"jobId": rootID, "path": path, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)}))
	a.analysisMu.Lock()
	job := a.analysisTreeJobs[started.JobID]
	a.analysisMu.Unlock()
	if job == nil {
		t.Fatal("tree start did not retain its task")
	}
	return job
}

func treeResult(t *testing.T, a *App, job *analysisTreeTask, expected string) analysis.Result {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("tree task did not finish")
	}
	status := decodeResponse[struct {
		Status string
		Error  string
		Result analysis.Result
	}](t, analysisRequest(t, a, "GET", "/api/analysis/tree/status?id="+job.ID, ""))
	if status.Status != expected {
		t.Fatal(status.Status, status.Error)
	}
	return status.Result
}

func TestTreeSourceIsRetainedBeforeQueueAndSurvivesAllOriginalOwners(t *testing.T) {
	isolateTestStorage(t)
	a, file, q := treeFixture(t)
	before, _ := os.ReadFile(file)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: "Tree"}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Source: q.Source, Mode: "visible", ViewID: view})
	q.SourceScope = &captured.SourceScope
	root := completedRecipeAnalysis(t, a, q)
	if root.Result.Tree == nil || root.Result.Grand.Exact != "11.3" || root.Result.Included != 5 || len(root.Result.Rows) != 2 || root.Result.InvalidNumbers != 1 || root.Result.MissingFormulaCaches != 1 {
		t.Fatal(root.Result)
	}
	data := root.SourceRows.data
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
	branch := treeStart(t, a, root.ID, []string{"北"})
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	a.analysisMu.Lock()
	delete(a.analysisJobs, root.ID)
	a.analysisMu.Unlock()
	root.closeSourceRows()
	if data.refs.Load() < 1 {
		t.Fatal("queued child did not retain its source")
	}
	release()
	r := treeResult(t, a, branch, "done")
	if r.Grand.Exact != "0.3" || r.MetricGrand[1].Exact != "0.15" || r.MetricGrand[2].Exact != "1" || r.Included != 2 || len(r.Rows) != 2 || !reflect.DeepEqual(r.Rows[0].Keys, []string{"北", "001"}) || !reflect.DeepEqual(r.Rows[1].Keys, []string{"北", "1"}) || !r.Tree.HasChildren {
		t.Fatal("child widened, changed leading-zero labels or summed child averages", r)
	}
	if data.refs.Load() != 0 {
		t.Fatal("last source reference was not released", data.refs.Load())
	}
	a.analysisMu.Lock()
	parents := len(a.analysisJobs)
	a.analysisMu.Unlock()
	if parents != 0 {
		t.Fatal("child was added to the parent result pool")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("tree modified source bytes")
	}
}

func TestTreeSubtotalsBlankColumnsDetailsAndWorkspaceReplayUseTheRootSource(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	root := completedRecipeAnalysis(t, a, q)
	branch := treeStart(t, a, root.ID, []string{"北", "1"})
	r := treeResult(t, a, branch, "done")
	if r.Tree.HasChildren || r.Grand.Exact != "9007199254740993.51" || r.Included != 2 || len(r.RowFields) != 3 {
		t.Fatal(r)
	}
	for _, tc := range []struct {
		selected bool
		rows     []int
	}{{false, []int{2, 3, 4}}, {true, []int{2, 4}}} {
		request := map[string]any{"query": root.Query, "jobId": root.ID, "keys": []string{"北"}, "columnSelected": tc.selected, "columnKey": "", "limit": 100}
		drill := decodeResponse[analysis.DrillResult](t, reconcilePost(t, a, "/api/analysis/drill", request))
		var got []int
		for _, row := range drill.Rows {
			got = append(got, row.Row)
		}
		if !reflect.DeepEqual(got, tc.rows) {
			t.Fatal("subtotal and blank column conflated", drill)
		}
		request["name"] = "层级明细"
		detail := decodeResponse[struct{ Rows int }](t, reconcilePost(t, a, "/api/analysis/detail/materialize", request))
		if detail.Rows != len(tc.rows) {
			t.Fatal(detail)
		}
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/analysis/report", map[string]any{"name": "层级报表", "query": root.Query}))
	backup := decodeResponse[struct{ Workspace analysis.Workspace }](t, analysisRequest(t, a, "GET", "/api/analysis/backup", ""))
	if backup.Workspace.Version != 3 || backup.Workspace.Reports[0].Query.Layout != "tree" {
		t.Fatal(backup)
	}
	if got := rowViewCall(t, a, "POST", "/api/analysis/tree/start", fmt.Sprintf(`{"jobId":%q,"path":["北"],"workbookKey":%q,"book":%q,"unknown":1}`, root.ID, a.currentWorkbook().Fingerprint, sourceBookID(a.currentWorkbook()))); got.Code != 400 {
		t.Fatal("unknown tree parameter accepted", got.Code)
	}
	for _, path := range [][]string{nil, {"北", "1", "股"}} {
		if _, err := a.startAnalysisTree(a.currentWorkbook(), root.ID, path); err == nil {
			t.Fatal("invalid expansion depth accepted", path)
		}
	}
}

func TestTreeQueueCancelClearAndAppCloseReleaseEverySourceReference(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	root := completedRecipeAnalysis(t, a, q)
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
	first := treeStart(t, a, root.ID, []string{"北"})
	second := treeStart(t, a, root.ID, []string{"南"})
	if _, err := a.startAnalysisTree(a.currentWorkbook(), root.ID, []string{"北"}); err == nil {
		t.Fatal("active tree limit was ignored")
	}
	featureCall(t, a, "POST", "/api/analysis/tree/clear", fmt.Sprintf(`{"id":%q}`, first.ID))
	select {
	case <-first.done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued clear waited for a scan slot")
	}
	if first.Progress != 0 || first.Result != nil {
		t.Fatal("cancelled queued branch read/published data")
	}
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("App close did not cancel queued child")
	}
	select {
	case <-second.done:
	default:
		t.Fatal("App close returned before child exit")
	}
	a.rootApp().sourceMu.Lock()
	pins := len(a.rootApp().sourcePins)
	a.rootApp().sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("tree leaked a source pin", pins)
	}
	release()
}

func TestTreeHistoryDoesNotEvictRootAndSourceChangesRefuseExpansion(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	root := completedRecipeAnalysis(t, a, q)
	for i := 0; i < formats.AnalysisTreeTasks+3; i++ {
		job := treeStart(t, a, root.ID, []string{"北"})
		treeResult(t, a, job, "done")
	}
	a.analysisMu.Lock()
	count, kept := len(a.analysisTreeJobs), a.analysisJobs[root.ID] == root
	a.analysisMu.Unlock()
	if count != formats.AnalysisTreeTasks || !kept {
		t.Fatal("expansion evicted root or retained unbounded history", count, kept)
	}
	// A scratch row snapshot with the same revision but different values must
	// not be accepted merely because its parent job ID still exists.
	wb := a.currentWorkbook()
	err := a.editAnalysis(wb, func(w *analysis.Workspace) error {
		w.Sheets = append(w.Sheets, analysis.Sheet{ID: "tree-target", Name: "编辑目标", Columns: []string{"机构", "团队", "金额"}, Rows: [][]string{{"北", "001", "2"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scratch := analysis.Query{Source: "scratch:tree-target", HeaderRow: 1, Rows: []string{"C1", "C2"}, Value: "C3", Aggregate: "sum", Layout: "tree"}
	parent := completedRecipeAnalysis(t, a, scratch)
	if err := a.editAnalysis(wb, func(w *analysis.Workspace) error { w.Sheets[0].Rows[0][2] = "3"; return nil }); err != nil {
		t.Fatal(err)
	}
	child := treeStart(t, a, parent.ID, []string{"北"})
	treeResult(t, a, child, "error")
	if !strings.Contains(child.Error, "来源数据已变化") || child.Result != nil {
		t.Fatal(child.Error)
	}
}

func TestTreeRecipeGuardStillReadsBindingsOfNotYetExpandedDimensions(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "guard.csv")
	if err := os.WriteFile(file, []byte("机构,团队,金额\n"+strings.Repeat("北,2,1\n", 9)+"北,类型变化,1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := openTreeTestApp(t, file)
	t.Cleanup(a.Close)
	wb := a.currentWorkbook()
	q := analysis.Query{Source: "wb:" + wb.Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1", "C2"}, Value: "C3", Aggregate: "sum", Layout: "tree"}
	job, err := a.startAnalysisTask(wb, q, &recipeRunPlan{Stamp: wb.Fingerprint, Masks: map[int]recipeTypeMask{1: recipeText, 2: recipeAmountText, 3: recipeAmountText}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("guarded tree did not finish")
	}
	if job.Status != "error" || !strings.Contains(job.Error, "B11") || job.Result.Tree != nil {
		t.Fatal("lazy root skipped a declared type guard", job.Status, job.Error)
	}
}

func TestTreeResultBudgetRejectsOversizeAndKeepsConcurrentStatusAndClearSafe(t *testing.T) {
	isolateTestStorage(t)
	a, _, q := treeFixture(t)
	root := completedRecipeAnalysis(t, a, q)
	// Exercise an actual serialized result overflow before publishing a success.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	job := &analysisTreeTask{ID: "budget", Workbook: a.currentWorkbook(), Query: q, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.analysisMu.Lock()
	a.analysisTreeJobs[job.ID] = job
	a.analysisMu.Unlock()
	a.finishAnalysisTree(job, analysis.Result{Rows: []analysis.ResultRow{{Keys: []string{strings.Repeat("x", formats.AnalysisTreeResultBytes+1)}}}}, nil)
	close(job.done)
	if job.Status != "error" || job.Result != nil {
		t.Fatal("oversized result published", job.Status)
	}
	for i := 0; i < 8; i++ {
		child := treeStart(t, a, root.ID, []string{"北"})
		var workers sync.WaitGroup
		for range 4 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for n := 0; n < 4; n++ {
					rowViewCall(t, a, "GET", "/api/analysis/tree/status?id="+child.ID, "")
				}
			}()
		}
		featureCall(t, a, "POST", "/api/analysis/tree/clear", fmt.Sprintf(`{"id":%q}`, child.ID))
		workers.Wait()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Fatal("clear did not finish")
		}
		var decoded analysis.Result
		if len(child.Result) > 0 && json.Unmarshal(child.Result, &decoded) != nil {
			t.Fatal("result bytes mutated during status")
		}
	}
}

func TestTreeRootAndExpansionRefuseChangedOriginalInsteadOfMixingSnapshots(t *testing.T) {
	isolateTestStorage(t)
	a, file, q := treeFixture(t)
	parent := completedRecipeAnalysis(t, a, q)
	archive, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	parts := make(map[string]string)
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[entry.Name] = string(data)
	}
	archive.Close()
	// Content identity intentionally ignores ZIP-container padding. Change an
	// actual worksheet part, rather than mistaking extra ZIP bytes for data.
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], "<v>0.1</v>", "<v>0.9</v>", 1)
	writeZipArchive(t, file, parts)
	child := treeStart(t, a, parent.ID, []string{"北"})
	treeResult(t, a, child, "error")
	if child.Result != nil {
		t.Fatal("changed source published children")
	}
	root, err := a.startAnalysisTask(a.currentWorkbook(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-root.done:
	case <-time.After(10 * time.Second):
		t.Fatal("root source check stalled")
	}
	if root.Status != "error" || len(root.Result.Rows) != 0 || root.Result.Tree != nil {
		t.Fatal("root skipped source identity check", root.Status, root.Error)
	}
}
