package workbook

import (
	"errors"
	"io"
	"os"
)

// Scan-local read-ahead: a million-row filter needs roughly one disk read per
// MiB instead of one read per row. Reordered/sparse source rows stay valid;
// unusual XML still uses the strict fallback in fastDecodeXMLRow.
type indexedStream struct {
	f          *os.File
	idx        *SheetIndex
	block      []byte
	start, end int64
	lastStart  int64
	hasLast    bool
}

const indexedReadAhead int64 = 1 << 20
const indexedRandomBlock int64 = 16 << 10

func (s *indexedStream) read(w *Workbook, row, first, last int, selected map[int]struct{}, format, formula bool) (RowData, error) {
	return s.readWithOptions(w, row, first, last, selected, RowReadOptions{FormatValue: format, IncludeFormula: formula})
}

func (s *indexedStream) readWithOptions(w *Workbook, row, first, last int, selected map[int]struct{}, options RowReadOptions) (RowData, error) {
	options = prepareRichRead(options)
	if row < 0 || row >= s.idx.RowCount() {
		return RowData{}, errors.New("invalid row index")
	}
	off := s.idx.RowAt(row)
	if off.Start < 0 || off.End <= off.Start || off.End > s.idx.XMLSize || off.End-off.Start > maxExternalRowBytes {
		return RowData{}, errors.New("行缓存范围无效")
	}
	if len(s.block) == 0 || off.Start < s.start || off.End > s.end {
		s.start = off.Start
		s.end = off.End
		distance := off.Start - s.lastStart
		switch {
		case !s.hasLast || distance >= 0 && distance <= indexedReadAhead/8:
			s.end = min(s.idx.XMLSize, max(off.End, off.Start+indexedReadAhead))
		case distance < 0 && -distance <= indexedReadAhead/8:
			// A reversed sort walks backwards through the source. Reading
			// forwards here would reload a MiB for every preceding tiny row.
			s.start = max(0, off.End-indexedReadAhead)
		default:
			// Scattered sort keys should not amplify each random row into a
			// MiB read. Align a small window; nearby rows can still reuse it.
			s.start = off.Start / indexedRandomBlock * indexedRandomBlock
			s.end = min(s.idx.XMLSize, max(off.End, s.start+indexedRandomBlock))
		}
		size := int(s.end - s.start)
		if cap(s.block) < size {
			s.block = make([]byte, size)
		}
		s.block = s.block[:size]
		if _, err := s.f.ReadAt(s.block, s.start); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return RowData{}, err
		}
	}
	s.lastStart, s.hasLast = off.Start, true
	data := s.block[off.Start-s.start : off.End-s.start]
	if isExternalRows(s.idx.XMLPath) {
		return w.decodeExternalRow(data, off.Number, first, last, selected, options)
	}
	return w.fastDecodeXMLRowWithOptions(s.f, s.idx, row, first, last, selected, options, data)
}
