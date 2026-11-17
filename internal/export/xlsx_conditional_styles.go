package export

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type xlsxStylePalette struct {
	ctx            context.Context
	raw            string
	doc            *styleXMLDoc
	variants       map[string]int
	components     map[string]int
	numberCodes    map[string]int
	numberByID     map[int]string
	usedNumbers    []bool
	definedNumbers []bool
	nextNumber     int
	baseStyles     int
}

func newXLSXStylePalette(ctx context.Context, raw string) *xlsxStylePalette {
	return &xlsxStylePalette{ctx: ctx, raw: raw, variants: make(map[string]int), components: make(map[string]int), nextNumber: 164}
}

func (p *xlsxStylePalette) ensure() error {
	if p.doc != nil {
		return nil
	}
	d, err := parseStyleXML(p.ctx, p.raw)
	if err != nil {
		return err
	}
	p.doc = d
	root := d.raw[d.root.start:d.root.headEnd]
	atom := func(name string) string { return styleQualified(root, name) }
	defaults := []struct {
		section string
		records []string
	}{
		{"fonts", []string{"<" + atom("font") + "/>"}},
		{"fills", []string{"<" + atom("fill") + "><" + atom("patternFill") + ` patternType="none"/></` + atom("fill") + ">", "<" + atom("fill") + "><" + atom("patternFill") + ` patternType="gray125"/></` + atom("fill") + ">"}},
		{"borders", []string{"<" + atom("border") + "/>"}},
		{"cellStyleXfs", []string{"<" + atom("xf") + ` numFmtId="0" fontId="0" fillId="0" borderId="0"/>`}},
		{"cellXfs", []string{"<" + atom("xf") + ` numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>`}},
	}
	for _, item := range defaults {
		if len(d.section(item.section).original) == 0 {
			for _, raw := range item.records {
				if _, err := d.appendRecord(item.section, raw); err != nil {
					return err
				}
			}
		}
	}
	p.baseStyles = d.section("cellXfs").count()
	p.usedNumbers = make([]bool, formats.XLSXStyleRecords)
	p.definedNumbers = make([]bool, formats.XLSXStyleRecords)
	p.numberCodes = make(map[string]int)
	p.numberByID = make(map[int]string)
	if err := d.reserve(len(p.usedNumbers) + len(p.definedNumbers) + 256); err != nil {
		return err
	}
	for id := range d.section("numFmts").original {
		f, err := p.fragment("numFmts", id)
		if err != nil {
			return err
		}
		n, err := styleIntAttr(f.attrs, "numFmtId", -1)
		if err != nil || n < 0 {
			return errors.New("源数字格式编号无效")
		}
		code, _, err := styleAttr(f.attrs, "formatCode")
		if err != nil {
			return err
		}
		if p.usedNumbers[n] {
			return errors.New("源数字格式编号重复，无法生成完整快照")
		}
		p.usedNumbers[n] = true
		p.definedNumbers[n] = true
		if code != "" {
			p.numberCodes[code] = n
			p.numberByID[n] = code
			if err := d.reserve(len(code)*2 + 192); err != nil {
				return err
			}
		}
	}
	// Preserve source IDs even where a malformed source omitted its definition.
	// They must not accidentally become a newly allocated conditional format.
	for _, section := range []string{"cellXfs", "cellStyleXfs"} {
		for id := 0; id < d.section(section).count(); id++ {
			f, err := p.fragment(section, id)
			if err != nil {
				return err
			}
			n, err := styleIntAttr(f.attrs, "numFmtId", 0)
			if err != nil {
				return err
			}
			p.usedNumbers[n] = true
		}
	}
	return nil
}

func (p *xlsxStylePalette) fragment(section string, id int) (*styleXMLFragment, error) {
	raw, err := p.doc.record(section, id)
	if err != nil {
		return nil, err
	}
	return parseStyleFragment(p.ctx, p.doc.wrapper(section), raw)
}
func styleBoolAttr(attrs []xml.Attr, name string) (bool, bool, error) {
	raw, found, err := styleAttr(attrs, name)
	if err != nil || !found {
		return false, found, err
	}
	switch raw {
	case "0", "false":
		return false, true, nil
	case "1", "true":
		return true, true, nil
	}
	return false, true, fmt.Errorf("源样式布尔属性无效：%s", name)
}

