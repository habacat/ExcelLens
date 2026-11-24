package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"excellens/internal/formats"
	"excellens/internal/searchservice"
	"excellens/internal/workbook"
)

func (a *App) handleSearchStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req searchRequest
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CheckpointRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	if err := req.normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeError(w, http.StatusConflict, "搜索来源已切换，请重新运行")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	if req.Recoverable {
		a.startCheckpointSearch(w, r, wb, req)
		return
	}
	var idx *workbook.SheetIndex
	ctx, cancel := context.WithCancel(context.Background())
	job := &SearchJob{
		Full: req.Full, Ticket: randomID(), ID: randomID(), Sheet: meta.Name, Query: req.Query, Mode: req.Mode, Scope: req.Scope,
		Status: "running", Stage: "准备搜索", Started: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{}),
	}
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
		writeError(w, http.StatusConflict, "工作簿已切换，请重试")
		return
	}
	// Reuse an in-flight or completed identical search instead of scanning again.
	for _, existing := range a.searchJobs {
		existing.mu.RLock()
		same := existing.Full == req.Full && searchCacheKey(existing.Sheet, existing.Query, existing.Mode, existing.Scope) == searchCacheKey(meta.Name, req.Query, req.Mode, req.Scope)
		status := existing.Status
		id := existing.ID
		running := status == "running" && existing.ctx != nil && existing.ctx.Err() == nil
		existing.mu.RUnlock()
		if same && (status == "done" || running) {
			a.mu.Unlock()
			cancel()
			writeJSON(w, map[string]any{"jobId": id, "cached": status == "done", "reused": true})
			return
		}
	}
	// A completed search is also cached on disk across application restarts.
	if !req.Full {
		if cached, err := loadSearchCache(wb, meta.Name, req.Query, req.Mode, req.Scope); err == nil {
			if !pruneSearchJobsLocked(a.searchJobs, formats.QueryTaskHistory) {
				a.mu.Unlock()
				cancel()
				writeError(w, http.StatusTooManyRequests, "正在回收搜索任务，请稍后重试")
				return
			}
			close(job.done)
			a.retainSubmitted(job.ID, job.done)
			job.Status = "done"
			job.Stage = "从本机缓存加载"
			job.Progress = 100
			job.Matches = cached.Matches
			job.Truncated = cached.Truncated
			a.searchJobs[job.ID] = job
			a.mu.Unlock()
			cancel()
			writeJSON(w, map[string]any{"jobId": job.ID, "cached": true, "reused": false})
			return
		}
	}
	// Check persistent query results before requiring a worksheet index: a user
	// can reopen prior search results without loading that Sheet's XML again.
	if req.Scope == "current" {
		a.cacheMu.RLock()
		idx = wb.GetCachedIndex(meta.Path)
		a.cacheMu.RUnlock()
		if idx == nil {
			a.mu.Unlock()
			cancel()
			writeError(w, http.StatusConflict, "请先打开工作表，再执行新的搜索")
			return
		}
	}
	if !pruneSearchJobsLocked(a.searchJobs, formats.QueryTaskHistory) {
		a.mu.Unlock()
		cancel()
		writeError(w, http.StatusTooManyRequests, "正在回收搜索任务，请稍后重试")
		return
	}
	for _, old := range a.searchJobs {
		old.mu.RLock()
		running := old.Status == "running"
		sameScope := req.Scope == "all" || old.Scope == "all" || old.Sheet == meta.Name
		old.mu.RUnlock()
		if sameScope && running {
			old.cancel()
		}
	}
	a.searchJobs[job.ID] = job
	a.mu.Unlock()
	a.retainSubmitted(job.ID, job.done)
	go a.runSearch(wb, meta, idx, job)
	writeJSON(w, map[string]any{"jobId": job.ID})
}

func (a *App) runSearch(wb *workbook.Workbook, current workbook.SheetMeta, currentIndex *workbook.SheetIndex, job *SearchJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			job.mu.Lock()
			job.Status = "error"
			job.Error = fmt.Sprintf("搜索过程发生异常：%v", recovered)
			job.mu.Unlock()
		}
		if job.checkpoint != nil {
			if err := job.checkpoint.close(); err != nil {
				job.mu.Lock()
				job.Status, job.Error = "error", err.Error()
				job.mu.Unlock()
			}
		}
		// The quick search cache also supplies the recent search list.
		if !job.Full {
			_ = saveSearchCache(wb, job)
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
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if job.Full {
		a.runFullSearch(wb, job)
		return
	}
	if job.checkpoint != nil {
		a.runCheckpointSearch(wb, job)
		return
	}
	if job.Scope == "all" {
		a.runWorkbookSearch(wb, job)
		return
	}
	matches, truncated, err := searchservice.QuickSheet(wb, currentIndex, current.Name, job.Query, job.Mode, job.ctx, func(scanned, total int, currentMatches []workbook.SearchResult) {
		job.mu.Lock()
		job.Scanned = scanned
		job.Total = total
		if total > 0 {
			job.Progress = float64(scanned) / float64(total) * 100
		}
		job.Stage = fmt.Sprintf("搜索当前 Sheet · %d/%d 行", scanned, total)
		job.Matches = append(job.Matches[:0], currentMatches...)
		job.mu.Unlock()
	})
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Matches = matches
	job.Truncated = truncated
	if err != nil {
		if job.ctx.Err() != nil || err.Error() == "search cancelled" {
			job.Status = "cancelled"
			job.Stage = "已取消"
		} else {
			job.Status = "error"
			job.Error = err.Error()
		}
		return
	}
	job.Status = "done"
	job.Progress = 100
	job.Scanned = job.Total
	job.Stage = "搜索完成"
}

