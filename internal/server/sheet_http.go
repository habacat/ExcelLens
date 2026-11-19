package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"excellens/internal/workbook"
)

func (a *App) handleSheets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	writeJSON(w, map[string]any{"sheets": wb.Sheets, "activeTab": wb.ActiveTab})
}

func (a *App) handleIndexStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Sheet string `json:"sheet"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	a.mu.RLock()
	maintenance := a.maintenance
	a.mu.RUnlock()
	if maintenance {
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	a.cacheMu.RLock()
	cachedIndex := wb.GetCachedIndex(meta.Path)
	a.cacheMu.RUnlock()
	if cachedIndex != nil {
		writeJSON(w, indexPayload(true, "", meta.Name, cachedIndex))
		return
	}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	if a.wb != wb {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "工作簿已切换，请重新选择工作表")
		return
	}
	if existingID := a.indexBySheet[meta.Path]; existingID != "" {
		if job := a.indexJobs[existingID]; job != nil {
			job.mu.RLock()
			active := (job.Status == "queued" || job.Status == "running") && job.ctx.Err() == nil
			job.mu.RUnlock()
			if active {
				a.mu.Unlock()
				writeJSON(w, map[string]any{"ready": false, "jobId": existingID})
				return
			}
		}
		delete(a.indexBySheet, meta.Path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &IndexJob{
		ID: randomID(), Sheet: meta.Name, Path: meta.Path, Status: "queued",
		Stage: "等待索引任务", Started: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{}),
	}
	a.indexJobs[job.ID] = job
	a.indexBySheet[meta.Path] = job.ID
	a.mu.Unlock()
	go a.runIndex(wb, meta, job)
	writeJSON(w, map[string]any{"ready": false, "jobId": job.ID})
}

func indexPayload(ready bool, jobID, sheet string, idx *workbook.SheetIndex) map[string]any {
	return map[string]any{
		"ready": ready, "jobId": jobID, "sheet": sheet,
		"dimension": idx.Dimension, "totalRows": idx.RowCount(), "maxRow": idx.MaxRow, "maxCol": idx.MaxCol,
	}
}

func (a *App) runIndex(wb *workbook.Workbook, meta workbook.SheetMeta, job *IndexJob) {
	defer close(job.done)
	select {
	case a.indexSem <- struct{}{}:
		defer func() { <-a.indexSem }()
	case <-job.ctx.Done():
		job.mu.Lock()
		job.Status = "cancelled"
		job.Stage = "已取消"
		job.mu.Unlock()
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = "正在解压并建立行索引"
	job.mu.Unlock()
	idx, err := wb.BuildIndex(meta, func(done, total int64, stage string) {
		job.mu.Lock()
		job.DoneBytes = done
		job.TotalBytes = total
		job.Stage = stage
		if total > 0 {
			job.Progress = float64(done) / float64(total) * 100
		}
		job.mu.Unlock()
	}, job.ctx.Done())
	job.mu.Lock()
	defer job.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || err.Error() == "index cancelled" || job.ctx.Err() != nil {
			job.Status = "cancelled"
			job.Stage = "已取消"
		} else {
			job.Status = "error"
			job.Stage = "索引失败"
			job.Error = err.Error()
		}
		return
	}
	job.Index = idx
	job.Progress = 100
	job.DoneBytes = job.TotalBytes
	job.Status = "done"
	job.Stage = "索引完成"
}

func (a *App) handleIndexStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	a.mu.RLock()
	job := a.indexJobs[id]
	a.mu.RUnlock()
	if job == nil {
		writeError(w, http.StatusNotFound, "索引任务不存在或已失效")
		return
	}
	job.mu.RLock()
	out := map[string]any{
		"id": job.ID, "sheet": job.Sheet, "status": job.Status, "stage": job.Stage,
		"progress": job.Progress, "doneBytes": job.DoneBytes, "totalBytes": job.TotalBytes,
		"error": job.Error,
	}
	if job.Index != nil {
		out["dimension"] = job.Index.Dimension
		out["totalRows"] = job.Index.RowCount()
		out["maxRow"] = job.Index.MaxRow
		out["maxCol"] = job.Index.MaxCol
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleIndexCancel(w http.ResponseWriter, r *http.Request) {
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
	job := a.indexJobs[req.ID]
	a.mu.RUnlock()
	if job == nil {
		writeJSON(w, map[string]any{"cancelled": false})
		return
	}
	job.cancel()
	writeJSON(w, map[string]any{"cancelled": true})
}

func (a *App) handleSheet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	a.mu.RLock()
	maintenance := a.maintenance
	a.mu.RUnlock()
	if maintenance {
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, http.StatusConflict, "工作表索引尚未建立")
		return
	}
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 100), 1, 150)
	startCol := clamp(parseIntDefault(r.URL.Query().Get("col"), 1), 1, 16384)
	colCount := clamp(parseIntDefault(r.URL.Query().Get("cols"), 24), 1, 80)
	var sparseCols []int
	if value := r.URL.Query().Get("columns"); value != "" {
		parts := strings.Split(value, ",")
		if len(parts) > 80 {
			writeError(w, 400, "可视列数量超限")
			return
		}
		previous := 0
		for _, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number <= previous || number > 16384 {
				writeError(w, 400, "可视列索引无效")
				return
			}
			sparseCols = append(sparseCols, number)
			previous = number
		}
		if len(sparseCols) == 0 {
			writeError(w, 400, "可视列不能为空")
			return
		}
		startCol = sparseCols[0]
		colCount = len(sparseCols)
	}
	var filtered *FilterResult
	filterID := strings.TrimSpace(r.URL.Query().Get("filterId"))
	var visible *sheetRowView
	if viewID := r.URL.Query().Get("viewId"); viewID != "" {
		var err error
		visible, err = a.rowViewSnapshot(viewID, wb, idx, meta.Path, filterID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer visible.close()
	}
	pinnedThrough := 0
	if r.URL.Query().Has("pinnedThroughRow") {
		value, err := strconv.Atoi(r.URL.Query().Get("pinnedThroughRow"))
		if visible == nil || err != nil || value < 1 || value > 30 || parseIntDefault(r.URL.Query().Get("excludeThroughRow"), 0) != 0 {
			writeError(w, 400, "冻结源行范围或可见行视图无效")
			return
		}
		pinnedThrough = value
	}
	if filterID != "" && visible == nil {
		filtered = a.filterSnapshot(filterID)
		defer filtered.close()
		if filtered == nil || filtered.SheetPath != meta.Path {
			writeError(w, http.StatusNotFound, "筛选结果已失效，请重新应用筛选条件")
			return
		}
	}
	totalRows := idx.RowCount()
	var rowView selectionRowView
	if filtered != nil || visible != nil {
		// Frozen title rows stay visible above the filtered scrollable body.
		through := clamp(parseIntDefault(r.URL.Query().Get("excludeThroughRow"), 0), 0, 30)
		if visible != nil && r.URL.Query().Has("excludeThroughRow") {
			value, err := strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
			if err != nil || value < 0 || value > 30 {
				writeError(w, 400, "冻结行参数无效")
				return
			}
			through = value
		}
		var viewErr error
		rowView, viewErr = newSelectionRowView(idx, filtered, through, visible)
		if viewErr != nil {
			writeError(w, 400, viewErr.Error())
			return
		}
		totalRows = rowView.count()
	}
	if pinnedThrough > 0 {
		totalRows = rowView.sourceRangeCount(1, pinnedThrough)
	}
	if rowStr := r.URL.Query().Get("row"); rowStr != "" {
		rowNum := clamp(parseIntDefault(rowStr, 1), 1, workbook.MaxRowsForIndex(idx))
		if filtered == nil && visible == nil {
			offset = idx.FindRowOffset(rowNum)
		} else {
			offset, _ = rowView.locate(rowNum)
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset > totalRows {
		offset = totalRows
	}
	end := offset + limit
	if end > totalRows {
		end = totalRows
	}
	lastCol := startCol + colCount - 1
	if lastCol > 16384 {
		lastCol = 16384
		colCount = lastCol - startCol + 1
	}
	var rows []workbook.RowData
	var err error
	readOptions := workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: r.URL.Query().Get("conditional") != "0"}
	if visible != nil {
		var numbers []int
		if pinnedThrough > 0 {
			first := 1
			if r.URL.Query().Has("row") {
				value, parseErr := strconv.Atoi(r.URL.Query().Get("row"))
				if parseErr != nil || value < 1 || value > pinnedThrough {
					writeError(w, 400, "冻结源行起点无效")
					return
				}
				first = value
			}
			offset = rowView.sourceRangeCount(1, first-1)
			numbers = rowView.appendSourceRange(make([]int, 0, min(limit, pinnedThrough)), first, min(limit, rowView.sourceRangeCount(first, pinnedThrough)))
		} else if filterID == "" && r.URL.Query().Get("rowWindow") == "1" {
			first := clamp(parseIntDefault(r.URL.Query().Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
			numbers = rowView.sourceWindow(first, limit)
		} else {
			numbers, err = rowView.numbersPage(offset, end-offset)
		}
		if err == nil {
			readOptions.Columns = sparseCols
			if len(sparseCols) > 0 {
				lastCol = sparseCols[len(sparseCols)-1]
			}
			rows, err = wb.ReadRowsByNumbersWithOptionsContext(r.Context(), idx, numbers, startCol, lastCol, readOptions)
		}
	} else if filtered == nil && r.URL.Query().Get("rowWindow") == "1" {
		firstRow := clamp(parseIntDefault(r.URL.Query().Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
		numbers := make([]int, 0, limit)
		for number := firstRow; number <= min(idx.MaxRow, firstRow+limit-1); number++ {
			numbers = append(numbers, number)
		}
		readOptions.Columns = sparseCols
		if len(sparseCols) > 0 {
			lastCol = sparseCols[len(sparseCols)-1]
		}
		rows, err = wb.ReadRowsByNumbersWithOptionsContext(r.Context(), idx, numbers, startCol, lastCol, readOptions)
	} else if len(sparseCols) > 0 {
		indices := make([]int, 0, end-offset)
		if filtered == nil {
			for i := offset; i < end; i++ {
				indices = append(indices, i)
			}
		} else {
			indices, err = rowView.page(offset, end-offset)
		}
		if err == nil {
			readOptions.Columns = sparseCols
			rows, err = wb.ReadRowsByIndicesWithOptionsContext(r.Context(), idx, indices, startCol, sparseCols[len(sparseCols)-1], readOptions)
		}
	} else if filtered == nil {
		rows, err = wb.ReadRowsWithOptionsContext(r.Context(), idx, offset, end-offset, startCol, lastCol, readOptions)
	} else {
		var indices []int
		indices, err = rowView.page(offset, end-offset)
		if err == nil {
			rows, err = wb.ReadRowsByIndicesWithOptionsContext(r.Context(), idx, indices, startCol, lastCol, readOptions)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeError(w, http.StatusInternalServerError, "读取单元格失败："+err.Error())
		return
	}
	// Hyperlinks are metadata, not cell values. Decode them lazily and
	// decorate only the current viewport; failures must not hide sheet data.
	linkError := ""
	if err := wb.DecorateHyperlinks(meta, rows, sparseCols, startCol, lastCol); err != nil {
		linkError = err.Error()
	}
	payload := map[string]any{
		"hyperlinkError": linkError,
		"sheet":          meta.Name, "dimension": idx.Dimension,
		"offset": offset, "limit": limit, "totalRows": totalRows,
		"totalRowsOriginal": idx.RowCount(), "filtered": filterID != "", "filterId": filterID,
		"maxRow": idx.MaxRow, "maxCol": idx.MaxCol,
		"startCol": startCol, "colCount": colCount, "columns": sparseCols,
		"rows": rows,
	}
	if visible != nil {
		payload["viewId"] = visible.data.id
	}
	if pinnedThrough > 0 {
		payload["pinnedThroughRow"] = pinnedThrough
	}
	if r.URL.Query().Get("compact") == "1" {
		payload["rows"] = packViewportRows(rows)
		payload["compact"] = 1
	}
	writeJSON(w, payload)
}

// handleFilterLocate maps an actual Excel row number to its current visible
// position. This keeps search-result navigation correct under active filters.
