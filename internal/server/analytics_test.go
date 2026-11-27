package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
)

func writeAnalysisWorkbook(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Invest" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:D5"/><sheetData>
        <row r="1"><c r="A1" t="inlineStr"><is><t>机构</t></is></c><c r="B1" t="inlineStr"><is><t>品种</t></is></c><c r="C1" t="inlineStr"><is><t>金额</t></is></c><c r="D1" t="inlineStr"><is><t>日期</t></is></c></row>
        <row r="2"><c r="A2" t="inlineStr"><is><t>北京</t></is></c><c r="B2" t="inlineStr"><is><t>债券</t></is></c><c r="C2"><v>0.1</v></c><c r="D2" t="inlineStr"><is><t>2026-01-05</t></is></c></row>
        <row r="3"><c r="A3" t="inlineStr"><is><t>北京</t></is></c><c r="B3" t="inlineStr"><is><t>债券</t></is></c><c r="C3"><f>SUM(C2,0.2)</f><v>0.2</v></c><c r="D3" t="inlineStr"><is><t>2026-01-07</t></is></c></row>
        <row r="4"><c r="A4" t="inlineStr"><is><t>上海</t></is></c><c r="B4" t="inlineStr"><is><t>基金</t></is></c><c r="C4"><v>9007199254740993</v></c><c r="D4" t="inlineStr"><is><t>2026-03-02</t></is></c></row>
        <row r="5"><c r="A5" t="inlineStr"><is><t>上海</t></is></c><c r="B5" t="inlineStr"><is><t>债券</t></is></c><c r="C5"><v>1e-2</v></c><c r="D5" t="inlineStr"><is><t>2026-03-07</t></is></c></row>
      </sheetData></worksheet>`,
	}
	for name, value := range entries {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(value)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.Close(); e != nil {
		t.Fatal(e)
	}
}
func analysisRequest(t *testing.T, app *App, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", app.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	out := httptest.NewRecorder()
	app.Handler().ServeHTTP(out, req)
	return out
}
func decodeResponse[T any](t *testing.T, out *httptest.ResponseRecorder) T {
	t.Helper()
	var decoded T
	if out.Code != 200 {
		t.Fatalf("request error %d: %s", out.Code, out.Body.String())
	}
	if err := json.Unmarshal(out.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
func awaitAnalysis(t *testing.T, a *App, id string) analysis.Result {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		out := analysisRequest(t, a, "GET", "/api/analysis/query/status?id="+id, "")
		var status struct {
			Status string          `json:"status"`
			Error  string          `json:"error"`
			Result analysis.Result `json:"result"`
		}
		status = decodeResponse[struct {
			Status string          `json:"status"`
			Error  string          `json:"error"`
			Result analysis.Result `json:"result"`
		}](t, out)
		if status.Status == "done" {
			return status.Result
		}
		if status.Status == "error" || status.Status == "cancelled" {
			t.Fatalf("async analysis %s error: %s", status.Status, status.Error)
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("analysis task timed out")
	return analysis.Result{}
}
func TestAnalysisAPIFullWorkflowKeepsOriginalReadOnly(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "investment.xlsx")
	writeAnalysisWorkbook(t, path)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256(original)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("Invest")
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	unauth := httptest.NewRequest("GET", "/api/analysis/workspace", nil)
	unauth = localTestConnection(unauth)
	reject := httptest.NewRecorder()
	a.Handler().ServeHTTP(reject, unauth)
	if reject.Code != http.StatusForbidden {
		t.Fatalf("analysis endpoint needs token; got %d", reject.Code)
	}
	fields := decodeResponse[struct {
		Fields    []analysis.Field    `json:"fields"`
		Preview   []analysis.DrillRow `json:"preview"`
		TotalRows int                 `json:"totalRows"`
	}](t,
		analysisRequest(t, a, "GET", "/api/analysis/fields?source=wb%3AInvest&headerRow=1", ""))
	if len(fields.Fields) != 4 || !fields.Fields[2].Numeric || fields.Fields[0].Name != "机构" || fields.TotalRows != 4 {
		t.Fatalf("schema/data inference wrong: %+v", fields)
	}
	query := `{"source":"wb:Invest","headerRow":1,"rows":["C1"],"columns":["C2"],"value":"C3","aggregate":"sum","precision":2,"sort":"value_desc"}`
	started := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", query))
	result := awaitAnalysis(t, a, started.JobID)
	if result.Grand.Exact != "9007199254740993.31" || result.Scanned != 4 || result.Included != 4 {
		t.Fatalf("bad exact original spreadsheet aggregation %+v", result)
	}
	detail := decodeResponse[analysis.DrillResult](t, analysisRequest(t, a, "POST", "/api/analysis/drill", `{"query":`+query+`,"keys":["北京"],"columnKey":"债券","offset":0,"limit":50}`))
	if len(detail.Rows) != 2 || detail.Rows[0].Row != 2 || detail.Rows[1].Row != 3 {
		t.Fatalf("drill physical rows wrong %+v", detail)
	}
	materialized := decodeResponse[struct {
		ID string `json:"id"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/materialize", fmt.Sprintf(`{"jobId":%q,"name":"我的透视结果"}`, started.JobID)))
	if materialized.ID == "" {
		t.Fatal("materialized ID missing")
	}
	pivotSheet := decodeResponse[struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+materialized.ID, ""))
	if len(pivotSheet.Columns) != 4 || !strings.HasPrefix(pivotSheet.Columns[3], "合计") || len(pivotSheet.Rows) == 0 || len(pivotSheet.Rows[0]) != 4 {
		t.Fatalf("pivot materialization must retain row totals and columns: %+v", pivotSheet)
	}
	pasted := analysisRequest(t, a, "POST", "/api/analysis/sheet/paste", fmt.Sprintf(`{"id":%q,"row":0,"col":0,"rows":[["测试", "0.25"]]}`, materialized.ID))
	if pasted.Code != 200 {
		t.Fatalf("materialized paste %s", pasted.Body.String())
	}
	sheet := decodeResponse[struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+materialized.ID, ""))
	if len(sheet.Rows) == 0 || sheet.Rows[0][1] != "0.25" {
		t.Fatalf("editable sheet not saved %+v", sheet)
	}
	scratchQuery := fmt.Sprintf(`{"source":"scratch:%s","rows":["C1"],"value":"C2","aggregate":"sum"}`, materialized.ID)
	scratch := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", scratchQuery))
	fromScratch := awaitAnalysis(t, a, scratch.JobID)
	if fromScratch.Grand.Exact == "" {
		t.Fatalf("scratch numeric not parsed %+v", fromScratch)
	}
	originalAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(originalAfter) != originalHash {
		t.Fatal("CRITICAL: user original XLSX bytes changed")
	}
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	restored := decodeResponse[struct {
		Sheets []struct {
			ID string `json:"id"`
		} `json:"sheets"`
	}](t, analysisRequest(t, b, "GET", "/api/analysis/workspace", ""))
	if len(restored.Sheets) != 1 || restored.Sheets[0].ID != materialized.ID {
		t.Fatalf("workspace not durable %+v", restored)
	}
}
func TestAnalysisAssetsAndNoOriginalWorkbook(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	without := analysisRequest(t, a, "GET", "/api/analysis/workspace", "")
	if without.Code != 409 {
		t.Fatalf("workspace should require workbook: %d", without.Code)
	}
	for _, file := range []string{"app.js", "analytics.css"} {
		res := analysisRequest(t, a, "GET", "/assets/"+file, "")
		if res.Code != 200 || res.Body.Len() < 100 {
			t.Errorf("new frontend asset missing: %s %d", file, res.Code)
		}
	}
}

