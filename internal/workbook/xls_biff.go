package workbook

// Read-only BIFF8 (Excel 97-2003) parser. XLS is never executed or converted
// through Office. Formula cells use their stored results; formula tokens are
// decoded only where unambiguous. Unsupported tokens remain explicitly marked.
import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"excellens/internal/formats"
)

type xlsSheet struct {
	name   string
	offset int64
	state  string
}
type xlsGlobals struct {
	sheets    []xlsSheet
	sst       []string
	styles    []StyleInfo
	visual    []VisualStyle
	formats   map[int]string
	date1904  bool
	catalogue Catalogue
}

type biffReader struct {
	r        *bufio.Reader
	pos, end int64
	scratch  []byte
}

func newBIFFReader(stream io.ReaderAt, offset, end int64) *biffReader {
	return &biffReader{r: bufio.NewReaderSize(io.NewSectionReader(stream, offset, end-offset), 256<<10), pos: offset, end: end}
}
func (r *biffReader) next() (uint16, []byte, error) {
	if r.pos >= r.end {
		return 0, nil, io.EOF
	}
	var hdr [4]byte
	if _, e := io.ReadFull(r.r, hdr[:]); e != nil {
		return 0, nil, e
	}
	size := int(binary.LittleEndian.Uint16(hdr[2:4]))
	r.pos += 4
	if size > 8224 || r.pos+int64(size) > r.end {
		return 0, nil, errors.New("BIFF 记录长度无效或已截断")
	}
	if cap(r.scratch) < size {
		r.scratch = make([]byte, size)
	}
	data := r.scratch[:size]
	if _, e := io.ReadFull(r.r, data); e != nil {
		return 0, nil, e
	}
	r.pos += int64(size)
	return binary.LittleEndian.Uint16(hdr[0:2]), data, nil
}
func xlsUint16(b []byte, i int) uint16 {
	if i < 0 || i+2 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint16(b[i : i+2])
}
func xlsUint32(b []byte, i int) uint32 {
	if i < 0 || i+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[i : i+4])
}
func xlsFloat64(b []byte, i int) float64 {
	if i < 0 || i+8 > len(b) {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b[i : i+8]))
}

func xlsInlineUnicode(b []byte, short bool) (string, int, error) {
	prefix := 3
	if short {
		prefix = 2
	}
	if len(b) < prefix {
		return "", 0, errors.New("BIFF Unicode 字符串头截断")
	}
	chars := int(xlsUint16(b, 0))
	flags := b[2]
	if short {
		chars = int(b[0])
		flags = b[1]
	}
	ptr := prefix
	rich, ext := 0, 0
	if flags&8 != 0 {
		if ptr+2 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		rich = int(xlsUint16(b, ptr))
		ptr += 2
	}
	if flags&4 != 0 {
		if ptr+4 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		ext = int(xlsUint32(b, ptr))
		ptr += 4
	}
	bytesPer := 1
	if flags&1 != 0 {
		bytesPer = 2
	}
	if chars < 0 || chars > 65535 || chars*bytesPer > len(b)-ptr {
		return "", 0, errors.New("BIFF Unicode 字符串长度无效")
	}
	result := decodeBIFFChars(b[ptr:ptr+chars*bytesPer], bytesPer == 2)
	ptr += chars * bytesPer
	if rich > 4096 || ext > 16<<20 || rich*4+ext > len(b)-ptr {
		return "", 0, errors.New("BIFF 字符串富文本数据越界")
	}
	ptr += rich*4 + ext
	return result, ptr, nil
}
func decodeBIFFChars(b []byte, u16 bool) string {
	if !u16 {
		var out strings.Builder
		out.Grow(len(b))
		for _, v := range b {
			out.WriteRune(rune(v))
		}
		return out.String()
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(units))
}

type xlsSegments struct {
	seg  [][]byte
	s, i int
}

