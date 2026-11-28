package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) handleFilterLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.filterHttp.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.filterHttp.worksheetDoesNotExist")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.filterHttp.worksheetIndexUnavailable")
		return
	}
	var result *FilterResult
	var visible *sheetRowView
	if id := r.URL.Query().Get("viewId"); id != "" {
		var err error
		visible, err = a.rowViewSnapshot(id, wb, idx, meta.Path, r.URL.Query().Get("filterId"))
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		defer visible.close()
	} else {
		result = a.filterSnapshot(r.URL.Query().Get("filterId"))
		defer result.close()
		if result == nil || result.SheetPath != meta.Path {
			writeMessage(w, http.StatusNotFound, "service.filter.expired")
			return
		}
	}
	row := clamp(parseIntDefault(r.URL.Query().Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
	through := clamp(parseIntDefault(r.URL.Query().Get("excludeThroughRow"), 0), 0, 30)
	if visible != nil && r.URL.Query().Has("excludeThroughRow") {
		value, err := strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || value < 0 || value > 30 {
			writeMessage(w, 400, "service.filterHttp.frozenRowParametersInvalid")
			return
		}
		through = value
	}
	view, err := newSelectionRowView(idx, result, through, visible)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	at, found := view.locate(row)
	payload := map[string]any{"found": found, "visualIndex": at + 1, "row": row}
	if visible != nil {
		payload["viewId"], payload["hidden"] = visible.data.id, !visible.data.visibility.Visible(row)
		if row <= through {
			payload["pinned"], payload["found"], payload["visualIndex"] = true, visible.data.visibility.Visible(row), row
		}
	}
	writeJSON(w, payload)
}

// Distinct values back an Excel-style checkbox dropdown. This endpoint does
// not change the sheet: its one-column scan is memoized for the workbook.
func (a *App) handleFilterDistinct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.filterHttp.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.filterHttp.unableFindWorksheet")
		return
	}
	col := parseIntDefault(r.URL.Query().Get("column"), 0)
	header := parseIntDefault(r.URL.Query().Get("headerRow"), 0)
	if col < 1 || col > formats.ExcelColumns || header < 0 || header > formats.ExternalRows {
		writeMessage(w, http.StatusBadRequest, "service.filterHttp.filteringColumnsOrHeaderRows")
		return
	}
	const maxDistinct = 2500
	key := fmt.Sprintf("%s|%s|%d|%d", wb.Fingerprint, meta.Path, col, header)
	a.mu.RLock()
	cached, found := a.distinctCache[key]
	a.mu.RUnlock()
	if found {
		writeJSON(w, map[string]any{"values": cached.Items, "truncated": cached.Truncated, "cached": true})
		return
	}
	release, err := a.backgroundQuery(r.Context())
	if err != nil {
		return
	}
	defer release()
	a.cacheMu.RLock()
	if a.currentWorkbook() != wb {
		a.cacheMu.RUnlock()
		writeMessage(w, http.StatusConflict, "service.filterHttp.workbookSwitchedReadFilterOptions")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		a.cacheMu.RUnlock()
		writeMessage(w, http.StatusConflict, "service.filterHttp.openWorksheetFirst")
		return
	}
	items, truncated, err := wb.DistinctColumnValues(idx, col, header, maxDistinct, r.Context().Done())
	a.cacheMu.RUnlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
			return
		}
		writeMessage(w, http.StatusInternalServerError, "service.filterHttp.failedReadFilterOptions", i18n.Args{"detail": err})
		return
	}
	// Never let a previous workbook's scan contaminate the new workbook.
	a.mu.Lock()
	if a.wb == wb {
		if len(a.distinctCache) >= 64 {
			a.distinctCache = make(map[string]DistinctCache)
		}
		a.distinctCache[key] = DistinctCache{Items: items, Truncated: truncated}
	}
	a.mu.Unlock()
	writeJSON(w, map[string]any{"values": items, "truncated": truncated, "cached": false})
}

func (a *App) handleFilterStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req filterRequest
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.FilterRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeMessage(w, http.StatusBadRequest, "service.filterHttp.requestedContentInvalid")
		return
	}
	if (len(req.Rules) == 0 && req.SortColumn == 0 && len(req.SortKeys) == 0) || len(req.Rules) > 32 {
		writeMessage(w, http.StatusBadRequest, "service.filterHttp.filterConditionsMustHaveValid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.filterHttp.noWorkbookOpen")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeMessage(w, http.StatusConflict, "service.filterHttp.filterSourceSwitchedRunProcess")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.filterHttp.unableFindSpecifiedWorksheet")
		return
	}
	if req.Recoverable {
		a.startCheckpointFilter(w, r, wb, req)
		return
	}
	a.cacheMu.RLock()
	if req.Book != "" {
		if err := wb.CheckSourceUnchanged(); err != nil {
			a.cacheMu.RUnlock()
			writeFailure(w, http.StatusConflict, err)
			return
		}
	}
	idx := wb.GetCachedIndex(meta.Path)
	a.cacheMu.RUnlock()
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.filterHttp.openWorksheetBeforePerformingFiltering")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &FilterJob{ID: randomID(), Sheet: meta.Name, Status: "queued", Stage: "stage.task.waitingForFilterTask", Started: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{})}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		cancel()
		writeMessage(w, http.StatusConflict, "service.filterHttp.localCacheBeingMaintainedTry")
		return
	}
	if a.wb != wb {
		a.mu.Unlock()
		cancel()
		writeMessage(w, http.StatusConflict, "service.filterHttp.workbookSwitchedChooseNewWorksheet")
		return
	}
	if !pruneFilterJobsLocked(a.filterJobs, formats.QueryTaskHistory) {
		a.mu.Unlock()
		cancel()
		writeMessage(w, http.StatusTooManyRequests, "service.filterHttp.screeningTaskBeingRecoveredTry")
		return
	}
	for _, old := range a.filterJobs {
		old.mu.RLock()
		running := old.Status == "queued" || old.Status == "running"
		old.mu.RUnlock()
		if running && old.Sheet == meta.Name {
			old.cancel()
		}
	}
	a.filterJobs[job.ID] = job
	a.mu.Unlock()
	a.retainSubmitted(job.ID, job.done)
	go a.runFilter(wb, meta, idx, req.Rules, workbook.FilterOptions{HeaderRow: req.HeaderRow, SortColumn: req.SortColumn, Descending: req.Descending, SortKeys: req.SortKeys}, job)
	writeJSON(w, map[string]any{"jobId": job.ID})
}

