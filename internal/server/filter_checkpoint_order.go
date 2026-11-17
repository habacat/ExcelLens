package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type filterTaskCursor struct {
	Index          string `json:"index"`
	Rows           int    `json:"rows"`
	Next           int    `json:"next"`
	Matched        int    `json:"matched"`
	FilterComplete bool   `json:"filterComplete"`
	OrderWritten   int    `json:"orderWritten"`
}

func (spec filterCheckpointSpec) sorted() bool {
	return spec.Filter.SortColumn > 0 || len(spec.Filter.SortKeys) > 0
}

func filterCheckpointCursor(m checkpoint.Manifest, spec filterCheckpointSpec) (filterTaskCursor, error) {
	var c filterTaskCursor
	if len(m.Definition.Sources) != 1 {
		return c, checkpoint.ErrBinding
	}
	format, ok := formats.Lookup(m.Definition.Sources[0].Format)
	if !ok || decodeSavedConfig(m.Cursor, &c) != nil || c.Rows < 0 || c.Rows > format.MaxRows || c.Next < 0 || c.Next > c.Rows || c.Matched < 0 || c.Matched > c.Next || c.OrderWritten < 0 || c.OrderWritten > c.Matched || m.Records != int64(c.Matched)+int64(c.OrderWritten) {
		return c, checkpoint.ErrCorrupt
	}
	if c.Index == "" {
		if c.Rows != 0 || c.Next != 0 || c.Matched != 0 || c.FilterComplete || c.OrderWritten != 0 || m.Complete || m.Stage != "prepare" || m.DataBytes != 0 {
			return c, checkpoint.ErrCorrupt
		}
		return c, nil
	}
	if len(c.Index) != 64 || c.FilterComplete && c.Next != c.Rows || !c.FilterComplete && c.OrderWritten != 0 || !spec.sorted() && c.OrderWritten != 0 {
		return c, checkpoint.ErrCorrupt
	}
	complete := c.FilterComplete && (!spec.sorted() || c.OrderWritten == c.Matched)
	stage := "filter"
	if c.FilterComplete {
		stage = "sort"
	}
	if complete {
		stage = "done"
	}
	if m.Complete != complete || m.Stage != stage || m.Blocks > uint64(m.Records) {
		return c, checkpoint.ErrCorrupt
	}
	return c, nil
}

func encodeFilterPositions(positions []int) ([]byte, error) {
	if len(positions) > formats.CheckpointOrderRows {
		return nil, checkpoint.ErrCapacity
	}
	data := make([]byte, len(positions)*4)
	for i, position := range positions {
		if position < 0 || position >= formats.ExternalRows {
			return nil, checkpoint.ErrCorrupt
		}
		binary.LittleEndian.PutUint32(data[i*4:], uint32(position))
	}
	return data, nil
}

// Both row orders remain private until the entire digest/chain and row shapes
// succeed. One streaming pass fills the ordinary bounded checked builders; no
// source-sized position slice is retained on the Go heap.
func readFilterCheckpointOrders(ctx context.Context, wb *workbook.Workbook, lease *checkpoint.Lease, cursor filterTaskCursor) (filtered, ordered *workbook.RowOrder, err error) {
	dir := filepath.Join(wb.CacheDir, "queries")
	ordered, err = workbook.BuildRowOrder(ctx, dir, cursor.Rows, func(ctx context.Context, addOrder func(int) error) error {
		var innerErr error
		filtered, innerErr = workbook.BuildRowOrder(ctx, dir, cursor.Rows, func(ctx context.Context, addFilter func(int) error) error {
			var records int64
			previous := -1
			buffer := make([]byte, formats.CheckpointIOBytes)
			err := lease.VisitCandidate(ctx, func(block checkpoint.Block, source io.Reader) error {
				if block.Records < 1 || block.Records > formats.CheckpointOrderRows || block.Bytes != block.Records*4 || block.Records > int64(cursor.Matched)+int64(cursor.OrderWritten)-records {
					return checkpoint.ErrCorrupt
				}
				remaining := block.Bytes
				for remaining > 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
					part := buffer[:int(min(int64(len(buffer)), remaining))]
					if _, err := io.ReadFull(source, part); err != nil {
						return err
					}
					for offset := 0; offset < len(part); offset += 4 {
						position := int(binary.LittleEndian.Uint32(part[offset:]))
						if position >= cursor.Rows {
							return checkpoint.ErrCorrupt
						}
						if records < int64(cursor.Matched) {
							if position <= previous || position >= cursor.Next {
								return checkpoint.ErrCorrupt
							}
							previous = position
							if err := addFilter(position); err != nil {
								return err
							}
						} else if err := addOrder(position); err != nil {
							return err
						}
						records++
					}
					remaining -= int64(len(part))
				}
				return nil
			})
			if err != nil {
				return err
			}
			if records != int64(cursor.Matched)+int64(cursor.OrderWritten) {
				return checkpoint.ErrCorrupt
			}
			return nil
		})
		return innerErr
	})
	if err == nil {
		for position := range ordered.Iter() {
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
			if _, found := filtered.Position(position); !found {
				err = checkpoint.ErrCorrupt
				break
			}
		}
	}
	if err != nil {
		return nil, nil, errors.Join(err, filtered.Close(), ordered.Close())
	}
	return filtered, ordered, nil
}