func (c *xlsSegments) next(n int, continueFlag bool) ([]byte, error) {
	if n < 0 || n > 64<<20 {
		return nil, errors.New("BIFF SST 字段过大")
	}
	data := make([]byte, n)
	at := 0
	for at < n {
		for c.s < len(c.seg) && c.i >= len(c.seg[c.s]) {
			c.s++
			c.i = 0
		}
		if c.s >= len(c.seg) {
			return nil, io.ErrUnexpectedEOF
		}
		avail := len(c.seg[c.s]) - c.i
		m := min(n-at, avail)
		copy(data[at:at+m], c.seg[c.s][c.i:c.i+m])
		c.i += m
		at += m
	}
	_ = continueFlag
	return data, nil
}
func (c *xlsSegments) nextBytes(n int) ([]byte, error) { return c.next(n, false) }
func (c *xlsSegments) stringChars(chars int, u16 bool) (string, error) {
	if chars < 0 || chars > 65535 {
		return "", errors.New("SST 字符串长度无效")
	}
	// Decode UTF-16 only *after* collecting all character units: a valid
	// surrogate pair can straddle a BIFF CONTINUE boundary. Decoding each
	// individual record turns such characters into two replacement runes.
	units := make([]uint16, 0, chars)
	for len(units) < chars {
		for c.s < len(c.seg) && c.i >= len(c.seg[c.s]) {
			c.s++
			c.i = 0
			if c.s < len(c.seg) && len(c.seg[c.s]) > 0 {
				// This byte is present only when CONTINUE splits the
				// *characters* of an XLUnicodeRichExtendedString.
				u16 = c.seg[c.s][0]&1 != 0
				c.i = 1
			}
		}
		if c.s >= len(c.seg) {
			return "", io.ErrUnexpectedEOF
		}
		avail := len(c.seg[c.s]) - c.i
		if u16 {
			n := min(chars-len(units), avail/2)
			if n == 0 {
				return "", errors.New("SST UTF-16 字符被截断")
			}
			for j := 0; j < n; j++ {
				units = append(units, xlsUint16(c.seg[c.s], c.i+j*2))
			}
			c.i += n * 2
		} else {
			n := min(chars-len(units), avail)
			if n == 0 {
				continue
			}
			for _, ch := range c.seg[c.s][c.i : c.i+n] {
				units = append(units, uint16(ch))
			}
			c.i += n
		}
	}
	result := string(utf16.Decode(units))
	if len(result) > maxExternalCell {
		return "", errors.New("SST 单字符串超过 8MB")
	}
	return result, nil
}
func parseXLSSST(segments [][]byte) ([]string, error) {
	c := &xlsSegments{seg: segments}
	head, e := c.nextBytes(8)
	if e != nil {
		return nil, e
	}
	unique := xlsUint32(head, 4)
	if unique > 8_000_000 {
		return nil, errors.New("BIFF 共享字符串数量超过安全上限")
	}
	result := make([]string, 0, min(int(unique), 8192))
	for i := uint32(0); i < unique; i++ {
		b, e := c.nextBytes(3)
		if e != nil {
			return nil, fmt.Errorf("SST 字符串 %d: %w", i, e)
		}
		chars := int(xlsUint16(b, 0))
		flags := b[2]
		rich, ext := 0, 0
		if flags&8 != 0 {
			v, e := c.nextBytes(2)
			if e != nil {
				return nil, e
			}
			rich = int(xlsUint16(v, 0))
		}
		if flags&4 != 0 {
			v, e := c.nextBytes(4)
			if e != nil {
				return nil, e
			}
			ext = int(xlsUint32(v, 0))
		}
		text, e := c.stringChars(chars, flags&1 != 0)
		if e != nil {
			return nil, fmt.Errorf("SST 字符串 %d: %w", i, e)
		}
		if rich > 4096 || ext > 16<<20 {
			return nil, errors.New("SST 富文本字段过长")
		}
		if _, e = c.nextBytes(rich*4 + ext); e != nil {
			return nil, e
		}
		result = append(result, text)
	}
	return result, nil
}

