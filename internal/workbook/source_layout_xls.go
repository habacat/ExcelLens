package workbook

import (
	"context"
	"errors"
	"fmt"
	"io"

	"excellens/internal/formats"
)

func (s *SourceLayout) captureBIFF(id uint16, data []byte) error {
	switch id {
	case 0x00E5: // MERGEDCELLS may occur after the entire cell table.
		if len(data) < 2 {
			return errors.New("XLS 合并区域记录截断")
		}
		count := int(xlsUint16(data, 0))
		if len(data) != 2+8*count || len(s.Merges)+count > 100000 {
			return errors.New("XLS 合并区域数量或记录长度无效")
		}
		for i := 0; i < count; i++ {
			at := 2 + 8*i
			m := CellMerge{Row: int(xlsUint16(data, at)) + 1, EndRow: int(xlsUint16(data, at+2)) + 1, Col: int(xlsUint16(data, at+4)) + 1, EndCol: int(xlsUint16(data, at+6)) + 1}
			if m.EndRow < m.Row || m.EndCol < m.Col || m.EndCol > 256 {
				return errors.New("XLS 合并区域坐标无效")
			}
			s.Merges = append(s.Merges, m)
		}
	case 0x0225: // DEFAULTROWHEIGHT, twips.
		if len(data) < 4 {
			return errors.New("XLS 默认行高记录截断")
		}
		s.ZeroHeight = xlsUint16(data, 0)&2 != 0
		s.DefaultRowZeroSize = xlsUint16(data, 2) == 0
		if s.DefaultRowZeroSize {
			s.DefaultRow = 0
		}
		if size := float64(xlsUint16(data, 2)) / 15; validSourceSize(size) {
			s.DefaultRow = size
		}
	case 0x0055, 0x0099: // DEFCOLWIDTH / STANDARDWIDTH.
		if len(data) < 2 {
			return errors.New("XLS 默认列宽记录截断")
		}
		width := float64(xlsUint16(data, 0))
		if id == 0x0099 {
			width /= 256
		}
		s.DefaultColZeroSize = width == 0
		if s.DefaultColZeroSize {
			s.DefaultCol = 0
		}
		size := sourceColumnPixels(width, s.digitWidth)
		if id == 0x0099 {
			size = storedColumnPixels(width, s.digitWidth)
		}
		if validSourceSize(size) {
			s.DefaultCol = size
		}
	case 0x007D: // COLINFO.
		if len(data) < 12 {
			return errors.New("XLS COLINFO 记录截断")
		}
		first, last := int(xlsUint16(data, 0))+1, int(xlsUint16(data, 2))+1
		if first < 1 || last > formats.BIFFColumns || first > last {
			return errors.New("XLS COLINFO 列范围无效")
		}
		flags := xlsUint16(data, 8)
		s.captureColumnAxisState(SourceAxisRange{Min: first, Max: last, Hidden: flags&1 != 0, HiddenSet: true, Level: uint8((flags >> 8) & 7), Collapsed: flags&0x0800 != 0, ZeroSize: xlsUint16(data, 4) == 0, PositiveSize: xlsUint16(data, 4) > 0}, formats.BIFFColumns)
		size := storedColumnPixels(float64(xlsUint16(data, 4))/256, s.digitWidth)
		if xlsUint16(data, 4) == 0 {
			for c := first; c <= last; c++ {
				delete(s.Cols, c)
			}
		}
		if validSourceSize(size) {
			if s.Cols == nil {
				s.Cols = make(map[int]float64)
			}
			for c := first; c <= last; c++ {
				s.Cols[c] = size
			}
		}
	case 0x0208: // ROW, height in twips; bit 15 means the default height.
		if len(data) < 16 {
			return errors.New("XLS ROW 记录截断")
		}
		height := xlsUint16(data, 6)
		row := int(xlsUint16(data, 0)) + 1
		flags := xlsUint16(data, 12)
		s.appendRowAxisState(SourceAxisRange{Min: row, Max: row, Hidden: flags&0x20 != 0, HiddenSet: true, Level: uint8(flags & 7), Collapsed: flags&0x10 != 0, ZeroSize: height == 0, PositiveSize: height&0x8000 == 0 && height > 0})
		if height&0x8000 == 0 {
			appendSourceRowSize(&s.Rows, int(xlsUint16(data, 0))+1, float64(height)/15)
		}
	case 0x0081: // WSBOOL: outline summaries below / right.
		if len(data) < 2 {
			return errors.New("XLS WSBOOL 记录截断")
		}
		below, right := xlsUint16(data, 0)&0x40 != 0, xlsUint16(data, 0)&0x80 != 0
		s.SummaryBelow, s.SummaryRight = &below, &right
	case 0x023E: // WINDOW2 display-gridlines bit.
		if len(data) < 2 {
			return errors.New("XLS WINDOW2 记录截断")
		}
		if s.GridLines == nil {
			on := xlsUint16(data, 0)&2 != 0
			s.GridLines = &on
		}
	}
	return nil
}

func (w *Workbook) readBIFFSourceLayout(ctx context.Context, meta SheetMeta) (SourceLayout, error) {
	layout := SourceLayout{digitWidth: w.normalDigitWidth()}
	if err := w.checkExternalUnchanged(); err != nil {
		return layout, err
	}
	var sheet *xlsSheet
	for i := range w.source.xlsSheets {
		if meta.Path == fmt.Sprintf("external/xls/%d", i) {
			sheet = &w.source.xlsSheets[i]
			break
		}
	}
	if sheet == nil {
		return layout, errors.New("XLS 工作表不存在")
	}
	cf, err := openCFB(w.Path)
	if err != nil {
		return layout, err
	}
	defer cf.Close()
	reader := newBIFFReader(cf.workbook, sheet.offset, cf.workbook.size)
	for {
		if err := ctx.Err(); err != nil {
			return SourceLayout{}, err
		}
		id, data, err := reader.next()
		if err == io.EOF {
			layout.finishMetadataCapture()
			return layout, nil
		}
		if err != nil {
			return layout, err
		}
		if id == 0x000A {
			layout.finishMetadataCapture()
			return layout, nil
		}
		if err = layout.captureBIFF(id, data); err != nil {
			return layout, err
		}
	}
}
