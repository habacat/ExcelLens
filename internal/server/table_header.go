package server

import "net/http"

func (a *App) handleTableHeader(w http.ResponseWriter, r *http.Request) {
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
	header, err := wb.ReadTextTableHeader(r.Context(), meta, parseIntDefault(r.URL.Query().Get("row"), 0))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, header)
}
