package server

import (
	"net/http"
	"slices"

	"excellens/internal/formats"
	"excellens/internal/reconcile"
)

func (a *App) handleRelationNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID string `json:"jobId"`
		Book  string `json:"book"`
		Side  int    `json:"side"`
		Row   int    `json:"row"`
		Col   int    `json:"col"`
	}
	if decodeRelationRequest(w, r, &req) != nil || req.Side < 0 || req.Side > 1 || req.Row < 1 || req.Row > formats.ExternalRows || req.Col < 1 || req.Col > formats.ExcelColumns {
		writeMessage(w, 400, "service.relationNavigation.invalidAssociatedSourceCoordinates")
		return
	}
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-r.Context().Done():
		writeMessage(w, 408, "service.relationNavigation.associatedPositioningCanceled")
		return
	}
	a.cacheMu.RLock()
	locked := true
	defer func() {
		if locked {
			a.cacheMu.RUnlock()
		}
	}()
	source, err := a.borrowRelationTask(req.JobID, a.currentWorkbook())
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	defer source.Close()
	data := source.Value()
	input := data.inputs.Value()
	book, ref := input.books[req.Side], input.refs[req.Side]
	if req.Book != ref.Book {
		writeMessage(w, 409, "service.relationNavigation.associatedLocationSourcesInconsistent")
		return
	}
	info := data.plan.Sources()[req.Side]
	if !slices.ContainsFunc(info.Fields, func(field reconcile.Field) bool { return field.Column == req.Col }) {
		writeMessage(w, 400, "service.relationNavigation.locatedFieldDoesNotBelong")
		return
	}
	if err := input.primary.CheckSourceUnchanged(); err != nil {
		writeFailure(w, 409, err)
		return
	}
	if err := data.plan.Check(r.Context()); err != nil {
		writeFailure(w, 409, err)
		return
	}
	if info.Kind == "analysis" {
		if req.Row > info.Total {
			writeMessage(w, 400, "service.relationNavigation.associatedSourceRowDoesNot")
			return
		}
	} else {
		meta, found := book.FindSheet(info.Sheet)
		if !found {
			writeMessage(w, 409, "service.relationNavigation.associatedSourceWorksheetBecomeInvalid")
			return
		}
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			writeMessage(w, 409, "service.relationNavigation.associatedSourceIndexBecomeInvalid")
			return
		}
		at := idx.FindRowOffset(req.Row)
		if req.Row <= info.HeaderRow || at >= idx.RowCount() || idx.RowAt(at).Number != req.Row {
			writeMessage(w, 400, "service.relationNavigation.associatedSourceRowDoesNot")
			return
		}
	}
	a.cacheMu.RUnlock()
	locked = false
	target, err := a.sourceNavigation(book, info.Kind, ref.Source, info.Sheet, req.Row, req.Col)
	if err != nil {
		writeFailure(w, 429, err)
		return
	}
	writeJSON(w, target)
}
