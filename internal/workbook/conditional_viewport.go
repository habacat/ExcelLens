package workbook

import (
	"sort"

	"excellens/internal/conditional"
)

// conditionalSourceRow borrows the decoded source cells. Dense or ordered sparse
// rows need no per-cell index. Irregular rows preserve the source reader's
// last-record-wins lookup without reordering its output.
type conditionalSourceRow struct {
	cells      []CellData
	denseFirst int
	positions  map[int]int
}

func indexConditionalRow(cells []CellData) conditionalSourceRow {
	row := conditionalSourceRow{cells: cells}
	if len(cells) == 0 {
		return row
	}
	row.denseFirst = cells[0].Col
	for i := 1; i < len(cells); i++ {
		if cells[i].Col <= cells[i-1].Col {
			row.denseFirst = 0
			row.positions = make(map[int]int, len(cells))
			for j := range cells {
				row.positions[cells[j].Col] = j
			}
			break
		}
		if cells[i].Col != cells[i-1].Col+1 {
			row.denseFirst = 0
		}
	}
	return row
}

func (r conditionalSourceRow) position(col int) int {
	if r.denseFirst > 0 {
		i := col - r.denseFirst
		if i >= 0 && i < len(r.cells) {
			return i
		}
		return -1
	}
	if r.positions != nil {
		if i, ok := r.positions[col]; ok {
			return i
		}
		return -1
	}
	i := sort.Search(len(r.cells), func(i int) bool { return r.cells[i].Col >= col })
	if i < len(r.cells) && r.cells[i].Col == col {
		return i
	}
	return -1
}

func (r conditionalSourceRow) cell(col int) CellData {
	if i := r.position(col); i >= 0 {
		return r.cells[i]
	}
	return CellData{}
}

func (r conditionalSourceRow) appendRange(out *conditional.RangeData, row, first, last int) int {
	textBytes := 0
	add := func(c *CellData) {
		if c.CacheMissing || c.Type != "" || c.Raw != "" {
			out.Cells = append(out.Cells, conditional.RangeCell{Row: row, Col: c.Col, Cell: toConditionalOperand(*c)})
			textBytes += len(c.Raw) + len(c.Type)
		}
	}
	if r.positions != nil {
		for col := first; col <= last; col++ {
			if i, ok := r.positions[col]; ok {
				add(&r.cells[i])
			}
		}
		return textBytes
	}
	start := max(0, first-r.denseFirst)
	if r.denseFirst == 0 {
		start = sort.Search(len(r.cells), func(i int) bool { return r.cells[i].Col >= first })
	}
	for i := start; i < len(r.cells) && r.cells[i].Col <= last; i++ {
		add(&r.cells[i])
	}
	return textBytes
}

// Presentation-only cells belong to the returned row, never to the source
// slice retained for references from later rows of the same viewport.
func (r conditionalSourceRow) withExtra(extra []CellData) []CellData {
	if len(extra) == 0 {
		return r.cells
	}
	out := make([]CellData, 0, len(r.cells)+len(extra))
	if r.positions != nil {
		out = append(out, r.cells...)
		out = append(out, extra...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Col < out[j].Col })
		return out
	}
	i := 0
	for _, c := range extra {
		start := i
		for i < len(r.cells) && r.cells[i].Col < c.Col {
			i++
		}
		out = append(out, r.cells[start:i]...)
		out = append(out, c)
	}
	return append(out, r.cells[i:]...)
}

func (r *conditionalRead) applyBatch(rows []RowData, first, last int, selected map[int]struct{}) error {
	if r == nil || len(rows) == 0 {
		return nil
	}
	r.batchFirst, r.batchLast, r.batchSelected = first, last, selected
	r.batchStart = rows[0].Number
	r.batchRows = make([]conditionalSourceRow, len(rows))
	for i := range rows {
		if rows[i].Number != r.batchStart+i && r.batchPositions == nil {
			r.batchPositions = make(map[int]int, len(rows))
			for j := 0; j < i; j++ {
				r.batchPositions[rows[j].Number] = j
			}
		}
		if r.batchPositions != nil {
			r.batchPositions[rows[i].Number] = i
		}
		r.batchRows[i] = indexConditionalRow(rows[i].Cells)
	}
	for i := range rows {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if err := r.applySource(&rows[i], r.batchRows[i], first, last, selected); err != nil {
			return err
		}
	}
	return nil
}

func (r *conditionalRead) batchRow(row int) (conditionalSourceRow, bool) {
	i := row - r.batchStart
	if r.batchPositions != nil {
		var ok bool
		i, ok = r.batchPositions[row]
		if !ok {
			return conditionalSourceRow{}, false
		}
	}
	if i < 0 || i >= len(r.batchRows) {
		return conditionalSourceRow{}, false
	}
	return r.batchRows[i], true
}
