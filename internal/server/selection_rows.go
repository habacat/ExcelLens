package server

import (
	"errors"
	"iter"
	"runtime"
	"sort"

	"excellens/internal/workbook"
)

var errSelectionLimit = errors.New("选区超过当前操作限额，请缩小选区")

// Dialogs can analyze either a captured visual selection or a manually typed
// source range. Keep this distinction explicit; a sorted view's endpoints
// cannot be interpreted as the intervening physical rows.
type analysisSelectionCoordinates struct {
	ViewID            string `json:"viewId,omitempty"`
	VisualStart       int    `json:"visualStart,omitempty"`
	VisualEnd         int    `json:"visualEnd,omitempty"`
	ExcludeThroughRow int    `json:"excludeThroughRow,omitempty"`
}

func analysisSelectionRows(idx *workbook.SheetIndex, filter *FilterResult, row1, row2 int, coords analysisSelectionCoordinates, limit int, views ...*sheetRowView) ([]int, error) {
	if coords.ViewID != "" && (len(views) != 1 || views[0] == nil || views[0].isClosed() || views[0].data.id != coords.ViewID) {
		return nil, errors.New("分析选区的可见行视图已失效，请重新选择")
	}
	visual := coords.ViewID != "" || coords.VisualStart != 0 || coords.VisualEnd != 0 || coords.ExcludeThroughRow != 0
	if visual && filter == nil && (len(views) == 0 || views[0] == nil) {
		return nil, errors.New("可视选区缺少有效筛选结果，请重新选择")
	}
	if visual {
		return sourceSelectionRows(idx, filter, row1, row2, coords.VisualStart, coords.VisualEnd, coords.ExcludeThroughRow, limit, views...)
	}
	if filter == nil {
		return sourceSelectionRows(idx, nil, row1, row2, 0, 0, 0, limit)
	}
	lo, hi := min(row1, row2), max(row1, row2)
	if filter.Rows.SourceRows() != idx.RowCount() {
		return nil, errors.New("筛选来源已变化，请重新筛选")
	}
	set := filter.Rows.Set()
	start, end := set.LowerBound(idx.FindRowOffset(lo)), set.LowerBound(idx.FindRowOffset(hi+1))
	if end-start > limit {
		return nil, errSelectionLimit
	}
	type selectedRow struct{ visual, number int }
	selected := make([]selectedRow, 0, end-start)
	for i := start; i < end; i++ {
		pos := set.At(i)
		if pos < 0 || pos >= idx.RowCount() {
			return nil, errors.New("筛选结果已失效，请重新筛选")
		}
		number := idx.RowAt(pos).Number
		at, found := filter.Rows.Position(pos)
		if !found || number < lo || number > hi {
			return nil, errors.New("筛选结果已失效，请重新筛选")
		}
		selected = append(selected, selectedRow{visual: at, number: number})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].visual < selected[j].visual })
	rows := make([]int, len(selected))
	for i, row := range selected {
		rows[i] = row.number
	}
	return rows, nil
}

// A filter owns immutable row order. At most 30 pinned source rows are removed
// from that order; keep their positions, not a second full-size row array.
type selectionRowView struct {
	idx     *workbook.SheetIndex
	filter  *FilterResult
	through int
	holes   []int
	visible *sheetRowView
	order   selectionRowOrder
	sorted  bool
}

type selectionRowOrder interface {
	Len() int
	SourceRows() int
	At(int) int
	Position(int) (int, bool)
	IterRange(int, int) iter.Seq[int]
}

