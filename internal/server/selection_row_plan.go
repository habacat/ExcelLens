package server

import (
	"iter"
	"runtime"

	"excellens/internal/workbook"
)

// A plan borrows its caller's retained selection snapshot. At most one source
// span and one visual span describe a selection, including its frozen prefix.
// No source-sized vector or map is built. Export owns the snapshot until done.
type selectionRowPlan struct {
	view                                 selectionRowView
	sourceFirst, sourceLast, sourceCount int
	orderFirst, orderCount               int
}

func (p selectionRowPlan) Index() *workbook.SheetIndex { return p.view.idx }
func (p selectionRowPlan) Len() int                    { return p.sourceCount + p.orderCount }

func (p selectionRowPlan) contiguousRange() (int, int, bool) {
	return p.sourceFirst, p.sourceLast, p.orderCount == 0 && p.sourceCount > 0 && p.sourceCount == p.sourceLast-p.sourceFirst+1
}

func (v selectionRowView) fullPlan() selectionRowPlan {
	if v.order == nil {
		last := max(v.idx.MaxRow, v.through)
		return selectionRowPlan{view: v, sourceFirst: 1, sourceLast: last, sourceCount: v.sourceRangeCount(1, last)}
	}
	return selectionRowPlan{view: v, sourceFirst: 1, sourceLast: v.through, sourceCount: v.sourceRangeCount(1, v.through), orderCount: v.count()}
}

func (p selectionRowPlan) Iter() iter.Seq[int] {
	return func(yield func(int) bool) {
		for ref := range p.Rows() {
			if !yield(ref.Number) {
				return
			}
		}
	}
}

// Rows carries the known index positions of ordered rows, avoiding one binary
// source-row lookup per record. Only genuinely absent source rows use -1.
func (p selectionRowPlan) Rows() iter.Seq[workbook.SourceRowReference] {
	return func(yield func(workbook.SourceRowReference) bool) {
		v := p.view
		defer runtime.KeepAlive(v.visible)
		defer runtime.KeepAlive(v.filter)
		if p.sourceCount > 0 {
			var numbers iter.Seq[int]
			if v.visible != nil {
				axis := v.visible.data.visibility
				numbers = axis.IterRange(axis.Before(p.sourceFirst), p.sourceCount)
			} else {
				numbers = func(yield func(int) bool) {
					for i := 0; i < p.sourceCount; i++ {
						if !yield(p.sourceFirst + i) {
							return
						}
					}
				}
			}
			position, previous, seen := 0, 0, false
			for number := range numbers {
				if seen && number == previous+1 {
					if position < v.idx.RowCount() && v.idx.RowAt(position).Number < number {
						position++
					}
				} else {
					position = v.idx.FindRowOffset(number)
				}
				previous, seen = number, true
				ref := workbook.SourceRowReference{Number: number, Position: -1}
				if position < v.idx.RowCount() && v.idx.RowAt(position).Number == number {
					ref.Position = position
				}
				if !yield(ref) {
					return
				}
			}
		}
		if p.orderCount == 0 {
			return
		}
		start := v.underlying(p.orderFirst)
		end := v.underlying(p.orderFirst+p.orderCount-1) + 1
		for position := range v.order.IterRange(start, end-start) {
			number := v.idx.RowAt(position).Number
			if number <= v.through {
				continue
			}
			if !yield(workbook.SourceRowReference{Number: number, Position: position}) {
				return
			}
		}
	}
}
