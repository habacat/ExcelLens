package workbook

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"excellens/internal/formats"
)

// Native cache search scans sequentially with one buffered reader. No XML
// decoding and no per-row file opens; search memory is bounded by one row.
func (w *Workbook) searchExternalResumable(ctx context.Context, idx *SheetIndex, sheet, query, mode string, limit int, state SearchCheckpoint, save SearchCheckpointSave, progress func(int, int, []SearchResult), stream *searchStream) (SearchCheckpoint, error) {
	matcher, err := newSearchMatcher(sheet, query, mode)
	if err != nil {
		return state, err
	}
	results := state.Matches
	budget, err := newSearchHitBudget(save, state.Matches, stream)
	if err != nil {
		return state, err
	}
	total := idx.RowCount()
	if state.Next == total {
		state.Complete = true
		return state, saveSearchCheckpoint(state, save, true, stream)
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return state, err
	}
	defer f.Close()
	if _, err = f.Seek(idx.RowAt(state.Next).Start, io.SeekStart); err != nil {
		return state, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	offset := idx.RowAt(state.Next).Start
	buf := make([]byte, 0, 4096)
	lastReport := time.Now()
	for i := state.Next; i < total; i++ {
		off := idx.RowAt(i)
		if i&63 == 0 || idx.odsRuns != nil {
			if err := ctx.Err(); err != nil {
				return state, err
			}
		}
		sameTemplate := idx.odsRuns != nil && len(buf) > 0 && off.End == offset && off.Start == offset-int64(len(buf))
		if skip := off.Start - offset; skip > 0 {
			if _, err = io.CopyN(io.Discard, br, skip); err != nil {
				return state, err
			}
			offset += skip
		} else if skip < 0 && !sameTemplate {
			return state, errors.New("索引交叠")
		}
		size := off.End - off.Start
		if size <= 0 || size > maxExternalRowBytes {
			return state, errors.New("外部行索引损坏")
		}
		if cap(buf) < int(size) {
			buf = make([]byte, size)
		}
		buf = buf[:int(size)]
		rowBytes := buf
		if !sameTemplate {
			if _, err = io.ReadFull(br, rowBytes); err != nil {
				return state, err
			}
			offset += size
		}
		row, err := w.decodeExternalRow(rowBytes, off.Number, 1, formats.ExcelColumns, nil, RowReadOptions{FormatValue: true, IncludeFormula: true})
		if err != nil {
			return state, fmt.Errorf("第 %d 行：%w", off.Number, err)
		}
		hitsBefore := budget.hits
		for _, cell := range row.Cells {
			matched := false
			if mode == "value" {
				matched = containsFold(cell.Raw, query) || (cell.Display != cell.Raw && containsFold(cell.Display, query))
			}
			if (mode == "formula" || mode == "reference") && cell.HasFormula && cell.Formula != "" {
				matched = matcher.matchFormula(strings.TrimPrefix(cell.Formula, "="))
			}
			if !matched {
				continue
			}
			hit := SearchResult{Sheet: sheet, Row: off.Number, Col: cell.Col, Ref: cell.Ref, Value: cell.Display}
			if mode != "value" {
				hit.Formula = cell.Formula
			}
			if err := budget.collect(&results, hit); err != nil {
				return state, err
			}
			if len(results) >= limit {
				state.Matches, state.Complete, state.Truncated = results, true, true
				if err := saveSearchCheckpoint(state, save, true, stream); err != nil {
					return state, err
				}
				if progress != nil {
					progress(i+1, total, results)
				}
				return state, nil
			}
		}
		if idx.odsRuns != nil && budget.hits == hitsBefore {
			// Simple search compares the stored values and formula text. Only
			// the result coordinate changes inside a repeated ODS template.
			run := idx.odsRunAt(i)
			i = min(total, run.Position+run.Count) - 1
		}
		state.Next, state.Matches, state.Complete = i+1, results, i+1 == total
		if err := saveSearchCheckpoint(state, save, state.Complete, stream); err != nil {
			return state, err
		}
		if progress != nil && (i == idx.RowCount()-1 || time.Since(lastReport) > 250*time.Millisecond) {
			progress(i+1, idx.RowCount(), results)
			lastReport = time.Now()
		}
	}
	return state, nil
}
