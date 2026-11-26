package workbook

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

type tagInfo struct {
	name        []byte
	closing     bool
	selfClosing bool
	valid       bool
}

func scanWorksheet(source io.Reader, total int64, cancel <-chan struct{}, progress func(done, total int64, stage string)) ([]RowOffset, map[string]SharedFormulaAnchor, string, int, int, error) {
	return scanWorksheetWithShared(source, total, cancel, progress, nil)
}

// scanWorksheetWithShared builds the *content* extent, not the styled extent.
// It deliberately ignores <c s="..."/> and empty v/t elements, including
// surrounding XML indentation. Shared-string references to blank strings are
// also excluded, without decoding a full XML tree for every cell.
func scanWorksheetWithShared(source io.Reader, total int64, cancel <-chan struct{}, progress func(done, total int64, stage string), shared []string) ([]RowOffset, map[string]SharedFormulaAnchor, string, int, int, error) {
	return scanWorksheetWithSharedHint(source, total, cancel, progress, shared, nil)
}

// The hyperlink tag offset is captured for free during the normal XML scan,
// avoiding an additional 500MB+ disk scan on initial viewport load.
func scanWorksheetWithSharedHint(source io.Reader, total int64, cancel <-chan struct{}, progress func(done, total int64, stage string), shared []string, hyperlinkStart *int64, layouts ...*SourceLayout) ([]RowOffset, map[string]SharedFormulaAnchor, string, int, int, error) {
	var layout *SourceLayout
	if len(layouts) > 0 {
		layout = layouts[0]
	}
	counted := &countingReader{r: source}
	reader := bufio.NewReaderSize(counted, 1<<20)
	rows := make([]RowOffset, 0, 4096)
	anchors := make(map[string]SharedFormulaAnchor)
	dimension := ""
	maxRow, maxCol := 0, 0
	rowStart := int64(0)
	rowNumber := 0
	inRow := false
	cellOpen := false
	cellColumn := 0
	cellPayload := false
	inCellValue := false
	inCellFormula := false
	cellDepth, inlineDepth, runDepth, valueDepth, formulaDepth := 0, 0, 0, 0, 0
	// Phonetic guidance / extension text must never count as a cell value.
	ignoredTextDepth := 0
	cellIsShared := false
	sharedValue := make([]byte, 0, 12)
	// A styled <c r="XFD1048576" s="17"/> is NOT used data.
	// Only formulas or nonempty cached/inline values extend the used range.
	currentCellRef := []byte(nil)
	anchorID := ""
	anchorRef := ""
	formulaText := make([]byte, 0, 128)
	inAnchorFormula := false
	lastProgress := int64(0)
	// A single tag buffer serves the entire XML scan. Attribute slices are only
	// valid until the next tag; references kept across tags are copied below.
	tagBuffer := make([]byte, 0, 128)
	var cellRefSmall [24]byte

	for {
		chunk, readErr := reader.ReadSlice('<')
		if len(chunk) > 0 {
			textPart := chunk
			hasTagStart := chunk[len(chunk)-1] == '<'
			if hasTagStart {
				textPart = chunk[:len(chunk)-1]
			}
			if layout != nil && layout.catalogue != nil && layout.catalogue.rootClosed && len(bytes.TrimSpace(textPart)) > 0 {
				layout.catalogue.notice = "工作表根结束后含无效文字，表声明未确认"
			}
			if cellOpen && inCellValue && cellDepth == valueDepth && len(bytes.TrimSpace(textPart)) > 0 {
				if cellIsShared {
					// Shared-string indexes are tiny. Do not buffer hostile
					// arbitrarily long XML text into memory.
					if len(sharedValue)+len(textPart) > 32 {
						cellPayload = true
					} else {
						sharedValue = append(sharedValue, textPart...)
					}
				} else {
					cellPayload = true
				}
			}
			if cellOpen && inCellFormula && cellDepth == formulaDepth && len(bytes.TrimSpace(textPart)) > 0 {
				cellPayload = true
			}
			if inAnchorFormula && cellDepth == formulaDepth && len(textPart) > 0 {
				if len(formulaText)+len(textPart) > 1<<20 {
					return nil, nil, "", 0, 0, errors.New("共享公式超过 1 MB 安全上限")
				}
				formulaText = append(formulaText, textPart...)
			}
			if hasTagStart {
				tagStart := counted.n - int64(reader.Buffered()) - 1
				tag, err := readXMLTag(reader, tagBuffer)
				tagBuffer = tag
				if err != nil {
					return nil, nil, "", 0, 0, fmt.Errorf("读取 XML 标签失败：%w", err)
				}
				// CDATA inside a value/formula is character data, not a
				// structural XML tag. Without this branch a valid cell whose
				// only contents are CDATA can disappear from used-range indexes.
				if bytes.HasPrefix(tag, []byte("<![CDATA[")) && bytes.HasSuffix(tag, []byte("]]>")) {
					content := tag[len("<![CDATA[") : len(tag)-len("]]>")]
					if cellOpen && inCellValue && cellDepth == valueDepth && len(bytes.TrimSpace(content)) > 0 {
						if cellIsShared && len(sharedValue)+len(content) <= 32 {
							sharedValue = append(sharedValue, content...)
						} else {
							cellPayload = true
						}
					}
					if cellOpen && inCellFormula && cellDepth == formulaDepth && len(bytes.TrimSpace(content)) > 0 {
						cellPayload = true
					}
					if inAnchorFormula && cellDepth == formulaDepth {
						if len(formulaText)+len(content) > 1<<20 {
							return nil, nil, "", 0, 0, errors.New("共享公式超过 1 MB 安全上限")
						}
						formulaText = append(formulaText, content...)
					}
				}
				info := inspectXMLTag(tag)
				if layout != nil && info.valid {
					n := 0
					if !info.closing && bytes.Equal(info.name, []byte("row")) {
						n = parsePositiveIntBytes(xmlAttribute(tag, "r"))
						if n <= 0 {
							n = len(rows) + 1
						}
					}
					layout.captureMetadataToken(tag, info, tagStart, counted.n-int64(reader.Buffered()), n)
				}
				if info.valid && !info.closing && hyperlinkStart != nil &&
					bytes.Equal(info.name, []byte("hyperlinks")) && *hyperlinkStart == -1 {
					*hyperlinkStart = tagStart
				}
				if info.valid {
					depth := 0
					if cellOpen {
						if info.closing {
							depth = cellDepth
							if cellDepth > 1 {
								cellDepth--
							}
						} else {
							depth = cellDepth + 1
							if !info.selfClosing {
								cellDepth = depth
							}
						}
					}
					if info.closing {
						if bytes.Equal(info.name, []byte("r")) && depth == runDepth {
							runDepth = 0
						}
						if bytes.Equal(info.name, []byte("is")) && depth == inlineDepth {
							inlineDepth, runDepth = 0, 0
						}
						if (bytes.Equal(info.name, []byte("rPh")) || bytes.Equal(info.name, []byte("extLst"))) && ignoredTextDepth > 0 {
							ignoredTextDepth--
						}
						switch {
						case bytes.Equal(info.name, []byte("f")) && depth == formulaDepth:
							inCellFormula = false
							if inAnchorFormula && anchorID != "" {
								formula := strings.TrimSpace(html.UnescapeString(string(formulaText)))
								if len(formulaText) > 1<<20 {
									return nil, nil, "", 0, 0, errors.New("共享公式超过 1 MB 安全上限")
								}
								if formula != "" {
									anchors[anchorID] = SharedFormulaAnchor{Ref: anchorRef, Formula: formula}
								}
							}
							inAnchorFormula = false
							formulaDepth = 0
							anchorID, anchorRef = "", ""
							formulaText = formulaText[:0]
						case (bytes.Equal(info.name, []byte("v")) || bytes.Equal(info.name, []byte("t"))) && depth == valueDepth:
							if cellOpen && cellIsShared && bytes.Equal(info.name, []byte("v")) && len(sharedValue) > 0 {
								index, err := strconv.Atoi(string(bytes.TrimSpace(sharedValue)))
								if err != nil || index < 0 || len(shared) == 0 || index >= len(shared) || strings.TrimSpace(shared[index]) != "" {
									cellPayload = true
								}
							}
							// Whitespace *after* </v> but before </c> must not turn
							// a styled blank into a used cell.
							inCellValue = false
							valueDepth = 0
						case bytes.Equal(info.name, []byte("c")) && depth == 1:
							if cellOpen && cellPayload {
								if rowNumber > maxRow {
									maxRow = rowNumber
								}
								if cellColumn > maxCol {
									maxCol = cellColumn
								}
							}
							cellOpen = false
							cellDepth, inlineDepth, runDepth, valueDepth, formulaDepth = 0, 0, 0, 0, 0
							inCellValue = false
							inCellFormula = false
							cellIsShared = false
							currentCellRef = nil
						case bytes.Equal(info.name, []byte("row")):
							if !inRow {
								return nil, nil, "", 0, 0, errors.New("发现未匹配的工作表行结束标签")
							}
							end := counted.n - int64(reader.Buffered())
							if rowNumber <= 0 {
								rowNumber = len(rows) + 1
							}
							if rowNumber > maxExcelRows {
								return nil, nil, "", 0, 0, fmt.Errorf("工作表行号超出 Excel 上限：%d", rowNumber)
							}
							rows = append(rows, RowOffset{Number: rowNumber, Start: rowStart, End: end})
							inRow = false
							rowNumber = 0
							currentCellRef = nil
						}
					} else {
						switch {
						case bytes.Equal(info.name, []byte("dimension")):
							if ref := xmlAttribute(tag, "ref"); len(ref) > 0 {
								dimension = html.UnescapeString(string(ref))
							}
						case bytes.Equal(info.name, []byte("row")):
							if inRow {
								return nil, nil, "", 0, 0, errors.New("工作表行标签嵌套，XML 结构异常")
							}
							inRow = !info.selfClosing
							rowStart = tagStart
							rowNumber = parsePositiveIntBytes(xmlAttribute(tag, "r"))
							cellColumn = 0
							if info.selfClosing {
								if rowNumber <= 0 {
									rowNumber = len(rows) + 1
								}
								rows = append(rows, RowOffset{Number: rowNumber, Start: rowStart, End: counted.n - int64(reader.Buffered())})

							}
						case bytes.Equal(info.name, []byte("rPh")) || bytes.Equal(info.name, []byte("extLst")):
							if !info.selfClosing {
								ignoredTextDepth++
							}
						case bytes.Equal(info.name, []byte("c")) && inRow && !cellOpen && ignoredTextDepth == 0:
							refAttr, typeAttr, _ := xmlCellAttributes(tag)
							currentCellRef = append(cellRefSmall[:0], refAttr...)
							parsedCol := parseColumnBytes(currentCellRef)
							if parsedCol > 0 {
								cellColumn = parsedCol
							} else {
								cellColumn++ // valid OOXML may omit the cell address
							}
							cellOpen = !info.selfClosing
							cellDepth, inlineDepth, runDepth, valueDepth, formulaDepth = 1, 0, 0, 0, 0
							cellPayload = false
							inCellValue = false
							inCellFormula = false
							cellIsShared = bytes.Equal(typeAttr, []byte("s"))
							sharedValue = sharedValue[:0]
						case bytes.Equal(info.name, []byte("is")) && cellOpen && depth == 2 && !info.selfClosing:
							inlineDepth = depth
						case bytes.Equal(info.name, []byte("r")) && inlineDepth > 0 && depth == inlineDepth+1 && !info.selfClosing:
							runDepth = depth
						case cellOpen && ((bytes.Equal(info.name, []byte("v")) && depth == 2) || (bytes.Equal(info.name, []byte("t")) && inlineDepth > 0 && (depth == inlineDepth+1 || runDepth > 0 && depth == runDepth+1))):
							inCellValue = !info.selfClosing && ignoredTextDepth == 0
							if inCellValue {
								valueDepth = depth
							}
						case bytes.Equal(info.name, []byte("f")) && cellOpen && depth == 2:
							// A shared-formula follower often has no formula text,
							// but it still represents a real, nonempty formula.
							fType := string(xmlAttribute(tag, "t"))
							if fType == "shared" || fType == "array" || fType == "dataTable" {
								cellPayload = true
							}
							inCellFormula = !info.selfClosing
							if inCellFormula {
								formulaDepth = depth
							}
							if fType == "shared" {
								id := string(xmlAttribute(tag, "si"))
								ref := xmlAttribute(tag, "ref")
								if id != "" && len(ref) > 0 && !info.selfClosing {
									anchorID = id
									anchorRef = string(currentCellRef)
									inAnchorFormula = true
									formulaText = formulaText[:0]
								}
							}
						}
					}
				}
			}
		}

		current := counted.n - int64(reader.Buffered())
		if progress != nil && current-lastProgress >= 8<<20 {
			progress(current, total, "正在解压工作表并建立行索引")
			lastProgress = current
		}
		if current-lastProgress >= 4<<20 || readErr == bufio.ErrBufferFull {
			select {
			case <-cancel:
				return nil, nil, "", 0, 0, errors.New("index cancelled")
			default:
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return nil, nil, "", 0, 0, readErr
		}
	}
	if inRow {
		return nil, nil, "", 0, 0, errors.New("工作表 XML 在行数据中意外结束")
	}
	if maxRow == 0 {
		maxRow = 1
	}
	if maxCol == 0 {
		maxCol = 1
	}
	// Formatting-only rows after the last content row must not keep the
	// viewport, auto-filters or searches scrolling for empty rows forever.
	end := sort.Search(len(rows), func(i int) bool { return rows[i].Number > maxRow })
	rows = rows[:end]
	if layout != nil {
		layout.finishMetadataCapture()
	}
	return rows, anchors, dimension, maxRow, maxCol, nil
}

// maxWorksheetTagBytes bounds memory even when a malformed or adversarial
// workbook omits the closing >. Legitimate OOXML cell tags are tiny; long
// inline strings are consumed by ReadSlice and do not count as a tag.
const maxWorksheetTagBytes = 8 << 20

// readXMLTag consumes a tag as buffered spans rather than issuing a ReadByte
// syscall-like method dispatch for every byte. A bare '>' is a terminator only
// when outside a quoted attribute; CDATA/comments retain the strict sentinel
// reader below. The destination is reused across tags and never aliases bufio's
// transient internal buffer, so callers can safely inspect the tag afterward.
func readXMLTag(reader *bufio.Reader, reuse []byte) ([]byte, error) {
	tag := append(reuse[:0], '<')
	first, err := reader.ReadByte()
	if err != nil {
		return tag, err
	}
	tag = append(tag, first)
	if first == '!' {
		if peek, _ := reader.Peek(2); bytes.Equal(peek, []byte("--")) {
			_, _ = reader.Discard(2)
			tag = append(tag, '-', '-')
			return readUntilSequence(reader, tag, []byte("-->"))
		}
		if peek, _ := reader.Peek(7); bytes.Equal(peek, []byte("[CDATA[")) {
			_, _ = reader.Discard(7)
			tag = append(tag, []byte("[CDATA[")...)
			return readUntilSequence(reader, tag, []byte("]]>"))
		}
	}
	if first == '?' {
		return readUntilSequence(reader, tag, []byte("?>"))
	}
	var quote byte
	for {
		part, readErr := reader.ReadSlice('>')
		if len(tag)+len(part) > maxWorksheetTagBytes {
			return tag, errors.New("XML 标签超过 8 MB 安全上限")
		}
		// A portion may end at '>' within an attribute string. Preserve the quote
		// state across buffered boundaries and do not split quoted markup.
		for _, ch := range part {
			if quote != 0 {
				if ch == quote {
					quote = 0
				}
			} else if ch == '\'' || ch == '"' {
				quote = ch
			}
		}
		tag = append(tag, part...)
		if readErr == nil && quote == 0 {
			return tag, nil
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return tag, readErr
		}
	}
}

func readUntilSequence(reader *bufio.Reader, prefix, ending []byte) ([]byte, error) {
	out := prefix
	for {
		if len(out) >= maxWorksheetTagBytes {
			return out, errors.New("XML 标签超过 8 MB 安全上限")
		}
		c, err := reader.ReadByte()
		if err != nil {
			return out, err
		}
		out = append(out, c)
		if len(out) >= len(ending) && bytes.Equal(out[len(out)-len(ending):], ending) {
			return out, nil
		}
	}
}

func inspectXMLTag(tag []byte) tagInfo {
	if len(tag) < 3 || tag[0] != '<' || tag[1] == '!' || tag[1] == '?' {
		return tagInfo{}
	}
	i := 1
	closing := false
	if tag[i] == '/' {
		closing = true
		i++
	}
	for i < len(tag) && isXMLSpace(tag[i]) {
		i++
	}
	start := i
	for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' && tag[i] != '?' {
		i++
	}
	if start == i {
		return tagInfo{}
	}
	name := tag[start:i]
	if colon := bytes.LastIndexByte(name, ':'); colon >= 0 {
		name = name[colon+1:]
	}
	j := len(tag) - 2
	for j >= 0 && isXMLSpace(tag[j]) {
		j--
	}
	selfClosing := j >= 0 && tag[j] == '/'
	return tagInfo{name: name, closing: closing, selfClosing: selfClosing, valid: true}
}

