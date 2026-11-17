package workbook

import (
	"encoding/xml"
	"math"
	"strconv"

	"excellens/internal/formats"
)

// A difference is separate from source style IDs. Absence inherits; explicit
// false, none and empty borders/fills override, including between priorities.
type DifferentialStyle struct {
	Font         RichFont                  `json:"font"`
	Background   *string                   `json:"background,omitempty"`
	Border       *DifferentialBorders      `json:"border,omitempty"`
	NumberFormat *DifferentialNumberFormat `json:"numberFormat,omitempty"`
	Horizontal   *string                   `json:"horizontal,omitempty"`
	Vertical     *string                   `json:"vertical,omitempty"`
	Wrap         *bool                     `json:"wrap,omitempty"`
	Rotation     *int                      `json:"rotation,omitempty"`
	Notice       string                    `json:"notice,omitempty"`
}

type DifferentialBorders struct {
	Top    *DifferentialBorderSide `json:"top,omitempty"`
	Bottom *DifferentialBorderSide `json:"bottom,omitempty"`
	Left   *DifferentialBorderSide `json:"left,omitempty"`
	Right  *DifferentialBorderSide `json:"right,omitempty"`
}

type DifferentialBorderSide struct {
	Style     *string `json:"style,omitempty"`
	Color     *string `json:"color,omitempty"`
	AutoColor bool    `json:"autoColor,omitempty"`
}

type DifferentialNumberFormat struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
}

type differentialStyleXML struct {
	Font         *richFontXML
	Fill         *differentialFillXML
	Border       *differentialBordersXML
	NumberFormat *struct {
		ID   int    `xml:"numFmtId,attr"`
		Code string `xml:"formatCode,attr"`
	}
	Alignment []xml.Attr
	Notice    string
}

type differentialBordersXML struct {
	Top, Bottom, Left, Right *differentialBorderXML
	unsupported              bool
}
type differentialBorderXML struct {
	Style       *string
	Color       *differentialColorXML
	unsupported bool
}

type differentialColorXML struct {
	color       styleColorXML
	unsupported bool
}
type differentialFillXML struct {
	pattern     *differentialPatternXML
	unsupported bool
}
type differentialPatternXML struct {
	kind                   string
	foreground, background *differentialColorXML
	unsupported            bool
}

