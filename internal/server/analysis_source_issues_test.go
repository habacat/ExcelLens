package server

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func TestAnalysisSourceIssuesExportRetainsScopeAfterAllAnalysisOwnersAreCleared(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "scoped-issues.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>类别</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2" hidden="1"><c r="A2" t="inlineStr"><is><t>隐藏问题</t></is></c><c r="B2" t="inlineStr"><is><t>1000</t></is></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>甲</t></is></c><c r="B3" t="inlineStr"><is><t>0001</t></is></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>甲</t></is></c><c r="B4"><v>0.10</v></c></row><row r="5" ht="0"><c r="A5" t="inlineStr"><is><t>隐藏问题</t></is></c><c r="B5"><f>1+2</f></c></row><row r="6"><c r="A6" t="inlineStr"><is><t>甲</t></is></c><c r="B6"><f>2+2</f></c></row></sheetData></worksheet>`,
	})
	before, _ := os.ReadFile(file)
	a := prepareFeatureApp(t, file)
	t.Cleanup(a.Close)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	q := analysis.Query{Source: "wb:First", HeaderRow: 1, Rows: []string{"C1"}, Aggregate: "sum", Value: "C2", Precision: 2, SourceScope: &captured.SourceScope}
	job := completedRecipeAnalysis(t, a, q)
	if job.Result.Scanned != 3 || job.Result.InvalidNumbers != 1 || job.Result.MissingFormulaCaches != 1 || job.Result.Grand.Exact != "0.1" {
		t.Fatal("scoped diagnostics changed native text/cache semantics", job.Result)
	}
	materialized := decodeResponse[struct {
		ID          string
		Rows, Total int
	}](t, reconcilePost(t, a, "/api/analysis/issues/materialize", map[string]any{"jobId": job.ID, "name": "可见问题", "limit": 100}))
	if materialized.Rows != 2 || materialized.Total != 2 {
		t.Fatal("issue materialization read hidden rows", materialized)
	}
	for i := 0; i < cap(a.exportSem); i++ {
		a.exportSem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := 0; i < cap(a.exportSem); i++ {
				<-a.exportSem
			}
		})
	}
	t.Cleanup(release)
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/issues/export", map[string]any{"jobId": job.ID, "protectCSV": false}))
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	a.clearAnalysisTasks()
	release()
	a.mu.RLock()
	export := a.exportJobs[started.JobID]
	a.mu.RUnlock()
	select {
	case <-export.done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued scoped-issue export did not finish")
	}
	export.mu.RLock()
	status, message, path := export.Status, export.Error, export.FilePath
	export.mu.RUnlock()
	if status != "done" {
		t.Fatal("export lost its own source lease", status, message)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[1][4] != "3" || records[2][4] != "6" || records[1][9] != "0001" || strings.Contains(string(data), "隐藏问题") {
		t.Fatal("full issue CSV widened, changed leading zeros or lost source rows", records)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("scoped analysis/issue export wrote the source")
	}
}

func TestAnalysisSourceRecipeCaptureKeepsOriginalScopeAsProvenanceWithoutApplyingItToAnotherTarget(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	q := analysis.Query{Source: "wb:First", HeaderRow: 1, Aggregate: "sum", Value: "C2", SourceScope: &captured.SourceScope}
	original := completedRecipeAnalysis(t, a, q)
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	capture := captureRecipe(t, a, original.ID)
	if capture.Status != "done" || capture.Draft.Origin.SourceScope == nil || capture.Draft.Origin.SourceScope.Digest() != captured.SourceScope.Digest() {
		t.Fatal("recipe capture lost the real scoped source", capture.Status, capture.Error)
	}
	mapped, _, err := capture.Draft.Query("wb:Another", 1, map[string]string{"C2": "C2"})
	if err != nil || mapped.SourceScope != nil {
		t.Fatal("generic analysis steps silently inherited a previous workbook's subset", mapped, err)
	}
}
