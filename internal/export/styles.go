package export

// Rebuild the common visual palette for non-OOXML sources. Cell style IDs
// remain stable, while identical fonts, fills and borders share one record.
import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

type exportFont struct {
	name, color                     string
	size                            float64
	bold, italic, underline, strike bool
}
type exportBorder struct{ left, right, top, bottom string }

func nativeStylesXML(wb *workbook.Workbook, enabled bool) string {
	n := 1
	if enabled {
		n = max(n, len(wb.Styles), len(wb.VisualStyles))
	}
	fonts := []exportFont{}
	fontIDs := map[exportFont]int{}
	fills := []string{"", ""} // mandatory none + gray125
	fillIDs := map[string]int{"": 0}
	borders := []exportBorder{}
	borderIDs := map[exportBorder]int{}
	fontAt, fillAt, borderAt := make([]int, n), make([]int, n), make([]int, n)
	for i := 0; i < n; i++ {
		v := workbook.VisualStyle{}
		if enabled && i < len(wb.VisualStyles) {
			v = wb.VisualStyles[i]
		}
		font := exportFont{v.FontName, cssColor(v.Color), v.FontSize, v.Bold, v.Italic, v.Underline, v.Strike}
		if font.name == "" {
			font.name = "Calibri"
		}
		if font.size <= 0 {
			font.size = 11
		}
		id, ok := fontIDs[font]
		if !ok {
			id = len(fonts)
			fontIDs[font] = id
			fonts = append(fonts, font)
		}
		fontAt[i] = id
		fill := cssColor(v.Background)
		id, ok = fillIDs[fill]
		if !ok {
			id = len(fills)
			fillIDs[fill] = id
			fills = append(fills, fill)
		}
		fillAt[i] = id
		border := exportBorder{v.BorderLeft, v.BorderRight, v.BorderTop, v.BorderBottom}
		id, ok = borderIDs[border]
		if !ok {
			id = len(borders)
			borderIDs[border] = id
			borders = append(borders, border)
		}
		borderAt[i] = id
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	custom := map[int]string{}
	if enabled {
		for _, s := range wb.Styles {
			if s.NumFmtID >= 164 && wb.NumFmts[s.NumFmtID] != "" {
				custom[s.NumFmtID] = wb.NumFmts[s.NumFmtID]
			}
		}
	}
	if len(custom) > 0 {
		ids := make([]int, 0, len(custom))
		for id := range custom {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		fmt.Fprintf(&b, `<numFmts count="%d">`, len(ids))
		for _, id := range ids {
			fmt.Fprintf(&b, `<numFmt numFmtId="%d" formatCode="%s"/>`, id, xmlAttr(custom[id]))
		}
		b.WriteString(`</numFmts>`)
	}
	fmt.Fprintf(&b, `<fonts count="%d">`, len(fonts))
	for _, f := range fonts {
		fmt.Fprintf(&b, `<font><sz val="%g"/><name val="%s"/>`, f.size, xmlAttr(f.name))
		if f.color != "" {
			fmt.Fprintf(&b, `<color rgb="FF%s"/>`, strings.ToUpper(f.color[1:]))
		}
		if f.bold {
			b.WriteString(`<b/>`)
		}
		if f.italic {
			b.WriteString(`<i/>`)
		}
		if f.underline {
			b.WriteString(`<u/>`)
		}
		if f.strike {
			b.WriteString(`<strike/>`)
		}
		b.WriteString(`</font>`)
	}
	fmt.Fprintf(&b, `</fonts><fills count="%d"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>`, len(fills))
	for _, fill := range fills[2:] {
		fmt.Fprintf(&b, `<fill><patternFill patternType="solid"><fgColor rgb="FF%s"/><bgColor indexed="64"/></patternFill></fill>`, strings.ToUpper(fill[1:]))
	}
	fmt.Fprintf(&b, `</fills><borders count="%d">`, len(borders))
	for _, border := range borders {
		b.WriteString(`<border>`)
		writeExportBorder(&b, "left", border.left)
		writeExportBorder(&b, "right", border.right)
		writeExportBorder(&b, "top", border.top)
		writeExportBorder(&b, "bottom", border.bottom)
		b.WriteString(`<diagonal/></border>`)
	}
	fmt.Fprintf(&b, `</borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="%d">`, n)
	for i := 0; i < n; i++ {
		numFmt := 0
		if enabled && i < len(wb.Styles) {
			id := wb.Styles[i].NumFmtID
			if id >= 0 && (id < 164 || custom[id] != "") {
				numFmt = id
			}
		}
		fmt.Fprintf(&b, `<xf numFmtId="%d" fontId="%d" fillId="%d" borderId="%d" xfId="0" applyNumberFormat="1" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1">`, numFmt, fontAt[i], fillAt[i], borderAt[i])
		v := workbook.VisualStyle{}
		if enabled && i < len(wb.VisualStyles) {
			v = wb.VisualStyles[i]
		}
		b.WriteString(`<alignment`)
		switch v.Horizontal {
		case "left", "right", "center", "general", "fill", "justify", "centerContinuous", "distributed":
			fmt.Fprintf(&b, ` horizontal="%s"`, v.Horizontal)
		}
		switch v.Vertical {
		case "top", "bottom", "center", "justify", "distributed":
			fmt.Fprintf(&b, ` vertical="%s"`, v.Vertical)
		}
		if v.Wrap {
			b.WriteString(` wrapText="1"`)
		}
		rotation := v.Rotation
		if rotation < 0 {
			rotation = 90 - rotation
		}
		if rotation > 0 && rotation <= 180 || rotation == 255 {
			fmt.Fprintf(&b, ` textRotation="%d"`, rotation)
		}
		b.WriteString(`/></xf>`)
	}
	b.WriteString(`</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`)
	return b.String()
}

func writeExportBorder(b *strings.Builder, side, css string) {
	fields := strings.Fields(css)
	style, color := "", "#000000"
	if len(fields) >= 2 {
		width, _ := strconv.ParseFloat(strings.TrimSuffix(fields[0], "px"), 64)
		switch fields[1] {
		case "solid":
			style = "thin"
			if width >= 2 {
				style = "medium"
			}
			if width >= 3 {
				style = "thick"
			}
		case "dotted":
			style = "dotted"
		case "dashed":
			style = "dashed"
			if width >= 2 {
				style = "mediumDashed"
			}
		case "double":
			style = "double"
		}
		if len(fields) >= 3 && cssColor(fields[2]) != "" {
			color = fields[2]
		}
	}
	if style == "" {
		fmt.Fprintf(b, `<%s/>`, side)
		return
	}
	fmt.Fprintf(b, `<%s style="%s"><color rgb="FF%s"/></%s>`, side, style, strings.ToUpper(color[1:]), side)
}
