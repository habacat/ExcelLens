package workbook

import (
	"context"
	"errors"
	"fmt"
	"io"

	"excellens/internal/formats"
)

// Only expose the worksheet's own records. Embedded chart substreams have
// their own BOF/EOF pairs; their EOF must not terminate the parent worksheet.
type biffWorksheetReader struct {
	cf      *cfbReader
	reader  *biffReader
	start   int64
	depth   int
	started bool
	done    bool
	ctx     context.Context
	cancel  <-chan struct{}
}

// Older BIFF row caches can end at an embedded chart EOF. Changing only this
// adapter's path namespace forces an ordinary rebuild without altering XLIDX009
// or other formats' caches. Legacy derived files remain available for cleanup.
const biffRowReaderVersion = 3

func (w *Workbook) biffSheet(meta SheetMeta) (*xlsSheet, error) {
	if w.source != nil {
		for i := range w.source.xlsSheets {
			if meta.Path == fmt.Sprintf("external/xls/%d", i) && meta.Name == w.source.xlsSheets[i].name {
				return &w.source.xlsSheets[i], nil
			}
		}
	}
	return nil, errors.New("XLS 工作表索引不存在或来源不匹配")
}

func (w *Workbook) openBIFFWorksheet(meta SheetMeta) (*biffWorksheetReader, error) {
	sheet, err := w.biffSheet(meta)
	if err != nil {
		return nil, err
	}
	cf, err := openCFB(w.Path)
	if err != nil {
		return nil, err
	}
	end := cf.workbook.size
	for _, other := range w.source.xlsSheets {
		if other.offset > sheet.offset && other.offset < end {
			end = other.offset
		}
	}
	if sheet.offset < 0 || sheet.offset >= end {
		cf.Close()
		return nil, errors.New("BIFF8工作表子流范围无效")
	}
	return &biffWorksheetReader{cf: cf, reader: newBIFFReader(cf.workbook, sheet.offset, end), start: sheet.offset}, nil
}

func (r *biffWorksheetReader) next() (uint16, []byte, error) {
	for !r.done {
		if r.ctx != nil {
			if err := r.ctx.Err(); err != nil {
				return 0, nil, err
			}
		}
		select {
		case <-r.cancel:
			return 0, nil, errors.New("读取已取消")
		default:
		}
		id, data, err := r.reader.next()
		if err == io.EOF {
			err = errors.New("XLS 工作表缺少 EOF，源文件可能已截断")
		}
		if err != nil {
			return 0, nil, err
		}
		if !r.started || id == 0x0809 {
			if id != 0x0809 || len(data) < 4 || xlsUint16(data, 0) != 0x0600 || (!r.started && xlsUint16(data, 2) != 0x0010) {
				return 0, nil, errors.New("不是有效的 BIFF8 工作表或子流")
			}
			if r.started {
				r.depth++
				if r.depth > formats.BIFFSubstreamDepth {
					return 0, nil, errors.New("BIFF8嵌套子流层数超限")
				}
				// Expose a boundary, never child cell/table records. This also
				// prevents joining table continuations across another substream.
				return id, data, nil
			}
			r.started = true
			continue
		}
		if id == 0x000A {
			if len(data) != 0 {
				return 0, nil, errors.New("BIFF8子流结束记录长度无效")
			}
			if r.depth > 0 {
				r.depth--
				continue
			}
			r.done = true
		}
		if r.depth == 0 {
			return id, data, nil
		}
	}
	return 0, nil, io.EOF
}
