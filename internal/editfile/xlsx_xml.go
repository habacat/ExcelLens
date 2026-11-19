package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
)

type xmlEvent struct {
	stack    []xml.Name
	token    xml.Token
	from, to int
}

func (e xmlEvent) local() string { return e.stack[len(e.stack)-1].Local }
func (e xmlEvent) parent() string {
	if len(e.stack) < 2 {
		return ""
	}
	return e.stack[len(e.stack)-2].Local
}

func walkXML(ctx context.Context, data []byte, visit func(xmlEvent) error) error {
	body := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	return walkXMLReader(ctx, bytes.NewReader(body), len(data)-len(body), visit)
}

func walkXMLReader(ctx context.Context, source io.Reader, skip int, visit func(xmlEvent) error) error {
	d := xml.NewDecoder(source)
	var stack []xml.Name
	roots := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		from := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF && len(stack) == 0 && roots == 1 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w：XML 不完整或无效", ErrXLSX)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				roots++
			}
			if roots != 1 || len(stack) >= 64 || len(t.Attr) > 128 {
				return ErrXLSX
			}
			for i, a := range t.Attr {
				for _, prior := range t.Attr[:i] {
					if prior.Name == a.Name {
						return ErrXLSX
					}
				}
			}
			stack = append(stack, t.Name)
		case xml.Directive:
			return ErrXLSX
		case xml.ProcInst:
			if t.Target != "xml" || from != 0 {
				return ErrXLSX
			}
			continue
		case xml.Comment:
			continue
		case xml.CharData:
			if len(stack) == 0 {
				if len(bytes.TrimSpace(t)) != 0 {
					return ErrXLSX
				}
				continue
			}
		}
		if err := visit(xmlEvent{stack, token, from + skip, int(d.InputOffset()) + skip}); err != nil {
			return err
		}
		if _, ok := token.(xml.EndElement); ok {
			stack = stack[:len(stack)-1]
		}
	}
}

func attribute(el xml.StartElement, space, name string) string {
	for _, a := range el.Attr {
		if a.Name.Space == space && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func nsDeclaration(a xml.Attr) bool {
	return a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns"
}

func mainAttributes(el xml.StartElement) bool {
	for _, a := range el.Attr {
		switch a.Name.Space {
		case "", "xmlns":
			continue
		case markupNS:
			if a.Name.Local == "Ignorable" {
				continue
			}
		case "http://www.w3.org/XML/1998/namespace":
			if a.Name.Local == "space" {
				continue
			}
		case "http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac":
			if a.Name.Local == "dyDescent" {
				continue
			}
		case revisionNS, "http://schemas.microsoft.com/office/spreadsheetml/2015/revision2":
			if a.Name.Local == "uid" {
				continue
			}
		}
		return false
	}
	return true
}
