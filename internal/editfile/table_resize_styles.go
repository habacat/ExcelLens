package editfile

import (
	"context"
	"encoding/xml"
	"strconv"

	"excellens/internal/editdoc"
)

type tableDifferential struct {
	Number *struct {
		ID int `xml:"numFmtId,attr"`
	} `xml:"numFmt"`
	Components []xml.Name `xml:",any"`
}

// A run of identically formatted worksheet cells need not be a table format.
// Excel applies the column's declared formatting when a resize absorbs cells.
// A numeric differential changes only the number format of each destination.
func (m tableResize) dataStyles(ctx context.Context, p *xlsxPackage, s rangeSheet, cells map[editdoc.Address]transferCell, updates map[string][]byte) (map[editdoc.Address]string, error) {
	if m.next.lastRow <= m.old.lastRow {
		return nil, nil
	}
	declared := map[int]string{}
	var numbers map[int]int
	var formats entryFormatTable
	var styleSheet struct {
		Differential []tableDifferential `xml:"dxfs>dxf"`
	}
	for i, field := range m.markup {
		col := m.next.firstCol + i
		if field.dataStyle != "" {
			declared[col] = ""
		}
		if field.dataDxf == "" {
			continue
		}
		if numbers == nil {
			var err error
			formats, err = entryFormats(ctx, p)
			if err != nil {
				return nil, err
			}
			// Scope dxf to the main collection; extensions may define their own.
			if err = xml.Unmarshal(formats.data, &styleSheet); err != nil {
				return nil, err
			}
			numbers = make(map[int]int)
		}
		id, err := strconv.Atoi(field.dataDxf)
		if err != nil || id < 0 || id >= len(styleSheet.Differential) {
			return nil, ErrXLSX
		}
		dxf := styleSheet.Differential[id]
		if dxf.Number != nil {
			numbers[col] = dxf.Number.ID
		}
		if len(dxf.Components) != 0 {
			declared[col] = ""
		}
	}
	if len(declared) == 0 && len(numbers) == 0 {
		return nil, nil
	}
	if len(declared) > 0 {
		if err := m.uniformColumnStyles(ctx, p, s, declared); err != nil {
			return nil, err
		}
	}
	styles := map[editdoc.Address]string{}
	ids := map[xfNumber]string{}
	requests := map[editdoc.Address]xfNumber{}
	body, old := m.body(m.next), m.body(m.old)
	for a := range cells {
		if !body.contains(a.Row, a.Col) || old.contains(a.Row, a.Col) || !m.old.colsSpan().has(a.Col) {
			continue
		}
		base := declared[a.Col]
		if base != "" {
			styles[a] = base
		}
		if number, ok := numbers[a.Col]; ok {
			if base == "" {
				base = cells[m.physicalSource(a)].node.value.Style
			}
			if base == "" {
				base = "0"
			}
			key := xfNumber{base, number}
			requests[a], ids[key] = key, ""
		}
	}
	ps, err := p.composeNumberFormats(ctx, formats, ids)
	if err != nil {
		return nil, err
	}
	if len(ps) > 0 {
		updates["xl/styles.xml"], err = applyXML(ctx, formats.data, ps)
		if err != nil {
			return nil, err
		}
	}
	for a, key := range requests {
		styles[a] = ids[key]
	}
	return styles, nil
}
