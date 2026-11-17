package workbook

import (
	"encoding/xml"
	"io"
	"strings"
)

type workbookSheetXML struct {
	Name  string
	RelID string
	State string
}

type workbookXML struct {
	Date1904  string `xml:"workbookPr>date1904,attr"`
	BookViews struct {
		Views []struct {
			ActiveTab int `xml:"activeTab,attr"`
		} `xml:"workbookView"`
	} `xml:"bookViews"`
	Sheets    []workbookSheetXML `xml:"sheets>sheet"`
	ActiveTab int
}

func (w *workbookXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "workbookPr":
				for _, a := range t.Attr {
					if a.Name.Local == "date1904" {
						w.Date1904 = a.Value
					}
				}
			case "workbookView":
				for _, a := range t.Attr {
					if a.Name.Local == "activeTab" {
						v := 0
						for _, c := range a.Value {
							if c < '0' || c > '9' {
								break
							}
							v = v*10 + int(c-'0')
						}
						w.ActiveTab = v
					}
				}
			case "sheet":
				var s struct {
					Name  string
					RelID string
					State string
				}
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "name":
						s.Name = a.Value
					case "id":
						s.RelID = a.Value
					case "state":
						s.State = a.Value
					}
				}
				w.Sheets = append(w.Sheets, workbookSheetXML{Name: s.Name, RelID: s.RelID, State: s.State})
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

type relationshipsXML struct {
	Relationships []relationship `xml:"Relationship"`
}

type relationship struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type sharedStringItem struct {
	Text string
}

func (s *sharedStringItem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	value, err := decodeRichText(d, start)
	if err != nil {
		return err
	}
	s.Text = value
	return nil
}

// OOXML rich text consists of a direct <t> or ordered <r><t> fragments.
// <rPh><t> phonetic annotations are NOT cell content. Mixing them into the
// visible value corrupts search/filter, copy and audit evidence.
func decodeRichText(d *xml.Decoder, start xml.StartElement) (string, error) {
	value, _, err := decodeRichTextContent(d, start, nil)
	return value, err
}

func decodeRichTextContent(d *xml.Decoder, start xml.StartElement, observe func(string, *richFontXML, bool)) (string, bool, error) {
	var result strings.Builder
	parents := []string{start.Name.Local}
	var font *richFontXML
	hasRuns := false
	for len(parents) > 0 {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return "", false, io.ErrUnexpectedEOF
			}
			return "", false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parent := parents[len(parents)-1]
			if len(parents) == 1 && t.Name.Local == "r" {
				font = nil
				hasRuns = true
			}
			if observe != nil && len(parents) == 2 && parent == "r" && t.Name.Local == "rPr" {
				previous := font
				font = new(richFontXML)
				if err := d.DecodeElement(font, &t); err != nil {
					return "", false, err
				}
				if previous != nil {
					font.unsupported = true
				}
				continue
			}
			validText := t.Name.Local == "t" && (len(parents) == 1 && parent == start.Name.Local || len(parents) == 2 && parent == "r")
			if validText {
				var value string
				if err := d.DecodeElement(&value, &t); err != nil {
					return "", false, err
				}
				result.WriteString(value)
				if observe != nil && value != "" {
					if parent == "r" {
						observe(value, font, true)
					} else {
						observe(value, nil, false)
					}
				}
			} else if len(parents) == 1 && t.Name.Local == "r" {
				parents = append(parents, t.Name.Local)
			} else if err := d.Skip(); err != nil {
				return "", false, err
			}
		case xml.EndElement:
			parents = parents[:len(parents)-1]
		}
	}
	return result.String(), hasRuns, nil
}

type xmlRow struct {
	Number int       `xml:"-"`
	Cells  []xmlCell `xml:"c"`
}

func (r *xmlRow) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Local == "r" {
			for _, c := range a.Value {
				if c < '0' || c > '9' {
					break
				}
				r.Number = r.Number*10 + int(c-'0')
			}
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "c" {
				var c xmlCell
				if err := d.DecodeElement(&c, &t); err != nil {
					return err
				}
				r.Cells = append(r.Cells, c)
			} else if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

type formulaXML struct {
	Text        string `xml:",chardata"`
	Type        string `xml:"t,attr"`
	Ref         string `xml:"ref,attr"`
	SharedIndex string `xml:"si,attr"`
}

type xmlCell struct {
	Ref          string
	Type         string
	Style        int
	StyleDefined bool
	Value        string
	HasValue     bool
	Formula      *formulaXML
	Inline       string
	RichText     *RichText
	richOwner    *Workbook
}

func (c *xmlCell) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "r":
			c.Ref = a.Value
		case "t":
			c.Type = a.Value
		case "s":
			c.StyleDefined = true
			v := 0
			for _, ch := range a.Value {
				if ch < '0' || ch > '9' {
					break
				}
				v = v*10 + int(ch-'0')
			}
			c.Style = v
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "f":
				var f formulaXML
				if err := d.DecodeElement(&f, &t); err != nil {
					return err
				}
				c.Formula = &f
			case "v":
				c.HasValue = true
				if err := d.DecodeElement(&c.Value, &t); err != nil {
					return err
				}
			case "is":
				var v string
				var err error
				if c.richOwner != nil {
					v, c.RichText, err = c.richOwner.readRichTextStyled(d, t)
				} else {
					v, err = readInlineText(d, t)
				}
				if err != nil {
					return err
				}
				c.Inline = v
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

func readInlineText(d *xml.Decoder, start xml.StartElement) (string, error) {
	return decodeRichText(d, start)
}
