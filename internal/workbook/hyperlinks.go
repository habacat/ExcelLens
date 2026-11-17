package workbook

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CellHyperlink is a resolved, safe hyperlink target. External links are
// restricted to http(s) and mailto. Source formulas and OOXML are never run.
type CellHyperlink struct {
	Kind    string `json:"kind"` // internal or external
	Sheet   string `json:"sheet,omitempty"`
	Ref     string `json:"ref,omitempty"`
	URL     string `json:"url,omitempty"`
	Tooltip string `json:"tooltip,omitempty"`
	Display string `json:"display,omitempty"`
}

type hyperlinkRange struct {
	firstRow, lastRow, firstCol, lastCol int
	link                                 CellHyperlink
}
type SheetHyperlinkIndex struct {
	single map[int]map[int]CellHyperlink
	ranges []hyperlinkRange
}

type rawHyperlink struct {
	Ref, RelID, Location, Display, Tooltip string
}

const maxWorksheetHyperlinks = 150000
const maxWorksheetHyperlinkXML int64 = 48 << 20

var hyperlinkGroupTag = regexp.MustCompile(`<(?:[A-Za-z_][A-Za-z0-9_.-]*:)?hyperlinks(?:\s|>)`)

// Search backward from the tail of the already-indexed XML. Excel writes
// <hyperlinks> after sheetData, so this normally reads only one small block.
// The bounded-memory fallback checks the full file for nonstandard exporters.
func findHyperlinkElement(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const blockSize int64 = 256 << 10
	size := st.Size()
	buf := make([]byte, int(blockSize)+96)
	next := make([]byte, 96)
	nextSize := 0
	for end := size; end > 0; {
		begin := end - blockSize
		if begin < 0 {
			begin = 0
		}
		n := int(end - begin)
		if _, err := f.ReadAt(buf[:n], begin); err != nil {
			return 0, err
		}
		copy(buf[n:n+nextSize], next[:nextSize])
		if match := hyperlinkGroupTag.FindIndex(buf[:n+nextSize]); match != nil {
			return begin + int64(match[0]), nil
		}
		nextSize = min(n, len(next))
		copy(next, buf[:nextSize])
		end = begin
	}
	return -1, nil
}

func parseHyperlinkElement(f *os.File, start int64) ([]rawHyperlink, error) {
	if start < 0 {
		return nil, nil
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	remain := st.Size() - start
	if remain > maxWorksheetHyperlinkXML {
		remain = maxWorksheetHyperlinkXML
	}
	decoder := xml.NewDecoder(io.NewSectionReader(f, start, remain))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("hyperlink XML start: %w", err)
	}
	root, ok := token.(xml.StartElement)
	if !ok || root.Name.Local != "hyperlinks" {
		return nil, errors.New("invalid hyperlink metadata root")
	}
	result := make([]rawHyperlink, 0, 8)
	for {
		token, err = decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("hyperlink XML truncated or invalid: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local != "hyperlink" {
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			entry := rawHyperlink{}
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "ref":
					entry.Ref = a.Value
				case "id":
					entry.RelID = a.Value
				case "location":
					entry.Location = a.Value
				case "display":
					entry.Display = a.Value
				case "tooltip":
					entry.Tooltip = a.Value
				}
			}
			result = append(result, entry)
			if len(result) > maxWorksheetHyperlinks {
				return nil, errors.New("too many worksheet hyperlinks")
			}
			if err := decoder.Skip(); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if t.Name.Local == "hyperlinks" {
				return result, nil
			}
		}
	}
}

func parseAddressRange(ref string) (firstRow, lastRow, firstCol, lastCol int, ok bool) {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(ref), "$", ""), ":")
	if len(parts) == 0 || len(parts) > 2 {
		return
	}
	c, r, err := ParseCellRef(parts[0])
	if err != nil {
		return
	}
	if r > maxExcelRows {
		return
	}
	firstRow, lastRow, firstCol, lastCol = r, r, c, c
	if len(parts) == 2 {
		c, r, err = ParseCellRef(parts[1])
		if err != nil {
			return
		}
		if r > maxExcelRows {
			return
		}
		if r < firstRow {
			firstRow, r = r, firstRow
		}
		lastRow = r
		if c < firstCol {
			firstCol, c = c, firstCol
		}
		lastCol = c
	}
	return firstRow, lastRow, firstCol, lastCol, true
}

