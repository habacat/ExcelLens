package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"strconv"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

// Pivot outputs can be upstream of ordinary formulas and chart series.
// Refresh their saved results against the same revision as the cache records.
func (p *xlsxPackage) refreshDependents(ctx context.Context, values objectValues, updates map[string][]byte) error {
	return p.refreshWithMetadata(ctx, values, updates, newArrayMetadata(p))
}

func (p *xlsxPackage) refreshWithMetadata(ctx context.Context, values objectValues, updates map[string][]byte, meta *arrayMetadata) error {
	sheets := map[string]bool{}
	for a := range values.calculation.Cells {
		sheets[a.Sheet] = true
	}
	for _, s := range p.layout.Sheets {
		if !sheets[s.Name] {
			continue
		}
		data, ok := updates[s.Part]
		if !ok {
			var err error
			data, err = p.readPart(ctx, s.Part)
			if err != nil {
				return err
			}
		}
		next, err := resultCaches(ctx, data, s.Name, values.calculation, meta)
		if err != nil {
			return err
		}
		if !bytes.Equal(next, data) {
			updates[s.Part] = next
		}
	}
	affected := make(map[string][]editdoc.Address)
	for a := range values.overlay {
		affected[a.Sheet] = append(affected[a.Sheet], a)
	}
	for a := range values.calculation.Cells {
		affected[a.Sheet] = append(affected[a.Sheet], a)
	}
	for _, f := range p.zip.File {
		if classifyPart(f.Name).role != "chart" {
			continue
		}
		data, err := p.readPart(ctx, f.Name)
		if err != nil {
			return err
		}
		if _, pivot, err := chartPivotSource(ctx, data); err != nil {
			return err
		} else if pivot {
			continue
		}
		next, err := p.updateChartCaches(ctx, data, values, affected)
		if err != nil {
			return err
		}
		if !bytes.Equal(next, data) {
			updates[f.Name] = next
		}
	}
	return meta.write(ctx, updates)
}

func resultCaches(ctx context.Context, data []byte, sheet string, calc *Calculation, meta *arrayMetadata) ([]byte, error) {
	var ps []xmlPatch
	var active bool
	var value string
	var hasFormula bool
	var start, end int
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "c" && e.parent() == "row" {
				col, row, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				item, changed := calc.Cells[editdoc.Address{Sheet: sheet, Row: row, Col: col}]
				hasFormula = false
				active = changed
				value = ""
				start, end = 0, 0
				if !active {
					return nil
				}
				kind := ""
				vm := 0
				if item.State == "calculated" {
					raw := item.Value.Raw
					switch item.Value.Kind {
					case "text":
						kind = "str"
					case "error":
						kind = "e"
						vm = meta.bind(arrayResult{}, arrayCell{value: item.Value}, true)
						if vm != 0 {
							raw = "#VALUE!"
						}
					case "boolean":
						kind = "b"
						raw = "0"
						if item.Value.Raw == "TRUE" {
							raw = "1"
						}
					case "date":
						kind = "d"
					case "blank":
						raw = "0"
					}
					value = `<v xmlns="` + mainNS + `">` + escapedXML(raw) + "</v>"
				}
				var patches []xmlPatch
				if kind != "" {
					patches, err = setXMLAttribute(data, e.from, e.to, "t", kind)
				} else {
					patches, err = removeXMLAttributes(data, e.from, e.to, "t")
				}
				ps = append(ps, patches...)
				if err != nil {
					return err
				}
				if vm != 0 {
					patches, err = setXMLAttribute(data, e.from, e.to, "vm", strconv.Itoa(vm))
				} else {
					patches, err = removeXMLAttributes(data, e.from, e.to, "vm")
				}
				ps = append(ps, patches...)
				return err
			}
			if active && e.local() == "f" && e.parent() == "c" {
				hasFormula = true
			}
			if active && e.local() == "v" && e.parent() == "c" {
				start = e.from
			}
		case xml.EndElement:
			if active && e.local() == "v" && e.parent() == "c" {
				end = e.to
			}
			if active && e.local() == "c" {
				if start == 0 {
					start, end = e.from, e.from
				}
				if e.from == e.to || !hasFormula {
					return ErrXLSX
				}
				ps = append(ps, xmlPatch{start, end, value})
				active = false
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, data, ps)
}
