package server

import (
	"excellens/internal/formats"
	"net/http"
)

func (a *App) handleSourceNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	release, err := a.lockQueryRead(r.Context())
	if err != nil {
		writeError(w, 408, err.Error())
		return
	}
	defer release()
	wb := a.currentWorkbook()
	if wb == nil || r.URL.Query().Get("book") != sourceBookID(wb) {
		writeError(w, 409, "批注来源已切换")
		return
	}
	if source, ok := formats.Lookup(wb.Format); !ok || !source.NoteMetadata {
		writeError(w, 422, "此来源没有可读取的工作簿批注")
		return
	}
	meta, found := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !found {
		writeError(w, 404, "工作表不存在")
		return
	}
	notes, threaded, err := wb.SheetNotes(r.Context(), meta)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": notes, "threaded": threaded, "sheet": meta.Name})
}