func (w *Workbook) resolveHyperlinkLocation(location, sourceSheet string, depth int) (CellHyperlink, bool) {
	if depth > 4 {
		return CellHyperlink{}, false
	}
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(location), "#"))
	if target == "" || len(target) > 2048 {
		return CellHyperlink{}, false
	}
	if strings.HasPrefix(target, "=") {
		target = strings.TrimSpace(target[1:])
	}
	if strings.Contains(target, "%") {
		if decoded, err := url.PathUnescape(target); err == nil {
			target = decoded
		}
	}
	sheetName := sourceSheet
	address := target
	// The last ! outside quoted sheet names separates a worksheet name from
	// an address. Apostrophes within names are represented as doubled quotes.
	separator := -1
	quoted := false
	for i := 0; i < len(target); i++ {
		switch target[i] {
		case '\'':
			if quoted && i+1 < len(target) && target[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
		case '!':
			if !quoted {
				separator = i
			}
		}
	}
	if quoted {
		return CellHyperlink{}, false
	}
	if separator >= 0 {
		sheetName = strings.TrimSpace(target[:separator])
		address = target[separator+1:]
		if len(sheetName) >= 2 && sheetName[0] == '\'' && sheetName[len(sheetName)-1] == '\'' {
			sheetName = strings.ReplaceAll(sheetName[1:len(sheetName)-1], "''", "'")
		}
	}
	if separator < 0 {
		_, _, _, _, ok := parseAddressRange(address)
		if !ok {
			key := strings.ToLower(strings.TrimSpace(address))
			if scope := w.LocalNamedRanges[strings.ToLower(sourceSheet)]; scope != nil {
				if expr := scope[key]; expr != "" {
					return w.resolveHyperlinkLocation(expr, sourceSheet, depth+1)
				}
			}
			if expr := w.NamedRanges[key]; expr != "" {
				return w.resolveHyperlinkLocation(expr, sourceSheet, depth+1)
			}
			return CellHyperlink{}, false
		}
	}
	s, found := w.FindSheet(sheetName)
	if !found {
		return CellHyperlink{}, false
	}
	row1, _, col1, _, ok := parseAddressRange(address)
	if !ok {
		return CellHyperlink{}, false
	}
	return CellHyperlink{Kind: "internal", Sheet: s.Name, Ref: ColumnName(col1) + strconv.Itoa(row1)}, true
}

func safeExternalHyperlink(target string) (string, bool) {
	if target == "" || len(target) > 8192 {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" || u.User != nil {
			return "", false
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return "", false
		}
	default:
		return "", false
	}
	return u.String(), true
}

func (w *Workbook) resolveHyperlink(raw rawHyperlink, relationships map[string]relationship, sourceSheet string) (CellHyperlink, bool) {
	rel, hasRel := relationships[raw.RelID]
	if hasRel && strings.EqualFold(rel.TargetMode, "External") {
		if urlString, ok := safeExternalHyperlink(rel.Target); ok {
			if raw.Location != "" && strings.HasPrefix(urlString, "http") {
				if u, err := url.Parse(urlString); err == nil && u.Fragment == "" {
					u.Fragment = strings.TrimPrefix(raw.Location, "#")
					urlString = u.String()
				}
			}
			return CellHyperlink{Kind: "external", URL: urlString, Tooltip: raw.Tooltip, Display: raw.Display}, true
		}
		if strings.HasPrefix(rel.Target, "#") && raw.Location == "" {
			raw.Location = rel.Target
		}
	}
	if raw.Location != "" {
		if link, ok := w.resolveHyperlinkLocation(raw.Location, sourceSheet, 0); ok {
			link.Tooltip = raw.Tooltip
			link.Display = raw.Display
			return link, true
		}
	}
	if hasRel && strings.HasPrefix(rel.Target, "#") {
		if link, ok := w.resolveHyperlinkLocation(rel.Target, sourceSheet, 0); ok {
			link.Tooltip = raw.Tooltip
			link.Display = raw.Display
			return link, true
		}
	}
	return CellHyperlink{}, false
}