func newSelectionRowView(idx *workbook.SheetIndex, filter *FilterResult, through int, views ...*sheetRowView) (selectionRowView, error) {
	v := selectionRowView{idx: idx, filter: filter, through: through}
	if idx == nil || through < 0 || through > 30 || len(views) > 1 {
		return v, errors.New("冻结行参数无效")
	}
	if filter != nil {
		v.order, v.sorted = filter.Rows, filter.Sorted
	}
	if len(views) == 1 && views[0] != nil {
		v.visible = views[0]
		if v.visible.isClosed() || v.visible.data.idx != idx || filter != nil && filter.ID != v.visible.data.filterID {
			return v, errors.New("可见行视图的来源已变化，请重新选择")
		}
		v.order, v.sorted = nil, v.visible.data.sorted
		if v.visible.data.projection != nil {
			v.order = v.visible.data.projection
		}
	}
	if v.order != nil && v.order.SourceRows() != idx.RowCount() {
		return v, errors.New("筛选来源已变化，请重新筛选")
	}
	if v.order == nil || through == 0 {
		return v, nil
	}
	if v.visible == nil && filter.PinnedPositions != nil {
		for _, at := range filter.PinnedPositions {
			if at < 0 || at >= filter.Rows.Len() {
				return v, errors.New("筛选结果已失效，请重新筛选")
			}
			pos := filter.Rows.At(at)
			if pos < 0 || pos >= idx.RowCount() {
				return v, errors.New("筛选结果已失效，请重新筛选")
			}
			if idx.RowAt(pos).Number <= through {
				v.holes = append(v.holes, at)
			}
		}
	} else {
		for row := 1; row <= through; row++ {
			pos := idx.FindRowOffset(row)
			if pos >= idx.RowCount() || idx.RowAt(pos).Number != row {
				continue
			}
			if at, found := v.order.Position(pos); found {
				v.holes = append(v.holes, at)
			}
		}
		sort.Ints(v.holes)
	}
	return v, nil
}

func (v selectionRowView) count() int {
	defer runtime.KeepAlive(v.visible)
	if v.order != nil {
		return v.order.Len() - len(v.holes)
	}
	if v.visible != nil {
		return max(0, v.visible.data.visibility.Before(v.idx.MaxRow+1)-v.visible.data.visibility.Before(v.through+1))
	}
	return max(0, v.idx.MaxRow-v.through)
}

func (v selectionRowView) underlying(visual int) int {
	for _, hole := range v.holes {
		if hole <= visual {
			visual++
		}
	}
	return visual
}

func (v selectionRowView) page(offset, limit int) ([]int, error) {
	defer runtime.KeepAlive(v.visible)
	if v.order == nil || offset < 0 || offset > v.count() || limit < 0 || limit > 10000 {
		return nil, errors.New("筛选读取范围无效")
	}
	rows := make([]int, min(limit, v.count()-offset))
	for i := range rows {
		pos := v.order.At(v.underlying(offset + i))
		if pos < 0 || pos >= v.idx.RowCount() {
			return nil, errors.New("筛选结果已失效，请重新筛选")
		}
		rows[i] = pos
	}
	return rows, nil
}

func (v selectionRowView) locate(row int) (int, bool) {
	defer runtime.KeepAlive(v.visible)
	if row <= v.through {
		return 0, false
	}
	pos := v.idx.FindRowOffset(row)
	if v.order == nil {
		if v.visible != nil {
			axis := v.visible.data.visibility
			return min(v.count(), max(0, axis.Before(row)-axis.Before(v.through+1))), row <= v.idx.MaxRow && axis.Visible(row)
		}
		return min(v.count(), row-v.through-1), row <= v.idx.MaxRow
	}
	if v.sorted {
		if pos >= v.idx.RowCount() || v.idx.RowAt(pos).Number != row {
			return v.count(), false
		}
		at, ok := v.order.Position(pos)
		if !ok {
			return v.count(), false
		}
		return at - sort.SearchInts(v.holes, at), true
	}
	var at int
	if v.visible == nil {
		at = v.filter.Rows.Set().LowerBound(pos)
	} else {
		at = sort.Search(v.order.Len(), func(i int) bool { return v.order.At(i) >= pos })
	}
	found := at < v.order.Len() && v.order.At(at) < v.idx.RowCount() && v.idx.RowAt(v.order.At(at)).Number == row
	return max(0, at-sort.SearchInts(v.holes, at)), found
}

// sourceSelectionRows preserves the endpoint/visual-index pairs of a drag.
// Frozen headers use source coordinates; the scrolling body uses visual order.
// The caller supplies a row budget, checked before any selection allocation.
func sourceSelectionRows(idx *workbook.SheetIndex, filter *FilterResult, row1, row2, visual1, visual2, through, limit int, views ...*sheetRowView) ([]int, error) {
	v, err := newSelectionRowView(idx, filter, through, views...)
	if err != nil {
		return nil, err
	}
	return v.rows(row1, row2, visual1, visual2, limit)
}

func (v selectionRowView) rows(row1, row2, visual1, visual2, limit int) ([]int, error) {
	plan, err := v.plan(row1, row2, visual1, visual2)
	if err != nil {
		return nil, err
	}
	if plan.Len() < 1 || plan.Len() > limit {
		return nil, errSelectionLimit
	}
	numbers := make([]int, 0, plan.Len())
	for number := range plan.Iter() {
		numbers = append(numbers, number)
	}
	return numbers, nil
}

