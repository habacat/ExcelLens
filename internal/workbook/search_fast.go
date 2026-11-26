package workbook

// SearchSheet scans the extracted OOXML worksheet as a sequential byte stream.
// Unlike the general-purpose row reader it does not allocate a full XML decoder
// per row or materialize every cell. It is a search-only scanner: cell details
// are decoded when a hit is found, and regular XML decoding remains available
// for rendering, which makes unusual format features safer to maintain.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"excellens/internal/formats"
)

func (w *Workbook) SearchSheet(idx *SheetIndex, sheetName, query, mode string, limit int, cancel <-chan struct{}, progress func(int, int, []SearchResult)) ([]SearchResult, bool, error) {
	if mode != "value" && mode != "reference" {
		mode = "formula"
	}
	ctx, stop := searchContext(cancel)
	defer stop()
	state, err := w.SearchSheetResumable(ctx, idx, sheetName, query, mode, limit, SearchCheckpoint{}, nil, progress)
	return state.Matches, state.Truncated, err
}

func (w *Workbook) searchSimpleResumable(ctx context.Context, idx *SheetIndex, sheetName, query, mode string, limit int, state SearchCheckpoint, save SearchCheckpointSave, progress func(int, int, []SearchResult), stream *searchStream) (SearchCheckpoint, error) {
	matcher, err := newSearchMatcher(sheetName, query, mode)
	if err != nil {
		return state, err
	}
	if isExternalRows(idx.XMLPath) {
		return w.searchExternalResumable(ctx, idx, sheetName, query, mode, limit, state, save, progress, stream)
	}
	budget, err := newSearchHitBudget(save, state.Matches, stream)
	if err != nil {
		return state, err
	}
	matches := state.Matches
	total := idx.RowCount()
	if state.Next == total {
		state.Complete = true
		return state, saveSearchCheckpoint(state, save, true, stream)
	}
	file, err := os.Open(idx.XMLPath)
	if err != nil {
		return state, err
	}
	defer file.Close()
	offset := idx.RowAt(state.Next).Start
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return state, err
	}
	reader := bufio.NewReaderSize(file, 1<<20)
	var buf []byte
	var anchorMatches map[string]bool
	var referenceGroups map[string]sharedRefCandidates
	if mode == "reference" {
		referenceGroups = make(map[string]sharedRefCandidates, 32)
	}
	if mode == "formula" && !matcher.relativeDependent {
		anchorMatches = make(map[string]bool, len(idx.SharedAnchors))
		for key, anchor := range idx.SharedAnchors {
			anchorMatches[key] = containsFold(anchor.Formula, query)
		}
	}
	lastProgress := time.Now()
	for i := state.Next; i < total; i++ {
		rowOff := idx.RowAt(i)
		if i&7 == 0 {
			if err := ctx.Err(); err != nil {
				return state, err
			}
		}
		if rowOff.Start < offset {
			return state, fmt.Errorf("行索引重叠：%d", rowOff.Number)
		}
		if skip := rowOff.Start - offset; skip > 0 {
			n, e := io.CopyN(io.Discard, reader, skip)
			offset += n
			if e != nil {
				return state, e
			}
		}
		length := rowOff.End - rowOff.Start
		if length <= 0 || length > 128<<20 {
			return state, fmt.Errorf("行 %d 长度超出限制", rowOff.Number)
		}
		if int64(cap(buf)) < length {
			buf = make([]byte, length)
		}
		row := buf[:int(length)]
		if _, err = io.ReadFull(reader, row); err != nil {
			return state, err
		}
		offset += length
		var rowErr error
		truncated := false
		// Uncommon valid OOXML features require a structural decoder. They
		// must never be silently interpreted as raw XML text or duplicate
		// annotated phonetic syllables in search results.
		special := bytes.Contains(row, []byte("<![CDATA[")) ||
			bytes.Contains(row, []byte("<rPh")) || bytes.Contains(row, []byte(":rPh")) ||
			bytes.Contains(row, []byte("<extLst")) || bytes.Contains(row, []byte("<mc:AlternateContent"))
		if special {
			parsed, parseErr := w.readRowFromFile(file, idx, i, 1, formats.ExcelColumns)
			if parseErr != nil {
				return state, parseErr
			}
			for _, cell := range parsed.Cells {
				matched := false
				if mode == "value" {
					matched = containsFold(cell.Raw, query) || containsFold(cell.Display, query)
				} else if cell.HasFormula && cell.Formula != "" {
					matched = matcher.matchFormula(strings.TrimPrefix(cell.Formula, "="))
				}
				if !matched {
					continue
				}
				hit := SearchResult{Sheet: sheetName, Ref: cell.Ref, Row: rowOff.Number, Col: cell.Col, Value: cell.Display}
				if mode != "value" {
					hit.Formula = cell.Formula
				}
				if err := budget.collect(&matches, hit); err != nil {
					return state, err
				}
				if len(matches) >= limit {
					truncated = true
					break
				}
			}
		} else {
			lastColumn := 0
			scanRowCells(row, func(tag, body []byte) bool {
				// Read the three common attributes once; construct the cell
				// reference string only on a search hit or an actual formula.
				// On multi-million-cell ledgers, most cells never match.
				refRaw, typ, styleRaw := xmlCellAttributes(tag)
				if len(refRaw) == 0 {
					// Valid OOXML permits omitted cell addresses. Every <c>,
					// including self-closing style-only cells, advances the
					// implicit column position (see scanRowCells below).
					lastColumn++
					if lastColumn > formats.ExcelColumns {
						rowErr = fmt.Errorf("第 %d 行隐式列超出 Excel 上限", rowOff.Number)
						return false
					}
				} else {
					lastColumn = parseColumnBytes(refRaw)
					if lastColumn == 0 {
						rowErr = fmt.Errorf("第 %d 行的单元格地址 %q 无效", rowOff.Number, refRaw)
						return false
					}
				}
				if matcher.mode == "value" {
					cellValue, hasValue := w.fastCellValueType(typ, body)
					if !hasValue {
						return true
					}
					valueMatched := containsFold(cellValue, query)
					style := 0
					if !valueMatched && (len(typ) == 0 || bytes.Equal(typ, []byte("n"))) {
						// Numeric presentation (dates, percent and grouping) only needs
						// to be computed when raw text did not already match.
						style = parsePositiveNonNegativeBytes(styleRaw)
						valueMatched = containsFold(w.formatNumeric(cellValue, style), query)
					}
					if valueMatched {
						ref := string(refRaw)
						if ref == "" {
							ref = ColumnName(lastColumn) + strconv.Itoa(rowOff.Number)
						}
						hit, e := w.fastSearchResult(tag, body, idx, rowOff.Number, ref, sheetName, mode)
						if e != nil {
							rowErr = e
							return false
						}
						if rowErr = budget.collect(&matches, hit); rowErr != nil {
							return false
						}
					}
				} else {
					formulaTag, formulaBytes, found := fastElement(body, "f")
					if !found {
						return true
					}
					ref := string(refRaw)
					if ref == "" {
						ref = ColumnName(lastColumn) + strconv.Itoa(rowOff.Number)
					}
					si := string(xmlAttribute(formulaTag, "si"))
					var formula string
					if len(formulaBytes) > 0 {
						formula = string(formulaBytes)
						if bytes.IndexByte(formulaBytes, '&') >= 0 {
							formula = html.UnescapeString(formula)
						}
					} else if si != "" {
						anchor, ok := idx.SharedAnchors[si]
						if !ok {
							return true
						}
						if anchorMatches != nil && !anchorMatches[si] {
							return true
						}
						if mode == "reference" {
							candidates, found := referenceGroups[si]
							if !found {
								candidates = newSharedRefCandidates(anchor)
								referenceGroups[si] = candidates
							}
							if !candidates.potentiallyReferences(ref, matcher.targetCol, matcher.targetRow) {
								return true
							}
						}
						formula = translateSharedFormula(anchor.Formula, anchor.Ref, ref)
					}
					if formula == "" {
						return true
					}
					if matcher.matchFormula(formula) {
						hit, e := w.fastSearchResult(tag, body, idx, rowOff.Number, ref, sheetName, mode)
						if e != nil {
							rowErr = e
							return false
						}
						if rowErr = budget.collect(&matches, hit); rowErr != nil {
							return false
						}
					}
				}
				if len(matches) >= limit {
					truncated = true
					return false
				}
				return true
			})
		}
		if rowErr != nil {
			return state, rowErr
		}
		if truncated {
			state.Matches, state.Complete, state.Truncated = matches, true, true
			if err := saveSearchCheckpoint(state, save, true, stream); err != nil {
				return state, err
			}
			if progress != nil {
				progress(i+1, total, matches)
			}
			return state, nil
		}
		state.Next, state.Matches, state.Complete = i+1, matches, i+1 == total
		if err := saveSearchCheckpoint(state, save, state.Complete, stream); err != nil {
			return state, err
		}
		if progress != nil && (i == total-1 || time.Since(lastProgress) > 120*time.Millisecond) {
			progress(i+1, total, matches)
			lastProgress = time.Now()
		}
	}
	return state, nil
}

