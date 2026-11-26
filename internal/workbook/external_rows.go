package workbook

// Native external-row cache. Adapters preserve their source limits and use
// independently indexed, length-checked records for bounded viewport reads.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

const (
	nativeRowMagic      = "ELNROW02"
	maxExternalRows     = formats.ExternalRows // memory/storage guard, NOT the Excel row limit
	maxExternalCell     = 8 << 20
	maxExternalRowBytes = 128 << 20
)

type externalCell struct {
	Col                int
	Raw                string
	Type               byte // 's' string, 'n' numeric, 'b' boolean, 'e' error
	Style              int
	Formula            string // optional, includes leading '='
	HasFormula         bool
	CacheMissing       bool
	SharedFormula      bool
	ArrayFormula       *CellMerge
	ArrayFormulaMember bool
	Rich               []byte // optional XLSB payload in ELNROW02 flag bit 2
}

type externalRowWriter struct {
	out        *bufio.Writer
	count      int64
	offsets    rowOffsetBuilder
	rows, cols int
	scratch    []byte
}

func newExternalRowWriter(f *os.File) (*externalRowWriter, error) {
	return newExternalRowWriterTo(f, filepath.Dir(f.Name()))
}

func newExternalRowWriterTo(out io.Writer, dir string) (*externalRowWriter, error) {
	w := &externalRowWriter{out: bufio.NewWriterSize(out, 1<<20), offsets: rowOffsetBuilder{rows: make([]RowOffset, 0, 4096), dir: dir}}
	if err := w.bytes([]byte(nativeRowMagic)); err != nil {
		return nil, err
	}
	return w, nil
}
func (w *externalRowWriter) bytes(b []byte) error {
	n, e := w.out.Write(b)
	w.count += int64(n)
	return e
}
func (w *externalRowWriter) str(s string) error {
	n, e := w.out.WriteString(s)
	w.count += int64(n)
	return e
}
func (w *externalRowWriter) uvar(v uint64) error {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], v)
	return w.bytes(b[:n])
}
func (w *externalRowWriter) begin(count, cols int) (int64, error) {
	return w.beginStyled(count, cols, 0)
}

