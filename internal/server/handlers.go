package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"excellens/internal/archives"
	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/ui"
	"excellens/internal/workbook"
)

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := ui.Index(a.token)
	if err != nil {
		http.Error(w, "界面资源不可用", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *App) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	b, contentType, err := ui.ReadAsset(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

func (a *App) currentWorkbook() *workbook.Workbook {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.wb
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		a.mu.RLock()
		startupError := a.startupError
		a.mu.RUnlock()
		writeJSON(w, map[string]any{"version": buildinfo.Version, "opened": false, "error": startupError, "archive": a.archiveSummary()})
		return
	}
	st, _ := os.Stat(wb.Path)
	var size int64
	if st != nil {
		size = st.Size()
	}
	writeJSON(w, map[string]any{
		"version":     buildinfo.Version,
		"opened":      true,
		"workbookKey": wb.Fingerprint,
		"book":        sourceBookID(wb),
		"fileName":    workbookDisplayName(wb),
		"fileSize":    size,
		"sheetCount":  len(wb.Sheets),
		"activeTab":   wb.ActiveTab,
		"sheets":      wb.Sheets,
		"source":      wb.SourceInfo,
		"archive":     a.archiveSummary(),
	})
}

// handleStyles sends the resolved, compact visual-style palette once per workbook.
// /api/sheet continues transmitting only the original style ID per cell.
func (a *App) handleStyles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	writeJSON(w, map[string]any{"styles": wb.VisualStyles, "richTextNotice": wb.RichTextNotice()})
}

func workbookDisplayName(wb *workbook.Workbook) string {
	if wb.DisplayName != "" {
		return wb.DisplayName
	}
	return filepath.Base(wb.Path)
}

func (a *App) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	const maxFileSize = formats.MaxUploadBytes
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+(64<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "读取文件失败："+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请先选择 Excel、CSV、TXT 或 SQLite 数据文件")
		return
	}
	defer f.Close()
	if h.Size <= 0 {
		writeError(w, http.StatusBadRequest, "文件为空")
		return
	}
	if h.Size > maxFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "文件超过 1 TiB 上传限制；超大本机文件可直接通过右键菜单或命令行打开")
		return
	}
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if !workbook.SupportedExtensions(ext) {
		writeError(w, http.StatusBadRequest, "请选择受支持的数据文件或 ZIP、7z、TAR、GZ、BZ2、XZ 压缩包")
		return
	}
	a.cacheMu.RLock()
	tmpPath, err := persistUploadedFile(f, ext, h.Size)
	if err != nil {
		a.cacheMu.RUnlock()
		writeError(w, http.StatusInternalServerError, "保存本机临时文件失败："+err.Error())
		return
	}
	releaseUpload := a.pinUploadPath(tmpPath)
	a.cacheMu.RUnlock()
	defer releaseUpload()
	if r.FormValue("reconcileSource") == "1" {
		a.handleReconcileUploaded(w, r, tmpPath, filepath.Base(h.Filename))
		return
	}
	if archives.IsPath(h.Filename) {
		if err := a.openArchive(r.Context(), tmpPath, filepath.Base(h.Filename), 0); err != nil {
			_ = os.Remove(tmpPath)
			writeError(w, 400, "无法读取压缩包："+err.Error())
			return
		}
		writeJSON(w, map[string]any{"archiveOnly": true, "opened": false, "archive": a.archiveSummary()})
		return
	}
	a.closeArchive()
	wb, releaseBook, err := a.openPinnedWorkbook(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeError(w, http.StatusBadRequest, "无法打开工作簿："+err.Error())
		return
	}
	defer releaseBook()
	wb.SetDisplayName(filepath.Base(h.Filename))
	a.replaceWorkbook(wb, tmpPath)
	a.recordRecent(wb)
	writeJSON(w, map[string]any{
		"opened":      true,
		"workbookKey": wb.Fingerprint,
		"book":        sourceBookID(wb),
		"fileName":    filepath.Base(h.Filename),
		"fileSize":    h.Size,
		"sheetCount":  len(wb.Sheets),
		"activeTab":   wb.ActiveTab,
		"sheets":      wb.Sheets,
		"source":      wb.SourceInfo,
	})
}

