// Package searchservice scans explicit full-result queries in workbook order.
// Progress, source-index admission and checkpoint locking are supplied by the caller.
package searchservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
	"excellens/internal/searchresults"
	"excellens/internal/workbook"
)

type Sheet struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type Cursor struct {
	Sheet         int    `json:"sheet"`
	Index         string `json:"index"`
	Rows          int    `json:"rows"`
	Next          int    `json:"next"`
	Before        int    `json:"before"`
	Count         int    `json:"count"`
	ScannedBefore int    `json:"scannedBefore"`
	SheetComplete bool   `json:"sheetComplete"`
	Truncated     bool   `json:"truncated"`
}
type Plan struct {
	Sheets      []Sheet
	Query, Mode string
	Index       func(workbook.SheetMeta, string) (*workbook.SheetIndex, error)
	Stamp       func(workbook.SheetMeta, *workbook.SheetIndex) (string, error)
	Expected    func(string) string
	Progress    func(sheet, next, rows, before int, hits int64)
	Lease       *checkpoint.Lease
	Advance     func(checkpoint.Advance, *io.SectionReader) (checkpoint.Manifest, error)
}

func (p Plan) Scan(ctx context.Context, wb *workbook.Workbook, results *searchresults.Store) error {
	if len(p.Sheets) == 0 {
		return errors.New("没有可搜索的工作表")
	}
	var c Cursor
	var m checkpoint.Manifest
	if p.Lease != nil {
		m = p.Lease.Snapshot()
		if err := json.Unmarshal(m.Cursor, &c); err != nil {
			return err
		}
		if c.Sheet < 0 || c.Sheet >= len(p.Sheets) || c.Next < 0 || c.Rows < c.Next || c.Before < 0 || c.Count < c.Before || c.ScannedBefore < 0 || int64(c.Count) != m.Records || c.Truncated || m.Complete != (c.SheetComplete && c.Sheet == len(p.Sheets)-1) || c.SheetComplete && c.Next != c.Rows {
			return checkpoint.ErrCorrupt
		}
		if (m.Stage == "prepare") != (c.Index == "") || (m.Stage == "done") != m.Complete || c.Index == "" && (c.Next != 0 || c.Rows != 0 || c.Count != c.Before || c.SheetComplete) || c.Index != "" && len(c.Index) != 64 {
			return checkpoint.ErrCorrupt
		}
		if err := p.restoreHits(ctx, c, results); err != nil {
			return err
		}
	}
	confirmed := results.Size()
	commit := func(stage string, complete bool) error {
		if p.Lease == nil {
			return nil
		}
		r, err := results.Range(confirmed)
		if err != nil {
			return err
		}
		c.Count = int(results.Count())
		cursor, err := json.Marshal(c)
		if err != nil {
			return err
		}
		m, err = p.Advance(checkpoint.Advance{Revision: m.Revision, Stage: stage, Cursor: cursor, Bytes: r.Size(), Records: results.Count() - m.Records, Complete: complete}, r)
		if err == nil {
			confirmed = results.Size()
		}
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sheet := p.Sheets[c.Sheet]
		meta, ok := wb.FindSheet(sheet.Name)
		if !ok || meta.Path != sheet.Path {
			return checkpoint.ErrBinding
		}
		idx, err := p.Index(meta, c.Index)
		if err != nil {
			return err
		}
		if p.Lease != nil {
			stamp, err := p.Stamp(meta, idx)
			if err != nil {
				return err
			}
			if c.Index == "" {
				c.Index, c.Rows = stamp, idx.RowCount()
				if err = commit("scan", false); err != nil {
					return err
				}
			} else if stamp != p.Expected(c.Index) || c.Rows != idx.RowCount() {
				return checkpoint.ErrBinding
			}
		} else {
			c.Rows = idx.RowCount()
		}
		update := func(n int) { p.Progress(c.Sheet, n, c.Rows, c.ScannedBefore, results.Count()) }
		update(c.Next)
		if !c.SheetComplete {
			advance := func(next int, complete bool) error {
				if !complete && (p.Lease == nil || next%formats.CheckpointSearchRows != 0 && results.Size()-confirmed < 1<<20) {
					return nil
				}
				c.Next, c.SheetComplete = next, complete
				last := complete && c.Sheet == len(p.Sheets)-1
				stage := "scan"
				if complete && p.Lease != nil {
					stamp, e := p.Stamp(meta, idx)
					if e != nil {
						return e
					}
					if stamp != p.Expected(c.Index) {
						return checkpoint.ErrBinding
					}
				}
				if last {
					stage = "done"
				}
				return commit(stage, last)
			}
			if err = wb.StreamSearch(ctx, idx, sheet.Name, p.Query, p.Mode, c.Next, results.Append, advance, func(n, _ int, _ []workbook.SearchResult) { update(n) }); err != nil {
				return err
			}
		}
		update(c.Rows)
		if c.Sheet+1 == len(p.Sheets) {
			return results.Seal()
		}
		c = Cursor{Sheet: c.Sheet + 1, Before: int(results.Count()), Count: int(results.Count()), ScannedBefore: c.ScannedBefore + c.Rows}
		if err := commit("prepare", false); err != nil {
			return err
		}
	}
}

// The destination remains private until the final block chain and live indexes verify.
func (p Plan) restoreHits(ctx context.Context, c Cursor, s *searchresults.Store) error {
	positions := make(map[string]int, len(p.Sheets))
	for i, v := range p.Sheets {
		positions[v.Name] = i
	}
	prevSheet, prevRow, prevCol := -1, 0, 0
	var index *workbook.SheetIndex
	err := p.Lease.VisitCandidate(ctx, func(b checkpoint.Block, r io.Reader) error {
		before := s.Count()
		err := searchresults.Walk(ctx, r, b.Bytes, func(_ int64, h searchresults.Hit) error {
			sheet, ok := positions[h.Sheet]
			col, row, err := workbook.ParseCellRef(h.Ref)
			if err != nil || !ok || col != h.Col || row != h.Row || sheet < prevSheet || sheet > c.Sheet || (sheet < c.Sheet) != (s.Count() < int64(c.Before)) || sheet == prevSheet && (row < prevRow || row == prevRow && (col <= prevCol || p.Mode == "advanced")) {
				return checkpoint.ErrCorrupt
			}
			if sheet != prevSheet {
				stamp := ""
				if sheet == c.Sheet {
					stamp = c.Index
				}
				meta := workbook.SheetMeta{Name: h.Sheet, Path: p.Sheets[sheet].Path}
				index, err = p.Index(meta, stamp)
				if err != nil {
					return err
				}
			}
			at := index.FindRowOffset(row)
			if at >= index.RowCount() || index.RowAt(at).Number != row || sheet == c.Sheet && at >= c.Next {
				return checkpoint.ErrCorrupt
			}
			prevSheet, prevRow, prevCol = sheet, row, col
			return s.Append(h)
		})
		if err == nil && s.Count()-before != b.Records {
			return checkpoint.ErrCorrupt
		}
		return err
	})
	if err == nil && s.Count() != int64(c.Count) {
		return checkpoint.ErrCorrupt
	}
	return err
}
