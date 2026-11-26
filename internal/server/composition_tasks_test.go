package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
)

func compositionStart(t *testing.T, a *App, request compositionStartRequest) *compositionTask {
	t.Helper()
	request.WorkbookKey, request.Book = a.currentWorkbook().Fingerprint, sourceBookID(a.currentWorkbook())
	response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/composition/start", request))
	a.mu.RLock()
	job := a.compositionJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("source task missing")
	}
	return job
}

func waitComposition(t *testing.T, a *App, job *compositionTask, status string) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(15 * time.Second):
		t.Fatal("composition task did not terminate", job.ID)
	}
	response := decodeResponse[struct{ Status, Error string }](t, analysisRequest(t, a, "GET", "/api/composition/status?id="+job.ID, ""))
	if response.Status != status {
		t.Fatal("unexpected source task state", response, status)
	}
}

func compositionResult(t *testing.T, a *App, job *compositionTask) compositionTaskResult {
	t.Helper()
	return decodeResponse[compositionTaskResult](t, analysisRequest(t, a, "GET", "/api/composition/result?id="+job.ID, ""))
}

func compositionUsed(a *App) int64 {
	root := a.rootApp()
	return root.compositionBudget.Used()
}

func clearComposition(t *testing.T, a *App, id string) {
	t.Helper()
	response := reconcilePost(t, a, "/api/composition/clear", map[string]string{"id": id})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestCompositionHTTPFieldsPreviewAndIndependentSourceOwnership(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	field := compositionStart(t, a, compositionStartRequest{Mode: "fields", Choice: &compositionChoice{Book: definition.Inputs[0].Ref.Book, Source: definition.Inputs[0].Ref.Source, HeaderRow: 1}})
	waitComposition(t, a, field, "done")
	schema := compositionResult(t, a, field)
	if schema.Info == nil || schema.Info.Ref.Stamp != definition.Inputs[0].Ref.Stamp || schema.Info.Rows != 2 || schema.Info.Fields[1].Name != "金额" || schema.Info.Fields[2].HeaderUnique || schema.Info.Fields[3].HeaderUnique {
		t.Fatal("field metadata guessed duplicate headers or lost source identity", schema)
	}
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition, KeepID: field.ID})
	waitComposition(t, a, job, "done")
	result := compositionResult(t, a, job)
	if result.Rows != 4 || len(result.Preview) != 4 || result.PreviewTruncated || result.Source != "append:"+definition.Digest() || result.Preview[1].Row != 4 || result.Preview[2].Input != 1 || result.Preview[2].Cells[0].SourceColumn != 2 || result.Preview[2].Cells[0].Ref != "B2" || result.Preview[2].Cells[0].Raw != "0002" {
		t.Fatal("preview changed source values/coordinates or result identity", result)
	}
	a.cacheMu.RLock()
	borrowed, err := a.borrowCompositionTask(job.ID, a.currentWorkbook())
	a.cacheMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(borrowed.close)
	clearComposition(t, a, field.ID)
	clearComposition(t, a, job.ID)
	if response := analysisRequest(t, a, "GET", "/api/composition/result?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("cleared task remained accessible", response.Code)
	}
	if len(compositionRows(t, a, borrowed, 1, 2)) != 4 || compositionUsed(a) != borrowed.data.bytes {
		t.Fatal("clearing task released a consumer's source or its budget", compositionUsed(a), borrowed.data.bytes)
	}
	borrowed.close()
	if compositionUsed(a) != 0 {
		t.Fatal("final source owner leaked retained budget", compositionUsed(a))
	}
}

func TestCompositionQueuedCancelAllowsRetryAndDoesNotHoldCacheReadLock(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	for range cap(a.querySem) {
		a.querySem <- struct{}{}
	}
	blocked := true
	releaseSlots := func() {
		if blocked {
			blocked = false
			for range cap(a.querySem) {
				<-a.querySem
			}
		}
	}
	t.Cleanup(releaseSlots)
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	cacheWriter := make(chan struct{})
	go func() { a.cacheMu.Lock(); close(cacheWriter); a.cacheMu.Unlock() }()
	select {
	case <-cacheWriter:
	case <-time.After(2 * time.Second):
		t.Fatal("queued task acquired cache lock before its query slot")
	}
	if response := reconcilePost(t, a, "/api/composition/cancel", map[string]string{"id": job.ID}); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	retry := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "cancelled")
	releaseSlots()
	waitComposition(t, a, retry, "done")
	clearComposition(t, a, retry.ID)
	if compositionUsed(a) != 0 {
		t.Fatal("cancel/retry leaked retained source bytes", compositionUsed(a))
	}
}

