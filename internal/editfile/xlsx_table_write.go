package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"excellens/internal/editdoc"
)

func equalTables(a, b []xlsxTable) bool {
	return slices.EqualFunc(a, b, func(x, y xlsxTable) bool {
		return x.Name == y.Name && x.Sheet == y.Sheet && x.Part == y.Part && x.Ref == y.Ref && x.Headers == y.Headers && x.Totals == y.Totals && x.Filtered == y.Filtered && slices.Equal(x.Columns, y.Columns)
	})
}

var builtinTotal = regexp.MustCompile(`(?i)^=SUBTOTAL\(\s*(10[1234579]|110)\s*,\s*(.+?)\s*\)$`)
var totalFunctions = map[string]string{"101": "average", "102": "countNums", "103": "count", "104": "max", "105": "min", "107": "stdDev", "109": "sum", "110": "var"}

func (t xlsxTable) totalColumn(index int, v editdoc.Value) (xlsxTableColumn, error) {
	c := t.Columns[index]
	c.Function, c.Label, c.TotalFormula = "", "", ""
	switch v.Kind {
	case "blank":
	case "text":
		c.Label = v.Raw
	case "formula":
		if matches := builtinTotal.FindStringSubmatch(v.Raw); len(matches) > 0 && strings.EqualFold(matches[2], t.Name+"["+tableCaptionEscape.Replace(c.Name)+"]") {
			c.Function = totalFunctions[matches[1]]
		} else {
			c.Function = "custom"
			c.TotalFormula = v.Raw
		}
	default:
		return c, fmt.Errorf("汇总行应保存为标签或公式")
	}
	return c, nil
}

func (p *xlsxPackage) tablePatches(ctx context.Context, edits map[editdoc.Address]editdoc.Value, templates map[editdoc.Address]string, result *XLSXResult) (map[string][]xmlPatch, error) {
	out := map[string][]xmlPatch{}
	if !equalTables(p.layout.Tables, result.Layout.Tables) {
		return nil, ErrBinding
	}
	result.Layout.Tables = slices.Clone(p.layout.Tables)
	found := 0
	for i, t := range p.layout.Tables {
		var markup []columnMarkup
		var data []byte
		first, _ := t.dataRows()
		area := t.bounds()
		for j, orig := range t.Columns {
			addr := editdoc.Address{Sheet: t.Sheet, Row: area.lastRow, Col: area.firstCol + j}
			next := orig
			if v, ok := edits[addr]; ok && t.Totals == 1 && v.Kind != "" {
				var err error
				next, err = t.totalColumn(j, v)
				if err != nil {
					return nil, err
				}
			}
			addr.Row = first
			if raw, ok := templates[addr]; ok {
				if orig.Array {
					return nil, ErrBinding
				}
				if raw != "" {
					if !(editdoc.Value{Kind: "formula", Raw: raw}).Valid() {
						return nil, ErrBinding
					}
					if _, err := p.parseFormula(ctx, raw[1:], addr, nil); err != nil {
						return nil, err
					}
				}
				next.Formula = raw
				found++
			}
			if next == orig {
				continue
			}
			var err error
			if markup == nil {
				data, err = p.readPart(ctx, t.Part)
				if err != nil {
					return nil, err
				}
				_, markup, err = parseTable(ctx, data, t.Sheet, t.Part, "")
				if err != nil {
					return nil, err
				}
				result.Layout.Tables[i].Columns = slices.Clone(t.Columns)
			}
			result.Layout.Tables[i].Columns[j] = next
			change, err := patchTableColumn(ctx, data, markup[j], orig, next)
			if err != nil {
				return nil, err
			}
			out[t.Part] = append(out[t.Part], change)
		}
	}
	if found != len(templates) {
		return nil, ErrBinding
	}
	return out, nil
}

func escapedXML(text string) string {
	var out strings.Builder
	_ = xml.EscapeText(&out, []byte(text))
	return out.String()
}
func patchTableColumn(ctx context.Context, data []byte, m columnMarkup, old, next xlsxTableColumn) (xmlPatch, error) {
	tag := m.column
	start := data[tag.from:tag.openEnd]
	omit := []string{}
	var attrs strings.Builder
	for _, field := range []struct{ name, before, after string }{{"totalsRowFunction", old.Function, next.Function}, {"totalsRowLabel", old.Label, next.Label}} {
		if field.before == field.after {
			continue
		}
		omit = append(omit, field.name)
		if field.after != "" {
			fmt.Fprintf(&attrs, ` %s="%s"`, field.name, escapedXML(field.after))
		}
	}
	changes, err := removeXMLAttributes(start, 0, len(start), omit...)
	if err != nil {
		return xmlPatch{}, err
	}
	var body strings.Builder
	if err := writeXMLPatches(ctx, &body, start, changes); err != nil {
		return xmlPatch{}, err
	}
	self := bytes.HasSuffix(start, []byte("/>"))
	open := strings.TrimSuffix(body.String(), ">")
	if self {
		open = strings.TrimSuffix(open, "/")
	}
	var children []xmlPatch
	for _, f := range []struct {
		name, before, after string
		node                tableElement
	}{{"calculatedColumnFormula", old.Formula, next.Formula, m.formula}, {"totalsRowFormula", old.TotalFormula, next.TotalFormula, m.total}} {
		if f.before == f.after {
			continue
		}
		if f.node.openEnd != 0 {
			if f.after == "" {
				children = append(children, xmlPatch{f.node.from, f.node.to, ""})
			} else {
				children = append(children, xmlPatch{f.node.openEnd, f.node.endStart, escapedXML(f.after[1:])})
			}
		} else if f.after != "" {
			pos := tag.endStart
			if m.childStart > 0 {
				pos = m.childStart
			}
			if f.name == "totalsRowFormula" && m.formula.to != 0 {
				pos = m.formula.to
			}
			children = append(children, xmlPatch{pos, pos, "<" + f.name + ` xmlns="` + mainNS + `">` + escapedXML(f.after[1:]) + "</" + f.name + ">"})
		}
	}
	body.Reset()
	body.WriteString(open)
	body.WriteString(attrs.String())
	if self && len(children) == 0 {
		body.WriteString("/>")
	} else {
		body.WriteString(">")
		slices.SortStableFunc(children, func(a, b xmlPatch) int { return a.from - b.from })
		for i := range children {
			children[i].from -= tag.openEnd
			children[i].to -= tag.openEnd
		}
		if err := writeXMLPatches(ctx, &body, data[tag.openEnd:tag.endStart], children); err != nil {
			return xmlPatch{}, err
		}
		if self {
			body.WriteString(closingTag(start))
		} else {
			body.Write(data[tag.endStart:tag.to])
		}
	}
	return xmlPatch{tag.from, tag.to, body.String()}, nil
}
