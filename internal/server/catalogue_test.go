package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func catalogueServerParts(names int) map[string]string {
	const ns = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	definitions := `<definedName name="金额范围">'明细'!$B$2:$B$4</definedName><definedName name="同名">'汇总'!$D$7</definedName><definedName name="同名" localSheetId="0" hidden="1">'明细'!$A$2</definedName><definedName name="结构化">账表[金额]</definedName><definedName name="多区域">'明细'!$A$2:$A$4,'汇总'!$D$7</definedName><definedName name="常量">&quot;000123&quot;</definedName><definedName name="外部">'[其他.xlsx]Sheet1'!$A$1</definedName>`
	for n := 0; n < names; n++ {
		definitions += fmt.Sprintf(`<definedName name="条目%03d">'明细'!$A$2</definedName>`, n)
	}
	return map[string]string{
		"xl/workbook.xml":                     `<workbook xmlns="` + ns + `" xmlns:r="` + rel + `"><sheets><sheet name="明细" sheetId="1" r:id="s1"/><sheet name="汇总" sheetId="2" r:id="s2"/></sheets><definedNames>` + definitions + `</definedNames></workbook>`,
		"xl/_rels/workbook.xml.rels":          `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="` + rel + `/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="` + rel + `/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":            `<worksheet xmlns="` + ns + `" xmlns:r="` + rel + `"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>001</t></is></c><c r="B2"><v>9007199254740993.31</v></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>002</t></is></c><c r="B3"><v>0</v></c></row><row r="4"><c r="B4"><f>SUM(B2:B3)</f></c></row></sheetData><tableParts count="1"><tablePart r:id="t1"/></tableParts></worksheet>`,
		"xl/worksheets/sheet2.xml":            `<worksheet xmlns="` + ns + `"><sheetData><row r="7"><c r="D7"><v>8</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/_rels/sheet1.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="t1" Type="` + rel + `/table" Target="../tables/table1.xml"/></Relationships>`,
		"xl/tables/table1.xml":                `<table xmlns="` + ns + `" id="1" name="账表" displayName="账表" ref="A1:B4"><tableColumns count="2"><tableColumn id="1" name="编号"/><tableColumn id="2" name="金额"/></tableColumns></table>`,
	}
}

func catalogueServerFixture(t *testing.T, names int) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "catalogue.xlsx")
	writeZipArchive(t, file, catalogueServerParts(names))
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

func catalogueStart(t *testing.T, a *App, mode, sheet string) *catalogueTask {
	t.Helper()
	wb := a.currentWorkbook()
	response := reconcilePost(t, a, "/api/catalogue/start", catalogueStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Mode: mode, Sheet: sheet})
	id := decodeResponse[struct{ JobID string }](t, response).JobID
	a.mu.RLock()
	job := a.catalogueJobs[id]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("start did not retain task")
	}
	return job
}

func catalogueWait(t *testing.T, a *App, job *catalogueTask) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("catalogue job did not terminate")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if job.Status != "done" || job.Snapshot == nil {
		t.Fatal(job.Status, job.Error)
	}
}

type cataloguePage struct {
	Items                                  []catalogueListItem `json:"items"`
	Offset, Previous, Next, Matched, Total int
	Complete                               bool
	Notices                                []string
	WorkbookKey, Book                      string
}

func cataloguePageFor(t *testing.T, a *App, job *catalogueTask, params url.Values) cataloguePage {
	t.Helper()
	params.Set("id", job.ID)
	return decodeResponse[cataloguePage](t, analysisRequest(t, a, "GET", "/api/catalogue/items?"+params.Encode(), ""))
}

func catalogueDetailFor(t *testing.T, a *App, job *catalogueTask, id string) workbook.CatalogueItem {
	t.Helper()
	data := decodeResponse[struct{ Item workbook.CatalogueItem }](t, analysisRequest(t, a, "GET", "/api/catalogue/detail?"+url.Values{"id": {job.ID}, "item": {id}}.Encode(), ""))
	return data.Item
}

func catalogueItemID(t *testing.T, job *catalogueTask, name, scope string) string {
	t.Helper()
	for _, item := range job.Snapshot.Value.Items {
		if item.Name == name && item.Scope == scope {
			return item.ID
		}
	}
	t.Fatal("missing catalogue item", name, scope)
	return ""
}

func TestCatalogueHTTPMetadataOnlyPagingScopesDefinitionsAndSourceTruth(t *testing.T) {
	a, file := catalogueServerFixture(t, 125)
	wb := a.currentWorkbook()
	before, _ := os.ReadFile(file)
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
	page := cataloguePageFor(t, a, job, url.Values{})
	if page.Total != 133 || page.Matched != 133 || len(page.Items) != formats.CataloguePageRows || !page.Complete || page.WorkbookKey != wb.Fingerprint || page.Book != sourceBookID(wb) {
		t.Fatal("metadata list mismatch", page)
	}
	seen := map[string]bool{}
	for offset := 0; offset < page.Total; offset = page.Next {
		page = cataloguePageFor(t, a, job, url.Values{"offset": {fmt.Sprint(offset)}})
		if page.Previous != max(0, offset-formats.CataloguePageRows) || page.Next <= offset || len(page.Items) > formats.CataloguePageRows {
			t.Fatal("pagination lost stable positions", page)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate page item", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 133 {
		t.Fatal("pagination dropped entries", len(seen))
	}
	filtered := cataloguePageFor(t, a, job, url.Values{"q": {"条目12"}, "kind": {"name"}})
	if filtered.Matched != 5 || len(filtered.Items) != 5 {
		t.Fatal("literal name search mismatch", filtered)
	}
	local := cataloguePageFor(t, a, job, url.Values{"q": {"同名"}, "sheet": {"明细"}})
	if len(local.Items) != 1 || !local.Items[0].Hidden || local.Items[0].Scope != "明细" {
		t.Fatal("local hidden scope lost", local)
	}
	global := cataloguePageFor(t, a, job, url.Values{"scope": {"workbook"}})
	if global.Matched != 131 {
		t.Fatal("global name scope mixed local names or tables", global)
	}
	for _, meta := range wb.Sheets {
		if wb.GetCachedIndex(meta.Path) != nil {
			t.Fatal("listing created a source index", meta)
		}
	}
	value := catalogueDetailFor(t, a, job, catalogueItemID(t, job, "常量", ""))
	if value.Status == "ready" || value.Definition != "\"000123\"" || len(value.Areas) > 0 {
		t.Fatal("literal name became a value or coordinate", value)
	}
	qualified := catalogueDetailFor(t, a, job, catalogueItemID(t, job, "多区域", ""))
	if len(qualified.Areas) != 2 || qualified.Areas[0].Sheet != "明细" || qualified.Areas[1].Start != "D7" {
		t.Fatal("union flattened or lost source coordinates", qualified)
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("metadata API wrote source")
	}
}

func TestCatalogueHTTPConfirmationRequiresPreparedSourceAndSnapshotsStayImmutable(t *testing.T) {
	a, _ := catalogueServerFixture(t, 0)
	wb := a.currentWorkbook()
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
	tableID := catalogueItemID(t, job, "账表", "")
	resolve := func(id, item string, area int) int {
		return analysisRequest(t, a, "GET", "/api/catalogue/resolve?"+url.Values{"id": {id}, "item": {item}, "area": {fmt.Sprint(area)}, "workbookKey": {wb.Fingerprint}, "book": {sourceBookID(wb)}}.Encode(), "").Code
	}
	if resolve(job.ID, tableID, 0) != 409 {
		t.Fatal("unverified table navigated")
	}
	if response := reconcilePost(t, a, "/api/catalogue/start", catalogueStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Mode: "confirm", Sheet: "明细"}); response.Code != 409 {
		t.Fatal("confirmation built an index", response.Code, response.Body.String())
	}
	meta, _ := wb.FindSheet("明细")
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := catalogueStart(t, a, "confirm", "明细")
	catalogueWait(t, a, confirmed)
	item := catalogueDetailFor(t, a, confirmed, catalogueItemID(t, confirmed, "账表", ""))
	if item.Status != "ready" || !item.Table.Verified || item.Areas[0].Start != "A1" || item.Areas[0].End != "B4" || resolve(confirmed.ID, item.ID, 0) != 200 {
		t.Fatal("confirmed table did not resolve", item)
	}
	alias := catalogueDetailFor(t, a, confirmed, catalogueItemID(t, confirmed, "结构化", ""))
	if alias.Status != "ready" || alias.Areas[0].Start != "B2" || alias.Areas[0].End != "B4" {
		t.Fatal("common structured reference resolver not used", alias)
	}
	if resolve(job.ID, tableID, 0) != 409 || catalogueDetailFor(t, a, job, tableID).Table.Verified {
		t.Fatal("new proof mutated earlier snapshot")
	}
	row, err := wb.ReadRow(idx, 1, 1, 2)
	if err != nil || row.Cells[0].Raw != "001" || row.Cells[0].Type != "inlineStr" || row.Cells[1].Raw != "9007199254740993.31" || row.Cells[1].Type != "" {
		t.Fatal("confirmation changed values/types", row, err)
	}
	missing, err := wb.ReadRow(idx, 3, 2, 2)
	if err != nil || !missing.Cells[0].HasFormula || !missing.Cells[0].CacheMissing {
		t.Fatal("missing formula cache was invented", missing, err)
	}
	if resolve(confirmed.ID, item.ID, -1) != 400 || resolve(confirmed.ID, item.ID, 1) != 409 {
		t.Fatal("invalid union area accepted")
	}
}

func TestCatalogueHTTPQueueCancellationPinsHistoryAndWorkbookReplacement(t *testing.T) {
	a, file := catalogueServerFixture(t, 0)
	wb := a.currentWorkbook()
	for n := 0; n < cap(a.querySem); n++ {
		a.querySem <- struct{}{}
	}
	defer func() {
		for n := 0; n < cap(a.querySem); n++ {
			<-a.querySem
		}
	}()
	first := catalogueStart(t, a, "read", "")
	second := catalogueStart(t, a, "read", "")
	if response := reconcilePost(t, a, "/api/catalogue/start", catalogueStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Mode: "read"}); response.Code != 429 {
		t.Fatal("active directory capacity not enforced", response.Code)
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 2 {
		t.Fatal("queued task did not pin source", pins)
	}
	if response := reconcilePost(t, a, "/api/catalogue/cancel", map[string]string{"id": first.ID}); response.Code != 200 {
		t.Fatal(response.Code)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("queued cancel did not exit")
	}
	if first.Status != "cancelled" || first.Snapshot != nil {
		t.Fatal("cancelled queue published metadata")
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
		t.Fatal("replace returned before directory task exited")
	}
	if len(a.catalogueJobs) != 0 || second.Snapshot != nil || second.Status != "cancelled" {
		t.Fatal("replaced source retained task/result")
	}
	a.sourceMu.Lock()
	pins = a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("cancelled/replaced task leaked source pin", pins)
	}
	current := a.currentWorkbook()
	if current.Fingerprint != wb.Fingerprint || sourceBookID(current) == sourceBookID(wb) {
		t.Fatal("copy did not isolate path identity")
	}
	if response := reconcilePost(t, a, "/api/catalogue/start", catalogueStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Mode: "read"}); response.Code != 409 {
		t.Fatal("same bytes at other path accepted stale directory request")
	}
}

func TestCatalogueHTTPHistoryClearSessionsMalformedRequestsAndSourceChanges(t *testing.T) {
	a, file := catalogueServerFixture(t, 0)
	firstID := ""
	for n := 0; n < formats.CatalogueTaskHistory+3; n++ {
		job := catalogueStart(t, a, "read", "")
		catalogueWait(t, a, job)
		if firstID == "" {
			firstID = job.ID
		}
	}
	a.mu.RLock()
	snapshots, total := 0, 0
	var last *catalogueTask
	for _, job := range a.catalogueJobs {
		if job.Snapshot != nil {
			snapshots++
			total += job.Snapshot.Bytes
			last = job
		}
	}
	count := len(a.catalogueJobs)
	a.mu.RUnlock()
	if count > formats.CatalogueTaskHistory || snapshots > formats.CatalogueSnapshots || total > formats.CatalogueSnapshotBytes {
		t.Fatal("directory history unbounded", count, snapshots, total)
	}
	if response := analysisRequest(t, a, "GET", "/api/catalogue/status?id="+firstID, ""); response.Code != 404 {
		t.Fatal("evicted task retained result")
	}
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	if response := analysisRequest(t, child.app, "GET", "/api/catalogue/status?id="+last.ID, ""); response.Code != 404 {
		t.Fatal("directory crossed browser session")
	}
	for _, body := range []string{`{}`, `{"mode":"unknown"}`, `{"mode":"read","unexpected":1}`, strings.Repeat("x", formats.CatalogueRequestBytes+1)} {
		if response := analysisRequest(t, a, "POST", "/api/catalogue/start", body); response.Code != 400 {
			t.Fatal("malformed request accepted", response.Code)
		}
	}
	for _, q := range []url.Values{{"offset": {"-1"}}, {"offset": {"9000"}}, {"kind": {"unknown"}}, {"q": {strings.Repeat("x", formats.CatalogueSearchBytes+1)}}} {
		q.Set("id", last.ID)
		if response := analysisRequest(t, a, "GET", "/api/catalogue/items?"+q.Encode(), ""); response.Code != 400 {
			t.Fatal("malformed page accepted", response.Code)
		}
	}
	parts := catalogueServerParts(0)
	parts["xl/worksheets/sheet2.xml"] = strings.Replace(parts["xl/worksheets/sheet2.xml"], "<v>8</v>", "<v>9</v>", 1)
	writeZipArchive(t, file, parts)
	if response := analysisRequest(t, a, "GET", "/api/catalogue/items?id="+last.ID, ""); response.Code != 409 {
		t.Fatal("changed source metadata still usable", response.Code)
	}
	if response := reconcilePost(t, a, "/api/catalogue/clear", map[string]string{"id": last.ID}); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if response := analysisRequest(t, a, "GET", "/api/catalogue/status?id="+last.ID, ""); response.Code != 404 {
		t.Fatal("clear did not release finished result")
	}
}

func TestCatalogueHTTPCancelAfterFastCompletionReleasesUnadoptedSnapshot(t *testing.T) {
	a, _ := catalogueServerFixture(t, 0)
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
	if response := reconcilePost(t, a, "/api/catalogue/cancel", map[string]string{"id": job.ID}); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if response := analysisRequest(t, a, "GET", "/api/catalogue/status?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("unadopted completed snapshot retained")
	}
}

func TestCatalogueHTTPOpenResponseProvidesPathIdentityBeforeAnyIndex(t *testing.T) {
	a, file := catalogueServerFixture(t, 0)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "uploaded.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/open", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("X-ExcelLens-Token", a.token)
	r = localTestConnection(r)
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, r)
	opened := decodeResponse[struct{ WorkbookKey, Book string }](t, response)
	wb := a.currentWorkbook()
	if opened.WorkbookKey != wb.Fingerprint || opened.Book == "" || opened.Book != sourceBookID(wb) {
		t.Fatal("upload response omitted or substituted source path identity", opened)
	}
	for _, sheet := range wb.Sheets {
		if wb.GetCachedIndex(sheet.Path) != nil {
			t.Fatal("source identity required a worksheet index")
		}
	}
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
}

func TestCatalogueHTTPListingNeverOpensUnsupportedEightGiBWorksheetBodies(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "metadata-only.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range catalogueServerParts(0) {
		if strings.HasPrefix(name, "xl/worksheets/sheet") {
			if _, err := z.CreateRaw(&zip.FileHeader{Name: name, Method: 255, UncompressedSize64: 8 << 30}); err != nil {
				t.Fatal(err)
			}
		} else {
			out, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := out.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
	page := cataloguePageFor(t, a, job, url.Values{})
	if page.Total != 8 {
		t.Fatal("real metadata catalogue missing entries", page)
	}
	for _, sheet := range a.currentWorkbook().Sheets {
		if a.currentWorkbook().GetCachedIndex(sheet.Path) != nil {
			t.Fatal("metadata request prepared a value index")
		}
	}
}

func TestCatalogueHTTPRejectsNonCatalogueFormatsAndShortensOnlyListLabels(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	if response := reconcilePost(t, a, "/api/catalogue/start", catalogueStartRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Mode: "read"}); response.Code != 422 {
		t.Fatal("format support inferred outside registry", response.Code)
	}
	a, _ = catalogueServerFixture(t, 0)
	wb = a.currentWorkbook()
	job := catalogueStart(t, a, "read", "")
	catalogueWait(t, a, job)
	long := strings.Repeat("长", formats.CatalogueLabelRunes+1)
	value := workbook.Catalogue{Complete: true, Items: []workbook.CatalogueItem{{ID: "long", Kind: "name", Name: long, Status: "unresolved", Definition: `"001"`}}}
	snapshot, err := newCatalogueSnapshot(value)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	job.Snapshot = snapshot
	a.mu.Unlock()
	page := cataloguePageFor(t, a, job, url.Values{})
	if !page.Items[0].Shortened || !strings.HasSuffix(page.Items[0].Name, "…") {
		t.Fatal("long list label not bounded", page)
	}
	item := catalogueDetailFor(t, a, job, "long")
	if item.Name != long || item.Definition != `"001"` {
		t.Fatal("presentation shortening changed source definition", item)
	}
	encoded, _ := json.Marshal(page)
	if bytes.Contains(encoded, []byte(`"definition"`)) {
		t.Fatal("list included raw definitions")
	}
}
