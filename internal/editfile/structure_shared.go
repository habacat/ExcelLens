package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type structureFormulaMember struct {
	address    editdoc.Address
	group      int
	start, end int
}

// Resolve each shared member in the old coordinates. Insertion between the
// master and a follower can change a relative offset outside the edited sheet.
func (p *xlsxPackage) expandStructureGroups(ctx context.Context, data []byte, sheet string, a axisEdit) ([]byte, error) {
	return p.expandMappedGroups(ctx, data, sheet, a, nil)
}
func (p *xlsxPackage) expandMappedGroups(ctx context.Context, data []byte, sheet string, a axisEdit, sourceRow func(int) int) ([]byte, error) {
	if a.action == "rename" || !bytes.Contains(data, []byte("shared")) {
		return data, nil
	}
	var members []structureFormulaMember
	masters := map[int]sharedGroup{}
	selected := map[int]bool{}
	var cell editdoc.Address
	var member structureFormulaMember
	var active bool
	var body strings.Builder
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			switch t.Name {
			case xml.Name{Space: mainNS, Local: "c"}:
				col, row, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				if sourceRow != nil {
					row = sourceRow(row)
				}
				cell = editdoc.Address{Sheet: sheet, Row: row, Col: col}
			case xml.Name{Space: mainNS, Local: "f"}:
				active = attribute(t, "", "t") == "shared" && e.parent() == "c"
				if !active {
					return nil
				}
				group, err := strconv.Atoi(attribute(t, "", "si"))
				if err != nil || group < 0 {
					return ErrXLSX
				}
				member = structureFormulaMember{address: cell, group: group, start: e.from}
				body.Reset()
			}
		case xml.CharData:
			if active {
				body.Write(t)
			}
		case xml.EndElement:
			if active && e.local() == "f" {
				active = false
				member.end = e.to
				members = append(members, member)
				if body.Len() == 0 {
					return nil
				}
				group := member.group
				if _, ok := masters[group]; ok {
					return ErrXLSX
				}
				needed := sheet == a.Sheet
				if !needed {
					_, err := calculator.RewriteReferences(body.String(), func(label, q, ref string) (string, error) {
						if strings.EqualFold(label, a.Sheet) {
							needed = true
						}
						return q + ref, nil
					})
					if err != nil {
						return err
					}
				}
				selected[group] = needed
				g := sharedGroup{area: cellRect{cell.Row, cell.Col, cell.Row, cell.Col}}
				if needed {
					var err error
					g.formula, err = p.parseFormula(ctx, body.String(), cell, nil)
					if err != nil {
						return err
					}
				}
				masters[group] = g
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var ps []xmlPatch
	var total int
	for _, m := range members {
		master, ok := masters[m.group]
		if !ok {
			return nil, ErrXLSX
		}
		if !selected[m.group] {
			continue
		}
		raw, err := master.formula.Shift(m.address.Row-master.area.firstRow, m.address.Col-master.area.firstCol)
		if err != nil {
			return nil, err
		}
		body := fmt.Sprintf(`<f xmlns="%s">%s</f>`, mainNS, escapedXML(strings.TrimPrefix(raw, "=")))
		total += len(body)
		if total > formats.EditXLSXPartBytes {
			return nil, ErrCalcCapacity
		}
		ps = append(ps, xmlPatch{m.start, m.end, body})
	}
	return applyXML(ctx, data, ps)
}
