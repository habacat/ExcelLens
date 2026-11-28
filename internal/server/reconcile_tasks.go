package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"excellens/internal/i18n"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

const maxReconcileResults = 4

type reconcileTask struct {
	mu                   sync.RWMutex
	ID                   string
	Workbook             *workbook.Workbook
	Refs                 [2]reconcileSourceRef
	Books                [2]*workbook.Workbook
	Sources              [2]reconcile.Source
	Query                reconcile.Query
	Result               reconcile.Result
	Record               *reconcileRecord
	RecordError          string
	Status, Stage, Error string
	Done, Total          int64
	Created              time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	release              func()
}

func (a *App) handleReconcileStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		WorkbookKey string `json:"workbookKey"`
		PrimaryBook string `json:"primaryBook"`
		Left, Right reconcileSourceRef
		Query       reconcile.Query `json:"query"`
	}
	if decodeAnalysis(r, &req) != nil || len(req.Query.Keys) < 1 || len(req.Query.Keys) > reconcile.MaxKeys || len(req.Query.Fields) > reconcile.MaxFields {
		writeMessage(w, 400, "service.reconcileTasks.verificationParametersInvalidSelectPrimary")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || primary.Fingerprint != req.WorkbookKey || sourceBookID(primary) != req.PrimaryBook {
		writeMessage(w, 409, "service.reconcileTasks.currentWorkbookSwitchedSelectSource")
		return
	}
	leftBook, rightBook := a.resolveReconcileBook(req.Left.Book), a.resolveReconcileBook(req.Right.Book)
	if leftBook == nil || rightBook == nil {
		writeMessage(w, 409, "service.reconcileTasks.sourceVerificationClosedOrRemoved")
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != primary || a.maintenance {
		writeMessage(w, 409, "service.reconcileTasks.workbookSwitchedOrCurrentlyUnder")
		return
	}
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	for _, old := range a.reconcileJobs {
		old.mu.RLock()
		active := (old.Status == "queued" || old.Status == "running") && old.ctx.Err() == nil
		old.mu.RUnlock()
		if active {
			writeMessage(w, 429, "service.reconcileTasks.thereAlreadyVerificationTasksComplete")
			return
		}
	}
	for len(a.reconcileJobs) >= maxReconcileResults {
		var oldest *reconcileTask
		for _, old := range a.reconcileJobs {
			select {
			case <-old.done:
			default:
				continue
			}
			if oldest == nil || old.Created.Before(oldest.Created) {
				oldest = old
			}
		}
		if oldest == nil {
			writeMessage(w, 429, "service.reconcileTasks.cancelingCanceledVerificationTaskTry")
			return
		}
		delete(a.reconcileJobs, oldest.ID)
		oldest.closeSources()
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &reconcileTask{ID: randomID(), Workbook: primary, Books: [2]*workbook.Workbook{leftBook, rightBook}, Refs: [2]reconcileSourceRef{req.Left, req.Right}, Query: req.Query, Status: "queued", Stage: "stage.task.waitingForVerificationTasks", Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), release: a.pinWorkbooks(leftBook, rightBook)}
	a.reconcileJobs[job.ID] = job
	a.retainSubmitted(job.ID, job.done)
	go a.runReconcile(job)
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) runReconcile(job *reconcileTask) {
	defer close(job.done)
	defer func() {
		job.mu.RLock()
		retained := job.Status == "done"
		job.mu.RUnlock()
		if !retained {
			job.closeSources()
		}
	}()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		job.finishError(job.ctx.Err())
		return
	}
	defer releaseScan()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job.mu.Lock()
	job.Status, job.Stage = "running", "stage.task.prepareSourceSnapshots"
	job.mu.Unlock()
	var sources [2]reconcile.Source
	for i := range sources {
		var err error
		sources[i], err = a.buildReconcileSource(job.ctx, job.Books[i], job.Refs[i], reconcile.NeededColumns(job.Query, i == 1))
		if err != nil {
			job.finishError(err)
			return
		}
	}
	job.mu.Lock()
	job.Stage = "stage.task.verificationOfInputSummary"
	job.mu.Unlock()
	record, recordErr := captureReconcileRecord(job, sources)
	// Compare classified, identifiable exception groups even if their totals
	// match. Invalid keys have no stable cross-batch identity.
	groups := sha256.New()
	sinks := reconcile.Sinks{Group: func(g reconcile.Group) error {
		if g.Status != "equal" && g.Status != "within_tolerance" && g.Status != "invalid_key" {
			fmt.Fprintf(groups, "%s/%s/%d/%d\n", g.KeyID, g.Status, g.LeftCount, g.RightCount)
		}
		return nil
	}}
	result, err := reconcile.Run(job.ctx, filepath.Join(job.Workbook.CacheDir, "queries"), job.Query, sources[0], sources[1], sinks, func(stage string, done, total int64) {
		job.mu.Lock()
		job.Stage, job.Done, job.Total = stage, done, total
		job.mu.Unlock()
	})
	if err != nil {
		job.finishError(err)
		return
	}
	job.mu.Lock()
	if err := job.ctx.Err(); err != nil {
		job.mu.Unlock()
		job.finishError(err)
		return
	}
	job.Result, job.Sources, job.Status, job.Stage = result, sources, "done", "stage.task.verificationCompleted"
	if recordErr != nil {
		job.RecordError = recordErr.Error()
	} else {
		record.Counts = result.Counts
		record.ExceptionGroupsSHA256 = hex.EncodeToString(groups.Sum(nil))
		job.Record = record
	}
	job.mu.Unlock()
}

