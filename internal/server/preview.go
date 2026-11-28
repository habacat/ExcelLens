package server

import (
	"errors"
	"net/http"

	"excellens/internal/i18n"
)

import "excellens/internal/workbook"

func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, 409, "service.preview.dataFileNotOpenedYet")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, 404, "service.preview.worksheetDoesNotExist")
		return
	}
	rows, err := wb.PreviewRows(r.Context(), meta, 80, 40)
	if errors.Is(err, workbook.ErrPreviewUnavailable) {
		writeJSON(w, map[string]any{"available": false, "partial": true})
		return
	}
	if err != nil {
		writeMessage(w, 400, "service.preview.firstScreenPreviewFailed", i18n.Args{"detail": err})
		return
	}
	maxRow, maxCol := 0, 0
	for _, row := range rows {
		maxRow = max(maxRow, row.Number)
		for _, cell := range row.Cells {
			maxCol = max(maxCol, cell.Col)
		}
	}
	header := PreviewHeader{Available: len(rows) > 0, Partial: true, Sheet: meta.Name, StartCol: 1, ColCount: 40, MaxRow: maxRow, MaxCol: maxCol, TotalRows: len(rows)}
	if r.URL.Query().Get("compact") == "1" {
		header.Compact = 1
		writeJSON(w, previewResponse[CompactRow]{header, packViewportRows(rows)})
		return
	}
	writeJSON(w, previewResponse[workbook.RowData]{header, rows})
}