func (w *externalRowWriter) beginStyled(count, cols, style int) (int64, error) {
	if w.rows >= maxExternalRows {
		return 0, fmt.Errorf("数据超过 %d 行的索引安全上限，请分批查看", maxExternalRows)
	}
	if cols > formats.ExcelColumns {
		return 0, fmt.Errorf("第 %d 行有 %d 列，超过当前界面支持的 %d 列", w.rows+1, cols, formats.ExcelColumns)
	}
	if count > formats.ExcelColumns {
		return 0, fmt.Errorf("第 %d 行非空列数量过大", w.rows+1)
	}
	start := w.count
	header := count
	if style != 0 {
		header |= 1 << 15
	}
	if err := w.uvar(uint64(header)); err != nil {
		return 0, err
	}
	if style != 0 {
		if err := w.uvar(uint64(style)); err != nil {
			return 0, err
		}
	}
	w.rows++
	if cols > w.cols {
		w.cols = cols
	}
	return start, nil
}
func (w *externalRowWriter) cell(c externalCell) error {
	if c.Col <= 0 || c.Col > formats.ExcelColumns {
		return errors.New("无效单元格列号")
	}
	if len(c.Raw) > maxExternalCell || len(c.Formula) > maxExternalCell {
		return fmt.Errorf("单元格内容超过 %d MB 安全上限", maxExternalCell>>20)
	}
	if err := w.uvar(uint64(c.Col)); err != nil {
		return err
	}
	kind := c.Type
	if kind == 0 {
		kind = 's'
	}
	flags := byte(0)
	if c.HasFormula {
		flags |= 1
	}
	if c.CacheMissing {
		flags |= 2
	}
	if len(c.Rich) != 0 {
		flags |= 4
	}
	if c.SharedFormula {
		flags |= 8
	}
	if c.ArrayFormulaMember {
		flags |= 16
	}
	if c.ArrayFormula != nil {
		flags |= 32
	}
	if err := w.bytes([]byte{kind, flags}); err != nil {
		return err
	}
	if err := w.uvar(uint64(c.Style)); err != nil {
		return err
	}
	if err := w.uvar(uint64(len(c.Raw))); err != nil {
		return err
	}
	if err := w.str(c.Raw); err != nil {
		return err
	}
	if c.HasFormula {
		if err := w.uvar(uint64(len(c.Formula))); err != nil {
			return err
		}
		if err := w.str(c.Formula); err != nil {
			return err
		}
	}
	if len(c.Rich) != 0 {
		if err := w.uvar(uint64(len(c.Rich))); err != nil {
			return err
		}
		if err := w.bytes(c.Rich); err != nil {
			return err
		}
	}
	if c.ArrayFormula != nil {
		if err := w.uvar(uint64(c.ArrayFormula.EndRow)); err != nil {
			return err
		}
		return w.uvar(uint64(c.ArrayFormula.EndCol))
	}
	return nil
}
func (w *externalRowWriter) complete(start int64) error {
	if w.count-start > maxExternalRowBytes {
		return fmt.Errorf("第 %d 行超过 128MB 安全上限", w.rows)
	}
	return w.offsets.append(RowOffset{Number: w.rows, Start: start, End: w.count})
}
func (w *externalRowWriter) row(fields []string) error {
	if w.rows >= maxExternalRows {
		return fmt.Errorf("数据超过 %d 行的索引安全上限，请分批查看", maxExternalRows)
	}
	if len(fields) > formats.ExcelColumns {
		return fmt.Errorf("第 %d 行有 %d 列，超过当前界面支持的 %d 列", w.rows+1, len(fields), formats.ExcelColumns)
	}
	count := 0
	for _, s := range fields {
		if s != "" {
			count++
		}
	}
	// Assemble one complete record in reusable memory. The previous path
	// allocated a varint buffer and performed several writes for every cell.
	w.scratch = binary.AppendUvarint(w.scratch[:0], uint64(count))
	for col, s := range fields {
		if s == "" {
			continue
		}
		if len(s) > maxExternalCell {
			return fmt.Errorf("第 %d 行 %d 列：单元格内容超过 8MB 安全上限", w.rows+1, col+1)
		}
		if len(w.scratch)+len(s)+32 > maxExternalRowBytes {
			return fmt.Errorf("第 %d 行超过 128MB 安全上限", w.rows+1)
		}
		w.scratch = binary.AppendUvarint(w.scratch, uint64(col+1))
		w.scratch = append(w.scratch, 's', 0, 0) // type, flags, style
		w.scratch = binary.AppendUvarint(w.scratch, uint64(len(s)))
		w.scratch = append(w.scratch, s...)
	}
	start := w.count
	if err := w.bytes(w.scratch); err != nil {
		return err
	}
	w.rows++
	w.cols = max(w.cols, len(fields))
	return w.complete(start)
}
func (w *externalRowWriter) cells(cells []externalCell, cols int) error {
	return w.cellsWithStyle(cells, cols, 0)
}

func storedExternalCell(c externalCell) bool {
	return c.Raw != "" || c.HasFormula || c.Style != 0 || c.Type == 'S' || c.Type == 't' || c.Type == 'z' || c.Type == '0'
}

func (w *externalRowWriter) cellsWithStyle(cells []externalCell, cols, style int) error {
	count := 0
	for _, c := range cells {
		if storedExternalCell(c) {
			count++
		}
	}
	start, err := w.beginStyled(count, cols, style)
	if err != nil {
		return err
	}
	prev := 0
	for _, c := range cells {
		if !storedExternalCell(c) {
			continue
		}
		if c.Col <= prev {
			return fmt.Errorf("第 %d 行的单元格列位置不递增", w.rows)
		}
		prev = c.Col
		if err := w.cell(c); err != nil {
			return fmt.Errorf("第 %d 行：%w", w.rows, err)
		}
	}
	return w.complete(start)
}
func (w *externalRowWriter) finish() error {
	if err := w.out.Flush(); err != nil {
		return err
	}
	return w.offsets.finish()
}
func (w *externalRowWriter) dimension() string {
	if w.rows == 0 || w.cols == 0 {
		return "A1"
	}
	return "A1:" + ColumnName(w.cols) + fmt.Sprint(w.rows)
}

