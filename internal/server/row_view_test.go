package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func rowViewFixture(t *testing.T) (*App, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "visibility.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="s1"/><sheet name="Other" sheetId="2" r:id="s2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetPr><outlinePr summaryBelow="1"/></sheetPr><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>001</t></is></c><c r="B1"><v>1.10</v></c></row><row r="2" hidden="1" outlineLevel="1"><c r="B2"><v>2.20</v></c></row><row r="3" hidden="1" outlineLevel="2"><c r="B3"><v>3.30</v></c></row><row r="4" collapsed="1"><c r="B4"><v>9007199254740993.31</v></c></row><row r="5" ht="0"><c r="B5"><v>5.50</v></c></row><row r="7"><c r="B7"><v>7.70</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>100</v></c></row><row r="2"><c r="A2"><v>200</v></c></row><row r="3"><c r="A3"><v>300</v></c></row><row r="4"><c r="A4"><v>400</v></c></row><row r="5"><c r="A5"><v>500</v></c></row><row r="7"><c r="A7"><v>700</v></c></row></sheetData></worksheet>`,
	})
	a := prepareFeatureApp(t, file)
	t.Cleanup(a.Close)
	return a, file
}

func rowViewStart(t *testing.T, a *App, req rowViewRequest) *rowViewTask {
	t.Helper()
	if req.Sheet == "" {
		req.Sheet = "First"
	}
	if req.WorkbookKey == "" || req.Book == "" {
		status := decodeResponse[struct{ WorkbookKey, Book string }](t, featureCall(t, a, "GET", "/api/status", ""))
		if status.WorkbookKey == "" || status.Book == "" {
			t.Fatal("public status did not expose source binding for row-view bootstrap")
		}
		if req.WorkbookKey == "" {
			req.WorkbookKey = status.WorkbookKey
		}
		if req.Book == "" {
			req.Book = status.Book
		}
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeResponse[struct{ JobID string }](t, featureCall(t, a, "POST", "/api/row-view/start", string(encoded)))
	a.mu.RLock()
	job := a.rowViewJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("missing asynchronous row-view task")
	}
	return job
}

func waitRowView(t *testing.T, a *App, job *rowViewTask, status string) string {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("row-view task did not finish")
	}
	response := decodeResponse[struct{ Status, Error, ViewID string }](t, featureCall(t, a, "GET", "/api/row-view/status?id="+job.ID, ""))
	if response.Status != status {
		t.Fatalf("row-view status %s, want %s: %s", response.Status, status, response.Error)
	}
	return response.ViewID
}

func rowViewCall(t *testing.T, a *App, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-ExcelLens-Token", a.token)
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	return out
}

func TestRowViewSourceWindowClipboardStatisticsAndOutlineKeepSourceCoordinates(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	before, _ := os.ReadFile(file)
	job := rowViewStart(t, a, rowViewRequest{})
	id := waitRowView(t, a, job, "done")
	view := decodeResponse[struct {
		ID           string
		TotalRows    int
		Revision     int64
		HiddenRanges []workbook.AxisInterval
		GroupCount   int
	}](t, featureCall(t, a, "GET", "/api/row-view?id="+id, ""))
	if view.ID != id || view.TotalRows != 4 || view.Revision != 0 || view.GroupCount != 2 || !reflect.DeepEqual(view.HiddenRanges, []workbook.AxisInterval{{Min: 2, Max: 3}, {Min: 5, Max: 5}}) {
		t.Fatalf("source visibility: %+v", view)
	}
	page := decodeResponse[struct {
		TotalRows int
		Rows      []workbook.RowData
		ViewID    string
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&viewId="+id+"&rowWindow=1&row=1&limit=10&col=1&cols=2", ""))
	numbers := make([]int, len(page.Rows))
	for i, row := range page.Rows {
		numbers[i] = row.Number
	}
	if page.TotalRows != 4 || page.ViewID != id || !reflect.DeepEqual(numbers, []int{1, 4, 6, 7}) {
		t.Fatalf("source window lost blank/hidden coordinates: %+v", page)
	}
	if page.Rows[0].Cells[0].Raw != "001" || page.Rows[0].Cells[0].Type != "inlineStr" || page.Rows[1].Cells[0].Raw != "9007199254740993.31" {
		t.Fatal("source values/types changed", page.Rows)
	}
	clip := decodeResponse[clipboardPayload](t, featureCall(t, a, "GET", "/api/range?sheet=First&viewId="+id+"&start=B1&end=B7&format=json", ""))
	numbers = nil
	for _, row := range clip.Rows {
		numbers = append(numbers, row.Row)
	}
	if clip.Filtered || !reflect.DeepEqual(numbers, []int{1, 4, 6, 7}) {
		t.Fatal("clipboard and viewport disagree", clip)
	}
	stats := decodeResponse[selectionStatResult](t, featureCall(t, a, "POST", "/api/selection/stats", `{"sheet":"First","viewId":"`+id+`","ranges":[{"start":"B1","end":"B7"}]}`))
	if stats.Total != 4 || stats.Number != 3 || stats.Sum != "9007199254741002.11" || stats.Filtered {
		t.Fatalf("visible statistics: %+v", stats)
	}
	profile := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","viewId":"`+id+`","start":"B1","end":"B7"}`))
	if profile.Sum != stats.Sum || profile.Total != stats.Total || profile.Number != stats.Number {
		t.Fatal("captured profile and quick statistics disagree", profile, stats)
	}
	manual := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","start":"B2","end":"B5"}`))
	if manual.Total != 4 || manual.Sum != "9007199254741004.31" {
		t.Fatal("typed source range silently excluded hidden rows", manual)
	}
	calculation := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", `{"kind":"sum","workbookKey":"`+a.currentWorkbook().Fingerprint+`","sheet":"First","viewId":"`+id+`","ranges":[{"start":"B1","end":"B7"}]}`))
	if calculation.Raw != stats.Sum || calculation.Total != stats.Total || calculation.Numeric != stats.Number {
		t.Fatal("visible calculation and statistics disagree", calculation, stats)
	}
	// Preserve the calculator's 4M single-area capacity rather than silently
	// falling back to the 400k multi-area/instant-statistics planner.
	large := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", `{"kind":"sum","workbookKey":"`+a.currentWorkbook().Fingerprint+`","sheet":"First","viewId":"`+id+`","ranges":[{"start":"A1","end":"IP2001"}]}`))
	if large.Total != 499500 || large.Numeric != 3 || large.Raw != stats.Sum {
		t.Fatal("visible single-area calculator capacity/type semantics changed", large)
	}
	groups := decodeResponse[struct {
		Total  int
		Groups []workbook.AxisOutlineGroup
	}](t, featureCall(t, a, "GET", "/api/row-view/groups?id="+id+"&offset=1&limit=1", ""))
	if groups.Total != 2 || len(groups.Groups) != 1 || groups.Groups[0].Min != 3 || groups.Groups[0].Parent != 1 {
		t.Fatal("outline pagination dropped nesting", groups)
	}
	locate := decodeResponse[struct {
		Found, Hidden bool
		VisualIndex   int
	}](t, featureCall(t, a, "GET", "/api/row-view/locate?sheet=First&viewId="+id+"&row=5", ""))
	if locate.Found || !locate.Hidden || locate.VisualIndex != 3 {
		t.Fatal("hidden row was silently located as visible", locate)
	}
	invalid := rowViewCall(t, a, "GET", "/api/range?sheet=First&viewId="+id+"&start=B2&end=B7", "")
	if invalid.Code != 400 {
		t.Fatal("hidden endpoint accepted", invalid.Code, invalid.Body.String())
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("visibility changed source file bytes")
	}
}