func TestCompositionTwoFilteredPreparationsShareQuerySlotsWithoutNestedWait(t *testing.T) {
	a, _ := rowViewFixture(t)
	definition := compositionDefinition(t, a)
	definition.Inputs[0].Ref.Scope = &sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: a.currentWorkbook().Fingerprint, Mode: "filtered", Filter: &sourceview.FilterSpec{HeaderRow: 1, SortColumn: 2, Descending: true}}
	for range cap(a.querySem) {
		a.querySem <- struct{}{}
	}
	jobs := []*compositionTask{
		compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition}),
		compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition}),
	}
	for range cap(a.querySem) {
		<-a.querySem
	}
	for _, job := range jobs {
		waitComposition(t, a, job, "done")
		result := compositionResult(t, a, job)
		if result.Rows != 7 || len(result.Preview) != 7 || result.Preview[0].Row != 4 || result.Preview[0].Cells[1].Raw != "9007199254740993.31" {
			t.Fatal("saved sorted scope was widened or its source rows replaced", result)
		}
	}
}

func TestCompositionPreviewBudgetKeepsSourcesCompleteAndStopsAtOneHundredRows(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	var body strings.Builder
	body.WriteString("编号,金额\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&body, "%04d,0.1\n", i)
	}
	book := attachReconcileCSV(t, a, "many.csv", body.String())
	other := a.resolveReconcileBook(book)
	definition.Inputs[1].Ref = compositionRef(t, a, book, "wb:"+other.Sheets[0].Name, 1)
	definition.Inputs[1].Columns = []int{1, 2}
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	result := compositionResult(t, a, job)
	if result.Rows != 152 || len(result.Preview) != formats.CompositionPreviewRows || !result.PreviewTruncated || result.Preview[99].Sequence != 100 {
		t.Fatal("bounded preview claimed a truncated source or kept too many rows", result.Rows, len(result.Preview), result.PreviewTruncated)
	}
	a.cacheMu.RLock()
	source, err := a.borrowCompositionTask(job.ID, a.currentWorkbook())
	a.cacheMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if len(compositionRows(t, a, source, 1)) != 152 {
		t.Fatal("preview limit became a data-source limit")
	}
}

func TestCompositionOversizedPreviewDoesNotTruncateAnExactSourceValue(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "large.csv")
	text := strings.Repeat("<", 400_000)
	if err := os.WriteFile(file, []byte("编号\n"+text+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := recordOpen(t, file)
	definition := compositionDefinition(t, a)
	definition.Fields = []string{"原文"}
	definition.Inputs[0].Columns, definition.Inputs[1].Columns = []int{1}, []int{2}
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	result := compositionResult(t, a, job)
	if result.Rows != 3 || len(result.Preview) != 0 || !result.PreviewTruncated {
		t.Fatal("oversized escaped row was partially published or source rejected", result.Rows, len(result.Preview))
	}
	a.cacheMu.RLock()
	source, err := a.borrowCompositionTask(job.ID, a.currentWorkbook())
	a.cacheMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if rows := compositionRows(t, a, source, 1); len(rows) != 3 || rows[0].Cells[0].Data.Raw != text {
		t.Fatal("preview byte limit changed source text")
	}
}

func TestCompositionFailedCandidateKeepsAcceptedResultAndForeignChangesRejectRead(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	accepted := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, accepted, "done")
	wrong, _ := definition.Normalized()
	wrong.Inputs[1].Ref.Stamp = "stale"
	failed := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &wrong, KeepID: accepted.ID})
	waitComposition(t, a, failed, "error")
	if compositionResult(t, a, accepted).Rows != 4 {
		t.Fatal("failed candidate erased the accepted result")
	}
	foreign := a.resolveReconcileBook(definition.Inputs[1].Ref.Book)
	if err := os.WriteFile(foreign.Path, []byte("changed input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if response := analysisRequest(t, a, "GET", "/api/composition/result?id="+accepted.ID, ""); response.Code != 409 {
		t.Fatal("changed secondary source was returned as a current result", response.Code, response.Body.String())
	}
}

func TestCompositionCrossTabBudgetAndShutdownReleaseAllTaskOwners(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	child, err := a.sessionFor(strings.Repeat("b", 24))
	if err != nil {
		t.Fatal(err)
	}
	releaseBudget, err := child.app.reserveCompositionBytes(formats.CompositionRetainedBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseBudget)
	response := reconcilePost(t, a, "/api/composition/start", compositionStartRequest{WorkbookKey: a.currentWorkbook().Fingerprint, Book: sourceBookID(a.currentWorkbook()), Mode: "append", Definition: &definition})
	if response.Code != 429 {
		t.Fatal("full shared budget did not reject queue retention", response.Code, response.Body.String())
	}
	if compositionUsed(a) != formats.CompositionRetainedBytes {
		t.Fatal("failed preparation altered another tab's reservation")
	}
	releaseBudget()
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	a.Close()
	if compositionUsed(a) != 0 {
		t.Fatal("application close leaked source/result budget", compositionUsed(a))
	}
	pins := a.rootApp().sourcePins.Len()
	if pins != 0 {
		t.Fatal("application close leaked source pins", pins)
	}
}

