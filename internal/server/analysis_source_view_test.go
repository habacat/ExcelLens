package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

type capturedAnalysisSource struct {
	ID          string
	Source      string
	SourceScope sourceview.Scope
	Rows        int
}

func captureAnalysisRows(t *testing.T, a *App, req analysisSourceCaptureRequest) capturedAnalysisSource {
	t.Helper()
	wb := a.currentWorkbook()
	req.WorkbookKey = wb.Fingerprint
	req.Book = sourceBookID(wb)
	if req.Source == "" {
		req.Source = "wb:First"
	}
	return decodeResponse[capturedAnalysisSource](t, reconcilePost(t, a, "/api/analysis/source/capture", req))
}
func analysisStatus(t *testing.T, a *App, id string) analysis.Query {
	t.Helper()
	return decodeResponse[struct{ Query analysis.Query }](t, analysisRequest(t, a, "GET", "/api/analysis/query/status?id="+id, "")).Query
}

func TestAnalysisSourcePreviewQueryDrillAndDetailsShareCapturedRowsAfterOwnerClear(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	before, _ := os.ReadFile(file)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	fields := decodeResponse[struct {
		Fields      []analysis.Field
		Preview     []analysis.DrillRow
		TotalRows   int
		SourceScope *sourceview.Scope
	}](t, analysisRequest(t, a, "GET", "/api/analysis/fields?source=wb:First&sourceId="+captured.ID+"&headerRow=1", ""))
	if fields.TotalRows != 3 || len(fields.Preview) != 3 || fields.Preview[0].Row != 4 || fields.Preview[1].Row != 6 || fields.Preview[2].Row != 7 || !fields.Fields[1].Numeric || fields.SourceScope == nil {
		t.Fatal("scoped preview widened or lost blank/source rows", fields)
	}
	profile := decodeResponse[struct{ Rows, Empty, Numeric int }](t, analysisRequest(t, a, "GET", "/api/analysis/profile?source=wb:First&sourceId="+captured.ID+"&headerRow=1&field=C2", ""))
	if profile.Rows != 3 || profile.Empty != 1 || profile.Numeric != 2 {
		t.Fatal("profile disagrees with preview", profile)
	}
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := 0; i < cap(a.analysisSem); i++ {
				<-a.analysisSem
			}
		})
	}
	t.Cleanup(release)
	q := analysis.Query{Source: "wb:First", HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2}
	payload := map[string]any{"source": q.Source, "headerRow": 1, "rows": q.Rows, "value": "C2", "aggregate": "sum", "precision": 2, "sourceId": captured.ID}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/analysis/query/start", payload))
	featureCall(t, a, "POST", "/api/row-view/clear", fmt.Sprintf(`{"id":%q}`, view))
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	release()
	result := awaitAnalysis(t, a, started.JobID)
	if result.Scanned != 3 || result.Included != 3 || result.Grand.Exact != "9007199254741001.01" {
		t.Fatal("queued aggregation lost its captured rows", result)
	}
	canonical := analysisStatus(t, a, started.JobID)
	if canonical.SourceScope == nil || canonical.SourceScope.Digest() != captured.SourceScope.Digest() {
		t.Fatal("completed query lost its durable scope", canonical)
	}
	drill := decodeResponse[analysis.DrillResult](t, reconcilePost(t, a, "/api/analysis/drill", map[string]any{"query": canonical, "jobId": started.JobID, "keys": []string{""}, "limit": 100}))
	var numbers []int
	for _, row := range drill.Rows {
		numbers = append(numbers, row.Row)
	}
	if !reflect.DeepEqual(numbers, []int{4, 6, 7}) {
		t.Fatal("drill widened after source owner clear", numbers)
	}
	materialized := decodeResponse[struct {
		ID   string
		Rows int
	}](t, reconcilePost(t, a, "/api/analysis/detail/materialize", map[string]any{"name": "可见明细", "query": canonical, "jobId": started.JobID, "limit": 100}))
	detail := decodeResponse[struct{ Rows [][]string }](t, analysisRequest(t, a, "GET", "/api/analysis/sheet?id="+materialized.ID, ""))
	if materialized.Rows != 3 || detail.Rows[0][0] != "4" || detail.Rows[1][0] != "6" || detail.Rows[2][0] != "7" {
		t.Fatal("materialized detail widened", detail)
	}
	restored := decodeResponse[capturedAnalysisSource](t, reconcilePost(t, a, "/api/analysis/source/restore", map[string]any{"source": "wb:First", "sourceScope": captured.SourceScope, "workbookKey": a.currentWorkbook().Fingerprint, "book": sourceBookID(a.currentWorkbook())}))
	if restored.Rows != 4 || restored.SourceScope.Digest() != captured.SourceScope.Digest() {
		t.Fatal("saved rules could not rebuild the same source", restored)
	}
	encoded, _ := json.Marshal(canonical)
	replayed := decodeResponse[struct{ JobID string }](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", string(encoded)))
	if again := awaitAnalysis(t, a, replayed.JobID); again.Grand.Exact != result.Grand.Exact || again.Scanned != result.Scanned {
		t.Fatal("saved scoped query changed", again)
	}
	settingsPath, _ := rowViewSettingsPath(a.currentWorkbook())
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatal("source capture/restore created display preferences", err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, before) {
		t.Fatal("source rows changed the original file")
	}
}