func (w *Workbook) SheetHyperlinks(meta SheetMeta) (*SheetHyperlinkIndex, error) {
	w.linkMu.Lock()
	defer w.linkMu.Unlock()
	if item, ok := w.linkCache[meta.Path]; ok {
		return item, nil
	}
	idx := w.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("sheet index is not available")
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	offset := idx.HyperlinkStart
	if offset == 0 || offset == -2 {
		// Backward compatibility with previously built XLIDX007 indexes.
		offset, err = findHyperlinkElement(f)
		if err != nil {
			return nil, err
		}
	}
	result := &SheetHyperlinkIndex{single: make(map[int]map[int]CellHyperlink)}
	if offset < 0 {
		w.storeHyperlinks(meta.Path, result)
		return result, nil
	}
	items, err := parseHyperlinkElement(f, offset)
	if err != nil {
		return nil, err
	}
	relationships := make(map[string]relationship)
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	wanted := relsPart(meta.Path)
	for _, entry := range zr.File {
		if path.Clean(strings.ReplaceAll(entry.Name, "\\", "/")) != wanted {
			continue
		}
		b, readErr := readZipEntry(map[string]*zip.File{wanted: entry}, wanted, maxMetadataPart)
		if readErr != nil {
			return nil, readErr
		}
		var rels relationshipsXML
		if readErr = xml.Unmarshal(b, &rels); readErr != nil {
			return nil, readErr
		}
		for _, rel := range rels.Relationships {
			if strings.HasSuffix(rel.Type, "/hyperlink") {
				relationships[rel.ID] = rel
			}
		}
		break
	}
	for _, raw := range items {
		r1, r2, c1, c2, valid := parseAddressRange(raw.Ref)
		if !valid {
			continue
		}
		link, ok := w.resolveHyperlink(raw, relationships, meta.Name)
		if !ok {
			continue
		} // Untrusted/unsupported targets must never execute.
		if r1 == r2 && c1 == c2 {
			if result.single[r1] == nil {
				result.single[r1] = make(map[int]CellHyperlink)
			}
			result.single[r1][c1] = link
		} else {
			result.ranges = append(result.ranges, hyperlinkRange{r1, r2, c1, c2, link})
		}
	}
	w.storeHyperlinks(meta.Path, result)
	return result, nil
}

func (w *Workbook) storeHyperlinks(key string, index *SheetHyperlinkIndex) {
	if w.linkCache == nil {
		w.linkCache = make(map[string]*SheetHyperlinkIndex)
	}
	w.linkCache[key] = index
}

func (index *SheetHyperlinkIndex) lookup(row, col int) (CellHyperlink, bool) {
	if index == nil {
		return CellHyperlink{}, false
	}
	if rowLinks := index.single[row]; rowLinks != nil {
		if link, ok := rowLinks[col]; ok {
			return link, true
		}
	}
	for _, area := range index.ranges {
		if row >= area.firstRow && row <= area.lastRow && col >= area.firstCol && col <= area.lastCol {
			return area.link, true
		}
	}
	return CellHyperlink{}, false
}

func (w *Workbook) linkFromCellFormula(cell CellData, sourceSheet string) (CellHyperlink, bool) {
	if !cell.HasFormula {
		return CellHyperlink{}, false
	}
	formula := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cell.Formula), "="))
	const prefix = "HYPERLINK("
	if len(formula) < len(prefix)+3 || !strings.EqualFold(formula[:len(prefix)], prefix) {
		return CellHyperlink{}, false
	}
	tail := strings.TrimLeft(formula[len(prefix):], " \t")
	if !strings.HasPrefix(tail, "\"") {
		return CellHyperlink{}, false
	}
	var target strings.Builder
	closed := false
	for i := 1; i < len(tail); i++ {
		switch tail[i] {
		case '"':
			if i+1 < len(tail) && tail[i+1] == '"' {
				target.WriteByte('"')
				i++
				continue
			}
			rest := strings.TrimSpace(tail[i+1:])
			closed = len(rest) > 0 && (rest[0] == ',' || rest[0] == ';' || rest[0] == ')')
			i = len(tail)
		default:
			target.WriteByte(tail[i])
		}
		if target.Len() > 8192 {
			return CellHyperlink{}, false
		}
	}
	if !closed {
		return CellHyperlink{}, false
	}
	val := target.String()
	if external, ok := safeExternalHyperlink(val); ok {
		return CellHyperlink{Kind: "external", URL: external}, true
	}
	return w.resolveHyperlinkLocation(val, sourceSheet, 0)
}