func (p *xlsxStylePalette) parent(f *styleXMLFragment) (*styleXMLFragment, error) {
	id, err := styleIntAttr(f.attrs, "xfId", 0)
	if err != nil {
		return nil, err
	}
	return p.fragment("cellStyleXfs", id)
}

func (p *xlsxStylePalette) effective(f *styleXMLFragment, flag string) (*styleXMLFragment, error) {
	value, found, err := styleBoolAttr(f.attrs, flag)
	if err != nil {
		return nil, err
	}
	if found && !value {
		return p.parent(f)
	}
	return f, nil
}

func (p *xlsxStylePalette) reference(f *styleXMLFragment, attribute, section string) (int, error) {
	id, err := styleIntAttr(f.attrs, attribute, 0)
	if err != nil {
		return 0, err
	}
	s := p.doc.section(section)
	if id >= len(s.original) && !(len(s.original) == 0 && id == 0) {
		return 0, fmt.Errorf("源%s样式编号不存在：%d", section, id)
	}
	return id, nil
}

func fontHasDifference(f workbook.RichFont) bool {
	return f.Name != "" || f.Size != 0 || f.Color != "" || f.AutoColor || f.Bold != nil || f.Italic != nil || f.Strike != nil || f.Underline != "" || f.Vertical != ""
}

func (p *xlsxStylePalette) validateBase(base *styleXMLFragment, diff workbook.DifferentialStyle) error {
	for _, item := range []struct{ flag, attribute, section string }{
		{"applyFont", "fontId", "fonts"},
		{"applyFill", "fillId", "fills"},
		{"applyBorder", "borderId", "borders"},
	} {
		// A replacement fill does not inherit any part of the old fill.
		if item.section == "fills" && diff.Background != nil {
			continue
		}
		effective, err := p.effective(base, item.flag)
		if err != nil {
			return err
		}
		if _, err := p.reference(effective, item.attribute, item.section); err != nil {
			return err
		}
	}
	if diff.NumberFormat == nil {
		effective, err := p.effective(base, "applyNumberFormat")
		if err != nil {
			return err
		}
		n, err := styleIntAttr(effective.attrs, "numFmtId", 0)
		if err != nil {
			return err
		}
		if n >= 164 && !p.definedNumbers[n] {
			return fmt.Errorf("源数字格式定义不存在：%d", n)
		}
	}
	_, err := p.effective(base, "applyAlignment")
	return err
}