func readXLSGlobals(stream io.ReaderAt, size int64) (*xlsGlobals, error) {
	b := newBIFFReader(stream, 0, size)
	g := &xlsGlobals{formats: make(map[int]string), styles: []StyleInfo{}}
	catalogue := new(biffCatalogueCapture)
	var ss [][]byte
	inSST := false
	var fonts []VisualStyle
	var xfRecords [][]byte
	var fontColors []uint16
	customPalette := map[uint16]string{}
	var country uint16
	for {
		id, data, e := b.next()
		if e == io.EOF {
			return nil, errors.New("BIFF 缺少全局 EOF 记录")
		}
		if e != nil {
			return nil, e
		}
		if id == 0x000A {
			break
		}
		catalogue.capture(id, data)
		if inSST && id != 0x003C {
			g.sst, e = parseXLSSST(ss)
			if e != nil {
				return nil, fmt.Errorf("BIFF SST 解析失败：%w", e)
			}
			inSST = false
			ss = nil
		}
		switch id {
		case 0x008C: // COUNTRY: current locale, used by Excel's localized date format 14.
			if len(data) >= 4 {
				country = xlsUint16(data, 2)
			}
		case 0x0809:
			if len(data) < 4 || xlsUint16(data, 0) != 0x0600 || xlsUint16(data, 2) != 0x0005 {
				return nil, errors.New("只支持 Excel 97-2003 BIFF8 格式的真正 .xls 文件")
			}
		case 0x002F: // FILEPASS: all supported BIFF8 encryption needs decryption.
			return nil, errors.New("XLS 文件已加密（FILEPASS），请使用原密码解密后再打开；不会尝试读取错误数据")
		case 0x0085:
			if len(data) < 8 {
				return nil, errors.New("BOUNDSHEET 数据截断")
			}
			if data[5] != 0 { // charts, macro sheets and VBA modules are not cell grids.
				continue
			}
			name, _, e := xlsInlineUnicode(data[6:], true)
			if e != nil {
				return nil, e
			}
			state := ""
			if data[4]&3 == 1 {
				state = "hidden"
			} else if data[4]&3 == 2 {
				state = "veryHidden"
			}
			g.sheets = append(g.sheets, xlsSheet{name: name, offset: int64(xlsUint32(data, 0)), state: state})
		case 0x00FC:
			inSST = true
			ss = [][]byte{append([]byte(nil), data...)}
		case 0x003C:
			if inSST {
				ss = append(ss, append([]byte(nil), data...))
			}
		case 0x0022:
			g.date1904 = len(data) >= 2 && xlsUint16(data, 0) == 1
		case 0x041E:
			if len(data) >= 5 {
				s, _, e := xlsInlineUnicode(data[2:], false)
				if e == nil {
					g.formats[int(xlsUint16(data, 0))] = s
				}
			}
		case 0x0092: // PALETTE indexed colors, 8..63.
			if len(data) >= 2 {
				count := int(xlsUint16(data, 0))
				if count <= 56 && len(data) >= 2+4*count {
					for i := 0; i < count; i++ {
						at := 2 + 4*i
						customPalette[uint16(i+8)] = fmt.Sprintf("#%02X%02X%02X", data[at], data[at+1], data[at+2])
					}
				}
			}
		case 0x00E0: // XF, consume after the entire globals stream (PALETTE may follow it).
			if len(data) >= 20 {
				xfRecords = append(xfRecords, append([]byte(nil), data[:20]...))
				g.styles = append(g.styles, StyleInfo{NumFmtID: int(xlsUint16(data, 2))})
			}
		case 0x0031:
			var v VisualStyle
			if len(data) >= 16 {
				v.FontSize = float64(xlsUint16(data, 0)) / 20
				v.Bold = xlsUint16(data, 6) >= 600
				v.Italic = xlsUint16(data, 2)&2 != 0
				v.Underline = data[10] != 0
				v.Strike = xlsUint16(data, 2)&8 != 0
				if len(data) >= 16+int(data[14]) {
					name, _, e := xlsInlineUnicode(data[14:], true)
					if e == nil {
						v.FontName = name
					}
				}
			}
			fonts = append(fonts, v)
			fontColors = append(fontColors, xlsUint16(data, 4))
		}
	}
	if inSST {
		var e error
		g.sst, e = parseXLSSST(ss)
		if e != nil {
			return nil, e
		}
	}
	if len(g.sheets) == 0 {
		return nil, errors.New("BIFF 文件没有工作表")
	}
	if len(g.sheets) > 256 {
		return nil, errors.New("XLS 工作表数量异常")
	}
	for _, s := range g.sheets {
		if s.offset < 0 || s.offset >= size {
			return nil, fmt.Errorf("工作表 %q 的 BIFF 偏移量无效", s.name)
		}
	}
	for _, xf := range xfRecords {
		v := VisualStyle{}
		font := int(xlsUint16(xf, 0))
		if font >= 4 {
			font--
		} // BIFF FONT record number 4 is reserved.
		if font >= 0 && font < len(fonts) {
			v = fonts[font]
			v.Color = biffPaletteColor(fontColors[font], customPalette)
		}
		alignment := int(xf[6] & 7)
		h := []string{"general", "left", "center", "right", "fill", "justify", "centerContinuous", "distributed"}
		if alignment < len(h) {
			v.Horizontal = h[alignment]
		}
		vert := []string{"top", "center", "bottom", "justify", "distributed"}
		vi := int((xf[6] >> 4) & 7)
		if vi < len(vert) {
			v.Vertical = vert[vi]
		}
		v.Wrap = xf[6]&8 != 0
		v.Rotation = int(xf[7])
		border1 := xlsUint32(xf, 10)
		border2 := xlsUint32(xf, 14)
		v.BorderLeft = biffBorder(int(border1&15), uint16((border1>>16)&0x7f), customPalette)
		v.BorderRight = biffBorder(int((border1>>4)&15), uint16((border1>>23)&0x7f), customPalette)
		v.BorderTop = biffBorder(int((border1>>8)&15), uint16(border2&0x7f), customPalette)
		v.BorderBottom = biffBorder(int((border1>>12)&15), uint16((border2>>7)&0x7f), customPalette)
		pattern := int((border2 >> 26) & 63)
		if pattern != 0 {
			v.Background = biffPaletteColor(uint16(xlsUint16(xf, 18)&0x7f), customPalette)
		}
		v.NumFmtID = int(xlsUint16(xf, 2))
		g.visual = append(g.visual, v)
	}
	if len(g.styles) == 0 {
		g.styles = []StyleInfo{{NumFmtID: 0}}
		g.visual = []VisualStyle{{}}
	}
	if country == 86 && g.formats[14] == "" {
		g.formats[14] = "yyyy/m/d"
	}
	for i := range g.styles {
		style := &g.styles[i]
		style.SectionColors = sectionTextColors(style.NumFmtID, g.formats[style.NumFmtID])
	}
	g.catalogue = catalogue.finish()
	return g, nil
}

