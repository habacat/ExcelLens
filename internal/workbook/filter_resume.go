package workbook

import (
	"context"
	"errors"
	"time"

	"excellens/internal/formats"
)

// FilterCheckpoint counts complete immutable source-index records. Matched
// includes retained source headers. Its positions are stored by the caller.
type FilterCheckpoint struct {
	Next     int  `json:"next"`
	Matched  int  `json:"matched"`
	Complete bool `json:"complete"`
}

type FilterCheckpointSave func(FilterCheckpoint, []int) error

// FilterSheetResumable emits only new positions in source order. Sorting keys
// are validated here but read only in the later SortFilteredRowOrder stage.
// Caller must bind source/index/rules and verify all previously saved positions.
func (w *Workbook) FilterSheetResumable(ctx context.Context, idx *SheetIndex, rules []FilterRule, opts FilterOptions, previous FilterCheckpoint, save FilterCheckpointSave, progress func(int, int)) (FilterCheckpoint, error) {
	if _, err := validatedFilterKeys(idx, rules, opts); err != nil {
		return previous, err
	}
	if previous.Next < 0 || previous.Next > idx.RowCount() || previous.Matched < 0 || previous.Matched > previous.Next || previous.Complete && previous.Next != idx.RowCount() {
		return previous, errors.New("筛选恢复游标与来源索引不一致")
	}
	if err := ctx.Err(); err != nil {
		return previous, err
	}
	if previous.Complete {
		return previous, nil
	}
	state := previous
	if state.Next == idx.RowCount() {
		state.Complete = true
		if save != nil {
			return state, save(state, []int{})
		}
		return state, nil
	}
	scanner, err := newFilterScanner(w, idx, rules, nil, state.Next == 0)
	if err != nil {
		return state, err
	}
	defer scanner.close()
	var added []int
	if save != nil {
		added = make([]int, 0, formats.CheckpointFilterRows)
	}
	lastReport := time.Now()
	err = scanner.scan(ctx, state.Next, opts.HeaderRow, func(position int, _ bool) error {
		state.Matched++
		if save != nil {
			added = append(added, position)
		}
		return nil
	}, func(next int) error {
		state.Next, state.Complete = next, next == idx.RowCount()
		if save != nil && (state.Complete || next%formats.CheckpointFilterRows == 0) {
			if err := save(state, append([]int{}, added...)); err != nil {
				return err
			}
			added = added[:0]
		}
		if progress != nil && (state.Complete || next%formats.CheckpointFilterRows == 0 && time.Since(lastReport) >= 250*time.Millisecond) {
			progress(next, idx.RowCount())
			lastReport = time.Now()
		}
		return nil
	})
	return state, err
}

// SortFilteredRowOrder rebuilds only the sorting stage from a complete,
// immutable source-ordered filter result. It reuses the ordinary comparator,
// source-type rules, column cache and bounded external sorter.
func (w *Workbook) SortFilteredRowOrder(ctx context.Context, idx *SheetIndex, filtered *RowOrder, opts FilterOptions, progress func(int, int)) (*RowOrder, error) {
	keys, err := validatedFilterKeys(idx, nil, opts)
	if err != nil || len(keys) == 0 {
		return nil, errors.Join(err, errors.New("排序阶段缺少有效排序规则"))
	}
	rows, err := filtered.Retain()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.SourceRows() != idx.RowCount() {
		return nil, errors.New("排序阶段行序与来源索引不一致")
	}
	whole := rows.Len() == idx.RowCount() && rows.data.increasing
	scanner, err := newFilterScanner(w, idx, nil, keys, whole)
	if err != nil {
		return nil, err
	}
	defer scanner.close()
	order, err := newRowOrderBuilder(filterOrderDirectory(w), idx.RowCount(), residentOrderRows, &processQueryQuota)
	if err != nil {
		return nil, err
	}
	defer order.abort()
	sorter, err := newRowSorter(filterOrderDirectory(w), idx.RowCount(), keys, formats.QuerySortMemoryBytes, &processQueryQuota)
	if err != nil {
		return nil, err
	}
	defer sorter.abort()
	rowKeys := make([]sortValue, len(keys))
	previous, visited := -1, 0
	lastReport := time.Now()
	for position := range rows.Iter() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if position <= previous || position < 0 || position >= idx.RowCount() {
			return nil, errors.New("排序阶段需要完整且按源序排列的筛选结果")
		}
		previous = position
		if err := scanner.read(position); err != nil {
			return nil, err
		}
		if idx.RowAt(position).Number <= opts.HeaderRow {
			err = order.add(ctx, position)
		} else {
			err = addFilterSortRecord(ctx, sorter, scanner, keys, rowKeys, position)
		}
		if err != nil {
			return nil, err
		}
		visited++
		if progress != nil && (visited == rows.Len() || visited%4096 == 0 && time.Since(lastReport) >= 250*time.Millisecond) {
			progress(visited, rows.Len())
			lastReport = time.Now()
		}
	}
	scanner.publishColumns()
	clear(rowKeys)
	if err := sorter.finish(ctx, func(position int) error { return order.add(ctx, position) }, opts.Phase); err != nil {
		return nil, err
	}
	if opts.Phase != nil {
		opts.Phase("正在建立行号定位", 0, 0)
	}
	return order.finish(ctx)
}
