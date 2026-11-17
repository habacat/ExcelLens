package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func dataRuleServerParts(extra int) map[string]string {
	parts := catalogueServerParts(0)
	var rules strings.Builder
	rules.WriteString(`<dataValidation type="list" sqref="A2:A1048576" allowBlank="1" showErrorMessage="1" errorStyle="warning" promptTitle="编号" prompt="保留前导零" errorTitle="核对" error="请选择保存的编号"><formula1>"001,,中文,&lt;img src=x&gt;"</formula1></dataValidation>`)
	rules.WriteString(`<dataValidation type="decimal" sqref="B2:B1048576"><formula1>9007199254740993.31</formula1><formula2>9007199254740994.31</formula2></dataValidation>`)
	rules.WriteString(`<dataValidation type="custom" sqref="B2 B2:B3"><formula1>OR(B2=0,B2&gt;1)</formula1></dataValidation>`)
	rules.WriteString(`<dataValidation type="whole" sqref="XFE1"><formula1>1</formula1><formula2>9</formula2></dataValidation>`)
	rules.WriteString(`<dataValidation type="list" sqref="D2"><formula1>候选名称</formula1></dataValidation>`)
	for at := 0; at < extra; at++ {
		fmt.Fprintf(&rules, `<dataValidation type="whole" sqref="C1:C1048576"><formula1>%d</formula1><formula2>%d</formula2></dataValidation>`, at, at+1)
	}
	ext := `<extLst><ext uri="{CCE6A557-97BC-4B89-ADB6-D9C93CAAB3DF}" xmlns:k="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main" xmlns:m="http://schemas.microsoft.com/office/excel/2006/main"><k:dataValidations count="1" disablePrompts="1"><k:dataValidation type="whole"><k:formula1><m:f>2</m:f></k:formula1><k:formula2><m:f>8</m:f></k:formula2><m:sqref>A2</m:sqref></k:dataValidation></k:dataValidations></ext></extLst>`
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `</worksheet>`, fmt.Sprintf(`<dataValidations count="%d">%s</dataValidations>%s</worksheet>`, 5+extra, rules.String(), ext), 1)
	return parts
}

func dataRuleServerFixture(t *testing.T, extra int) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "data-rules.xlsx")
	writeZipArchive(t, file, dataRuleServerParts(extra))
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	return a, file
}

func prepareDataRuleSheet(t *testing.T, a *App) {
	t.Helper()
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("明细")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func dataRuleStart(t *testing.T, a *App) *dataRuleTask {
	t.Helper()
	wb := a.currentWorkbook()
	response := reconcilePost(t, a, "/api/data-rules/start", dataRuleStartRequest{wb.Fingerprint, sourceBookID(wb), "明细"})
	id := decodeResponse[struct{ JobID string }](t, response).JobID
	a.mu.RLock()
	job := a.dataRuleJobs[id]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("start did not retain data-rule task")
	}
	return job
}

func dataRuleWait(t *testing.T, a *App, job *dataRuleTask) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("data-rule task did not terminate")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if job.Status != "done" || job.Snapshot == nil {
		t.Fatal(job.Status, job.Error)
	}
}

type dataRulePageResponse struct {
	Page                          workbook.DataRuleListPage
	Summary                       workbook.DataRuleSummary
	WorkbookKey, Book, Sheet, Ref string
}

func dataRulePageFor(t *testing.T, a *App, job *dataRuleTask, params url.Values) dataRulePageResponse {
	t.Helper()
	params.Set("id", job.ID)
	return decodeResponse[dataRulePageResponse](t, analysisRequest(t, a, "GET", "/api/data-rules/items?"+params.Encode(), ""))
}

