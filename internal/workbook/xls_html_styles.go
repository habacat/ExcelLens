package workbook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type htmlWorkbookMetadata struct {
	Version     int
	Fingerprint string
	Checksum    string
	Names       []string
	Styles      htmlStyleMetadata
}

func (m htmlWorkbookMetadata) checksum() string {
	m.Checksum = ""
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type htmlStyleMetadata struct {
	Sheets  []htmlDocumentSheet
	Palette []VisualStyle
	IDs     map[string]int
	rules   map[string]string
	base    string
}

func newHTMLStyleMetadata() *htmlStyleMetadata {
	return &htmlStyleMetadata{Palette: []VisualStyle{{}}, IDs: make(map[string]int), rules: make(map[string]string)}
}

var htmlCSSRules = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
var htmlCSSComments = regexp.MustCompile(`(?s)/\*.*?\*/`)
var htmlCSSClass = regexp.MustCompile(`^(?:td|th)?\.([A-Za-z_][A-Za-z0-9_-]*)$`)

func (m *htmlStyleMetadata) addCSS(css string) {
	css = htmlCSSComments.ReplaceAllString(css, "")
	css = strings.ReplaceAll(strings.ReplaceAll(css, "<!--", ""), "-->", "")
	for _, match := range htmlCSSRules.FindAllStringSubmatch(css, -1) {
		for _, selector := range strings.Split(match[1], ",") {
			selector = strings.TrimSpace(selector)
			if selector == "td" || selector == "th" {
				m.base += ";" + match[2]
				continue
			}
			if class := htmlCSSClass.FindStringSubmatch(selector); class != nil {
				m.rules[class[1]] += ";" + match[2]
			}
		}
	}
}

func (m htmlStyleMetadata) valid() bool {
	if len(m.Palette) == 0 || len(m.Palette) > 65536 || len(m.IDs) > 65536 {
		return false
	}
	for key, id := range m.IDs {
		if len(key) > 16384 || id < 0 || id >= len(m.Palette) {
			return false
		}
	}
	if len(m.Sheets) > 4096 {
		return false
	}
	for _, sheet := range m.Sheets {
		if len(sheet.Merges) > 100000 || len(sheet.Rows) > 100000 || len(sheet.Cols) > formats.ExcelColumns {
			return false
		}
		last := 0
		for _, r := range sheet.Rows {
			if r.Min <= last || r.Max < r.Min || r.Max > maxExternalRows || !(r.Size >= 10 && r.Size <= 1200) {
				return false
			}
			last = r.Max
		}
		for c, size := range sheet.Cols {
			if c < 1 || c > formats.ExcelColumns || !(size >= 10 && size <= 1200) {
				return false
			}
		}
		for _, r := range sheet.Merges {
			if r.Row < 1 || r.Col < 1 || r.EndRow < r.Row || r.EndCol < r.Col || r.EndRow > maxExternalRows || r.EndCol > formats.ExcelColumns {
				return false
			}
		}
	}
	return true
}

func relevantHTMLCSS(raw string) string {
	var out strings.Builder
	for _, decl := range strings.Split(raw, ";") {
		key, _, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		switch key {
		case "color", "background", "background-color", "font-family", "font-size", "font-weight", "font-style", "text-decoration", "text-align", "vertical-align", "white-space", "border", "border-top", "border-bottom", "border-left", "border-right", "border-width", "border-style", "border-color", "border-left-width", "border-left-style", "border-left-color", "border-right-width", "border-right-style", "border-right-color", "border-top-width", "border-top-style", "border-top-color", "border-bottom-width", "border-bottom-style", "border-bottom-color":
			out.WriteString(strings.TrimSpace(decl))
			out.WriteByte(';')
		}
	}
	return out.String()
}

func (m *htmlStyleMetadata) styleFor(token htmlTableToken, collect bool) (int, error) {
	return m.styleForChain([]htmlTableToken{token}, collect)
}

func (m *htmlStyleMetadata) styleForChain(tokens []htmlTableToken, collect bool) (int, error) {
	var chain strings.Builder
	for i, token := range tokens {
		if i > 0 {
			chain.WriteByte(1)
		}
		class, _ := token.attr("class")
		inline, _ := token.attr("style")
		extra := ""
		switch token.name {
		case "b", "strong":
			extra = "font-weight:bold;"
		case "i", "em":
			extra = "font-style:italic;"
		case "u":
			extra = "text-decoration:underline;"
		case "s", "strike":
			extra = "text-decoration:line-through;"
		}
		if value, ok := token.attr("align"); ok {
			extra += "text-align:" + value + ";"
		}
		if value, ok := token.attr("valign"); ok {
			extra += "vertical-align:" + value + ";"
		}
		if token.name == "font" {
			if value, ok := token.attr("face"); ok {
				extra += "font-family:" + value + ";"
			}
			if value, ok := token.attr("color"); ok {
				extra += "color:" + value + ";"
			}
		}
		chain.WriteString(class)
		chain.WriteByte(0)
		chain.WriteString(relevantHTMLCSS(extra + inline))
	}
	key := chain.String()
	if len(key) > 16384 {
		return 0, errors.New("HTML 样式继承链超过安全上限")
	}
	if id, ok := m.IDs[key]; ok {
		return id, nil
	}
	if !collect {
		return 0, nil
	}
	if len(m.IDs) >= 65536 {
		return 0, errors.New("HTML 样式组合超过 65,536 安全上限")
	}
	m.IDs[key] = 0
	return 0, nil
}

func (m *htmlStyleMetadata) seal() {
	for i := range m.Sheets {
		m.Sheets[i].widthVotes = nil
		m.Sheets[i].explicitCols = nil
	}
	keys := make([]string, 0, len(m.IDs))
	for key := range m.IDs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	m.Palette = []VisualStyle{{}}
	palette := map[VisualStyle]int{{}: 0}
	for _, key := range keys {
		v := VisualStyle{}
		applyHTMLCSS(&v, m.base)
		for _, part := range strings.Split(key, "\x01") {
			class, inline, _ := strings.Cut(part, "\x00")
			for _, name := range strings.Fields(class) {
				applyHTMLCSS(&v, m.rules[name])
			}
			applyHTMLCSS(&v, inline)
		}
		id, ok := palette[v]
		if !ok {
			id = len(m.Palette)
			m.Palette = append(m.Palette, v)
			palette[v] = id
		}
		m.IDs[key] = id
	}
	m.rules = nil
	m.base = ""
}

func htmlColor(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "#") && (len(value) == 4 || len(value) == 7) {
		for _, r := range value[1:] {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return ""
			}
		}
		if len(value) == 4 {
			return fmt.Sprintf("#%c%c%c%c%c%c", value[1], value[1], value[2], value[2], value[3], value[3])
		}
		return value
	}
	return map[string]string{"black": "#000000", "white": "#ffffff", "red": "#ff0000", "blue": "#0000ff", "green": "#008000", "yellow": "#ffff00", "gray": "#808080", "silver": "#c0c0c0"}[value]
}

