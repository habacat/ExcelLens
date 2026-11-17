package server

import (
	"excellens/internal/workbook"
	"log"
)

// Retain under the map lock, before pruning/clear can release its owner lease.
// Each consumer closes its independent snapshot when the operation finishes.
func (a *App) filterSnapshot(id string) *FilterResult {
	a.mu.RLock()
	defer a.mu.RUnlock()
	base := a.filterResults[id]
	return base.snapshot()
}

func (base *FilterResult) snapshot() *FilterResult {
	if base == nil || base.Rows == nil {
		return nil
	}
	rows, err := base.Rows.Retain()
	if err != nil {
		return nil
	}
	copy := *base
	copy.Rows = rows
	return &copy
}

func (f *FilterResult) close() {
	if f != nil {
		if err := f.Rows.Close(); err != nil {
			log.Printf("释放筛选行序失败：%v", err)
		}
	}
}

func (f *FilterResult) position(idx *workbook.SheetIndex, row int) (int, bool) {
	pos := idx.FindRowOffset(row)
	if pos >= idx.RowCount() || idx.RowAt(pos).Number != row {
		return 0, false
	}
	return f.Rows.Position(pos)
}
