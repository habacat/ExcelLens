package server

import (
	"errors"
	"log"
	"runtime"
	"sync/atomic"
	"time"

	"excellens/internal/workbook"
)

// A lease binds a projection to one immutable source/index/filter/visibility
// snapshot. Matching row counts alone never prove source identity. The pin
// outlives map eviction and workbook changes while another consumer has a lease.
type sheetRowView struct {
	data   *sheetRowViewData
	closed atomic.Bool
}

type sheetRowViewData struct {
	refs     atomic.Int64
	id       string
	wb       *workbook.Workbook
	idx      *workbook.SheetIndex
	meta     workbook.SheetMeta
	filterID string
	sorted   bool
	settings workbook.AxisViewSettings
	revision int64
	// A transient, explicitly requested view may bypass unreadable preferences.
	// Preserve that state across derived snapshots; never treat it as saved.
	settingsWarning string
	visibility      *workbook.AxisVisibility
	projection      *workbook.RowProjection
	filter          *FilterResult
	created         time.Time
	unpin           func()
}

func ownSheetRowView(data *sheetRowViewData) *sheetRowView {
	data.refs.Store(1)
	v := &sheetRowView{data: data}
	runtime.SetFinalizer(v, (*sheetRowView).close)
	return v
}

func (v *sheetRowView) retain() (*sheetRowView, error) {
	if v == nil || v.closed.Load() {
		return nil, errors.New("可见行视图已释放，请重新打开")
	}
	defer runtime.KeepAlive(v)
	for {
		count := v.data.refs.Load()
		if count <= 0 {
			return nil, errors.New("可见行视图已释放，请重新打开")
		}
		if v.data.refs.CompareAndSwap(count, count+1) {
			next := &sheetRowView{data: v.data}
			runtime.SetFinalizer(next, (*sheetRowView).close)
			return next, nil
		}
	}
}

func (v *sheetRowView) close() {
	if v == nil || !v.closed.CompareAndSwap(false, true) || v.data.refs.Add(-1) != 0 {
		return
	}
	if err := v.data.projection.Close(); err != nil {
		log.Printf("释放可见行投影失败：%v", err)
	}
	v.data.filter.close()
	if v.data.unpin != nil {
		v.data.unpin()
	}
}

// Caller holds the shared cache read lock. Retain under the map lock so
// eviction cannot release the source between lookup and retaining its lease.
func (a *App) rowViewSnapshot(id string, wb *workbook.Workbook, idx *workbook.SheetIndex, path, filterID string) (*sheetRowView, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	v := a.rowViews[id]
	if a.wb != wb || v == nil || v.data.wb != wb || v.data.idx != idx || v.data.meta.Path != path || v.data.filterID != filterID {
		return nil, errors.New("可见行视图的来源或筛选已变化，请重新打开视图")
	}
	return v.retain()
}

func (v *sheetRowView) count() int {
	defer runtime.KeepAlive(v)
	if v.data.projection != nil {
		return v.data.projection.Len()
	}
	return v.data.visibility.Before(v.data.idx.MaxRow + 1)
}

func (v *sheetRowView) payload() map[string]any {
	defer runtime.KeepAlive(v)
	var matchedPinned []int
	if v.data.projection != nil {
		for row := 1; row <= 30; row++ {
			pos := v.data.idx.FindRowOffset(row)
			if pos < v.data.idx.RowCount() && v.data.idx.RowAt(pos).Number == row {
				if _, found := v.data.projection.Position(pos); found {
					matchedPinned = append(matchedPinned, row)
				}
			}
		}
	}
	notice := v.data.visibility.Notice()
	if v.data.settingsWarning != "" {
		if notice != "" {
			notice += "；"
		}
		notice += v.data.settingsWarning
	}
	return map[string]any{
		"id": v.data.id, "sheet": v.data.meta.Name, "filterId": v.data.filterID,
		"workbookKey": v.data.wb.Fingerprint, "book": sourceBookID(v.data.wb),
		"revision": v.data.revision, "settings": v.data.settings,
		"totalRows": v.count(), "maxRow": v.data.idx.MaxRow,
		"digest": v.data.visibility.Digest(), "notice": notice,
		"settingsWarning":   v.data.settingsWarning,
		"matchedPinnedRows": matchedPinned,
	}
}

func pruneRowViewsLocked(views map[string]*sheetRowView, keep int) {
	for len(views) > keep {
		oldest := ""
		for id, v := range views {
			if oldest == "" || v.data.created.Before(views[oldest].data.created) {
				oldest = id
			}
		}
		views[oldest].close()
		delete(views, oldest)
	}
}
