package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

// Each pool entry, job, request and export owns an independent lease. The row
// plan borrows the immutable selection until the final lease is closed.
type analysisSourceSnapshot struct {
	data   *analysisSourceSnapshotData
	closed atomic.Bool
}

type analysisSourceSnapshotData struct {
	refs        atomic.Int64
	id          string
	source      string
	wb          *workbook.Workbook
	index       *workbook.SheetIndex
	scope       sourceview.Scope
	snapshot    selectionSnapshot
	plan        selectionRowPlan
	created     time.Time
	unpin       func()
	composition *compositionSnapshot
}

func (s *analysisSourceSnapshot) retain() (*analysisSourceSnapshot, error) {
	if s == nil || s.closed.Load() {
		return nil, errors.New("分析来源快照已失效，请重新捕获")
	}
	defer runtime.KeepAlive(s)
	for {
		count := s.data.refs.Load()
		if count < 1 {
			return nil, errors.New("分析来源快照已释放")
		}
		if s.data.refs.CompareAndSwap(count, count+1) {
			next := &analysisSourceSnapshot{data: s.data}
			runtime.SetFinalizer(next, (*analysisSourceSnapshot).close)
			return next, nil
		}
	}
}

func (s *analysisSourceSnapshot) close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) || s.data.refs.Add(-1) != 0 {
		return
	}
	s.data.snapshot.close()
	s.data.composition.close()
	if s.data.unpin != nil {
		s.data.unpin()
	}
}

func ownAnalysisSource(wb *workbook.Workbook, idx *workbook.SheetIndex, source string, scope sourceview.Scope, snapshot selectionSnapshot, unpin func()) (*analysisSourceSnapshot, error) {
	visible := snapshot.visible
	if scope.Mode == "filtered" {
		visible = nil
	}
	view, err := newSelectionRowView(idx, snapshot.filter, scope.FrozenThrough, visible)
	if err != nil {
		return nil, err
	}
	plan := view.fullPlan()
	if scope.Selection != nil {
		r := scope.Selection
		plan, err = view.plan(r.StartRow, r.EndRow, r.VisualStart, r.VisualEnd)
		if err != nil {
			return nil, err
		}
	}
	data := &analysisSourceSnapshotData{id: randomID(), source: source, wb: wb, index: idx, scope: scope, snapshot: snapshot, plan: plan, created: time.Now(), unpin: unpin}
	data.refs.Store(1)
	out := &analysisSourceSnapshot{data: data}
	runtime.SetFinalizer(out, (*analysisSourceSnapshot).close)
	return out, nil
}

type analysisSourceCaptureRequest struct {
	WorkbookKey       string `json:"workbookKey"`
	Book              string `json:"book"`
	Source            string `json:"source"`
	Mode              string `json:"mode"`
	FilterID          string `json:"filterId,omitempty"`
	ViewID            string `json:"viewId,omitempty"`
	Start             string `json:"start,omitempty"`
	End               string `json:"end,omitempty"`
	VisualStart       int    `json:"visualStart,omitempty"`
	VisualEnd         int    `json:"visualEnd,omitempty"`
	ExcludeThroughRow int    `json:"excludeThroughRow,omitempty"`
}

func decodeAnalysisSourceRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.AnalysisSourceRequestBytes))
	if err != nil {
		return err
	}
	return decodeSavedConfig(data, dst)
}

func (a *App) captureAnalysisSource(wb *workbook.Workbook, req analysisSourceCaptureRequest) (*analysisSourceSnapshot, error) {
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) || !strings.HasPrefix(req.Source, "wb:") {
		return nil, errors.New("分析来源已切换，请重新捕获")
	}
	meta, ok := wb.FindSheet(strings.TrimPrefix(req.Source, "wb:"))
	if !ok {
		return nil, errors.New("分析源工作表不存在")
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("请先打开并索引来源工作表")
	}
	if req.Mode == "visible" && req.ViewID == "" || req.Mode == "filtered" && req.FilterID == "" {
		return nil, errors.New("分析范围缺少成功的筛选或显隐视图")
	}
	snapshot, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			snapshot.close()
		}
	}()
	scope := sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: wb.Fingerprint, Mode: req.Mode, FrozenThrough: req.ExcludeThroughRow}
	if snapshot.filter != nil {
		spec := newFilterSpec(snapshot.filter.Spec.Rules, snapshot.filter.Spec.options())
		if !spec.active() {
			return nil, errors.New("筛选快照缺少可重放规则，不能伪装成完整源表")
		}
		f := sourceview.FilterSpec(spec)
		scope.Filter = &f
	}
	if req.Mode == "filtered" {
		scope.FrozenThrough = 0
	} else if snapshot.visible != nil {
		settings := snapshot.visible.data.settings
		scope.Visibility = &settings
	}
	if req.Mode == "selection" {
		c1, r1, err1 := workbook.ParseCellRef(req.Start)
		c2, r2, err2 := workbook.ParseCellRef(req.End)
		if err1 != nil || err2 != nil {
			return nil, errors.New("分析选区地址无效")
		}
		scope.Selection = &sourceview.Selection{StartRow: r1, EndRow: r2, StartColumn: min(c1, c2), EndColumn: max(c1, c2), VisualStart: req.VisualStart, VisualEnd: req.VisualEnd}
		if snapshot.filter == nil {
			scope.FrozenThrough = 0
		}
	}
	scope, err = normalizedAnalysisScope(wb, req.Source, scope)
	if err != nil {
		return nil, err
	}
	unpin := a.pinWorkbooks(wb)
	out, err := ownAnalysisSource(wb, idx, req.Source, scope, snapshot, unpin)
	if err != nil {
		unpin()
		return nil, err
	}
	accepted = true
	return out, nil
}

