package workbook

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"excellens/internal/searchdsl"
)

// SearchSheetAdvanced evaluates boolean predicates on the SAME physical row.
// Values from different rows must never be combined into a false match.
// Only source XML is read; no workbook, calculation or macro is executed.
func (w *Workbook) SearchSheetAdvanced(idx *SheetIndex, sheet, query string, limit int, cancel <-chan struct{}, progress func(int, int, []SearchResult)) ([]SearchResult, bool, error) {
	ctx, stop := searchContext(cancel)
	defer stop()
	state, err := w.SearchSheetResumable(ctx, idx, sheet, query, "advanced", limit, SearchCheckpoint{}, nil, progress)
	if err != nil {
		return nil, false, err
	}
	return state.Matches, state.Truncated, nil
}

func (w *Workbook) searchAdvancedResumable(ctx context.Context, idx *SheetIndex, sheet, query string, limit int, state SearchCheckpoint, save SearchCheckpointSave, progress func(int, int, []SearchResult), stream *searchStream) (SearchCheckpoint, error) {
	expr, err := searchdsl.Parse(query)
	if err != nil {
		return state, err
	}
	if idx == nil {
		return state, errors.New("源工作表缺少索引")
	}
	if limit < 1 || limit > 10000 {
		return state, errors.New("结果数量限制无效")
	}
	budget, err := newSearchHitBudget(save, state.Matches, stream)
	if err != nil {
		return state, err
	}
	headers := map[int]string{}
	first := idx.FindRowOffset(1)
	if first < idx.RowCount() && idx.RowAt(first).Number == 1 {
		header, err := w.ReadRow(idx, first, 1, min(idx.MaxCol, 16384))
		if err != nil {
			return state, err
		}
		for _, c := range header.Cells {
			headers[c.Col] = c.Display
		}
	}
	schema := searchdsl.NewSchema(headers, idx.MaxCol)
	if err := expr.CheckSchema(schema); err != nil {
		return state, err
	}
	columns := expr.Columns(schema)
	if len(columns) > 512 {
		return state, fmt.Errorf("工作表有%d列；无指定列的全行搜索最多512列，请增加列条件", len(columns))
	}
	// A scoped query can use the sparse row reader. Unscoped queries use the
	// verified full-range reader; never silently omit columns beyond a limit.
	sort.Ints(columns)
	matches := state.Matches
	scanned := state.Next
	total := idx.RowCount()
	if state.Next == total {
		state.Complete = true
		return state, saveSearchCheckpoint(state, save, true, stream)
	}
	for start := state.Next; start < total; start += 128 {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		end := min(start+128, total)
		var rows []RowData
		if len(columns) <= 80 {
			indices := make([]int, end-start)
			for i := range indices {
				indices[i] = start + i
			}
			rows, err = w.ReadRowsSparseContext(ctx, idx, indices, columns)
		} else {
			// A one-time full-range read is more predictable than repeatedly
			// opening an XML file for groups of 80 columns.
			rows, err = w.ReadRowsContext(ctx, idx, start, end-start, 1, columns[len(columns)-1])
		}
		if err != nil {
			return state, err
		}
		if len(rows) != end-start {
			return state, errors.New("搜索读取记录数与来源索引不一致")
		}
		for offset, row := range rows {
			if row.Number != idx.RowAt(start+offset).Number {
				return state, errors.New("搜索读取顺序与来源索引不一致")
			}
			scanned++
			cells := make(map[int]searchdsl.Cell, len(row.Cells))
			for _, cell := range row.Cells {
				cells[cell.Col] = searchdsl.Cell{Column: cell.Col, Display: cell.Display, Raw: cell.Raw, Formula: cell.Formula,
					Numeric: cell.Type == "n" || cell.Type == "", MissingCache: cell.CacheMissing}
			}
			if expr.Matches(searchdsl.Row{Sheet: sheet, Cells: cells}, schema) {
				col := 1
				if len(columns) == 1 {
					col = columns[0]
				} else {
					// Point the user to a populated source cell on that row.
					for _, c := range row.Cells {
						if c.Raw != "" || c.Formula != "" {
							col = c.Col
							break
						}
					}
				}
				ref := fmt.Sprintf("%s%d", ColumnName(col), row.Number)
				matched := cells[col]
				hit := SearchResult{Sheet: sheet, Ref: ref, Row: row.Number, Col: col, Value: matched.Display, Formula: matched.Formula}
				if err := budget.collect(&matches, hit); err != nil {
					return state, err
				}
				if len(matches) >= limit {
					state.Matches, state.Complete, state.Truncated = matches, true, true
					if err := saveSearchCheckpoint(state, save, true, stream); err != nil {
						return state, err
					}
					if progress != nil {
						progress(scanned, total, matches)
					}
					return state, nil
				}
			}
			state.Next, state.Matches, state.Complete = scanned, matches, scanned == total
			if err := saveSearchCheckpoint(state, save, state.Complete, stream); err != nil {
				return state, err
			}
		}
		if progress != nil {
			progress(scanned, total, matches)
		}
	}
	return state, nil
}