// Successful history owns its source snapshots for navigation and exports.
// Failed/cancelled history needs only its status. Detach under the task lock
// and reclaim outside it; an already borrowed export owns independent pins.
func (job *reconcileTask) closeSources() {
	job.mu.Lock()
	release := job.release
	job.release = nil
	job.Books, job.Sources = [2]*workbook.Workbook{}, [2]reconcile.Source{}
	job.mu.Unlock()
	if release != nil {
		release()
	}
}

func (job *reconcileTask) finishError(err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status, job.Stage, job.Error = "error", "stage.task.verificationFailed", err.Error()
	if errors.Is(err, context.Canceled) {
		job.Status, job.Stage, job.Error = "cancelled", "stage.task.verificationCanceled", ""
	}
}

func (a *App) getReconcileTask(id string) *reconcileTask {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	return a.reconcileJobs[id]
}

type reconcileSnapshot struct {
	book        *workbook.Workbook
	books       [2]*workbook.Workbook
	sources     [2]reconcile.Source
	refs        [2]reconcileSourceRef
	query       reconcile.Query
	counts      reconcile.Counts
	record      *reconcileRecord
	recordError string
}

// Caller holds the shared cache read lock. Retain source pins under the task
// map lock so pruning cannot release the last owner between lookup and export.
func (a *App) snapshotReconcile(id string) (reconcileSnapshot, func(), error) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	job := a.reconcileJobs[id]
	if job == nil {
		return reconcileSnapshot{}, nil, i18n.Error("service.reconcileTasks.resultExpiredRunAgain")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != "done" {
		return reconcileSnapshot{}, nil, i18n.Error("service.reconcileTasks.verificationNotCompletedYet")
	}
	snapshot := reconcileSnapshot{book: job.Workbook, books: job.Books, sources: job.Sources, refs: job.Refs, query: job.Query, counts: job.Result.Counts, record: job.Record, recordError: job.RecordError}
	return snapshot, a.pinWorkbooks(job.Books[0], job.Books[1]), nil
}

func (a *App) handleReconcileStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getReconcileTask(r.URL.Query().Get("id"))
	if job == nil {
		writeMessage(w, 404, "service.reconcileTasks.taskExpiredRunAgain")
		return
	}
	job.mu.RLock()
	out := map[string]any{"jobId": job.ID, "status": job.Status, "stage": responseLocale(w).Stage(job.Stage), "error": job.Error, "done": job.Done, "total": job.Total, "elapsedMs": time.Since(job.Created).Milliseconds()}
	if job.Status == "done" {
		out["result"] = job.Result
		out["recordError"] = job.RecordError
		if job.Record != nil {
			out["exceptionGroupsSHA256"] = job.Record.ExceptionGroupsSHA256
		}
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleReconcileCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.reconcileTasks.verificationTaskParametersInvalid")
		return
	}
	if job := a.getReconcileTask(req.ID); job != nil {
		job.cancel()
	}
	writeJSON(w, map[string]bool{"cancelled": true})
}

func (a *App) handleReconcileClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.reconcileTasks.invalidVerificationResultParameters")
		return
	}
	a.reconcileMu.Lock()
	job := a.reconcileJobs[req.ID]
	if job != nil {
		select {
		case <-job.done:
		default:
			a.reconcileMu.Unlock()
			writeMessage(w, 409, "service.reconcileTasks.cancelOrCompleteVerificationTask")
			return
		}
		delete(a.reconcileJobs, req.ID)
	}
	a.reconcileMu.Unlock()
	if job != nil {
		job.closeSources()
	}
	writeJSON(w, map[string]bool{"cleared": job != nil})
}

func (a *App) cancelReconciliations() {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	for _, job := range a.reconcileJobs {
		job.cancel()
	}
}

func (a *App) detachReconciliations() func() {
	a.reconcileMu.Lock()
	jobs, books := a.reconcileJobs, a.reconcileBooks
	for _, job := range jobs {
		job.cancel()
	}
	a.reconcileJobs, a.reconcileBooks = make(map[string]*reconcileTask), make(map[string]*reconcileAttachment)
	a.reconcileMu.Unlock()
	return func() {
		for _, job := range jobs {
			<-job.done
			job.closeSources()
		}
		for _, book := range books {
			book.release()
		}
	}
}