func TestAnalysisSourceFullMatchesAndVisibleFrozenSelectionRemainDifferentAndReplayable(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	spec := newFilterSpec(nil, workbook.FilterOptions{HeaderRow: 1, SortColumn: 2, Descending: true})
	order, err := wb.FilterRowOrder(context.Background(), idx, spec.Rules, spec.options(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.filterResults["sort"] = &FilterResult{ID: "sort", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: order, Spec: spec}
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "sort"}), "done")
	for _, tc := range []struct {
		req  analysisSourceCaptureRequest
		want []int
	}{
		{analysisSourceCaptureRequest{Mode: "filtered", FilterID: "sort", ViewID: view, ExcludeThroughRow: 6}, []int{4, 7, 5, 3, 2}},
		{analysisSourceCaptureRequest{Mode: "visible", FilterID: "sort", ViewID: view, ExcludeThroughRow: 6}, []int{4, 6, 7}},
		{analysisSourceCaptureRequest{Mode: "selection", FilterID: "sort", ViewID: view, ExcludeThroughRow: 6, Start: "B4", End: "B7", VisualStart: 4, VisualEnd: 1}, []int{4, 6, 7}},
		{analysisSourceCaptureRequest{Mode: "selection", FilterID: "sort", ViewID: view, ExcludeThroughRow: 6, Start: "B7", End: "B4", VisualStart: 1, VisualEnd: 4}, []int{4, 6, 7}},
		{analysisSourceCaptureRequest{Mode: "selection", FilterID: "sort", ViewID: view, ExcludeThroughRow: 1, Start: "B4", End: "B7", VisualStart: 1, VisualEnd: 2}, []int{4, 7}},
	} {
		captured := captureAnalysisRows(t, a, tc.req)
		lease, err := a.analysisSourceSnapshot(captured.ID, wb)
		if err != nil {
			t.Fatal(err)
		}
		q := analysis.Query{Source: "wb:First", HeaderRow: 1, SourceScope: &captured.SourceScope}
		a.cacheMu.RLock()
		src, err := a.sourceQuery(context.Background(), wb, q, nil, lease)
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		err = src.scan(context.Background(), []string{"C2"}, func(r analysis.Record) error { got = append(got, r.Number); return nil })
		src.close()
		a.cacheMu.RUnlock()
		lease.close()
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal("captured rows differ", tc.req.Mode, got, err)
		}
		if tc.req.Mode == "selection" {
			f := decodeResponse[struct{ Fields []analysis.Field }](t, analysisRequest(t, a, "GET", "/api/analysis/fields?source=wb:First&sourceId="+captured.ID+"&headerRow=1", ""))
			if len(f.Fields) != 1 || f.Fields[0].ID != "C2" || !f.Fields[0].Numeric {
				t.Fatal("selected source column was renumbered or misclassified", f)
			}
		}
		a.cacheMu.RLock()
		rebuilt, err := a.restoreAnalysisSource(context.Background(), wb, "wb:First", captured.SourceScope)
		a.cacheMu.RUnlock()
		if err != nil {
			t.Fatal(err)
		}
		var replayed []int
		for row := range rebuilt.rowsAfter(1) {
			replayed = append(replayed, row.Number)
		}
		rebuilt.close()
		if !reflect.DeepEqual(replayed, tc.want) {
			t.Fatal("restored source lost frozen/unmatched/selection positions", replayed)
		}
		if tc.req.Mode == "selection" && tc.req.ExcludeThroughRow == 1 {
			changed := captured.SourceScope
			filter := *changed.Filter
			filter.Descending = false
			changed.Filter = &filter
			a.cacheMu.RLock()
			wrong, err := a.restoreAnalysisSource(context.Background(), wb, "wb:First", changed)
			a.cacheMu.RUnlock()
			wrong.close()
			if err == nil {
				t.Fatal("changed display order silently reinterpreted saved source endpoints")
			}
		}
	}
}