func htmlBorder(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "none" || value == "0" || value == "hidden" {
		return ""
	}
	width, kind, color := 1.0, "", "#000000"
	for _, word := range strings.Fields(value) {
		if c := htmlColor(word); c != "" {
			color = c
			continue
		}
		switch word {
		case "solid", "dotted", "dashed", "double":
			kind = word
		case "thin":
			width = 1
		case "medium":
			width = 2
		case "thick":
			width = 3
		default:
			factor := 1.0
			numeric := word
			if strings.HasSuffix(word, "pt") {
				factor = 4.0 / 3
				numeric = strings.TrimSuffix(word, "pt")
			} else {
				numeric = strings.TrimSuffix(word, "px")
			}
			if n, e := strconv.ParseFloat(numeric, 64); e == nil && n > 0 && n < 20 {
				width = n * factor
			}
		}
	}
	if kind == "" {
		return ""
	}
	return fmt.Sprintf("%.3gpx %s %s", width, kind, color)
}

func applyHTMLCSS(v *VisualStyle, css string) {
	// BI Publisher emits the three border components separately rather than
	// a shorthand declaration. Resolve them before applying later shorthands.
	parts := make(map[string]string)
	for _, decl := range strings.Split(css, ";") {
		key, value, ok := strings.Cut(decl, ":")
		if ok {
			parts[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	for _, edge := range []string{"", "-left", "-right", "-top", "-bottom"} {
		prefix := "border" + edge
		if parts[prefix+"-style"] != "" {
			width := parts[prefix+"-width"]
			if width == "" {
				width = "1px"
			}
			color := parts[prefix+"-color"]
			if color == "" {
				color = "#000000"
			}
			css = prefix + ":" + width + " " + parts[prefix+"-style"] + " " + color + ";" + css
		}
	}
	for _, decl := range strings.Split(css, ";") {
		key, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "color":
			v.Color = htmlColor(value)
		case "background", "background-color":
			v.Background = htmlColor(value)
		case "font-family":
			name := strings.Trim(strings.TrimSpace(strings.Split(value, ",")[0]), "\"'")
			if len(name) <= 128 {
				v.FontName = name
			}
		case "font-size":
			factor := 1.0
			if strings.HasSuffix(value, "px") {
				factor = .75
			}
			n, e := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(value, "pt"), "px"), 64)
			if e == nil && n > 0 && n <= 96 {
				v.FontSize = n * factor
			}
		case "font-weight":
			n, _ := strconv.Atoi(value)
			v.Bold = value == "bold" || n >= 600
		case "font-style":
			v.Italic = value == "italic" || value == "oblique"
		case "text-decoration":
			v.Underline = strings.Contains(value, "underline")
			v.Strike = strings.Contains(value, "line-through")
		case "text-align":
			switch value {
			case "left", "right", "center", "justify", "distributed", "general":
				v.Horizontal = value
			}
		case "vertical-align":
			switch value {
			case "middle":
				v.Vertical = "center"
			case "top", "bottom":
				v.Vertical = value
			}
		case "white-space":
			v.Wrap = value == "normal" || value == "pre-wrap"
		case "border":
			b := htmlBorder(value)
			v.BorderTop = b
			v.BorderBottom = b
			v.BorderLeft = b
			v.BorderRight = b
		case "border-top":
			v.BorderTop = htmlBorder(value)
		case "border-bottom":
			v.BorderBottom = htmlBorder(value)
		case "border-left":
			v.BorderLeft = htmlBorder(value)
		case "border-right":
			v.BorderRight = htmlBorder(value)
		}
	}
}
