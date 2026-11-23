package workbook

// OOXML visual style support. A workbook's style IDs form a compact palette:
// the viewer sends it once, while cell payloads carry only their original ID.
// This avoids repeating colors/font descriptions millions of times.

import (
	"archive/zip"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

type VisualStyle struct {
	ShortDate      string  `json:"shortDate,omitempty"`
	NumFmtID       int     `json:"numFmtId,omitempty"`
	Color          string  `json:"color,omitempty"`
	Background     string  `json:"background,omitempty"`
	FontName       string  `json:"fontName,omitempty"`
	FontSize       float64 `json:"fontSize,omitempty"`
	Bold           bool    `json:"bold,omitempty"`
	Italic         bool    `json:"italic,omitempty"`
	Underline      bool    `json:"underline,omitempty"`
	UnderlineStyle string  `json:"underlineStyle,omitempty"`
	FontVertical   string  `json:"fontVertical,omitempty"`
	Strike         bool    `json:"strike,omitempty"`
	Horizontal     string  `json:"horizontal,omitempty"`
	Vertical       string  `json:"vertical,omitempty"`
	Wrap           bool    `json:"wrap,omitempty"`
	Rotation       int     `json:"rotation,omitempty"`
	BorderTop      string  `json:"borderTop,omitempty"`
	BorderBottom   string  `json:"borderBottom,omitempty"`
	BorderLeft     string  `json:"borderLeft,omitempty"`
	BorderRight    string  `json:"borderRight,omitempty"`
}

type styleColorXML struct {
	RGB     string  `xml:"rgb,attr"`
	Theme   *int    `xml:"theme,attr"`
	Indexed *int    `xml:"indexed,attr"`
	Tint    float64 `xml:"tint,attr"`
	Auto    string  `xml:"auto,attr"`
}

type flagXML struct {
	Val string `xml:"val,attr"`
}

func enabled(f *flagXML) bool {
	if f == nil {
		return false
	}
	return f.Val == "" || (f.Val != "0" && !strings.EqualFold(f.Val, "false"))
}

type valXML struct {
	Val string `xml:"val,attr"`
}
type styleFontXML struct {
	Name      valXML         `xml:"name"`
	Size      valXML         `xml:"sz"`
	Color     *styleColorXML `xml:"color"`
	Bold      *flagXML       `xml:"b"`
	Italic    *flagXML       `xml:"i"`
	Underline *flagXML       `xml:"u"`
	Strike    *flagXML       `xml:"strike"`
	Vertical  valXML         `xml:"vertAlign"`
}
type styleFillXML struct {
	Pattern struct {
		Type       string         `xml:"patternType,attr"`
		Foreground *styleColorXML `xml:"fgColor"`
		Background *styleColorXML `xml:"bgColor"`
	} `xml:"patternFill"`
}
type styleSideXML struct {
	Style string         `xml:"style,attr"`
	Color *styleColorXML `xml:"color"`
}
type styleBorderXML struct {
	Left   styleSideXML `xml:"left"`
	Right  styleSideXML `xml:"right"`
	Top    styleSideXML `xml:"top"`
	Bottom styleSideXML `xml:"bottom"`
}
type styleXFXML struct {
	NumFmtID          int    `xml:"numFmtId,attr"`
	FontID            int    `xml:"fontId,attr"`
	FillID            int    `xml:"fillId,attr"`
	BorderID          int    `xml:"borderId,attr"`
	XFID              int    `xml:"xfId,attr"`
	ApplyFont         string `xml:"applyFont,attr"`
	ApplyFill         string `xml:"applyFill,attr"`
	ApplyBorder       string `xml:"applyBorder,attr"`
	ApplyNumberFormat string `xml:"applyNumberFormat,attr"`
	ApplyAlignment    string `xml:"applyAlignment,attr"`
	Alignment         struct {
		Horizontal string `xml:"horizontal,attr"`
		Vertical   string `xml:"vertical,attr"`
		WrapText   string `xml:"wrapText,attr"`
		Rotation   int    `xml:"textRotation,attr"`
	} `xml:"alignment"`
}
type stylesXML struct {
	XMLName xml.Name `xml:"styleSheet"`
	NumFmts struct {
		Items []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmt"`
	} `xml:"numFmts"`
	Fonts        []styleFontXML   `xml:"fonts>font"`
	Fills        []styleFillXML   `xml:"fills>fill"`
	Borders      []styleBorderXML `xml:"borders>border"`
	CellXfs      []styleXFXML     `xml:"cellXfs>xf"`
	CellStyleXfs []styleXFXML     `xml:"cellStyleXfs>xf"`
	CellStyles   []struct {
		BuiltinID *int `xml:"builtinId,attr"`
		XfID      int  `xml:"xfId,attr"`
	} `xml:"cellStyles>cellStyle"`
	IndexedColors []struct {
		RGB string `xml:"rgb,attr"`
	} `xml:"colors>indexedColors>rgbColor"`
	Differential differentialStylesXML `xml:"dxfs"`
}

type themeColorXML struct {
	SRGB *struct {
		Val string `xml:"val,attr"`
	} `xml:"srgbClr"`
	System *struct {
		Last string `xml:"lastClr,attr"`
		Val  string `xml:"val,attr"`
	} `xml:"sysClr"`
}

func (c themeColorXML) value() string {
	if c.SRGB != nil {
		return parseHexColor(c.SRGB.Val)
	}
	if c.System != nil {
		return parseHexColor(c.System.Last)
	}
	return ""
}

type themeXML struct {
	Scheme struct {
		Light1          themeColorXML    `xml:"lt1"`
		Dark1           themeColorXML    `xml:"dk1"`
		Light2          themeColorXML    `xml:"lt2"`
		Dark2           themeColorXML    `xml:"dk2"`
		Accents         [0]themeColorXML `xml:"-"` // accents are named elements below
		Accent1         themeColorXML    `xml:"accent1"`
		Accent2         themeColorXML    `xml:"accent2"`
		Accent3         themeColorXML    `xml:"accent3"`
		Accent4         themeColorXML    `xml:"accent4"`
		Accent5         themeColorXML    `xml:"accent5"`
		Accent6         themeColorXML    `xml:"accent6"`
		Hyperlink       themeColorXML    `xml:"hlink"`
		FollowHyperlink themeColorXML    `xml:"folHlink"`
	} `xml:"themeElements>clrScheme"`
}

var defaultTheme = []string{
	"#FFFFFF", "#000000", "#EEECE1", "#1F497D", "#4F81BD", "#C0504D",
	"#9BBB59", "#8064A2", "#4BACC6", "#F79646", "#0000FF", "#800080",
}

// ECMA-376 indexed palette: the eight primary colors followed by legacy colors.
var defaultIndexed = []string{
	"#000000", "#FFFFFF", "#FF0000", "#00FF00", "#0000FF", "#FFFF00", "#FF00FF", "#00FFFF",
	"#000000", "#FFFFFF", "#FF0000", "#00FF00", "#0000FF", "#FFFF00", "#FF00FF", "#00FFFF",
	"#800000", "#008000", "#000080", "#808000", "#800080", "#008080", "#C0C0C0", "#808080",
	"#9999FF", "#993366", "#FFFFCC", "#CCFFFF", "#660066", "#FF8080", "#0066CC", "#CCCCFF",
	"#000080", "#FF00FF", "#FFFF00", "#00FFFF", "#800080", "#800000", "#008080", "#0000FF",
	"#00CCFF", "#CCFFFF", "#CCFFCC", "#FFFF99", "#99CCFF", "#FF99CC", "#CC99FF", "#FFCC99",
	"#3366FF", "#33CCCC", "#99CC00", "#FFCC00", "#FF9900", "#FF6600", "#666699", "#969696",
	"#003366", "#339966", "#003300", "#333300", "#993300", "#993366", "#333399", "#333333",
}

func parseHexColor(value string) string {
	v := strings.TrimSpace(strings.TrimPrefix(value, "#"))
	if len(v) == 8 {
		v = v[2:]
	} // Excel's ARGB alpha does not apply to cell colors.
	if len(v) != 6 {
		return ""
	}
	if _, err := hex.DecodeString(v); err != nil {
		return ""
	}
	return "#" + strings.ToUpper(v)
}

func loadTheme(f *zip.File) []string {
	colors := append([]string(nil), defaultTheme...)
	if f == nil || f.UncompressedSize64 > maxMetadataPart {
		return colors
	}
	r, err := f.Open()
	if err != nil {
		return colors
	}
	defer r.Close()
	var doc themeXML
	if err := xml.NewDecoder(io.LimitReader(r, maxMetadataPart+1)).Decode(&doc); err != nil {
		return colors
	}
	scheme := doc.Scheme
	entries := []themeColorXML{scheme.Light1, scheme.Dark1, scheme.Light2, scheme.Dark2,
		scheme.Accent1, scheme.Accent2, scheme.Accent3, scheme.Accent4, scheme.Accent5, scheme.Accent6, scheme.Hyperlink, scheme.FollowHyperlink}
	for i, c := range entries {
		if v := c.value(); v != "" {
			colors[i] = v
		}
	}
	return colors
}

func tintHex(color string, tint float64) string {
	if color == "" || tint == 0 {
		return color
	}
	if tint < -1 {
		tint = -1
	}
	if tint > 1 {
		tint = 1
	}
	raw, _ := hex.DecodeString(color[1:])
	if len(raw) != 3 {
		return color
	}
	r, g, b := float64(raw[0])/255, float64(raw[1])/255, float64(raw[2])/255
	mx := math.Max(r, math.Max(g, b))
	mn := math.Min(r, math.Min(g, b))
	l := (mx + mn) / 2
	var h, s float64
	if mx != mn {
		d := mx - mn
		if l > 0.5 {
			s = d / (2 - mx - mn)
		} else {
			s = d / (mx + mn)
		}
		switch mx {
		case r:
			h = (g - b) / d
			if g < b {
				h += 6
			}
		case g:
			h = (b-r)/d + 2
		default:
			h = (r-g)/d + 4
		}
		h /= 6
	}
	if tint < 0 {
		l *= 1 + tint
	} else {
		l = l*(1-tint) + tint
	}
	l = math.Max(0, math.Min(1, l))
	if s == 0 {
		v := uint8(math.Round(l * 255))
		return fmt.Sprintf("#%02X%02X%02X", v, v, v)
	}
	q := l * (1 + s)
	if l >= 0.5 {
		q = l + s - l*s
	}
	p := 2*l - q
	hue := func(t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		if t < 1.0/6 {
			return p + (q-p)*6*t
		}
		if t < 0.5 {
			return q
		}
		if t < 2.0/3 {
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	return fmt.Sprintf("#%02X%02X%02X", int(math.Round(hue(h+1.0/3)*255)), int(math.Round(hue(h)*255)), int(math.Round(hue(h-1.0/3)*255)))
}

func (w *Workbook) resolveColor(c *styleColorXML, indexed []string) string {
	if c == nil {
		return ""
	}
	var col string
	switch {
	case c.RGB != "":
		col = parseHexColor(c.RGB)
	case c.Theme != nil && *c.Theme >= 0 && *c.Theme < len(w.ThemeColors):
		col = w.ThemeColors[*c.Theme]
	case c.Indexed != nil && *c.Indexed >= 0 && *c.Indexed < len(indexed):
		col = indexed[*c.Indexed]
	}
	return tintHex(col, c.Tint)
}

func borderCSS(side styleSideXML, color string) string {
	if side.Style == "" || strings.EqualFold(side.Style, "none") {
		return ""
	}
	// Browsers can snap fractional thin and medium rules to the same pixel.
	// Use the same distinct screen weights as the native workbook border styles.
	width, kind := "1px", "solid"
	switch side.Style {
	case "medium", "mediumDashed", "mediumDashDot", "mediumDashDotDot":
		width = "2px"
	case "thick":
		width = "3px"
	case "hair":
		width = "0.35px"
	}
	switch side.Style {
	case "dotted", "hair":
		kind = "dotted"
	case "dashed", "mediumDashed", "slantDashDot":
		kind = "dashed"
	case "dashDot", "dashDotDot", "mediumDashDot", "mediumDashDotDot":
		kind = "dashed"
	case "double":
		width = "3px"
		kind = "double"
	}
	if color == "" {
		color = "#000000"
	}
	return width + " " + kind + " " + color
}

func (w *Workbook) readStyles(f *zip.File) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	var sx stylesXML
	if err := xml.NewDecoder(io.LimitReader(r, maxMetadataPart+1)).Decode(&sx); err != nil {
		return err
	}
	normalFontID := 0
	for _, style := range sx.CellStyles {
		if style.BuiltinID != nil && *style.BuiltinID == 0 && style.XfID >= 0 && style.XfID < len(sx.CellStyleXfs) {
			normalFontID = sx.CellStyleXfs[style.XfID].FontID
			break
		}
	}
	if normalFontID >= 0 && normalFontID < len(sx.Fonts) {
		f := sx.Fonts[normalFontID]
		size, _ := strconv.ParseFloat(f.Size.Val, 64)
		w.normalFont = VisualStyle{FontName: f.Name.Val, FontSize: size, Bold: enabled(f.Bold), Italic: enabled(f.Italic)}
	}
	w.NumFmts = make(map[int]string, len(sx.NumFmts.Items))
	for _, nf := range sx.NumFmts.Items {
		w.NumFmts[nf.ID] = nf.Code
	}
	indexed := append([]string(nil), defaultIndexed...)
	for i, item := range sx.IndexedColors {
		if i >= len(indexed) {
			break
		}
		if v := parseHexColor(item.RGB); v != "" {
			indexed[i] = v
		}
	}
	w.IndexedColors = indexed
	w.differentialStyles = make([]DifferentialStyle, len(sx.Differential.Items))
	w.differentialNotice = sx.Differential.Notice
	if sx.Differential.seen && sx.Differential.namespace != sx.XMLName.Space {
		w.differentialNotice = "条件样式命名空间与样式表不匹配"
	}
	for i := range sx.Differential.Items {
		w.differentialStyles[i] = w.resolveDifferentialStyle(sx.Differential.Items[i])
	}
	w.Styles = make([]StyleInfo, len(sx.CellXfs))
	w.VisualStyles = make([]VisualStyle, len(sx.CellXfs))
	for i, xf := range sx.CellXfs {
		xf = inheritSourceXF(xf, sx.CellStyleXfs)
		var visual VisualStyle
		if xf.FontID >= 0 && xf.FontID < len(sx.Fonts) {
			font := sx.Fonts[xf.FontID]
			visual.Color = w.resolveColor(font.Color, indexed)
			visual.FontName = font.Name.Val
			if size, e := strconv.ParseFloat(font.Size.Val, 64); e == nil && size >= 1 && size <= 409 {
				visual.FontSize = size
			} else if font.Size.Val == "" {
				visual.FontSize = w.normalFont.FontSize
			}
			visual.Bold = enabled(font.Bold)
			visual.Italic = enabled(font.Italic)
			visual.Underline = enabled(font.Underline) && font.Underline.Val != "none"
			if font.Underline != nil {
				switch font.Underline.Val {
				case "none", "double", "singleAccounting", "doubleAccounting":
					visual.UnderlineStyle = font.Underline.Val
				}
			}
			switch font.Vertical.Val {
			case "baseline", "superscript", "subscript":
				visual.FontVertical = font.Vertical.Val
			}
			visual.Strike = enabled(font.Strike)
		}
		if xf.FillID >= 0 && xf.FillID < len(sx.Fills) {
			p := sx.Fills[xf.FillID].Pattern
			if p.Type == "solid" {
				visual.Background = w.resolveColor(p.Foreground, indexed)
				if visual.Background == "" {
					visual.Background = w.resolveColor(p.Background, indexed)
				}
			}
		}
		if xf.BorderID >= 0 && xf.BorderID < len(sx.Borders) {
			border := sx.Borders[xf.BorderID]
			visual.BorderTop = borderCSS(border.Top, w.resolveColor(border.Top.Color, indexed))
			visual.BorderBottom = borderCSS(border.Bottom, w.resolveColor(border.Bottom.Color, indexed))
			visual.BorderLeft = borderCSS(border.Left, w.resolveColor(border.Left.Color, indexed))
			visual.BorderRight = borderCSS(border.Right, w.resolveColor(border.Right.Color, indexed))
		}
		a := xf.Alignment
		switch a.Horizontal {
		case "left", "center", "right", "justify", "distributed", "fill", "centerContinuous", "general":
			visual.Horizontal = a.Horizontal
		}
		switch a.Vertical {
		case "top", "center", "bottom", "justify", "distributed":
			visual.Vertical = a.Vertical
		}
		visual.Wrap = a.WrapText == "1" || strings.EqualFold(a.WrapText, "true")
		if a.Rotation >= 0 && a.Rotation <= 90 {
			visual.Rotation = a.Rotation
		}
		w.Styles[i] = StyleInfo{NumFmtID: xf.NumFmtID, SectionColors: sectionTextColors(xf.NumFmtID, w.NumFmts[xf.NumFmtID])}
		visual.NumFmtID = xf.NumFmtID
		visual.ShortDate = w.GridShortDate(xf.NumFmtID)
		w.VisualStyles[i] = visual
	}
	return nil
}

// XLSXCalendarStyle recognizes single-section date and time-of-day number
// formats, with the same cell style inheritance as ordinary workbook reads.
func XLSXCalendarStyle(data []byte, index int) (string, error) {
	var styles stylesXML
	if err := xml.Unmarshal(data, &styles); err != nil {
		return "", err
	}
	if index < 0 || index >= len(styles.CellXfs) {
		return "", nil
	}
	xf := inheritSourceXF(styles.CellXfs[index], styles.CellStyleXfs)
	for _, fmt := range styles.NumFmts.Items {
		if fmt.ID != xf.NumFmtID {
			continue
		}
		code := fmt.Code
		if strings.ContainsAny(code, ";[]") || !isDateFormat(code) {
			return "", nil
		}
		if formatHasToken(code, 'y') || formatHasToken(code, 'd') {
			return "date", nil
		}
		if formatHasToken(code, 'h') || formatHasToken(code, 's') {
			return "time", nil
		}
		return "", nil
	}
	switch xf.NumFmtID {
	case 14, 15, 16, 17, 22:
		return "date", nil
	case 18, 19, 20, 21, 45, 47:
		return "time", nil
	}
	return "", nil
}

// XLSXNumberLabels provides the reader's numeric display for text labels
// generated from styled cells (for example, new table headers).
func XLSXNumberLabels(styles *zip.File, date1904 bool) (func(string, int) string, error) {
	w := Workbook{Date1904: date1904}
	if styles != nil {
		if err := w.readStyles(styles); err != nil {
			return nil, err
		}
	}
	return w.formatNumeric, nil
}

// CalendarNumberFormat is a source style whose numbers Excel can put into a
// date-valued pivot cache. The worksheet itself still stores a number.
type CalendarNumberFormat struct {
	ID   int
	Code string
}

func (f CalendarNumberFormat) Display(serial float64, date1904 bool) string {
	if f.Code != "" {
		return formatCustomDate(serial, f.Code, date1904)
	}
	return formatExcelDate(serial, date1904, f.ID)
}

// XLSXCalendarFormats resolves the cell XFs once for an edit calculation.
// A text cell with the same style retains its original text type.
func XLSXCalendarFormats(data []byte) (map[string]CalendarNumberFormat, error) {
	var styles stylesXML
	if err := xml.Unmarshal(data, &styles); err != nil {
		return nil, err
	}
	formats := map[int]string{}
	for _, f := range styles.NumFmts.Items {
		formats[f.ID] = f.Code
	}
	out := map[string]CalendarNumberFormat{}
	for i, xf := range styles.CellXfs {
		id := inheritSourceXF(xf, styles.CellStyleXfs).NumFmtID
		code := formats[id]
		if code != "" {
			section, _, _ := strings.Cut(code, ";")
			if !isDateFormat(section) {
				continue
			}
			code = section
		} else if !(id >= 14 && id <= 22 || id >= 27 && id <= 36 || id >= 45 && id <= 47 || id >= 50 && id <= 58) {
			continue
		}
		out[strconv.Itoa(i)] = CalendarNumberFormat{id, code}
	}
	return out, nil
}

// An explicit false delegates that attribute group to the named cell style.
// Missing flags retain the source cell XF, as ordinary Excel files require.
func inheritSourceXF(xf styleXFXML, parents []styleXFXML) styleXFXML {
	if xf.XFID < 0 || xf.XFID >= len(parents) {
		return xf
	}
	parent := parents[xf.XFID]
	no := func(v string) bool { return v == "0" || v == "false" }
	if no(xf.ApplyFont) {
		xf.FontID = parent.FontID
	}
	if no(xf.ApplyFill) {
		xf.FillID = parent.FillID
	}
	if no(xf.ApplyBorder) {
		xf.BorderID = parent.BorderID
	}
	if no(xf.ApplyNumberFormat) {
		xf.NumFmtID = parent.NumFmtID
	}
	if no(xf.ApplyAlignment) {
		xf.Alignment = parent.Alignment
	}
	return xf
}

// Number-format color directives override the font color for the matching
// positive/negative/zero section. Precompute their values per XF rather than
// interpreting a format string for every visible cell.
func sectionTextColors(formatID int, format string) [3]string {
	var out [3]string
	if formatID == 38 || formatID == 40 {
		out[1] = "#FF0000"
		return out
	}
	if format == "" {
		return out
	}
	sections := splitFormatSections(format)
	palette := map[string]string{
		"black": "#000000", "white": "#FFFFFF", "red": "#FF0000",
		"green": "#008000", "blue": "#0000FF", "yellow": "#FFFF00",
		"magenta": "#FF00FF", "cyan": "#00FFFF",
	}
	for i, section := range sections {
		if i >= 3 {
			break
		}
		// Only look for bracket directives outside quoted/escaped literals.
		for p := 0; p < len(section); p++ {
			if section[p] == '"' {
				p++
				for p < len(section) && section[p] != '"' {
					p++
				}
				continue
			}
			if section[p] == '\\' {
				p++
				continue
			}
			if section[p] != '[' {
				continue
			}
			end := strings.IndexByte(section[p+1:], ']')
			if end < 0 {
				break
			}
			token := strings.ToLower(strings.TrimSpace(section[p+1 : p+1+end]))
			if col, ok := palette[token]; ok {
				out[i] = col
			}
			p += end + 1
		}
	}
	return out
}

func (w *Workbook) numericTextColor(raw string, style int) string {
	if raw == "" || style < 0 || style >= len(w.Styles) {
		return ""
	}
	colors := w.Styles[style].SectionColors
	if colors == [3]string{} {
		return ""
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return ""
	}
	if value < 0 {
		return colors[1]
	}
	if value == 0 {
		if colors[2] != "" {
			return colors[2]
		}
		return colors[0]
	}
	return colors[0]
}