func TestDataRuleHTTPExplicitPreparedSourcePagingCellHitsAndSourceTruth(t *testing.T) {
	a, file := dataRuleServerFixture(t, 125)
	wb := a.currentWorkbook()
	before, _ := os.ReadFile(file)
	if response := reconcilePost(t, a, "/api/data-rules/start", dataRuleStartRequest{wb.Fingerprint, sourceBookID(wb), "明细"}); response.Code != 409 {
		t.Fatal("metadata request silently built an index", response.Code)
	}
	prepareDataRuleSheet(t, a)
	job := dataRuleStart(t, a)
	dataRuleWait(t, a, job)
	page := dataRulePageFor(t, a, job, url.Values{})
	if page.Page.Total != 131 || page.Page.Matched != 131 || len(page.Page.Items) != formats.DataRulePageRows || page.Page.ScopeComplete || page.Summary.UnknownScopes != 1 || page.Summary.PromptSettingsKnown || page.Book != sourceBookID(wb) || page.WorkbookKey != wb.Fingerprint || page.Sheet != "明细" {
		t.Fatal(page)
	}
	seen := map[int]bool{}
	for offset := 0; offset < page.Page.Total; offset = page.Page.Next {
		page = dataRulePageFor(t, a, job, url.Values{"offset": {fmt.Sprint(offset)}})
		for _, item := range page.Page.Items {
			if seen[item.ID] {
				t.Fatal("rule duplicated across pages", item)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 131 {
		t.Fatal("paging omitted rules", len(seen))
	}
	hits := dataRulePageFor(t, a, job, url.Values{"ref": {"b2"}})
	if hits.Ref != "B2" || hits.Page.Matched != 2 || hits.Page.Items[0].ID != 2 || hits.Page.Items[1].ID != 3 {
		t.Fatal("source-cell hits lost overlap or source order", hits)
	}
	issues := dataRulePageFor(t, a, job, url.Values{"status": {"issues"}})
	if issues.Page.Matched != 2 {
		t.Fatal("unresolved and invalid definitions lost", issues)
	}
	zero := dataRulePageFor(t, a, job, url.Values{"ref": {"IV100"}})
	if zero.Page.Matched != 0 || zero.Page.ScopeComplete {
		t.Fatal("empty hit hid unknown source scope", zero)
	}
	jsonPage, _ := json.Marshal(page)
	if strings.Contains(string(jsonPage), `"rawXml"`) || strings.Contains(string(jsonPage), `"formula1"`) || strings.Contains(string(jsonPage), `"inlineList"`) {
		t.Fatal("list response included full definitions")
	}
	detail := decodeResponse[struct{ Detail workbook.DataRuleDetailPage }](t, analysisRequest(t, a, "GET", "/api/data-rules/detail?id="+job.ID+"&item=1", ""))
	item := detail.Detail.Item
	if item.InlineList[0] != "001" || item.InlineList[1] != "" || item.InlineList[3] != "<img src=x>" || item.ErrorStyle != "warning" || item.Prompt != "保留前导零" || item.RawXML == "" {
		t.Fatal("source rule was normalized or invented", item)
	}
	resolve := decodeResponse[struct{ Area map[string]string }](t, analysisRequest(t, a, "GET", "/api/data-rules/resolve?"+url.Values{"id": {job.ID}, "item": {"1"}, "area": {"0"}, "workbookKey": {wb.Fingerprint}, "book": {sourceBookID(wb)}}.Encode(), ""))
	if resolve.Area["sheet"] != "明细" || resolve.Area["start"] != "A2" || resolve.Area["end"] != "A1048576" {
		t.Fatal("whole-column rule expanded or changed scope", resolve)
	}
	meta, _ := wb.FindSheet("汇总")
	if wb.GetCachedIndex(meta.Path) != nil {
		t.Fatal("rule request prepared another sheet")
	}
	cell := decodeResponse[struct{ Cell workbook.CellData }](t, analysisRequest(t, a, "GET", "/api/cell?sheet="+url.QueryEscape("明细")+"&ref=B2", ""))
	if cell.Cell.Raw != "9007199254740993.31" || cell.Cell.Type != "" {
		t.Fatal("rule interface changed exact source amount or type", cell)
	}
	missing := decodeResponse[struct{ Cell workbook.CellData }](t, analysisRequest(t, a, "GET", "/api/cell?sheet="+url.QueryEscape("明细")+"&ref=B4", ""))
	if !missing.Cell.HasFormula || !missing.Cell.CacheMissing {
		t.Fatal("missing formula cache was invented", missing)
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("data rules wrote source content")
	}
}

func TestDataRuleHTTPQueuedCancelLimitsPinsReplacementAndLateCompletion(t *testing.T) {
	a, file := dataRuleServerFixture(t, 0)
	prepareDataRuleSheet(t, a)
	wb := a.currentWorkbook()
	for at := 0; at < cap(a.querySem); at++ {
		a.querySem <- struct{}{}
	}
	first, second := dataRuleStart(t, a), dataRuleStart(t, a)
	if response := reconcilePost(t, a, "/api/data-rules/start", dataRuleStartRequest{wb.Fingerprint, sourceBookID(wb), "明细"}); response.Code != 429 {
		t.Fatal("active rule capacity not enforced", response.Code)
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 2 {
		t.Fatal("queued rule tasks did not pin source", pins)
	}
	reconcilePost(t, a, "/api/data-rules/cancel", map[string]string{"id": first.ID})
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled queue did not exit")
	}
	if first.Status != "cancelled" || first.Snapshot != nil {
		t.Fatal("queue cancellation published partial rules")
	}
	clone := filepath.Join(t.TempDir(), "equal-copy.xlsx")
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(clone, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(clone); err != nil {
		t.Fatal(err)
	}
	select {
	case <-second.done:
	default:
		t.Fatal("replace returned while a rule worker remained active")
	}
	for at := 0; at < cap(a.querySem); at++ {
		<-a.querySem
	}
	if len(a.dataRuleJobs) != 0 || second.Snapshot != nil || second.Status != "cancelled" {
		t.Fatal("replacement kept old rules")
	}
	a.sourceMu.Lock()
	pins = a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("cancelled rule task leaked source pins", pins)
	}
	if response := reconcilePost(t, a, "/api/data-rules/start", dataRuleStartRequest{wb.Fingerprint, sourceBookID(wb), "明细"}); response.Code != 409 {
		t.Fatal("same bytes at another path accepted stale start", response.Code)
	}
	prepareDataRuleSheet(t, a)
	job := dataRuleStart(t, a)
	dataRuleWait(t, a, job)
	reconcilePost(t, a, "/api/data-rules/cancel", map[string]string{"id": job.ID})
	if response := analysisRequest(t, a, "GET", "/api/data-rules/status?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("unadopted fast completion retained snapshot", response.Code)
	}
}

func TestDataRuleHTTPHistorySessionsSourceChangesAndInvalidRequests(t *testing.T) {
	a, file := dataRuleServerFixture(t, 0)
	prepareDataRuleSheet(t, a)
	wb := a.currentWorkbook()
	var first string
	var last *dataRuleTask
	for at := 0; at < formats.DataRuleTaskHistory+3; at++ {
		last = dataRuleStart(t, a)
		dataRuleWait(t, a, last)
		if first == "" {
			first = last.ID
		}
	}
	a.mu.RLock()
	count, bytes := 0, int64(0)
	for _, job := range a.dataRuleJobs {
		if job.Snapshot != nil {
			count++
			bytes += job.Snapshot.BudgetBytes()
		}
	}
	tasks := len(a.dataRuleJobs)
	a.mu.RUnlock()
	if count > formats.DataRuleSnapshots || tasks > formats.DataRuleTaskHistory || bytes > formats.DataRuleSnapshotBytes {
		t.Fatal("unbounded rule task history", count, tasks, bytes)
	}
	if response := analysisRequest(t, a, "GET", "/api/data-rules/status?id="+first, ""); response.Code != 404 {
		t.Fatal("evicted rule task still accessible")
	}
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	if response := analysisRequest(t, child.app, "GET", "/api/data-rules/status?id="+last.ID, ""); response.Code != 404 {
		t.Fatal("rule result crossed browser sessions")
	}
	for _, body := range []string{`{}`, `{"sheet":"明细","unexpected":1}`, strings.Repeat("x", formats.DataRuleRequestBytes+1)} {
		if response := analysisRequest(t, a, "POST", "/api/data-rules/start", body); response.Code != 400 {
			t.Fatal("malformed rule request accepted", response.Code)
		}
	}
	for _, values := range []url.Values{{"offset": {"-1"}}, {"offset": {"9999"}}, {"ref": {"A0"}}, {"ref": {"A+1"}}, {"ref": {"A1048577"}}, {"type": {"unknown"}}, {"status": {"valid"}}} {
		values.Set("id", last.ID)
		if response := analysisRequest(t, a, "GET", "/api/data-rules/items?"+values.Encode(), ""); response.Code != 400 {
			t.Fatal("invalid source query accepted", values, response.Code)
		}
	}
	for _, values := range []url.Values{{"item": {"0"}}, {"item": {"1"}, "rangeOffset": {"2"}}, {"item": {"1"}, "listOffset": {"999"}}} {
		values.Set("id", last.ID)
		if response := analysisRequest(t, a, "GET", "/api/data-rules/detail?"+values.Encode(), ""); response.Code != 400 {
			t.Fatal("invalid detail query accepted", response.Code)
		}
	}
	if response := analysisRequest(t, a, "GET", "/api/data-rules/resolve?"+url.Values{"id": {last.ID}, "item": {"1"}, "book": {"other"}, "workbookKey": {wb.Fingerprint}}.Encode(), ""); response.Code != 409 {
		t.Fatal("foreign source accepted for location")
	}
	parts := dataRuleServerParts(0)
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], "9007199254740993.31", "9007199254740993.32", 1)
	writeZipArchive(t, file, parts)
	if response := analysisRequest(t, a, "GET", "/api/data-rules/items?id="+last.ID, ""); response.Code != 409 {
		t.Fatal("changed source still served rule snapshot", response.Code)
	}
	reconcilePost(t, a, "/api/data-rules/clear", map[string]string{"id": last.ID})
	if response := analysisRequest(t, a, "GET", "/api/data-rules/status?id="+last.ID, ""); response.Code != 404 {
		t.Fatal("clear retained rules")
	}
}

func TestDataRuleHTTPUnsupportedFormatsUseSharedRegistry(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	if response := reconcilePost(t, a, "/api/data-rules/start", dataRuleStartRequest{wb.Fingerprint, sourceBookID(wb), "any"}); response.Code != 422 {
		t.Fatal("rule support inferred outside formats", response.Code)
	}
}

func TestDataRuleHTTPReadFailureNeverPublishesAnEmptySuccessfulList(t *testing.T) {
	for _, mode := range []string{"declared-count", "cache-crc"} {
		t.Run(mode, func(t *testing.T) {
			a, file := dataRuleServerFixture(t, 0)
			if mode == "declared-count" {
				parts := dataRuleServerParts(0)
				parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<dataValidations count="5">`, `<dataValidations count="6">`, 1)
				writeZipArchive(t, file, parts)
				if err := a.OpenPath(file); err != nil {
					t.Fatal(err)
				}
			}
			prepareDataRuleSheet(t, a)
			if mode == "cache-crc" {
				wb := a.currentWorkbook()
				meta, _ := wb.FindSheet("明细")
				idx := wb.GetCachedIndex(meta.Path)
				data, err := os.ReadFile(idx.XMLPath)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.Replace(string(data), "保留前导零", "保留前导一", 1))
				if err := os.WriteFile(idx.XMLPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			job := dataRuleStart(t, a)
			select {
			case <-job.done:
			case <-time.After(5 * time.Second):
				t.Fatal("failed rule read did not terminate")
			}
			status := decodeResponse[struct{ Status, Error string }](t, analysisRequest(t, a, "GET", "/api/data-rules/status?id="+job.ID, ""))
			if status.Status != "error" || status.Error == "" || job.Snapshot != nil {
				t.Fatal("failed read became a successful partial result", status)
			}
			if response := analysisRequest(t, a, "GET", "/api/data-rules/items?id="+job.ID, ""); response.Code != 409 {
				t.Fatal("failed read served an empty success", response.Code)
			}
		})
	}
}
