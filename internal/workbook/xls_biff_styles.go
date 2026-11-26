package workbook

import (
	"errors"
	"fmt"
	"io"

	"excellens/internal/fileidentity"
)

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
	fp, st, e := fileidentity.Fingerprint(filePath, "xls")
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