func (a *App) handleAnalysisSourceCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req analysisSourceCaptureRequest
	if err := decodeAnalysisSourceRequest(w, r, &req); err != nil {
		writeError(w, 400, "分析来源参数无效")
		return
	}
	wb := a.currentWorkbook()
	snapshot, err := a.captureAnalysisSource(wb, req)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer snapshot.close()
	if err = a.storeAnalysisSource(r.Context(), snapshot); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": snapshot.data.id, "source": req.Source, "sourceScope": snapshot.data.scope, "rows": snapshot.data.plan.Len(), "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func (a *App) storeAnalysisSource(ctx context.Context, snapshot *analysisSourceSnapshot) error {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	if a.currentWorkbook() != snapshot.data.wb || ctx.Err() != nil {
		return errors.New("工作簿已切换或来源捕获已取消")
	}
	if a.analysisSources == nil {
		a.analysisSources = make(map[string]*analysisSourceSnapshot)
	}
	for len(a.analysisSources) >= formats.AnalysisSourceSnapshots {
		oldest := ""
		for id, old := range a.analysisSources {
			if oldest == "" || old.data.created.Before(a.analysisSources[oldest].data.created) {
				oldest = id
			}
		}
		a.analysisSources[oldest].close()
		delete(a.analysisSources, oldest)
	}
	owner, _ := snapshot.retain()
	a.analysisSources[snapshot.data.id] = owner
	return nil
}

func (a *App) handleAnalysisSourceRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		WorkbookKey string           `json:"workbookKey"`
		Book        string           `json:"book"`
		Source      string           `json:"source"`
		Scope       sourceview.Scope `json:"sourceScope"`
	}
	if decodeAnalysisSourceRequest(w, r, &req) != nil {
		writeError(w, 400, "保存的分析范围无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，未恢复分析范围")
		return
	}
	releaseSlot, err := a.acquireAnalysisQuerySlot(r.Context(), req.Source, req.Scope.Filter != nil)
	if err != nil {
		return
	}
	defer releaseSlot()
	a.cacheMu.RLock()
	if a.currentWorkbook() != wb {
		a.cacheMu.RUnlock()
		writeError(w, 409, "工作簿已切换，未恢复分析范围")
		return
	}
	snapshot, err := a.restoreAnalysisSource(r.Context(), wb, req.Source, req.Scope)
	a.cacheMu.RUnlock()
	releaseSlot()
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer snapshot.close()
	if err = a.storeAnalysisSource(r.Context(), snapshot); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": snapshot.data.id, "source": req.Source, "sourceScope": snapshot.data.scope, "rows": snapshot.data.plan.Len(), "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func (a *App) handleAnalysisSourceClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeAnalysisSourceRequest(w, r, &req); err != nil {
		writeError(w, 400, "来源快照标识无效")
		return
	}
	a.analysisMu.Lock()
	snapshot := a.analysisSources[req.ID]
	delete(a.analysisSources, req.ID)
	a.analysisMu.Unlock()
	snapshot.close()
	writeJSON(w, map[string]bool{"cleared": snapshot != nil})
}

func (a *App) analysisSourceSnapshot(id string, wb *workbook.Workbook) (*analysisSourceSnapshot, error) {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	snapshot := a.analysisSources[id]
	if snapshot == nil || snapshot.data.wb != wb || a.currentWorkbook() != wb {
		return nil, errors.New("分析来源快照已失效或来源已切换，请重新捕获")
	}
	return snapshot.retain()
}

