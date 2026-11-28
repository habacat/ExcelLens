package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/printing"
	"excellens/internal/workbook"
)

type printTask = sourceMetadataTask[printSnapshot]

type printSnapshot struct {
	plan       *printing.Plan
	properties map[string]map[string]workbook.SourcePrintProperty
	merges     []workbook.CellMerge
	notices    []string
	complete   bool
	bytes      int64
}

func (snapshot *printSnapshot) BudgetBytes() int64 { return snapshot.bytes }

type printStartRequest struct {
	WorkbookKey string           `json:"workbookKey"`
	Book        string           `json:"book"`
	Sheet       string           `json:"sheet"`
	Options     printing.Options `json:"options"`
}

func (a *App) handlePrintStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		printStartRequest
		IndexIfNeeded bool `json:"indexIfNeeded,omitempty"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.PrintPreviewRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.Sheet == "" {
		writeMessage(w, 400, "service.printingTasks.printPreviewParametersInvalid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.printingTasks.printPreviewSourceSwitchedPrepare")
		return
	}
	source, supported := formats.Lookup(wb.Format)
	if !supported || !source.PrintMetadata {
		writeMessage(w, 422, "service.printingTasks.thisSourceFormatDoesNot")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	meta, found := wb.FindSheet(req.Sheet)
	if !found {
		writeMessage(w, 404, "service.printingTasks.printPreviewWorksheetDoesNot")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil && req.IndexIfNeeded {
		release, err := a.backgroundIndex(r.Context())
		if err != nil {
			writeMessage(w, 408, "service.printingTasks.preparationWorkTableCancelled")
			return
		}
		idx, err = wb.BuildIndex(meta, nil, r.Context().Done())
		release()
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
	}
	if idx == nil {
		writeMessage(w, 409, "service.printingTasks.openCompleteWorksheetIndexBefore")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &printTask{ID: randomID(), Mode: "preview", Status: "queued", Stage: "stage.task.waitingForPrintPreviewPreparation", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 409, "service.printingTasks.workbookSwitchedOrCurrentlyUnder")
		return
	}
	if activeMetadataTasks(a.printJobs) >= formats.PrintPreviewActiveTasks {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 429, "service.printingTasks.printPreviewBeingPreparedWait")
		return
	}
	a.prunePrintTasksLocked("", formats.PrintPreviewTaskHistory-1)
	unpin := a.pinWorkbooks(wb)
	a.printJobs[job.ID] = job
	a.mu.Unlock()
	go runSourceMetadataTask(a, job, unpin, func() bool {
		return a.wb == wb && a.printJobs[job.ID] == job && wb.GetCachedIndex(meta.Path) == idx
	}, func() { a.prunePrintTasksLocked(job.ID, formats.PrintPreviewTaskHistory) }, func(progress func(int64, int64, string)) (*printSnapshot, error) {
		source, err := wb.ReadPrintSource(job.ctx, meta, idx, progress)
		if err != nil {
			return nil, err
		}
		progress(0, 0, "stage.task.calculatingPaperPagination")
		plan, err := printing.New(job.ctx, source, req.Options)
		if err != nil {
			return nil, err
		}
		layout, err := wb.SourceLayoutContext(job.ctx, meta, idx)
		if err != nil {
			return nil, err
		}
		if len(layout.Merges) >= formats.PrintPreviewMergeLimit {
			return nil, i18n.Error("service.printingTasks.sourceMergeAreaReachedCapacity")
		}
		if count, known := source.MergeCount(); known && count != len(layout.Merges) {
			return nil, i18n.Error("service.printingTasks.publicMergedMetadataPrintPreview")
		}
		settings := source.Settings()
		propertyData, err := json.Marshal(settings.Properties)
		if err != nil {
			return nil, err
		}
		bytes := plan.BudgetBytes() + int64(len(layout.Merges))*64 + int64(len(propertyData))*2 + 4096
		if bytes > formats.PrintPreviewSnapshotBytes {
			return nil, i18n.Error("service.printingTasks.printPreviewSnapshotExceedsCapacity")
		}
		notices := slices.Clone(settings.Notices)
		if layout.StateNotice != "" {
			notices = append(notices, layout.StateNotice)
		}
		return &printSnapshot{plan: plan, properties: settings.Properties, merges: slices.Clone(layout.Merges), notices: notices, complete: settings.Complete, bytes: bytes}, nil
	})
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "sheet": meta.Name})
}

func (a *App) prunePrintTasksLocked(keep string, maximum int) {
	pruneMetadataTasks(a.printJobs, keep, metadataTaskLimits{maximum, formats.PrintPreviewSnapshots, formats.PrintPreviewSnapshotBytes}, (*printSnapshot).BudgetBytes)
}

func (a *App) handlePrintStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.printJobs[r.URL.Query().Get("id")]
	if job == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		writeMessage(w, 404, "service.printingTasks.printPreviewTaskFailedPrepare")
		return
	}
	payload := metadataTaskPayload(responseLocale(w), job)
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) printAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.PrintPreviewRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := metadataTaskAction(a.printJobs, id, clear)
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"changed": changed})
}

func (a *App) handlePrintCancel(w http.ResponseWriter, r *http.Request) { a.printAction(w, r, false) }
func (a *App) handlePrintClear(w http.ResponseWriter, r *http.Request)  { a.printAction(w, r, true) }

// Capture and pin under the cache read lock. A queued page retains the source
// without holding the lock. The task and source are checked again before use.
func (a *App) printSnapshotFor(r *http.Request) (*printTask, *printSnapshot, func(), error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.RLock()
	defer a.mu.RUnlock()
	job := a.printJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.Snapshot == nil || job.discarded || a.wb != job.Workbook {
		return nil, nil, nil, i18n.Error("service.printingTasks.printPreviewResultBecomeInvalid")
	}
	return job, job.Snapshot, a.pinWorkbooks(job.Workbook), nil
}

func (a *App) printSnapshotCurrent(ctx context.Context, job *printTask, snapshot *printSnapshot) error {
	if err := snapshot.plan.Validate(ctx); err != nil {
		return err
	}
	a.mu.RLock()
	current := a.wb == job.Workbook && a.printJobs[job.ID] == job && !job.discarded && job.Snapshot == snapshot
	a.mu.RUnlock()
	if !current {
		return i18n.Error("service.printingTasks.sourceOrTaskPrintPreview")
	}
	return nil
}

func printEnvelope(job *printTask) map[string]any {
	return map[string]any{"id": job.ID, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "sheet": job.Meta.Name}
}

func (a *App) handlePrintSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job, snapshot, release, err := a.printSnapshotFor(r)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	defer release()
	payload := printEnvelope(job)
	payload["summary"], payload["sourceProperties"] = snapshot.plan.Summary(), snapshot.properties
	payload["sourceComplete"], payload["sourceNotices"] = snapshot.complete, snapshot.notices
	if err := a.printSnapshotCurrent(r.Context(), job, snapshot); err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, payload)
}

func (a *App) handlePrintPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || index < 0 {
		writeMessage(w, 400, "service.printingTasks.printPreviewPageNumberInvalid")
		return
	}
	job, snapshot, release, err := a.printSnapshotFor(r)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	defer release()
	if index >= snapshot.plan.Summary().Pages {
		writeMessage(w, 400, "service.printingTasks.printPreviewPageNumberOut")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(job.ctx, cancel)
	defer func() { stop(); cancel() }()
	releaseQuery, err := a.lockQueryRead(ctx)
	if err != nil {
		if r.Context().Err() == nil {
			writeMessage(w, 409, "service.printingTasks.printPreviewTaskCancelledPrepare")
		}
		return
	}
	defer releaseQuery()
	if err = a.printSnapshotCurrent(ctx, job, snapshot); err != nil {
		if r.Context().Err() == nil {
			writeFailure(w, 409, err)
		}
		return
	}
	page, err := readPrintPage(ctx, job, snapshot, index)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if r.Context().Err() == nil {
				writeMessage(w, 409, "service.printingTasks.printPreviewTaskCancelledPrepare")
			}
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, errPrintPageCapacity) {
			status = http.StatusRequestEntityTooLarge
		}
		if currentErr := a.printSnapshotCurrent(ctx, job, snapshot); currentErr != nil {
			err, status = currentErr, http.StatusConflict
		}
		writeFailure(w, status, err)
		return
	}
	payload := printEnvelope(job)
	payload["page"], payload["content"] = index, page
	data, err := json.Marshal(payload)
	if err != nil || len(data) > formats.PrintPreviewPageBytes {
		writeMessage(w, 413, "service.printingTasks.contentOnThisPageExceeds")
		return
	}
	if err := a.printSnapshotCurrent(ctx, job, snapshot); err != nil {
		writeFailure(w, 409, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