func TestRowViewSortedFrozenSelectionSurvivesFilterClearWithoutChangingMatches(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["order"] = &FilterResult{ID: "order", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "order"}), "done")
	payload := decodeResponse[struct{ MatchedPinnedRows []int }](t, featureCall(t, a, "GET", "/api/row-view?id="+id, ""))
	if !reflect.DeepEqual(payload.MatchedPinnedRows, []int{1, 4, 7}) {
		t.Fatal("visible frozen-match counts include hidden or unindexed source rows", payload)
	}
	if a.filterResults["order"].Rows.Len() != 6 {
		t.Fatal("visibility modified the filter match set")
	}
	reused := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "order", BaseID: id}), "done")
	if reused == id || a.rowViews[reused].data.visibility != a.rowViews[id].data.visibility {
		t.Fatal("unchanged source settings rebuilt or mutated the immutable visibility model")
	}
	featureCall(t, a, "POST", "/api/filter/clear", `{"id":"order"}`)
	// Group/temporary-show operations still use the captured full match set
	// after its original filter map owner has been cleared or evicted.
	shown := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "order", BaseID: id, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: false}}), "done")
	if a.rowViews[shown].count() != 4 || a.rowViews[id].count() != 3 {
		t.Fatal("temporary show changed the old filter/view snapshot")
	}
	page := decodeResponse[struct {
		TotalRows int
		Filtered  bool
		Rows      []workbook.RowData
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&filterId=order&viewId="+id+"&offset=0&limit=10&excludeThroughRow=1", ""))
	if !page.Filtered || page.TotalRows != 2 || len(page.Rows) != 2 || page.Rows[0].Number != 7 || page.Rows[1].Number != 4 {
		t.Fatalf("sorted visible body: %+v", page)
	}
	pinned := decodeResponse[struct {
		TotalRows, PinnedThroughRow int
		Rows                        []workbook.RowData
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&filterId=order&viewId="+id+"&pinnedThroughRow=6&row=1&limit=30", ""))
	if pinned.TotalRows != 3 || pinned.PinnedThroughRow != 6 || len(pinned.Rows) != 3 || pinned.Rows[0].Number != 1 || pinned.Rows[1].Number != 4 || pinned.Rows[2].Number != 6 {
		t.Fatal("frozen source prefix leaked hidden rows or lost unmatched blank headers", pinned)
	}
	clip := decodeResponse[clipboardPayload](t, featureCall(t, a, "GET", "/api/range?sheet=First&filterId=order&viewId="+id+"&start=B1&end=B4&visualStart=1&visualEnd=2&excludeThroughRow=1&format=json", ""))
	numbers := []int{}
	for _, row := range clip.Rows {
		numbers = append(numbers, row.Row)
	}
	if !clip.Filtered || !reflect.DeepEqual(numbers, []int{1, 7, 4}) {
		t.Fatal("frozen header and sorted body disagree", clip)
	}
	stats := decodeResponse[selectionStatResult](t, featureCall(t, a, "POST", "/api/selection/stats", `{"sheet":"First","filterId":"order","viewId":"`+id+`","excludeThroughRow":1,"ranges":[{"start":"B7","end":"B4","visualStart":1,"visualEnd":2}]}`))
	if stats.Total != 2 || !stats.Filtered || stats.Sum != "9007199254741001.01" {
		t.Fatalf("sorted statistics: %+v", stats)
	}
	profile := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","filterId":"order","viewId":"`+id+`","start":"B7","end":"B4","visualStart":1,"visualEnd":2,"excludeThroughRow":1}`))
	if profile.Total != stats.Total || profile.Sum != stats.Sum {
		t.Fatal("sorted captured profile lost view snapshot", profile, stats)
	}
	calculation := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", `{"kind":"sum","workbookKey":"`+wb.Fingerprint+`","sheet":"First","filterId":"order","viewId":"`+id+`","excludeThroughRow":1,"ranges":[{"start":"B7","end":"B4","visualStart":1,"visualEnd":2}]}`))
	if calculation.Total != stats.Total || calculation.Raw != stats.Sum {
		t.Fatal("sorted calculator lost view snapshot", calculation, stats)
	}
	inspection := decodeResponse[struct {
		Matched int
		Matches []workbook.SearchResult
	}](t, featureCall(t, a, "POST", "/api/inspect", `{"sheet":"First","filterId":"order","viewId":"`+id+`","start":"B1","end":"B4","visualStart":1,"visualEnd":2,"excludeThroughRow":1,"mode":"constants"}`))
	if inspection.Matched != 3 {
		t.Fatal("inspection read hidden or unselected rows", inspection)
	}
	locate := decodeResponse[struct {
		Found       bool
		VisualIndex int
	}](t, featureCall(t, a, "GET", "/api/row-view/locate?sheet=First&filterId=order&viewId="+id+"&row=4&excludeThroughRow=1", ""))
	if !locate.Found || locate.VisualIndex != 2 {
		t.Fatal("locate disagrees with sorted frozen body", locate)
	}
	wrong := rowViewCall(t, a, "GET", "/api/range?sheet=First&filterId=order&viewId="+id+"&start=B4&end=B7&visualStart=1&visualEnd=2&excludeThroughRow=1", "")
	if wrong.Code != 400 {
		t.Fatal("mismatched source/visual endpoints accepted", wrong.Code, wrong.Body.String())
	}
	wrong = rowViewCall(t, a, "GET", "/api/sheet?sheet=First&viewId="+id, "")
	if wrong.Code != 409 {
		t.Fatal("filter-bound view accepted as an unfiltered view", wrong.Code)
	}
	lease, err := a.rowViewSnapshot(id, wb, idx, meta.Path, "order")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	if lease.data.filter.Rows.Len() != 6 {
		t.Fatal("full match set not retained by snapshot")
	}
	manual, err := analysisSelectionRows(idx, lease.data.filter, 2, 5, analysisSelectionCoordinates{}, 10, lease)
	if err != nil || !reflect.DeepEqual(manual, []int{2, 5, 4, 3}) {
		t.Fatal("manual source range was reinterpreted as visible selection", manual, err)
	}
	captured, err := analysisSelectionRows(idx, nil, 7, 4, analysisSelectionCoordinates{ViewID: id, VisualStart: 1, VisualEnd: 2, ExcludeThroughRow: 1}, 10, lease)
	if err != nil || !reflect.DeepEqual(captured, []int{7, 4}) {
		t.Fatal("captured analysis selection lost visible ordering", captured, err)
	}
}

func TestRowViewRejectsOtherSheetIndexAndSourceAndPinsRetainedSnapshots(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	wb := a.currentWorkbook()
	first, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(first.Path)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	other, _ := wb.FindSheet("Other")
	otherIdx, err := wb.BuildIndex(other, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.RowCount() != otherIdx.RowCount() {
		t.Fatal("test requires equal row counts")
	}
	if v, err := a.rowViewSnapshot(id, wb, otherIdx, other.Path, ""); err == nil {
		v.close()
		t.Fatal("equal-sized other sheet accepted")
	}
	copyIndex := *idx
	if v, err := a.rowViewSnapshot(id, wb, &copyIndex, first.Path, ""); err == nil {
		v.close()
		t.Fatal("different index with equal counts accepted")
	}
	lease, err := a.rowViewSnapshot(id, wb, idx, first.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	if lease.count() != 4 {
		t.Fatal("clearing owner destroyed retained snapshot")
	}
	copyPath := filepath.Join(t.TempDir(), "copy.xlsx")
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(copyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	oldBook := sourceBookID(wb)
	if err := a.OpenPath(copyPath); err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(rowViewRequest{WorkbookKey: wb.Fingerprint, Book: oldBook, Sheet: "First"})
	if response := rowViewCall(t, a, "POST", "/api/row-view/start", string(stale)); response.Code != 409 {
		t.Fatal("same-content source path switch accepted stale operation", response.Code)
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 1 {
		t.Fatal("retained snapshot did not pin original source", pins)
	}
	v, err := newSelectionRowView(idx, nil, 0, lease)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := v.numbersPage(0, 10)
	if err != nil || !reflect.DeepEqual(rows, []int{1, 4, 6, 7}) {
		t.Fatal("old snapshot stopped after source replacement", rows, err)
	}
	lease.close()
	a.sourceMu.Lock()
	pins = a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("last snapshot reference leaked its source pin", pins)
	}
}

func TestRowViewCancellationAndSupersessionDoNotPublishOrPersistPartialSettings(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	oldID := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	defer func() {
		for len(a.querySem) > 0 {
			<-a.querySem
		}
	}()
	zero := int64(0)
	one := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "hide-all"}})
	two := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "show-all"}})
	waitRowView(t, a, one, "cancelled")
	featureCall(t, a, "POST", "/api/row-view/cancel", `{"id":"`+two.ID+`"}`)
	waitRowView(t, a, two, "cancelled")
	path, _ := rowViewSettingsPath(a.currentWorkbook())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled operation wrote view preferences", err)
	}
	if response := rowViewCall(t, a, "GET", "/api/row-view?id="+oldID, ""); response.Code != 200 {
		t.Fatal("cancelled build removed active view")
	}
	for i := 0; i < cap(a.querySem); i++ {
		<-a.querySem
	}
	three := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "hide-all"}})
	id := waitRowView(t, a, three, "done")
	view := decodeResponse[struct {
		TotalRows int
		Revision  int64
	}](t, featureCall(t, a, "GET", "/api/row-view?id="+id, ""))
	if view.TotalRows != 0 || view.Revision != 1 {
		t.Fatal("completed empty result not published", view)
	}
	emptyPage := decodeResponse[struct {
		TotalRows int
		Rows      []workbook.RowData
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&viewId="+id+"&offset=0&limit=10", ""))
	if emptyPage.TotalRows != 0 || len(emptyPage.Rows) != 0 {
		t.Fatal("empty visible result leaked source rows", emptyPage)
	}
	if response := decodeResponse[struct{ Cancelled bool }](t, featureCall(t, a, "POST", "/api/row-view/cancel", `{"id":"`+id+`"}`)); response.Cancelled {
		t.Fatal("cancel claimed to undo an already committed preference")
	}
	// A stale completed computation is rejected independently of ctx cancellation.
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	base, err := a.rowViewSnapshot(oldID, wb, idx, meta.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer base.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := &rowViewTask{ID: "late", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if err := a.publishRowView(base, late, true, &zero, path, durableConfigOps); err == nil {
		t.Fatal("late result published after another operation")
	}
	store, err := readRowViewSettings(path, wb)
	if err != nil || store.Revision != 1 {
		t.Fatal("late operation changed confirmed preferences", store, err)
	}
}

func TestRowViewInvalidGroupAndCrossTabRevisionConflictKeepConfirmedViews(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	old := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	zero := int64(0)
	invalid := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "group", Group: &workbook.AxisOutlineOverride{AxisOutlineKey: workbook.AxisOutlineKey{Min: 999, Max: 1000, Level: 1}, Collapsed: true}}})
	waitRowView(t, a, invalid, "error")
	if response := rowViewCall(t, a, "GET", "/api/row-view?id="+old, ""); response.Code != 200 {
		t.Fatal("failed group change removed confirmed view")
	}
	b := prepareFeatureApp(t, file)
	defer b.Close()
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
		b.querySem <- struct{}{}
	}
	one := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "hide-all"}})
	two := rowViewStart(t, b, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "show-all"}})
	for i := 0; i < cap(a.querySem); i++ {
		<-a.querySem
		<-b.querySem
	}
	for _, job := range []*rowViewTask{one, two} {
		select {
		case <-job.done:
		case <-time.After(10 * time.Second):
			t.Fatal("conflict jobs stalled")
		}
	}
	statuses := []string{one.Status, two.Status}
	if !(reflect.DeepEqual(statuses, []string{"done", "error"}) || reflect.DeepEqual(statuses, []string{"error", "done"})) {
		t.Fatal("cross-tab CAS did not have one winner", statuses)
	}
	path, _ := rowViewSettingsPath(a.currentWorkbook())
	store, err := readRowViewSettings(path, a.currentWorkbook())
	if err != nil || store.Revision != 1 {
		t.Fatal("cross-tab save overwrote confirmed revision", store, err)
	}
	// Reopening loads saved settings; transient display changes never save.
	reloaded := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	payload := decodeResponse[struct {
		Revision int64
		Settings workbook.AxisViewSettings
	}](t, featureCall(t, a, "GET", "/api/row-view?id="+reloaded, ""))
	if payload.Revision != 1 || payload.Settings.Mode != store.Sheets[a.currentWorkbook().Sheets[0].Path].Mode {
		t.Fatal("reopen did not use saved row settings", payload)
	}
	rawBefore, _ := os.ReadFile(path)
	temporary := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{BaseID: reloaded, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: false}}), "done")
	if temporary == reloaded {
		t.Fatal("transient override reused mutable view identity")
	}
	if rawAfter, _ := os.ReadFile(path); !bytes.Equal(rawBefore, rawAfter) {
		t.Fatal("temporary show persisted over saved preferences")
	}
	if response := rowViewCall(t, a, "GET", "/api/sheet?sheet=Other&viewId="+temporary, ""); response.Code != 409 {
		t.Fatal("row view read another sheet", response.Code)
	}
}

