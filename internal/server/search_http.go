package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"excellens/internal/formats"
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
		ID: randomID(), Sheet: meta.Name, Query: req.Query, Mode: req.Mode, Scope: req.Scope,
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
		same := searchCacheKey(existing.Sheet, existing.Query, existing.Mode, existing.Scope) == searchCacheKey(meta.Name, req.Query, req.Mode, req.Scope)
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
	if cached, err := loadSearchCache(wb, meta.Name, req.Query, req.Mode, req.Scope); err == nil {
		if !pruneSearchJobsLocked(a.searchJobs, formats.QueryTaskHistory) {
			a.mu.Unlock()
			cancel()
			writeError(w, http.StatusTooManyRequests, "正在回收搜索任务，请稍后重试")
			return
		}
		close(job.done)
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
	a.retainSubmitted(job.done)
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
		_ = saveSearchCache(wb, job) // Search completion also persists its on-disk index.
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
	if job.checkpoint != nil {
		a.runCheckpointSearch(wb, job)
		return
	}
	if job.Scope == "all" {
		a.runWorkbookSearch(wb, job)
		return
	}
	search := func(idx *workbook.SheetIndex, sheet, query, mode string, limit int, done <-chan struct{}, update func(int, int, []workbook.SearchResult)) ([]workbook.SearchResult, bool, error) {
		if mode == "advanced" {
			return wb.SearchSheetAdvanced(idx, sheet, query, limit, done, update)
		}
		return wb.SearchSheet(idx, sheet, query, mode, limit, done, update)
	}
	matches, truncated, err := search(currentIndex, current.Name, job.Query, job.Mode, 500, job.ctx.Done(), func(scanned, total int, currentMatches []workbook.SearchResult) {
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

// runWorkbookSearch overlaps worksheet indexing and scanning. At most two
// workers run concurrently to avoid flooding the disk with large extracted XML
// files. Results are merged in workbook order, not worker completion order.
func (a *App) runWorkbookSearch(wb *workbook.Workbook, job *SearchJob) {
	sheets := wb.Sheets
	if len(sheets) == 0 {
		job.mu.Lock()
		job.Status = "done"
		job.Progress = 100
		job.mu.Unlock()
		return
	}
	const maxHits = 500
	workerCount := 2
	if len(sheets) < workerCount {
		workerCount = len(sheets)
	}
	ctx, cancelWork := context.WithCancel(job.ctx)
	defer cancelWork()
	type sheetResult struct {
		position  int
		matches   []workbook.SearchResult
		truncated bool
		err       error
		rows      int
	}
	tasks := make(chan int)
	results := make(chan sheetResult, workerCount)
	var workers sync.WaitGroup
	job.mu.Lock()
	job.SheetsTotal = len(sheets)
	job.Stage = fmt.Sprintf("准备搜索全部 %d 个 Sheet", len(sheets))
	job.mu.Unlock()
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for position := range tasks {
				if ctx.Err() != nil {
					return
				}
				meta := sheets[position]
				report := func(stage string, ratio float64) {
					job.mu.Lock()
					job.Stage = fmt.Sprintf("%s %d/%d · %s", stage, position+1, len(sheets), meta.Name)
					// Progress must be monotonic while concurrent sheets finish.
					estimate := (float64(position) + ratio) / float64(len(sheets)) * 100
					if estimate > job.Progress {
						job.Progress = estimate
					}
					job.mu.Unlock()
				}
				idx := wb.GetCachedIndex(meta.Path)
				var err error
				if idx == nil {
					report("建立索引", 0)
					releaseIndex, admissionErr := a.backgroundIndex(ctx)
					if admissionErr != nil {
						return
					}
					// Another worker or interactive request may have just finished.
					idx = wb.GetCachedIndex(meta.Path)
					if idx == nil {
						idx, err = wb.BuildIndex(meta, func(done, total int64, stage string) {
							if total > 0 {
								report("正在索引", .4*float64(done)/float64(total))
							}
						}, ctx.Done())
					}
					releaseIndex()
				}
				if err != nil {
					select {
					case results <- sheetResult{position: position, err: fmt.Errorf("建立 Sheet「%s」索引失败：%w", meta.Name, err)}:
					case <-ctx.Done():
					}
					return
				}
				report("搜索", .4)
				last := time.Now()
				search := func(idx *workbook.SheetIndex, sheet, query, mode string, limit int, done <-chan struct{}, update func(int, int, []workbook.SearchResult)) ([]workbook.SearchResult, bool, error) {
					if mode == "advanced" {
						return wb.SearchSheetAdvanced(idx, sheet, query, limit, done, update)
					}
					return wb.SearchSheet(idx, sheet, query, mode, limit, done, update)
				}
				hits, truncated, err := search(idx, meta.Name, job.Query, job.Mode, maxHits, ctx.Done(), func(scanned, total int, _ []workbook.SearchResult) {
					if time.Since(last) > 250*time.Millisecond {
						if total > 0 {
							report("搜索中", .4+.6*float64(scanned)/float64(total))
						}
						last = time.Now()
					}
				})
				result := sheetResult{position: position, matches: hits, truncated: truncated, err: err, rows: idx.RowCount()}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(tasks)
		for i := range sheets {
			select {
			case tasks <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	// Merge by original order. A fast later Sheet must never reorder results
	// or push an earlier matching cell beyond the first 500 matches.
	pending := make(map[int]sheetResult, workerCount)
	all := make([]workbook.SearchResult, 0, 64)
	doneCount := 0
	scannedRows := 0
	truncated := false
	var firstErr error
	for next := 0; next < len(sheets); {
		var item sheetResult
		var ok bool
		if item, ok = pending[next]; ok {
			delete(pending, next)
		} else {
			select {
			case item = <-results:
			case <-ctx.Done():
				firstErr = ctx.Err()
				break
			}
			if firstErr != nil {
				break
			}
			if item.position != next {
				pending[item.position] = item
				continue
			}
		}
		if item.err != nil {
			firstErr = item.err
			break
		}
		scannedRows += item.rows
		remaining := maxHits - len(all)
		if remaining > 0 && len(item.matches) > 0 {
			take := len(item.matches)
			if take > remaining {
				take = remaining
			}
			all = append(all, item.matches[:take]...)
		}
		doneCount++
		next++
		job.mu.Lock()
		job.Matches = append(job.Matches[:0], all...)
		job.SheetsDone = doneCount
		job.Scanned = scannedRows
		job.Total = scannedRows
		progress := float64(doneCount) / float64(len(sheets)) * 100
		if progress > job.Progress {
			job.Progress = progress
		}
		job.mu.Unlock()
		if len(all) >= maxHits {
			truncated = item.truncated || next < len(sheets)
			break
		}
	}
	cancelWork() // Cancel pending operations and finish before returning to caller.
	workers.Wait()
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.ctx.Err() != nil || errors.Is(firstErr, context.Canceled) {
		job.Status = "cancelled"
		job.Stage = "已取消"
	} else if firstErr != nil {
		job.Status = "error"
		job.Error = firstErr.Error()
	} else {
		job.Status = "done"
		job.Progress = 100
		job.Stage = fmt.Sprintf("搜索完成 · 已检查 %d 个 Sheet", doneCount)
	}
	job.Matches = all
	job.Truncated = truncated
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
	mux.HandleFunc("/api/search/cancel", a.handleSearchCancel)
}