// decodeExternalRow accepts a bounded row record and only allocates cells for
// requested columns. The outer row index supplies the original row number.
// MaxRowsForIndex preserves the Excel worksheet ceiling for OOXML while
// allowing external CSV/TXT/SQLite rows to extend beyond it. XLS is BIFF8
// and stays within its own 65,536-row source ceiling.
func MaxRowsForIndex(idx *SheetIndex) int {
	if idx != nil {
		if source, ok := formats.Lookup(idx.SourceFormat); ok {
			return source.MaxRows
		}
	}
	if idx != nil && isExternalRows(idx.XMLPath) {
		return maxExternalRows
	}
	return maxExcelRows
}

var nativeColumnNames = func() [16385]string {
	var names [16385]string
	for col := 1; col < len(names); col++ {
		names[col] = ColumnName(col)
	}
	return names
}()

func (w *Workbook) decodeExternalRow(data []byte, number, first, last int, selected map[int]struct{}, options RowReadOptions) (RowData, error) {
	if w.source != nil && w.source.ods != nil {
		return w.decodeODS(data, number, first, last, selected, options.FormatValue, options.IncludeFormula)
	}
	options = prepareRichRead(options)
	res := RowData{Number: number}
	rowSuffix := strconv.Itoa(number)
	p := 0
	get := func() (uint64, error) {
		if p >= len(data) {
			return 0, io.ErrUnexpectedEOF
		}
		v, n := binary.Uvarint(data[p:])
		if n <= 0 {
			return 0, errors.New("外部行缓存的整数编码损坏")
		}
		p += n
		return v, nil
	}
	count, err := get()
	if err != nil {
		return res, err
	}
	if count&(1<<15) != 0 && w.Format == "xlsb" {
		count &^= 1 << 15
		style, err := get()
		if err != nil || style >= uint64(len(w.Styles)) {
			return res, errors.New("XLSB 行缓存样式索引无效")
		}
		res.Style = int(style)
	}
	if count > formats.ExcelColumns {
		return res, errors.New("外部行缓存列数损坏")
	}
	capacity := min(int(count), max(0, last-first+1), 32)
	if selected != nil {
		capacity = min(capacity, len(selected))
	}
	res.Cells = make([]CellData, 0, capacity)
	prev := uint64(0)
	for i := uint64(0); i < count; i++ {
		col, err := get()
		if err != nil {
			return res, err
		}
		if col <= prev || col > formats.ExcelColumns {
			return res, errors.New("外部行缓存列号无效")
		}
		prev = col
		if p+2 > len(data) {
			return res, io.ErrUnexpectedEOF
		}
		kind, flags := data[p], data[p+1]
		p += 2
		style, err := get()
		if err != nil {
			return res, err
		}
		if style > 1<<16 {
			return res, errors.New("外部行缓存样式索引无效")
		}
		size, err := get()
		if err != nil {
			return res, err
		}
		if size > maxExternalCell || size > uint64(len(data)-p) {
			return res, errors.New("外部行缓存单元格长度损坏")
		}
		rawStart := p
		p += int(size)
		var formula string
		if flags&1 != 0 {
			fsize, err := get()
			if err != nil {
				return res, err
			}
			if fsize > maxExternalCell || fsize > uint64(len(data)-p) {
				return res, errors.New("外部行缓存公式长度损坏")
			}
			if options.IncludeFormula {
				formula = string(data[p : p+int(fsize)])
			}
			p += int(fsize)
		}
		var richData []byte
		if flags&4 != 0 {
			rsize, err := get()
			if err != nil {
				return res, err
			}
			if w.Format != "xlsb" || rsize < 1 || rsize > 4*formats.MaxCellRichRuns+1 || rsize > uint64(len(data)-p) {
				return res, errors.New("外部行缓存富文本长度无效")
			}
			richData = data[p : p+int(rsize)]
			p += int(rsize)
		}
		var array *CellMerge
		if flags&32 != 0 {
			r, e1 := get()
			c, e2 := get()
			if e1 != nil || e2 != nil || w.Format != "xlsb" || r < uint64(number) || r > formats.ExcelRows || c < col || c > formats.ExcelColumns {
				return res, errors.New("XLSB 行缓存的数组公式范围无效")
			}
			array = &CellMerge{Row: number, Col: int(col), EndRow: int(r), EndCol: int(c)}
		}
		if int(col) < first || int(col) > last {
			continue
		}
		if selected != nil {
			if _, ok := selected[int(col)]; !ok {
				continue
			}
		}
		raw := string(data[rawStart : rawStart+int(size)])
		c := w.externalCellValue(rowSuffix, externalCell{Col: int(col), Raw: raw, Style: int(style), Type: kind,
			HasFormula: flags&1 != 0, Formula: formula, CacheMissing: flags&2 != 0, SharedFormula: flags&8 != 0,
			ArrayFormula: array, ArrayFormulaMember: flags&16 != 0}, options.FormatValue)
		if options.RichText && len(richData) != 0 {
			if options.richBudget.admit(len(richData)) {
				c.RichText, err = w.xlsbRichPresentation(raw, richData)
				if err != nil {
					return res, err
				}
			} else {
				c.RichText = &RichText{Notice: "本次读取的富文本超过呈现容量"}
			}
		}
		res.Cells = append(res.Cells, c)
	}
	if p != len(data) {
		return res, errors.New("外部行缓存存在多余字节")
	}
	return res, nil
}

