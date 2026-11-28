package server

import "net/http"

func (a *App) handleTableHeader(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, 409, "service.tableHeader.dataFileNotOpenedYet")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, 404, "service.tableHeader.worksheetDoesNotExist")
		return
	}
	header, err := wb.ReadTextTableHeader(r.Context(), meta, parseIntDefault(r.URL.Query().Get("row"), 0))
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	writeJSON(w, header)
}