// Complete Excel 97-2003 default palette (indices 8..63). XLS files may
// omit PALETTE entirely; silently losing indexes 24..63 makes corporate
// financial report highlights, borders and warning fills display incorrectly.
var biffDefaultPalette = [...]string{
	"#000000", "#FFFFFF", "#FF0000", "#00FF00", "#0000FF", "#FFFF00", "#FF00FF", "#00FFFF",
	"#800000", "#008000", "#000080", "#808000", "#800080", "#008080", "#C0C0C0", "#808080",
	"#9999FF", "#993366", "#FFFFCC", "#CCFFFF", "#660066", "#FF8080", "#0066CC", "#CCCCFF",
	"#000080", "#FF00FF", "#FFFF00", "#00FFFF", "#800080", "#800000", "#008080", "#0000FF",
	"#00CCFF", "#CCFFFF", "#CCFFCC", "#FFFF99", "#99CCFF", "#FF99CC", "#CC99FF", "#FFCC99",
	"#3366FF", "#33CCCC", "#99CC00", "#FFCC00", "#FF9900", "#FF6600", "#666699", "#969696",
	"#003366", "#339966", "#003300", "#333300", "#993300", "#993366", "#333399", "#333333",
}

func biffPaletteColor(id uint16, custom map[uint16]string) string {
	if v := custom[id]; v != "" {
		return v
	}
	if id < 8 { // System palette colors are duplicated at 8..15.
		return biffDefaultPalette[id]
	}
	if id >= 8 && id < 64 {
		return biffDefaultPalette[id-8]
	}
	return ""
}
func biffBorder(style int, color uint16, custom map[uint16]string) string {
	if style <= 0 {
		return ""
	}
	width, kind := "1px", "solid"
	switch style {
	case 2:
		width = "2px"
	case 3, 9:
		kind = "dashed"
	case 4, 7:
		kind = "dotted"
	case 5:
		width = "3px"
	case 6:
		kind = "double"
		width = "3px"
	case 8:
		width = "0.5px"
	case 10, 11, 12, 13:
		kind = "dashed"
		width = "2px"
	}
	c := biffPaletteColor(color, custom)
	if c == "" {
		c = "#404040"
	}
	return width + " " + kind + " " + c
}

func openBIFFWorkbook(filePath string) (*Workbook, error) {
	// This path only accepts genuine CFB/BIFF bytes. Office HTML exports are
	// detected separately before entering the binary reader.
	cf, e := openCFB(filePath)
	if e != nil {
		return nil, e
	}
	globals, e := readXLSGlobals(cf.workbook, cf.workbook.size)
	_ = cf.Close()
	if e != nil {
		return nil, e
	}
	fp, st, e := fingerprintFile(filePath, "xls")
	if e != nil {
		return nil, e
	}
	wb, e := newExternalWorkbook(filePath, "xls", fp, st)
	if e != nil {
		return nil, e
	}
	wb.source.xlsSheets = globals.sheets
	wb.source.xlsCatalogue = &globals.catalogue
	wb.Shared = globals.sst
	wb.Styles = globals.styles
	wb.VisualStyles = globals.visual
	if len(globals.visual) > 0 {
		wb.normalFont = globals.visual[0]
	}
	wb.NumFmts = globals.formats
	wb.Date1904 = globals.date1904
	wb.SourceInfo = SourceInfo{Format: "xls", ReadOnly: true, Notice: "Excel 97–2003 BIFF8 二进制工作簿；读取保存的公式结果，不执行公式或宏。"}
	for i, s := range globals.sheets {
		wb.Sheets = append(wb.Sheets, SheetMeta{Name: s.name, Path: fmt.Sprintf("external/xls/%d", i), State: s.state})
	}
	return wb, nil
}