func (w *Workbook) externalCellValue(rowSuffix string, value externalCell, formatValue bool) CellData {
	c := CellData{Col: value.Col, Ref: nativeColumnNames[value.Col] + rowSuffix, Raw: value.Raw, Display: value.Raw,
		Style: value.Style, Formula: value.Formula, HasFormula: value.HasFormula, CacheMissing: value.CacheMissing, SharedFormula: value.SharedFormula,
		ArrayFormula: value.ArrayFormula, ArrayFormulaMember: value.ArrayFormulaMember}
	switch value.Type {
	case 'n':
		c.Type = "n"
		if formatValue {
			c.Display = w.formatNumeric(c.Raw, c.Style)
			c.ValueColor = w.numericTextColor(c.Raw, c.Style)
		}
	case 'b':
		c.Type = "b"
		switch c.Raw {
		case "1":
			c.Raw, c.Display = "TRUE", "TRUE"
		case "0":
			c.Raw, c.Display = "FALSE", "FALSE"
		}
	case 'e':
		c.Type = "e"
	case 'z':
	case 'S':
		c.Type = "s"
	case 'F':
		c.Type = "str"
	case '0':
		c.SourceType, c.Display = "null", "[NULL]"
	case 'x':
		c.Type, c.SourceType = "inlineStr", "binary"
	case 'd':
		c.Type, c.SourceType = "d", "date"
	default:
		c.Type = "inlineStr"
		if w.Format == "db-online" {
			c.SourceType = "string"
		}
	}
	return c
}
func (w *Workbook) readExternalRow(f *os.File, idx *SheetIndex, rowIndex, first, last int, selected map[int]struct{}, options RowReadOptions, reuse *[]byte) (RowData, error) {
	if rowIndex < 0 || rowIndex >= idx.RowCount() {
		return RowData{}, errors.New("行索引超出范围")
	}
	off := idx.RowAt(rowIndex)
	length := off.End - off.Start
	if length <= 0 || length > maxExternalRowBytes || length > idx.XMLSize {
		return RowData{}, errors.New("外部行缓存偏移量无效")
	}
	if int64(cap(*reuse)) < length {
		*reuse = make([]byte, length)
	}
	b := (*reuse)[:int(length)]
	if _, err := f.ReadAt(b, off.Start); err != nil {
		return RowData{}, err
	}
	return w.decodeExternalRow(b, off.Number, first, last, selected, options)
}
func isExternalRows(path string) bool { return strings.HasSuffix(path, ".rows") }