func (v selectionRowView) plan(row1, row2, visual1, visual2 int) (selectionRowPlan, error) {
	defer runtime.KeepAlive(v.visible)
	empty := selectionRowPlan{}
	if row1 < 1 || row2 < 1 || row1 > workbook.MaxRowsForIndex(v.idx) || row2 > workbook.MaxRowsForIndex(v.idx) {
		return empty, errors.New("选区地址无效")
	}
	headerLo, headerHi := 0, -1
	lo, hi := min(row1, row2), max(row1, row2)
	if v.visible != nil {
		if !v.visible.data.visibility.Visible(row1) || !v.visible.data.visibility.Visible(row2) {
			return empty, errors.New("选区端点已隐藏，请重新选择")
		}
		if v.order != nil {
			for _, endpoint := range [][2]int{{row1, visual1}, {row2, visual2}} {
				if endpoint[0] <= v.through {
					continue
				}
				at := endpoint[1] - 1
				if at < 0 || at >= v.count() || v.idx.RowAt(v.order.At(v.underlying(at))).Number != endpoint[0] {
					return empty, errors.New("选区与可见行视图不匹配，请重新选择")
				}
			}
		}
	}
	if v.order != nil {
		pinned1, pinned2 := row1 <= v.through, row2 <= v.through
		switch {
		case pinned1 && pinned2:
			headerLo, headerHi, lo, hi = lo, hi, 0, -1
		case pinned1:
			headerLo, headerHi, lo, hi = row1, v.through, 1, visual2
		case pinned2:
			headerLo, headerHi, lo, hi = row2, v.through, 1, visual1
		default:
			lo, hi = min(visual1, visual2), max(visual1, visual2)
		}
		if !(pinned1 && pinned2) && (lo < 1 || hi < lo || hi > v.count()) {
			return empty, errors.New("筛选视图的选区已失效，请重新选择")
		}
	}
	if v.order == nil {
		return selectionRowPlan{view: v, sourceFirst: lo, sourceLast: hi, sourceCount: v.sourceRangeCount(lo, hi)}, nil
	}
	return selectionRowPlan{view: v, sourceFirst: headerLo, sourceLast: headerHi, sourceCount: v.sourceRangeCount(headerLo, headerHi), orderFirst: max(0, lo-1), orderCount: max(0, hi-lo+1)}, nil
}

func (v selectionRowView) sourceRangeCount(first, last int) int {
	if first < 1 || last < first {
		return 0
	}
	if v.visible != nil {
		axis := v.visible.data.visibility
		return axis.Before(last+1) - axis.Before(first)
	}
	return last - first + 1
}

func (v selectionRowView) appendSourceRange(out []int, first, count int) []int {
	if v.visible == nil {
		for i := 0; i < count; i++ {
			out = append(out, first+i)
		}
	} else {
		axis := v.visible.data.visibility
		at := axis.Before(first)
		for i := 0; i < count; i++ {
			out = append(out, axis.At(at+i))
		}
	}
	return out
}

func (v selectionRowView) numbersPage(offset, limit int) ([]int, error) {
	defer runtime.KeepAlive(v.visible)
	count := v.count()
	if offset < 0 || offset > count || limit < 0 || limit > 10000 {
		return nil, errors.New("可见行读取范围无效")
	}
	if offset == count || limit == 0 {
		return []int{}, nil
	}
	if v.order == nil {
		first := v.through + 1 + offset
		if v.visible != nil {
			first = v.visible.data.visibility.At(v.visible.data.visibility.Before(v.through+1) + offset)
		}
		return v.appendSourceRange(make([]int, 0, min(limit, v.count()-offset)), first, min(limit, v.count()-offset)), nil
	}
	positions, err := v.page(offset, limit)
	if err != nil {
		return nil, err
	}
	for i, pos := range positions {
		positions[i] = v.idx.RowAt(pos).Number
	}
	return positions, nil
}

func (v selectionRowView) sourceWindow(first, limit int) []int {
	defer runtime.KeepAlive(v.visible)
	first = max(first, v.through+1)
	count := min(limit, v.sourceRangeCount(first, v.idx.MaxRow))
	return v.appendSourceRange(make([]int, 0, count), first, count)
}