// Caller holds cacheMu.RLock and has acquired a query slot if input.Filter
// is set. The rebuilt row order belongs to the returned source snapshot.
func (a *App) restoreAnalysisSource(ctx context.Context, wb *workbook.Workbook, source string, input sourceview.Scope) (*analysisSourceSnapshot, error) {
	scope, err := normalizedAnalysisScope(wb, source, input)
	if err != nil {
		return nil, err
	}
	meta, ok := wb.FindSheet(strings.TrimPrefix(source, "wb:"))
	if !ok {
		return nil, errors.New("分析源工作表不存在")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("分析源工作表尚未索引")
	}
	if err = wb.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	snapshot := selectionSnapshot{}
	accepted := false
	defer func() {
		if !accepted {
			snapshot.close()
		}
	}()
	if scope.Filter != nil {
		spec := filterSpec(*scope.Filter)
		if err = workbook.ValidateFilterSpec(idx, spec.Rules, spec.options()); err != nil {
			return nil, err
		}
		order, buildErr := wb.FilterRowOrder(ctx, idx, spec.Rules, spec.options(), nil)
		if buildErr != nil {
			return nil, buildErr
		}
		snapshot.filter = &FilterResult{ID: randomID(), Sheet: meta.Name, SheetPath: meta.Path, Rows: order, Sorted: spec.SortColumn > 0 || len(spec.SortKeys) > 0, Spec: spec, Created: time.Now()}
	}
	if scope.Visibility != nil {
		layout, layoutErr := wb.SourceLayoutContext(ctx, meta, idx)
		if layoutErr != nil {
			return nil, layoutErr
		}
		axis, axisErr := layout.BuildAxisVisibility(ctx, "row", workbook.MaxRowsForIndex(idx), *scope.Visibility)
		if axisErr != nil {
			return nil, axisErr
		}
		var projection *workbook.RowProjection
		if snapshot.filter != nil {
			projection, err = workbook.NewRowProjection(ctx, wb.CacheDir, idx, snapshot.filter.Rows, axis, 0, nil)
			if err != nil {
				return nil, err
			}
		}
		filterID := ""
		sorted := false
		if snapshot.filter != nil {
			filterID = snapshot.filter.ID
			sorted = snapshot.filter.Sorted
		}
		snapshot.visible = ownSheetRowView(&sheetRowViewData{id: randomID(), wb: wb, idx: idx, meta: meta, filterID: filterID, sorted: sorted, settings: *scope.Visibility, visibility: axis, projection: projection, filter: snapshot.filter, created: time.Now()})
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	unpin := a.pinWorkbooks(wb)
	out, err := ownAnalysisSource(wb, idx, source, scope, snapshot, unpin)
	if err != nil {
		unpin()
		return nil, err
	}
	accepted = true
	return out, nil
}

func (s *analysisSourceSnapshot) rowsAfter(header int) iter.Seq[workbook.SourceRowReference] {
	return func(yield func(workbook.SourceRowReference) bool) {
		defer runtime.KeepAlive(s)
		plan := s.data.plan
		if plan.sourceCount > 0 {
			plan.sourceFirst = max(plan.sourceFirst, header+1)
			plan.sourceCount = plan.view.sourceRangeCount(plan.sourceFirst, plan.sourceLast)
		}
		for ref := range plan.Rows() {
			if ref.Number > header && !yield(ref) {
				return
			}
		}
	}
}

// Header exclusion is counted from the smaller index/order side, without
// reading cell data, building row vectors, or assuming a sorted display.
func (s *analysisSourceSnapshot) countAfter(ctx context.Context, header int) (int, error) {
	defer runtime.KeepAlive(s)
	p := s.data.plan
	count := p.Len() - p.view.sourceRangeCount(p.sourceFirst, min(header, p.sourceLast))
	if p.orderCount == 0 {
		return count, ctx.Err()
	}
	first, last := p.view.underlying(p.orderFirst), p.view.underlying(p.orderFirst+p.orderCount-1)
	before := p.view.idx.FindRowOffset(header + 1)
	if before < p.orderCount {
		for pos := 0; pos < before; pos++ {
			if pos&4095 == 0 && ctx.Err() != nil {
				return 0, ctx.Err()
			}
			number := p.view.idx.RowAt(pos).Number
			if number <= p.view.through {
				continue
			}
			at, found := p.view.order.Position(pos)
			if found && at >= first && at <= last {
				count--
			}
		}
	} else {
		seen := 0
		for pos := range p.view.order.IterRange(first, last-first+1) {
			if seen&4095 == 0 && ctx.Err() != nil {
				return 0, ctx.Err()
			}
			seen++
			number := p.view.idx.RowAt(pos).Number
			if number > p.view.through && number <= header {
				count--
			}
		}
	}
	return count, ctx.Err()
}

func (s *analysisSourceSnapshot) canonicalScope() *sourceview.Scope {
	if s.data.composition != nil {
		return nil
	}
	// Marshal only this bounded declarative description, never source values.
	data, _ := json.Marshal(s.data.scope)
	var out sourceview.Scope
	_ = json.Unmarshal(data, &out)
	return &out
}