func (a *App) runFilter(wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, rules []workbook.FilterRule, opts workbook.FilterOptions, job *FilterJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failFilterJob(job, i18n.Error("service.filterHttp.exceptionOccurredDuringFilteringProcess", i18n.Args{"recovered": recovered}))
		}
		if job.checkpoint != nil {
			if err := job.checkpoint.close(); err != nil {
				failFilterJob(job, err)
			}
		}
		close(job.done)
	}()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		job.mu.Lock()
		job.Status = "cancelled"
		job.Stage = "stage.task.cancelled"
		job.mu.Unlock()
		return
	}
	defer releaseScan()
	if job.checkpoint != nil {
		a.runCheckpointFilter(wb, job)
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = "stage.task.scanningTheWorksheet"
	job.Total = idx.RowCount()
	job.mu.Unlock()
	a.cacheMu.RLock()
	sorting := opts.SortColumn > 0 || len(opts.SortKeys) > 0
	opts.Phase = func(stage string, done, total int) {
		job.mu.Lock()
		job.Stage = stage
		progress := 85.0
		if total > 0 {
			progress += 14 * float64(done) / float64(total)
		}
		if stage == "stage.task.establishingRowNumberPositioning" {
			progress = 99
		}
		job.Progress = max(job.Progress, progress)
		job.mu.Unlock()
	}
	var matches *workbook.RowOrder
	var err error
	func() {
		defer a.cacheMu.RUnlock()
		matches, err = wb.FilterRowOrder(job.ctx, idx, rules, opts, func(done, total int) {
			job.mu.Lock()
			job.Scanned = done
			job.Total = total
			if total > 0 {
				job.Progress = float64(done) / float64(total) * 100
				if sorting {
					job.Progress *= .85
				}
			}
			job.Stage = "stage.task.filteringInProgress"
			job.mu.Unlock()
		})
	}()
	if err != nil {
		failFilterJob(job, err)
		return
	}
	a.publishFilterResult(wb, meta, idx, rules, opts, matches, job)
}

func pruneFilterResultsLocked(results map[string]*FilterResult, keep int) {
	for len(results) > keep {
		oldestID := ""
		var oldest time.Time
		for id, result := range results {
			if oldestID == "" || result.Created.Before(oldest) {
				oldestID, oldest = id, result.Created
			}
		}
		if oldestID == "" {
			return
		}
		results[oldestID].close()
		delete(results, oldestID)
	}
}

func (a *App) handleFilterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.filterJobs[r.URL.Query().Get("id")]
	a.mu.RUnlock()
	if job == nil {
		writeMessage(w, http.StatusNotFound, "service.filterHttp.filteringTaskDoesNotExist")
		return
	}
	job.mu.RLock()
	out := map[string]any{"id": job.ID, "sheet": job.Sheet, "status": job.Status, "stage": responseLocale(w).Stage(job.Stage), "progress": job.Progress, "scanned": job.Scanned, "durationMs": time.Since(job.Started).Milliseconds(), "total": job.Total, "error": job.Error, "filterId": job.ResultID}
	out["taskId"] = job.TaskID
	status, resultID := job.Status, job.ResultID
	job.mu.RUnlock()
	if status == "done" {
		result := a.filterSnapshot(resultID)
		if result != nil {
			out["matchedRows"] = result.Rows.Len()
			out["filter"] = result.Spec
			result.close()
		} else {
			out["status"], out["error"], out["filterId"] = "error", "筛选结果已过期或取消，请重新运行", ""
		}
	}
	writeJSON(w, out)
}

func (a *App) handleFilterCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeControlRequest(w, r, 4096, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "service.filterHttp.requestedContentInvalid")
		return
	}
	a.mu.Lock()
	job := a.filterJobs[req.ID]
	if job == nil {
		a.mu.Unlock()
		writeJSON(w, map[string]any{"cancelled": false})
		return
	}
	job.cancel()
	old := a.filterResults[req.ID]
	delete(a.filterResults, req.ID)
	a.mu.Unlock()
	old.close()
	writeJSON(w, map[string]any{"cancelled": true})
}

func (a *App) handleFilterClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeControlRequest(w, r, 4096, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "service.filterHttp.requestedContentInvalid")
		return
	}
	a.mu.Lock()
	old := a.filterResults[req.ID]
	delete(a.filterResults, req.ID)
	a.mu.Unlock()
	old.close()
	writeJSON(w, map[string]any{"cleared": true})
}
