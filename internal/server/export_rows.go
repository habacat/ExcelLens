package server

import (
	"errors"

	"excellens/internal/workbook"
)

// An export retains the exact selection/filter snapshot before entering its
// queue. Clearing a view or replacing a map owner cannot change these rows.
type exportRowSource struct {
	index    *workbook.SheetIndex
	path     string
	snapshot selectionSnapshot
	plan     *selectionRowPlan
	unpin    func()
}

func (s *exportRowSource) close() {
	if s == nil {
		return
	}
	s.snapshot.close()
	if s.unpin != nil {
		s.unpin()
	}
}

func (a *App) captureExportRows(wb *workbook.Workbook, req exportRequest) (*exportRowSource, error) {
	if req.Scope != "selection" && req.Scope != "filtered" && req.Scope != "visible" {
		return nil, nil
	}
	if req.Scope == "selection" && req.ViewID == "" && req.FilterID == "" {
		return nil, nil
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		return nil, errors.New("导出工作表不存在")
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("请先打开并索引工作表")
	}
	snapshot, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
	if err != nil {
		return nil, err
	}
	out := &exportRowSource{index: idx, path: meta.Path, snapshot: snapshot, unpin: a.pinWorkbooks(wb)}
	if req.Scope == "filtered" {
		if snapshot.filter == nil {
			out.close()
			return nil, errors.New("完整筛选导出缺少匹配结果")
		}
		return out, nil
	}
	view, err := newSelectionRowView(idx, snapshot.filter, req.ExcludeThroughRow, snapshot.visible)
	if err != nil {
		out.close()
		return nil, err
	}
	if req.Scope == "visible" {
		plan := view.fullPlan()
		out.plan = &plan
		return out, nil
	}
	_, row1, _ := workbook.ParseCellRef(req.Start)
	_, row2, _ := workbook.ParseCellRef(req.End)
	plan, err := view.plan(row1, row2, req.VisualStart, req.VisualEnd)
	if err != nil {
		out.close()
		return nil, err
	}
	out.plan = &plan
	return out, nil
}