type searchMatcher struct {
	mode              string
	query             string
	refSheet          string
	relativeDependent bool
	qualified         *regexp.Regexp
	unqualified       *regexp.Regexp
	currentSheet      string
	targetCol         int
	targetRow         int
}

func newSearchMatcher(sheetName, query, mode string) (searchMatcher, error) {
	m := searchMatcher{mode: mode, query: query, refSheet: sheetName, currentSheet: sheetName}
	// A translated address can change even a one-letter or a whole-column
	// substring. Only a function token ending in '(' is invariant under fill.
	function, call := strings.CutSuffix(query, "(")
	m.relativeDependent = !call || function == "" || strings.IndexFunc(function, func(c rune) bool {
		return !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == '.')
	}) >= 0
	if mode != "reference" {
		return m, nil
	}
	ref := query
	if bang := strings.LastIndex(ref, "!"); bang >= 0 {
		m.refSheet = strings.ReplaceAll(strings.Trim(ref[:bang], "'"), "''", "'")
		ref = ref[bang+1:]
	}
	col, row, err := ParseCellRef(ref)
	m.targetCol, m.targetRow = col, row
	if err != nil {
		return m, errors.New("引用追踪地址无效")
	}
	c := regexp.QuoteMeta(ColumnName(col))
	r := regexp.QuoteMeta(itoa(row))
	m.unqualified = regexp.MustCompile(`(?i)(^|[^A-Z0-9_.!])\$?` + c + `\$?` + r + `([^A-Z0-9_.]|$)`)
	quoted := regexp.QuoteMeta(strings.ReplaceAll(m.refSheet, "'", "''"))
	plain := regexp.QuoteMeta(m.refSheet)
	m.qualified = regexp.MustCompile(`(?i)(?:'` + quoted + `'|` + plain + `)!\$?` + c + `\$?` + r + `([^A-Z0-9_.]|$)`)
	m.relativeDependent = true
	return m, nil
}
func (m searchMatcher) matchFormula(formula string) bool {
	if m.mode != "reference" {
		return containsFold(formula, m.query)
	}
	formula = stripFormulaStringLiterals(formula)
	if m.qualified.MatchString(formula) {
		return true
	}
	return strings.EqualFold(m.currentSheet, m.refSheet) && m.unqualified.MatchString(formula)
}

