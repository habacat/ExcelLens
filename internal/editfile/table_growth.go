package editfile

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// The grid needs only the identity and the current rectangle. Column formulas,
// filter criteria and styles stay with the checked OOXML package.
type TableRange struct {
	Name   string `json:"name"`
	Sheet  string `json:"sheet"`
	Ref    string `json:"ref"`
	Totals int    `json:"totals,omitempty"`
}

func (l XLSXLayout) TableRanges() []TableRange {
	var ranges []TableRange
	for _, t := range l.Tables {
		ranges = append(ranges, TableRange{Name: t.Name, Sheet: t.Sheet, Ref: t.Ref, Totals: t.Totals})
	}
	return ranges
}

type TableGrowth struct {
	table     string
	sheet     string
	bounds    cellRect
	rowOffset int
}

// Only a contiguous grid input can append records. Clearing, formatting,
// filling and an isolated edit of a totals cell use their ordinary transactions.
func (l XLSXLayout) TableTail(changes []editdoc.Change) (*TableGrowth, error) {
	if len(changes) == 0 || len(l.Tables) == 0 {
		return nil, nil
	}
	first := changes[0].Address
	area := anchorRect(first)
	hasValue := false
	for _, c := range changes {
		if c.Sheet != first.Sheet || c.Value.Kind == "" {
			return nil, nil
		}
		area = area.hull(anchorRect(c.Address))
		hasValue = hasValue || c.Value.Kind != "blank"
	}
	if !hasValue || int64(area.lastRow-area.firstRow+1)*int64(area.lastCol-area.firstCol+1) != int64(len(changes)) {
		return nil, nil
	}
	var result *TableGrowth
	for _, t := range l.Tables {
		if t.Sheet != first.Sheet {
			continue
		}
		old := t.bounds()
		firstData, lastData := t.dataRows()
		if area.firstCol == old.lastCol+1 && area.firstRow >= old.firstRow && area.lastRow <= old.lastRow {
			if result != nil {
				return nil, errors.New("选区同时与多个表格相邻，请分别输入")
			}
			next := old
			next.lastCol = area.lastCol
			result = &TableGrowth{table: t.Name, sheet: t.Sheet, bounds: next}
			continue
		}
		if area.firstCol < old.firstCol || area.firstCol > old.lastCol || area.firstRow < firstData || area.firstRow > old.lastRow+1 || area.lastRow <= lastData {
			continue
		}
		if t.Totals > 0 && area.firstRow == old.lastRow && area.lastRow == old.lastRow {
			continue
		}
		offset := 0
		if area.firstRow == old.lastRow+1 {
			offset = -t.Totals
		}
		dest := old
		dest.lastRow = area.lastRow + offset + t.Totals
		// A paste may include adjacent worksheet columns. Leave them at the
		// chosen coordinates; a totals row cannot shift just half an input.
		if area.lastCol > old.lastCol && t.Totals > 0 {
			return nil, errors.New("追加明细时请将粘贴列放在同一个表格内")
		}
		if dest.lastRow > formats.ExcelRows {
			return nil, ErrTarget
		}
		if result != nil {
			return nil, errors.New("输入同时跨过多个表格的表尾，请分别粘贴")
		}
		result = &TableGrowth{t.Name, t.Sheet, dest, offset}
	}
	return result, nil
}

// A protected sheet can still accept an ordinary edit of an unlocked
// neighbour; do not convert that allowed edit into a structural command.
func (l Layout) GridTableTail(ctx context.Context, f *workbook.EditSourceFile, changes []editdoc.Change) (*TableGrowth, error) {
	if l.XLSX == nil {
		return nil, nil
	}
	g, err := l.XLSX.TableTail(changes)
	if err != nil || g == nil {
		return g, err
	}
	first := changes[0].Address
	cells, err := l.Read(ctx, f, []editdoc.Address{first})
	if err != nil {
		return nil, err
	}
	if cells[first].protected {
		return nil, nil
	}
	return g, nil
}

func (l XLSXLayout) resizeTarget(op editdoc.Structure) (xlsxTable, cellRect, error) {
	r, ok := parseRect(op.Range)
	if !ok || r.lastCol > formats.ExcelColumns {
		return xlsxTable{}, r, ErrTarget
	}
	for _, t := range l.Tables {
		if t.Sheet != op.Sheet || !strings.EqualFold(t.Name, op.Name) {
			continue
		}
		old := t.bounds()
		if old == r {
			return t, r, editdoc.ErrNoChange
		}
		if old.firstRow != r.firstRow || r.firstCol > old.lastCol || old.firstCol > r.lastCol || r.lastRow-r.firstRow+1 < t.Headers+t.Totals+1 {
			return t, r, errors.New("新范围须与原表格重叠，保持标题所在行，并至少包含一行明细")
		}
		for _, other := range l.Tables {
			if other.Sheet == t.Sheet && other.Part != t.Part && r.overlaps(other.bounds()) {
				return t, r, fmt.Errorf("新范围与表格 %s 重叠", other.Name)
			}
		}
		return t, r, nil
	}
	return xlsxTable{}, r, errors.New("请在当前工作表选择要调整的 Excel 表格")
}
