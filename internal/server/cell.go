package server

import (
	"net/http"

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
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	ref := r.URL.Query().Get("ref")
	col, rowNumber, err := workbook.ParseCellRef(ref)
	if err != nil {
		writeError(w, http.StatusBadRequest, "单元格地址无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, http.StatusConflict, "该工作表尚未建立索引，请先打开工作表")
		return
	}
	if rowNumber > workbook.MaxRowsForIndex(idx) {
		writeError(w, http.StatusBadRequest, "单元格地址超出源格式范围")
		return
	}
	cell := workbook.CellData{Ref: ref, Col: col, Raw: "", Display: ""}
	presentationNotice := ""
	{
		rows, readErr := wb.ReadRowsByNumbersWithOptionsContext(r.Context(), idx, []int{rowNumber}, col, col, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: r.URL.Query().Get("conditional") != "0"})
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "读取单元格失败："+readErr.Error())
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