func TestRowViewSavedSettingsFaultsPreserveOldFileAndView(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	zero := int64(0)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "show-all"}}), "done")
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	path, _ := rowViewSettingsPath(wb)
	original, _ := os.ReadFile(path)
	for _, stage := range []string{"sync", "rename"} {
		t.Run(stage, func(t *testing.T) {
			settings := workbook.AxisViewSettings{Mode: "hide-all"}
			layout, err := wb.SourceLayout(meta, idx)
			if err != nil {
				t.Fatal(err)
			}
			axis, err := layout.BuildAxisVisibility(context.Background(), "row", workbook.MaxRowsForIndex(idx), settings)
			if err != nil {
				t.Fatal(err)
			}
			candidate := ownSheetRowView(&sheetRowViewData{id: "fault-" + stage, wb: wb, idx: idx, meta: meta, visibility: axis, settings: settings})
			defer candidate.close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			job := &rowViewTask{ID: candidate.data.id, ctx: ctx, cancel: cancel}
			a.mu.Lock()
			a.rowViewLatest[meta.Path] = job.ID
			a.mu.Unlock()
			ops := durableConfigOps
			if stage == "sync" {
				ops.sync = func(*os.File) error { return fmt.Errorf("injected sync failure") }
			} else {
				ops.rename = func(string, string) error { return fmt.Errorf("injected publish failure") }
			}
			one := int64(1)
			if err := a.publishRowView(candidate, job, true, &one, path, ops); err == nil {
				t.Fatal("injected failure accepted")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(original, after) {
				t.Fatal("failed preference write destroyed old settings")
			}
			confirmed := decodeResponse[struct {
				TotalRows int
				Revision  int64
			}](t, featureCall(t, a, "GET", "/api/row-view?id="+id, ""))
			if confirmed.TotalRows != 7 || confirmed.Revision != 1 {
				t.Fatal("failed save changed confirmed view", confirmed)
			}
			if _, exists := a.rowViews[candidate.data.id]; exists {
				t.Fatal("failed preference write published partial view")
			}
		})
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(rowViewRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: "First"})
	if response := rowViewCall(t, a, "POST", "/api/row-view/start", string(encoded)); response.Code != 422 {
		t.Fatal("corrupt settings silently replaced by defaults", response.Code)
	}
	if after, _ := os.ReadFile(path); string(after) != "broken" {
		t.Fatal("corrupt source evidence was overwritten")
	}
	// A fresh page has no captured base. Its explicit recovery choice must
	// still open the data without repairing or discarding the damaged file.
	recovered := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Settings: &workbook.AxisViewSettings{Mode: "show-all"}}), "done")
	recovery := decodeResponse[struct {
		TotalRows       int
		Revision        int64
		SettingsWarning string
		Notice          string
	}](t, featureCall(t, a, "GET", "/api/row-view?id="+recovered, ""))
	if recovery.TotalRows != 7 || recovery.Revision != 0 || recovery.SettingsWarning == "" || !strings.Contains(recovery.Notice, recovery.SettingsWarning) {
		t.Fatal("explicit recovery hid its unsaved preference state", recovery)
	}
	derived := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{BaseID: recovered, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: true}}), "done")
	derivedPayload := decodeResponse[struct {
		TotalRows       int
		SettingsWarning string
	}](t, featureCall(t, a, "GET", "/api/row-view?id="+derived, ""))
	if derivedPayload.TotalRows != 6 || derivedPayload.SettingsWarning != recovery.SettingsWarning {
		t.Fatal("transient recovery lost the preference warning", derivedPayload)
	}
	encoded, _ = json.Marshal(rowViewRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: "First", BaseID: recovered, Persist: true, ExpectedRevision: &zero, Settings: &workbook.AxisViewSettings{Mode: "show-all"}})
	if response := rowViewCall(t, a, "POST", "/api/row-view/start", string(encoded)); response.Code != 422 {
		t.Fatal("explicit recovery silently repaired damaged saved preferences", response.Code)
	}
	// An already captured view remains usable for transient operations even
	// when an unrelated later preference-file write is damaged.
	waitRowView(t, a, rowViewStart(t, a, rowViewRequest{BaseID: id, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: false}}), "done")
	if after, _ := os.ReadFile(path); string(after) != "broken" {
		t.Fatal("transient view repaired/overwrote a damaged preference file")
	}
}

