package workbook

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

var xlsbBorders = [...]string{"", "thin", "medium", "dashed", "dotted", "thick", "double", "hair", "mediumDashed", "dashDot", "mediumDashDot", "dashDotDot", "mediumDashDotDot", "slantDashDot"}
var xlsbHorizontal = [...]string{"general", "left", "center", "right", "fill", "justify", "centerContinuous", "distributed"}
var xlsbVertical = [...]string{"top", "center", "bottom", "justify", "distributed"}

func xlsbColor(f *xlsbFields) *styleColorXML {
	kind, index := f.u8()>>1, int(f.u8())
	tint := int16(f.u16())
	r, g, b := f.u8(), f.u8(), f.u8()
	f.u8() // alpha; the grid uses the same RGB semantics as its OOXML styles
	c := &styleColorXML{Tint: float64(tint) / 32767}
	if tint < 0 {
		c.Tint = float64(tint) / 32768
	}
	switch kind {
	case 0:
		c.Auto = "1"
	case 1:
		c.Indexed = &index
	case 2:
		c.RGB = fmt.Sprintf("%02X%02X%02X", r, g, b)
	case 3:
		c.Theme = &index
	default:
		if f.err == nil {
			f.err = errors.New("BIFF12 颜色类型无效")
		}
	}
	return c
}

func xlsbFont(f *xlsbFields) styleFontXML {
	size, bits, weight, script := f.u16(), f.u16(), f.u16(), f.u16()
	underline := f.u8()
	f.take(3) // family, character set, reserved
	color := xlsbColor(f)
	f.u8() // font scheme, with the source's resolved fallback name below
	name := f.wide(false, 255)
	out := styleFontXML{
		Name: valXML{Val: name}, Size: valXML{Val: strconv.FormatFloat(float64(size)/20, 'f', -1, 64)},
		Color: color,
	}
	if weight >= 700 {
		out.Bold = &flagXML{}
	}
	if bits&2 != 0 {
		out.Italic = &flagXML{}
	}
	if bits&8 != 0 {
		out.Strike = &flagXML{}
	}
	if label := xlsbUnderline(underline); label != "none" {
		out.Underline = &flagXML{Val: label}
	}
	if script == 1 {
		out.Vertical.Val = "superscript"
	}
	if script == 2 {
		out.Vertical.Val = "subscript"
	}
	return out
}

func xlsbUnderline(value byte) string {
	switch value {
	case 1:
		return "single"
	case 2:
		return "double"
	case 33:
		return "singleAccounting"
	case 34:
		return "doubleAccounting"
	default:
		return "none"
	}
}

func xlsbBorderSide(f *xlsbFields) styleSideXML {
	kind := int(f.u8())
	f.u8()
	color := xlsbColor(f)
	if kind >= len(xlsbBorders) {
		if f.err == nil {
			f.err = errors.New("BIFF12 边框种类无效")
		}
		return styleSideXML{}
	}
	return styleSideXML{Style: xlsbBorders[kind], Color: color}
}

func xlsbStyleXF(f *xlsbFields, cell bool) (styleXFXML, bool) {
	xf := styleXFXML{
		XFID: int(f.u16()), NumFmtID: int(f.u16()), FontID: int(f.u16()), FillID: int(f.u16()), BorderID: int(f.u16()),
	}
	rot := f.u8()
	indent := f.u8()
	flags := f.u32()
	xf.Alignment.Rotation = int(rot)
	xf.Alignment.Horizontal = xlsbHorizontal[flags&7]
	if v := (flags >> 3) & 7; v < uint32(len(xlsbVertical)) {
		xf.Alignment.Vertical = xlsbVertical[v]
	}
	if flags&(1<<6) != 0 {
		xf.Alignment.WrapText = "1"
	}
	if cell {
		apply := func(bit uint32) string {
			if flags&(1<<(16+bit)) == 0 {
				return "0"
			}
			return "1"
		}
		xf.ApplyNumberFormat, xf.ApplyFont, xf.ApplyAlignment, xf.ApplyBorder, xf.ApplyFill = apply(0), apply(1), apply(2), apply(3), apply(4)
	}
	return xf, indent != 0 || rot == 254 || flags&(1<<7|1<<8|3<<10) != 0
}

