package editfile

import (
	"errors"
	"slices"

	"excellens/internal/editdoc"
)

// A native visible-cells selection can omit rows, columns or both. Its
// cells move together, but relative formulas still originate at their real addresses.
func visibleClipboardRange(source *editdoc.PortableRange) (*editdoc.PortableRange, error) {
	if len(source.VisibleSourceRows) == 0 && len(source.VisibleSourceCols) == 0 {
		return nil, ErrTarget
	}
	offsets := func(start, size int, visible []int) ([]int, error) {
		if len(visible) == 0 {
			all := make([]int, size)
			for i := range all {
				all[i] = i
			}
			return all, nil
		}
		out := make([]int, len(visible))
		for i, position := range visible {
			offset := position - start
			if offset < 0 || offset >= size || i > 0 && offset <= out[i-1] {
				return nil, ErrTarget
			}
			out[i] = offset
		}
		return out, nil
	}
	rows, err := offsets(source.Start.Row, source.Rows, source.VisibleSourceRows)
	if err != nil {
		return nil, err
	}
	cols, err := offsets(source.Start.Col, source.Cols, source.VisibleSourceCols)
	if err != nil {
		return nil, err
	}
	result := *source
	result.Rows, result.Cols = len(rows), len(cols)
	if len(source.VisibleSourceRows) > 0 {
		result.SourceRows, result.Start.Row = source.VisibleSourceRows, source.VisibleSourceRows[0]
	}
	if len(source.VisibleSourceCols) > 0 {
		result.SourceCols, result.Start.Col = source.VisibleSourceCols, source.VisibleSourceCols[0]
	}
	result.VisibleSourceRows, result.VisibleSourceCols = nil, nil
	result.Cells = make([]editdoc.PortableCell, 0, len(rows)*len(cols))
	for _, row := range rows {
		for _, col := range cols {
			result.Cells = append(result.Cells, source.Cells[row*source.Cols+col])
		}
	}
	result.Merges = nil
	for _, m := range source.Merges {
		if !m.Valid() || m.EndRow > source.Rows || m.EndCol > source.Cols {
			return nil, ErrTarget
		}
		top, _ := slices.BinarySearch(rows, m.Start.Row-1)
		bottom, _ := slices.BinarySearch(rows, m.EndRow)
		left, _ := slices.BinarySearch(cols, m.Start.Col-1)
		right, _ := slices.BinarySearch(cols, m.EndCol)
		if top == bottom || left == right {
			continue
		}
		if bottom-top != m.EndRow-m.Start.Row+1 || right-left != m.EndCol-m.Start.Col+1 {
			return nil, errors.New("可见区域只覆盖部分合并单元格，请粘贴全部单元格")
		}
		m.Start.Row, m.EndRow, m.Start.Col, m.EndCol = top+1, bottom, left+1, right
		result.Merges = append(result.Merges, m)
	}
	return &result, nil
}
