package server

import (
	"encoding/json"
	"excellens/internal/searchdsl"
	"net/http"
)

func (a *App) handleSearchValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if a.currentWorkbook() == nil {
		writeError(w, http.StatusConflict, "请先打开原始工作簿")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, 400, "查询语句无效")
		return
	}
	if _, err := searchdsl.Parse(req.Query); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"valid": true})
}
