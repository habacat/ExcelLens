package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const entryStylePart = "xl/styles.xml"

// These are the implicit style components of a new worksheet. Only missing
// collections are supplied. Existing format codes, fonts and extensions retain
// their original bytes and indexes.
var entryStyleDefaults = []struct{ name, items string }{
	{"fonts", `<font><sz val="11"/><color theme="1"/><name val="Calibri"/><family val="2"/><scheme val="minor"/></font>`},
	{"fills", `<fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>`},
	{"borders", `<border><left/><right/><top/><bottom/><diagonal/></border>`},
	{"cellStyleXfs", `<xf numFmtId="0" fontId="0" fillId="0" borderId="0"/>`},
	{"cellXfs", `<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>`},
	{"cellStyles", `<cellStyle name="Normal" xfId="0" builtinId="0"/>`},
}

func seedEntryFormats(ctx context.Context, raw []byte) (entryFormatTable, error) {
	if len(raw) == 0 {
		raw = []byte(`<styleSheet xmlns="` + mainNS + `"></styleSheet>`)
	}
	type span struct{ from, to, children int }
	sections := map[string]*span{}
	var root xmlEvent
	end := 0
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch e.token.(type) {
		case xml.StartElement:
			switch len(e.stack) {
			case 1:
				root = e
			case 2:
				sections[e.local()] = &span{from: e.from}
			case 3:
				sections[e.parent()].children++
			}
		case xml.EndElement:
			switch len(e.stack) {
			case 1:
				end = e.from
			case 2:
				sections[e.local()].to = e.to
			}
		}
		return nil
	})
	if err != nil {
		return entryFormatTable{}, err
	}
	if root.to == 0 {
		return entryFormatTable{}, ErrXLSX
	}
	// Work backwards to find the insertion point before the next collection.
	// Forward order below is retained for several insertions at the same offset.
	offset := end
	for _, n := range []string{"dxfs", "tableStyles", "colors", "extLst"} {
		if s := sections[n]; s != nil {
			offset = min(offset, s.from)
		}
	}
	var patches []xmlPatch
	for i := len(entryStyleDefaults) - 1; i >= 0; i-- {
		d := entryStyleDefaults[i]
		to := offset
		if s := sections[d.name]; s != nil {
			offset = s.from
			to = s.to
			if s.children > 0 {
				continue
			}
		}
		count := 1
		if d.name == "fills" {
			count = 2
		}
		xml := fmt.Sprintf(`<%s xmlns="%s" count="%d">%s</%s>`, d.name, mainNS, count, d.items, d.name)
		patches = append(patches, xmlPatch{offset, to, xml})
	}
	slices.Reverse(patches)
	if bytes.HasSuffix(raw[root.from:root.to], []byte("/>")) {
		var body strings.Builder
		for _, p := range patches {
			body.WriteString(p.data)
		}
		patches = []xmlPatch{{root.to - 2, root.to, ">" + body.String() + closingTag(raw[root.from:root.to])}}
	}
	data, err := applyXML(ctx, raw, patches)
	if err != nil {
		return entryFormatTable{}, err
	}
	return parseEntryFormats(ctx, data)
}

// Register a new style part in the original package. All patches, including
// an independent calcChain removal, still address the original XML offsets.
func (p *xlsxPackage) installEntryStyles(ctx context.Context, changes []xmlPatch, patches map[string][]xmlPatch) (map[string][]byte, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	if p.files[entryStylePart] != nil {
		patches[entryStylePart] = changes
		return nil, nil
	}
	style, err := applyXML(ctx, nil, changes)
	if err != nil {
		return nil, err
	}
	kind := classifyPart(entryStylePart)
	for _, part := range []string{"[Content_Types].xml", "xl/_rels/workbook.xml.rels"} {
		raw, err := p.readPart(ctx, part)
		if err != nil {
			return nil, err
		}
		ids := map[string]bool{}
		err = walkXML(ctx, raw, func(e xmlEvent) error {
			if tag, ok := e.token.(xml.StartElement); ok && len(e.stack) == 2 {
				ids[attribute(tag, "", "Id")] = true
			}
			if _, ok := e.token.(xml.EndElement); !ok || len(e.stack) != 1 {
				return nil
			}
			if e.from == e.to {
				return ErrXLSX
			}
			var node string
			if part == "[Content_Types].xml" {
				node = fmt.Sprintf(`<Override xmlns="%s" PartName="/%s" ContentType="%s"/>`, contentNS, entryStylePart, kind.mime)
			} else {
				id := 1
				for ids["rId"+strconv.Itoa(id)] {
					id++
				}
				node = fmt.Sprintf(`<Relationship xmlns="%s" Id="rId%d" Type="%s" Target="styles.xml"/>`, packageNS, id, kind.relation)
			}
			patches[part] = append(patches[part], xmlPatch{e.from, e.from, node})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return map[string][]byte{entryStylePart: style}, nil
}
