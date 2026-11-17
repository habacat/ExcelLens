package workbook

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"excellens/internal/formats"
)

// RichText is presentation metadata over the unchanged cell text. End offsets
// count UTF-16 code units so the browser can slice without splitting a rune.
// A missing font property inherits the cell font; explicit false/none clears it.
type RichText struct {
	Runs   []RichTextRun `json:"runs,omitempty"`
	Notice string        `json:"notice,omitempty"`
}

type RichTextRun struct {
	End  int      `json:"end"`
	Font RichFont `json:"font"`
}

type RichFont struct {
	Name      string  `json:"name,omitempty"`
	Size      float64 `json:"size,omitempty"`
	Color     string  `json:"color,omitempty"`
	AutoColor bool    `json:"autoColor,omitempty"`
	Bold      *bool   `json:"bold,omitempty"`
	Italic    *bool   `json:"italic,omitempty"`
	Strike    *bool   `json:"strike,omitempty"`
	Underline string  `json:"underline,omitempty"`
	Vertical  string  `json:"vertical,omitempty"`
}

type richFontXML struct {
	Name, Size, Vertical, Underline *valXML
	Color                           *styleColorXML
	Bold, Italic, Strike            *flagXML
	unsupported                     bool
	cellFont                        bool // DXF <font> uses name; rich runs use rFont.
}

func (f *richFontXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var seen uint16
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if f.cellFont && token.Name.Space != start.Name.Space {
				f.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			var bit uint16
			switch token.Name.Local {
			case "rFont", "name":
				bit = 1
			case "sz":
				bit = 2
			case "vertAlign":
				bit = 4
			case "u":
				bit = 8
			case "b":
				bit = 16
			case "i":
				bit = 32
			case "strike":
				bit = 64
			case "color":
				bit = 128
			}
			if seen&bit != 0 {
				f.unsupported = true
			}
			seen |= bit
			switch token.Name.Local {
			case "rFont", "name", "sz", "vertAlign", "u":
				for _, attr := range token.Attr {
					if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
						continue
					}
					if attr.Name.Space != "" || attr.Name.Local != "val" {
						f.unsupported = true
					}
				}
				if token.Name.Local == "name" && !f.cellFont || token.Name.Local == "rFont" && f.cellFont {
					f.unsupported = true
					if err := d.Skip(); err != nil {
						return err
					}
					continue
				}
				value := new(valXML)
				if err := d.DecodeElement(value, &token); err != nil {
					return err
				}
				switch token.Name.Local {
				case "rFont", "name":
					f.Name = value
				case "sz":
					f.Size = value
				case "vertAlign":
					f.Vertical = value
				case "u":
					f.Underline = value
				}
			case "b", "i", "strike":
				for _, attr := range token.Attr {
					if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
						continue
					}
					if attr.Name.Space != "" || attr.Name.Local != "val" {
						f.unsupported = true
					}
				}
				value := new(flagXML)
				if err := d.DecodeElement(value, &token); err != nil {
					return err
				}
				switch token.Name.Local {
				case "b":
					f.Bold = value
				case "i":
					f.Italic = value
				case "strike":
					f.Strike = value
				}
			case "color":
				f.Color = new(styleColorXML)
				for _, attr := range token.Attr {
					if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
						continue
					}
					if attr.Name.Space != "" {
						f.unsupported = true
						continue
					}
					switch attr.Name.Local {
					case "rgb":
						f.Color.RGB = attr.Value
					case "auto":
						f.Color.Auto = attr.Value
					case "theme", "indexed":
						value, err := strconv.Atoi(attr.Value)
						if err != nil {
							f.unsupported = true
						} else if attr.Name.Local == "theme" {
							f.Color.Theme = &value
						} else {
							f.Color.Indexed = &value
						}
					case "tint":
						value, err := strconv.ParseFloat(attr.Value, 64)
						if err != nil {
							f.unsupported = true
							value = math.NaN()
						}
						f.Color.Tint = value
					default:
						f.unsupported = true
					}
				}
				if err := d.Skip(); err != nil {
					return err
				}
			case "charset", "family":
				// Encoding is already Unicode. An explicit font face determines
				// the browser family; these classification hints add no CSS.
				if err := d.Skip(); err != nil {
					return err
				}
			default:
				// Keep the text, but do not pretend unknown effects or theme
				// font substitutions have been faithfully rendered.
				f.unsupported = true
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if token.Name == start.Name {
				return nil
			}
		}
	}
}

func richFlag(value *flagXML) (*bool, bool) {
	if value == nil {
		return nil, true
	}
	switch strings.ToLower(value.Val) {
	case "", "1", "true":
		v := true
		return &v, true
	case "0", "false":
		v := false
		return &v, true
	default:
		return nil, false
	}
}

