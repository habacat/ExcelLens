package server

import (
	"context"

	"net/http"
	"strings"
	"sync"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type analysisTask struct {
	mu           sync.RWMutex
	ID           string
	Workbook     *workbook.Workbook
	Query        analysis.Query
	SourceStamp  string
	SourceFields []analysis.Field
	RecipePlan   *recipeRunPlan
	Status       string
	Stage        string
	Progress     int64
	Total        int64
	Result       analysis.Result
	Error        string
	Created      time.Time
	cancel       context.CancelFunc
	ctx          context.Context
	done         chan struct{}
	release      func()
	SourceRows   *analysisSourceSnapshot
}

func (a *App) handleAnalysisQueryStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	if key := r.Header.Get("X-ExcelLens-Workbook"); key != "" && key != wb.Fingerprint {
		writeMessage(w, 409, "service.analysisQueryJob.workbookSwitchedReselectAnalysisData")
		return
	}
	var req struct {
		analysis.Query
		SourceID string `json:"sourceId,omitempty"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeMessage(w, 400, "service.analysisQueryJob.invalidAnalysisConditions")
		return
	}
	q := req.Query
	if q.HeaderRow == 0 && !strings.HasPrefix(q.Source, "append:") {
		q.HeaderRow = 1
	}
	if len(q.Source) == 0 || len(q.Rows) > 3 || len(q.Filters) > 8 || len(q.Transforms) > 8 {
		writeMessage(w, 400, "service.analysisQueryJob.analysisConditionsExceedLimits")
		return
	}
	var captured *analysisSourceSnapshot
	if req.SourceID != "" {
		var err error
		if strings.HasPrefix(q.Source, "append:") {
			captured, err = a.borrowCompositionAnalysisSource(wb, q.Source, req.SourceID)
		} else {
			captured, err = a.analysisSourceSnapshot(req.SourceID, wb)
		}
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		defer captured.close()
	}
	job, err := a.startAnalysisTask(wb, q, nil, captured)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) startAnalysisTask(wb *workbook.Workbook, q analysis.Query, plan *recipeRunPlan, captured ...*analysisSourceSnapshot) (*analysisTask, error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if len(captured) > 1 {
		return nil, i18n.Error("service.analysisQueryJob.numberAnalysisSourceSnapshotsInvalid")
	}
	var base *analysisSourceSnapshot
	if len(captured) == 1 {
		base = captured[0]
	}
	if base != nil && base.isClosed() {
		return nil, i18n.Error("service.analysisQueryJob.analysisSourceSnapshotReleased")
	}
	var err error
	q, err = compositionAnalysisDefinition(q, base)
	if err != nil {
		return nil, err
	}
	if q.Composition != nil && plan != nil {
		return nil, i18n.Error("service.analysisQueryJob.jointSourceAnalysisSchemeNot")
	}
	if q.SourceScope != nil {
		scope, err := normalizedAnalysisScope(wb, q.Source, *q.SourceScope)
		if err != nil {
			return nil, err
		}
		q.SourceScope = &scope
	}
	var rows *analysisSourceSnapshot
	if base != nil {
		if base.isClosed() || base.data.wb != wb || base.data.source != q.Source || q.SourceScope != nil && q.SourceScope.Digest() != base.data.scope.Digest() {
			return nil, i18n.Error("service.analysisQueryJob.analysisSourceSnapshotDoesNot")
		}
		var err error
		rows, err = base.retain()
		if err != nil {
			return nil, err
		}
		q.SourceScope = base.canonicalScope()
	}
	accepted := false
	var releaseBooks func()
	if q.Composition != nil && rows == nil {
		releaseBooks, err = a.pinCompositionAnalysisBooks(q)
		if err != nil {
			rows.close()
			return nil, err
		}
	}
	defer func() {
		if !accepted {
			rows.close()
			if releaseBooks != nil {
				releaseBooks()
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	job := &analysisTask{ID: randomID(), Workbook: wb, Query: q, RecipePlan: plan, SourceRows: rows, Status: "queued", Stage: "stage.task.prepareDataSource", Created: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{})}
	a.analysisMu.Lock()
	if a.currentWorkbook() != wb {
		a.analysisMu.Unlock()
		cancel()
		return nil, i18n.Error("service.analysisQueryJob.workbookSwitchedAnalysisNotInitiated")
	}
	// Bound retained jobs; old completed results remain accessible until evicted.
	if len(a.analysisJobs) >= 12 {
		var oldest *analysisTask
		for _, j := range a.analysisJobs {
			select {
			case <-j.done:
				if oldest == nil || j.Created.Before(oldest.Created) {
					oldest = j
				}
			default:
			}
		}
		if oldest == nil {
			a.analysisMu.Unlock()
			cancel()
			return nil, i18n.Error("service.analysisQueryJob.thereAlreadyAnalysisTasksComplete")
		}
		oldest.cancel()
		oldest.closeSourceRows()
		delete(a.analysisJobs, oldest.ID)
	}
	primaryRelease := a.pinWorkbooks(wb)
	job.release = func() {
		primaryRelease()
		if releaseBooks != nil {
			releaseBooks()
		}
	}
	a.analysisJobs[job.ID] = job
	a.analysisMu.Unlock()
	accepted = true
	a.retainSubmitted(job.ID, job.done)
	go a.runAnalysisQuery(job, q)
	return job, nil
}
func (a *App) runAnalysisQuery(job *analysisTask, q analysis.Query) {
	defer close(job.done)
	defer func() {
		job.mu.Lock()
		if job.Status != "done" {
			job.SourceRows.close()
			job.SourceRows = nil
		}
		job.mu.Unlock()
	}()
	if job.release != nil {
		defer job.release()
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-job.ctx.Done():
		job.mu.Lock()
		job.Status = "cancelled"
		job.mu.Unlock()
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = "stage.task.readingFieldsCreatingIndexes"
	job.mu.Unlock()
	wb := job.Workbook
	if strings.HasPrefix(q.Source, "wb:") {
		meta, ok := wb.FindSheet(strings.TrimPrefix(q.Source, "wb:"))
		if !ok {
			a.finishAnalysis(job, analysis.Result{}, i18n.Error("service.analysisQueryJob.sourceWorksheetDoesNotExist"))
			return
		}
		if wb.GetCachedIndex(meta.Path) == nil {
			releaseIndex, admissionErr := a.backgroundIndex(job.ctx)
			if admissionErr != nil {
				a.finishAnalysis(job, analysis.Result{}, job.ctx.Err())
				return
			}
			_, err := wb.BuildIndex(meta, nil, job.ctx.Done())
			releaseIndex()
			if err != nil {
				a.finishAnalysis(job, analysis.Result{}, err)
				return
			}
		}
	}
	rebuildFilter := job.SourceRows == nil && q.SourceScope != nil && q.SourceScope.Filter != nil
	releaseSlot, err := a.acquireAnalysisQuerySlot(job.ctx, q.Source, rebuildFilter)
	if err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	defer releaseSlot()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if q.Composition != nil && job.SourceRows == nil {
		composition, err := a.captureComposition(job.ctx, *q.Composition)
		if err != nil {
			a.finishAnalysis(job, analysis.Result{}, err)
			return
		}
		job.mu.Lock()
		job.SourceRows = ownCompositionAnalysisSource(wb, composition)
		job.mu.Unlock()
	}
	var observer sourceCellObserver
	if strings.HasPrefix(q.Source, "wb:") && (job.RecipePlan != nil || q.Layout == "tree") {
		if err := wb.CheckSourceUnchanged(); err != nil {
			a.finishAnalysis(job, analysis.Result{}, err)
			return
		}
	}
	if plan := job.RecipePlan; plan != nil {
		observer = guardRecipeTypes(wb.Format, plan.Masks)
		if strings.HasPrefix(q.Source, "scratch:") {
			observer = guardRecipeTypes("scratch", plan.Masks)
		}
	}
	src, err := a.sourceQuery(job.ctx, wb, q, observer, job.SourceRows)
	if rebuildFilter {
		releaseSlot()
	}
	if err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	defer src.close()
	if src.lease != nil && job.SourceRows == nil {
		job.mu.Lock()
		job.SourceRows, _ = src.lease.retain()
		job.mu.Unlock()
	}
	if err = analysis.Validate(q, src.fields); err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	stamp := src.stamp()
	if job.RecipePlan != nil && job.RecipePlan.Stamp != stamp {
		a.finishAnalysis(job, analysis.Result{}, i18n.Error("service.analysisQueryJob.sourceAnalysisPlanObjectivesModified"))
		return
	}
	job.mu.Lock()
	job.Stage = "stage.task.aggregatingRowRowExactDecimal"
	job.Total = int64(src.total)
	job.SourceStamp = stamp
	needed := make(map[string]bool)
	for _, id := range analysis.RequiredFields(q) {
		needed[id] = true
	}
	job.SourceFields = make([]analysis.Field, 0, len(needed))
	for _, field := range src.fields {
		if needed[field.ID] {
			job.SourceFields = append(job.SourceFields, field)
		}
	}
	job.mu.Unlock()
	scan := src.scan
	if job.RecipePlan != nil {
		scan = recipeScanInputs(scan, job.RecipePlan.Masks)
	}
	result, err := analysis.Run(job.ctx, q, src.fields, scan, func(scanned int64) { job.mu.Lock(); job.Progress = scanned; job.mu.Unlock() })
	a.finishAnalysis(job, result, err)
}
func (a *App) finishAnalysis(job *analysisTask, res analysis.Result, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if err == nil && job.ctx.Err() != nil {
		err = job.ctx.Err()
	}
	if err != nil {
		if job.ctx.Err() != nil {
			job.Status = "cancelled"
		} else {
			job.Status = "error"
		}
		job.Stage = "stage.task.taskCompleted"
		job.Error = err.Error()
		return
	}
	if a.currentWorkbook() != job.Workbook {
		job.Status = "cancelled"
		return
	}
	job.Result = res
	job.Status = "done"
	job.Stage = "stage.task.completed"
	job.Progress = res.Scanned
}
func (a *App) getAnalysisJob(id string) *analysisTask {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	return a.analysisJobs[id]
}

func (a *App) clearAnalysisTasks() {
	a.analysisMu.Lock()
	wait := a.detachAnalysisTasksLocked()
	a.analysisMu.Unlock()
	wait()
}

func (a *App) detachAnalysisTasksLocked() func() {
	jobs := a.analysisJobs
	a.analysisJobs = make(map[string]*analysisTask)
	trees := a.analysisTreeJobs
	a.analysisTreeJobs = make(map[string]*analysisTreeTask)
	sources := a.analysisSources
	a.analysisSources = make(map[string]*analysisSourceSnapshot)
	for _, job := range jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	for _, job := range trees {
		job.cancel()
	}
	return func() {
		for _, source := range sources {
			source.close()
		}
		for _, job := range jobs {
			if job.done != nil {
				<-job.done
			}
			job.closeSourceRows()
		}
		for _, job := range trees {
			<-job.done
		}
	}
}
func (a *App) handleAnalysisQueryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getAnalysisJob(r.URL.Query().Get("id"))
	if job == nil || a.currentWorkbook() != job.Workbook {
		writeMessage(w, 404, "service.analysisQueryJob.analysisTaskDoesNotExist")
		return
	}
	job.mu.RLock()
	response := map[string]any{"status": job.Status, "stage": responseLocale(w).Stage(job.Stage), "progress": job.Progress, "total": job.Total, "error": job.Error}
	if job.Status == "done" {
		response["result"] = job.Result
		response["query"] = job.Query
		response["sourceStamp"] = job.SourceStamp
		response["sourceFields"] = job.SourceFields
		if job.RecipePlan != nil {
			response["recipeId"], response["exportFormat"] = job.RecipePlan.RecipeID, job.RecipePlan.ExportFormat
		}
	}
	// Published results and query fields are immutable. Copy their references
	// with the status, then keep both JSON encoding and network IO outside mu.
	job.mu.RUnlock()
	writeJSON(w, response)
}
func (a *App) handleAnalysisQueryCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeMessage(w, 400, "service.analysisQueryJob.parameterInvalid")
		return
	}
	job := a.getAnalysisJob(req.ID)
	if job == nil {
		writeMessage(w, 404, "service.analysisQueryJob.taskDoesNotExist")
		return
	}
	job.cancel()
	writeJSON(w, map[string]bool{"cancelled": true})
}
