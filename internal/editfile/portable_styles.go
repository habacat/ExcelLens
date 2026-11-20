package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// A copied style is expressed through its components rather than through the
// indices of its source stylesheet. The destination keeps its own style IDs.
type clipXF struct {
	font, fill, border, number, parent int
	align, protect                     string
	inherit                            map[string]bool
}
type clipStyleSection struct {
	open, tagEnd, end, close int
	nodes                    []string
	additions                []string
	index                    map[string]int
}
type clipStyleBook struct {
	raw          []byte
	sections     map[string]*clipStyleSection
	parents, xfs []clipXF
	codes        map[int]string
	formats      map[string]int
	maxFormat    int
}

func readClipStyleBook(ctx context.Context, raw []byte) (*clipStyleBook, error) {
	seeded, err := seedEntryFormats(ctx, raw)
	if err != nil {
		return nil, err
	}
	b := &clipStyleBook{raw: seeded.data, sections: map[string]*clipStyleSection{}, codes: map[int]string{}, formats: map[string]int{}, maxFormat: 163}
	var section string
	var xf clipXF
	var node strings.Builder
	depth := 0
	err = walkXML(ctx, b.raw, func(e xmlEvent) error {
		level := len(e.stack)
		switch t := e.token.(type) {
		case xml.StartElement:
			if level == 2 {
				section = e.local()
				b.sections[section] = &clipStyleSection{open: e.from, tagEnd: e.to, index: map[string]int{}}
			}
			if level == 3 && section == "numFmts" && e.local() == "numFmt" {
				id, err := strconv.Atoi(attribute(t, "", "numFmtId"))
				if err != nil || id < 0 || id > 65535 {
					return ErrXLSX
				}
				code := attribute(t, "", "formatCode")
				b.codes[id] = code
				b.formats[code] = id
				b.maxFormat = max(b.maxFormat, id)
			}
			if level == 3 && (section == "cellXfs" || section == "cellStyleXfs") {
				id := func(key string) int { v, _ := strconv.Atoi(attribute(t, "", key)); return v }
				xf = clipXF{font: id("fontId"), fill: id("fillId"), border: id("borderId"), number: id("numFmtId"), parent: id("xfId"), inherit: map[string]bool{}}
				for _, name := range []string{"Font", "Fill", "Border", "NumberFormat", "Alignment", "Protection"} {
					v := attribute(t, "", "apply"+name)
					xf.inherit[name] = v == "0" || v == "false"
				}
			}
			component := level == 3 && (section == "fonts" || section == "fills" || section == "borders") || level == 4 && (section == "cellXfs" || section == "cellStyleXfs") && (e.local() == "alignment" || e.local() == "protection")
			if component {
				depth = level
				node.Reset()
			}
			if depth != 0 && t.Name.Space == mainNS {
				node.WriteString("<" + e.local())
				if level == depth {
					node.WriteString(` xmlns="` + mainNS + `"`)
				}
				for _, a := range t.Attr {
					if a.Name.Space == "" && !nsDeclaration(a) {
						fmt.Fprintf(&node, ` %s="%s"`, a.Name.Local, escapedXML(a.Value))
					}
				}
				node.WriteByte('>')
			}
		case xml.EndElement:
			if depth != 0 && t.Name.Space == mainNS {
				node.WriteString("</" + e.local() + ">")
			}
			if depth != 0 && depth == level {
				value := node.String()
				if level == 3 {
					s := b.sections[section]
					s.index[value] = len(s.nodes)
					s.nodes = append(s.nodes, value)
				} else if e.local() == "alignment" {
					xf.align = value
				} else {
					xf.protect = value
				}
				depth = 0
			}
			if level == 3 {
				if section == "cellStyleXfs" {
					b.parents = append(b.parents, xf)
				}
				if section == "cellXfs" {
					b.xfs = append(b.xfs, xf)
				}
			}
			if level == 2 {
				s := b.sections[section]
				s.end, s.close = e.to, e.from
			}
		}
		return nil
	})
	return b, err
}
func (b *clipStyleBook) style(v editdoc.Value) (editdoc.PortableStyle, error) {
	var s editdoc.PortableStyle
	index := func(key string) (clipXF, error) {
		if key == "" {
			key = "0"
		}
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(b.xfs) {
			return clipXF{}, ErrXLSX
		}
		x := b.xfs[i]
		if x.parent >= 0 && x.parent < len(b.parents) {
			p := b.parents[x.parent]
			if x.inherit["Font"] {
				x.font = p.font
			}
			if x.inherit["Fill"] {
				x.fill = p.fill
			}
			if x.inherit["Border"] {
				x.border = p.border
			}
			if x.inherit["NumberFormat"] {
				x.number = p.number
			}
			if x.inherit["Alignment"] {
				x.align = p.align
			}
			if x.inherit["Protection"] {
				x.protect = p.protect
			}
		}
		return x, nil
	}
	x, err := index(v.Style)
	if err != nil {
		return s, err
	}
	n, err := index(v.NumberFormatStyle())
	if err != nil {
		return s, err
	}
	format := n.number
	if v.BuiltinFormat != 0 {
		format = v.BuiltinFormat
	}
	if code, ok := b.codes[format]; ok {
		s.Format = code
	} else if format < 164 {
		s.Builtin = format
	} else {
		return s, ErrXLSX
	}
	for _, c := range []struct {
		part string
		idx  int
		dest *string
	}{{"fonts", x.font, &s.Font}, {"fills", x.fill, &s.Fill}, {"borders", x.border, &s.Border}} {
		list := b.sections[c.part].nodes
		if c.idx < 0 || c.idx >= len(list) {
			return s, ErrXLSX
		}
		*c.dest = list[c.idx]
	}
	s.Alignment, s.Protection = x.align, x.protect
	return s, nil
}
func (s *clipStyleSection) intern(node string) int {
	if i, ok := s.index[node]; ok {
		return i
	}
	i := len(s.nodes) + len(s.additions)
	s.index[node] = i
	s.additions = append(s.additions, node)
	return i
}