func TestAnalysisSourceSelectionRetainsBlankAddressesAndOriginalColumnIdentities(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	for _, selection := range [][2]string{{"C4", "D7"}, {"B1000", "B1002"}} {
		captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "selection", ViewID: view, Start: selection[0], End: selection[1]})
		fields := decodeResponse[struct {
			Fields    []analysis.Field
			Preview   []analysis.DrillRow
			TotalRows int
		}](t, analysisRequest(t, a, "GET", "/api/analysis/fields?source=wb:First&sourceId="+captured.ID+"&headerRow=1", ""))
		expectedColumn := 3
		if selection[0] == "B1000" {
			expectedColumn = 2
		}
		if fields.TotalRows != 3 || len(fields.Preview) != 3 || fields.Fields[0].ID != "C"+strconv.Itoa(expectedColumn) || fields.Fields[0].Numeric {
			t.Fatal("blank selected coordinates were clamped, renumbered or typed", selection, fields)
		}
		q := analysis.Query{Source: "wb:First", HeaderRow: 1, SourceScope: &captured.SourceScope, Aggregate: "count"}
		job := completedRecipeAnalysis(t, a, q)
		if job.Result.Scanned != 3 || job.Result.Grand.Exact != "3" {
			t.Fatal("blank address selection did not preserve actual row count", selection, job.Result)
		}
	}
}

func TestAnalysisSourceAllHiddenRemainsAnEmptyDataset(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Change: &rowViewChange{Action: "hide-all"}}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	fields := decodeResponse[struct {
		TotalRows int
		Preview   []analysis.DrillRow
	}](t, analysisRequest(t, a, "GET", "/api/analysis/fields?source=wb:First&sourceId="+captured.ID+"&headerRow=1", ""))
	if fields.TotalRows != 0 || len(fields.Preview) != 0 {
		t.Fatal("empty scope fabricated records", fields)
	}
	job := completedRecipeAnalysis(t, a, analysis.Query{Source: "wb:First", HeaderRow: 1, SourceScope: &captured.SourceScope, Aggregate: "count"})
	if job.Result.Scanned != 0 || job.Result.Included != 0 || job.Result.Grand.Exact != "0" {
		t.Fatal("empty scope fell back to source records", job.Result)
	}
}

