package workbook

// Native external-row cache. CSV, TXT, SQLite and BIFF XLS are not Excel
// worksheets and must not be inflated into OOXML or limited to 1,048,576 rows.
// Records have length-checked, forward-only encoding and independently indexed
// byte ranges for constant-memory viewport reads.

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
	Col          int
	Raw          string
	Type         byte // 's' string, 'n' numeric, 'b' boolean, 'e' error
	Style        int
	Formula      string // optional, includes leading '='
	HasFormula   bool
	CacheMissing bool
}

type externalRowWriter struct {
	out        *bufio.Writer
	count      int64
	offsets    rowOffsetBuilder
	rows, cols int
	scratch    []byte
}

func newExternalRowWriter(f *os.File) (*externalRowWriter, error) {
	w := &externalRowWriter{out: bufio.NewWriterSize(f, 1<<20), offsets: rowOffsetBuilder{rows: make([]RowOffset, 0, 4096), dir: filepath.Dir(f.Name())}}
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
	if w.rows >= maxExternalRows {
		return 0, fmt.Errorf("数据超过 %d 行的索引安全上限，请分批查看", maxExternalRows)
	}
	if cols > 16384 {
		return 0, fmt.Errorf("第 %d 行有 %d 列，超过当前界面支持的 16384 列", w.rows+1, cols)
	}
	if count > 16384 {
		return 0, fmt.Errorf("第 %d 行非空列数量过大", w.rows+1)
	}
	start := w.count
	if err := w.uvar(uint64(count)); err != nil {
		return 0, err
	}
	w.rows++
	if cols > w.cols {
		w.cols = cols
	}
	return start, nil
}
func (w *externalRowWriter) cell(c externalCell) error {
	if c.Col <= 0 || c.Col > 16384 {
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
	if len(fields) > 16384 {
		return fmt.Errorf("第 %d 行有 %d 列，超过当前界面支持的 16384 列", w.rows+1, len(fields))
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
	count := 0
	for _, c := range cells {
		if c.Raw != "" || c.HasFormula || c.Style != 0 {
			count++
		}
	}
	start, err := w.begin(count, cols)
	if err != nil {
		return err
	}
	prev := 0
	for _, c := range cells {
		if c.Raw == "" && !c.HasFormula && c.Style == 0 {
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

func (w *Workbook) decodeExternalRow(data []byte, number, first, last int, selected map[int]struct{}, formatValue, includeFormula bool) (RowData, error) {
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
	if count > 16384 {
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
		if col <= prev || col > 16384 {
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
			if includeFormula {
				formula = string(data[p : p+int(fsize)])
			}
			p += int(fsize)
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
		display := raw
		if formatValue && kind == 'n' {
			display = w.formatNumeric(raw, int(style))
		}
		typ := "inlineStr"
		switch kind {
		case 'n':
			typ = "n"
		case 'b':
			typ = "b"
		case 'e':
			typ = "e"
		}
		c := CellData{Col: int(col), Ref: nativeColumnNames[col] + rowSuffix, Raw: raw, Display: display, Type: typ, Style: int(style), HasFormula: flags&1 != 0, Formula: formula, CacheMissing: flags&2 != 0}
		if formatValue && kind == 'n' {
			c.ValueColor = w.numericTextColor(raw, int(style))
		}
		if kind == 'b' {
			if raw == "1" {
				c.Raw = "TRUE"
				c.Display = "TRUE"
			} else if raw == "0" {
				c.Raw = "FALSE"
				c.Display = "FALSE"
			}
		}
		res.Cells = append(res.Cells, c)
	}
	if p != len(data) {
		return res, errors.New("外部行缓存存在多余字节")
	}
	return res, nil
}
func (w *Workbook) readExternalRow(f *os.File, idx *SheetIndex, rowIndex, first, last int, selected map[int]struct{}, formatValue, includeFormula bool, reuse *[]byte) (RowData, error) {
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
	return w.decodeExternalRow(b, off.Number, first, last, selected, formatValue, includeFormula)
}
func isExternalRows(path string) bool { return strings.HasSuffix(path, ".rows") }
