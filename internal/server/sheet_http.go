package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) handleSheets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.sheetHttp.noWorkbookOpen")
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
	if err := decodeControlRequest(w, r, 8<<10, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "service.sheetHttp.requestedContentInvalid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.sheetHttp.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.sheetHttp.unableFindSpecifiedWorksheet")
		return
	}
	a.mu.RLock()
	maintenance := a.maintenance
	a.mu.RUnlock()
	if maintenance {
		writeMessage(w, http.StatusConflict, "service.sheetHttp.localCacheBeingMaintainedTry")
		return
	}
	a.cacheMu.RLock()
	cachedIndex := wb.GetCachedIndex(meta.Path)
	ready := cachedIndex != nil
	a.cacheMu.RUnlock()
	if ready {
		writeJSON(w, indexPayload(true, "", meta.Name, cachedIndex))
		return
	}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeMessage(w, http.StatusConflict, "service.sheetHttp.localCacheBeingMaintainedTry")
		return
	}
	if a.wb != wb {
		a.mu.Unlock()
		writeMessage(w, http.StatusConflict, "service.sheetHttp.workbookSwitchedChooseNewWorksheet")
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
		Stage: "stage.task.waitingForIndexTasks", Started: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{}),
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
		job.Stage = "stage.task.cancelled"
		job.mu.Unlock()
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = "stage.task.decompressingEstablishingRowIndices"
	job.mu.Unlock()
	update := func(done, total int64, stage string) {
		job.mu.Lock()
		job.DoneBytes = done
		job.TotalBytes = total
		job.Stage = stage
		if total > 0 {
			job.Progress = float64(done) / float64(total) * 100
		}
		job.mu.Unlock()
	}
	idx, err := wb.BuildIndex(meta, update, job.ctx.Done())
	job.mu.Lock()
	defer job.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || err.Error() == "index cancelled" || job.ctx.Err() != nil {
			job.Status = "cancelled"
			job.Stage = "stage.task.cancelled"
		} else {
			job.Status = "error"
			job.Stage = "stage.task.indexFailure"
			job.Error = err.Error()
		}
		return
	}
	job.Index = idx
	job.Progress = 100
	job.DoneBytes = job.TotalBytes
	job.Status = "done"
	job.Stage = "stage.task.indexComplete"
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
		writeMessage(w, http.StatusNotFound, "service.sheetHttp.indexTaskDoesNotExist")
		return
	}
	job.mu.RLock()
	out := map[string]any{
		"id": job.ID, "sheet": job.Sheet, "status": job.Status, "stage": responseLocale(w).Stage(job.Stage),
		"progress": job.Progress, "doneBytes": job.DoneBytes, "totalBytes": job.TotalBytes,
		"error": job.Error,
	}
	if job.TotalRows > 0 && job.Status == "running" {
		out["statisticsProgress"] = map[string]int{"done": job.DoneRows, "total": job.TotalRows}
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
	if err := decodeControlRequest(w, r, 4096, &req); err != nil {
		writeMessage(w, http.StatusBadRequest, "service.sheetHttp.requestedContentInvalid")
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
		writeMessage(w, http.StatusConflict, "service.sheetHttp.noWorkbookOpen")
		return
	}
	a.mu.RLock()
	maintenance := a.maintenance
	a.mu.RUnlock()
	if maintenance {
		writeMessage(w, http.StatusConflict, "service.sheetHttp.localCacheBeingMaintainedTry")
		return
	}
	query := r.URL.Query()
	meta, ok := wb.FindSheet(query.Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.sheetHttp.unableFindSpecifiedWorksheet")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx, integrityErr := wb.CachedIndexForRead(r.Context(), meta.Path)
	if integrityErr != nil {
		writeFailure(w, http.StatusConflict, integrityErr)
		return
	}
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.sheetHttp.worksheetIndexNotCreatedYet")
		return
	}
	offset := parseIntDefault(query.Get("offset"), 0)
	limit := clamp(parseIntDefault(query.Get("limit"), 100), 1, 150)
	startCol := clamp(parseIntDefault(query.Get("col"), 1), 1, formats.ExcelColumns)
	colCount := clamp(parseIntDefault(query.Get("cols"), 24), 1, 80)
	var sparseCols []int
	if value := query.Get("columns"); value != "" {
		parts := strings.Split(value, ",")
		if len(parts) > 80 {
			writeMessage(w, 400, "service.sheetHttp.numberVisibleColumnsExceedsLimit")
			return
		}
		previous := 0
		for _, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number <= previous || number > formats.ExcelColumns {
				writeMessage(w, 400, "service.sheetHttp.indexVisibleColumnsInvalid")
				return
			}
			sparseCols = append(sparseCols, number)
			previous = number
		}
		if len(sparseCols) == 0 {
			writeMessage(w, 400, "service.sheetHttp.visualColumnsCannotBeEmpty")
			return
		}
		startCol = sparseCols[0]
		colCount = len(sparseCols)
	}
	var filtered *FilterResult
	filterID := strings.TrimSpace(query.Get("filterId"))
	var visible *sheetRowView
	if viewID := query.Get("viewId"); viewID != "" {
		var err error
		visible, err = a.rowViewSnapshot(viewID, wb, idx, meta.Path, filterID)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		defer visible.close()
	}
	pinnedThrough := 0
	if query.Has("pinnedThroughRow") {
		value, err := strconv.Atoi(query.Get("pinnedThroughRow"))
		if visible == nil || err != nil || value < 1 || value > 30 || parseIntDefault(query.Get("excludeThroughRow"), 0) != 0 {
			writeMessage(w, 400, "service.sheetHttp.sourceRowRangeFrozenOr")
			return
		}
		pinnedThrough = value
	}
	if filterID != "" && visible == nil {
		filtered = a.filterSnapshot(filterID)
		defer filtered.close()
		if filtered == nil || filtered.SheetPath != meta.Path {
			writeMessage(w, http.StatusNotFound, "service.filter.expired")
			return
		}
	}
	totalRows := idx.RowCount()
	var rowView selectionRowView
	if filtered != nil || visible != nil {
		// Frozen title rows stay visible above the filtered scrollable body.
		through := clamp(parseIntDefault(query.Get("excludeThroughRow"), 0), 0, 30)
		if visible != nil && query.Has("excludeThroughRow") {
			value, err := strconv.Atoi(query.Get("excludeThroughRow"))
			if err != nil || value < 0 || value > 30 {
				writeMessage(w, 400, "service.sheetHttp.frozenRowParametersInvalid")
				return
			}
			through = value
		}
		var viewErr error
		rowView, viewErr = newSelectionRowView(idx, filtered, through, visible)
		if viewErr != nil {
			writeFailure(w, 400, viewErr)
			return
		}
		totalRows = rowView.count()
	}
	var pinnedBorderNeighbor []int
	if pinnedThrough > 0 {
		totalRows = rowView.sourceRangeCount(1, pinnedThrough)
		if query.Get("pinnedBorderNeighbor") == "1" {
			// Read one adjacent source row without extending the frozen prefix.
			pinnedBorderNeighbor = rowView.sourceWindow(pinnedThrough+1, 1)
			totalRows += len(pinnedBorderNeighbor)
		}
	}
	if rowStr := query.Get("row"); rowStr != "" {
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
	if lastCol > formats.ExcelColumns {
		lastCol = formats.ExcelColumns
		colCount = lastCol - startCol + 1
	}
	var rows []workbook.RowData
	var err error
	conditionalOn := query.Get("conditional") != "0"
	readOptions := workbook.RowReadOptions{
		FormatValue:              true,
		IncludeFormula:           true,
		RichText:                 true,
		ConditionalFormats:       conditionalOn,
		ConditionalStatsReadOnly: conditionalOn,
	}
	if conditionalOn {
		readOptions.ConditionalStatsStatus = a.conditionalStatsSheetStatus(meta.Path)
	}
	if visible != nil {
		var numbers []int
		if pinnedThrough > 0 {
			first := 1
			if query.Has("row") {
				value, parseErr := strconv.Atoi(query.Get("row"))
				if parseErr != nil || value < 1 || value > pinnedThrough {
					writeMessage(w, 400, "service.sheetHttp.freezingSourceRowStartPoint")
					return
				}
				first = value
			}
			offset = rowView.sourceRangeCount(1, first-1)
			numbers = rowView.appendSourceRange(make([]int, 0, min(limit, pinnedThrough)), first, min(limit, rowView.sourceRangeCount(first, pinnedThrough)))
			if len(numbers) < limit {
				numbers = append(numbers, pinnedBorderNeighbor...)
			}
		} else if filterID == "" && query.Get("rowWindow") == "1" {
			first := clamp(parseIntDefault(query.Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
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
	} else if filtered == nil && query.Get("rowWindow") == "1" {
		firstRow := clamp(parseIntDefault(query.Get("row"), 1), 1, workbook.MaxRowsForIndex(idx))
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
		writeMessage(w, http.StatusInternalServerError, "service.sheetHttp.failedReadCell", i18n.Args{"detail": err})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	// Hyperlinks are metadata, not cell values. Decode them lazily and
	// decorate only the current viewport; failures must not hide sheet data.
	linkError := ""
	if err := wb.DecorateHyperlinks(meta, rows, sparseCols, startCol, lastCol); err != nil {
		linkError = err.Error()
	}
	if r.Context().Err() != nil {
		return
	}
	conditionalStatsPending := false
	if conditionalOn {
	pendingCheck:
		for i := range rows {
			for j := range rows[i].Cells {
				if rows[i].Cells[j].Conditional != nil && rows[i].Cells[j].Conditional.Pending {
					conditionalStatsPending = true
					break pendingCheck
				}
			}
		}
	}
	header := ViewportHeader{
		Sheet: meta.Name, Dimension: idx.Dimension, Offset: offset, Limit: limit, TotalRows: totalRows, TotalRowsOriginal: idx.RowCount(),
		Filtered: filterID != "", FilterID: filterID, MaxRow: idx.MaxRow, MaxCol: idx.MaxCol, StartCol: startCol, ColCount: colCount,
		Columns: sparseCols, HyperlinkError: linkError, ConditionalStatsPending: conditionalStatsPending, PinnedThroughRow: pinnedThrough,
	}
	if visible != nil {
		header.ViewID = visible.data.id
	}
	if query.Get("compact") == "1" {
		header.Compact = 1
		writeJSON(w, viewportResponse[CompactRow]{header, packViewportRows(rows)})
		return
	}
	writeJSON(w, viewportResponse[workbook.RowData]{header, rows})
}

// handleFilterLocate maps an actual Excel row number to its current visible
// position. This keeps search-result navigation correct under active filters.
