package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type rowViewTask struct {
	mu                    sync.RWMutex
	ID, SheetPath, Status string
	Error, ResultID       string
	Scanned, Total        int
	started               time.Time
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  chan struct{}
}

type rowViewRequest struct {
	WorkbookKey      string                     `json:"workbookKey"`
	Book             string                     `json:"book"`
	Sheet            string                     `json:"sheet"`
	FilterID         string                     `json:"filterId,omitempty"`
	BaseID           string                     `json:"baseId,omitempty"`
	Settings         *workbook.AxisViewSettings `json:"settings,omitempty"`
	Change           *rowViewChange             `json:"change,omitempty"`
	Persist          bool                       `json:"persist,omitempty"`
	ExpectedRevision *int64                     `json:"expectedRevision,omitempty"`
}

func (a *App) handleRowViewStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req rowViewRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.RowViewRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.Settings != nil && req.Change != nil || req.Persist && req.ExpectedRevision == nil || req.ExpectedRevision != nil && (*req.ExpectedRevision < 0 || *req.ExpectedRevision >= maxConfigRevision) {
		writeMessage(w, 400, "service.rowViewTasks.rowVisibilityParametersOrRevision")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.rowViewTasks.workbookSourceSwitchedOpenView")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, 404, "service.rowViewTasks.worksheetDoesNotExist")
		return
	}
	a.cacheMu.RLock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		a.cacheMu.RUnlock()
		writeMessage(w, 409, "service.rowViewTasks.openIndexWorksheetFirst")
		return
	}
	var base *sheetRowView
	if req.BaseID != "" {
		a.mu.RLock()
		old := a.rowViews[req.BaseID]
		if a.wb == wb && old != nil && old.data.wb == wb && old.data.idx == idx && old.data.meta.Path == meta.Path {
			base, _ = old.retain()
		}
		a.mu.RUnlock()
		if base == nil {
			a.cacheMu.RUnlock()
			writeMessage(w, 409, "service.rowViewTasks.originalVisibleRowViewBecome")
			return
		}
		defer base.close()
	}
	var filtered *FilterResult
	if base != nil && req.FilterID != "" && base.data.filterID == req.FilterID {
		filtered = base.data.filter.snapshot()
		if filtered == nil {
			a.cacheMu.RUnlock()
			writeMessage(w, 409, "service.filter.expired")
			return
		}
	} else {
		selection, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, "")
		if err != nil {
			a.cacheMu.RUnlock()
			writeFailure(w, 409, err)
			return
		}
		filtered = selection.filter
	}
	unpin := a.pinWorkbooks(wb)
	a.cacheMu.RUnlock()
	defer func() {
		if unpin != nil {
			unpin()
			filtered.close()
		}
	}()
	path, err := rowViewSettingsPath(wb)
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	var store rowViewSettingsStore
	settingsWarning := ""
	if base == nil || req.Persist {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err = readRowViewSettings(path, wb)
		lock.RUnlock()
		if err != nil {
			if req.Settings == nil || req.Persist {
				writeFailure(w, 422, err)
				return
			}
			// Only an explicit, non-persistent choice may recover the display.
			// Do not use a partially decoded revision or repair the original file.
			store = rowViewSettingsStore{}
			settingsWarning = "已保存的行显隐设置无法读取；当前操作临时生效，原文件已保留"
		}
	}
	settings, revision := store.Sheets[meta.Path], store.Revision
	var reuse *workbook.AxisVisibility
	if base != nil {
		settings, revision = base.data.settings, base.data.revision
		settingsWarning = base.data.settingsWarning
		if req.Settings == nil && req.Change == nil {
			reuse = base.data.visibility
		}
	}
	if req.Persist && (*req.ExpectedRevision != store.Revision || *req.ExpectedRevision != revision) {
		writeMessage(w, 409, "service.rowViewTasks.rowVisibilitySettingsHaveModified")
		return
	}
	if req.Settings != nil {
		settings = *req.Settings
	}
	maximum := workbook.MaxRowsForIndex(idx)
	if req.Change != nil {
		settings, err = applyRowViewChange(settings, maximum, *req.Change)
	} else {
		settings, err = settings.Normalized(maximum)
	}
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &rowViewTask{ID: randomID(), SheetPath: meta.Path, Status: "queued", started: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 409, "service.rowViewTasks.workbookSwitchedOrCurrentlyUnder")
		return
	}
	active := 0
	for _, old := range a.rowViewJobs {
		select {
		case <-old.done:
		default:
			active++
		}
	}
	if active >= formats.RowViewActiveTasks {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 429, "service.rowViewTasks.thereTwoVisibleTaskRows")
		return
	}
	for _, old := range a.rowViewJobs {
		if old.SheetPath == meta.Path {
			old.cancel()
		}
	}
	a.rowViewJobs[job.ID] = job
	a.rowViewLatest[meta.Path] = job.ID
	pruneRowViewTasksLocked(a.rowViewJobs)
	a.mu.Unlock()
	go a.runRowView(wb, meta, idx, filtered, settings, revision, settingsWarning, req, path, unpin, reuse, job)
	unpin = nil // job, then the published snapshot, owns the source pin and order
	writeJSON(w, map[string]any{"jobId": job.ID})
}

