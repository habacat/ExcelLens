package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
)

func (p pivotPlan) writeMeasures(ctx context.Context, raw []byte, caption string) ([]byte, error) {
	measures := p.measures
	if len(measures) == 0 {
		measures = []pivotDataField{{Fld: p.data, Name: caption, Subtotal: p.aggregation}}
	}
	if slices.Equal(p.table.DataFields, measures) {
		return raw, nil
	}
	origins := p.measureOrigins
	if origins == nil {
		for _, m := range measures {
			origins = append(origins, slices.Index(p.table.DataFields, m))
		}
	}
	return reorderPivotChildren(ctx, raw, "dataFields", "dataField", len(p.table.DataFields), origins, func(i, origin int, original []byte) ([]byte, error) {
		m := measures[i]
		if origin < 0 {
			return []byte(fmt.Sprintf(`<dataField xmlns="%s" name="%s" fld="%d" subtotal="%s"/>`, mainNS, escapedXML(m.Name), m.Fld, m.method())), nil
		}
		old := p.table.DataFields[origin]
		if old == m {
			return original, nil
		}
		// Keep the existing number format, extension children and namespace scope.
		decoder := xml.NewDecoder(bytes.NewReader(original))
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		var patches []xmlPatch
		for _, attr := range []struct{ name, before, after string }{{"name", old.Name, m.Name}, {"subtotal", old.method(), m.method()}} {
			if attr.before == attr.after {
				continue
			}
			changed, err := setXMLAttribute(original, 0, end, attr.name, escapedXML(attr.after))
			if err != nil {
				return nil, err
			}
			patches = append(patches, changed...)
		}
		return applyXML(ctx, original, patches)
	})
}
