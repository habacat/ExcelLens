package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"iter"
	"path/filepath"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type exportCheckpointSelections struct {
	selections []excelExport.Selection
	rows       []*workbook.SourceRowSnapshot
}

func (s *exportCheckpointSelections) close() error {
	if s == nil {
		return nil
	}
	var err error
	for _, row := range s.rows {
		err = errors.Join(err, row.Close())
	}
	return err
}

// This returns private candidates only after the entire block chain, every
// shape/digest, and the actual source/index have passed. No view is re-queried.
// The caller retains the Workbook/cache read lease until the returned owner is
// closed; input recovery does not publish a download or complete an export.
func readExportCheckpointSelections(ctx context.Context, wb *workbook.Workbook, lease *checkpoint.Lease) (out *exportCheckpointSelections, err error) {
	m := lease.Snapshot()
	spec, err := exportCheckpointSpecFor(m)
	if err != nil {
		return nil, err
	}
	cursor, err := exportCheckpointCursor(m, spec)
	if err != nil {
		return nil, err
	}
	if !cursor.Snapshot {
		return nil, errExportCaptureIncomplete
	}
	candidate := &exportCheckpointSelections{}
	defer func() {
		if err != nil {
			err = errors.Join(err, candidate.close())
		}
	}()
	if wb == nil {
		return nil, checkpoint.ErrBinding
	}
	policy, err := indexPolicy(wb, m)
	if err != nil {
		return nil, err
	}
	for _, sheet := range spec.Sheets {
		meta, found := wb.FindSheet(sheet.Name)
		if !found || meta.Path != sheet.Path {
			return nil, checkpoint.ErrBinding
		}
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil && policy.rebuild {
			if source, e := exportTaskSource(wb); e != nil {
				return nil, e
			} else if e = bindTaskSource(ctx, wb, source, m.Definition.Sources[0], true); e != nil {
				return nil, e
			}
			idx, err = wb.BuildIndex(meta, nil, ctx.Done())
			if err != nil {
				return nil, err
			}
		}
		if idx == nil {
			return nil, errors.New("导出恢复所需索引已被清理，请重新运行")
		}
		candidate.selections = append(candidate.selections, excelExport.Selection{Sheet: meta, Index: idx, FirstRow: sheet.FirstRow, LastRow: sheet.LastRow, FirstCol: sheet.FirstCol, LastCol: sheet.LastCol, Filtered: sheet.Filtered, Compact: sheet.Compact})
	}
	if err := checkExportSources(ctx, wb, m.Definition, spec, candidate.selections, policy); err != nil {
		return nil, err
	}
	var visitErr error
	blocks := func(yield func(checkpoint.Block, io.Reader) bool) {
		visitErr = lease.VisitCandidate(ctx, func(block checkpoint.Block, source io.Reader) error {
			if !yield(block, source) {
				return context.Canceled
			}
			return nil
		})
	}
	// Pull2 uses a synchronous coroutine, not an I/O goroutine or an unbounded
	// channel. A builder can span blocks while the lease verifies one pass.
	// Never call another lease method while this iterator is suspended.
	next, stop := iter.Pull2(blocks)
	defer stop()
	buffer := make([]byte, formats.CheckpointIOBytes)
	var inputBlocks uint64
	for i, sheet := range spec.Sheets {
		if !sheet.Filtered {
			continue
		}
		digest := sha256.New()
		rows, buildErr := workbook.BuildSourceRowSnapshot(ctx, filepath.Join(wb.CacheDir, "queries"), candidate.selections[i].Index, sheet.Rows, func(ctx context.Context, add func(int) error) error {
			seen := 0
			for seen < sheet.Rows {
				block, source, ok := next()
				if !ok {
					return errors.Join(checkpoint.ErrCorrupt, visitErr)
				}
				if block.Records < 1 || block.Records > formats.CheckpointOrderRows || block.Records > int64(sheet.Rows-seen) || block.Bytes != block.Records*4 {
					return checkpoint.ErrCorrupt
				}
				inputBlocks++
				for remaining := block.Bytes; remaining > 0; {
					if err := ctx.Err(); err != nil {
						return err
					}
					part := buffer[:int(min(int64(len(buffer)), remaining))]
					if _, err := io.ReadFull(source, part); err != nil {
						return err
					}
					_, _ = digest.Write(part)
					for offset := 0; offset < len(part); offset += 4 {
						if err := add(int(binary.LittleEndian.Uint32(part[offset:]))); err != nil {
							return err
						}
						seen++
					}
					remaining -= int64(len(part))
				}
			}
			return nil
		})
		if buildErr != nil {
			return nil, buildErr
		}
		candidate.rows = append(candidate.rows, rows)
		if hex.EncodeToString(digest.Sum(nil)) != sheet.RowHash {
			return nil, checkpoint.ErrCorrupt
		}
		candidate.selections[i].Rows = rows
	}
	if cursor.InputBlocks != 0 && inputBlocks != cursor.InputBlocks {
		return nil, checkpoint.ErrCorrupt
	}
	for {
		block, source, ok := next()
		if !ok {
			break
		}
		if cursor.Output == nil || block.Records > 1 {
			return nil, checkpoint.ErrCorrupt
		}
		if _, err := io.CopyBuffer(io.Discard, source, buffer); err != nil {
			return nil, err
		}
	}
	if visitErr != nil {
		return nil, visitErr
	}
	if err := checkExportSources(ctx, wb, m.Definition, spec, candidate.selections, policy); err != nil {
		return nil, err
	}
	if err := excelExport.Validate(spec.Options, candidate.selections); err != nil {
		return nil, err
	}
	return candidate, nil
}
