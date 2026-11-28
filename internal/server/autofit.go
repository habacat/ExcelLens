package server

import (
	"context"
	"errors"
	"net/http"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// Uniform deterministic sampling; never traverse a whole million-row column
// for a double-click. Include both ends because titles and totals often live
// there. At most 512 row reads, one file descriptor, one reusable XML buffer.
func autofitSampleRows(total int) []int {
	if total <= 0 {
		return nil
	}
	const capSamples = 512
	if total <= capSamples {
		indexes := make([]int, total)
		for i := range indexes {
			indexes[i] = i
		}
		return indexes
	}
	// Keep the exact ceiling of 512 requests. Header, early records and
	// closing totals get 32 samples each, with 448 uniformly spaced samples
	// across the middle. There is no map allocation or large-row scan.
	const edgeSamples = 32
	const middleSamples = capSamples - edgeSamples*2
	result := make([]int, 0, capSamples)
	for i := 0; i < edgeSamples; i++ {
		result = append(result, i)
	}
	for i := 0; i < middleSamples; i++ {
		result = append(result, edgeSamples+i*(total-edgeSamples*2-1)/(middleSamples-1))
	}
	for i := total - edgeSamples; i < total; i++ {
		result = append(result, i)
	}
	return result
}

func (a *App) handleAutofit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.autofit.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.autofit.worksheetDoesNotExist")
		return
	}
	kind := r.URL.Query().Get("kind")
	index := parseIntDefault(r.URL.Query().Get("index"), 0)
	if kind != "col" && kind != "row" || index < 1 || (kind == "col" && index > formats.ExcelColumns) || (kind == "row" && index > formats.ExternalRows) {
		writeMessage(w, http.StatusBadRequest, "service.autofit.automaticAdaptationTargetInvalid")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.currentWorkbook() != wb {
		writeMessage(w, http.StatusConflict, "service.autofit.workbookSwitched")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.autofit.worksheetIndexNotCreatedYet")
		return
	}
	var offsets []int
	firstCol, lastCol := index, index
	if kind == "col" {
		offsets = autofitSampleRows(idx.RowCount())
	} else {
		firstCol = parseIntDefault(r.URL.Query().Get("firstCol"), 1)
		lastCol = parseIntDefault(r.URL.Query().Get("lastCol"), firstCol)
		if firstCol < 1 || firstCol > formats.ExcelColumns || lastCol < firstCol || lastCol > formats.ExcelColumns || lastCol-firstCol+1 > 80 {
			writeMessage(w, http.StatusBadRequest, "service.autofit.rowHeightAutomaticallyAdaptsUp")
			return
		}
		at := idx.FindRowOffset(index)
		if at < idx.RowCount() && idx.RowAt(at).Number == index {
			offsets = []int{at}
		}
	}
	var rows []workbook.RowData
	if len(offsets) > 0 {
		var err error
		rows, err = wb.ReadRowsByIndicesContext(r.Context(), idx, offsets, firstCol, lastCol)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			writeMessage(w, http.StatusInternalServerError, "service.autofit.automaticAdaptationPreviewFailed", i18n.Args{"detail": err})
			return
		}
	}
	writeJSON(w, map[string]any{"kind": kind, "index": index, "sampledRows": len(offsets), "totalRows": idx.RowCount(), "rows": rows})
}