func TestRowViewIndependentConsumersSurviveClearAndAppClose(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["parallel"] = &FilterResult{ID: "parallel", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "parallel"}), "done")
	consumers := make([]*sheetRowView, 12)
	for i := range consumers {
		var err error
		consumers[i], err = a.rowViewSnapshot(id, wb, idx, meta.Path, "parallel")
		if err != nil {
			t.Fatal(err)
		}
	}
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	a.Close()
	var group sync.WaitGroup
	for _, consumer := range consumers {
		group.Go(func() {
			defer consumer.close()
			view, err := newSelectionRowView(idx, nil, 1, consumer)
			if err != nil {
				t.Error(err)
				return
			}
			for i := 0; i < 100; i++ {
				rows, err := view.numbersPage(0, 10)
				if err != nil || !reflect.DeepEqual(rows, []int{7, 4}) {
					t.Error("cleared/closed owner released another consumer's projection", rows, err)
					return
				}
				if consumer.data.filter.Rows.Len() != 6 {
					t.Error("full match snapshot changed")
					return
				}
			}
		})
	}
	group.Wait()
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("parallel final release leaked source pin", pins)
	}
}

func TestRowViewEvictionQueuedReplacementAndResetPreserveOwnership(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	lease, err := a.rowViewSnapshot(id, wb, idx, meta.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	for i := 0; i < 10; i++ {
		waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	}
	if len(a.rowViews) > 4 || len(a.rowViewJobs) > 8 {
		t.Fatal("view/task history grew beyond bounds", len(a.rowViews), len(a.rowViewJobs))
	}
	if _, ok := a.rowViews[id]; ok || lease.count() != 4 {
		t.Fatal("eviction failed or destroyed retained snapshot")
	}
	zero := int64(0)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "hide-all"}}), "done")
	one := int64(1)
	waitRowView(t, a, rowViewStart(t, a, rowViewRequest{BaseID: view, Persist: true, ExpectedRevision: &one, Change: &rowViewChange{Action: "original"}}), "done")
	path, _ := rowViewSettingsPath(wb)
	store, err := readRowViewSettings(path, wb)
	if err != nil || store.Revision != 2 || len(store.Sheets) != 0 {
		t.Fatal("reset did not release saved setting capacity", store, err)
	}
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	queued := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &store.Revision, Change: &rowViewChange{Action: "hide-all"}})
	// A workbook replacement must wake cancelled semaphore waiters, without
	// waiting for another query to relinquish its slot.
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queued.done:
	case <-time.After(time.Second):
		t.Fatal("replacement left a queued visibility task live")
	}
	for i := 0; i < cap(a.querySem); i++ {
		<-a.querySem
	}
	if queued.Status != "cancelled" || len(a.rowViews) != 0 || len(a.rowViewJobs) != 0 {
		t.Fatal("replacement retained old tasks/views", queued.Status)
	}
	after, err := readRowViewSettings(path, wb)
	if err != nil || after.Revision != 2 {
		t.Fatal("queued task saved after workbook replacement", after, err)
	}
}

