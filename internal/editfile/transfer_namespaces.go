package editfile

import (
	"bytes"
	"encoding/xml"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// A producer may declare the cell's prefix on the worksheet, sheetData, or
// a single row. Carry inherited bindings when the destination uses another
// prefix. Namespace declarations on the cell and its rich text remain local.
type transferNamespaces struct {
	data map[string]string
	rows map[int]map[string]string
}

func (s *transferNamespaces) observe(e xmlEvent, el xml.StartElement) {
	row := e.local() == "row" && e.parent() == "sheetData"
	if len(e.stack) != 1 && !(len(e.stack) == 2 && e.local() == "sheetData") && !row {
		return
	}
	var updated map[string]string
	for _, a := range el.Attr {
		if !nsDeclaration(a) {
			continue
		}
		if updated == nil {
			updated = maps.Clone(s.data)
			if updated == nil {
				updated = map[string]string{}
			}
		}
		name := a.Name.Local
		if a.Name.Space == "xmlns" {
			name = "xmlns:" + name
		}
		updated[name] = a.Value
	}
	if updated == nil {
		return
	}
	if !row {
		s.data = updated
		return
	}
	n, _ := strconv.Atoi(attribute(el, "", "r"))
	if s.rows == nil {
		s.rows = map[int]map[string]string{}
	}
	s.rows[n] = updated
}

func (s transferNamespaces) at(row int) map[string]string {
	if local, ok := s.rows[row]; ok {
		return local
	}
	return s.data
}

func transferredBindings(raw []byte, tagEnd int, source, target map[string]string) ([]xmlPatch, error) {
	var ps []xmlPatch
	for name, uri := range source {
		if target[name] == uri || name != "xmlns" && !bytes.Contains(raw, []byte(name[len("xmlns:"):]+":")) {
			continue
		}
		local, err := removeXMLAttributes(raw, 0, tagEnd, name)
		if err != nil {
			return nil, err
		}
		if len(local) != 0 {
			continue
		}
		at := tagEnd - 1
		if raw[at-1] == '/' {
			at--
		}
		ps = append(ps, xmlPatch{at, at, " " + name + "=\"" + escapedXML(uri) + "\""})
	}
	slices.SortFunc(ps, func(a, b xmlPatch) int { return strings.Compare(a.data, b.data) })
	return ps, nil
}