func pruneRowViewTasksLocked(jobs map[string]*rowViewTask) {
	for len(jobs) > formats.RowViewTaskHistory {
		oldest := ""
		for id, job := range jobs {
			select {
			case <-job.done:
				if oldest == "" || job.started.Before(jobs[oldest].started) {
					oldest = id
				}
			default:
			}
		}
		if oldest == "" {
			return
		}
		delete(jobs, oldest)
	}
}

func (a *App) runRowView(wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, filtered *FilterResult, settings workbook.AxisViewSettings, revision int64, settingsWarning string, req rowViewRequest, path string, unpin func(), reuse *workbook.AxisVisibility, job *rowViewTask) {
	defer close(job.done)
	defer job.cancel()
	defer func() { filtered.close() }()
	defer func() {
		if unpin != nil {
			unpin()
		}
	}()
	var err error
	defer func() {
		if err != nil {
			job.mu.Lock()
			if job.ctx.Err() != nil || errors.Is(err, context.Canceled) {
				job.Status = "cancelled"
			} else {
				job.Status, job.Error = "error", "生成可见行视图失败："+err.Error()
			}
			job.mu.Unlock()
		}
	}()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		err = job.ctx.Err()
		return
	}
	defer releaseScan()
	job.mu.Lock()
	job.Status = "running"
	job.Total = idx.RowCount()
	job.mu.Unlock()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if err = wb.CheckSourceUnchanged(); err != nil {
		return
	}
	visibility := reuse
	if visibility == nil {
		var layout workbook.SourceLayout
		layout, err = wb.SourceLayoutContext(job.ctx, meta, idx)
		if err != nil {
			return
		}
		visibility, err = layout.BuildAxisVisibility(job.ctx, "row", workbook.MaxRowsForIndex(idx), settings)
		if err != nil {
			return
		}
	}
	var projection *workbook.RowProjection
	if filtered != nil {
		projection, err = workbook.NewRowProjection(job.ctx, wb.CacheDir, idx, filtered.Rows, visibility, 0, func(done, total int) {
			job.mu.Lock()
			job.Scanned, job.Total = done, total
			job.mu.Unlock()
		})
		if err != nil {
			return
		}
	}
	v := ownSheetRowView(&sheetRowViewData{id: job.ID, wb: wb, idx: idx, meta: meta, filterID: req.FilterID, sorted: filtered != nil && filtered.Sorted, settings: settings, revision: revision, settingsWarning: settingsWarning, visibility: visibility, projection: projection, filter: filtered, created: time.Now(), unpin: unpin})
	unpin, filtered = nil, nil
	defer func() { v.close() }()
	if err = wb.CheckSourceUnchanged(); err != nil {
		return
	}
	err = a.publishRowView(v, job, req.Persist, req.ExpectedRevision, path, durableConfigOps)
}

// Publication is the commit point. All cancellation/switch operations take
// App.mu before canceling a row-view job. A save failure keeps both the old
// settings and the old view. Only small configuration IO occurs under the lock.
func (a *App) publishRowView(v *sheetRowView, job *rowViewTask, persist bool, expected *int64, path string, ops configWriteOps) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wb != v.data.wb || a.maintenance || a.rowViewLatest[v.data.meta.Path] != job.ID || job.ctx.Err() != nil || v.data.wb.GetCachedIndex(v.data.meta.Path) != v.data.idx {
		return context.Canceled
	}
	if persist {
		if expected == nil {
			return i18n.Error("service.rowViewTasks.missingRowVisibilitySettingsRevision")
		}
		revision, err := saveRowViewSettings(path, v.data.wb, v.data.meta.Path, v.data.settings, *expected, ops)
		if err != nil {
			return err
		}
		v.data.revision = revision // not yet shared with any consumer
	}
	owner, err := v.retain()
	if err != nil {
		return err
	}
	a.rowViews[v.data.id] = owner
	pruneRowViewsLocked(a.rowViews, formats.RowViewSnapshots)
	job.mu.Lock()
	job.Status, job.ResultID, job.Scanned = "done", v.data.id, job.Total
	job.mu.Unlock()
	return nil
}