func TestAnalysisSourceConcurrentConsumersKeepIndependentLeasesAcrossClearAndAppClose(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	owner, err := a.analysisSourceSnapshot(captured.ID, wb)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		lease, err := owner.retain()
		if err != nil {
			t.Fatal(err)
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer lease.close()
			<-start
			for iteration := 0; iteration < 20; iteration++ {
				copy, err := lease.retain()
				if err != nil {
					failures <- err
					return
				}
				var numbers []int
				err = scanWorkbookAnalysisRows(context.Background(), wb, copy.data.index, copy.rowsAfter(1), []string{"C2"}, nil, func(row analysis.Record) error { numbers = append(numbers, row.Number); return nil })
				copy.close()
				if err != nil || !reflect.DeepEqual(numbers, []int{4, 6, 7}) {
					failures <- fmt.Errorf("concurrent source rows %v: %v", numbers, err)
					return
				}
			}
		}()
	}
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	owner.close()
	a.Close()
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if owner.refs() != 0 {
		t.Fatal("final concurrent source lease did not close", owner.refs())
	}
}

func TestAnalysisSourceRejectsStaleBindingsAndMalformedScopesWithoutFullSourceFallback(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	for _, change := range []func(*analysisSourceCaptureRequest){func(r *analysisSourceCaptureRequest) { r.Book = "other" }, func(r *analysisSourceCaptureRequest) { r.Source = "wb:Other" }, func(r *analysisSourceCaptureRequest) { r.ViewID = "missing" }, func(r *analysisSourceCaptureRequest) { r.Mode = "selection"; r.Start = "B2"; r.End = "B7" }} {
		req := analysisSourceCaptureRequest{Mode: "visible", ViewID: view, Source: "wb:First", WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}
		change(&req)
		data, _ := json.Marshal(req)
		response := rowViewCall(t, a, "POST", "/api/analysis/source/capture", string(data))
		if response.Code == 200 {
			t.Fatal("stale/hidden selection captured", req)
		}
	}
	for _, change := range []func(*sourceview.Scope){func(s *sourceview.Scope) { s.Version = 99 }, func(s *sourceview.Scope) { s.Mode = "unknown" }, func(s *sourceview.Scope) { s.Fingerprint = "other" }, func(s *sourceview.Scope) {
		s.Visibility.Groups = []sourceview.AxisOutlineOverride{{AxisOutlineKey: sourceview.AxisOutlineKey{Min: 44, Max: 50, Level: 1}}}
	}} {
		scope := captured.SourceScope
		settings := *scope.Visibility
		scope.Visibility = &settings
		change(&scope)
		body, _ := json.Marshal(map[string]any{"source": "wb:First", "sourceScope": scope, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
		if response := rowViewCall(t, a, "POST", "/api/analysis/source/restore", string(body)); response.Code == 200 {
			t.Fatal("bad scope silently widened", scope)
		}
	}
	featureCall(t, a, "POST", "/api/analysis/source/clear", fmt.Sprintf(`{"id":%q}`, captured.ID))
	if response := rowViewCall(t, a, "GET", "/api/analysis/fields?source=wb:First&sourceId="+captured.ID+"&headerRow=1", ""); response.Code == 200 {
		t.Fatal("missing source token fell back to full fields")
	}
	body, _ := json.Marshal(map[string]any{"source": "wb:First", "sourceId": captured.ID, "headerRow": 1, "rows": []string{"C1"}, "aggregate": "count"})
	if response := rowViewCall(t, a, "POST", "/api/analysis/query/start", string(body)); response.Code == 200 {
		t.Fatal("missing source token fell back to full query")
	}
	if response := rowViewCall(t, a, "POST", "/api/analysis/source/capture", `{"source":"wb:First","mode":"selection","unknownViewId":"missing"}`); response.Code != 400 {
		t.Fatal("unknown source request field was ignored", response.Code)
	}
}

func TestAnalysisSourceQueuedProfileCancellationDoesNotConsumeWorkOrSnapshotReferences(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(a.analysisSem); i++ {
			<-a.analysisSem
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest("GET", "/api/analysis/profile?source=wb:First&headerRow=1&field=C2", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	a.handleAnalysisProfile(response, request)
	if response.Code != 408 || len(a.analysisSources) != 0 {
		t.Fatal("cancelled queued profile consumed a source", response.Code, len(a.analysisSources))
	}
}

func TestAnalysisSourcePoolEvictionAndQueuedCancellationReleaseOnlyTheirOwnLeases(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	lease, err := a.analysisSourceSnapshot(captured.ID, wb)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	for i := 0; i < formats.AnalysisSourceSnapshots+2; i++ {
		captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	}
	if len(a.analysisSources) != formats.AnalysisSourceSnapshots {
		t.Fatal("source pool exceeded capacity", len(a.analysisSources))
	}
	if _, exists := a.analysisSources[captured.ID]; exists {
		t.Fatal("old owner was not evicted")
	}
	var got []int
	for ref := range lease.rowsAfter(1) {
		got = append(got, ref.Number)
	}
	if !reflect.DeepEqual(got, []int{4, 6, 7}) {
		t.Fatal("owner eviction invalidated an independent consumer", got)
	}
	for i := 0; i < cap(a.analysisSem); i++ {
		a.analysisSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(a.analysisSem); i++ {
			<-a.analysisSem
		}
	}()
	job, err := a.startAnalysisTask(wb, analysis.Query{Source: "wb:First", HeaderRow: 1, Rows: []string{"C1"}, Aggregate: "count"}, nil, lease)
	if err != nil {
		t.Fatal(err)
	}
	job.cancel()
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued scoped analysis did not cancel")
	}
	job.mu.RLock()
	status, rows := job.Status, job.SourceRows
	job.mu.RUnlock()
	if status != "cancelled" || rows != nil {
		t.Fatal("cancelled job retained source rows", status)
	}
	lease.close()
	if lease.refs() != 0 {
		t.Fatal("final scoped-source reference was not reclaimed", lease.refs())
	}
}

func TestAnalysisSourceHeaderCountsMatchIndependentRowsAcrossVisibilitySortingAndSparseFrozenPrefixes(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	layout, err := wb.SourceLayout(meta, idx)
	if err != nil {
		t.Fatal(err)
	}
	for sample := 0; sample < 120; sample++ {
		positions := []int{0, 1, 2, 3, 4, 5}
		shift := sample % len(positions)
		positions = append(positions[shift:], positions[:shift]...)
		if sample&1 != 0 {
			slices.Reverse(positions)
		}
		order := rowOrderFixture(t, idx.RowCount(), positions...)
		filter := &FilterResult{ID: "oracle-" + strconv.Itoa(sample), Rows: order, Sorted: true}
		settings := workbook.AxisViewSettings{Mode: "show-all", Ranges: []workbook.AxisVisibilityOverride{{Min: 1 + sample%5, Max: 1 + sample%5, Hidden: true}}}
		axis, err := layout.BuildAxisVisibility(context.Background(), "row", workbook.MaxRowsForIndex(idx), settings)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := workbook.NewRowProjection(context.Background(), wb.CacheDir, idx, order, axis, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		visible := ownSheetRowView(&sheetRowViewData{wb: wb, idx: idx, meta: meta, filterID: filter.ID, filter: filter, visibility: axis, projection: projection, sorted: true})
		scope := sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: wb.Fingerprint, Mode: "visible", Visibility: &settings, FrozenThrough: sample % 8}
		snapshot, err := ownAnalysisSource(wb, idx, "wb:First", scope, selectionSnapshot{visible: visible, filter: filter}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var all []int
		for ref := range snapshot.data.plan.Rows() {
			all = append(all, ref.Number)
		}
		for header := 0; header < 9; header++ {
			want := 0
			for _, row := range all {
				if row > header {
					want++
				}
			}
			count, err := snapshot.countAfter(context.Background(), header)
			if err != nil || count != want {
				t.Fatal("header count differs from independent full plan", sample, header, all, count, want, err)
			}
		}
		snapshot.close()
	}
}