// scanRowCells scans only <c> elements. Text nodes, XML comments and rich text
// do not contain worksheet-cell tags in valid OOXML.
func scanRowCells(row []byte, callback func(tag, body []byte) bool) {
	p := 0
	for p < len(row) {
		i := bytes.Index(row[p:], []byte("<c"))
		if i < 0 {
			return
		}
		p += i
		if p+2 >= len(row) {
			return
		}
		if !isXMLSpace(row[p+2]) && row[p+2] != '>' && row[p+2] != '/' {
			p += 2
			continue
		}
		end := fastTagEnd(row, p)
		if end < 0 {
			return
		}
		tag := row[p : end+1]
		if bytes.HasSuffix(bytes.TrimSpace(tag), []byte("/>")) {
			// Even an empty cell participates in implicit-column addressing.
			if !callback(tag, nil) {
				return
			}
			p = end + 1
			continue
		}
		closeAt := bytes.Index(row[end+1:], []byte("</c>"))
		if closeAt < 0 {
			return
		}
		bodyStart := end + 1
		bodyEnd := bodyStart + closeAt
		if !callback(tag, row[bodyStart:bodyEnd]) {
			return
		}
		p = bodyEnd + len("</c>")
	}
}
func fastTagEnd(b []byte, start int) int {
	quote := byte(0)
	for i := start; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '>' {
			return i
		}
	}
	return -1
}

