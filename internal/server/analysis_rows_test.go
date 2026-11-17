package server

import (
	"fmt"
	"testing"
)

func TestAppendAnalysisRowsUsesPersistedSizeAndNeverClearsCells(t *testing.T) {
	a, _ := setupProApp(t)
	created := decodeResponse[struct {
		ID string `json:"id"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/sheet/create", `{"name":"行追加","columns":["内容"]}`))
	// The browser still has a zero-row summary after a paste extends the sheet.
	body := fmt.Sprintf(`{"id":%q,"expectedRevision":0,"changes":[{"row":9,"col":0,"value":"原有第十行"},{"row":19,"col":0,"value":"新增的第二十行"}]}`, created.ID)
	if response := analysisRequest(t, a, "POST", "/api/analysis/sheet/batch", body); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	appendBody := fmt.Sprintf(`{"id":%q,"workbookKey":%q,"expectedRevision":1,"count":10}`, created.ID, a.currentWorkbook().Fingerprint)
	appended := decodeResponse[struct {
		Total    int   `json:"total"`
		Revision int64 `json:"revision"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/sheet/rows", appendBody))
	if appended.Total != 30 || appended.Revision != 2 {
		t.Fatalf("wrong persisted append: %+v", appended)
	}
	page := decodeResponse[struct {
		Rows     [][]string `json:"rows"`
		Revision int64      `json:"revision"`
	}](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+created.ID, ""))
	if len(page.Rows) != 30 || page.Rows[9][0] != "原有第十行" || page.Rows[19][0] != "新增的第二十行" {
		t.Fatal("append changed existing values", page)
	}
	if response := analysisRequest(t, a, "POST", "/api/analysis/sheet/rows", appendBody); response.Code != 409 {
		t.Fatal("stale append was accepted", response.Code)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"id":%q,"workbookKey":"old","expectedRevision":2,"count":10}`, created.ID),
		fmt.Sprintf(`{"id":%q,"workbookKey":"old","expectedRevision":2,"changes":[{"row":0,"col":0,"value":"bad"}]}`, created.ID),
	} {
		endpoint := "/api/analysis/sheet/rows"
		if bad[len(bad)-2:] == "]}" {
			endpoint = "/api/analysis/sheet/batch"
		}
		if response := analysisRequest(t, a, "POST", endpoint, bad); response.Code != 409 {
			t.Fatal("wrong workbook accepted", response.Code, response.Body.String())
		}
	}
	tooMany := fmt.Sprintf(`{"id":%q,"workbookKey":%q,"expectedRevision":2,"count":10000}`, created.ID, a.currentWorkbook().Fingerprint)
	if response := analysisRequest(t, a, "POST", "/api/analysis/sheet/rows", tooMany); response.Code != 400 {
		t.Fatal("row limit ignored")
	}
	after := decodeResponse[struct {
		Total    int   `json:"total"`
		Revision int64 `json:"revision"`
	}](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+created.ID, ""))
	if after.Total != 30 || after.Revision != 2 {
		t.Fatal("rejected append changed sheet", after)
	}
}
