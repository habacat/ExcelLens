package server

import (
	"context"
	"encoding/json"
	"errors"

	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/i18n"
	"excellens/internal/recipe"
	"excellens/internal/workbook"
)

const maxRecipeCaptures = 4

type recipeCaptureTask struct {
	mu                   sync.RWMutex
	ID                   string
	Workbook             *workbook.Workbook
	Query                analysis.Query
	SourceStamp          string
	Format               string
	Status, Stage, Error string
	Progress, Total      int64
	Draft                recipe.Recipe
	Created              time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	release              func()
	SourceRows           *analysisSourceSnapshot
}

func (a *App) getRecipeCapture(id string) *recipeCaptureTask {
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	return a.recipeCaptures[id]
}

func (a *App) handleRecipeCaptureStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID       string `json:"jobId"`
		Book        string `json:"book"`
		WorkbookKey string `json:"workbookKey"`
		Format      string `json:"format"`
	}
	if decodeAnalysis(r, &req) != nil || (req.Format != "csv" && req.Format != "xlsx") {
		writeMessage(w, 400, "service.recipesCapture.analysisPlanCaptureParametersInvalid")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb := a.currentWorkbook()
	if wb == nil || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint {
		writeMessage(w, 409, "service.recipesCapture.workbookSwitchedSelectAnalysisResult")
		return
	}
	original := a.getAnalysisJob(req.JobID)
	if original == nil {
		writeMessage(w, 409, "service.recipesCapture.analysisResultsHaveExpiredRun")
		return
	}
	original.mu.RLock()
	if original.Status != "done" || original.Workbook != wb || original.SourceStamp == "" {
		original.mu.RUnlock()
		writeMessage(w, 409, "service.recipesCapture.analysisResultIncompleteOrSource")
		return
	}
	if original.Query.Composition != nil {
		original.mu.RUnlock()
		writeMessage(w, 409, "service.recipesCapture.saveReportJointSourcesAnalysis")
		return
	}
	encoded, _ := json.Marshal(original.Query)
	stamp := original.SourceStamp
	var captured *analysisSourceSnapshot
	if original.Query.SourceScope != nil {
		var err error
		captured, err = original.SourceRows.retain()
		if err != nil {
			original.mu.RUnlock()
			writeFailure(w, 409, err)
			return
		}
	}
	original.mu.RUnlock()
	defer captured.close()
	var query analysis.Query
	if json.Unmarshal(encoded, &query) != nil {
		writeMessage(w, 400, "service.recipesCapture.analysisConfigurationCannotCapture")
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || a.maintenance {
		writeMessage(w, 409, "service.recipesCapture.workbookSwitchedOrUnderMaintenance")
		return
	}
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	for _, old := range a.recipeCaptures {
		select {
		case <-old.done:
		default:
			writeMessage(w, 429, "service.recipesCapture.thereAlreadyAnalysisPlanCapture")
			return
		}
	}
	for len(a.recipeCaptures) >= maxRecipeCaptures {
		var oldest *recipeCaptureTask
		for _, old := range a.recipeCaptures {
			if oldest == nil || old.Created.Before(oldest.Created) {
				oldest = old
			}
		}
		delete(a.recipeCaptures, oldest.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &recipeCaptureTask{ID: randomID(), Workbook: wb, Query: query, SourceStamp: stamp, Format: req.Format, Status: "queued", Stage: "stage.task.waitingForFieldTypeCapture", Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), release: a.pinWorkbooks(wb)}
	if captured != nil {
		job.SourceRows, _ = captured.retain()
	}
	a.recipeCaptures[job.ID] = job
	go a.runRecipeCapture(job)
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (job *recipeCaptureTask) fail(err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status, job.Stage, job.Error = "error", "stage.task.analysisPlanCaptureFailed", err.Error()
	if errors.Is(err, context.Canceled) {
		job.Status, job.Stage, job.Error = "cancelled", "stage.task.analysisPlanCaptureCancelled", ""
	}
}

func (a *App) runRecipeCapture(job *recipeCaptureTask) {
	defer close(job.done)
	defer job.release()
	defer job.SourceRows.close()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		job.fail(job.ctx.Err())
		return
	}
	defer releaseScan()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb, q := job.Workbook, job.Query
	job.mu.Lock()
	job.Status, job.Stage = "running", "stage.task.sourceThatAnalyzedVerified"
	job.mu.Unlock()
	ref := reconcileSourceRef{Book: sourceBookID(wb), Source: q.Source, HeaderRow: q.HeaderRow}
	if strings.HasPrefix(q.Source, "scratch:") {
		ref.Stamp = job.SourceStamp
	}
	meta, err := a.buildReconcileSource(job.ctx, wb, ref, nil)
	if err != nil {
		job.fail(err)
		return
	}
	needed := analysis.RequiredFields(q)
	if len(needed) > analysis.MaxSourceFields {
		job.fail(i18n.Error("service.recipesCapture.analysisPlanExceedsCurrentAnalysis"))
		return
	}
	fields := make([]recipe.Field, 0, len(needed))
	sourceFields := recipeSourceFields(meta.Info)
	byColumn := make(map[int]int, len(needed))
	for _, id := range needed {
		column, e := strconv.Atoi(strings.TrimPrefix(id, "C"))
		if e != nil || column < 1 || column > analysis.MaxSourceColumns || column > len(meta.Info.Fields) {
			job.fail(i18n.Error("service.recipesCapture.analysisSchemeFieldDoesNot", i18n.Args{"id": id}))
			return
		}
		f := sourceFields[column-1]
		byColumn[column] = len(fields)
		fields = append(fields, f)
	}
	format := wb.Format
	if strings.HasPrefix(q.Source, "scratch:") {
		format = "scratch"
	}
	src, err := a.sourceQuery(job.ctx, wb, q, func(row int, cell workbook.CellData, numeric bool) error {
		if slot, ok := byColumn[cell.Col]; ok {
			fields[slot].Mask |= uint16(recipeCellMask(format, cell, numeric))
		}
		return job.ctx.Err()
	}, job.SourceRows)
	if err != nil {
		job.fail(err)
		return
	}
	defer src.close()
	if src.stamp() != job.SourceStamp {
		job.fail(i18n.Error("service.recipesCapture.analysisSourceModifiedRunAnalysis"))
		return
	}
	if err := analysis.Validate(q, src.fields); err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	job.Stage, job.Total = "stage.task.captureActualTypeDataRead", int64(src.total)
	job.mu.Unlock()
	if len(needed) > 0 {
		var scanned int64
		last := time.Now()
		err = src.scan(job.ctx, needed, func(analysis.Record) error {
			scanned++
			if scanned == int64(src.total) || scanned&4095 == 0 && time.Since(last) > 200*time.Millisecond {
				job.mu.Lock()
				job.Progress = scanned
				job.mu.Unlock()
				last = time.Now()
			}
			return job.ctx.Err()
		})
		if err == nil && scanned != int64(src.total) {
			err = i18n.Error("service.recipesCapture.fieldTypeCaptureDidNot")
		}
		if err != nil {
			job.fail(err)
			return
		}
	}
	for i := range fields {
		mask := recipeTypeMask(fields[i].Mask)
		fields[i].Uncertain = mask.uncertain()
		if mask == 0 || fields[i].Uncertain {
			job.fail(i18n.Error("service.recipesCapture.readingTypeFieldVerifyThis", i18n.Args{"value": fields[i].Name, "label": mask.label()}))
			return
		}
	}
	now := job.Created.UTC().Format(time.RFC3339Nano)
	draft := recipe.Recipe{Version: recipe.VersionForQuery(q), ID: randomID(), Name: "待保存分析方案", Origin: recipe.Origin{Name: workbookDisplayName(wb), Source: q.Source, Format: format, Fingerprint: wb.Fingerprint, HeaderRow: q.HeaderRow, SourceScope: q.SourceScope}, Fields: fields, Steps: recipe.StepsFromQuery(q, job.Format), Created: now, Updated: now}
	if err := validateSavedRecipe(draft); err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	if job.ctx.Err() != nil {
		job.mu.Unlock()
		job.fail(job.ctx.Err())
		return
	}
	job.Draft, job.Status, job.Stage, job.Progress = draft, "done", "stage.task.fieldTypeCaptureCompleteBut", int64(src.total)
	job.mu.Unlock()
}

func (a *App) handleRecipeCaptureStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getRecipeCapture(r.URL.Query().Get("id"))
	if job == nil || a.currentWorkbook() != job.Workbook {
		writeMessage(w, 404, "service.recipesCapture.analysisPlanCapturedExpiredData")
		return
	}
	job.mu.RLock()
	out := map[string]any{"status": job.Status, "stage": responseLocale(w).Stage(job.Stage), "error": job.Error, "progress": job.Progress, "total": job.Total}
	if job.Status == "done" {
		out["result"] = map[string]any{"captureId": job.ID, "fields": job.Draft.Fields, "steps": len(job.Draft.Steps), "origin": job.Draft.Origin}
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleRecipeCaptureCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.recipesCapture.captureInvalidTaskParameters")
		return
	}
	if job := a.getRecipeCapture(req.ID); job != nil {
		job.cancel()
	}
	writeJSON(w, map[string]bool{"cancelled": true})
}

func (a *App) handleRecipeCaptureCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		Book             string `json:"book"`
		WorkbookKey      string `json:"workbookKey"`
		ExpectedRevision *int64 `json:"expectedRevision"`
	}
	if decodeAnalysis(r, &req) != nil || req.ExpectedRevision == nil || !recipe.ValidName(strings.TrimSpace(req.Name)) {
		writeMessage(w, 400, "service.recipesCapture.invalidAnalysisPlanNameOr")
		return
	}
	job := a.getRecipeCapture(req.ID)
	if job == nil {
		writeMessage(w, 409, "service.recipesCapture.analysisPlanExpiredCaptureAgain")
		return
	}
	job.mu.RLock()
	if job.Status != "done" || job.ctx.Err() != nil {
		job.mu.RUnlock()
		writeMessage(w, 409, "service.recipesCapture.analysisPlanCaptureUncompletedOr")
		return
	}
	value := job.Draft
	wb := job.Workbook
	job.mu.RUnlock()
	value.Name = strings.TrimSpace(req.Name)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint {
		writeMessage(w, 409, "service.recipesCapture.workbookSwitchedAnalysisPlanNot")
		return
	}
	revision, err := saveCapturedRecipe(value, *req.ExpectedRevision)
	if err != nil {
		code := 500
		if errors.Is(err, errRecipeRevision) || errors.Is(err, errRecipeName) {
			code = 409
		}
		writeFailure(w, code, err)
		return
	}
	writeJSON(w, map[string]any{"id": value.ID, "revision": revision})
}

func (a *App) cancelRecipeCaptures() {
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	for _, job := range a.recipeCaptures {
		job.cancel()
	}
}
func (a *App) detachRecipeCaptures() func() {
	a.recipeMu.Lock()
	jobs := a.recipeCaptures
	a.recipeCaptures = make(map[string]*recipeCaptureTask)
	for _, job := range jobs {
		job.cancel()
	}
	a.recipeMu.Unlock()
	return func() {
		for _, job := range jobs {
			<-job.done
		}
	}
}
