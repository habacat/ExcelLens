package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func (p *xlsxPackage) readOfficeRules(ctx context.Context, data []byte, sheet string, structure *worksheetStructure) error {
	rules, complete, err := workbook.WorksheetValidationRules(ctx, data)
	if err != nil {
		return err
	}
	if !complete {
		structure.unknownScope = true
	}
	for _, source := range rules {
		if source.Type == "none" && source.Status == "described" {
			continue
		}
		for _, rect := range source.Ranges {
			if len(structure.blocks)+len(structure.nativeRules) >= formats.EditXLSXRangeLimit {
				return ErrXLSX
			}
			area := editdoc.Area{Start: editdoc.Address{Sheet: sheet, Row: rect.Row, Col: rect.Col}, EndRow: rect.EndRow, EndCol: rect.EndCol}
			rule := editdoc.Rule{Area: area, Type: source.Type, Operator: source.Operator, AllowBlank: source.AllowBlank, First: source.Formula1, Second: source.Formula2, Reference: source.RequiresListLookup()}
			if rule.Type == "list" {
				rule.Operator = ""
			}
			var compiled validatedRule
			switch {
			case rule.Reference && rule.Valid():
				compiled = validatedRule{Rule: rule, nativeList: true}
				compiled.reference, err = p.boundOfficeList(rule)
			case source.Status == "described":
				compiled, err = compileRule(rule)
			default:
				err = errRule
			}
			if err != nil {
				structure.blocks = append(structure.blocks, ownedRange{area: cellRect{rect.Row, rect.Col, rect.EndRow, rect.EndCol}, reason: "此数据验证条件需要在 Excel 中编辑"})
				continue
			}
			structure.nativeRules = append(structure.nativeRules, compiled)
		}
	}
	return nil
}

func (p *xlsxPackage) boundOfficeList(rule editdoc.Rule) (editdoc.Area, error) {
	key := strings.ToUpper(strings.TrimPrefix(rule.First, "="))
	target, exists := p.names[nameKey{rule.Start.Sheet, key}]
	if !exists {
		target, exists = p.names[nameKey{"", key}]
	}
	if !exists {
		return p.layout.officeListReference(rule)
	}
	if len(target.areas) != 1 {
		return editdoc.Area{}, errReferenceSyntax
	}
	area := target.areas[0]
	ref := editdoc.Area{Start: editdoc.Address{Sheet: area.Sheet, Row: area.Row, Col: area.Col}, EndRow: area.EndRow, EndCol: area.EndCol}
	if !ref.Valid() || ref.Start.Row != ref.EndRow && ref.Start.Col != ref.EndCol || ref.Intersects(rule.Area) {
		return ref, errReferenceSyntax
	}
	return ref, nil
}

func (l XLSXLayout) officeListReference(rule editdoc.Rule) (editdoc.Area, error) {
	sheet, address, qualified, err := workbook.SplitSheetReference(strings.TrimPrefix(rule.First, "="))
	if err != nil {
		return editdoc.Area{}, err
	}
	if !qualified {
		sheet = rule.Start.Sheet
	}
	idx := slices.IndexFunc(l.Sheets, func(s XLSXSheet) bool { return strings.EqualFold(sheet, s.Name) })
	if idx < 0 {
		return editdoc.Area{}, errReferenceTarget
	}
	ref, err := workbook.AbsoluteSourceArea("xlsx", l.Sheets[idx].Name, address)
	if err != nil {
		return editdoc.Area{}, err
	}
	area := editdoc.Area{Start: editdoc.Address{Sheet: ref.Sheet, Row: ref.Row, Col: ref.Col}, EndRow: ref.EndRow, EndCol: ref.EndCol}
	if area.Start.Row != area.EndRow && area.Start.Col != area.EndCol || area.Intersects(rule.Area) {
		return area, errReferenceSyntax
	}
	return area, nil
}

// For large list sources (often an entire column), read only coordinates with
// a stored value or formula. A style-only cell cannot contribute a choice.
// Scan each source sheet once even when several validations share a list.
func (l Layout) readOfficeListMembers(ctx context.Context, f *workbook.EditSourceFile, rules []validatedRule) ([]editdoc.Address, error) {
	members := map[editdoc.Area][]editdoc.Address{}
	sparse := map[string][]editdoc.Area{}
	for _, rule := range rules {
		if !rule.nativeList {
			continue
		}
		ref := rule.reference
		if _, ok := members[ref]; ok {
			continue
		}
		members[ref] = nil
		if ref.EndRow-ref.Start.Row+ref.EndCol-ref.Start.Col+1 <= formats.EditReferenceListCells {
			members[ref] = slices.Collect(referenceAddresses(ref))
		} else {
			sparse[ref.Start.Sheet] = append(sparse[ref.Start.Sheet], ref)
		}
	}
	if len(sparse) > 0 {
		p, err := openXLSXPackage(f, f.Size())
		if err != nil {
			return nil, err
		}
		for _, sheet := range l.XLSX.Sheets {
			refs := sparse[sheet.Name]
			if len(refs) == 0 {
				continue
			}
			var cell editdoc.Address
			err = officeWalk(ctx, p.files[sheet.Part], func(e xmlEvent) error {
				start, ok := e.token.(xml.StartElement)
				if !ok || len(e.stack) < 4 || e.stack[1] != (xml.Name{Space: mainNS, Local: "sheetData"}) || e.stack[2] != (xml.Name{Space: mainNS, Local: "row"}) || e.stack[3] != (xml.Name{Space: mainNS, Local: "c"}) {
					return nil
				}
				if len(e.stack) == 4 {
					col, row, err := workbook.ParseCellRef(attribute(start, "", "r"))
					if err != nil {
						return err
					}
					cell = editdoc.Address{Sheet: sheet.Name, Row: row, Col: col}
				} else if len(e.stack) == 5 && start.Name.Space == mainNS && (start.Name.Local == "v" || start.Name.Local == "is" || start.Name.Local == "f") && cell.Row != 0 {
					for _, ref := range refs {
						if !ref.Contains(cell) {
							continue
						}
						if len(members[ref]) >= formats.EditReferenceListCells {
							return fmt.Errorf("下拉来源中有值的单元格超过 %d 个", formats.EditReferenceListCells)
						}
						members[ref] = append(members[ref], cell)
					}
					cell.Row = 0
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	seen := map[editdoc.Address]bool{}
	var addresses []editdoc.Address
	for i, rule := range rules {
		if !rule.nativeList {
			continue
		}
		rules[i].members = members[rule.reference]
		for _, a := range rules[i].members {
			if seen[a] {
				continue
			}
			seen[a] = true
			if len(seen) > formats.EditReferenceCells {
				return nil, fmt.Errorf("下拉来源合计超过 %d 格", formats.EditReferenceCells)
			}
			addresses = append(addresses, a)
		}
	}
	return addresses, nil
}

// RuleAt returns a rule collected from this coordinate's preserved sheet.
func (c SourceCell) RuleAt(a editdoc.Address) *editdoc.Rule {
	for _, r := range c.rules {
		if r.Contains(a) {
			return &r.Rule
		}
	}
	return nil
}
