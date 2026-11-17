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
		writeError(w, 400, "联合来源定位参数无效")
		return
	}
	wb := a.currentWorkbook()
	job := a.getAnalysisJob(req.JobID)
	if job == nil || wb == nil {
		writeError(w, 409, "分析结果已过期，请重新运行分析")
		return
	}
	job.mu.RLock()
	query, done, current := job.Query, job.Status == "done", job.Workbook == wb
	job.mu.RUnlock()
	if !done || !current || query.Composition == nil {
		writeError(w, 409, "联合来源分析结果已变化")
		return
	}
	a.cacheMu.RLock()
	src, err := a.sourceResult(r.Context(), wb, query, req.JobID)
	if err != nil {
		a.cacheMu.RUnlock()
		writeError(w, 409, err.Error())
		return
	}
	defer src.close()
	snapshot := src.lease.data.composition
	if req.Input >= len(snapshot.data.info) {
		a.cacheMu.RUnlock()
		writeError(w, 400, "来源序号无效")
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
		writeError(w, 400, "来源身份或源坐标无效")
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
			writeError(w, 409, "分析表已修改或移除，请重新核对")
			return
		}
	} else {
		meta, found := book.FindSheet(info.Sheet)
		if !found {
			a.cacheMu.RUnlock()
			writeError(w, 409, "来源工作表已变化")
			return
		}
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			a.cacheMu.RUnlock()
			writeError(w, 409, "来源索引已失效")
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
			writeError(w, 400, "源行超出来源范围")
			return
		}
	}
	a.cacheMu.RUnlock()
	target, err := a.sourceNavigation(book, info.Kind, info.Ref.Source, info.Sheet, req.Row, req.Col)
	if err != nil {
		writeError(w, 429, err.Error())
		return
	}
	writeJSON(w, target)
}
