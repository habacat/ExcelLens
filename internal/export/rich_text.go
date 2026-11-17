package export

import (
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

// Both writers split the same validated unchanged text, retaining UTF-16 run
// boundaries without allocating a rune-offset table for the whole cell.
func visitRichParts(text string, rich *workbook.RichText, visit func(string, workbook.RichFont) error) error {
	if err := workbook.ValidateRichText(text, rich); err != nil {
		return err
	}
	start, units, run := 0, 0, 0
	for offset, r := range text {
		if run < len(rich.Runs) && units == rich.Runs[run].End {
			if err := visit(text[start:offset], rich.Runs[run].Font); err != nil {
				return err
			}
			start, run = offset, run+1
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	font := workbook.RichFont{}
	if run < len(rich.Runs) {
		font = rich.Runs[run].Font
	}
	if start < len(text) {
		return visit(text[start:], font)
	}
	return nil
}

func validateRichSnapshot(cell workbook.CellData, text string) error {
	if cell.RichText.Notice != "" {
		return fmt.Errorf("无法完整导出富文本：%s", cell.RichText.Notice)
	}
	if text != cell.Raw || cell.HasFormula {
		return fmt.Errorf("显示值或公式与富文本原文不同，无法沿用原文字体片段")
	}
	return workbook.ValidateRichText(text, cell.RichText)
}

func writeRichXLSX(out io.Writer, text string, rich *workbook.RichText, overlays ...workbook.RichFont) error {
	return visitRichParts(text, rich, func(part string, font workbook.RichFont) error {
		if len(overlays) > 0 {
			font = overlayRichFont(font, overlays[0])
		}
		var b strings.Builder
		b.WriteString(`<r><rPr>`)
		if font.Name != "" {
			fmt.Fprintf(&b, `<rFont val="%s"/>`, xmlAttr(font.Name))
		}
		if font.Size > 0 {
			fmt.Fprintf(&b, `<sz val="%g"/>`, font.Size)
		}
		if font.Color != "" {
			fmt.Fprintf(&b, `<color rgb="FF%s"/>`, strings.ToUpper(font.Color[1:]))
		} else if font.AutoColor {
			b.WriteString(`<color auto="1"/>`)
		}
		for _, item := range []struct {
			name  string
			value *bool
		}{{"b", font.Bold}, {"i", font.Italic}, {"strike", font.Strike}} {
			if item.value != nil {
				value := "0"
				if *item.value {
					value = "1"
				}
				fmt.Fprintf(&b, `<%s val="%s"/>`, item.name, value)
			}
		}
		if font.Underline != "" {
			fmt.Fprintf(&b, `<u val="%s"/>`, font.Underline)
		}
		if font.Vertical != "" {
			fmt.Fprintf(&b, `<vertAlign val="%s"/>`, font.Vertical)
		}
		fmt.Fprintf(&b, `</rPr><t xml:space="preserve">%s</t></r>`, xmlText(part))
		_, err := io.WriteString(out, b.String())
		return err
	})
}

func richHTMLNotice(raw, text string, rich *workbook.RichText) string {
	if rich == nil {
		return ""
	}
	if text != raw {
		return "显示值与原文不同，富文本片段未应用"
	}
	if err := workbook.ValidateRichText(text, rich); err != nil {
		return "富文本样式无效，原文保留：" + err.Error()
	}
	for _, run := range rich.Runs {
		if strings.HasSuffix(run.Font.Underline, "Accounting") {
			if rich.Notice != "" {
				return rich.Notice + "；会计下划线按片段下划线显示"
			}
			return "会计下划线按片段下划线显示"
		}
	}
	return rich.Notice
}

// Escape CSS strings as CSS first, then escape the complete HTML attribute.
func cssFontName(name string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range name {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
			b.WriteRune(r)
		} else if r < 32 || strings.ContainsRune("<>&;", r) {
			fmt.Fprintf(&b, `\%x `, r)
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func htmlDecoration(underline, strike bool) string {
	var parts []string
	if underline {
		parts = append(parts, "underline")
	}
	if strike {
		parts = append(parts, "line-through")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func htmlCellStyle(v workbook.VisualStyle, valueColor string, rich bool) string {
	var b strings.Builder
	if bg := cssColor(v.Background); bg != "" {
		fmt.Fprintf(&b, "background:%s;", bg)
	}
	fg := cssColor(valueColor)
	if fg == "" {
		fg = cssColor(v.Color)
	}
	if fg != "" {
		fmt.Fprintf(&b, "color:%s;", fg)
	}
	if v.FontName != "" {
		fmt.Fprintf(&b, "font-family:%s;", cssFontName(v.FontName))
	}
	if v.FontSize > 0 && v.FontSize <= 409 {
		fmt.Fprintf(&b, "font-size:%gpt;", v.FontSize)
	}
	if v.Bold {
		b.WriteString("font-weight:bold;")
	}
	if v.Italic {
		b.WriteString("font-style:italic;")
	}
	if !rich && (v.Underline || v.Strike) {
		fmt.Fprintf(&b, "text-decoration-line:%s;", htmlDecoration(v.Underline, v.Strike))
	}
	switch v.Horizontal {
	case "left", "center", "right", "justify":
		fmt.Fprintf(&b, "text-align:%s;", v.Horizontal)
	}
	if v.Vertical == "top" || v.Vertical == "bottom" {
		fmt.Fprintf(&b, "vertical-align:%s;", v.Vertical)
	} else if v.Vertical == "center" {
		b.WriteString("vertical-align:middle;")
	}
	if v.Wrap {
		b.WriteString("white-space:pre-wrap;")
	}
	for _, side := range []struct{ name, value string }{{"top", v.BorderTop}, {"bottom", v.BorderBottom}, {"left", v.BorderLeft}, {"right", v.BorderRight}} {
		parts := strings.Fields(side.value)
		if len(parts) != 3 || !strings.HasSuffix(parts[0], "px") {
			continue
		}
		width, err := strconv.ParseFloat(strings.TrimSuffix(parts[0], "px"), 64)
		if err == nil && width > 0 && width <= 4 && (parts[1] == "solid" || parts[1] == "double" || parts[1] == "dashed" || parts[1] == "dotted") && cssColor(parts[2]) != "" {
			fmt.Fprintf(&b, "border-%s:%s;", side.name, side.value)
		}
	}
	return b.String()
}

func writeRichHTML(out io.Writer, text string, rich *workbook.RichText, base workbook.VisualStyle, overlays ...workbook.RichFont) error {
	return visitRichParts(text, rich, func(part string, font workbook.RichFont) error {
		if len(overlays) > 0 {
			font = overlayRichFont(font, overlays[0])
		}
		if font.Vertical == "" {
			font.Vertical = base.FontVertical
		}
		var b strings.Builder
		if font.Name != "" {
			fmt.Fprintf(&b, "font-family:%s;", cssFontName(font.Name))
		}
		if font.Color != "" {
			fmt.Fprintf(&b, "color:%s;", font.Color)
		} else if font.AutoColor {
			b.WriteString("color:#000000;")
		}
		for _, flag := range []struct {
			name, yes, no string
			value         *bool
		}{{"font-weight", "700", "400", font.Bold}, {"font-style", "italic", "normal", font.Italic}} {
			if flag.value != nil {
				value := flag.no
				if *flag.value {
					value = flag.yes
				}
				fmt.Fprintf(&b, "%s:%s;", flag.name, value)
			}
		}
		u := base.Underline
		if font.Underline != "" {
			u = font.Underline != "none"
		}
		strike := base.Strike
		if font.Strike != nil {
			strike = *font.Strike
		}
		fmt.Fprintf(&b, "text-decoration-line:%s;", htmlDecoration(u, strike))
		kind := font.Underline
		if kind == "" {
			kind = base.UnderlineStyle
		}
		if kind == "double" || kind == "doubleAccounting" {
			b.WriteString("text-decoration-style:double;")
		}
		if strings.HasSuffix(kind, "Accounting") {
			b.WriteString("text-underline-position:under;")
		}
		raised := font.Vertical == "superscript" || font.Vertical == "subscript"
		if font.Size > 0 || raised {
			size := font.Size
			if size == 0 {
				size = base.FontSize
			}
			if size <= 0 {
				size = 8.25
			}
			if raised {
				size *= .65
			}
			b.WriteString("font-size:" + strconv.FormatFloat(size, 'f', -1, 64) + "pt;")
		}
		switch font.Vertical {
		case "superscript":
			b.WriteString("vertical-align:super;")
		case "subscript":
			b.WriteString("vertical-align:sub;")
		case "baseline":
			b.WriteString("vertical-align:baseline;")
		}
		_, err := fmt.Fprintf(out, `<span style="%s">%s</span>`, html.EscapeString(b.String()), html.EscapeString(part))
		return err
	})
}

func overlayRichFont(base, overlay workbook.RichFont) workbook.RichFont {
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	if overlay.Size != 0 {
		base.Size = overlay.Size
	}
	if overlay.Color != "" || overlay.AutoColor {
		base.Color, base.AutoColor = overlay.Color, overlay.AutoColor
	}
	if overlay.Bold != nil {
		base.Bold = overlay.Bold
	}
	if overlay.Italic != nil {
		base.Italic = overlay.Italic
	}
	if overlay.Strike != nil {
		base.Strike = overlay.Strike
	}
	if overlay.Underline != "" {
		base.Underline = overlay.Underline
	}
	if overlay.Vertical != "" {
		base.Vertical = overlay.Vertical
	}
	return base
}

func conditionalHTMLNotice(rich string, cell workbook.CellData, row string) string {
	parts := []string{}
	for _, v := range []string{rich, row} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	if c := cell.Conditional; c != nil {
		if c.Notice != "" {
			parts = append(parts, c.Notice)
		}
		if c.Style.Rotation != nil && *c.Style.Rotation != 0 {
			parts = append(parts, "条件格式文字旋转尚未显示")
		}
		if strings.HasSuffix(c.Style.Font.Underline, "Accounting") {
			parts = append(parts, "会计下划线按文字下划线显示")
		}
	}
	return strings.Join(parts, "；")
}
