package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// Copy changes relative axes, including mixed references. Cut instead follows
// the moved address even for absolute axes. Neither rewrites quoted strings.
func copyRangeFormula(raw string, dr, dc int) (string, error) {
	return calculator.RewriteReferences(raw, func(_, q, ref string) (string, error) {
		if ref == "" {
			return q, nil
		}
		parts := strings.Split(ref, ":")
		for i, part := range parts {
			a := axisAddress(part)
			row, col := a.row, a.col
			if row != 0 && !strings.HasPrefix(a.number, "$") {
				row += dr
			}
			if col != 0 && !strings.HasPrefix(a.letters, "$") {
				col += dc
			}
			if a.row != 0 && (row < 1 || row > formats.ExcelRows) || a.col != 0 && (col < 1 || col > formats.ExcelColumns) {
				parts[i] = "#REF!"
			} else {
				parts[i] = a.at(row, col)
			}
		}
		return q + strings.Join(parts, ":"), nil
	})
}

func (m rangeTransfer) rebindParts(ctx context.Context, p *xlsxPackage, updates map[string][]byte, roots map[editdoc.Address]editdoc.Value) error {
	owners := map[string]string{"xl/workbook.xml": ""}
	for _, s := range p.layout.Sheets {
		owners[s.Part] = s.Name
		for _, chart := range p.chartPartsFor(s.Name) {
			owners[chart] = s.Name
		}
	}
	for _, t := range p.layout.Tables {
		owners[t.Part] = t.Sheet
	}
	for part, scope := range owners {
		raw, expanded := updates[part]
		var err error
		if !expanded {
			raw, err = p.readPart(ctx, part)
			if err != nil {
				return err
			}
		}
		if !expanded && classifyPart(part).role == "sheet" {
			// Resolve third-sheet followers in their old coordinates only if
			// their shared formula refers to a sheet participating in the cut.
			for _, name := range []string{m.from, m.to} {
				raw, err = p.expandStructureGroups(ctx, raw, scope, axisEdit{Structure: editdoc.Structure{Sheet: name}})
				if err != nil {
					return err
				}
				if m.from == m.to {
					break
				}
			}
		}
		before := raw
		raw, err = p.rewriteFormulaBodies(ctx, raw, scope, func(old, scope string, a editdoc.Address, isCell bool) (string, error) {
			nextScope := scope
			if isCell {
				switch {
				case scope == m.from && m.source.contains(a.Row, a.Col):
					nextScope = m.to
				case scope == m.to && m.target.contains(a.Row, a.Col):
					return old, nil
				}
			}
			next, err := m.formula(old, scope, nextScope)
			if err == nil && isCell && old != next {
				roots[a] = editdoc.Value{}
			}
			return next, err
		})
		if err != nil {
			return fmt.Errorf("%s：%w", part, err)
		}
		if !bytes.Equal(raw, before) {
			updates[part] = raw
		}
	}
	return nil
}

func (p *xlsxPackage) checkTransferOwners(ctx context.Context, s rangeSheet, r cellRect) error {
	if err := p.checkRangeOwners(s, r); err != nil {
		return err
	}
	for _, a := range s.owners.arrays {
		if a.area.overlaps(r) {
			return fmt.Errorf("区域包含数组公式 %s，请先调整数组范围", pivotRectRef(a.area))
		}
	}
	for _, part := range p.links[s.sheet.Part] {
		if classifyPart(part).role != "comments" {
			continue
		}
		data, err := p.readPart(ctx, part)
		if err != nil {
			return err
		}
		notes, err := commentAddresses(ctx, data)
		if err != nil {
			return err
		}
		for ref := range notes {
			a, _ := parseRect(ref)
			if r.overlaps(a) {
				return fmt.Errorf("区域包含批注 %s，暂不能整体传输", ref)
			}
		}
	}
	// These records own cells separately from <c>. Silently copying only the
	// cell XML would leave a validation or hyperlink attached to the wrong cell.
	var extOwner string
	return walkXML(ctx, s.data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			var refs, label string
			switch e.local() {
			case "dataValidation":
				refs, label = attribute(t, "", "sqref"), "数据验证"
			case "conditionalFormatting":
				refs, label = attribute(t, "", "sqref"), "条件格式"
			case "hyperlink":
				refs, label = attribute(t, "", "ref"), "超链接"
			case "sparkline":
				label = "迷你图"
			}
			if t.Name.Space == x14NS && label != "" {
				extOwner = label
			}
			return transferOwnerRefs(r, refs, label)
		case xml.CharData:
			if extOwner != "" && e.local() == "sqref" {
				return transferOwnerRefs(r, string(t), extOwner)
			}
		case xml.EndElement:
			if e.local() == "dataValidation" || e.local() == "conditionalFormatting" || e.local() == "sparkline" {
				extOwner = ""
			}
		}
		return nil
	})
}

// Excel leaves existing pivot results visible until refresh. Mark a cache
// only when a changed cell or recalculated dependent belongs to its source.
func (p *xlsxPackage) markTransferPivotSources(ctx context.Context, roots map[editdoc.Address]editdoc.Value, calc *Calculation, updates map[string][]byte) error {
	for part, source := range p.pivotSources {
		touched := func(a editdoc.Address) bool { return a.Sheet == source.sheet && source.area.contains(a.Row, a.Col) }
		hit := false
		for a := range roots {
			if touched(a) {
				hit = true
				break
			}
		}
		if !hit {
			for a := range calc.Cells {
				if touched(a) {
					hit = true
					break
				}
			}
		}
		if !hit {
			continue
		}
		if !source.refreshable {
			return errors.New("透视缓存的来源已变化，但工作簿禁止刷新")
		}
		data, err := p.readPart(ctx, part)
		if err != nil {
			return err
		}
		ps, err := pivotRefreshPatches(ctx, data, false)
		if err != nil {
			return err
		}
		if len(ps) > 0 {
			updates[part], err = applyXML(ctx, data, ps)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func transferOwnerRefs(r cellRect, refs, label string) error {
	for _, ref := range strings.Fields(refs) {
		owned, ok := parseRect(strings.ReplaceAll(ref, "$", ""))
		if !ok {
			return errors.New("无法定位" + label + "区域")
		}
		if r.overlaps(owned) {
			return fmt.Errorf("区域包含%s %s，暂不能整体传输", label, ref)
		}
	}
	return nil
}
