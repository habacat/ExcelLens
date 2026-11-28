package server

import (
	"net/http"
	"strings"

	"excellens/internal/analysis"
)

func (a *App) handleAnalysisCompositionNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID string `json:"jobId"`
		Book  string `json:"book"`
		Input int    `json:"input"`
		Row   int    `json:"row"`
		Col   int    `json:"col"`
	}
	if decodeAnalysis(r, &req) != nil || req.Row < 1 || req.Col < 1 || req.Input < 0 {
		writeMessage(w, 400, "service.analysisCompositionNavigation.invalidPositioningParametersJointSources")
		return
	}
	wb := a.currentWorkbook()
	job := a.getAnalysisJob(req.JobID)
	if job == nil || wb == nil {
		writeMessage(w, 409, "service.analysisCompositionNavigation.analysisResultsHaveExpiredRun")
		return
	}
	job.mu.RLock()
	query, done, current := job.Query, job.Status == "done", job.Workbook == wb
	job.mu.RUnlock()
	if !done || !current || query.Composition == nil {
		writeMessage(w, 409, "service.analysisCompositionNavigation.combinedSourceAnalysisResultsHave")
		return
	}
	a.cacheMu.RLock()
	src, err := a.sourceResult(r.Context(), wb, query, req.JobID)
	if err != nil {
		a.cacheMu.RUnlock()
		writeFailure(w, 409, err)
		return
	}
	defer src.close()
	snapshot := src.lease.data.composition
	if req.Input >= len(snapshot.data.info) {
		a.cacheMu.RUnlock()
		writeMessage(w, 400, "service.analysisCompositionNavigation.invalidSourceSerialNumber")
		return
	}
	info := snapshot.data.info[req.Input]
	book := snapshot.data.books[req.Input]
	mapped := false
	for _, column := range query.Composition.Inputs[req.Input].Columns {
		if column == req.Col {
			mapped = true
			break
		}
	}
	if !mapped || req.Book != info.Ref.Book || req.Row <= info.Ref.HeaderRow {
		a.cacheMu.RUnlock()
		writeMessage(w, 400, "service.analysisCompositionNavigation.invalidSourceIdentityOrCoordinates")
		return
	}
	if strings.HasPrefix(info.Ref.Source, "scratch:") {
		workspace, loadErr := a.loadAnalysis(book)
		var sheet *analysis.Sheet
		if loadErr == nil {
			sheet = workspace.Sheet(strings.TrimPrefix(info.Ref.Source, "scratch:"))
		}
		if sheet == nil || analysisSheetStamp(sheet) != info.Ref.Stamp || req.Row > len(sheet.Rows) {
			a.cacheMu.RUnlock()
			writeMessage(w, 409, "service.analysisCompositionNavigation.analysisTableModifiedOrRemoved")
			return
		}
	} else {
		meta, found := book.FindSheet(info.Sheet)
		if !found {
			a.cacheMu.RUnlock()
			writeMessage(w, 409, "service.analysisCompositionNavigation.sourceWorksheetChanged")
			return
		}
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			a.cacheMu.RUnlock()
			writeMessage(w, 409, "service.analysisCompositionNavigation.sourceIndexBecomeInvalid")
			return
		}
		maxRow := idx.MaxRow
		if scope := info.Ref.Scope; scope != nil {
			maxRow = max(maxRow, scope.FrozenThrough)
			if scope.Selection != nil {
				maxRow = max(maxRow, scope.Selection.StartRow, scope.Selection.EndRow)
			}
		}
		if req.Row > maxRow {
			a.cacheMu.RUnlock()
			writeMessage(w, 400, "service.analysisCompositionNavigation.sourceRowExceedsSourceRange")
			return
		}
	}
	a.cacheMu.RUnlock()
	target, err := a.sourceNavigation(book, info.Kind, info.Ref.Source, info.Sheet, req.Row, req.Col)
	if err != nil {
		writeFailure(w, 429, err)
		return
	}
	writeJSON(w, target)
}
