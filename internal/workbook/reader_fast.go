package workbook

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
)

// fastReadRowAt reads one XML row using the byte offsets already validated by
// the worksheet index. It only decodes cells in the requested column range.
//
// This is the hot path for virtual scrolling. It is deliberately separate from
// the strict encoding/xml reader, which remains the compatibility fallback and
// the reference implementation in tests. Nothing is written to the source.
func (w *Workbook) fastReadRowAt(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, reuse *[]byte) (RowData, error) {
	return w.fastReadRowWithOptions(f, idx, rowIndex, firstCol, lastCol, nil, true, true, reuse)
}

// fastReadRowWithOptions shares the same byte scanner used for viewport reads.
// A sparse selected-column set prevents decoding unrelated cells during large
// multi-key sort/filter passes. The strict parser is the fallback for unusual
// OOXML constructs, preserving source fidelity over speed.
func (w *Workbook) fastReadRowWithOptions(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, selected map[int]struct{}, formatValue, includeFormula bool, reuse *[]byte) (RowData, error) {
	if isExternalRows(idx.XMLPath) {
		return w.readExternalRow(f, idx, rowIndex, firstCol, lastCol, selected, RowReadOptions{FormatValue: formatValue, IncludeFormula: includeFormula}, reuse)
	}
	if rowIndex < 0 || rowIndex >= idx.RowCount() {
		return RowData{}, errors.New("row index out of range")
	}
	entry := idx.RowAt(rowIndex)
	length := entry.End - entry.Start
	if length <= 0 || length > idx.XMLSize || length > 128<<20 {
		return RowData{}, fmt.Errorf("invalid row length %d for row %d", length, entry.Number)
	}
	if int64(cap(*reuse)) < length {
		*reuse = make([]byte, length)
	}
	row := (*reuse)[:int(length)]
	if _, err := f.ReadAt(row, entry.Start); err != nil {
		// Indexed ranges must be entirely readable. Never silently truncate.
		if err == io.EOF {
			return RowData{}, io.ErrUnexpectedEOF
		}
		return RowData{}, err
	}
	return w.fastDecodeXMLRow(f, idx, rowIndex, firstCol, lastCol, selected, formatValue, includeFormula, row)
}

func (w *Workbook) fastDecodeXMLRow(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, selected map[int]struct{}, formatValue, includeFormula bool, row []byte) (RowData, error) {
	return w.fastDecodeXMLRowWithOptions(f, idx, rowIndex, firstCol, lastCol, selected, RowReadOptions{FormatValue: formatValue, IncludeFormula: includeFormula}, row)
}