// BIFF8 ColInfo records live before the cell table, so source visibility can
// be read without building or scanning the entire sheet. Saving a local
// "show hidden columns" override never edits the source XLS file.
func (w *Workbook) xlsHiddenColumns(meta SheetMeta) ([]HiddenColumnRange, error) {
	if err := w.checkExternalUnchanged(); err != nil {
		return nil, err
	}
	var sheet *xlsSheet
	for i := range w.source.xlsSheets {
		if meta.Path == fmt.Sprintf("external/xls/%d", i) {
			sheet = &w.source.xlsSheets[i]
			break
		}
	}
	if sheet == nil {
		return nil, errors.New("XLS 工作表不存在")
	}
	cf, err := openCFB(w.Path)
	if err != nil {
		return nil, err
	}
	defer cf.Close()
	b := newBIFFReader(cf.workbook, sheet.offset, cf.workbook.size)
	hidden := make([]bool, 257)
	for {
		id, data, err := b.next()
		if err != nil {
			return nil, err
		}
		switch id {
		case 0x000A, 0x0208, 0x00FD, 0x0203, 0x027E, 0x00BD, 0x0201, 0x00BE, 0x0205, 0x0006:
			return compressHiddenColumns(hidden), nil
		case 0x007D: // COLINFO: zero-based inclusive range, hidden in flags bit 0.
			if len(data) < 12 {
				return nil, errors.New("XLS COLINFO 记录截断")
			}
			first, last := int(xlsUint16(data, 0))+1, int(xlsUint16(data, 2))+1
			if first < 1 || last > 256 || first > last {
				return nil, errors.New("XLS COLINFO 列范围无效")
			}
			isHidden := xlsUint16(data, 8)&1 != 0
			for i := first; i <= last; i++ {
				hidden[i] = isHidden
			}
		}
	}
}