// Decorate visible data only. Never expand large hyperlink ranges into a
// million-cell map. A sheet with no hyperlinks costs one map lookup per cell.
func (w *Workbook) DecorateHyperlinks(meta SheetMeta, rows []RowData, columns []int, startCol, lastCol int) error {
	index, err := w.SheetHyperlinks(meta)
	if err != nil {
		return err
	}
	if index == nil || len(rows) == 0 {
		return nil
	}
	if len(columns) == 0 {
		for c := startCol; c <= lastCol; c++ {
			columns = append(columns, c)
		}
	}
	minRow, maxRow := rows[0].Number, rows[0].Number
	for _, row := range rows {
		if row.Number < minRow {
			minRow = row.Number
		}
		if row.Number > maxRow {
			maxRow = row.Number
		}
	}
	ranges := make([]hyperlinkRange, 0)
	for _, area := range index.ranges {
		if area.lastRow >= minRow && area.firstRow <= maxRow {
			ranges = append(ranges, area)
		}
	}
	for ri := range rows {
		row := &rows[ri]
		single := index.single[row.Number]
		if len(single) == 0 && len(ranges) == 0 {
			for ci := range row.Cells {
				if link, ok := w.linkFromCellFormula(row.Cells[ci], meta.Name); ok {
					row.Cells[ci].Hyperlink = &link
				}
			}
			continue
		}
		existing := make(map[int]int, len(row.Cells))
		for ci := range row.Cells {
			existing[row.Cells[ci].Col] = ci
		}
		for _, col := range columns {
			var link CellHyperlink
			ok := false
			if single != nil {
				link, ok = single[col]
			}
			if !ok {
				for _, area := range ranges {
					if row.Number >= area.firstRow && row.Number <= area.lastRow && col >= area.firstCol && col <= area.lastCol {
						link, ok = area.link, true
						break
					}
				}
			}
			ci, present := existing[col]
			if !ok {
				if present {
					if formulaLink, has := w.linkFromCellFormula(row.Cells[ci], meta.Name); has {
						row.Cells[ci].Hyperlink = &formulaLink
					}
				}
				continue
			}
			if present {
				row.Cells[ci].Hyperlink = &link
				continue
			}
			// Metadata may describe a link on a cell without a <c> node.
			// Only expose such a cell if Excel provides visible display text.
			if link.Display == "" {
				continue
			}
			row.Cells = append(row.Cells, CellData{Ref: ColumnName(col) + strconv.Itoa(row.Number), Col: col, Raw: link.Display, Display: link.Display, Type: "str", Hyperlink: &link})
		}
		if len(row.Cells) > 1 {
			sort.Slice(row.Cells, func(i, j int) bool { return row.Cells[i].Col < row.Cells[j].Col })
		}
	}
	return nil
}

func (w *Workbook) HyperlinkAt(meta SheetMeta, cell CellData, row, col int) (*CellHyperlink, error) {
	index, err := w.SheetHyperlinks(meta)
	if err != nil {
		return nil, err
	}
	if link, ok := index.lookup(row, col); ok {
		return &link, nil
	}
	if link, ok := w.linkFromCellFormula(cell, meta.Name); ok {
		return &link, nil
	}
	return nil, nil
}

// Named destinations come from workbook.xml; formula expressions
// and external references are deliberately not evaluated.
func readNamedRanges(data []byte, original []workbookSheetXML) (map[string]string, map[string]map[string]string) {
	global := make(map[string]string)
	local := make(map[string]map[string]string)
	// The legacy workbook decoder accepts extension lookalikes by local tag
	// name. Derive scope ordinals from the same strict source nodes as F12,
	// retaining chart/macro sheets while excluding extension pseudo-sheets.
	scopes, err := catalogueSheetsXML(data)
	if err != nil {
		return global, local
	}
	if len(scopes) > 0 {
		original = scopes
	}
	catalogue, err := catalogueNamesXML(data, original)
	if err != nil || !catalogue.Complete {
		return global, local
	}
	groups := make(map[string][]CatalogueItem)
	for _, item := range catalogue.Items {
		key := catalogueScopeKey(item.Scope, item.Name)
		groups[key] = append(groups[key], item)
	}
	for _, group := range groups {
		if len(group) != 1 {
			continue
		}
		item := group[0]
		if item.Status == "invalid" || item.Status == "unavailable" || len(item.Definition) > 2048 {
			continue
		}
		key, value := strings.ToLower(item.Name), strings.TrimSpace(item.Definition)
		if item.Scope == "" {
			global[key] = value
			continue
		}
		sheet := strings.ToLower(item.Scope)
		if local[sheet] == nil {
			local[sheet] = make(map[string]string)
		}
		local[sheet][key] = value
	}
	return global, local
}
