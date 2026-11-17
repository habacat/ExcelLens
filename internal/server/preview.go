package server

import (
	"errors"
	"net/http"
)

import "excellens/internal/workbook"

func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "尚未打开数据文件")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, 404, "工作表不存在")
		return
	}
	rows, err := wb.PreviewRows(r.Context(), meta, 80, 40)
	if errors.Is(err, workbook.ErrPreviewUnavailable) {
		writeJSON(w, map[string]any{"available": false, "partial": true})
		return
	}
	if err != nil {
		writeError(w, 400, "首屏预览失败："+err.Error())
		return
	}
	maxRow, maxCol := 0, 0
	for _, row := range rows {
		maxRow = max(maxRow, row.Number)
		for _, cell := range row.Cells {
			maxCol = max(maxCol, cell.Col)
		}
	}
	payload := map[string]any{"available": len(rows) > 0, "partial": true, "sheet": meta.Name, "startCol": 1, "colCount": 40, "rows": rows, "maxRow": maxRow, "maxCol": maxCol, "totalRows": len(rows)}
	if r.URL.Query().Get("compact") == "1" {
		payload["rows"] = packViewportRows(rows)
		payload["compact"] = 1
	}
	writeJSON(w, payload)
}