func xmlAttribute(tag []byte, wanted string) []byte {
	i := 1
	if i < len(tag) && tag[i] == '/' {
		i++
	}
	for i < len(tag) && isXMLSpace(tag[i]) {
		i++
	}
	for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' && tag[i] != '?' {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (isXMLSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' || tag[i] == '?' {
			break
		}
		nameStart := i
		for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		name := tag[nameStart:i]
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		if i >= len(tag) {
			break
		}
		quote := tag[i]
		if quote != '\'' && quote != '"' {
			continue
		}
		i++
		valueStart := i
		for i < len(tag) && tag[i] != quote {
			i++
		}
		value := tag[valueStart:i]
		if i < len(tag) {
			i++
		}
		if bytes.Equal(name, []byte(wanted)) {
			return value
		}
	}
	return nil
}

func parsePositiveIntBytes(value []byte) int {
	if len(value) == 0 {
		return 0
	}
	n := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > maxExcelRows {
			return 0
		}
	}
	return n
}

func parseColumnBytes(ref []byte) int {
	if len(ref) == 0 {
		return 0
	}
	i := 0
	for i < len(ref) && ref[i] == '$' {
		i++
	}
	col := 0
	for i < len(ref) {
		c := ref[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c < 'A' || c > 'Z' {
			break
		}
		col = col*26 + int(c-'A'+1)
		if col > formats.ExcelColumns {
			return 0
		}
		i++
	}
	if col < 1 || col > formats.ExcelColumns {
		return 0
	}
	return col
}

func isXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