func (w *Workbook) readXLSBStyles(file *zip.File) error {
	var sx stylesXML
	var diffs, extendedDiffs [][]byte
	var zone uint16
	var seen, ended bool
	var cellCount, parentCount int
	var layeredFills, decoratedBorders []bool
	var advancedAlignment bool
	err := readXLSBPart(file, maxMetadataPart, func(kind uint16, data []byte) error {
		f := xlsbFields{data: data}
		switch kind {
		case brtBeginStyleSheet:
			seen = true
		case brtEndStyleSheet:
			ended = true
		case brtBeginCellXFs:
			zone, cellCount = kind, int(f.u32())
		case brtBeginCellStyleXFs:
			zone, parentCount = kind, int(f.u32())
		case brtEndCellXFs, brtEndCellStyleXFs:
			zone = 0
		case brtFmt:
			id, code := int(f.u16()), f.wide(false, 255)
			sx.NumFmts.Items = append(sx.NumFmts.Items, struct {
				ID   int    `xml:"numFmtId,attr"`
				Code string `xml:"formatCode,attr"`
			}{id, code})
		case brtFont:
			sx.Fonts = append(sx.Fonts, xlsbFont(&f))
		case brtFill:
			var fill styleFillXML
			pattern := f.u32()
			if pattern == 1 {
				fill.Pattern.Type = "solid"
			}
			layeredFills = append(layeredFills, pattern > 1)
			fill.Pattern.Foreground, fill.Pattern.Background = xlsbColor(&f), xlsbColor(&f)
			sx.Fills = append(sx.Fills, fill)
		case brtBorder:
			diag := f.u8()
			top, bottom, left, right := xlsbBorderSide(&f), xlsbBorderSide(&f), xlsbBorderSide(&f), xlsbBorderSide(&f)
			xlsbBorderSide(&f)
			decoratedBorders = append(decoratedBorders, diag&3 != 0)
			sx.Borders = append(sx.Borders, styleBorderXML{Left: left, Right: right, Top: top, Bottom: bottom})
		case brtXF:
			switch zone {
			case brtBeginCellXFs:
				if len(sx.CellXfs) >= formats.XLSXStyleRecords {
					return errors.New("XLSB 单元格样式数量超限")
				}
				xf, advanced := xlsbStyleXF(&f, true)
				advancedAlignment = advancedAlignment || advanced
				sx.CellXfs = append(sx.CellXfs, xf)
			case brtBeginCellStyleXFs:
				xf, advanced := xlsbStyleXF(&f, false)
				advancedAlignment = advancedAlignment || advanced
				sx.CellStyleXfs = append(sx.CellStyleXfs, xf)
			}
		case brtStyle:
			ixfe, flags, builtin := int(f.u32()), f.u16(), int(f.u8())
			if flags&1 != 0 {
				sx.CellStyles = append(sx.CellStyles, struct {
					BuiltinID *int `xml:"builtinId,attr"`
					XfID      int  `xml:"xfId,attr"`
				}{&builtin, ixfe})
			}
		case brtIndexedColor:
			red, green, blue := f.u8(), f.u8(), f.u8()
			sx.IndexedColors = append(sx.IndexedColors, struct {
				RGB string `xml:"rgb,attr"`
			}{fmt.Sprintf("%02X%02X%02X", red, green, blue)})
		case brtDXF:
			diffs = append(diffs, append([]byte(nil), data...))
		case brtDXF14:
			f.take(4) // FRTBlank preceding XFProps
			extendedDiffs = append(extendedDiffs, append([]byte(nil), f.data...))
		}
		return f.err
	})
	if err != nil {
		return err
	}
	if !seen || !ended || len(sx.CellXfs) != cellCount || len(sx.CellStyleXfs) != parentCount {
		return errors.New("XLSB 样式表结构不完整")
	}
	w.source.xlsb.fonts = sx.Fonts
	if err := w.applyStyleTable(sx); err != nil {
		return err
	}
	w.source.xlsb.baseDXFCount = len(diffs)
	w.differentialStyles = make([]DifferentialStyle, 0, len(diffs)+len(extendedDiffs))
	for _, raw := range append(diffs, extendedDiffs...) {
		style, err := w.xlsbDXF(raw)
		if err != nil {
			return err
		}
		w.differentialStyles = append(w.differentialStyles, style)
	}
	var layeredFill, decoratedBorder bool
	for _, xf := range sx.CellXfs {
		style := inheritSourceXF(xf, sx.CellStyleXfs)
		layeredFill = layeredFill || style.FillID < len(layeredFills) && layeredFills[style.FillID]
		decoratedBorder = decoratedBorder || style.BorderID < len(decoratedBorders) && decoratedBorders[style.BorderID]
	}
	var omitted []string
	if layeredFill {
		omitted = append(omitted, "图案或渐变填充")
	}
	if decoratedBorder {
		omitted = append(omitted, "对角线边框")
	}
	if advancedAlignment {
		omitted = append(omitted, "缩进、文字方向或缩小字体等对齐细节")
	}
	if len(omitted) != 0 {
		if w.SourceInfo.PresentationNotice != "" {
			w.SourceInfo.PresentationNotice += "；"
		}
		w.SourceInfo.PresentationNotice += "网格未呈现部分" + strings.Join(omitted, "、")
	}
	return nil
}

