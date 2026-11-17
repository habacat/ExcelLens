package workbook

import (
	"context"
	"errors"
	"iter"
)

// SourceRowSnapshot owns source coordinates, including genuine sparse blanks.
// Its private order stores coordinate offsets, never source-index positions;
// callers cannot accidentally pass it as a record-based RowOrder. Storage,
// uniqueness checks, spill files and leases reuse the bounded order builder.
// The caller pins the Workbook and its precise index for this owner's lifetime.
type SourceRowSnapshot struct {
	index *SheetIndex
	rows  *RowOrder
}

func BuildSourceRowSnapshot(ctx context.Context, dir string, index *SheetIndex, count int, scan func(context.Context, func(int) error) error) (*SourceRowSnapshot, error) {
	if index == nil || count < 0 || count > MaxRowsForIndex(index) || scan == nil {
		return nil, errors.New("源行快照的索引或数量无效")
	}
	seen := 0
	rows, err := BuildRowOrder(ctx, dir, MaxRowsForIndex(index), func(ctx context.Context, add func(int) error) error {
		var admission error
		err := scan(ctx, func(number int) error {
			if admission != nil {
				return admission
			}
			if seen >= count || number < 1 || number > MaxRowsForIndex(index) {
				admission = errors.New("源行快照超出已确认范围或数量")
				return admission
			}
			admission = add(number - 1)
			if admission == nil {
				seen++
			}
			return admission
		})
		if err == nil && admission == nil && seen != count {
			admission = errors.New("源行快照未完整读取")
		}
		return errors.Join(err, admission)
	})
	if err != nil {
		return nil, err
	}
	return &SourceRowSnapshot{index: index, rows: rows}, nil
}

func (s *SourceRowSnapshot) Index() *SheetIndex { return s.index }
func (s *SourceRowSnapshot) Len() int           { return s.rows.Len() }

func (s *SourceRowSnapshot) Retain() (*SourceRowSnapshot, error) {
	if s == nil {
		return nil, errors.New("源行快照已经释放")
	}
	rows, err := s.rows.Retain()
	if err != nil {
		return nil, err
	}
	return &SourceRowSnapshot{index: s.index, rows: rows}, nil
}

func (s *SourceRowSnapshot) Close() error {
	if s == nil {
		return nil
	}
	return s.rows.Close()
}

func (s *SourceRowSnapshot) Rows() iter.Seq[SourceRowReference] {
	return s.RowsRange(0, s.Len())
}

// RowsRange starts at a confirmed ordinal without reading the earlier prefix.
func (s *SourceRowSnapshot) RowsRange(offset, count int) iter.Seq[SourceRowReference] {
	numbers := func(yield func(int) bool) {
		for position := range s.rows.IterRange(offset, count) {
			if !yield(position + 1) {
				return
			}
		}
	}
	return sourceRowReferences(s.index, numbers)
}
