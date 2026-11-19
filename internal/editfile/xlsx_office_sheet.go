package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// Inspect coordinate-owning metadata on the worksheet being edited. Visual
// instructions and the objects they reference are copied in their source XML.
func (p *xlsxPackage) officeSheetElement(e xmlEvent, shape XLSXSheet, s *worksheetStructure) (bool, error) {
	if len(e.stack) < 2 {
		return false, nil
	}
	root := e.stack[1]
	if root == (xml.Name{Space: mainNS, Local: "extLst"}) {
		if len(e.stack) > 2 && e.stack[2] != (xml.Name{Space: mainNS, Local: "ext"}) {
			s.unknownScope = true
		}
		if len(e.stack) >= 4 {
			switch e.stack[3] {
			case xml.Name{Space: x14NS, Local: "dataValidations"}:
				// The source reader checks the extension URI, namespace and sqref.
				s.hasRules = true
			case xml.Name{Space: x14NS, Local: "conditionalFormattings"}, xml.Name{Space: x14NS, Local: "sparklineGroups"}:
				// Display metadata can be preserved as a subtree.
			default:
				s.unknownScope = true
			}
		}
		return true, nil
	}
	if root == (xml.Name{Space: markupNS, Local: "AlternateContent"}) {
		if el, ok := e.token.(xml.StartElement); ok {
			if !controlMarkup(el.Name) {
				s.unknownScope = true
			}
			if el.Name == (xml.Name{Space: mainNS, Local: "control"}) && !controlPart.MatchString(p.links[shape.Part][attribute(el, relationNS, "id")]) {
				return true, fmt.Errorf("%w：表单控件关系无效", ErrXLSX)
			}
		}
		return true, nil
	}
	if root.Space != mainNS {
		return false, nil
	}
	switch root.Local {
	case "dataValidations":
		s.hasRules = true
	case "hyperlinks":
		if el, ok := e.token.(xml.StartElement); ok && e.local() == "hyperlink" {
			if _, ok := parseRect(attribute(el, "", "ref")); !ok {
				return true, ErrXLSX
			}
			if id := attribute(el, relationNS, "id"); id != "" && p.links[shape.Part][id] != externalBookTarget {
				return true, ErrXLSX
			}
		}
	case "conditionalFormatting", "phoneticPr", "cellWatches", "dataConsolidate", "rowBreaks", "colBreaks", "ignoredErrors":
	case "autoFilter":
		if el, ok := e.token.(xml.StartElement); ok {
			if len(e.stack) == 2 {
				s.filter, _ = parseRect(attribute(el, "", "ref"))
			}
			if e.local() == "filterColumn" {
				s.filtered = true
			}
		}
		// Only the values and formula cells are changed. Excel keeps filter and
		// sort settings, row visibility and the original AutoFilter range.
	default:
		return false, nil
	}
	return true, nil
}

func (s *worksheetStructure) blockRefs(refs, reason string) error {
	fields := strings.Fields(refs)
	if len(fields) == 0 {
		return ErrXLSX
	}
	for _, ref := range fields {
		area, ok := parseRect(ref)
		if !ok || len(s.blocks) >= formats.EditXLSXRangeLimit {
			return ErrXLSX
		}
		s.blocks = append(s.blocks, ownedRange{area: area, reason: reason})
	}
	return nil
}

func (p *xlsxPackage) readOfficeCells(ctx context.Context, cells []editdoc.Address) (map[editdoc.Address]SourceCell, error) {
	result := make(map[editdoc.Address]SourceCell, len(cells))
	requested := map[string][]editdoc.Address{}
	for _, a := range cells {
		requested[a.Sheet] = append(requested[a.Sheet], a)
	}
	for _, s := range p.layout.Sheets {
		if len(requested[s.Name]) == 0 {
			continue
		}
		window, err := p.officeWindow(ctx, s, requested[s.Name])
		if err != nil {
			return nil, fmt.Errorf("工作表 %s：%w", s.Name, err)
		}
		for _, row := range window.targets.rows {
			for _, c := range row.cells {
				v := c.original
				if len(v.value.Raw) > formats.EditCellBytes || v.cache != nil && len(v.cache.Raw) > formats.EditCellBytes {
					return nil, ErrXLSX
				}
				result[c.address] = SourceCell{Value: v.value, Cache: v.cache, ReadOnly: v.readOnly, formula: v.formula, names: p.names, rules: v.rules, protected: v.protected}
			}
		}
	}
	if len(result) != len(cells) {
		return nil, ErrTarget
	}
	return result, nil
}
