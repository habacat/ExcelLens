package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	customPropsNS = "http://schemas.openxmlformats.org/officeDocument/2006/custom-properties"
	variantTypeNS = "http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"
)

var propertyGUID = regexp.MustCompile(`(?i)^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$`)

func plainAttributes(el xml.StartElement, names ...string) bool {
	for _, attr := range el.Attr {
		if !nsDeclaration(attr) && (attr.Name.Space != "" || !slices.Contains(names, attr.Name.Local)) {
			return false
		}
	}
	return true
}

// Only properties with a single, independent scalar are retained. A
// linkTarget value is a workbook-backed cache and needs its own policy.
func checkXLSXCustomProperties(ctx context.Context, data []byte) error {
	ids := map[int64]bool{}
	var hasValue bool
	return walkXML(ctx, data, func(e xmlEvent) error {
		depth := len(e.stack)
		switch el := e.token.(type) {
		case xml.StartElement:
			switch depth {
			case 1:
				if el.Name != (xml.Name{Space: customPropsNS, Local: "Properties"}) || !plainAttributes(el) {
					return ErrXLSX
				}
			case 2:
				if el.Name != (xml.Name{Space: customPropsNS, Local: "property"}) || !plainAttributes(el, "fmtid", "pid", "name", "linkTarget") {
					return ErrXLSX
				}
				for _, attr := range el.Attr {
					if attr.Name == (xml.Name{Local: "linkTarget"}) {
						return fmt.Errorf("%w：自定义属性关联工作簿数据", ErrXLSX)
					}
				}
				pid, err := strconv.ParseInt(strings.TrimSpace(attribute(el, "", "pid")), 10, 32)
				if err != nil || pid < 2 || ids[pid] || !propertyGUID.MatchString(attribute(el, "", "fmtid")) {
					return ErrXLSX
				}
				ids[pid], hasValue = true, false
			case 3:
				if el.Name.Space != variantTypeNS || hasValue || !plainAttributes(el) || !scalarProperty(el.Name.Local) {
					return ErrXLSX
				}
				hasValue = true
			default:
				return ErrXLSX
			}
		case xml.CharData:
			if (depth < 3 || e.local() == "empty" || e.local() == "null") && len(bytes.TrimSpace(el)) != 0 {
				return ErrXLSX
			}
		case xml.EndElement:
			if depth == 2 && !hasValue {
				return ErrXLSX
			}
		}
		return nil
	})
}

func scalarProperty(name string) bool {
	switch name {
	case "empty", "null", "i1", "i2", "i4", "i8", "int", "ui1", "ui2", "ui4", "ui8", "uint",
		"r4", "r8", "decimal", "lpstr", "lpwstr", "bstr", "date", "filetime", "bool", "cy", "error", "clsid":
		return true
	}
	return false
}