func (a *App) handleReconcileNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID    string `json:"jobId"`
		Side     string `json:"side"`
		Row, Col int
	}
	if decodeAnalysis(r, &req) != nil || req.Side != "left" && req.Side != "right" {
		writeMessage(w, 400, "service.reconcileTasks.verificationPositioningParametersInvalid")
		return
	}
	index := 0
	if req.Side == "right" {
		index = 1
	}
	a.cacheMu.RLock()
	locked := true
	defer func() {
		if locked {
			a.cacheMu.RUnlock()
		}
	}()
	snapshot, release, err := a.snapshotReconcile(req.JobID)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	defer release()
	if a.currentWorkbook() != snapshot.book {
		writeMessage(w, 409, "service.reconcileTasks.workbookSwitchedRerunVerification")
		return
	}
	book, source, ref := snapshot.books[index], snapshot.sources[index], snapshot.refs[index]
	validCol := false
	for _, field := range source.Info.Fields {
		if field.Column == req.Col {
			validCol = true
			break
		}
	}
	if req.Row < 1 || !validCol {
		writeMessage(w, 400, "service.reconcileTasks.invalidSourceCoordinatePositioning")
		return
	}
	if source.Info.Kind == "analysis" {
		current, err := a.reconcileScratchSource(book, reconcileSourceRef{Book: ref.Book, Source: ref.Source, Stamp: source.Info.Stamp}, nil)
		if err != nil || req.Row > current.Info.Total {
			writeMessage(w, 409, "service.reconcileTasks.analysisEditingTableModifiedOr")
			return
		}
	} else {
		if err := book.CheckSourceUnchanged(); err != nil {
			writeMessage(w, 409, "service.reconcileTasks.sourceFileChangedOrInaccessible", i18n.Args{"detail": err})
			return
		}
		meta, _ := book.FindSheet(source.Info.Sheet)
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			writeMessage(w, 409, "service.reconcileTasks.sourceIndexBecomeInvalid")
			return
		}
		at := idx.FindRowOffset(req.Row)
		if at >= idx.RowCount() || idx.RowAt(at).Number != req.Row {
			writeMessage(w, 400, "service.reconcileTasks.sourceRowDoesNotExist")
			return
		}
	}
	a.cacheMu.RUnlock()
	locked = false
	target, err := a.sourceNavigation(book, source.Info.Kind, ref.Source, source.Info.Sheet, req.Row, req.Col)
	if err != nil {
		writeFailure(w, 429, err)
		return
	}
	writeJSON(w, target)
}

func (a *App) registerComparisonRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/reconcile/batches", a.handleBatchComparison)
	mux.HandleFunc("/api/reconcile/versions", a.handleVersionCompare)
	mux.HandleFunc("/api/reconcile/report/open", a.handleOpenAuditReport)
	mux.HandleFunc("/api/reconcile/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/reconcile/fields", a.handleReconcileFields)
	mux.HandleFunc("/api/reconcile/start", a.handleReconcileStart)
	mux.HandleFunc("/api/relation/limits", a.handleRelationLimits)
	mux.HandleFunc("/api/relation/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/relation/start", a.handleRelationStart)
	mux.HandleFunc("/api/relation/status", a.handleRelationStatus)
	mux.HandleFunc("/api/relation/result", a.handleRelationResult)
	mux.HandleFunc("/api/relation/cancel", a.handleRelationCancel)
	mux.HandleFunc("/api/relation/clear", a.handleRelationClear)
	mux.HandleFunc("/api/relation/export", a.handleRelationExport)
	mux.HandleFunc("/api/relation/navigate", a.handleRelationNavigate)
	mux.HandleFunc("/api/composition/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/composition/limits", a.handleCompositionLimits)
	mux.HandleFunc("/api/composition/definitions", a.handleCompositionDefinitions)
	mux.HandleFunc("/api/composition/start", a.handleCompositionStart)
	mux.HandleFunc("/api/composition/status", a.handleCompositionStatus)
	mux.HandleFunc("/api/composition/result", a.handleCompositionResult)
	mux.HandleFunc("/api/composition/cancel", a.handleCompositionCancel)
	mux.HandleFunc("/api/composition/clear", a.handleCompositionClear)
	mux.HandleFunc("/api/reconcile/status", a.handleReconcileStatus)
	mux.HandleFunc("/api/reconcile/cancel", a.handleReconcileCancel)
	mux.HandleFunc("/api/reconcile/clear", a.handleReconcileClear)
	mux.HandleFunc("/api/reconcile/export", a.handleReconcileExport)
	mux.HandleFunc("/api/reconcile/source/remove", a.handleReconcileRemoveSource)
	mux.HandleFunc("/api/reconcile/navigate", a.handleReconcileNavigate)
}