func xlsRK(v uint32) float64 {
	var f float64
	if v&2 != 0 {
		f = float64(int32(v) >> 2)
	} else {
		f = math.Float64frombits(uint64(v&0xFFFFFFFC) << 32)
	}
	if v&1 != 0 {
		f /= 100
	}
	return f
}
func xlsNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "#NUM!"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
func xlsError(code byte) string {
	switch code {
	case 0:
		return "#NULL!"
	case 7:
		return "#DIV/0!"
	case 15:
		return "#VALUE!"
	case 23:
		return "#REF!"
	case 29:
		return "#NAME?"
	case 36:
		return "#NUM!"
	case 42:
		return "#N/A"
	case 43:
		return "#GETTING_DATA"
	default:
		return fmt.Sprintf("#ERROR(%d)", code)
	}
}
func (w *Workbook) streamXLSRows(meta SheetMeta, writer *externalRowWriter, cancel <-chan struct{}, progress func(int64, int64, string), catalogue *biffTableCapture) error {
	sheet, e := w.openBIFFWorksheet(meta)
	if e != nil {
		return e
	}
	defer sheet.cf.Close()
	sheet.cancel = cancel
	current := 0
	cells := make([]externalCell, 0, 64)
	maxCol := 0
	pendingString := false
	flush := func(target int) error {
		for current < target {
			if err := writer.cells(cells, maxCol); err != nil {
				return err
			}
			current++
			cells = cells[:0]
			maxCol = 0
		}
		return nil
	}
	add := func(r, col, xf int, typ byte, raw, formula string, hasFormula, missing bool) error {
		if r >= 65536 || col >= 256 {
			return fmt.Errorf("BIFF8 单元格越界 R%dC%d", r+1, col+1)
		}
		if r < current {
			return fmt.Errorf("XLS 工作表包含乱序单元格：第 %d 行位于第 %d 行之后", r+1, current)
		}
		if r > current {
			if err := flush(r); err != nil {
				return err
			}
		}
		if col+1 > maxCol {
			maxCol = col + 1
		}
		if len(cells) > 0 && col+1 <= cells[len(cells)-1].Col {
			// A duplicate cell record is legal in a few producer variants.
			// Keeping the last record matches the saved worksheet value.
			i := sort.Search(len(cells), func(i int) bool { return cells[i].Col >= col+1 })
			if i < len(cells) && cells[i].Col == col+1 {
				cells[i] = externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing}
				return nil
			}
			cells = append(cells, externalCell{})
			copy(cells[i+1:], cells[i:])
			cells[i] = externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing}
			return nil
		}
		cells = append(cells, externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing})
		return nil
	}
	lastProgressRows := -1
	for {
		id, data, err := sheet.next()
		if err != nil {
			return err
		}
		if cancel != nil {
			select {
			case <-cancel:
				return errors.New("读取已取消")
			default:
			}
		}
		if catalogue != nil {
			catalogue.capture(id, data)
		}
		if id == 0x000A {
			if len(cells) > 0 {
				if err := flush(current + 1); err != nil {
					return err
				}
			}
			return nil
		}
		if progress != nil && writer.rows > 0 && writer.rows%4096 == 0 && writer.rows != lastProgressRows {
			lastProgressRows = writer.rows
			progress(sheet.reader.pos, sheet.reader.end, fmt.Sprintf("正在读取 XLS 第 %d 行", writer.rows))
		}
		if pendingString {
			if id == 0x0207 {
				if s, _, e := xlsInlineUnicode(data, false); e == nil && len(cells) > 0 {
					cells[len(cells)-1].Raw = s
					cells[len(cells)-1].Type = 's'
					cells[len(cells)-1].CacheMissing = false
				}
			}
			pendingString = false
		}
		if len(data) < 6 {
			continue
		}
		row := int(xlsUint16(data, 0))
		col := int(xlsUint16(data, 2))
		xf := int(xlsUint16(data, 4))
		switch id {
		case 0x00FD: // LABELSST
			if len(data) < 10 {
				return errors.New("LABELSST 记录截断")
			}
			index := int(xlsUint32(data, 6))
			if index < 0 || index >= len(w.Shared) {
				return errors.New("XLS 共享字符串索引无效")
			}
			if err := add(row, col, xf, 's', w.Shared[index], "", false, false); err != nil {
				return err
			}
		case 0x0203: // NUMBER
			if len(data) < 14 {
				return errors.New("NUMBER 记录截断")
			}
			if err := add(row, col, xf, 'n', xlsNumber(xlsFloat64(data, 6)), "", false, false); err != nil {
				return err
			}
		case 0x027E: // RK
			if len(data) < 10 {
				return errors.New("RK 记录截断")
			}
			if err := add(row, col, xf, 'n', xlsNumber(xlsRK(xlsUint32(data, 6))), "", false, false); err != nil {
				return err
			}
		case 0x00BD: // MULRK
			if len(data) < 12 || ((len(data)-6)%6) != 0 {
				return errors.New("MULRK 记录长度无效")
			}
			firstCol := int(xlsUint16(data, 2))
			lastCol := int(xlsUint16(data, len(data)-2))
			if lastCol-firstCol+1 != (len(data)-6)/6 {
				return errors.New("MULRK 列范围不匹配")
			}
			for i := firstCol; i <= lastCol; i++ {
				at := 4 + (i-firstCol)*6
				if err := add(row, i, int(xlsUint16(data, at)), 'n', xlsNumber(xlsRK(xlsUint32(data, at+2))), "", false, false); err != nil {
					return err
				}
			}
		case 0x0201: // BLANK, styles on empty cells carry borders/fills.
			if err := add(row, col, xf, 's', "", "", false, false); err != nil {
				return err
			}
		case 0x00BE: // MULBLANK, XF per blank cell.
			if len(data) < 8 || (len(data)-6)%2 != 0 {
				return errors.New("MULBLANK 长度无效")
			}
			first := int(xlsUint16(data, 2))
			last := int(xlsUint16(data, len(data)-2))
			if last-first+1 != (len(data)-6)/2 {
				return errors.New("MULBLANK 列范围不匹配")
			}
			for c := first; c <= last; c++ {
				if err := add(row, c, int(xlsUint16(data, 4+2*(c-first))), 's', "", "", false, false); err != nil {
					return err
				}
			}
		case 0x0205: // BOOLERR
			if len(data) < 8 {
				return errors.New("BOOLERR 截断")
			}
			typ, value := 'b', "0"
			if data[7] != 0 {
				typ = 'e'
				value = xlsError(data[6])
			} else if data[6] != 0 {
				value = "1"
			}
			if err := add(row, col, xf, byte(typ), value, "", false, false); err != nil {
				return err
			}
		case 0x0204, 0x00D6: // LABEL, RSTRING
			if len(data) < 9 {
				return errors.New("LABEL 截断")
			}
			s, _, e := xlsInlineUnicode(data[6:], false)
			if e != nil {
				return e
			}
			if err := add(row, col, xf, 's', s, "", false, false); err != nil {
				return err
			}
		case 0x0006: // FORMULA
			if len(data) < 22 {
				return errors.New("FORMULA 截断")
			}
			raw := ""
			typ := byte('n')
			missing := false
			cache := data[6:14]
			if cache[6] == 0xFF && cache[7] == 0xFF {
				switch cache[0] {
				case 0:
					missing = true
					typ = 's'
					pendingString = true
				case 1:
					typ = 'b'
					if cache[2] != 0 {
						raw = "1"
					} else {
						raw = "0"
					}
				case 2:
					typ = 'e'
					raw = xlsError(cache[2])
				default:
					missing = true
					typ = 's'
				}
			} else {
				raw = xlsNumber(xlsFloat64(cache, 0))
			}
			tokLen := int(xlsUint16(data, 20))
			if tokLen > len(data)-22 {
				return errors.New("FORMULA 令牌截断")
			}
			formula := decodeBIFFFormula(data[22:22+tokLen], row, col)
			if err := add(row, col, xf, typ, raw, formula, true, missing); err != nil {
				return err
			}
		}
	}
}

