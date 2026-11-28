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
		writeFailure(w, 408, err)
		return
	}
	defer release()
	wb := a.currentWorkbook()
	if wb == nil || r.URL.Query().Get("book") != sourceBookID(wb) {
		writeMessage(w, 409, "service.sourceNotes.annotationSourceSwitched")
		return
	}
	if source, ok := formats.Lookup(wb.Format); !ok || !source.NoteMetadata {
		writeMessage(w, 422, "service.sourceNotes.thisSourceNoReadableWorkbook")
		return
	}
	meta, found := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !found {
		writeMessage(w, 404, "service.sourceNotes.worksheetDoesNotExist")
		return
	}
	notes, threaded, err := wb.SheetNotes(r.Context(), meta)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, map[string]any{"items": notes, "threaded": threaded, "sheet": meta.Name})
}
