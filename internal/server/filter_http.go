package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func (a *App) handleFilterLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "工作表不存在")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, http.StatusConflict, "工作表索引不可用")
		return
	}
	var result *FilterResult
	var visible *sheetRowView
	if id := r.URL.Query().Get("viewId"); id != "" {
		var err error
		visible, err = a.rowViewSnapshot(id, wb, idx, meta.Path, r.URL.Query().Get("filterId"))
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer visible.close()
	} else {
		result = a.filterSnapshot(r.URL.Query().Get("filterId"))
		defer result.close()
		if result == nil || result.SheetPath != meta.Path {
			writeError(w, http.StatusNotFound, "筛选结果已失效")
			return
		}
	}
	row := clamp(parseIntDefault(r.URL.Query().Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
	through := clamp(parseIntDefault(r.URL.Query().Get("excludeThroughRow"), 0), 0, 30)
	if visible != nil && r.URL.Query().Has("excludeThroughRow") {
		value, err := strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || value < 0 || value > 30 {
			writeError(w, 400, "冻结行参数无效")
			return
		}
		through = value
	}
	view, err := newSelectionRowView(idx, result, through, visible)
	if err != nil {
		writeError(w, 400, err.Error())
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
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "找不到工作表")
		return
	}
	col := parseIntDefault(r.URL.Query().Get("column"), 0)
	header := parseIntDefault(r.URL.Query().Get("headerRow"), 0)
	if col < 1 || col > formats.ExcelColumns || header < 0 || header > formats.ExternalRows {
		writeError(w, http.StatusBadRequest, "筛选列或表头行无效")
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
		writeError(w, http.StatusConflict, "工作簿已切换，请重新读取筛选选项")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		a.cacheMu.RUnlock()
		writeError(w, http.StatusConflict, "请先打开工作表")
		return
	}
	items, truncated, err := wb.DistinctColumnValues(idx, col, header, maxDistinct, r.Context().Done())
	a.cacheMu.RUnlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusInternalServerError, "读取筛选选项失败："+err.Error())
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
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	if (len(req.Rules) == 0 && req.SortColumn == 0 && len(req.SortKeys) == 0) || len(req.Rules) > 32 {
		writeError(w, http.StatusBadRequest, "筛选条件必须有有效的过滤项或排序列，最多32项")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeError(w, http.StatusConflict, "筛选来源已切换，请重新运行")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
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
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	idx := wb.GetCachedIndex(meta.Path)
	a.cacheMu.RUnlock()
	if idx == nil {
		writeError(w, http.StatusConflict, "请先打开工作表，再执行筛选")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &FilterJob{ID: randomID(), Sheet: meta.Name, Status: "queued", Stage: "等待筛选任务", Started: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{})}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		cancel()
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	if a.wb != wb {
		a.mu.Unlock()
		cancel()
		writeError(w, http.StatusConflict, "工作簿已切换，请重新选择工作表")
		return
	}
	if !pruneFilterJobsLocked(a.filterJobs, formats.QueryTaskHistory) {
		a.mu.Unlock()
		cancel()
		writeError(w, http.StatusTooManyRequests, "正在回收筛选任务，请稍后重试")
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
	a.retainSubmitted(job.done)
	go a.runFilter(wb, meta, idx, req.Rules, workbook.FilterOptions{HeaderRow: req.HeaderRow, SortColumn: req.SortColumn, Descending: req.Descending, SortKeys: req.SortKeys}, job)
	writeJSON(w, map[string]any{"jobId": job.ID})
}

func (a *App) runFilter(wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, rules []workbook.FilterRule, opts workbook.FilterOptions, job *FilterJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failFilterJob(job, fmt.Errorf("筛选过程发生异常：%v", recovered))
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
		job.Stage = "已取消"
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
	job.Stage = "正在扫描工作表"
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
		if stage == "正在建立行号定位" {
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
			job.Stage = fmt.Sprintf("筛选中 · %d/%d 行", done, total)
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
		writeError(w, http.StatusNotFound, "筛选任务不存在或已失效")
		return
	}
	job.mu.RLock()
	out := map[string]any{"id": job.ID, "sheet": job.Sheet, "status": job.Status, "stage": job.Stage, "progress": job.Progress, "scanned": job.Scanned, "durationMs": time.Since(job.Started).Milliseconds(), "total": job.Total, "error": job.Error, "filterId": job.ResultID}
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	a.mu.Lock()
	old := a.filterResults[req.ID]
	delete(a.filterResults, req.ID)
	a.mu.Unlock()
	old.close()
	writeJSON(w, map[string]any{"cleared": true})
}