// The formula reader does NOT evaluate expressions. Valid tokens for common
// operators, references and functions are reconstructed from RPN. Unsupported
// or external references are explicitly labelled, never invented.
func decodeBIFFFormula(tokens []byte, row, col int) string {
	if len(tokens) == 0 {
		return "[BIFF 公式无令牌]"
	}
	stack := make([]string, 0, 12)
	work := 0
	take := func() (string, bool) {
		if len(stack) == 0 {
			return "", false
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, true
	}
	fail := func() string { return fmt.Sprintf("[BIFF公式令牌未支持: %X]", tokens) }
	binaryOps := map[byte]string{3: "+", 4: "-", 5: "*", 6: "/", 7: "^", 8: "&", 9: "<", 10: "<=", 11: "=", 12: ">=", 13: ">", 14: "<>", 15: " ", 16: ",", 17: ":"}
	for p := 0; p < len(tokens); {
		if len(stack) > 0 {
			work += len(stack[len(stack)-1])
			if len(stack[len(stack)-1]) > formats.BIFFFormulaBytes || work > formats.BIFFFormulaWork {
				return fail()
			}
		}
		kind := tokens[p]
		// Operand tokens have BIFF stack classes: R=0x20, V=0x40,
		// A=0x60. 0x44 and 0x64 must both decode as ptgRef(0x24),
		// not as unrelated tokens.
		if kind >= 0x60 {
			kind -= 0x40
		} else if kind >= 0x40 {
			kind -= 0x20
		}
		p++
		if op, ok := binaryOps[kind]; ok {
			right, ok := take()
			if !ok {
				return fail()
			}
			left, ok := take()
			if !ok {
				return fail()
			}
			stack = append(stack, "("+left+op+right+")")
			continue
		}
		switch kind {
		case 0x12, 0x13, 0x14, 0x15:
			a, ok := take()
			if !ok {
				return fail()
			}
			switch kind {
			case 0x12:
				a = "+" + a
			case 0x13:
				a = "-" + a
			case 0x14:
				a = a + "%"
			case 0x15:
				a = "(" + a + ")"
			}
			stack = append(stack, a)
		case 0x1E:
			if p+2 > len(tokens) {
				return fail()
			}
			stack = append(stack, strconv.Itoa(int(xlsUint16(tokens, p))))
			p += 2
		case 0x1F:
			if p+8 > len(tokens) {
				return fail()
			}
			value := xlsFloat64(tokens, p)
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fail()
			}
			stack = append(stack, xlsNumber(value))
			p += 8
		case 0x1D:
			if p >= len(tokens) {
				return fail()
			}
			if tokens[p] > 1 {
				return fail()
			}
			if tokens[p] != 0 {
				stack = append(stack, "TRUE")
			} else {
				stack = append(stack, "FALSE")
			}
			p++
		case 0x1C:
			if p >= len(tokens) {
				return fail()
			}
			stack = append(stack, xlsError(tokens[p]))
			p++
		case 0x17:
			if p+2 > len(tokens) {
				return fail()
			}
			n := int(tokens[p])
			p++
			flags := tokens[p]
			p++
			size := n
			if flags&1 != 0 {
				size *= 2
			}
			if p+size > len(tokens) {
				return fail()
			}
			text := ""
			if n > 0 {
				encoded := append([]byte{flags & 1}, tokens[p:p+size]...)
				var err error
				text, _, err = biffCatalogueString(encoded, n)
				if err != nil {
					return fail()
				}
			}
			stack = append(stack, "\""+strings.ReplaceAll(text, "\"", "\"\"")+"\"")
			p += size
		case 0x24, 0x2C: // PtgRef uses coordinates; PtgRefN uses relative offsets.
			if p+4 > len(tokens) {
				return fail()
			}
			ref, ok := biffFormulaCellRef(xlsUint16(tokens, p), xlsUint16(tokens, p+2), row, col, kind == 0x2C)
			p += 4
			if !ok {
				return fail()
			}
			stack = append(stack, ref)
		case 0x25, 0x2D: // PtgArea / PtgAreaN retain each corner's display anchors.
			if p+8 > len(tokens) {
				return fail()
			}
			left, lok := biffFormulaCellRef(xlsUint16(tokens, p), xlsUint16(tokens, p+4), row, col, kind == 0x2D)
			right, rok := biffFormulaCellRef(xlsUint16(tokens, p+2), xlsUint16(tokens, p+6), row, col, kind == 0x2D)
			p += 8
			if !lok || !rok {
				return fail()
			}
			stack = append(stack, left+":"+right)
		case 0x21, 0x22:
			argc := 0
			if kind == 0x22 {
				if p+3 > len(tokens) {
					return fail()
				}
				argc = int(tokens[p] & 0x7F)
				p++
			} else {
				if p+2 > len(tokens) {
					return fail()
				}
			}
			fn := xlsUint16(tokens, p)
			p += 2
			name, known := xlsFunctionNames[fn]
			if !known {
				return fail()
			}
			if kind == 0x21 {
				var fixed bool
				argc, fixed = xlsFixedArity[fn]
				if !fixed {
					return fail()
				}
			}
			if argc > len(stack) {
				return fail()
			}
			args := append([]string(nil), stack[len(stack)-argc:]...)
			stack = stack[:len(stack)-argc]
			stack = append(stack, name+"("+strings.Join(args, ",")+")")
		default:
			_ = row
			_ = col
			return fail()
		}
	}
	if len(stack) != 1 || len(stack[0]) > formats.BIFFFormulaBytes {
		return fail()
	}
	return "=" + stack[0]
}

