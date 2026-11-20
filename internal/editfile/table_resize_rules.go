package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

// When a total moves, split only the addresses whose cells actually rotate.
// Unrelated parts of a discontiguous validation/formatting rule keep their anchors.
func (m tableResize) ruleAreas(r cellRect) []cellRect {
	if m.old.lastRow == m.next.lastRow {
		return []cellRect{r}
	}
	body := m.body(m.old)
	newBody := m.body(m.next)
	if r.firstRow == body.firstRow && r.lastRow == body.lastRow && r.colsSpan().inside(m.heightColumns()) {
		r.lastRow = newBody.lastRow
		if m.table.Totals > 0 {
			if m.next.lastRow < m.old.lastRow {
				return []cellRect{r, {m.next.lastRow + 1, r.firstCol, m.old.lastRow, r.lastCol}}
			}
			if m.next.lastRow == m.old.lastRow+1 {
				r.lastRow++
			}
		}
		return []cellRect{r}
	}
	if m.table.Totals == 0 {
		return []cellRect{r}
	}
	lane := m.totalsLane()
	if !r.overlaps(lane) || r.covers(lane) {
		return []cellRect{r}
	}
	parts := r.minus(lane)
	inside := r.intersection(lane)
	total := lane
	total.firstRow, total.lastRow = m.old.lastRow, m.old.lastRow
	if inside.overlaps(total) {
		parts = append(parts, inside.intersection(total).offset(m.next.lastRow-m.old.lastRow, 0))
	}
	delta := 1
	if m.next.lastRow > m.old.lastRow {
		delta = -1
	}
	for _, other := range inside.minus(total) {
		parts = append(parts, other.offset(delta, 0))
	}
	return parts
}

func (m tableResize) ruleRefs(raw string) (string, error) {
	var refs []string
	for _, ref := range strings.Fields(raw) {
		r, ok := parseRect(strings.ReplaceAll(ref, "$", ""))
		if !ok {
			return "", fmt.Errorf("无法定位规则范围 %s", ref)
		}
		parts := m.ruleAreas(r)
		if len(parts) == 1 && parts[0] == r {
			refs = append(refs, ref)
			continue
		}
		for _, part := range parts {
			text := pivotRectRef(part)
			if part.single() {
				text = strings.SplitN(text, ":", 2)[0]
			}
			refs = append(refs, text)
		}
	}
	return strings.Join(refs, " "), nil
}
func (m tableResize) resizeRules(ctx context.Context, raw []byte) ([]byte, error) {
	var ps []xmlPatch
	var node, content int
	var body strings.Builder
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "dataValidation" || e.local() == "conditionalFormatting" {
				if t.Name.Space == mainNS {
					refs := attribute(t, "", "sqref")
					next, err := m.ruleRefs(refs)
					if err != nil {
						return err
					}
					if next != refs {
						more, err := setXMLAttribute(raw, e.from, e.to, "sqref", next)
						ps = append(ps, more...)
						return err
					}
				} else if t.Name.Space == x14NS {
					node = len(e.stack)
				}
			}
			if node > 0 && len(e.stack) == node+1 && e.local() == "sqref" {
				content = e.to
				body.Reset()
			}
			if m.table.Totals > 0 && m.old.lastRow != m.next.lastRow && e.local() == "hyperlink" {
				if ref, ok := parseRect(attribute(t, "", "ref")); ok && ref.overlaps(m.totalsLane()) {
					return errors.New("汇总行移动的区域包含超链接")
				}
			}
		case xml.CharData:
			if content > 0 {
				body.Write(t)
			}
		case xml.EndElement:
			if content > 0 && e.local() == "sqref" {
				next, err := m.ruleRefs(body.String())
				if err != nil {
					return err
				}
				if next != body.String() {
					ps = append(ps, xmlPatch{content, e.from, next})
				}
				content = 0
			}
			if len(e.stack) == node {
				node = 0
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, ps)
}

func (m tableResize) uniformColumnStyles(ctx context.Context, p *xlsxPackage, s rangeSheet, styles map[int]string) error {
	body := m.body(m.old)
	count := map[int]int{}
	structure := worksheetStructure{visitCell: func(row, col int, c sheetCell) error {
		if _, needed := styles[col]; !needed || !body.contains(row, col) {
			return nil
		}
		count[col]++
		if count[col] == 1 {
			styles[col] = c.value.Style
		} else if styles[col] != c.value.Style {
			styles[col] = ""
		}
		return nil
	}}
	if _, err := p.scanSheet(ctx, s.data, s.sheet, nil, nil, nil, &structure); err != nil {
		return err
	}
	for col, n := range count {
		if n != body.lastRow-body.firstRow+1 || styles[col] == "0" {
			delete(styles, col)
		}
	}
	return nil
}
func cellWithStyle(ctx context.Context, raw, style string) (string, bool, error) {
	data := []byte(raw)
	d := xml.NewDecoder(bytes.NewReader(data))
	if _, err := d.RawToken(); err != nil {
		return "", false, err
	}
	ps, err := setXMLAttribute(data, 0, int(d.InputOffset()), "s", style)
	if err != nil {
		return "", false, err
	}
	result, err := applyXML(ctx, data, ps)
	return string(result), true, err
}

func (m tableResize) checkNotes(ctx context.Context, p *xlsxPackage, s rangeSheet) error {
	if m.table.Totals == 0 || m.old.lastRow == m.next.lastRow {
		return nil
	}
	for _, part := range p.links[s.sheet.Part] {
		if classifyPart(part).role != "comments" {
			continue
		}
		raw, err := p.readPart(ctx, part)
		if err != nil {
			return err
		}
		notes, err := commentAddresses(ctx, raw)
		if err != nil {
			return err
		}
		for ref := range notes {
			r, _ := parseRect(ref)
			if r.overlaps(m.totalsLane()) {
				return fmt.Errorf("汇总行移动的区域包含批注 %s", ref)
			}
		}
	}
	return nil
}