func (w *Workbook) fastDecodeXMLRowWithOptions(f *os.File, idx *SheetIndex, rowIndex, firstCol, lastCol int, selected map[int]struct{}, options RowReadOptions, row []byte) (RowData, error) {
	options = prepareRichRead(options)
	remaining := 0
	if options.richBudget != nil {
		remaining = options.richBudget.remaining
	}
	entry := idx.RowAt(rowIndex)
	fallback := func() (RowData, error) {
		if options.richBudget != nil {
			options.richBudget.remaining = remaining
		}
		return w.readRowFromFileWithOptions(f, idx, rowIndex, firstCol, lastCol, selected, options)
	}
	// A small number of OOXML extensions can contain constructs that the
	// compact scanner intentionally does not interpret. Use the strict parser
	// for those rows, rather than guessing their meaning.
	if bytes.Contains(row, []byte("<![CDATA[")) || bytes.Contains(row, []byte("<mc:AlternateContent")) ||
		bytes.Contains(row, []byte("<rPh")) || bytes.Contains(row, []byte(":rPh")) ||
		bytes.Contains(row, []byte("<extLst")) {
		return fallback()
	}
	headerAt := bytes.Index(row, []byte("<row"))
	if headerAt < 0 || headerAt+4 >= len(row) || !isXMLSpace(row[headerAt+4]) && row[headerAt+4] != '>' && row[headerAt+4] != '/' {
		return fallback()
	}
	headerEnd := fastTagEnd(row, headerAt)
	if headerEnd < 0 {
		return fallback()
	}
	header := row[headerAt : headerEnd+1]
	rowNum := entry.Number
	if n := parsePositiveIntBytes(xmlAttribute(header, "r")); n > 0 {
		rowNum = n
	}
	style := 0
	if v := xmlAttribute(header, "s"); len(v) > 0 {
		n := parsePositiveNonNegativeBytes(v)
		if n < len(w.Styles) {
			style = n
		}
	}
	// A sparse filter over A, M and XFD must not allocate a 36-cell slice
	// for each source row when it only materializes three cells.
	capacity := minInt(lastCol-firstCol+1, 36)
	if selected != nil {
		capacity = minInt(len(selected), capacity)
	}
	result := RowData{Number: rowNum, Style: style, Cells: options.cells(capacity)}
	if bytes.HasSuffix(bytes.TrimSpace(header), []byte("/>")) {
		return result, nil
	}
	if !bytes.Contains(row[headerEnd+1:], []byte("</row>")) {
		return fallback()
	}
	position := headerEnd + 1
	for position < len(row) {
		relative := bytes.Index(row[position:], []byte("<c"))
		if relative < 0 {
			break
		}
		position += relative
		if position+2 >= len(row) {
			break
		}
		if !isXMLSpace(row[position+2]) && row[position+2] != '>' && row[position+2] != '/' {
			position += 2
			continue
		}
		end := fastTagEnd(row, position)
		if end < 0 {
			return fallback()
		}
		tag := row[position : end+1]
		ref, cellType, cellStyle := xmlCellAttributes(tag)
		if len(ref) == 0 {
			// The strict reader infers sequential coordinates; skipping such a
			// cell would silently lose data (even if it is not selected).
			return fallback()
		}
		col := parseColumnBytes(ref)
		if col == 0 {
			return fallback()
		}
		selfClosing := bytes.HasSuffix(bytes.TrimSpace(tag), []byte("/>"))
		bodyStart := end + 1
		bodyEnd := bodyStart
		if !selfClosing {
			closing := bytes.Index(row[bodyStart:], []byte("</c>"))
			if closing < 0 {
				return fallback()
			}
			bodyEnd = bodyStart + closing
		}
		if col >= firstCol && col <= lastCol && (selected == nil || containsColumn(selected, col)) {
			cell := xmlCell{Ref: string(ref), Type: string(cellType)}
			if attr := cellStyle; len(attr) > 0 {
				cell.StyleDefined = true
				cell.Style = parsePositiveNonNegativeBytes(attr)
			}
			if !selfClosing {
				body := row[bodyStart:bodyEnd]
				if ftag, formula, ok := fastElement(body, "f"); ok {
					cell.Formula = &formulaXML{
						Text: xmlUnescape(formula), Type: string(xmlAttribute(ftag, "t")),
						Ref: string(xmlAttribute(ftag, "ref")), SharedIndex: string(xmlAttribute(ftag, "si")),
					}
				}
				if _, raw, ok := fastElement(body, "v"); ok {
					cell.Value = xmlUnescape(raw)
					cell.HasValue = true
				}
				if cell.Type == "inlineStr" {
					if options.RichText && (bytes.Contains(body, []byte("<r")) || bytes.Contains(body, []byte(":r"))) {
						var err error
						cell.Inline, cell.RichText, err = w.inlineStringStyled(body)
						if err != nil {
							return fallback()
						}
					} else {
						var ok bool
						cell.Inline, ok = inlineStringValue(body)
						if !ok {
							return fallback()
						}
					}
				}
			}
			if !cell.StyleDefined && style != 0 {
				cell.Style = style
			}
			result.Cells = append(result.Cells, w.resolveCellWithPresentation(cell, col, idx.SharedAnchors, options))
		}
		if selfClosing {
			position = end + 1
		} else {
			position = bodyEnd + len("</c>")
		}
	}
	return result, nil
}

func (w *Workbook) inlineStringStyled(body []byte) (string, *RichText, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := d.Token()
		if err == io.EOF {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "is" {
			return w.readRichTextStyled(d, start)
		}
	}
}

func containsColumn(columns map[int]struct{}, col int) bool {
	_, ok := columns[col]
	return ok
}

func xmlUnescape(raw []byte) string {
	if bytes.IndexByte(raw, '&') < 0 && bytes.IndexByte(raw, '\r') < 0 {
		return string(raw)
	}
	// XML 1.0 normalizes literal CRLF / CR line endings BEFORE decoding character
	// references. Numerical &#13; references, however, must remain literal CR.
	// This matches encoding/xml and prevents subtle cached-value differences.
	if bytes.IndexByte(raw, '\r') >= 0 {
		raw = bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	}
	if bytes.IndexByte(raw, '&') < 0 {
		return string(raw)
	}
	return html.UnescapeString(string(raw))
}
