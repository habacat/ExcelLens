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
		writeMessage(w, http.StatusConflict, "service.searchValidation.openOriginalWorkbookFirst")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeMessage(w, 400, "service.searchValidation.invalidQueryStatement")
		return
	}
	if _, err := searchdsl.Parse(req.Query); err != nil {
		writeFailure(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"valid": true})
}