func TestRowViewSourceChangeWhileQueuedRejectsResultWithoutSaving(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	zero := int64(0)
	job := rowViewStart(t, a, rowViewRequest{Persist: true, ExpectedRevision: &zero, Change: &rowViewChange{Action: "show-all"}})
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	parts := make(map[string]string)
	for _, entry := range zr.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(r)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal(readErr, closeErr)
		}
		parts[entry.Name] = string(data)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], "2.20", "9.90", 1)
	writeZipArchive(t, file, parts)
	for i := 0; i < cap(a.querySem); i++ {
		<-a.querySem
	}
	waitRowView(t, a, job, "error")
	path, _ := rowViewSettingsPath(a.currentWorkbook())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("source-change failure saved preferences", err)
	}
	if response := rowViewCall(t, a, "GET", "/api/row-view?id="+id, ""); response.Code != 200 {
		t.Fatal("source change destroyed previous immutable view")
	}
}

func TestRowViewAdmissionCountsQueuedTasksAndRejectsInvalidFrozenBoundaries(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	other, _ := wb.FindSheet("Other")
	if _, err := wb.BuildIndex(other, nil, nil); err != nil {
		t.Fatal(err)
	}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	for _, endpoint := range []string{"/api/sheet?sheet=First", "/api/row-view/locate?sheet=First&row=4"} {
		response := rowViewCall(t, a, "GET", endpoint+"&viewId="+id+"&excludeThroughRow=-1", "")
		if response.Code != 400 {
			t.Fatal("invalid frozen boundary silently changed view", endpoint, response.Code)
		}
	}
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	defer func() {
		for len(a.querySem) > 0 {
			<-a.querySem
		}
	}()
	one := rowViewStart(t, a, rowViewRequest{Sheet: "First"})
	two := rowViewStart(t, a, rowViewRequest{Sheet: "Other"})
	encoded, _ := json.Marshal(rowViewRequest{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: "First"})
	if response := rowViewCall(t, a, "POST", "/api/row-view/start", string(encoded)); response.Code != 429 {
		t.Fatal("queued tasks did not participate in admission budget", response.Code, response.Body.String())
	}
	for _, job := range []*rowViewTask{one, two} {
		featureCall(t, a, "POST", "/api/row-view/cancel", `{"id":"`+job.ID+`"}`)
		waitRowView(t, a, job, "cancelled")
	}
}
