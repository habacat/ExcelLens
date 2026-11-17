package server

import (
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestHTMLDocumentArchiveHTTPLayoutFilterAndRecent(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	html := []byte(`<html><table name="section-one"><tr><td colspan="2"><b>报表标题</b></td></tr></table><p><br></p><table id="not-a-tab"><tr><td>编号</td><td>金额</td></tr><tr><td>=&quot;000000000000000001&quot;</td><td>1.2500001</td></tr><tr><td>00002</td><td>2.50</td></tr></table></html>`)
	uploadArchiveFixture(t, app, "reports.zip", zipArchiveFixture(t, map[string][]byte{"账簿.xls": html}))
	id := app.archiveSummary()["id"].(string)
	awaitArchiveFixture(t, app, id, 0)
	wb := app.currentWorkbook()
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "账簿" {
		t.Fatal("hashed name or fake sections", wb.Sheets)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil || idx.MaxRow != 5 {
		t.Fatal(idx, err)
	}
	params := url.Values{"sheet": []string{"账簿"}}
	layout := decodeResponse[sheetLayout](t, featureCall(t, app, "GET", "/api/layout?"+params.Encode(), ""))
	if len(layout.SourceMerges) != 1 || layout.SourceMerges[0] != (workbook.HTMLMerge{Row: 1, Col: 1, EndRow: 1, EndCol: 2}) {
		t.Fatal(layout.SourceMerges)
	}
	cell := featureCall(t, app, "GET", "/api/cell?"+params.Encode()+"&ref=A4", "")
	if !strings.Contains(cell.Body.String(), `"raw":"000000000000000001"`) || !strings.Contains(cell.Body.String(), `"hasFormula":true`) {
		t.Fatal(cell.Body.String())
	}
	started := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, featureCall(t, app, "POST", "/api/filter/start", `{"sheet":"账簿","headerRow":3,"rules":[{"column":1,"operator":"in","values":["00002"]}]}`))
	app.mu.RLock()
	job := app.filterJobs[started.JobID]
	app.mu.RUnlock()
	if job == nil {
		t.Fatal("filter missing")
	}
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("filter timeout")
	}
	job.mu.RLock()
	resultID, status, why := job.ResultID, job.Status, job.Error
	job.mu.RUnlock()
	if status != "done" {
		t.Fatal(status, why)
	}
	app.mu.RLock()
	result := app.filterResults[resultID]
	app.mu.RUnlock()
	if result == nil || !reflect.DeepEqual(slices.Collect(result.Rows.Iter()), []int{0, 1, 2, 4}) {
		t.Fatal("source filter coordinates shifted", result)
	}
	featureCall(t, app, "GET", "/assets/merges.js", "")
	recents := readRecents()
	if len(recents.Items) != 1 {
		t.Fatal(recents)
	}
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	opened := featureCall(t, other, "POST", "/api/recent", `{"action":"open","id":"`+recents.Items[0].ID+`"}`)
	var response struct {
		Sheets []workbook.SheetMeta `json:"sheets"`
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &response); err != nil || len(response.Sheets) != 1 || response.Sheets[0].Name != "账簿" {
		t.Fatal("recent restored fake tabs", opened.Body.String(), err)
	}
}