func (p *xlsxStylePalette) styleID(cell workbook.CellData) (int, error) {
	if cell.Conditional == nil {
		return cell.Style, nil
	}
	if cell.Conditional.Notice != "" || cell.Conditional.Style.Notice != "" {
		notice := cell.Conditional.Notice
		if notice == "" {
			notice = cell.Conditional.Style.Notice
		}
		return 0, fmt.Errorf("条件格式未完整还原：%s", notice)
	}
	if cell.Conditional.Visual == nil {
		return cell.Style, nil
	}
	if err := p.ensure(); err != nil {
		return 0, err
	}
	if cell.Style < 0 || cell.Style >= p.baseStyles {
		return 0, fmt.Errorf("源单元格样式编号不存在：%d", cell.Style)
	}
	diff := cell.Conditional.Style
	encoded, err := json.Marshal(diff)
	if err != nil {
		return 0, err
	}
	key := strconv.Itoa(cell.Style) + ":" + string(encoded)
	if id, ok := p.variants[key]; ok {
		return id, nil
	}
	base, err := p.fragment("cellXfs", cell.Style)
	if err != nil {
		return 0, err
	}
	if err := p.validateBase(base, diff); err != nil {
		return 0, err
	}
	attrs := make(map[string]string)
	children := make(map[string]string)
	if fontHasDifference(diff.Font) {
		effective, err := p.effective(base, "applyFont")
		if err != nil {
			return 0, err
		}
		fontID, err := p.reference(effective, "fontId", "fonts")
		if err != nil {
			return 0, err
		}
		fontID, err = p.fontID(fontID, diff.Font)
		if err != nil {
			return 0, err
		}
		attrs["fontId"], attrs["applyFont"] = strconv.Itoa(fontID), "1"
	}
	if diff.Background != nil {
		id, err := p.fillID(*diff.Background)
		if err != nil {
			return 0, err
		}
		attrs["fillId"], attrs["applyFill"] = strconv.Itoa(id), "1"
	}
	if b := diff.Border; b != nil && (b.Top != nil || b.Bottom != nil || b.Left != nil || b.Right != nil) {
		effective, err := p.effective(base, "applyBorder")
		if err != nil {
			return 0, err
		}
		id, err := p.reference(effective, "borderId", "borders")
		if err != nil {
			return 0, err
		}
		id, err = p.borderID(id, *b, *cell.Conditional.Visual)
		if err != nil {
			return 0, err
		}
		attrs["borderId"], attrs["applyBorder"] = strconv.Itoa(id), "1"
	}
	number := diff.NumberFormat
	if diff.Font.Color != "" || diff.Font.AutoColor {
		id, code := 0, ""
		if number != nil {
			id, code = number.ID, number.Code
		} else {
			effective, err := p.effective(base, "applyNumberFormat")
			if err != nil {
				return 0, err
			}
			id, err = styleIntAttr(effective.attrs, "numFmtId", 0)
			if err != nil {
				return 0, err
			}
			code = p.numberByID[id]
		}
		if clean, changed := withoutNumberFormatColors(code); changed {
			// DXF font color overrides number-format color in the source view.
			// A plain XF would restore [Red]/[ColorN] above its new font color.
			if clean == "" {
				clean = `""` // retain an intentionally empty display section
			}
			number = &workbook.DifferentialNumberFormat{Code: clean}
		} else if code == "" && (id == 38 || id == 40) {
			attrs["numFmtId"], attrs["applyNumberFormat"] = strconv.Itoa(id-1), "1"
		}
	}
	if number != nil {
		id, err := p.numberID(*number)
		if err != nil {
			return 0, err
		}
		attrs["numFmtId"], attrs["applyNumberFormat"] = strconv.Itoa(id), "1"
	}
	if diff.Horizontal != nil || diff.Vertical != nil || diff.Wrap != nil || diff.Rotation != nil {
		alignment, err := p.alignment(base, diff)
		if err != nil {
			return 0, err
		}
		children["alignment"] = alignment
		attrs["applyAlignment"] = "1"
	}
	derived, err := rewriteStyleFragment(base, attrs, children, []string{"alignment", "protection", "extLst"})
	if err != nil {
		return 0, err
	}
	id, err := p.doc.appendRecord("cellXfs", derived)
	if err != nil {
		return 0, err
	}
	if err := p.doc.reserve(len(key)*2 + 96); err != nil {
		return 0, err
	}
	p.variants[key] = id
	return id, nil
}

func withoutNumberFormatColors(code string) (string, bool) {
	quoted := false
	last := 0
	var out strings.Builder
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '\\', '_', '*':
			i++
		case '"':
			quoted = !quoted
		case '[':
			if quoted {
				continue
			}
			end := strings.IndexByte(code[i+1:], ']')
			if end < 0 {
				return code, false
			}
			end += i + 2
			value := strings.ToLower(strings.TrimSpace(code[i+1 : end-1]))
			color := false
			switch value {
			case "black", "white", "red", "green", "blue", "yellow", "magenta", "cyan":
				color = true
			default:
				if strings.HasPrefix(value, "color") {
					n, err := strconv.Atoi(strings.TrimSpace(value[5:]))
					color = err == nil && n >= 1 && n <= 56
				}
			}
			if color {
				out.WriteString(code[last:i])
				last = end
			}
			i = end - 1
		}
	}
	if last == 0 || quoted {
		return code, false
	}
	out.WriteString(code[last:])
	return out.String(), true
}

func (p *xlsxStylePalette) componentKey(kind string, base int, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return kind + ":" + strconv.Itoa(base) + ":" + string(encoded), nil
}
func (p *xlsxStylePalette) rememberComponent(key string, id int) error {
	if err := p.doc.reserve(len(key)*2 + 96); err != nil {
		return err
	}
	p.components[key] = id
	return nil
}