func (w *Workbook) resolveRichFont(source *richFontXML) (RichFont, bool) {
	if source == nil {
		return RichFont{}, true
	}
	out, complete := RichFont{}, !source.unsupported
	var valid bool
	out.Bold, valid = richFlag(source.Bold)
	complete = complete && valid
	out.Italic, valid = richFlag(source.Italic)
	complete = complete && valid
	out.Strike, valid = richFlag(source.Strike)
	complete = complete && valid
	if source.Name != nil {
		name := source.Name.Val
		if name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 80 && !strings.ContainsAny(name, "\x00\r\n") {
			out.Name = name
		} else {
			complete = false
		}
	}
	if source.Size != nil {
		size, err := strconv.ParseFloat(source.Size.Val, 64)
		if err == nil && !math.IsNaN(size) && size >= 1 && size <= 409 {
			out.Size = size
		} else {
			complete = false
		}
	}
	if source.Color != nil {
		if math.IsNaN(source.Color.Tint) || math.IsInf(source.Color.Tint, 0) || source.Color.Tint < -1 || source.Color.Tint > 1 {
			complete = false
		} else {
			out.AutoColor = source.Color.Auto == "1" || strings.EqualFold(source.Color.Auto, "true")
			if !out.AutoColor {
				indexed := w.IndexedColors
				if len(indexed) == 0 {
					indexed = defaultIndexed
				}
				out.Color = w.resolveColor(source.Color, indexed)
				if out.Color == "" {
					complete = false
				}
			}
		}
	}
	if source.Underline != nil {
		switch source.Underline.Val {
		case "":
			out.Underline = "single"
		case "none", "single", "double", "singleAccounting", "doubleAccounting":
			out.Underline = source.Underline.Val
		default:
			complete = false
		}
	}
	if source.Vertical != nil {
		switch source.Vertical.Val {
		case "baseline", "superscript", "subscript":
			out.Vertical = source.Vertical.Val
		default:
			complete = false
		}
	}
	return out, complete
}

func utf16TextLength(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

func sameOptionalFlag(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func sameRichFont(a, b RichFont) bool {
	return a.Name == b.Name && a.Size == b.Size && a.Color == b.Color && a.AutoColor == b.AutoColor && a.Underline == b.Underline && a.Vertical == b.Vertical && sameOptionalFlag(a.Bold, b.Bold) && sameOptionalFlag(a.Italic, b.Italic) && sameOptionalFlag(a.Strike, b.Strike)
}

// readRichTextStyled is opt-in. Ordinary values, indexes, scans and numeric
// interpretation keep using decodeRichText without allocating run metadata.
func (w *Workbook) readRichTextStyled(d *xml.Decoder, start xml.StartElement) (string, *RichText, error) {
	var runs []RichTextRun
	var notice string
	truncated := false
	offset := 0
	text, hasRuns, err := decodeRichTextContent(d, start, func(text string, font *richFontXML, inRun bool) {
		before := offset
		offset += utf16TextLength(text)
		if !inRun && len(runs) == 0 {
			return
		}
		if len(runs) == 0 && before > 0 {
			runs = append(runs, RichTextRun{End: before})
		}
		if truncated {
			return
		}
		resolved, complete := w.resolveRichFont(font)
		if !complete && notice == "" {
			notice = "部分富文本字体属性尚未还原，原文保留"
		}
		if len(runs) > 0 && sameRichFont(runs[len(runs)-1].Font, resolved) {
			runs[len(runs)-1].End = offset
			return
		}
		if len(runs) >= formats.MaxCellRichRuns {
			truncated = true
			notice = "富文本样式片段超过显示容量，剩余原文保留"
			return
		}
		runs = append(runs, RichTextRun{End: offset, Font: resolved})
	})
	if err != nil {
		return "", nil, err
	}
	if !hasRuns {
		return text, nil, nil
	}
	if len(runs) == 0 && offset > 0 {
		runs = []RichTextRun{{End: offset}}
	}
	if notice == "" && (len(runs) == 0 || len(runs) == 1 && sameRichFont(runs[0].Font, RichFont{})) {
		return text, nil, nil
	}
	return text, &RichText{Runs: runs, Notice: notice}, nil
}

// ValidateRichText guards persisted and transported metadata before it may
// split text. It does not build a rune-boundary map proportional to cell size.
func ValidateRichText(text string, value *RichText) error {
	if value == nil {
		return nil
	}
	if len(value.Runs) > formats.MaxCellRichRuns || len(value.Notice) > 1024 || !utf8.ValidString(text) || !utf8.ValidString(value.Notice) {
		return fmt.Errorf("富文本元数据超限或编码无效")
	}
	position, previous := 0, 0
	remaining := text
	for _, run := range value.Runs {
		if run.End <= previous {
			return fmt.Errorf("富文本片段顺序无效")
		}
		for position < run.End && remaining != "" {
			r, n := utf8.DecodeRuneInString(remaining)
			remaining = remaining[n:]
			position++
			if r > 0xffff {
				position++
			}
		}
		if position != run.End {
			return fmt.Errorf("富文本片段超出原文或拆开Unicode字符")
		}
		font := run.Font
		if utf8.RuneCountInString(font.Name) > 80 || !utf8.ValidString(font.Name) || strings.ContainsAny(font.Name, "\x00\r\n") || math.IsNaN(font.Size) || math.IsInf(font.Size, 0) || font.Size != 0 && (font.Size < 1 || font.Size > 409) || font.Color != "" && parseHexColor(font.Color) != font.Color {
			return fmt.Errorf("富文本字体属性无效")
		}
		switch font.Underline {
		case "", "none", "single", "double", "singleAccounting", "doubleAccounting":
		default:
			return fmt.Errorf("富文本下划线无效")
		}
		switch font.Vertical {
		case "", "baseline", "superscript", "subscript":
		default:
			return fmt.Errorf("富文本上下标无效")
		}
		previous = run.End
	}
	if value.Notice == "" && previous != utf16TextLength(text) {
		return fmt.Errorf("富文本片段未覆盖完整原文")
	}
	return nil
}