func persistUploadedFile(src interface {
	Read([]byte) (int, error)
	Close() error
}, ext string, expected int64) (string, error) {
	if diskFile, ok := src.(*os.File); ok {
		sourcePath := diskFile.Name()
		_ = diskFile.Close()
		if err := os.MkdirAll(workbook.UploadRoot(), 0700); err != nil {
			return "", err
		}
		targetPath := filepath.Join(workbook.UploadRoot(), "ExcelLens-upload-"+randomID()+ext)
		if err := os.Rename(sourcePath, targetPath); err == nil {
			return targetPath, nil
		}
		reopened, err := os.Open(sourcePath)
		if err != nil {
			return "", err
		}
		src = reopened
	}
	defer src.Close()
	if err := os.MkdirAll(workbook.UploadRoot(), 0700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(workbook.UploadRoot(), "ExcelLens-upload-*"+ext)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	buffer := make([]byte, 1<<20)
	copied, copyErr := io.CopyBuffer(tmp, src, buffer)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || copied != expected {
		_ = os.Remove(tmpPath)
		switch {
		case copyErr != nil:
			return "", copyErr
		case syncErr != nil:
			return "", syncErr
		case closeErr != nil:
			return "", closeErr
		default:
			return "", fmt.Errorf("文件复制长度不一致：期望 %d，实际 %d", expected, copied)
		}
	}
	return tmpPath, nil
}

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
	a.distinctMu.Lock()
	cached, found := a.distinctCache[key]
	a.distinctMu.Unlock()
	if found {
		writeJSON(w, map[string]any{"values": cached.Items, "truncated": cached.Truncated, "cached": true})
		return
	}
	a.cacheMu.RLock()
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
	if a.currentWorkbook() == wb {
		a.distinctMu.Lock()
		if len(a.distinctCache) >= 64 {
			a.distinctCache = make(map[string]DistinctCache)
		}
		a.distinctCache[key] = DistinctCache{Items: items, Truncated: truncated}
		a.distinctMu.Unlock()
	}
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
	if a.querySem != nil {
		select {
		case a.querySem <- struct{}{}:
			defer func() { <-a.querySem }()
		case <-job.ctx.Done():
			job.mu.Lock()
			job.Status = "cancelled"
			job.Stage = "已取消"
			job.mu.Unlock()
			return
		}
	}
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
	if a.querySem != nil {
		select {
		case a.querySem <- struct{}{}:
			defer func() { <-a.querySem }()
		case <-job.ctx.Done():
			job.mu.Lock()
			job.Status = "cancelled"
			job.Stage = "已取消"
			job.mu.Unlock()
			return
		}
	}
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
					select {
					case a.indexSem <- struct{}{}:
					case <-ctx.Done():
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
					<-a.indexSem
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

func (a *App) handleCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeJSON(w, map[string]any{"opened": false, "size": 0})
		return
	}
	a.cacheMu.RLock()
	size, files := directorySize(wb.CacheDir)
	a.cacheMu.RUnlock()
	writeJSON(w, map[string]any{"opened": true, "size": size, "files": files})
}

func (a *App) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) {
		writeError(w, http.StatusConflict, "当前工作簿仍有在用索引或查询文件，请退出程序后清理本机缓存；在用数据不会被删除")
		return
	}
	if a.otherTabUsesCache(wb.CacheDir) {
		writeError(w, http.StatusConflict, "其他页面、可见行视图或核对任务仍在使用此来源，请先关闭或清除相应结果")
		return
	}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "缓存维护正在进行")
		return
	}
	a.maintenance = true
	indexes := make([]*IndexJob, 0, len(a.indexJobs))
	searches := make([]*SearchJob, 0, len(a.searchJobs))
	for _, job := range a.indexJobs {
		job.cancel()
		indexes = append(indexes, job)
	}
	for _, job := range a.searchJobs {
		job.cancel()
		searches = append(searches, job)
	}
	a.mu.Unlock()
	waitJobs(indexes, searches)
	a.cacheMu.Lock()
	var clearErr error
	conflict := false
	if a.currentWorkbook() != wb {
		clearErr, conflict = errors.New("工作簿已切换，缓存清理已取消"), true
	} else if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) || a.otherTabUsesCache(wb.CacheDir) {
		clearErr, conflict = errors.New("缓存仍有在用索引或查询结果，未执行清理"), true
	} else {
		clearErr = os.RemoveAll(wb.CacheDir)
		if clearErr == nil {
			clearErr = os.MkdirAll(wb.CacheDir, 0700)
		}
	}
	if clearErr == nil {
		wb.ClearIndexes()
	}
	a.cacheMu.Unlock()
	a.mu.Lock()
	if a.wb == wb {
		a.indexJobs = make(map[string]*IndexJob)
		a.indexBySheet = make(map[string]string)
		a.searchJobs = make(map[string]*SearchJob)
	}
	a.maintenance = false
	a.mu.Unlock()
	if clearErr != nil {
		if conflict {
			writeError(w, http.StatusConflict, clearErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "清理缓存失败："+clearErr.Error())
		return
	}
	writeJSON(w, map[string]any{"cleared": true})
}

func (a *App) handleExit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"exiting": true})
	go func() {
		time.Sleep(120 * time.Millisecond)
		a.rootApp().Shutdown()
	}()
}

func directorySize(root string) (int64, int) {
	var size int64
	files := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			size += info.Size()
			files++
		}
		return nil
	})
	return size, files
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "请求方法不允许")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": message})
}

func parseIntDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