func TestAnalysisCleanDetailsAndDashboardOrder(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "investment.xlsx")
	writeAnalysisWorkbook(t, path)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	meta, _ := wb.FindSheet("Invest")
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	q := `{"source":"wb:Invest","headerRow":1,"rows":["C1"],"value":"C3","aggregate":"sum","transforms":[{"id":"D_month","name":"月份","source":"C4","op":"yearmonth"}]}`
	result := decodeResponse[struct {
		ID        string `json:"id"`
		Rows      int    `json:"rows"`
		Truncated bool   `json:"truncated"`
	}](t,
		analysisRequest(t, app, "POST", "/api/analysis/detail/materialize", `{"query":`+q+`,"keys":["北京"],"name":"北京明细","limit":1}`))
	if result.ID == "" || result.Rows != 1 || !result.Truncated {
		t.Fatalf("partial extract wrong: %+v", result)
	}
	sheet := decodeResponse[struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}](t,
		analysisRequest(t, app, "GET", "/api/analysis/sheet?id="+result.ID, ""))
	if len(sheet.Rows) != 1 || sheet.Rows[0][0] != "2" || sheet.Rows[0][3] != "0.1" || sheet.Rows[0][5] != "2026-01" {
		t.Fatalf("derived extract loses data: %+v", sheet)
	}
	first := decodeResponse[struct {
		ID string `json:"id"`
	}](t, analysisRequest(t, app, "POST", "/api/analysis/report", `{"name":"A","query":`+q+`}`))
	second := decodeResponse[struct {
		ID string `json:"id"`
	}](t, analysisRequest(t, app, "POST", "/api/analysis/report", `{"name":"B","query":`+q+`}`))
	order := fmt.Sprintf(`{"ids":[%q,%q]}`, second.ID, first.ID)
	accepted := analysisRequest(t, app, "POST", "/api/analysis/dashboard/order", order)
	if accepted.Code != 200 {
		t.Fatalf("dashboard order failed %s", accepted.Body.String())
	}
	invalid := analysisRequest(t, app, "POST", "/api/analysis/dashboard/order", fmt.Sprintf(`{"ids":[%q,%q]}`, first.ID, first.ID))
	if invalid.Code != 400 {
		t.Fatalf("duplicate order accepted %d", invalid.Code)
	}
	state := decodeResponse[struct {
		DashboardOrder []string `json:"dashboardOrder"`
		Fingerprint    string   `json:"fingerprint"`
	}](t, analysisRequest(t, app, "GET", "/api/analysis/workspace", ""))
	if len(state.DashboardOrder) != 2 || state.DashboardOrder[0] != second.ID || state.Fingerprint == "" {
		t.Fatalf("dashboard not durable %+v", state)
	}
	deleted := analysisRequest(t, app, "POST", "/api/analysis/delete", fmt.Sprintf(`{"kind":"report","id":%q}`, second.ID))
	if deleted.Code != 200 {
		t.Fatalf("delete order error %d", deleted.Code)
	}
	remaining := decodeResponse[struct {
		DashboardOrder []string `json:"dashboardOrder"`
	}](t, analysisRequest(t, app, "GET", "/api/analysis/workspace", ""))
	if len(remaining.DashboardOrder) != 1 || remaining.DashboardOrder[0] != first.ID {
		t.Fatalf("deleted report lingers in dashboard %+v", remaining)
	}
}