// RgceLoc stores coordinates even when its flags request relative display.
// RgceLocRel (the N variants) stores signed offsets for relative components;
// MS-XLS specifies one source-domain adjustment at the row/column boundary.
func biffFormulaCellRef(rawRow, rawCol uint16, atRow, atCol int, offsets bool) (string, bool) {
	if offsets && (rawCol&0x8000 != 0 && (atRow < 0 || atRow >= formats.BIFFRows) || rawCol&0x4000 != 0 && (atCol < 0 || atCol >= formats.BIFFColumns)) {
		return "", false
	}
	row := int(rawRow)
	col := int(rawCol & 0x3FFF)
	if offsets && rawCol&0x8000 != 0 {
		row = atRow + int(int16(rawRow))
		if row < 0 {
			row += 65536
		} else if row >= 65536 {
			row -= 65536
		}
	}
	if offsets && rawCol&0x4000 != 0 {
		col = atCol + int(int16(rawCol<<2)>>2)
		if col < 0 {
			col += 256
		} else if col >= 256 {
			col -= 256
		}
	}
	if row < 0 || row >= 65536 || col < 0 || col >= 256 {
		return "", false
	}
	var b strings.Builder
	if rawCol&0x4000 == 0 {
		b.WriteByte('$')
	}
	b.WriteString(ColumnName(col + 1))
	if rawCol&0x8000 == 0 {
		b.WriteByte('$')
	}
	b.WriteString(strconv.Itoa(row + 1))
	return b.String(), true
}

var xlsFunctionNames = map[uint16]string{
	0: "COUNT", 1: "IF", 4: "SUM", 5: "AVERAGE", 6: "MIN", 7: "MAX", 8: "ROW", 9: "COLUMN",
	10: "NA", 15: "SIN", 16: "COS", 17: "TAN", 19: "PI", 20: "SQRT", 21: "EXP", 22: "LN", 23: "LOG10", 24: "ABS",
	25: "INT", 27: "ROUND", 29: "INDEX", 31: "MID", 32: "LEN", 34: "TRUE", 35: "FALSE", 36: "AND", 37: "OR", 38: "NOT",
	39: "MOD", 48: "TEXT", 63: "RAND", 64: "MATCH", 65: "DATE", 66: "TIME", 74: "NOW", 82: "SEARCH", 97: "ATAN2",
	100: "CHOOSE", 101: "HLOOKUP", 102: "VLOOKUP", 109: "LOG", 115: "LEFT", 116: "RIGHT", 117: "EXACT", 124: "FIND",
	125: "CELL", 148: "INDIRECT", 169: "COUNTA", 183: "PRODUCT", 197: "TRUNC", 198: "ISLOGICAL", 210: "ROUNDUP",
	211: "ROUNDDOWN", 228: "SUMPRODUCT", 279: "EVEN", 280: "ODD", 321: "RANDBETWEEN", 345: "SUMIF", 346: "COUNTIF", 357: "SUMIFS", 358: "COUNTIFS",
}
var xlsFixedArity = map[uint16]int{10: 0, 15: 1, 16: 1, 17: 1, 19: 0, 20: 1, 21: 1, 22: 1, 23: 1, 24: 1, 25: 1, 27: 2, 32: 1, 34: 0, 35: 0, 38: 1, 39: 2, 48: 2, 63: 0, 65: 3, 66: 3, 74: 0, 97: 2, 117: 2, 198: 1, 279: 1, 280: 1, 321: 2}
