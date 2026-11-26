package workbook

import (
	"context"
	"errors"
	"os"

	"excellens/internal/formats"
)

// ReadRowsSparseContext reads at most the requested columns. It preserves
// original column numbers, row numbers, formatting and formulas. Hidden source
// columns are only omitted from the *view*, never from the workbook index.
// A selected-column map lets the existing verified fast scanner skip values in
// large gaps (e.g. A visible, B:IV hidden, IW visible).
func (w *Workbook) ReadRowsSparseContext(ctx context.Context, idx *SheetIndex, indices []int, columns []int) ([]RowData, error) {
	if len(columns) == 0 || len(columns) > 80 {
		return nil, errors.New("invalid sparse columns")
	}
	selected := make(map[int]struct{}, len(columns))
	prev := 0
	for _, col := range columns {
		if col <= prev || col > formats.ExcelColumns {
			return nil, errors.New("sparse columns must be ascending and unique")
		}
		selected[col] = struct{}{}
		prev = col
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return w.readIndexedBatch(ctx, f, idx, indices, columns[0], columns[len(columns)-1], selected)
}
