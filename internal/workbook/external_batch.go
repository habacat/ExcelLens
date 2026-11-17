package workbook

import (
	"context"
	"errors"
	"io"
	"os"
)

// Bounded read coalescing turns a contiguous viewport into one disk request.
// Arbitrary filtered/sorted order is preserved; only adjacent source ranges
// share a block. Cancellation is checked between every decoded row.
func (w *Workbook) readIndexedBatch(ctx context.Context, f *os.File, idx *SheetIndex, indices []int, first, last int, selected map[int]struct{}) ([]RowData, error) {
	return w.readIndexedBatchWithOptions(ctx, f, idx, indices, first, last, selected, RowReadOptions{FormatValue: true, IncludeFormula: true})
}

func (w *Workbook) readIndexedBatchWithOptions(ctx context.Context, f *os.File, idx *SheetIndex, indices []int, first, last int, selected map[int]struct{}, options RowReadOptions) ([]RowData, error) {
	options = prepareRichRead(options)
	presentation, err := w.newConditionalRead(ctx, idx, f, options)
	if err != nil {
		return nil, err
	}
	rows := make([]RowData, 0, len(indices))
	var block []byte
	const batchLimit = 4 << 20
	for start := 0; start < len(indices); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		at := indices[start]
		if at < 0 || at >= idx.RowCount() {
			return nil, errors.New("invalid row index")
		}
		base, end := idx.RowAt(at).Start, idx.RowAt(at).End
		stop := start + 1
		for stop < len(indices) && indices[stop] == indices[stop-1]+1 && indices[stop] < idx.RowCount() {
			next := idx.RowAt(indices[stop])
			if next.Start < end || next.Start-end > 4096 || next.End-base > batchLimit {
				break
			}
			end = next.End
			stop++
		}
		if base < 0 || end <= base || end > idx.XMLSize || end-base > maxExternalRowBytes {
			return nil, errors.New("外部行缓存偏移量无效")
		}
		size := int(end - base)
		if cap(block) < size {
			block = make([]byte, size)
		}
		if _, err := f.ReadAt(block[:size], base); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		for i := start; i < stop; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			off := idx.RowAt(indices[i])
			if off.Start < base || off.End <= off.Start || off.End > end {
				return nil, errors.New("外部行索引交叠或损坏")
			}
			var row RowData
			var err error
			if isExternalRows(idx.XMLPath) {
				row, err = w.decodeExternalRow(block[off.Start-base:off.End-base], off.Number, first, last, selected, options.FormatValue, options.IncludeFormula)
			} else {
				row, err = w.fastDecodeXMLRowWithOptions(f, idx, indices[i], first, last, selected, options, block[off.Start-base:off.End-base])
			}
			if err != nil {
				return nil, err
			}
			if presentation != nil {
				if err := presentation.apply(&row, first, last, selected); err != nil {
					return nil, err
				}
			}
			rows = append(rows, row)
		}
		start = stop
	}
	return rows, nil
}