func TestCompositionQueuedWorkbookReplacementCancelsAndClearsOnlyOldTasks(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	for range cap(a.querySem) {
		a.querySem <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(a.querySem) {
			<-a.querySem
		}
	})
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	file := filepath.Join(t.TempDir(), "replacement.csv")
	if err := os.WriteFile(file, []byte("编号,金额\nnew,1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	default:
		t.Fatal("workbook replacement returned before cancelled task exit")
	}
	if response := analysisRequest(t, a, "GET", "/api/composition/result?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("old result remained visible in new workbook", response.Code)
	}
	if compositionUsed(a) != 0 || a.currentWorkbook().Path != file {
		t.Fatal("source switch retained old source budget or replaced new workbook")
	}
}

func TestCompositionFieldsPinRemovedAttachmentUntilTheirOwnerIsCleared(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	ref := definition.Inputs[1].Ref
	job := compositionStart(t, a, compositionStartRequest{Mode: "fields", Choice: &compositionChoice{Book: ref.Book, Source: ref.Source, HeaderRow: 1}})
	waitComposition(t, a, job, "done")
	response := reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": ref.Book})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if a.resolveReconcileBook(ref.Book) == nil || compositionResult(t, a, job).Info.Ref.Book != ref.Book {
		t.Fatal("removing the attachment invalidated a retained field source")
	}
	clearComposition(t, a, job.ID)
	if a.resolveReconcileBook(ref.Book) != nil || compositionUsed(a) != 0 {
		t.Fatal("cleared field owner retained its attachment or budget")
	}
}

func TestCompositionCompleteCancellationDropsResultEvenBeforeDoneSignal(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	// The publication/cleanup boundary can expose terminal state before the
	// worker's done signal. Cancellation must retire the published owner too.
	a.mu.Lock()
	original := job.done
	job.done = make(chan struct{})
	a.mu.Unlock()
	response := reconcilePost(t, a, "/api/composition/cancel", map[string]string{"id": job.ID})
	a.mu.Lock()
	close(job.done)
	job.done = original
	a.mu.Unlock()
	if response.Code != 200 || compositionUsed(a) != 0 {
		t.Fatal("late cancellation kept a terminal result owner", response.Code, compositionUsed(a))
	}
	if response := analysisRequest(t, a, "GET", "/api/composition/result?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("late cancelled result remained usable", response.Code)
	}
}

func TestCompositionRequestsAreBoundedStrictAndUseCentralLimits(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	limits := decodeResponse[formats.CompositionLimits](t, analysisRequest(t, a, "GET", "/api/composition/limits", ""))
	if limits != formats.Composition() {
		t.Fatal("endpoint duplicated its own source/operation capacities", limits)
	}
	for _, body := range []string{`{"mode":"append","unknown":true}`, `{"mode":"fields"}`, `{"mode":"other"}`, strings.Repeat(" ", formats.CompositionRequestBytes+1)} {
		response := analysisRequest(t, a, "POST", "/api/composition/start", body)
		if response.Code != 400 {
			t.Fatal("invalid source request accepted", response.Code)
		}
	}
	for _, route := range []string{"start", "cancel", "clear"} {
		if response := analysisRequest(t, a, "GET", "/api/composition/"+route, ""); response.Code != 405 {
			t.Fatal("mutation allowed an unsupported method", route, response.Code)
		}
	}
}

func TestCompositionFieldsAndExplicitMappingReachTheLastNativeColumn(t *testing.T) {
	a, _ := recordFixture(t, formats.ExcelColumns, "")
	definition := compositionDefinition(t, a)
	fields := compositionStart(t, a, compositionStartRequest{Mode: "fields", Choice: &compositionChoice{Book: definition.Inputs[0].Ref.Book, Source: definition.Inputs[0].Ref.Source, HeaderRow: 1}})
	waitComposition(t, a, fields, "done")
	info := compositionResult(t, a, fields).Info
	if info == nil || len(info.Fields) != formats.ExcelColumns || info.Fields[0].HeaderUnique || info.Fields[formats.ExcelColumns-1].HeaderLoaded {
		t.Fatal("wide header metadata was truncated or claimed unproved uniqueness")
	}
	definition.Fields = []string{"最后字段"}
	definition.Inputs[0].Columns, definition.Inputs[1].Columns = []int{formats.ExcelColumns}, []int{2}
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition, KeepID: fields.ID})
	waitComposition(t, a, job, "done")
	result := compositionResult(t, a, job)
	if result.Preview[0].Cells[0].Raw != "末值" || result.Preview[0].Cells[0].SourceColumn != formats.ExcelColumns || result.Preview[0].Cells[0].Ref != "XFD2" || result.Preview[1].Cells[0].Exists {
		t.Fatal("explicit last-column mapping changed source address or invented absent values", result.Preview)
	}
}
