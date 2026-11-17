package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestCSVUploadIndexSheetSearchExportAndRecent(t *testing.T) {
	isolateTestStorage(t)
	original := []byte("单号,金额,摘要\r\n00001,123456789012345.99999,\"医疗,结算\"\r\n00002,-0.01,备用\r\n")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "audit-ledger.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(original); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/open", &buf)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-ExcelLens-Token", app.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("upload status %d %s", response.Code, response.Body.String())
	}
	var uploaded struct {
		Source   workbook.SourceInfo  `json:"source"`
		Sheets   []workbook.SheetMeta `json:"sheets"`
		FileName string               `json:"fileName"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Source.Format != "csv" || uploaded.FileName != "audit-ledger.csv" {
		t.Fatalf("bad status: %+v", uploaded)
	}
	idxBegin := featureCall(t, app, "POST", "/api/index/start", `{"sheet":"数据"}`)
	var begin struct {
		JobID string `json:"jobId"`
	}
	if err = json.Unmarshal(idxBegin.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	job := app.indexJobs[begin.JobID]
	app.mu.RUnlock()
	if job == nil {
		t.Fatal("index not queued")
	}
	select {
	case <-job.done:
	case <-time.After(8 * time.Second):
		t.Fatal("CSV index timed out")
	}
	job.mu.RLock()
	status, indexError := job.Status, job.Error
	job.mu.RUnlock()
	if status != "done" {
		t.Fatalf("index=%s error=%s", status, indexError)
	}
	cell := featureCall(t, app, "GET", "/api/cell?sheet=%E6%95%B0%E6%8D%AE&ref=A2", "")
	if !strings.Contains(cell.Body.String(), "00001") {
		t.Fatalf("cell data missing %s", cell.Body.String())
	}
	recent := featureCall(t, app, "GET", "/api/recent", "")
	if !strings.Contains(recent.Body.String(), "audit-ledger.csv") {
		t.Fatalf("recent missing %s", recent.Body.String())
	}
	for _, fmt := range []string{"csv", "xlsx"} {
		out := featureCall(t, app, "POST", "/api/export/start", `{"scope":"sheet","sheet":"数据","format":"`+fmt+`","mode":"values","styles":true}`)
		var sent struct {
			JobID string `json:"jobId"`
		}
		if e := json.Unmarshal(out.Body.Bytes(), &sent); e != nil {
			t.Fatal(e)
		}
		app.mu.RLock()
		exportJob := app.exportJobs[sent.JobID]
		app.mu.RUnlock()
		if exportJob == nil {
			t.Fatalf("export %s not queued", fmt)
		}
		select {
		case <-exportJob.done:
		case <-time.After(8 * time.Second):
			t.Fatalf("export %s timed out", fmt)
		}
		exportJob.mu.RLock()
		state, savePath, why := exportJob.Status, exportJob.FilePath, exportJob.Error
		exportJob.mu.RUnlock()
		if state != "done" {
			t.Fatalf("export %s failed: %s", fmt, why)
		}
		if fmt == "xlsx" {
			out, err := workbook.OpenWorkbook(savePath)
			if err != nil {
				t.Fatal(err)
			}
			m := out.Sheets[0]
			ind, err := out.BuildIndex(m, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			row, err := out.ReadRow(ind, 1, 1, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(row.Cells) != 3 || row.Cells[0].Raw != "00001" || row.Cells[1].Raw != "123456789012345.99999" {
				t.Fatalf("XLSX roundtrip damaged financial values: %+v", row.Cells)
			}
			r, err := zip.OpenReader(savePath)
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Close()
		} else {
			b, err := os.ReadFile(savePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(b, []byte("00001")) {
				t.Fatal("CSV export missing identifier")
			}
		}
	}
	// Only source temp copy is changed through import, never the file content.
	uploadedPath := app.currentWorkbook().Path
	saved, err := os.ReadFile(uploadedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, saved) {
		t.Fatal("upload changed file content")
	}
}
func TestCSVRecentReopensAndTwoTabsAreIsolated(t *testing.T) {
	isolateTestStorage(t)
	p1 := filepath.Join(t.TempDir(), "one.csv")
	p2 := filepath.Join(t.TempDir(), "two.txt")
	_ = os.WriteFile(p1, []byte("ID,金额\n00001,150.80\n"), 0600)
	_ = os.WriteFile(p2, []byte("ID|金额\n002|199.00\n"), 0600)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	a, err := app.OpenPathForLaunch(p1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := app.OpenPathForLaunch(p2)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, name string }{{a, "one.csv"}, {b, "two.txt"}} {
		status := sessionCall(t, app, tc.id, "GET", "/api/status", "")
		if status.Code != 200 || !strings.Contains(status.Body.String(), tc.name) {
			t.Fatalf("session %s: %s", tc.id, status.Body.String())
		}
	}
}

func TestOracleTextLedgerExactAccountingStatsAndPivot(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "oracle-ledger.txt")
	original := []byte("机构|账户|金额\n北京|000012345|(234.25)\n北京|000017890|1,234.50\n上海|000000102|0.000001\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 4 || idx.MaxCol != 3 {
		t.Fatalf("unexpected shape %d %d", idx.MaxRow, idx.MaxCol)
	}
	if got := cellValueServer(t, wb, idx, 2, 3); got != "(234.25)" {
		t.Fatalf("changed original amount %q", got)
	}
	fields := decodeResponse[struct {
		Fields []struct {
			Name    string `json:"name"`
			Numeric bool   `json:"numeric"`
		} `json:"fields"`
	}](t,
		analysisRequest(t, app, "GET", "/api/analysis/fields?source=wb%3A%E6%95%B0%E6%8D%AE&headerRow=1", ""))
	if len(fields.Fields) != 3 || !fields.Fields[2].Numeric || fields.Fields[1].Numeric {
		t.Fatalf("field numeric recognition failed: %+v", fields.Fields)
	}
	stats := decodeResponse[selectionStatResult](t,
		analysisRequest(t, app, "POST", "/api/selection/stats", `{"sheet":"数据","ranges":[{"start":"C2","end":"C4"}]}`))
	if stats.Number != 3 || stats.Sum != "1000.250001" {
		t.Fatalf("bad exact selection stats: %+v", stats)
	}
	profile := decodeResponse[profileReport](t,
		analysisRequest(t, app, "POST", "/api/profile", `{"sheet":"数据","start":"C2","end":"C4"}`))
	if profile.Number != 3 || profile.Sum != "1000.250001" {
		t.Fatalf("bad exact profile: %+v", profile)
	}
	started := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t,
		analysisRequest(t, app, "POST", "/api/analysis/query/start", `{"source":"wb:数据","headerRow":1,"rows":["C1"],"value":"C3","aggregate":"sum","precision":6}`))
	app.analysisMu.Lock()
	job := app.analysisJobs[started.JobID]
	app.analysisMu.Unlock()
	if job == nil {
		t.Fatal("analysis job not created")
	}
	select {
	case <-job.done:
	case <-time.After(8 * time.Second):
		t.Fatal("pivot timed out")
	}
	res := awaitAnalysis(t, app, started.JobID)
	if res.Grand.Exact != "1000.250001" || len(res.Rows) != 2 {
		t.Fatalf("pivot wrong %+v", res)
	}
	if updated, _ := os.ReadFile(path); !bytes.Equal(updated, original) {
		t.Fatal("Oracle source was modified")
	}
}

func cellValueServer(t *testing.T, w *workbook.Workbook, idx *workbook.SheetIndex, row, col int) string {
	t.Helper()
	got, err := w.ReadRow(idx, row-1, col, col)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Cells {
		if c.Col == col {
			return c.Raw
		}
	}
	return ""
}

func TestOracleAtAndOfficeHTMLUploadStatsAndXLSXExport(t *testing.T) {
	for _, tc := range []struct{ name, body, format string }{
		{"oracle.txt", "账户@金额@贷方\n0000123456789012345@1.234567@\n0002@2@\n0003@-0.000001@\n", "txt"},
		{"wps.xls", `<html><table><tr><th>账户</th><th>金额</th><th>贷方</th></tr><tr><td x:str>0000123456789012345</td><td x:num>1.234567</td><td></td></tr><tr><td>0002</td><td>2</td><td></td></tr><tr><td>0003</td><td>-0.000001</td><td></td></tr></table></html>`, "xls-html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			app, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := mw.CreateFormFile("file", tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/open", &body)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("X-ExcelLens-Token", app.token)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, req)
			if response.Code != 200 {
				t.Fatalf("upload %d: %s", response.Code, response.Body.String())
			}
			wb := app.currentWorkbook()
			if wb.Format != tc.format {
				t.Fatalf("wrong format: %+v", wb.SourceInfo)
			}
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if idx.MaxRow != 4 || idx.MaxCol != 3 {
				t.Fatalf("shape %d x %d", idx.MaxRow, idx.MaxCol)
			}
			encoded, err := json.Marshal(map[string]any{"sheet": wb.Sheets[0].Name, "ranges": []map[string]string{{"start": "B2", "end": "B4"}}})
			if err != nil {
				t.Fatal(err)
			}
			stats := decodeResponse[selectionStatResult](t, analysisRequest(t, app, "POST", "/api/selection/stats", string(encoded)))
			if stats.Number != 3 || stats.Sum != "3.234566" {
				t.Fatalf("exact accounting stats: %+v", stats)
			}
			encoded, err = json.Marshal(map[string]any{"scope": "sheet", "sheet": wb.Sheets[0].Name, "format": "xlsx", "mode": "values", "styles": true})
			if err != nil {
				t.Fatal(err)
			}
			started := decodeResponse[struct {
				JobID string `json:"jobId"`
			}](t, featureCall(t, app, "POST", "/api/export/start", string(encoded)))
			app.mu.RLock()
			job := app.exportJobs[started.JobID]
			app.mu.RUnlock()
			if job == nil {
				t.Fatal("export was not queued")
			}
			select {
			case <-job.done:
			case <-time.After(8 * time.Second):
				t.Fatal("XLSX export timed out")
			}
			job.mu.RLock()
			status, outPath, why := job.Status, job.FilePath, job.Error
			job.mu.RUnlock()
			if status != "done" {
				t.Fatalf("export %s: %s", status, why)
			}
			out, err := workbook.OpenWorkbook(outPath)
			if err != nil {
				t.Fatal(err)
			}
			outIndex, err := out.BuildIndex(out.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cellValueServer(t, out, outIndex, 2, 1) != "0000123456789012345" || cellValueServer(t, out, outIndex, 2, 2) != "1.234567" || cellValueServer(t, out, outIndex, 4, 2) != "-0.000001" {
				t.Fatal("XLSX export changed an account number or financial amount")
			}
			if saved, err := os.ReadFile(wb.Path); err != nil || string(saved) != tc.body {
				t.Fatal("uploaded source content was changed", err)
			}
		})
	}
}
