package workbook

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func (w *Workbook) resolveCell(c xmlCell, col int, anchors map[string]SharedFormulaAnchor, formatValue, includeFormula bool) CellData {
	raw := c.Value
	switch c.Type {
	case "s":
		if n, err := parseNonNegativeInt(c.Value); err == nil && n < len(w.Shared) {
			raw = w.Shared[n]
		} else {
			raw = "[共享字符串索引无效]"
		}
	case "inlineStr":
		raw = c.Inline
	case "b":
		if c.Value == "1" {
			raw = "TRUE"
		} else if c.Value == "0" {
			raw = "FALSE"
		}
	}
	formula := ""
	sharedFormula := false
	if c.Formula != nil {
		sharedFormula = c.Formula.Type == "shared"
		if !includeFormula {
			formula = ""
		} else if c.Formula.Text != "" {
			formula = "=" + strings.TrimSpace(c.Formula.Text)
		} else if c.Formula.Type == "shared" {
			if anchor, ok := anchors[c.Formula.SharedIndex]; ok {
				formula = "=" + translateSharedFormula(anchor.Formula, anchor.Ref, c.Ref)
			} else {
				formula = fmt.Sprintf("[共享公式组 si=%s：未找到主公式]", c.Formula.SharedIndex)
			}
		}
	}
	display := raw
	color := ""
	if formatValue && (c.Type == "" || c.Type == "n") {
		color = w.numericTextColor(c.Value, c.Style)
	}
	if formatValue && (c.Type == "" || c.Type == "n") {
		display = w.formatNumeric(raw, c.Style)
	}
	return CellData{
		Ref:           c.Ref,
		Col:           col,
		Raw:           raw,
		Display:       display,
		Formula:       formula,
		HasFormula:    c.Formula != nil,
		Type:          c.Type,
		Style:         c.Style,
		ValueColor:    color,
		CacheMissing:  c.Formula != nil && !c.HasValue,
		SharedFormula: sharedFormula,
	}
}

func parseNonNegativeInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty integer")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("invalid integer")
		}
		digit := int(c - '0')
		if n > (int(^uint(0)>>1)-digit)/10 {
			return 0, errors.New("integer overflow")
		}
		n = n*10 + digit
	}
	return n, nil
}

func (idx *SheetIndex) FindRowOffset(rowNumber int) int {
	lo, hi := 0, idx.RowCount()
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if idx.RowAt(mid).Number < rowNumber {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (w *Workbook) ReadRow(idx *SheetIndex, rowIndex, firstCol, lastCol int) (RowData, error) {
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return RowData{}, err
	}
	defer f.Close()
	return w.readRowFromFile(f, idx, rowIndex, firstCol, lastCol)
}

func (w *Workbook) ReadRowAll(idx *SheetIndex, rowIndex int) (RowData, error) {
	return w.ReadRow(idx, rowIndex, 1, 16384)
}

// readRowFromFile parses a bounded XML section directly from disk. It deliberately
// avoids allocating a byte slice for the entire row and skips cells outside the
// requested column window before materializing their XML structures.
func (w *Workbook) readRowFromFile(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int) (RowData, error) {
	return w.readRowFromFileMode(f, idx, rowIndex, firstCol, lastCol, true, true)
}

func (w *Workbook) readRowFromFileMode(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, formatValue, includeFormula bool) (RowData, error) {
	return w.readRowFromFileSelected(f, idx, rowIndex, firstCol, lastCol, nil, formatValue, includeFormula)
}

// readRowFromFileSelected decodes only requested columns. A nil column set means
// a contiguous range; a non-nil set avoids materializing unrelated cells when
// evaluating filters over distant columns.
func (w *Workbook) readRowFromFileSelected(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, selected map[int]struct{}, formatValue, includeFormula bool) (RowData, error) {
	return w.readRowFromFileWithOptions(f, idx, rowIndex, firstCol, lastCol, selected, RowReadOptions{FormatValue: formatValue, IncludeFormula: includeFormula})
}

func (w *Workbook) readRowFromFileWithOptions(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, selected map[int]struct{}, options RowReadOptions) (RowData, error) {
	options = prepareRichRead(options)
	if isExternalRows(idx.XMLPath) {
		var reuse []byte
		return w.readExternalRow(f, idx, rowIndex, firstCol, lastCol, selected, options.FormatValue, options.IncludeFormula, &reuse)
	}
	if rowIndex < 0 || rowIndex >= idx.RowCount() {
		return RowData{}, errors.New("行索引超出范围")
	}
	off := idx.RowAt(rowIndex)
	length := off.End - off.Start
	if length <= 0 || length > idx.XMLSize {
		return RowData{}, fmt.Errorf("第 %d 行缓存偏移量无效：%d", off.Number, length)
	}
	if length > 128<<20 {
		return RowData{}, fmt.Errorf("第 %d 行数据超过 128 MB 安全上限", off.Number)
	}
	section := io.NewSectionReader(f, off.Start, length)
	dec := xml.NewDecoder(section)
	dec.Strict = false
	rowNum := off.Number
	rowStyle := 0
	lastColumn := 0 // OOXML permits <c> without an r attribute.
	cells := make([]CellData, 0, 32)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return RowData{Number: rowNum, Style: rowStyle, Cells: cells}, nil
		}
		if err != nil {
			return RowData{}, fmt.Errorf("解析第 %d 行失败：%w", off.Number, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "r":
				if n, e := parseNonNegativeInt(attr.Value); e == nil && n > 0 {
					rowNum = n
				}
			case "s":
				if n, e := parseNonNegativeInt(attr.Value); e == nil && n < len(w.Styles) {
					rowStyle = n
				}
			}
		}
		for {
			tok, err = dec.Token()
			if err == io.EOF {
				return RowData{Number: rowNum, Style: rowStyle, Cells: cells}, nil
			}
			if err != nil {
				return RowData{}, fmt.Errorf("解析第 %d 行单元格失败：%w", rowNum, err)
			}
			switch token := tok.(type) {
			case xml.StartElement:
				if token.Name.Local != "c" {
					if err := dec.Skip(); err != nil {
						return RowData{}, err
					}
					continue
				}
				ref := ""
				for _, attr := range token.Attr {
					if attr.Name.Local == "r" {
						ref = attr.Value
						break
					}
				}
				col := 0
				if ref == "" {
					// Missing r is legal and means the next sequential column. The
					// inferred reference is needed for formulas and navigation.
					col = lastColumn + 1
					ref = ColumnName(col) + itoa(rowNum)
				} else {
					var parseErr error
					col, _, parseErr = ParseCellRef(ref)
					if parseErr != nil {
						return RowData{}, fmt.Errorf("第 %d 行存在无效单元格地址 %q", rowNum, ref)
					}
				}
				if col < 1 || col > 16384 {
					return RowData{}, fmt.Errorf("第 %d 行单元格列超出 Excel 上限：%d", rowNum, col)
				}
				lastColumn = col
				_, selectedColumn := selected[col]
				if col < firstCol || col > lastCol || (selected != nil && !selectedColumn) {
					if err := dec.Skip(); err != nil {
						return RowData{}, err
					}
					continue
				}
				var cell xmlCell
				if options.RichText {
					cell.richOwner = w
				}
				if err := dec.DecodeElement(&cell, &token); err != nil {
					return RowData{}, fmt.Errorf("解析单元格 %s 失败：%w", ref, err)
				}
				if cell.Ref == "" {
					cell.Ref = ref
				}
				if !cell.StyleDefined && rowStyle != 0 {
					cell.Style = rowStyle
				}
				cells = append(cells, w.resolveCellWithPresentation(cell, col, idx.SharedAnchors, options))
			case xml.EndElement:
				if token.Name.Local == "row" {
					return RowData{Number: rowNum, Style: rowStyle, Cells: cells}, nil
				}
			}
		}
	}
}

