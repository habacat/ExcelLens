package server

import (
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// filter is borrowed from visible when a view token is supplied, otherwise
// this object owns a retained filter lease. Close the composite exactly once.
type selectionSnapshot struct {
	filter  *FilterResult
	visible *sheetRowView
}

func (s selectionSnapshot) close() {
	if s.visible != nil {
		s.visible.close()
	} else {
		s.filter.close()
	}
}

// Caller holds the shared cache read lock. Resolve source, index and row order
// together so a same-sized worksheet cannot satisfy a stale visual selection.
func (a *App) selectionSnapshot(wb *workbook.Workbook, idx *workbook.SheetIndex, path, filterID, viewID string) (selectionSnapshot, error) {
	if viewID != "" {
		visible, err := a.rowViewSnapshot(viewID, wb, idx, path, filterID)
		if err != nil {
			return selectionSnapshot{}, err
		}
		return selectionSnapshot{filter: visible.data.filter, visible: visible}, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || wb.GetCachedIndex(path) != idx {
		return selectionSnapshot{}, i18n.Error("service.selectionSnapshot.sourceSelectionChangedReselect")
	}
	if filterID == "" {
		return selectionSnapshot{}, nil
	}
	filter := a.filterResults[filterID]
	if filter == nil || filter.SheetPath != path {
		return selectionSnapshot{}, i18n.Error("service.filter.expired")
	}
	copy := filter.snapshot()
	if copy == nil {
		return selectionSnapshot{}, i18n.Error("service.selectionSnapshot.filterResultsHaveReleasedFilter")
	}
	return selectionSnapshot{filter: copy}, nil
}
