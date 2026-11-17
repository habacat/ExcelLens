package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/clustering"
)

func clusterFixture(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "clusters.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Cluster" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>机构</t></is></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>A</t></is></c></row>
<row r="3" hidden="1"><c r="A3" t="inlineStr"><is><t>a</t></is></c></row>
<row r="4"><c r="A4" t="inlineStr"><is><t>A</t></is></c></row>
<row r="5"><c r="A5" t="str"><f>"a"</f><v>a</v></c></row>
<row r="6"><c r="A6"><v>7</v></c></row>
<row r="7"><c r="A7" t="inlineStr"><is><t>0007</t></is></c></row>
<row r="8"><c r="A8" t="inlineStr"><is><t>0007</t></is></c></row>
<row r="9"><c r="A9" t="e"><v>#N/A</v></c></row>
<row r="10"><c r="A10" t="b"><v>1</v></c></row>
<row r="11"><c r="A11"><f>1+2</f></c></row>
<row r="12"><c r="B12" t="inlineStr"><is><t>行保留</t></is></c></row>
<row r="13"><c r="A13" t="inlineStr"><is><t>12</t></is></c></row>
<row r="14"><c r="A14"><v>12</v></c></row>
</sheetData></worksheet>`,
	})
	return openTreeTestApp(t, file), file
}

func clusterStart(t *testing.T, a *App, sourceID string) *clusterTask {
	t.Helper()
	wb := a.currentWorkbook()
	req := clusterStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: "wb:Cluster", SourceID: sourceID, HeaderRow: 1, Field: "C1", Options: clustering.Options{FoldCase: true}}
	id := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/clusters/start", req)).JobID
	a.mu.RLock()
	job := a.clusterJobs[id]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("start did not retain candidate owner")
	}
	return job
}

func clusterWait(t *testing.T, a *App, job *clusterTask, status string) *clusterSnapshot {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(30 * time.Second):
		t.Fatal("clustering did not finish")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if job.Status != status {
		t.Fatal(job.Status, job.Error)
	}
	return job.Snapshot
}

func clusterConfirm(t *testing.T, a *App, job *clusterTask, group int, representative string) *httptest.ResponseRecorder {
	t.Helper()
	return reconcilePost(t, a, "/api/analysis/clusters/confirm", map[string]any{"id": job.ID, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "choices": []clusterChoice{{Group: group, Representative: representative}}})
}

func TestClusterHTTPPreservesSourceTypesFullCountsMappingsAndNavigation(t *testing.T) {
	a, file := clusterFixture(t)
	before, _ := os.ReadFile(file)
	job := clusterStart(t, a, "")
	snapshot := clusterWait(t, a, job, "done")
	if snapshot.Result.Counts.Scanned != 13 || snapshot.Result.Counts.Eligible != 7 || snapshot.Result.Counts.Numeric != 2 ||
		snapshot.Result.Counts.OtherTypes != 2 || snapshot.Result.Counts.Blank != 1 || snapshot.Result.Counts.MissingCache != 1 {
		t.Fatal(snapshot.Result.Counts)
	}
	page := decodeResponse[struct {
		Total int
		Items []struct {
			ID       int
			Kind     string
			Count    int64
			Variants int
		}
	}](t, analysisRequest(t, a, "GET", "/api/analysis/clusters/groups?id="+job.ID, ""))
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatal(page)
	}
	groupID := -1
	for _, g := range snapshot.Result.Groups {
		if g.Kind == "similar" {
			groupID = g.ID
			if g.Count != 4 || len(g.Variants) != 3 {
				t.Fatal(g)
			}
		}
	}
	if groupID < 0 {
		t.Fatal("missing case candidate")
	}
	confirmed := decodeResponse[struct{ Transform analysis.Transform }](t, clusterConfirm(t, a, job, groupID, "A"))
	if confirmed.Transform.Source != "C1" || confirmed.Transform.Op != "map_text" || len(confirmed.Transform.Mapping) != 1 ||
		confirmed.Transform.Mapping[0] != (analysis.TextMapping{From: "a", To: "A"}) {
		t.Fatal(confirmed)
	}
	wb := a.currentWorkbook()
	a.cacheMu.RLock()
	src, err := a.source(wb, snapshot.Source, snapshot.HeaderRow)
	if err != nil {
		a.cacheMu.RUnlock()
		t.Fatal(err)
	}
	err = src.scan(context.Background(), []string{"C1"}, func(rec analysis.Record) error {
		old := rec.Values["C1"]
		analysis.ApplyTransforms(rec.Values, []analysis.Transform{confirmed.Transform})
		got := rec.Values[confirmed.Transform.ID]
		if (rec.Number == 3 || rec.Number == 5) && got.Text != "A" {
			return fmt.Errorf("trusted text did not map: %+v", rec)
		}
		if rec.Number != 3 && rec.Number != 5 && (got.Text != old.Text || got.Numeric != old.Numeric || got.MissingCache != old.MissingCache) {
			return fmt.Errorf("mapping changed excluded type/input at %d: %+v -> %+v", rec.Number, old, got)
		}
		return nil
	})
	src.close()
	a.cacheMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if bad := clusterConfirm(t, a, job, groupID, "invented"); bad.Code != 400 {
		t.Fatal("invented representative accepted", bad.Code, bad.Body.String())
	}
	detail := decodeResponse[struct {
		Group    int
		Variants []clustering.Variant
		Total    int
	}](t, analysisRequest(t, a, "GET", fmt.Sprintf("/api/analysis/clusters/detail?id=%s&group=%d", job.ID, groupID), ""))
	resolve := fmt.Sprintf("/api/analysis/clusters/resolve?id=%s&group=%d&variant=0&sample=0&workbookKey=%s&book=%s", job.ID, groupID, wb.Fingerprint, sourceBookID(wb))
	position := decodeResponse[struct {
		Ref         string
		Row, Column int
	}](t, analysisRequest(t, a, "GET", resolve, ""))
	if position.Row != detail.Variants[0].Rows[0] || position.Column != 1 || position.Ref != fmt.Sprintf("A%d", position.Row) {
		t.Fatal(position, detail)
	}
	if bad := analysisRequest(t, a, "GET", "/api/analysis/clusters/groups?id="+job.ID+"&offset=3", ""); bad.Code != 400 {
		t.Fatal("invalid page accepted", bad.Body.String())
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("candidate scan or confirmation changed source")
	}
}

func blockClusterQueue(t *testing.T, a *App) func() {
	t.Helper()
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := 0; i < cap(a.querySem); i++ {
				<-a.querySem
			}
		})
	}
	t.Cleanup(release)
	return release
}

func TestClusterQueuedSourceSurvivesOriginalOwnersAndCancellationReleasesLeases(t *testing.T) {
	a, _ := clusterFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: "Cluster"}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Source: "wb:Cluster", Mode: "visible", ViewID: view})
	release := blockClusterQueue(t, a)
	job := clusterStart(t, a, captured.ID)
	reconcilePost(t, a, "/api/row-view/clear", map[string]string{"id": view})
	reconcilePost(t, a, "/api/analysis/source/clear", map[string]string{"id": captured.ID})
	release()
	snapshot := clusterWait(t, a, job, "done")
	if snapshot.Scope == nil || snapshot.Result.Counts.Scanned != 12 || snapshot.Result.Counts.Eligible != 6 {
		t.Fatal("captured view widened", snapshot)
	}
	for _, g := range snapshot.Result.Groups {
		for _, v := range g.Variants {
			for _, row := range v.Rows {
				if row == 3 {
					t.Fatal("hidden row reappeared after view clear")
				}
			}
		}
	}
	release = blockClusterQueue(t, a)
	cancelled := clusterStart(t, a, "")
	reconcilePost(t, a, "/api/analysis/clusters/cancel", map[string]string{"id": cancelled.ID})
	if snapshot := clusterWait(t, a, cancelled, "cancelled"); snapshot != nil {
		t.Fatal("cancelled queued result published")
	}
	release()
	retry := clusterStart(t, a, "")
	clusterWait(t, a, retry, "done")
	reconcilePost(t, a, "/api/analysis/clusters/clear", map[string]string{"id": retry.ID})
	if out := analysisRequest(t, a, "GET", "/api/analysis/clusters/groups?id="+retry.ID, ""); out.Code != 409 {
		t.Fatal("cleared result reused", out.Code)
	}
}

func TestClusterSourceIdentityScratchChangesAndUntrustedFormatsFailClosed(t *testing.T) {
	a, file := clusterFixture(t)
	wb := a.currentWorkbook()
	job := clusterStart(t, a, "")
	clusterWait(t, a, job, "done")
	copyPath := filepath.Join(t.TempDir(), "same-content.xlsx")
	content, _ := os.ReadFile(file)
	if err := os.WriteFile(copyPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(copyPath); err != nil {
		t.Fatal(err)
	}
	if out := clusterConfirm(t, a, job, 0, "A"); out.Code != 409 {
		t.Fatal("identical content at another path reused old candidate", out.Code)
	}
	wb = a.currentWorkbook()
	if err := a.editAnalysis(wb, func(w *analysis.Workspace) error {
		w.Sheets = append(w.Sheets, analysis.Sheet{ID: "test", Name: "文字", Columns: []string{"机构"}, Rows: [][]string{{"Alpha"}, {"alpha"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req := clusterStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Source: "scratch:test", HeaderRow: 1, Field: "C1", Options: clustering.Options{FoldCase: true}}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/clusters/start", req))
	a.mu.RLock()
	scratchJob := a.clusterJobs[started.JobID]
	a.mu.RUnlock()
	clusterWait(t, a, scratchJob, "done")
	if err := a.editAnalysis(wb, func(w *analysis.Workspace) error { w.Sheet("test").Rows[0][0] = "different"; return nil }); err != nil {
		t.Fatal(err)
	}
	if out := analysisRequest(t, a, "GET", "/api/analysis/clusters/groups?id="+scratchJob.ID, ""); out.Code != 409 {
		t.Fatal("same revision different scratch content accepted", out.Code, out.Body.String())
	}
	// The registry capability is enforced before any read, independent of
	// extension or a frontend support list.
	wb.Format = "sqlite"
	req.Source, req.SourceID = "wb:Cluster", ""
	if out := reconcilePost(t, a, "/api/analysis/clusters/start", req); out.Code != 422 || !strings.Contains(out.Body.String(), "文本类型") {
		t.Fatal("unknown storage types guessed from display placeholders", out.Code, out.Body.String())
	}
	wb.Format = "xlsx"
}

func TestClusterHTTPStrictRequestRejectsExtraDataAndMissingIdentity(t *testing.T) {
	a, _ := clusterFixture(t)
	for _, raw := range []string{`{"source":"wb:Cluster","headerRow":1,"field":"C1","options":{}}`, `{"unknown":true}`, `{} {}`} {
		out := analysisRequest(t, a, "POST", "/api/analysis/clusters/start", raw)
		if out.Code < 400 {
			t.Fatal("invalid request accepted", raw, out.Body.String())
		}
	}
	encoded, _ := json.Marshal(map[string]any{"choices": []clusterChoice{{Group: -1, Representative: "x"}}})
	if out := analysisRequest(t, a, "POST", "/api/analysis/clusters/confirm", string(encoded)); out.Code != 409 {
		t.Fatal(out.Code, out.Body.String())
	}
}
