package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type inspectRequest struct {
	analysisSelectionCoordinates
	Sheet          string `json:"sheet"`
	Mode           string `json:"mode"`
	Start          string `json:"start"`
	End            string `json:"end"`
	FilterID       string `json:"filterId"`
	Limit          int    `json:"limit"`
	SkipThroughRow int    `json:"skipThroughRow,omitempty"`
}

func (a *App) handleInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var request inspectRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "检查条件无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(request.Sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "工作表不存在")
		return
	}
	startCol, startRow, err1 := workbook.ParseCellRef(request.Start)
	endCol, endRow, err2 := workbook.ParseCellRef(request.End)
	selectionStart, selectionEnd := startRow, endRow
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "检查范围地址无效")
		return
	}
	if startRow > endRow {
		startRow, endRow = endRow, startRow
	}
	if startCol > endCol {
		startCol, endCol = endCol, startCol
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	limit := request.Limit
	if limit == 0 {
		limit = 600
	}
	if limit < 1 || limit > 2500 {
		writeError(w, http.StatusBadRequest, "结果数量限制无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.currentWorkbook() != wb {
		writeError(w, 409, "工作簿已切换")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, http.StatusConflict, "工作表索引未准备好")
		return
	}
	if mode == "duplicates" && request.FilterID == "" && request.ViewID == "" && endRow-startRow+1 > formats.LargeDataRows {
		writeError(w, http.StatusBadRequest, "超大范围不支持整列查重，请先选取不超过500万行的区域")
		return
	}
	opt := workbook.InspectOptions{Mode: mode, FirstRow: startRow, LastRow: endRow,
		FirstCol: startCol, LastCol: endCol, Limit: limit}
	visual := request.ViewID != "" || request.VisualStart != 0 || request.VisualEnd != 0 || request.ExcludeThroughRow != 0
	if request.FilterID != "" || visual {
		selection, err := a.selectionSnapshot(wb, idx, meta.Path, request.FilterID, request.ViewID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		defer selection.close()
		numbers, err := analysisSelectionRows(idx, selection.filter, selectionStart, selectionEnd, request.analysisSelectionCoordinates, 12_000_000/(endCol-startCol+1), selection.visible)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if request.SkipThroughRow < 0 || request.SkipThroughRow > workbook.MaxRowsForIndex(idx) {
			writeError(w, 400, "表头行无效")
			return
		}
		kept := numbers[:0]
		for _, number := range numbers {
			if number > request.SkipThroughRow {
				kept = append(kept, number)
			}
		}
		numbers = kept
		if mode == "duplicates" && len(numbers) > formats.LargeDataRows {
			writeError(w, 400, "超大范围不支持整列查重，请先选取不超过500万行的区域")
			return
		}
		sort.Ints(numbers)
		opt.RowNumbers = numbers
		if len(numbers) > 0 {
			opt.FirstRow, opt.LastRow = numbers[0], numbers[len(numbers)-1]
		}
	}
	// Limit simultaneous large worksheet scans without blocking grid reads.
	select {
	case a.inspectSem <- struct{}{}:
		defer func() { <-a.inspectSem }()
	case <-r.Context().Done():
		return
	}
	started := time.Now()
	result, err := wb.InspectSelection(r.Context(), idx, opt)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"sheet": meta.Name, "start": request.Start, "end": request.End,
		"mode": mode, "matches": result.Matches, "matched": result.Matched,
		"scanned": result.Scanned, "duplicateGroups": result.DuplicateGroups,
		"truncated": result.Truncated, "durationMs": time.Since(started).Milliseconds(),
	})
}