// fastElement recognizes normal and self-closing OOXML elements and preserves
// the encoded text for the caller to unescape only when necessary.
func fastElement(body []byte, name string) (tag, content []byte, found bool) {
	prefix := "<" + name
	p := 0
	for p < len(body) {
		i := bytes.Index(body[p:], []byte(prefix))
		if i < 0 {
			return nil, nil, false
		}
		i += p
		next := i + len(prefix)
		if next >= len(body) {
			return nil, nil, false
		}
		if !isXMLSpace(body[next]) && body[next] != '>' && body[next] != '/' {
			p = next
			continue
		}
		end := fastTagEnd(body, i)
		if end < 0 {
			return nil, nil, false
		}
		tag = body[i : end+1]
		if bytes.HasSuffix(bytes.TrimSpace(tag), []byte("/>")) {
			return tag, nil, true
		}
		closing := []byte("</" + name + ">")
		k := bytes.Index(body[end+1:], closing)
		if k < 0 {
			return nil, nil, false
		}
		return tag, body[end+1 : end+1+k], true
	}
	return nil, nil, false
}
func parsePositiveNonNegativeBytes(b []byte) int {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0
		}
	}
	return n
}
func (w *Workbook) fastCellValue(tag, body []byte) (string, bool) {
	return w.fastCellValueType(xmlAttribute(tag, "t"), body)
}

func (w *Workbook) fastCellValueType(typ, body []byte) (string, bool) {
	_, raw, ok := fastElement(body, "v")
	if bytes.Equal(typ, []byte("inlineStr")) {
		inline, found := inlineStringValue(body)
		if found {
			return inline, true
		}
	}
	if !ok {
		return "", false
	}
	v := xmlUnescape(raw)
	if bytes.Equal(typ, []byte("s")) {
		n, e := parseNonNegativeInt(v)
		if e != nil || n >= len(w.Shared) {
			return "[共享字符串索引无效]", true
		}
		return w.Shared[n], true
	}
	if bytes.Equal(typ, []byte("b")) {
		if v == "1" {
			return "TRUE", true
		}
		if v == "0" {
			return "FALSE", true
		}
	}
	return v, true
}
func inlineStringValue(body []byte) (string, bool) {
	_, inner, ok := fastElement(body, "is")
	if !ok {
		return "", false
	}
	if value, simple := fastSimpleTextContent(bytes.TrimSpace(inner)); simple {
		return value, true
	}
	if value, simple := fastSimpleRichContent(inner); simple {
		return value, true
	}
	// Only direct plain text or the validated flat-run subset bypasses XML.
	// Phonetics, namespaces, nested font metadata and extensions share the
	// reference decoder; descendant <t> is never promoted into the value.
	dec := xml.NewDecoder(io.MultiReader(strings.NewReader("<is>"), bytes.NewReader(inner), strings.NewReader("</is>")))
	start, err := dec.Token()
	if err != nil {
		return "", false
	}
	value, err := readInlineText(dec, start.(xml.StartElement))
	return value, err == nil
}
func (w *Workbook) fastSearchResult(tag, body []byte, idx *SheetIndex, rowNumber int, ref, sheet, mode string) (SearchResult, error) {
	col := parseColumnBytes([]byte(ref))
	v, _ := w.fastCellValue(tag, body)
	display := v
	typ := xmlAttribute(tag, "t")
	if mode == "value" && (len(typ) == 0 || bytes.Equal(typ, []byte("n"))) {
		display = w.formatNumeric(v, parsePositiveNonNegativeBytes(xmlAttribute(tag, "s")))
	}
	form := ""
	if fTag, fValue, ok := fastElement(body, "f"); ok && mode != "value" {
		if len(fValue) > 0 {
			form = "=" + html.UnescapeString(string(fValue))
		} else if si := string(xmlAttribute(fTag, "si")); si != "" {
			if anchor, has := idx.SharedAnchors[si]; has {
				form = "=" + translateSharedFormula(anchor.Formula, anchor.Ref, ref)
			}
		}
	}
	return SearchResult{Sheet: sheet, Ref: ref, Row: rowNumber, Col: col, Formula: form, Value: display}, nil
}