func (p *xlsxStylePalette) fontID(base int, font workbook.RichFont) (int, error) {
	key, err := p.componentKey("font", base, font)
	if err != nil {
		return 0, err
	}
	if id, ok := p.components[key]; ok {
		return id, nil
	}
	f, err := p.fragment("fonts", base)
	if err != nil {
		return 0, err
	}
	q := func(name string) string { return styleQualified(f.raw, name) }
	props := make(map[string]string)
	for _, item := range []struct{ name, value string }{{"name", font.Name}, {"u", font.Underline}, {"vertAlign", font.Vertical}} {
		if item.value != "" {
			props[item.name] = "<" + q(item.name) + ` val="` + xmlAttr(item.value) + `"/>`
		}
	}
	if font.Size != 0 {
		props["sz"] = "<" + q("sz") + ` val="` + strconv.FormatFloat(font.Size, 'f', -1, 64) + `"/>`
	}
	if font.Color != "" {
		props["color"] = "<" + q("color") + ` rgb="FF` + strings.TrimPrefix(font.Color, "#") + `"/>`
	} else if font.AutoColor {
		props["color"] = "<" + q("color") + ` auto="1"/>`
	}
	for _, flag := range []struct {
		name  string
		value *bool
	}{{"b", font.Bold}, {"i", font.Italic}, {"strike", font.Strike}} {
		if flag.value != nil {
			value := "0"
			if *flag.value {
				value = "1"
			}
			props[flag.name] = "<" + q(flag.name) + ` val="` + value + `"/>`
		}
	}
	raw, err := rewriteStyleFragment(f, nil, props, nil)
	if err != nil {
		return 0, err
	}
	id, err := p.doc.appendRecord("fonts", raw)
	if err != nil {
		return 0, err
	}
	if err := p.rememberComponent(key, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (p *xlsxStylePalette) fillID(color string) (int, error) {
	key := "fill:" + color
	if id, ok := p.components[key]; ok {
		return id, nil
	}
	w := p.doc.wrapper("fills")
	q := func(name string) string { return styleQualified(w.head[strings.LastIndex(w.head, "<"):], name) }
	raw := "<" + q("fill") + "><" + q("patternFill") + ` patternType="none"/></` + q("fill") + ">"
	if color != "" {
		raw = "<" + q("fill") + "><" + q("patternFill") + ` patternType="solid"><` + q("fgColor") + ` rgb="FF` + strings.TrimPrefix(color, "#") + `"/></` + q("patternFill") + "></" + q("fill") + ">"
	}
	id, err := p.doc.appendRecord("fills", raw)
	if err != nil {
		return 0, err
	}
	if err := p.rememberComponent(key, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (p *xlsxStylePalette) borderID(base int, border workbook.DifferentialBorders, visual workbook.VisualStyle) (int, error) {
	key, err := p.componentKey("border", base, struct {
		Diff                     workbook.DifferentialBorders
		Top, Bottom, Left, Right string
	}{border, visual.BorderTop, visual.BorderBottom, visual.BorderLeft, visual.BorderRight})
	if err != nil {
		return 0, err
	}
	if id, ok := p.components[key]; ok {
		return id, nil
	}
	f, err := p.fragment("borders", base)
	if err != nil {
		return 0, err
	}
	props := make(map[string]string)
	wrapper := p.doc.wrapper("borders").within(f.raw[:f.span.headEnd])
	for _, side := range []struct {
		name  string
		value *workbook.DifferentialBorderSide
		css   string
	}{{"left", border.Left, visual.BorderLeft}, {"right", border.Right, visual.BorderRight}, {"top", border.Top, visual.BorderTop}, {"bottom", border.Bottom, visual.BorderBottom}} {
		if side.value == nil {
			continue
		}
		raw := "<" + styleQualified(f.raw, side.name) + "/>"
		for _, child := range f.children {
			if child.name.Space == f.name.Space && child.name.Local == side.name {
				raw = f.raw[child.span.start:child.span.end]
				break
			}
		}
		s, err := parseStyleFragment(p.ctx, wrapper, raw)
		if err != nil {
			return 0, err
		}
		attrs := make(map[string]string)
		colors := make(map[string]string)
		if side.value.Style != nil {
			style := *side.value.Style
			if style == "" {
				style = "none"
			}
			attrs["style"] = style
		}
		if side.value.Color != nil {
			colors["color"] = "<" + styleQualified(s.raw, "color") + ` rgb="FF` + strings.TrimPrefix(*side.value.Color, "#") + `"/>`
		} else if side.value.AutoColor {
			// Resolve automatic conditional edges at the source coordinate. A
			// plain XF's automatic color is not necessarily the same as DXF.
			color := "#000000"
			if at := strings.LastIndexByte(side.css, ' '); at >= 0 {
				color = side.css[at+1:]
			}
			colors["color"] = "<" + styleQualified(s.raw, "color") + ` rgb="FF` + strings.TrimPrefix(color, "#") + `"/>`
		} else if side.value.Style != nil && *side.value.Style != "" && *side.value.Style != "none" {
			// Freeze the evaluated default color, too. Without this, an added
			// edge with no color can reopen with a different default color.
			hasSourceColor := false
			for _, child := range s.children {
				if child.name.Space == s.name.Space && child.name.Local == "color" {
					hasSourceColor = true
					break
				}
			}
			if at := strings.LastIndexByte(side.css, ' '); !hasSourceColor && at >= 0 {
				colors["color"] = "<" + styleQualified(s.raw, "color") + ` rgb="FF` + strings.TrimPrefix(side.css[at+1:], "#") + `"/>`
			}
		}
		value, err := rewriteStyleFragment(s, attrs, colors, nil)
		if err != nil {
			return 0, err
		}
		props[side.name] = value
	}
	raw, err := rewriteStyleFragment(f, nil, props, []string{"start", "end", "left", "right", "top", "bottom", "diagonal", "vertical", "horizontal", "extLst"})
	if err != nil {
		return 0, err
	}
	id, err := p.doc.appendRecord("borders", raw)
	if err != nil {
		return 0, err
	}
	if err := p.rememberComponent(key, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (p *xlsxStylePalette) numberID(number workbook.DifferentialNumberFormat) (int, error) {
	if strings.EqualFold(strings.TrimSpace(number.Code), "General") {
		return 0, nil
	}
	if id, ok := p.numberCodes[number.Code]; ok {
		return id, nil
	}
	for p.nextNumber < len(p.usedNumbers) && p.usedNumbers[p.nextNumber] {
		p.nextNumber++
	}
	if p.nextNumber >= len(p.usedNumbers) {
		return 0, errors.New("XLSX数字格式编号已用尽")
	}
	id := p.nextNumber
	w := p.doc.wrapper("numFmts")
	q := styleQualified(w.head[strings.LastIndex(w.head, "<"):], "numFmt")
	if _, err := p.doc.appendRecord("numFmts", "<"+q+` numFmtId="`+strconv.Itoa(id)+`" formatCode="`+xmlAttr(number.Code)+`"/>`); err != nil {
		return 0, err
	}
	if err := p.doc.reserve(len(number.Code)*2 + 192); err != nil {
		return 0, err
	}
	p.usedNumbers[id] = true
	p.definedNumbers[id] = true
	p.numberCodes[number.Code] = id
	p.numberByID[id] = number.Code
	return id, nil
}

func (p *xlsxStylePalette) alignment(base *styleXMLFragment, diff workbook.DifferentialStyle) (string, error) {
	source, err := p.effective(base, "applyAlignment")
	if err != nil {
		return "", err
	}
	raw := "<" + styleQualified(source.raw, "alignment") + "/>"
	section := "cellXfs"
	if source != base {
		section = "cellStyleXfs"
	}
	wrapper := p.doc.wrapper(section).within(source.raw[:source.span.headEnd])
	for _, child := range source.children {
		if child.name.Space == source.name.Space && child.name.Local == "alignment" {
			raw = source.raw[child.span.start:child.span.end]
			break
		}
	}
	f, err := parseStyleFragment(p.ctx, wrapper, raw)
	if err != nil {
		return "", err
	}
	attrs := make(map[string]string)
	if diff.Horizontal != nil {
		attrs["horizontal"] = *diff.Horizontal
	}
	if diff.Vertical != nil {
		attrs["vertical"] = *diff.Vertical
	}
	if diff.Wrap != nil {
		v := "0"
		if *diff.Wrap {
			v = "1"
		}
		attrs["wrapText"] = v
	}
	if diff.Rotation != nil {
		attrs["textRotation"] = strconv.Itoa(*diff.Rotation)
	}
	if source != base {
		for k, v := range f.scope {
			if k != "xmlns:xml" {
				attrs[k] = v
			}
		}
	}
	return rewriteStyleFragment(f, attrs, nil, nil)
}

func (p *xlsxStylePalette) write(out io.Writer) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if p.doc == nil {
		_, err := io.WriteString(styleContextWriter{p.ctx, out}, p.raw)
		return err
	}
	return p.doc.write(p.ctx, out)
}
