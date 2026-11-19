package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
)

// Remove the independently bound form-button shapes from the XML used for
// note-coordinate validation only. The output package retains the original VML.
func stripButtonShapes(ctx context.Context, data []byte, count int, macro bool) ([]byte, error) {
	var cuts []xmlPatch
	types, ids := map[string]bool{}, map[string]bool{}
	start, clients, buttons := -1, 0, 0
	shape := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 2 && t.Name.Space == vmlNS {
				switch e.local() {
				case "shapetype":
					if attribute(t, vmlOfficeNS, "spt") == "201" {
						id := attribute(t, "", "id")
						if id == "" || types[id] {
							return ErrXLSX
						}
						types[id] = true
						start = e.from
					}
				case "shape":
					ref, ok := strings.CutPrefix(attribute(t, "", "type"), "#")
					if ok && types[ref] {
						id := attribute(t, "", "id")
						if id == "" || ids[id] {
							return ErrXLSX
						}
						ids[id] = true
						start, clients, shape = e.from, 0, true
					}
				}
			}
			if start >= 0 {
				for _, a := range t.Attr {
					if a.Name.Space == relationNS || a.Name.Local == "href" || a.Name.Local == "src" {
						return ErrXLSX
					}
				}
				if t.Name.Space == vmlExcelNS {
					switch e.local() {
					case "ClientData":
						if !shape || len(e.stack) != 3 || attribute(t, "", "ObjectType") != "Button" {
							return ErrXLSX
						}
						clients++
					case "FmlaMacro":
						if !macro {
							return ErrXLSX
						}
					case "FmlaLink", "FmlaRange", "FmlaGroup":
						return ErrXLSX
					}
				}
			}
		case xml.EndElement:
			if len(e.stack) == 2 && start >= 0 {
				if shape {
					if clients != 1 {
						return ErrXLSX
					}
					buttons++
				}
				cuts = append(cuts, xmlPatch{from: start, to: e.to})
				start, shape = -1, false
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if buttons != count {
		return nil, fmt.Errorf("%w：表单按钮和控件属性数量不匹配", ErrXLSX)
	}
	var out bytes.Buffer
	if err = writeXMLPatches(ctx, &out, data, cuts); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// A worksheet can carry form buttons in alternate content. Interpret only the
// control relationship and its anchor; every other branch must keep its scope.
func controlMarkup(name xml.Name) bool {
	switch name.Space {
	case markupNS:
		return slices.Contains([]string{"AlternateContent", "Choice", "Fallback"}, name.Local)
	case mainNS:
		return slices.Contains([]string{"controls", "control", "controlPr", "anchor", "from", "to"}, name.Local)
	case drawingNS:
		return slices.Contains([]string{"col", "colOff", "row", "rowOff"}, name.Local)
	default:
		return false
	}
}
