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
		writeMessage(w, http.StatusBadRequest, "service.inspect.invalidCheckConditions")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.inspect.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(request.Sheet)
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.inspect.worksheetDoesNotExist")
		return
	}
	startCol, startRow, err1 := workbook.ParseCellRef(request.Start)
	endCol, endRow, err2 := workbook.ParseCellRef(request.End)
	selectionStart, selectionEnd := startRow, endRow
	if err1 != nil || err2 != nil {
		writeMessage(w, http.StatusBadRequest, "service.inspect.invalidVerificationScopeAddress")
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
		writeMessage(w, http.StatusBadRequest, "service.inspect.invalidResultQuantityLimit")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.currentWorkbook() != wb {
		writeMessage(w, 409, "service.inspect.workbookSwitched")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.inspect.worksheetIndexNotReady")
		return
	}
	if mode == "duplicates" && request.FilterID == "" && request.ViewID == "" && endRow-startRow+1 > formats.LargeDataRows {
		writeMessage(w, http.StatusBadRequest, "service.inspect.largeRangesDoNotSupport")
		return
	}
	opt := workbook.InspectOptions{Mode: mode, FirstRow: startRow, LastRow: endRow,
		FirstCol: startCol, LastCol: endCol, Limit: limit}
	visual := request.ViewID != "" || request.VisualStart != 0 || request.VisualEnd != 0 || request.ExcludeThroughRow != 0
	if request.FilterID != "" || visual {
		selection, err := a.selectionSnapshot(wb, idx, meta.Path, request.FilterID, request.ViewID)
		if err != nil {
			writeFailure(w, http.StatusConflict, err)
			return
		}
		defer selection.close()
		numbers, err := analysisSelectionRows(idx, selection.filter, selectionStart, selectionEnd, request.analysisSelectionCoordinates, 12_000_000/(endCol-startCol+1), selection.visible)
		if err != nil {
			writeFailure(w, 400, err)
			return
		}
		if request.SkipThroughRow < 0 || request.SkipThroughRow > workbook.MaxRowsForIndex(idx) {
			writeMessage(w, 400, "service.inspect.headerRowInvalid")
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
			writeMessage(w, 400, "service.inspect.largeRangesDoNotSupport")
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
		writeFailure(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{
		"sheet": meta.Name, "start": request.Start, "end": request.End,
		"mode": mode, "matches": result.Matches, "matched": result.Matched,
		"scanned": result.Scanned, "duplicateGroups": result.DuplicateGroups,
		"truncated": result.Truncated, "durationMs": time.Since(started).Milliseconds(),
	})
}

func (a *App) registerInspectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/catalogue/start", a.handleCatalogueStart)
	mux.HandleFunc("/api/catalogue/status", a.handleCatalogueStatus)
	mux.HandleFunc("/api/catalogue/cancel", a.handleCatalogueCancel)
	mux.HandleFunc("/api/catalogue/clear", a.handleCatalogueClear)
	mux.HandleFunc("/api/catalogue/items", a.handleCatalogueItems)
	mux.HandleFunc("/api/catalogue/detail", a.handleCatalogueDetail)
	mux.HandleFunc("/api/catalogue/resolve", a.handleCatalogueResolve)
	mux.HandleFunc("/api/data-rules/start", a.handleDataRuleStart)
	mux.HandleFunc("/api/data-rules/status", a.handleDataRuleStatus)
	mux.HandleFunc("/api/data-rules/cancel", a.handleDataRuleCancel)
	mux.HandleFunc("/api/data-rules/clear", a.handleDataRuleClear)
	mux.HandleFunc("/api/data-rules/items", a.handleDataRuleItems)
	mux.HandleFunc("/api/data-rules/detail", a.handleDataRuleDetail)
	mux.HandleFunc("/api/data-rules/resolve", a.handleDataRuleResolve)
	mux.HandleFunc("/api/printing/start", a.handlePrintStart)
	mux.HandleFunc("/api/printing/status", a.handlePrintStatus)
	mux.HandleFunc("/api/printing/cancel", a.handlePrintCancel)
	mux.HandleFunc("/api/printing/clear", a.handlePrintClear)
	mux.HandleFunc("/api/printing/summary", a.handlePrintSummary)
	mux.HandleFunc("/api/printing/page", a.handlePrintPage)
	mux.HandleFunc("/api/calculator/evaluate", a.handleCalculatorEvaluate)
	mux.HandleFunc("/api/calculator/formula", a.handleCalculatorFormula)
	mux.HandleFunc("/api/calculator/functions", a.handleCalculatorFunctions)
	mux.HandleFunc("/api/calculator/capture", a.handleCalculatorCapture)
	mux.HandleFunc("/api/calculator/history", a.handleCalculatorHistory)
}
