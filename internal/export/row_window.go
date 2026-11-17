package export

import (
	"context"
	"errors"
	"iter"

	"excellens/internal/workbook"
)

type rangedRowSequence interface {
	RowSequence
	RowsRange(offset, count int) iter.Seq[workbook.SourceRowReference]
}

// Read a confirmed ordinal window directly. Output coordinates still follow
// the original complete selection, never the window's temporary origin.
func forEachRowWindow(ctx context.Context, wb *workbook.Workbook, s Selection, offset, count int64, options workbook.RowReadOptions, emit func(original, output int, row workbook.RowData) error) error {
	if offset < 0 || count < 0 || offset > s.RowCount() || count > s.RowCount()-offset {
		return ErrResumeState
	}
	seen := int64(0)
	visit := func(row workbook.RowData) error {
		if seen >= count {
			return errors.New("导出恢复行流超出已确认数量")
		}
		out := row.Number
		if s.Filtered {
			out = int(offset+seen) + 1
		} else if s.Compact {
			out = row.Number - s.FirstRow + 1
		}
		seen++
		return emit(row.Number, out, row)
	}
	var err error
	switch {
	case s.Rows != nil:
		var refs iter.Seq[workbook.SourceRowReference]
		if ranged, ok := s.Rows.(rangedRowSequence); ok {
			refs = ranged.RowsRange(int(offset), int(count))
		} else {
			refs = func(yield func(workbook.SourceRowReference) bool) {
				position := int64(0)
				for ref := range s.Rows.Rows() {
					if ctx.Err() != nil || position >= offset+count {
						return
					}
					position++
					if position > offset && !yield(ref) {
						return
					}
				}
			}
		}
		err = wb.VisitRowsByReferencesWithOptionsContext(ctx, s.Index, refs, s.FirstCol, s.LastCol, options, visit)
	case s.Filtered && s.RowNumbers == nil:
		refs := func(yield func(workbook.SourceRowReference) bool) {
			for position := range s.Order.IterRange(int(offset), int(count)) {
				if !yield(workbook.SourceRowReference{Number: s.Index.RowAt(position).Number, Position: position}) {
					return
				}
			}
		}
		err = wb.VisitRowsByReferencesWithOptionsContext(ctx, s.Index, refs, s.FirstCol, s.LastCol, options, visit)
	default:
		numbers := func(yield func(int) bool) {
			for n := offset; n < offset+count; n++ {
				number := s.FirstRow + int(n)
				if s.RowNumbers != nil {
					number = s.RowNumbers[int(n)]
				}
				if !yield(number) {
					return
				}
			}
		}
		err = wb.VisitRowsByNumbersWithOptionsContext(ctx, s.Index, numbers, s.FirstCol, s.LastCol, options, visit)
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if seen != count {
		return errors.New("导出恢复行流未完整读取")
	}
	return nil
}
