package workbook

import (
	"bufio"
	"errors"
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HTMLMerge describes a source range. Covered cells remain empty and keep
// their original coordinates; only its upper-left anchor holds a value.
type HTMLMerge = CellMerge

type htmlDocumentSheet struct {
	Merges       []HTMLMerge       `json:"merges,omitempty"`
	Rows         []SourceSizeRange `json:"rows,omitempty"`
	Cols         map[int]float64   `json:"cols,omitempty"`
	GridLines    bool              `json:"gridLines,omitempty"`
	widthVotes   map[int]map[float64]int
	explicitCols map[int]bool
}

// SourceSizeRange keeps repeated report row sizes compact even for large TXT-like
// HTML reports. Dimensions describe display only and never alter source values.
func (w *Workbook) HTMLLayout(meta SheetMeta) ([]SourceSizeRange, map[int]float64, *bool) {
	if w.source == nil || w.source.html == nil {
		return nil, nil, nil
	}
	i, err := strconv.Atoi(strings.TrimPrefix(meta.Path, "external/html/"))
	if err != nil || i < 0 || i >= len(w.source.html.Sheets) {
		return nil, nil, nil
	}
	sheet := &w.source.html.Sheets[i]
	return sheet.Rows, sheet.Cols, &sheet.GridLines
}

func (w *Workbook) HTMLMerges(meta SheetMeta) []HTMLMerge {
	if w.source == nil || w.source.html == nil {
		return nil
	}
	i, err := strconv.Atoi(strings.TrimPrefix(meta.Path, "external/html/"))
	if err != nil || i < 0 || i >= len(w.source.html.Sheets) {
		return nil
	}
	return w.source.html.Sheets[i].Merges
}

// One HTML document is one worksheet unless Office explicitly declares
// multiple worksheets. Report sections / page breaks / table IDs are not tabs.
func readStyledHTMLTables(src io.Reader, target int, emit func([]string) error, styled func([]string, []int) error, metadata *htmlStyleMetadata) ([]string, error) {
	h := htmlTableReader{r: bufio.NewReaderSize(src, 256<<10)}
	var officeNames []string
	tables, sheet, row, col, occupiedMax := 0, 0, 0, 1, 0
	inside, rowOpen, cellOpen, helper, body := false, false, false, false, true
	var fields []string
	var cellStyles []int
	var stack []htmlTableToken
	var text, paragraph strings.Builder
	paragraphOpen, pendingSpace, chosenStyle := false, false, false
	colspan, rowspan, cellStyle := 1, 1, 0
	tableWidth, rowSize, cellWidth, layoutCol := 0.0, 0.0, 0.0, 1
	occupied := make([]int, 16385)
	active := func() bool { return target < 0 || target == sheet }
	ensureSheet := func() {
		if metadata != nil && target < 0 {
			for len(metadata.Sheets) <= sheet {
				metadata.Sheets = append(metadata.Sheets, htmlDocumentSheet{})
			}
		}
	}
	appendText := func(s string) {
		for _, r := range s {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\f' {
				pendingSpace = true
				continue
			}
			if pendingSpace && text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
				text.WriteByte(' ')
			}
			pendingSpace = false
			text.WriteRune(r)
		}
	}
	output := func(values []string, styles []int) error {
		if target < 0 || target != sheet {
			return nil
		}
		if styled != nil {
			return styled(values, styles)
		}
		if emit != nil {
			return emit(values)
		}
		return nil
	}
	flushCell := func() error {
		if !cellOpen {
			return nil
		}
		cellOpen = false
		for col <= 16384 && occupied[col] >= row {
			col++
		}
		last := col + colspan - 1
		if last > 16384 || row+rowspan-1 > maxExternalRows {
			return errors.New("HTML 合并单元格超出行列安全上限")
		}
		for c := col; c <= last; c++ {
			if occupied[c] >= row {
				return errors.New("HTML 合并单元格位置重叠")
			}
			occupied[c] = row + rowspan - 1
		}
		occupiedMax = max(occupiedMax, last)
		if metadata != nil && target < 0 && (rowspan > 1 || colspan > 1) {
			ensureSheet()
			if len(metadata.Sheets[sheet].Merges) >= 100000 {
				return errors.New("HTML 合并范围超过 100,000 安全上限")
			}
			metadata.Sheets[sheet].Merges = append(metadata.Sheets[sheet].Merges, HTMLMerge{row, col, row + rowspan - 1, last})
		}
		if metadata != nil && target < 0 && cellWidth > 0 && colspan == 1 {
			ensureSheet()
			metadata.Sheets[sheet].addColumns(col, 1, cellWidth)
		}
		if target >= 0 && active() {
			for len(fields) < last {
				fields = append(fields, "")
				cellStyles = append(cellStyles, 0)
			}
			fields[col-1] = strings.Trim(text.String(), " \t\r\n\f")
			cellStyles[col-1] = cellStyle
		}
		text.Reset()
		pendingSpace = false
		stack = nil
		col = last + 1
		return nil
	}
	flushRow := func() error {
		if !rowOpen {
			return nil
		}
		if err := flushCell(); err != nil {
			return err
		}
		rowOpen = false
		if metadata != nil && target < 0 && rowSize > 0 {
			ensureSheet()
			metadata.Sheets[sheet].addRowSize(row, rowSize)
		}
		return output(fields, cellStyles)
	}
	for {
		t, err := h.next()
		if err == io.EOF {
			if inside {
				return nil, errors.New("HTML 表格缺少结束标签，文件可能不完整")
			}
			if tables == 0 {
				return nil, nil
			}
			if len(officeNames) > 1 {
				if tables != len(officeNames) {
					return nil, errors.New("HTML 中声明的工作表与表格段无法可靠对应，请另存为 XLSX")
				}
				return officeNames, nil
			}
			if len(officeNames) == 1 {
				return officeNames, nil
			}
			return []string{""}, nil
		}
		if err != nil {
			return nil, err
		}
		if t.kind == 'c' && !inside && tables == 0 {
			if metadata != nil && target < 0 && strings.Contains(strings.ToLower(t.data), "<x:displaygridlines") {
				ensureSheet()
				metadata.Sheets[sheet].GridLines = true
			}
			for _, match := range htmlWorksheetNames.FindAllStringSubmatch(t.data, -1) {
				officeNames = append(officeNames, html.UnescapeString(match[1]))
				if len(officeNames) > 4096 {
					return nil, errors.New("HTML 工作表数量超过安全上限")
				}
			}
		}
		if t.kind == 'd' {
			lower := strings.ToLower(t.data)
			if strings.Contains(lower, "[if supportmisalignedcolumns]") {
				helper = true
			} else if strings.Contains(lower, "[endif]") {
				helper = false
			}
		}
		if t.kind == 's' && t.name == "head" {
			body = false
		}
		if t.kind == 'e' && t.name == "head" {
			body = true
		}
		if t.kind == 's' && t.name == "style" && metadata != nil && target < 0 {
			css, e := h.until("</style>", maxMetadataPart)
			if e != nil {
				return nil, e
			}
			metadata.addCSS(css)
			continue
		}
		if t.kind == 's' && (t.name == "script" || t.name == "style") {
			if err := h.skipRaw(t.name); err != nil {
				return nil, err
			}
			continue
		}
		if helper {
			continue
		}
		if t.kind == 's' && t.name == "table" {
			if inside {
				return nil, errors.New("暂不支持 HTML 嵌套表格，请导出为平面表格或 XLSX")
			}
			if tables >= 200000 {
				return nil, errors.New("HTML 表格段数量超过安全上限")
			}
			if len(officeNames) > 1 {
				sheet = tables
				row = 0
			}
			tables++
			ensureSheet()
			inside = true
			col = 1
			layoutCol = 1
			if metadata != nil && target < 0 {
				tableWidth = metadata.dimension(t, "width", 0)
			}
			clear(occupied)
			occupiedMax = 0
			paragraphOpen = false
			continue
		}
		if inside && !rowOpen && t.kind == 's' && t.name == "col" && metadata != nil && target < 0 {
			span := 1
			if value, ok := t.attr("span"); ok {
				if parsed, e := strconv.Atoi(value); e == nil && parsed > 0 && parsed <= 16384 {
					span = parsed
				}
			}
			metadata.Sheets[sheet].addColumns(layoutCol, span, metadata.dimension(t, "width", tableWidth), true)
			layoutCol += span
			continue
		}
		if t.kind == 'e' && t.name == "table" && inside {
			if err := flushRow(); err != nil {
				return nil, err
			}
			inside = false
			continue
		}
		if !inside {
			if !body {
				continue
			}
			if t.kind == 's' && t.name == "p" {
				paragraphOpen = true
				paragraph.Reset()
			}
			if paragraphOpen && t.kind == 't' {
				paragraph.WriteString(html.UnescapeString(t.data))
				if paragraph.Len() > maxExternalCell {
					return nil, errors.New("HTML 段落超过 8MB 安全上限")
				}
			}
			if paragraphOpen && t.kind == 'e' && t.name == "p" {
				paragraphOpen = false
				row++
				if row > maxExternalRows {
					return nil, errors.New("HTML 工作表超过行数安全上限")
				}
				if err := output([]string{strings.TrimSpace(paragraph.String())}, nil); err != nil {
					return nil, err
				}
			}
			continue
		}
		if t.kind == 's' && t.name == "tr" {
			if err := flushRow(); err != nil {
				return nil, err
			}
			row++
			if row > maxExternalRows {
				return nil, errors.New("HTML 工作表超过行数安全上限")
			}
			rowOpen = true
			rowSize = 0
			if metadata != nil && target < 0 {
				rowSize = metadata.dimension(t, "height", 0)
			}
			col = 1
			fields = nil
			cellStyles = nil
			if target >= 0 && active() {
				for c := 1; c <= occupiedMax; c++ {
					if occupied[c] >= row {
						for len(fields) < c {
							fields = append(fields, "")
							cellStyles = append(cellStyles, 0)
						}
					}
				}
			}
		} else if t.kind == 'e' && t.name == "tr" {
			if err := flushRow(); err != nil {
				return nil, err
			}
		} else if t.kind == 's' && (t.name == "td" || t.name == "th") {
			if !rowOpen {
				return nil, errors.New("HTML 单元格缺少所属行")
			}
			if err := flushCell(); err != nil {
				return nil, err
			}
			colspan, rowspan = 1, 1
			for _, item := range []struct {
				name string
				span *int
				max  int
			}{{"colspan", &colspan, 16384}, {"rowspan", &rowspan, maxExternalRows}} {
				if value, ok := t.attr(item.name); ok {
					span, e := strconv.Atoi(value)
					if e != nil || span < 1 || span > item.max {
						return nil, fmt.Errorf("HTML %s 范围无效：%q", item.name, value)
					}
					*item.span = span
				}
			}
			cellOpen = true
			cellWidth = 0
			if metadata != nil && target < 0 {
				cellWidth = metadata.dimension(t, "width", tableWidth)
				if rowSize == 0 && rowspan == 1 {
					rowSize = metadata.dimension(t, "height", 0)
				}
			}
			chosenStyle = false
			stack = []htmlTableToken{t}
			cellStyle = 0
			if metadata != nil {
				cellStyle, err = metadata.styleForChain(stack, target < 0)
				if err != nil {
					return nil, err
				}
			}
		} else if t.kind == 'e' && (t.name == "td" || t.name == "th") {
			if err := flushCell(); err != nil {
				return nil, err
			}
		} else if cellOpen {
			if t.kind == 's' && htmlInlineStyleTag(t.name) {
				if len(stack) >= 64 {
					return nil, errors.New("HTML 单元格样式嵌套超过 64 层安全上限")
				}
				stack = append(stack, t)
			}
			if t.kind == 'e' && htmlInlineStyleTag(t.name) {
				for i := len(stack) - 1; i >= 1; i-- {
					if stack[i].name == t.name {
						stack = stack[:i]
						break
					}
				}
				if t.name == "p" && text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
					text.WriteByte('\n')
					pendingSpace = false
				}
			}
			if t.kind == 't' {
				if !utf8.ValidString(t.data) {
					return nil, errors.New("HTML 单元格含无效 UTF-8 编码")
				}
				value := html.UnescapeString(t.data)
				if !chosenStyle && strings.Trim(value, " \t\r\n\f") != "" && metadata != nil {
					cellStyle, err = metadata.styleForChain(stack, target < 0)
					if err != nil {
						return nil, err
					}
					chosenStyle = true
				}
				if target >= 0 && active() {
					appendText(value)
				}
			} else if t.kind == 's' && t.name == "br" && target >= 0 && active() {
				text.WriteByte('\n')
				pendingSpace = false
			}
			if text.Len() > maxExternalCell {
				return nil, errors.New("HTML 单元格超过 8MB 安全上限")
			}
		}
	}
}

func htmlInlineStyleTag(name string) bool {
	switch name {
	case "p", "span", "b", "strong", "i", "em", "u", "s", "strike", "font", "div":
		return true
	}
	return false
}

func htmlLiteralValue(raw string) (string, bool) {
	if len(raw) < 3 || !strings.HasPrefix(raw, "=\"") || !strings.HasSuffix(raw, "\"") {
		return raw, false
	}
	inner := raw[2 : len(raw)-1]
	var value strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '"' {
			if i+1 >= len(inner) || inner[i+1] != '"' {
				return raw, false
			}
			i++
		}
		value.WriteByte(inner[i])
	}
	return value.String(), true
}

func htmlExternalCell(raw string, style, col int) externalCell {
	c := externalCell{Col: col, Raw: raw, Style: style, Type: 's'}
	if value, ok := htmlLiteralValue(raw); ok {
		c.Raw = value
		c.Formula = raw
		c.HasFormula = true
	}
	return c
}