func saveRowViewSettings(path string, wb *workbook.Workbook, sheetPath string, settings workbook.AxisViewSettings, expected int64, ops configWriteOps) (int64, error) {
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readRowViewSettings(path, wb)
	if err != nil {
		return 0, err
	}
	if expected != store.Revision || store.Revision+1 >= maxConfigRevision {
		return 0, i18n.Error("service.rowViewTasks.displaySettingsThisRowHave")
	}
	if (settings.Mode == "" || settings.Mode == "original") && len(settings.Ranges) == 0 && len(settings.Groups) == 0 && settings.Depth == nil {
		delete(store.Sheets, sheetPath)
	} else {
		if _, exists := store.Sheets[sheetPath]; !exists && len(store.Sheets) >= formats.RowViewSavedSheets {
			return 0, i18n.Error("service.rowViewTasks.numberWorksheetsRowVisibilitySettings")
		}
		store.Sheets[sheetPath] = settings
	}
	store.Revision++
	if err := writeSavedConfig(path, formats.RowViewConfigBytes, store, ops); err != nil {
		return 0, err
	}
	return store.Revision, nil
}

func (a *App) handleRowViewStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.rowViewJobs[r.URL.Query().Get("id")]
	a.mu.RUnlock()
	if job == nil {
		writeMessage(w, 404, "service.rowViewTasks.visibleRowTasksHaveBecome")
		return
	}
	job.mu.RLock()
	payload := map[string]any{"id": job.ID, "status": job.Status, "scanned": job.Scanned, "total": job.Total, "error": job.Error, "viewId": job.ResultID}
	job.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) handleRowViewCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeMessage(w, 400, "service.rowViewTasks.taskIdentifierInvalid")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	job := a.rowViewJobs[req.ID]
	cancelled := false
	if job != nil {
		job.mu.RLock()
		cancelled = job.Status == "queued" || job.Status == "running"
		job.mu.RUnlock()
		if cancelled {
			job.cancel()
		}
	}
	writeJSON(w, map[string]any{"cancelled": cancelled})
}

func (a *App) handleRowViewClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeMessage(w, 400, "service.rowViewTasks.viewIdentifierInvalid")
		return
	}
	a.mu.Lock()
	v := a.rowViews[req.ID]
	delete(a.rowViews, req.ID)
	if job := a.rowViewJobs[req.ID]; job != nil {
		job.cancel()
	}
	a.mu.Unlock()
	v.close()
	writeJSON(w, map[string]any{"cleared": v != nil})
}

func (a *App) handleRowView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	base := a.rowViews[r.URL.Query().Get("id")]
	var v *sheetRowView
	if base != nil && base.data.wb == a.wb {
		v, _ = base.retain()
	}
	a.mu.RUnlock()
	defer v.close()
	if v == nil {
		writeMessage(w, 404, "service.rowViewTasks.visibleRowViewInvalidReopen")
		return
	}
	payload := v.payload()
	payload["hiddenRanges"] = v.data.visibility.HiddenRanges()
	payload["groupCount"] = v.data.visibility.GroupCount()
	writeJSON(w, payload)
}

func (a *App) handleRowViewGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	offset, e1 := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, e2 := strconv.Atoi(r.URL.Query().Get("limit"))
	if e1 != nil || e2 != nil || offset < 0 || limit < 1 || limit > formats.RowViewGroupPage {
		writeMessage(w, 400, "service.rowViewTasks.invalidOutlineReadingRange")
		return
	}
	a.mu.RLock()
	base := a.rowViews[r.URL.Query().Get("id")]
	var v *sheetRowView
	if base != nil && base.data.wb == a.wb {
		v, _ = base.retain()
	}
	a.mu.RUnlock()
	defer v.close()
	if v == nil {
		writeMessage(w, 404, "service.rowViewTasks.visibleRowViewInvalidReopen")
		return
	}
	groups, err := v.data.visibility.GroupPage(offset, limit)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"id": v.data.id, "groups": groups, "total": v.data.visibility.GroupCount()})
}