func (p *xlsxPackage) importClipStyles(ctx context.Context, styles []editdoc.PortableStyle, updates map[string][]byte) ([]string, error) {
	var original []byte
	var err error
	if p.files[entryStylePart] != nil {
		original, err = p.readPart(ctx, entryStylePart)
		if err != nil {
			return nil, err
		}
	}
	b, err := readClipStyleBook(ctx, original)
	if err != nil {
		return nil, err
	}
	existing := map[editdoc.PortableStyle]int{}
	for i := range b.xfs {
		st, err := b.style(editdoc.Value{Style: strconv.Itoa(i)})
		if err == nil {
			if _, ok := existing[st]; !ok {
				existing[st] = i
			}
		}
	}
	ids := make([]string, len(styles))
	var numbers []string
	for i, s := range styles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id, ok := existing[s]; ok {
			ids[i] = strconv.Itoa(id)
			continue
		}
		num := s.Builtin
		if s.Format != "" {
			var ok bool
			num, ok = b.formats[s.Format]
			if !ok {
				b.maxFormat++
				if b.maxFormat > 65535 {
					return nil, ErrXLSX
				}
				num = b.maxFormat
				b.formats[s.Format] = num
				numbers = append(numbers, fmt.Sprintf(`<numFmt xmlns="%s" numFmtId="%d" formatCode="%s"/>`, mainNS, num, escapedXML(s.Format)))
			}
		}
		xf := fmt.Sprintf(`<xf xmlns="%s" numFmtId="%d" fontId="%d" fillId="%d" borderId="%d" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyNumberFormat="1" applyAlignment="1" applyProtection="1">%s%s</xf>`, mainNS, num, b.sections["fonts"].intern(s.Font), b.sections["fills"].intern(s.Fill), b.sections["borders"].intern(s.Border), s.Alignment, s.Protection)
		xs := b.sections["cellXfs"]
		id := len(b.xfs) + len(xs.additions)
		if id >= min(65490, formats.EditXLSXStyleIndex+1) {
			return nil, ErrXLSX
		}
		xs.additions = append(xs.additions, xf)
		existing[s] = id
		ids[i] = strconv.Itoa(id)
	}
	var patches []xmlPatch
	if len(numbers) > 0 && b.sections["numFmts"] == nil {
		off := b.sections["fonts"].open
		patches = append(patches, xmlPatch{off, off, fmt.Sprintf(`<numFmts xmlns="%s" count="%d">%s</numFmts>`, mainNS, len(numbers), strings.Join(numbers, ""))})
	}
	for _, name := range []string{"numFmts", "fonts", "fills", "borders", "cellXfs"} {
		s := b.sections[name]
		if s == nil {
			continue
		}
		additions := s.additions
		count := len(s.nodes)
		if name == "cellXfs" {
			count = len(b.xfs)
		}
		if name == "numFmts" {
			additions = numbers
			count = len(b.codes)
		}
		if len(additions) == 0 {
			continue
		}
		ps, err := setXMLAttribute(b.raw, s.open, s.tagEnd, "count", strconv.Itoa(count+len(additions)))
		if err != nil {
			return nil, err
		}
		patches = append(patches, ps...)
		child := strings.Join(additions, "")
		if s.end == s.tagEnd {
			patches = append(patches, xmlPatch{s.tagEnd - 2, s.tagEnd, ">" + child + closingTag(b.raw[s.open:s.tagEnd])})
		} else {
			patches = append(patches, xmlPatch{s.close, s.close, child})
		}
	}
	if len(patches) == 0 && p.files[entryStylePart] != nil {
		return ids, nil
	}
	data, err := applyXML(ctx, b.raw, patches)
	if err != nil {
		return nil, err
	}
	if len(data) > formats.EditXLSXPartBytes {
		return nil, ErrXLSX
	}
	if p.files[entryStylePart] == nil {
		extra := map[string][]xmlPatch{}
		_, err = p.installEntryStyles(ctx, []xmlPatch{{0, 0, string(data)}}, extra)
		if err != nil {
			return nil, err
		}
		for part, ps := range extra {
			old, err := p.readPart(ctx, part)
			if err != nil {
				return nil, err
			}
			updates[part], err = applyXML(ctx, old, ps)
			if err != nil {
				return nil, err
			}
		}
	}
	updates[entryStylePart] = slices.Clone(data)
	return ids, nil
}
