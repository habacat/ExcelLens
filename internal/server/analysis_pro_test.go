package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func setupProApp(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "read_only.xlsx")
	writeAnalysisWorkbook(t, path)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	sheet, ok := wb.FindSheet("Invest")
	if !ok {
		t.Fatal("missing sheet")
	}
	if _, err = wb.BuildIndex(sheet, nil, nil); err != nil {
		t.Fatal(err)
	}
	return app, path
}
func TestAdvancedSearchAPIRealOOXML(t *testing.T) {
	app, path := setupProApp(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{`A:北京 AND C BETWEEN 0.1 AND 0.2`, []string{"A2", "A3"}},
		{`[金额]>=9007199254740993`, []string{"C4"}},
		{`[机构]:北京 AND [金额]>9007199254740992`, nil},
		{`[品种] IN ("债券","基金") AND NOT [机构]:上海`, []string{"A2", "A3"}},
		{`formula:SUM`, []string{"A3"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			valid := analysisRequest(t, app, "POST", "/api/search/validate", fmt.Sprintf(`{"query":%q}`, tc.query))
			if valid.Code != 200 {
				t.Fatalf("validate: %s", valid.Body.String())
			}
			started := decodeResponse[struct {
				JobID string `json:"jobId"`
			}](t, analysisRequest(t, app, "POST", "/api/search/start", fmt.Sprintf(`{"sheet":"Invest","query":%q,"mode":"advanced","scope":"current"}`, tc.query)))
			app.mu.RLock()
			job := app.searchJobs[started.JobID]
			app.mu.RUnlock()
			if job == nil {
				t.Fatal("job missing")
			}
			select {
			case <-job.done:
			case <-time.After(8 * time.Second):
				t.Fatal("search timeout")
			}
			result := decodeResponse[struct {
				Status  string `json:"status"`
				Error   string `json:"error"`
				Matches []struct {
					Ref string `json:"ref"`
				} `json:"matches"`
			}](t, analysisRequest(t, app, "GET", "/api/search/status?id="+started.JobID, ""))
			if result.Status != "done" {
				t.Fatalf("search %s %s", result.Status, result.Error)
			}
			if len(result.Matches) != len(tc.want) {
				t.Fatalf("got %+v want %v", result.Matches, tc.want)
			}
			for i, x := range result.Matches {
				if x.Ref != tc.want[i] {
					t.Fatalf("mismatch %+v want %v", result.Matches, tc.want)
				}
			}
		})
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("original XLSX modified by advanced search")
	}
	bad := analysisRequest(t, app, "POST", "/api/search/validate", `{"query":"A~\"[\""}`)
	if bad.Code != 400 {
		t.Fatalf("malformed regex accepted: %d", bad.Code)
	}
}
func TestAdvancedAnalysisBatchBackupExport(t *testing.T) {
	app, _ := setupProApp(t)
	create := decodeResponse[struct {
		ID string `json:"id"`
	}](t, analysisRequest(t, app, "POST", "/api/analysis/sheet/create", `{"name":"测试分析","columns":["标签","金额"]}`))
	body := fmt.Sprintf(`{"id":%q,"expectedRevision":0,"changes":[{"row":0,"col":0,"value":"北京"},{"row":0,"col":1,"value":"0.1"}]}`, create.ID)
	data := decodeResponse[struct {
		Revision int64                 `json:"revision"`
		Previous []analysis.CellChange `json:"previous"`
	}](t, analysisRequest(t, app, "POST", "/api/analysis/sheet/batch", body))
	if data.Revision != 1 || len(data.Previous) != 2 || data.Previous[0].Value != "" {
		t.Fatalf("batch %+v", data)
	}
	conflict := analysisRequest(t, app, "POST", "/api/analysis/sheet/batch", body)
	if conflict.Code != 409 {
		t.Fatalf("stale revision unexpectedly accepted: %d", conflict.Code)
	}
	page := decodeResponse[struct {
		Rows     [][]string `json:"rows"`
		Revision int64      `json:"revision"`
	}](t, analysisRequest(t, app, "GET", "/api/analysis/sheet?id="+create.ID, ""))
	if page.Revision != 1 || page.Rows[0][0] != "北京" || page.Rows[0][1] != "0.1" {
		t.Fatalf("source changed from stale edit %+v", page)
	}
	undo := fmt.Sprintf(`{"id":%q,"expectedRevision":1,"changes":[{"row":0,"col":0,"value":""},{"row":0,"col":1,"value":""}]}`, create.ID)
	saved := analysisRequest(t, app, "POST", "/api/analysis/sheet/batch", undo)
	if saved.Code != 200 {
		t.Fatalf("undo %s", saved.Body.String())
	}
	backup := analysisRequest(t, app, "GET", "/api/analysis/backup", "")
	if backup.Code != 200 {
		t.Fatalf("backup: %s", backup.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(backup.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	raw["fingerprint"] = "different-file"
	tampered, _ := json.Marshal(raw)
	rejection := analysisRequest(t, app, "POST", "/api/analysis/backup", string(tampered))
	if rejection.Code != 400 {
		t.Fatalf("foreign backup accepted: %d", rejection.Code)
	}
	restore := analysisRequest(t, app, "POST", "/api/analysis/backup", backup.Body.String())
	if restore.Code != 200 {
		t.Fatalf("restoring matching backup: %s", restore.Body.String())
	}
	profile := analysisRequest(t, app, "GET", "/api/analysis/profile?source=wb%3AInvest&field=C3&headerRow=1", "")
	p := decodeResponse[struct {
		Rows      int    `json:"rows"`
		Numeric   int    `json:"numeric"`
		MaxNumber string `json:"maxNumber"`
	}](t, profile)
	if p.Rows != 4 || p.Numeric != 4 || p.MaxNumber != "9007199254740993" {
		t.Fatalf("profile: %+v", p)
	}
	query := `{"source":"wb:Invest","headerRow":1,"rows":["C1"],"columns":["C2"],"aggregate":"sum","metrics":[{"field":"C3","aggregate":"sum","name":"金额"},{"aggregate":"count","name":"笔数"}],"precision":2}`
	job := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, analysisRequest(t, app, "POST", "/api/analysis/query/start", query))
	result := awaitAnalysis(t, app, job.JobID)
	if len(result.MetricGrand) != 2 || result.MetricGrand[0].Exact != "9007199254740993.31" || result.MetricGrand[1].Exact != "4" {
		t.Fatalf("multi-metric output %+v", result.MetricGrand)
	}
	export := analysisRequest(t, app, "GET", "/api/analysis/export?id="+job.JobID+"&format=xlsx", "")
	if export.Code != 200 {
		t.Fatalf("XLSX export: %s", export.Body.String())
	}
	zipFile, err := zip.NewReader(bytes.NewReader(export.Body.Bytes()), int64(export.Body.Len()))
	if err != nil {
		t.Fatalf("export invalid xlsx: %v", err)
	}
	foundSheet := false
	for _, f := range zipFile.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			foundSheet = true
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			_, _ = b.ReadFrom(r)
			r.Close()
			if !strings.Contains(b.String(), "9007199254740993") || !strings.Contains(b.String(), "合计") {
				t.Fatalf("export loses precision or totals")
			}
		}
	}
	if !foundSheet {
		t.Fatal("missing xlsx sheet xml")
	}
	materialized := analysisRequest(t, app, "POST", "/api/analysis/materialize", fmt.Sprintf(`{"name":"透视报表","jobId":%q}`, job.JobID))
	if materialized.Code != 200 {
		t.Fatalf("materialize: %s", materialized.Body.String())
	}
}
