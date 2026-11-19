package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

type pivotPageGrid struct {
	rows, cols int
	over       bool
}

func pageGrid(count, wrap int, over bool) pivotPageGrid {
	if count == 0 {
		return pivotPageGrid{}
	}
	if wrap <= 0 {
		wrap = count
	}
	first := min(wrap, count)
	rest := (count-1)/first + 1
	if over {
		return pivotPageGrid{rest, first, true}
	}
	return pivotPageGrid{first, rest, false}
}
func (g pivotPageGrid) area(body cellRect, i int) cellRect {
	row, col := 0, 0
	if g.over {
		row, col = i/g.cols, i%g.cols
	} else {
		row, col = i%g.rows, i/g.rows
	}
	r, c := body.firstRow-1-g.rows+row, body.firstCol+3*col
	return cellRect{firstRow: r, lastRow: r, firstCol: c, lastCol: c + 1}
}
func (t pivotTableXML) oldPageAreas(body cellRect) ([]cellRect, error) {
	count := len(t.PageFields)
	grid := pageGrid(count, t.Wrap, isTrue(t.Over))
	if count == 0 {
		return nil, nil
	}
	if t.Area.PageRows > 0 && t.Area.PageRows != grid.rows || t.Area.PageCols > 0 && t.Area.PageCols != grid.cols {
		return nil, errors.New("分页区域的保存位置与布局不一致")
	}
	areas := make([]cellRect, 0, count)
	for i := 0; i < count; i++ {
		r := grid.area(body, i)
		if r.firstRow < 1 || r.lastCol > formats.ExcelColumns {
			return nil, ErrXLSX
		}
		areas = append(areas, r)
	}
	return areas, nil
}

// Register the cells occupied by page filters, not every cell above the body.
// Unrecognised producer layouts retain a conservative read-only band.
func (p *xlsxPackage) registerPivotAreas(sheet, part string, body cellRect, raw []byte, hasPages bool) {
	add := func(r cellRect) {
		p.pivotRanges[sheet] = append(p.pivotRanges[sheet], ownedRange{area: r, reason: lockPivotResult, pivot: part})
	}
	add(body)
	if !hasPages {
		return
	}
	var t pivotTableXML
	if err := xml.Unmarshal(raw, &t); err == nil {
		if areas, err := t.oldPageAreas(body); err == nil && len(areas) > 0 {
			for _, r := range areas {
				add(r)
			}
			return
		}
	}
	if body.firstRow > 1 {
		add(cellRect{1, 1, body.firstRow - 1, formats.ExcelColumns})
	}
}

func (p pivotPlan) owns(row, col int) bool {
	if p.old.contains(row, col) {
		return true
	}
	for _, r := range p.oldPages {
		if r.contains(row, col) {
			return true
		}
	}
	return false
}

func (g pivotCanvas) cover(ctx context.Context, r cellRect) error {
	if r.firstRow < 1 || r.firstCol < 1 || r.lastRow > formats.ExcelRows || r.lastCol > formats.ExcelColumns || int64(r.lastRow-r.firstRow+1)*int64(r.lastCol-r.firstCol+1) > formats.EditCalcSourceCells {
		return errors.New("透视结果区域超出工作表或刷新容量")
	}
	for row := r.firstRow; row <= r.lastRow; row++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for col := r.firstCol; col <= r.lastCol; col++ {
			g.set(row, col, editdoc.Value{Kind: "blank"})
		}
	}
	return nil
}
