package server

import (
	"log"
	"runtime"
	"time"

	"excellens/internal/i18n"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

var errRowViewReleased = i18n.Error("service.rowView.visibleRowViewReleasedOpen")

// A lease binds a projection to one immutable source/index/filter/visibility
// snapshot. Matching row counts alone never prove source identity. The pin
// outlives map eviction and workbook changes while another consumer has a lease.
type sheetRowView struct {
	data  *sheetRowViewData
	lease *taskmanager.Lease[*sheetRowViewData]
}

type sheetRowViewData struct {
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
	lease := taskmanager.NewFinalizedLease(data, func() {
		if err := data.projection.Close(); err != nil {
			log.Printf("释放可见行投影失败：%v", err)
		}
		data.filter.close()
		if data.unpin != nil {
			data.unpin()
		}
	})
	return &sheetRowView{data: data, lease: lease}
}

func (v *sheetRowView) isClosed() bool {
	return v == nil || v.lease.Closed()
}

func (v *sheetRowView) retain() (*sheetRowView, error) {
	if v == nil || v.lease == nil {
		return nil, errRowViewReleased
	}
	next, err := v.lease.Retain(errRowViewReleased)
	if err != nil {
		return nil, err
	}
	return &sheetRowView{data: next.Value(), lease: next}, nil
}

func (v *sheetRowView) close() {
	if v == nil || v.lease == nil {
		return
	}
	v.lease.Close()
}

// Caller holds the shared cache read lock. Retain under the map lock so
// eviction cannot release the source between lookup and retaining its lease.
func (a *App) rowViewSnapshot(id string, wb *workbook.Workbook, idx *workbook.SheetIndex, path, filterID string) (*sheetRowView, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	v := a.rowViews[id]
	if a.wb != wb || v == nil || v.data.wb != wb || v.data.idx != idx || v.data.meta.Path != path || v.data.filterID != filterID {
		return nil, i18n.Error("service.rowView.sourceOrFilterVisibleRow")
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
