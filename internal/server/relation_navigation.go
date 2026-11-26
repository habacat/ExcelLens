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
		writeError(w, 400, "关联源坐标无效")
		return
	}
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-r.Context().Done():
		writeError(w, 408, "关联定位已取消")
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
		writeError(w, 409, err.Error())
		return
	}
	defer source.Close()
	data := source.Value()
	input := data.inputs.Value()
	book, ref := input.books[req.Side], input.refs[req.Side]
	if req.Book != ref.Book {
		writeError(w, 409, "关联定位来源不一致")
		return
	}
	info := data.plan.Sources()[req.Side]
	if !slices.ContainsFunc(info.Fields, func(field reconcile.Field) bool { return field.Column == req.Col }) {
		writeError(w, 400, "定位字段不属于关联结果")
		return
	}
	if err := input.primary.CheckSourceUnchanged(); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err := data.plan.Check(r.Context()); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if info.Kind == "analysis" {
		if req.Row > info.Total {
			writeError(w, 400, "关联来源行不存在")
			return
		}
	} else {
		meta, found := book.FindSheet(info.Sheet)
		if !found {
			writeError(w, 409, "关联来源工作表已失效")
			return
		}
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			writeError(w, 409, "关联来源索引已失效")
			return
		}
		at := idx.FindRowOffset(req.Row)
		if req.Row <= info.HeaderRow || at >= idx.RowCount() || idx.RowAt(at).Number != req.Row {
			writeError(w, 400, "关联来源行不存在")
			return
		}
	}
	a.cacheMu.RUnlock()
	locked = false
	target, err := a.sourceNavigation(book, info.Kind, ref.Source, info.Sheet, req.Row, req.Col)
	if err != nil {
		writeError(w, 429, err.Error())
		return
	}
	writeJSON(w, target)
}