func (w *Workbook) xlsbDXF(data []byte) (DifferentialStyle, error) {
	var out DifferentialStyle
	f := xlsbFields{data: data}
	f.take(4)
	count := int(f.u16())
	fmtID, fmtCode := -1, ""
	for range count {
		prop, size := f.u16(), int(f.u16())
		p := xlsbFields{data: f.take(size - 4)}
		switch prop {
		case 0: // fill pattern
			if p.u8() == 0 {
				empty := ""
				out.Background = &empty
			}
		case 1, 2: // foreground/background colors
			value := w.resolveColor(xlsbColor(&p), w.IndexedColors)
			if value != "" && (out.Background == nil || *out.Background == "" || prop == 1) {
				out.Background = &value
			}
		case 5:
			color := xlsbColor(&p)
			out.Font.Color = w.resolveColor(color, w.IndexedColors)
			out.Font.AutoColor = color.Auto == "1"
		case 6, 7, 8, 9: // border colors and style
			color := xlsbColor(&p)
			line := int(p.u16())
			if line >= len(xlsbBorders) {
				out.Notice = "条件样式包含未知的 BIFF12 边框"
				continue
			}
			if out.Border == nil {
				out.Border = &DifferentialBorders{}
			}
			c, s := w.resolveColor(color, w.IndexedColors), xlsbBorders[line]
			side := &DifferentialBorderSide{Style: &s, Color: &c, AutoColor: color.Auto == "1"}
			switch prop {
			case 6:
				out.Border.Top = side
			case 7:
				out.Border.Bottom = side
			case 8:
				out.Border.Left = side
			case 9:
				out.Border.Right = side
			}
		case 0x0F:
			if a := int(p.u8()); a < len(xlsbHorizontal) {
				value := xlsbHorizontal[a]
				out.Horizontal = &value
			}
		case 0x10:
			if a := int(p.u8()); a < len(xlsbVertical) {
				value := xlsbVertical[a]
				out.Vertical = &value
			}
		case 0x11:
			v := int(p.u8())
			out.Rotation = &v
		case 0x14:
			v := p.u8() != 0
			out.Wrap = &v
		case 0x18:
			out.Font.Name = p.utf16(uint32(p.u16()), 255)
		case 0x19:
			v := p.u16() >= 700
			out.Font.Bold = &v
		case 0x1A:
			out.Font.Underline = xlsbUnderline(p.u8())
		case 0x1B:
			if script := p.u16(); script <= 2 {
				out.Font.Vertical = [...]string{"baseline", "superscript", "subscript"}[script]
			}
		case 0x1C:
			v := p.u8() != 0
			out.Font.Italic = &v
		case 0x1D:
			v := p.u8() != 0
			out.Font.Strike = &v
		case 0x24:
			out.Font.Size = float64(p.u32()) / 20
		case 0x26:
			fmtCode = p.utf16(uint32(p.u16()), 255)
		case 0x29:
			fmtID = int(p.u16())
		default:
			out.Notice = "条件样式包含当前网格无法呈现的 BIFF12 属性"
		}
		if p.err != nil && f.err == nil {
			f.err = p.err
		}
	}
	if fmtID >= 0 || fmtCode != "" {
		out.NumberFormat = &DifferentialNumberFormat{ID: fmtID, Code: fmtCode}
	}
	return out, f.err
}

func (w *Workbook) xlsbTabColor(part *zip.File) (string, error) {
	input, err := part.Open()
	if err != nil {
		return "", err
	}
	defer input.Close()
	r := newXLSBRecords(input)
	for r.offset < maxMetadataPart {
		kind, data, err := r.next()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
		switch kind {
		case brtWsProp:
			f := xlsbFields{data: data}
			f.take(3)
			value := w.resolveColor(xlsbColor(&f), w.IndexedColors)
			return value, f.err
		case brtWsDim, brtBeginSheetData, brtEndSheet:
			return "", nil
		}
	}
	return "", errors.New("XLSB 工作表首部超过读取容量")
}