func (w *Workbook) ReadRows(idx *SheetIndex, start, limit, firstCol, lastCol int) ([]RowData, error) {
	return w.ReadRowsContext(context.Background(), idx, start, limit, firstCol, lastCol)
}

// ReadRowsContext aborts obsolete viewport reads promptly when the browser
// cancels a fast-scroll request. This avoids CPU/disk contention with the
// latest visible viewport; existing internal callers retain ReadRows.
func (w *Workbook) ReadRowsContext(ctx context.Context, idx *SheetIndex, start, limit, firstCol, lastCol int) ([]RowData, error) {
	return w.ReadRowsWithOptionsContext(ctx, idx, start, limit, firstCol, lastCol, RowReadOptions{FormatValue: true, IncludeFormula: true})
}

func (w *Workbook) ReadRowsWithOptionsContext(ctx context.Context, idx *SheetIndex, start, limit, firstCol, lastCol int, options RowReadOptions) ([]RowData, error) {
	if start < 0 {
		start = 0
	}
	if start > idx.RowCount() {
		start = idx.RowCount()
	}
	if limit < 0 {
		limit = 0
	}
	end := start + limit
	if end > idx.RowCount() {
		end = idx.RowCount()
	}
	indices := make([]int, end-start)
	for i := range indices {
		indices[i] = start + i
	}
	return w.ReadRowsByIndicesWithOptionsContext(ctx, idx, indices, firstCol, lastCol, options)
}

// ReadRowsByIndices reads a non-contiguous set of row-index positions. It is
// used by whole-sheet filters while keeping the source row index immutable.
func (w *Workbook) ReadRowsByIndices(idx *SheetIndex, indices []int, firstCol, lastCol int) ([]RowData, error) {
	return w.ReadRowsByIndicesContext(context.Background(), idx, indices, firstCol, lastCol)
}

func (w *Workbook) ReadRowsByIndicesContext(ctx context.Context, idx *SheetIndex, indices []int, firstCol, lastCol int) ([]RowData, error) {
	return w.ReadRowsByIndicesWithOptionsContext(ctx, idx, indices, firstCol, lastCol, RowReadOptions{FormatValue: true, IncludeFormula: true})
}

func (w *Workbook) ReadRowsByIndicesWithOptionsContext(ctx context.Context, idx *SheetIndex, indices []int, firstCol, lastCol int, options RowReadOptions) ([]RowData, error) {
	var selected map[int]struct{}
	if len(options.Columns) > 0 {
		if len(options.Columns) > 80 {
			return nil, errors.New("invalid sparse columns")
		}
		selected = make(map[int]struct{}, len(options.Columns))
		previous := 0
		for _, col := range options.Columns {
			if col <= previous || col < firstCol || col > lastCol || col > 16384 {
				return nil, errors.New("sparse columns must be ascending and unique")
			}
			selected[col] = struct{}{}
			previous = col
		}
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return w.readIndexedBatchWithOptions(ctx, f, idx, indices, firstCol, lastCol, selected, options)
}
