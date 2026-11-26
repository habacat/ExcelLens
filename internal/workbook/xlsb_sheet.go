package workbook

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"excellens/internal/formats"
)

type xlsbMetaRecord struct {
	Kind uint16
	Data []byte
}

type xlsbSheetData struct {
	Layout          SourceLayout
	Dimension       CellMerge
	DefaultRowTwips uint16
	DefaultColRaw   uint32
	DefaultColBase  uint16
	PrintFit        *bool
	Columns         []xlsbColumn
	Drawing         string
	Records         []xlsbMetaRecord
	recordBytes     int
}

type xlsbColumn struct {
	Min, Max int
	Width    float64
	Style    int
}

func newXLSBSheetData(digits float64) *xlsbSheetData {
	return &xlsbSheetData{Layout: SourceLayout{Cols: make(map[int]float64), digitWidth: digits}}
}

func (m *xlsbSheetData) capture(kind uint16, data []byte) error {
	f := xlsbFields{data: data}
	switch kind {
	case brtWsDim:
		m.Dimension = f.area()
	case brtWsProp:
		bits := f.u16()
		summaryRows := bits&64 != 0
		summaryCols := bits&128 != 0
		printFit := bits&256 != 0
		m.PrintFit = &printFit
		m.Layout.SummaryBelow = &summaryRows
		m.Layout.SummaryRight = &summaryCols
	case brtBeginWsView:
		flags := f.u16()
		if m.Layout.GridLines == nil {
			lines := flags&4 != 0
			m.Layout.GridLines = &lines
		}
	case brtPane:
		x, y := f.f64(), f.f64()
		f.take(12) // first visible cell and active pane
		flags := f.u8()
		if flags&3 != 0 {
			if x < 0 || x >= formats.ExcelColumns || y < 0 || y >= formats.ExcelRows || math.Trunc(x) != x || math.Trunc(y) != y {
				return errors.New("XLSB 冻结窗格的行列坐标无效")
			}
			m.Layout.FrozenCols, m.Layout.FrozenRows = int(x), int(y)
		}
	case brtWsFmtInfo:
		width, fallback, height := f.u32(), f.u16(), f.u16()
		m.DefaultColRaw, m.DefaultColBase, m.DefaultRowTwips = width, fallback, height
		flags := f.u16()
		m.Layout.ZeroHeight = flags&2 != 0
		m.Layout.DefaultRowZeroSize = height == 0 && flags&1 != 0
		if h := float64(height) / 15; validSourceSize(h) {
			m.Layout.DefaultRow = h
		}
		col := defaultColumnPixels(float64(fallback), m.Layout.digitWidth)
		if width != ^uint32(0) {
			col = storedColumnPixels(float64(width)/256, m.Layout.digitWidth)
			m.Layout.DefaultColZeroSize = width == 0
		}
		if validSourceSize(col) {
			m.Layout.DefaultCol = col
		}
	case brtColInfo:
		first, last := int(f.u32())+1, int(f.u32())+1
		width, style, flags := f.u32(), f.u32(), f.u16()
		if first <= 0 || last < first || last > formats.ExcelColumns || width > math.MaxUint16 || style > formats.XLSXStyleRecords {
			return errors.New("XLSB 列范围无效")
		}
		state := SourceAxisRange{Min: first, Max: last, Hidden: flags&1 != 0, HiddenSet: true, Level: uint8((flags >> 8) & 7), Collapsed: flags&0x1000 != 0, ZeroSize: width == 0, PositiveSize: width > 0}
		size := storedColumnPixels(float64(width)/256, m.Layout.digitWidth)
		m.Layout.ColStates = append(m.Layout.ColStates, state)
		if validSourceSize(size) {
			for col := first; col <= last; col++ {
				m.Layout.Cols[col] = size
			}
		}
		m.Columns = append(m.Columns, xlsbColumn{Min: first, Max: last, Width: float64(width) / 256, Style: int(style)})
	case brtRowHdr:
		row := int(f.u32()) + 1
		f.u32() // row style is recorded with the row
		height := f.u16()
		f.u8()
		flags := f.u8()
		if row <= 0 || row > formats.ExcelRows {
			return errors.New("XLSB 行坐标无效")
		}
		custom := flags&32 != 0
		if size := float64(height) / 15; custom && validSourceSize(size) {
			appendSourceRowSize(&m.Layout.Rows, row, size)
		}
		if flags&31 != 0 || custom || m.Layout.ZeroHeight {
			m.Layout.appendRowAxisState(SourceAxisRange{Min: row, Max: row, Hidden: flags&16 != 0, HiddenSet: true, Level: flags & 7, Collapsed: flags&8 != 0, ZeroSize: custom && height == 0, PositiveSize: custom && height > 0})
		}
	case brtMergeCell:
		if len(m.Layout.Merges) >= 100000 {
			return errors.New("XLSB 合并区域数量超限")
		}
		m.Layout.Merges = append(m.Layout.Merges, f.area())
	case brtDrawing:
		m.Drawing = f.wide(true, 255)
	case brtHLink, brtBeginDVals, brtBeginDVals14, brtDVal, brtDVal14, brtListPart, brtPrintOptions, brtMargins, brtPageSetup, brtBeginHeaderFooter,
		brtBeginRwBrk, brtBeginColBrk, brtBrk, brtEndRwBrk, brtEndColBrk,
		brtBeginConditionalFormatting, brtEndConditionalFormatting, brtBeginCFRule, brtEndCFRule,
		brtBeginColorScale, brtEndColorScale, brtBeginDatabar, brtEndDatabar, brtBeginIconSet, brtEndIconSet, brtCFVO, brtColor,
		brtBeginConditionalFormatting14, brtEndConditionalFormatting14, brtBeginCFRule14, brtEndCFRule14,
		brtBeginDatabar14, brtBeginIconSet14, brtBeginColorScale14, brtCFVO14, brtColor14, brtCFIcon, brtCFRuleExt:
		m.recordBytes += len(data) + 8
		if m.recordBytes > maxMetadataPart || len(m.Records) >= formats.XLSXStyleRecords {
			return errors.New("XLSB 工作表元数据超过读取容量")
		}
		m.Records = append(m.Records, xlsbMetaRecord{kind, append([]byte(nil), data...)})
	}
	return f.err
}