func (c *differentialColorXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	seen := make(map[string]bool, 5)
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			c.unsupported = true
			continue
		}
		if seen[a.Name.Local] {
			c.unsupported = true
		}
		seen[a.Name.Local] = true
		switch a.Name.Local {
		case "rgb":
			c.color.RGB = a.Value
		case "auto":
			c.color.Auto = a.Value
			if _, valid := sourceBool([]byte(a.Value)); !valid {
				c.unsupported = true
			}
		case "theme", "indexed":
			v, err := strconv.Atoi(a.Value)
			if err != nil {
				c.unsupported = true
			} else if a.Name.Local == "theme" {
				c.color.Theme = &v
			} else {
				c.color.Indexed = &v
			}
		case "tint":
			v, err := strconv.ParseFloat(a.Value, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 1 {
				c.unsupported = true
			} else {
				c.color.Tint = v
			}
		default:
			c.unsupported = true
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			c.unsupported = true
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

func (f *differentialFillXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
			f.unsupported = true
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Local != "patternFill" || t.Name.Space != start.Name.Space {
				f.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			if f.pattern != nil {
				f.unsupported = true
			}
			f.pattern = &differentialPatternXML{}
			if err := d.DecodeElement(f.pattern, &t); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

func (p *differentialPatternXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			p.unsupported = true
			continue
		}
		if a.Name.Local == "patternType" {
			p.kind = a.Value
		} else {
			p.unsupported = true
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			var target **differentialColorXML
			if t.Name.Space != start.Name.Space {
				p.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			switch t.Name.Local {
			case "fgColor":
				target = &p.foreground
			case "bgColor":
				target = &p.background
			}
			if target == nil {
				p.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			if *target != nil {
				p.unsupported = true
			}
			*target = &differentialColorXML{}
			if err := d.DecodeElement(*target, &t); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

type differentialStylesXML struct {
	Items     []differentialStyleXML
	Notice    string
	seen      bool
	namespace string
}

func (list *differentialStylesXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if list.seen {
		list.Notice = "条件样式目录重复，无法可靠绑定编号"
	} else {
		list.namespace = start.Name.Space
	}
	list.seen = true
	begin := d.InputOffset()
	count := 0
	declared := -1
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			list.Notice = "条件样式目录含未知属性"
			continue
		}
		if a.Name.Local == "count" {
			n, err := strconv.Atoi(a.Value)
			if err != nil || n < 0 {
				list.Notice = "条件样式数量声明无效"
			} else {
				declared = n
			}
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Local != "dxf" || t.Name.Space != start.Name.Space {
				list.Notice = "条件样式含未知子项，未完整还原"
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			count++
			if count > formats.DifferentialStyles || d.InputOffset()-begin > formats.ConditionalMetadataBytes {
				list.Notice = "条件样式超过读取容量，未完整还原"
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			var item differentialStyleXML
			if err := d.DecodeElement(&item, &t); err != nil {
				return err
			}
			if d.InputOffset()-begin > formats.ConditionalMetadataBytes {
				item = differentialStyleXML{Notice: "条件样式超过读取容量，未完整还原"}
				list.Notice = item.Notice
			}
			list.Items = append(list.Items, item)
		case xml.EndElement:
			if t.Name == start.Name {
				if declared >= 0 && declared != count {
					list.Notice = "条件样式声明数量与实际记录不符"
				}
				return nil
			}
		}
	}
}

func (s *differentialStyleXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
			s.Notice = "条件样式含未知属性，未完整还原"
		}
	}
	seen := make(map[string]bool, 6)
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Space != start.Name.Space {
				s.Notice = "条件样式含未知命名空间的属性"
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			if seen[t.Name.Local] {
				s.Notice = "条件样式存在重复属性，未完整还原"
			}
			seen[t.Name.Local] = true
			switch t.Name.Local {
			case "font":
				s.Font = &richFontXML{cellFont: true}
				if err := d.DecodeElement(s.Font, &t); err != nil {
					return err
				}
			case "fill":
				s.Fill = &differentialFillXML{}
				if err := d.DecodeElement(s.Fill, &t); err != nil {
					return err
				}
			case "border":
				s.Border = &differentialBordersXML{}
				if err := d.DecodeElement(s.Border, &t); err != nil {
					return err
				}
			case "numFmt":
				for _, a := range t.Attr {
					if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
						continue
					}
					if a.Name.Space != "" || a.Name.Local != "numFmtId" && a.Name.Local != "formatCode" {
						s.Notice = "条件数字格式含未知属性"
					}
				}
				s.NumberFormat = &struct {
					ID   int    `xml:"numFmtId,attr"`
					Code string `xml:"formatCode,attr"`
				}{}
				if err := d.DecodeElement(s.NumberFormat, &t); err != nil {
					return err
				}
			case "alignment":
				s.Alignment = append([]xml.Attr{}, t.Attr...)
				if err := d.Skip(); err != nil {
					return err
				}
			default:
				s.Notice = "部分条件样式属性尚未还原"
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

func (s *differentialBordersXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
			s.unsupported = true
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Space != start.Name.Space {
				s.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			var target **differentialBorderXML
			switch t.Name.Local {
			case "top":
				target = &s.Top
			case "bottom":
				target = &s.Bottom
			case "left":
				target = &s.Left
			case "right":
				target = &s.Right
			}
			if target == nil {
				s.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			if *target != nil {
				s.unsupported = true
			}
			*target = &differentialBorderXML{}
			if err := d.DecodeElement(*target, &t); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

func (s *differentialBorderXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			s.unsupported = true
			continue
		}
		if a.Name.Local == "style" {
			v := a.Value
			s.Style = &v
		} else {
			s.unsupported = true
		}
	}
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Local == "color" && t.Name.Space == start.Name.Space {
				if s.Color != nil {
					s.unsupported = true
				}
				s.Color = &differentialColorXML{}
				if err := d.DecodeElement(s.Color, &t); err != nil {
					return err
				}
			} else {
				s.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}

func (w *Workbook) resolveDifferentialStyle(s differentialStyleXML) DifferentialStyle {
	out := DifferentialStyle{Notice: s.Notice}
	notice := func() {
		if out.Notice == "" {
			out.Notice = "部分条件样式属性无效或尚未还原"
		}
	}
	if s.Font != nil {
		var complete bool
		out.Font, complete = w.resolveRichFont(s.Font)
		if !complete {
			notice()
		}
	}
	if s.Fill != nil {
		p := s.Fill.pattern
		if s.Fill.unsupported || p == nil {
			notice()
		} else {
			if p.unsupported {
				notice()
			}
			switch p.kind {
			case "none":
				v := ""
				out.Background = &v
			case "solid":
				source := p.foreground
				if source == nil {
					source = p.background
				}
				v, auto, valid := w.resolveDifferentialColor(source)
				if !valid || auto {
					notice()
				} else {
					out.Background = &v
				}
			default:
				notice()
			}
		}
	}
	if s.Border != nil {
		out.Border = &DifferentialBorders{}
		if s.Border.unsupported {
			notice()
		}
		for _, item := range []struct {
			source *differentialBorderXML
			target **DifferentialBorderSide
		}{{s.Border.Top, &out.Border.Top}, {s.Border.Bottom, &out.Border.Bottom}, {s.Border.Left, &out.Border.Left}, {s.Border.Right, &out.Border.Right}} {
			if item.source == nil {
				continue
			}
			src := item.source
			v := &DifferentialBorderSide{Style: src.Style}
			if src.unsupported {
				notice()
			}
			if src.Style != nil && *src.Style != "" && *src.Style != "none" && borderCSS(styleSideXML{Style: *src.Style}, "#000000") == "" {
				notice()
			}
			if src.Color != nil {
				color, auto, valid := w.resolveDifferentialColor(src.Color)
				v.AutoColor = auto
				if !valid {
					notice()
				} else if !auto {
					v.Color = &color
				}
			}
			if src.Style == nil && src.Color == nil {
				empty := ""
				v.Style = &empty
			}
			*item.target = v
		}
	}
	if s.NumberFormat != nil {
		f := s.NumberFormat
		if f.ID < 0 || f.ID > 65535 || len(f.Code) > formats.ConditionalFormulaBytes || f.Code == "" {
			notice()
		} else {
			out.NumberFormat = &DifferentialNumberFormat{ID: f.ID, Code: f.Code}
		}
	}
	for _, a := range s.Alignment {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			notice()
			continue
		}
		value := a.Value
		switch a.Name.Local {
		case "horizontal":
			switch value {
			case "general", "left", "center", "right", "fill", "justify", "centerContinuous", "distributed":
				out.Horizontal = &value
			default:
				notice()
			}
		case "vertical":
			switch value {
			case "top", "center", "bottom", "justify", "distributed":
				out.Vertical = &value
			default:
				notice()
			}
		case "wrapText":
			v, valid := sourceBool([]byte(value))
			if valid {
				out.Wrap = &v
			} else {
				notice()
			}
		case "textRotation":
			v, err := strconv.Atoi(value)
			if err == nil && v >= 0 && v <= 90 {
				out.Rotation = &v
			} else {
				notice()
			}
		default:
			notice()
		}
	}
	return out
}

func (s DifferentialStyle) empty() bool {
	return sameRichFont(s.Font, RichFont{}) && s.Background == nil && s.Border.empty() && s.NumberFormat == nil && s.Horizontal == nil && s.Vertical == nil && s.Wrap == nil && s.Rotation == nil
}

func (b *DifferentialBorders) empty() bool {
	return b == nil || b.Top == nil && b.Bottom == nil && b.Left == nil && b.Right == nil
}

func (w *Workbook) resolveDifferentialColor(source *differentialColorXML) (string, bool, bool) {
	if source == nil || source.unsupported {
		return "", false, false
	}
	font, valid := w.resolveRichFont(&richFontXML{Color: &source.color})
	return font.Color, font.AutoColor, valid
}
