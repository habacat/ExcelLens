package server

import (
	"net/http"

	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// handleCell returns one exact cell without loading an entire row or changing
// the workbook. It is used by formula-reference inspection and cell tooltips.
func (a *App) handleCell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.cell.noWorkbookOpen")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.cell.unableFindSpecifiedWorksheet")
		return
	}
	ref := r.URL.Query().Get("ref")
	col, rowNumber, err := workbook.ParseCellRef(ref)
	if err != nil {
		writeMessage(w, http.StatusBadRequest, "service.cell.cellAddressInvalid")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.cell.thisWorksheetNotIndexedYet")
		return
	}
	if rowNumber > workbook.MaxRowsForIndex(idx) {
		writeMessage(w, http.StatusBadRequest, "service.cell.cellAddressExceedsSourceFormat")
		return
	}
	cell := workbook.CellData{Ref: ref, Col: col, Raw: "", Display: ""}
	presentationNotice := ""
	{
		conditionalOn := r.URL.Query().Get("conditional") != "0"
		readOpts := workbook.RowReadOptions{
			FormatValue:              true,
			IncludeFormula:           true,
			RichText:                 true,
			ConditionalFormats:       conditionalOn,
			ConditionalStatsReadOnly: conditionalOn,
		}
		if conditionalOn {
			readOpts.ConditionalStatsStatus = a.conditionalStatsSheetStatus(meta.Path)
		}
		rows, readErr := wb.ReadRowsByNumbersWithOptionsContext(r.Context(), idx, []int{rowNumber}, col, col, readOpts)
		if readErr != nil {
			writeMessage(w, http.StatusInternalServerError, "service.cell.failedReadCell", i18n.Args{"detail": readErr})
			return
		}
		if len(rows) > 0 {
			presentationNotice = rows[0].PresentationNotice
			for _, candidate := range rows[0].Cells {
				if candidate.Col == col {
					cell = candidate
					break
				}
			}
		}
	}
	if link, linkErr := wb.HyperlinkAt(meta, cell, rowNumber, col); linkErr == nil {
		cell.Hyperlink = link
		if cell.Display == "" && link != nil && link.Display != "" {
			cell.Raw, cell.Display, cell.Type = link.Display, link.Display, "str"
		}
	}
	writeJSON(w, map[string]any{"sheet": meta.Name, "ref": ref, "cell": cell, "presentationNotice": presentationNotice})
}