type xlsbPosition struct {
	row, col int
}

type xlsbFormulaGroup struct {
	area      CellMerge
	formula   xlsbFormula
	arrayText string
	kind      uint16
}

type xlsbRowCell struct {
	externalCell
	anchor xlsbPosition
	group  bool
}

func (w *Workbook) streamXLSBRows(meta SheetMeta, writer *externalRowWriter, cancel <-chan struct{}, progress func(int64, int64, string)) (*xlsbSheetData, error) {
	data, err := w.walkXLSBRows(meta, 0, 0, cancel, progress, nil, func(row, style int, cells []externalCell) error {
		if row <= writer.rows {
			return errors.New("XLSB 行坐标顺序无效")
		}
		last := 0
		if len(cells) != 0 {
			last = cells[len(cells)-1].Col
		}
		writer.rows = row - 1
		return writer.cellsWithStyle(cells, last, style)
	})
	if err != nil {
		return nil, err
	}
	writer.rows = max(writer.rows, data.Dimension.EndRow)
	writer.cols = max(writer.cols, data.Dimension.EndCol)
	return data, nil
}

func (w *Workbook) walkXLSBRows(meta SheetMeta, previewRows, previewCols int, cancel <-chan struct{}, progress func(int64, int64, string), wrap func(io.Reader) io.Reader, emit func(int, int, []externalCell) error) (*xlsbSheetData, error) {
	pkg, parts, err := openXLSBParts(w.Path)
	if err != nil {
		return nil, err
	}
	defer pkg.Close()
	src := parts[meta.Path]
	if src == nil {
		return nil, errors.New("XLSB 工作表部件不存在")
	}
	b := w.source.xlsb
	var stringsLookup func(uint32) (xlsbString, error)
	if previewRows != 0 && parts[b.sharedStrings] != nil {
		get, close, err := xlsbPreviewStrings(parts[b.sharedStrings], wrap)
		if err != nil {
			return nil, err
		}
		defer close()
		stringsLookup = get
	} else {
		var sst *xlsbSST
		defer func() { sst.close() }()
		stringsLookup = func(id uint32) (xlsbString, error) {
			if sst == nil {
				var err error
				sst, err = w.xlsbStrings(parts[b.sharedStrings], cancel)
				if err != nil {
					return xlsbString{}, err
				}
			}
			return sst.get(id)
		}
	}
	td := newXLSBSheetData(w.normalDigitWidth())
	groups := make(map[xlsbPosition]xlsbFormulaGroup)
	expires := make(map[int][]xlsbPosition)
	groupBytes := 0
	raw := make([]xlsbRowCell, 0, 64)
	cells := make([]externalCell, 0, 64)
	row, style := -1, 0
	lastProgress := 0
	flush := func() error {
		if row < 0 {
			return nil
		}
		cells = cells[:0]
		for _, p := range raw {
			cell := p.externalCell
			if p.group {
				group, ok := groups[p.anchor]
				if !ok || row+1 < group.area.Row || row+1 > group.area.EndRow || cell.Col < group.area.Col || cell.Col > group.area.EndCol {
					cell.Formula = "[XLSB 共享或数组公式锚点无法核实]"
				} else if group.kind == brtTable {
					cell.Formula = "[XLSB 模拟运算表结果，实际算式在表头]"
				} else if group.kind == brtArrFmla {
					cell.Formula = group.arrayText
					if row+1 == group.area.Row && cell.Col == group.area.Col {
						area := group.area
						cell.ArrayFormula = &area
					} else {
						cell.ArrayFormulaMember = true
					}
				} else {
					cell.Formula, _ = b.formulaText(group.formula, row, cell.Col-1, true)
					cell.SharedFormula = true
				}
			}
			cells = append(cells, cell)
		}
		return emit(row+1, style, cells)
	}
	var begin, inData, dataDone, end bool
	err = readXLSBPartWithReader(src, 0, wrap, func(kind uint16, data []byte) error {
		select {
		case <-cancel:
			return context.Canceled
		default:
		}
		if !begin && kind != brtBeginSheet || end {
			return errors.New("BIFF12 工作表记录顺序无效")
		}
		if err := td.capture(kind, data); err != nil {
			return err
		}
		switch kind {
		case brtBeginSheet:
			if begin {
				return errors.New("BIFF12 工作表开始记录重复")
			}
			begin = true
		case brtEndSheet:
			end = true
		case brtBeginSheetData:
			if inData || dataDone {
				return errors.New("BIFF12 工作表数据区重复")
			}
			inData = true
		case brtEndSheetData:
			if !inData {
				return errors.New("BIFF12 工作表没有数据区开始记录")
			}
			if err := flush(); err != nil {
				return err
			}
			inData, dataDone = false, true
		case brtRowHdr:
			if !inData {
				return errors.New("BIFF12 行记录出现在数据区之外")
			}
			if err := flush(); err != nil {
				return err
			}
			f := xlsbFields{data: data}
			next, rowStyle := int(f.u32()), int(f.u32())
			f.take(3)
			flags := f.u8()
			f.u8()
			spans := f.u32()
			if f.err != nil || next <= row || next >= formats.ExcelRows || spans > 16 || len(f.data) != int(spans)*8 {
				return errors.New("BIFF12 行记录无效")
			}
			if previewRows != 0 && next >= previewRows {
				return errPreviewDone
			}
			for previous := max(0, row); len(expires) != 0 && previous < next; previous++ {
				for _, key := range expires[previous] {
					g := groups[key]
					groupBytes -= len(g.formula.tokens) + len(g.formula.extra)
					delete(groups, key)
				}
				delete(expires, previous)
			}
			clear(raw)
			row, raw, style = next, raw[:0], 0
			if flags&64 != 0 {
				if rowStyle < 0 || rowStyle != 0 && rowStyle >= len(w.Styles) {
					return errors.New("BIFF12 行样式索引无效")
				}
				style = rowStyle
			}
			if progress != nil && next-lastProgress >= 4096 {
				lastProgress = next
				progress(int64(next), int64(max(next, td.Dimension.EndRow)), "正在建立 XLSB 行索引")
			}
		case brtCellBlank, brtCellRk, brtCellError, brtCellBool, brtCellReal, brtCellSt, brtCellIsst, brtCellRString,
			brtFmlaString, brtFmlaNum, brtFmlaBool, brtFmlaError:
			if row < 0 || !inData {
				return errors.New("BIFF12 单元格缺少行坐标")
			}
			if previewCols != 0 && len(data) >= 8 && binary.LittleEndian.Uint32(data) >= uint32(previewCols) {
				return nil
			}
			cell, err := w.readXLSBCell(kind, data, stringsLookup, row)
			if err != nil {
				return err
			}
			if len(raw) != 0 && cell.Col <= raw[len(raw)-1].Col {
				return errors.New("BIFF12 单元格列坐标顺序无效")
			}
			raw = append(raw, cell)
		case brtShrFmla, brtArrFmla, brtTable:
			f := xlsbFields{data: data}
			area := f.area()
			if f.err == nil && previewCols != 0 && area.Col > previewCols {
				return nil
			}
			var formula xlsbFormula
			switch kind {
			case brtArrFmla:
				f.u8()
				formula = f.formula()
			case brtShrFmla:
				formula = f.formula()
			case brtTable:
				f.take(17) // Two input positions and flags. There is no formula token stream.
			}
			key := xlsbPosition{area.Row - 1, area.Col - 1}
			if f.err != nil || !inData || key.row != row || len(raw) == 0 || !raw[len(raw)-1].group || raw[len(raw)-1].anchor != key {
				return errors.New("BIFF12 共享或数组公式记录无效")
			}
			if _, duplicate := groups[key]; duplicate {
				return errors.New("BIFF12 公式锚点重复")
			}
			groupBytes += len(formula.tokens) + len(formula.extra)
			if groupBytes > maxMetadataPart || len(groups) >= formats.XLSXStyleRecords {
				return errors.New("XLSB 同时有效的共享或数组公式超限")
			}
			group := xlsbFormulaGroup{area: area, formula: formula.copy(), kind: kind}
			if kind == brtArrFmla {
				group.arrayText, _ = b.formulaText(formula, key.row, key.col, false)
			}
			groups[key] = group
			expires[area.EndRow-1] = append(expires[area.EndRow-1], key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !begin || !dataDone || !end {
		return nil, errors.New("XLSB 工作表数据或终止记录不完整")
	}
	if !td.Layout.valid() {
		return nil, errors.New("XLSB 工作表布局无效或超出容量")
	}
	return td, nil
}

func (w *Workbook) readXLSBCell(kind uint16, data []byte, lookup func(uint32) (xlsbString, error), row int) (xlsbRowCell, error) {
	f := xlsbFields{data: data}
	col, style := f.u32(), f.u32()&0xFFFFFF
	if f.err != nil || col >= formats.ExcelColumns || style > 65536 || style != 0 && int(style) >= len(w.Styles) {
		return xlsbRowCell{}, errors.New("BIFF12 单元格坐标或样式无效")
	}
	c := xlsbRowCell{externalCell: externalCell{Col: int(col) + 1, Style: int(style)}}
	switch kind {
	case brtCellBlank:
		c.Type = 'z'
	case brtCellRk:
		value := xlsRK(f.u32())
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return c, errors.New("BIFF12 RK 数值无效")
		}
		c.Type, c.Raw = 'n', xlsbNumber(value)
	case brtCellReal, brtFmlaNum:
		c.Type, c.Raw = 'n', xlsbNumber(f.f64())
	case brtCellError, brtFmlaError:
		c.Type, c.Raw = 'e', xlsbErrorValue(f.u8())
	case brtCellBool, brtFmlaBool:
		value := f.u8()
		if value > 1 {
			return c, errors.New("BIFF12 布尔缓存值无效")
		}
		c.Type, c.Raw = 'b', strconv.Itoa(int(value))
	case brtCellSt:
		c.Type, c.Raw = 't', f.wide(false, 32767)
	case brtFmlaString:
		c.Type, c.Raw = 'F', f.wide(false, 32767)
	case brtCellIsst:
		value, err := lookup(f.u32())
		if err != nil {
			return c, err
		}
		c.Type, c.Raw, c.Rich = 'S', value.text, value.rich
	case brtCellRString:
		value := f.richString()
		c.Type, c.Raw, c.Rich = 't', value.text, value.rich
	}
	if kind >= brtFmlaString && kind <= brtFmlaError {
		f.u16() // GrbitFmla describes recalculation, not the presence of this cache.
		formula := f.formula()
		c.HasFormula = true
		if rr, cc, ok := formula.pointer(); ok {
			c.group, c.anchor = true, xlsbPosition{rr, cc}
		} else {
			c.Formula, _ = w.source.xlsb.formulaText(formula, row, int(col), false)
		}
	}
	return c, f.err
}

func (w *Workbook) xlsbMetaPath(sheet SheetMeta) string {
	_, index := w.cachePaths(sheet.Path)
	return index + ".xlsb-meta"
}

type xlsbMetaFile struct {
	Fingerprint string
	Sheet       string
	Data        *xlsbSheetData
}

func (w *Workbook) saveXLSBSheetData(sheet SheetMeta, data *xlsbSheetData) error {
	raw, err := json.Marshal(xlsbMetaFile{w.Fingerprint, sheet.Path, data})
	if err != nil {
		return err
	}
	if len(raw) > 2*maxMetadataPart {
		return errors.New("XLSB 工作表元数据缓存超限")
	}
	tmp, err := os.CreateTemp(w.CacheDir, ".xlsb-meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(raw)
	if err = errors.Join(err, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	if err := replaceFile(tmp.Name(), w.xlsbMetaPath(sheet)); err != nil {
		return err
	}
	w.rememberXLSBSheetData(sheet, data)
	return nil
}

func (w *Workbook) rememberXLSBSheetData(sheet SheetMeta, data *xlsbSheetData) {
	b := w.source.xlsb
	b.metaMu.Lock()
	defer b.metaMu.Unlock()
	if b.metadata == nil || len(b.metadata) >= 4 {
		b.metadata = make(map[string]*xlsbSheetData)
	}
	b.metadata[sheet.Path] = data
}

func (w *Workbook) xlsbSheetMeta(ctx context.Context, sheet SheetMeta) (*xlsbSheetData, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := w.source.xlsb
	b.metaMu.Lock()
	cached := b.metadata[sheet.Path]
	b.metaMu.Unlock()
	if cached != nil {
		return cached, nil
	}
	f, err := os.Open(w.xlsbMetaPath(sheet))
	if err == nil {
		var cache xlsbMetaFile
		err = json.NewDecoder(io.LimitReader(f, 2*maxMetadataPart)).Decode(&cache)
		f.Close()
		if err == nil && cache.Fingerprint == w.Fingerprint && cache.Sheet == sheet.Path && cache.Data != nil && cache.Data.Layout.valid() {
			w.rememberXLSBSheetData(sheet, cache.Data)
			return cache.Data, nil
		}
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	pkg, parts, err := openXLSBParts(w.Path)
	if err != nil {
		return nil, err
	}
	defer pkg.Close()
	meta := newXLSBSheetData(w.normalDigitWidth())
	var ended bool
	err = readXLSBPart(parts[sheet.Path], 0, func(kind uint16, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if kind == brtEndSheet {
			ended = true
		}
		return meta.capture(kind, data)
	})
	if err != nil {
		return nil, err
	}
	if !ended || !meta.Layout.valid() {
		return nil, fmt.Errorf("XLSB 工作表 %q 元数据未完整读出", filepath.Base(sheet.Path))
	}
	if err := w.checkExternalUnchanged(); err != nil {
		return nil, err
	}
	w.rememberXLSBSheetData(sheet, meta)
	return meta, nil
}