func (a *App) runWorkbookSearch(wb *workbook.Workbook, job *SearchJob) {
	job.mu.Lock()
	job.SheetsTotal = len(wb.Sheets)
	job.Stage = fmt.Sprintf("准备搜索全部 %d 个 Sheet", len(wb.Sheets))
	job.mu.Unlock()
	result, err := searchservice.QuickWorkbook(job.ctx, wb, job.Query, job.Mode, a.backgroundIndex, func(p searchservice.QuickProgress) {
		job.mu.Lock()
		defer job.mu.Unlock()
		job.Progress = max(job.Progress, p.Percent)
		if p.Stage != "" {
			job.Stage = p.Stage
		}
		if p.SheetsDone > 0 {
			job.Matches = append(job.Matches[:0], p.Matches...)
			job.SheetsDone = p.SheetsDone
			job.Scanned, job.Total = p.Rows, p.Rows
		}
	})
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Matches, job.Truncated = result.Matches, result.Truncated
	switch {
	case job.ctx.Err() != nil || errors.Is(err, context.Canceled):
		job.Status, job.Stage = "cancelled", "已取消"
	case err != nil:
		job.Status, job.Error = "error", err.Error()
	default:
		job.Status, job.Progress = "done", 100
		job.Stage = fmt.Sprintf("搜索完成 · 已检查 %d 个 Sheet", result.SheetsDone)
	}
}

func (a *App) handleSearchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.searchJobs[r.URL.Query().Get("id")]
	a.mu.RUnlock()
	if job == nil {
		writeError(w, http.StatusNotFound, "搜索任务不存在或已失效")
		return
	}
	job.mu.RLock()
	data := map[string]any{
		"id": job.ID, "sheet": job.Sheet, "query": job.Query, "mode": job.Mode, "scope": job.Scope,
		"status": job.Status, "stage": job.Stage, "progress": job.Progress, "scanned": job.Scanned, "durationMs": time.Since(job.Started).Milliseconds(),
		"total": job.Total, "sheetsDone": job.SheetsDone, "sheetsTotal": job.SheetsTotal,
		"matches": append([]workbook.SearchResult{}, job.Matches...), "truncated": job.Truncated, "error": job.Error,
		"taskId": job.TaskID,
	}
	if job.Full {
		data["full"], data["hitCount"] = true, job.HitCount
		if job.Status == "done" {
			data["downloadUrl"] = "/api/search/export?" + (url.Values{"id": {job.ID}, "ticket": {job.Ticket}, "session": {r.Header.Get("X-ExcelLens-Session")}}).Encode()
		}
	}
	job.mu.RUnlock()
	writeJSON(w, data)
}

func (a *App) handleSearchCancel(w http.ResponseWriter, r *http.Request) {
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
	a.mu.RLock()
	job := a.searchJobs[req.ID]
	a.mu.RUnlock()
	if job == nil {
		writeJSON(w, map[string]any{"cancelled": false})
		return
	}
	job.cancel()
	writeJSON(w, map[string]any{"cancelled": true})
}

func (a *App) registerQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/index/start", a.handleIndexStart)
	mux.HandleFunc("/api/index/status", a.handleIndexStatus)
	mux.HandleFunc("/api/index/cancel", a.handleIndexCancel)
	mux.HandleFunc("/api/filter/start", a.handleFilterStart)
	mux.HandleFunc("/api/filter/distinct", a.handleFilterDistinct)
	mux.HandleFunc("/api/filter/status", a.handleFilterStatus)
	mux.HandleFunc("/api/filter/cancel", a.handleFilterCancel)
	mux.HandleFunc("/api/filter/clear", a.handleFilterClear)
	mux.HandleFunc("/api/filter/locate", a.handleFilterLocate)
	mux.HandleFunc("/api/row-view/start", a.handleRowViewStart)
	mux.HandleFunc("/api/row-view/status", a.handleRowViewStatus)
	mux.HandleFunc("/api/row-view/cancel", a.handleRowViewCancel)
	mux.HandleFunc("/api/row-view/clear", a.handleRowViewClear)
	mux.HandleFunc("/api/row-view/groups", a.handleRowViewGroups)
	mux.HandleFunc("/api/row-view/locate", a.handleFilterLocate)
	mux.HandleFunc("/api/row-view", a.handleRowView)
	mux.HandleFunc("/api/search/start", a.handleSearchStart)
	mux.HandleFunc("/api/search/validate", a.handleSearchValidate)
	mux.HandleFunc("/api/search/history", a.handleSearchHistory)
	mux.HandleFunc("/api/search/status", a.handleSearchStatus)
	mux.HandleFunc("/api/search/page", a.handleSearchResults)
	mux.HandleFunc("/api/search/export", a.handleSearchResults)
	mux.HandleFunc("/api/search/cancel", a.handleSearchCancel)
}
