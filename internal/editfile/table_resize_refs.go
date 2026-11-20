package editfile

import (
	"bytes"
	"context"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// Excel adjusts height across the old fields when widening, and across
// surviving fields when narrowing. Remaining fields retain their sheet columns.
func (m tableResize) heightColumns() refSpan {
	if m.next.lastCol-m.next.firstCol < m.old.lastCol-m.old.firstCol {
		return refSpan{max(m.old.firstCol, m.next.firstCol), min(m.old.lastCol, m.next.lastCol)}
	}
	return m.old.colsSpan()
}
func (m tableResize) originalRow(row int) int {
	if m.table.Totals == 0 || m.old.lastRow == m.next.lastRow {
		return row
	}
	if row == m.next.lastRow {
		return m.old.lastRow
	}
	if row >= min(m.old.lastRow, m.next.lastRow) && row <= max(m.old.lastRow, m.next.lastRow) {
		if m.next.lastRow > m.old.lastRow {
			return row + 1
		}
		return row - 1
	}
	return row
}
func (m tableResize) movedRow(row int) int {
	m.old, m.next = m.next, m.old
	return m.originalRow(row)
}
func (m tableResize) referenceArea(r cellRect, pair bool) cellRect {
	old, newEnd := m.old.lastRow, m.next.lastRow
	if old == newEnd {
		return r
	}
	cols := m.heightColumns()
	lo, hi := min(old, newEnd), max(old, newEnd)
	if r.colsSpan().inside(cols) {
		if pair && r.lastRow == old-m.table.Totals && (r.firstRow == m.old.firstRow || r.firstRow == m.old.firstRow+m.table.Headers) {
			r.lastRow = newEnd - m.table.Totals
			return r
		}
		if m.table.Totals == 0 {
			return r
		}
		switch {
		case r.firstRow == old:
			r.firstRow, r.lastRow = newEnd, max(newEnd, m.movedRow(r.lastRow))
		case r.lastRow == old:
			r.firstRow, r.lastRow = min(newEnd, m.movedRow(r.firstRow)), newEnd
		case r.firstRow > lo || r.lastRow < hi:
			r.firstRow, r.lastRow = m.movedRow(r.firstRow), m.movedRow(r.lastRow)
		}
		return r
	}
	if m.table.Totals == 0 {
		return r
	}
	// References spanning an edge of the field band retain their surviving
	// strip. Excel applies this at both ends of the displaced record block.
	r = r.without(cellRect{lo, cols.lo, hi - 1, cols.hi}, newEnd > old)
	return r.without(cellRect{hi + 1, cols.lo, min(formats.ExcelRows, hi+hi-lo), cols.hi}, newEnd < old)
}
func (m tableResize) rebindFormula(body, scope string) (string, error) {
	return calculator.RewriteReferences(body, func(sheet, q, raw string) (string, error) {
		if sheet == "" {
			sheet = scope
		}
		if !strings.EqualFold(sheet, m.table.Sheet) || raw == "" {
			return q + raw, nil
		}
		first, last, pair := strings.Cut(raw, ":")
		if !pair {
			last = first
		}
		a, b := axisAddress(first), axisAddress(last)
		if a.row == 0 || a.col == 0 {
			return q + raw, nil
		}
		r := cellRect{min(a.row, b.row), min(a.col, b.col), max(a.row, b.row), max(a.col, b.col)}
		next := m.referenceArea(r, pair)
		if next == r {
			return q + raw, nil
		}
		if next == (cellRect{}) {
			return q + "#REF!", nil
		}
		top, bottom, left, right := next.firstRow, next.lastRow, next.firstCol, next.lastCol
		if a.row > b.row {
			top, bottom = bottom, top
		}
		if a.col > b.col {
			left, right = right, left
		}
		v := a.at(top, left)
		if pair {
			v += ":" + b.at(bottom, right)
		}
		return q + v, nil
	})
}

func (m tableResize) tableOperand(raw string, local, detached bool) string {
	name, tail, _ := strings.Cut(raw, "[")
	if name == "" && !local || name != "" && !strings.EqualFold(name, m.table.Name) {
		return raw
	}
	inner, ok := strings.CutSuffix(tail, "]")
	if !ok {
		return raw
	}
	_, fields, ok := tableSelector(inner)
	if !ok {
		return raw
	}
	for _, f := range fields {
		for i, c := range m.table.Columns {
			if strings.EqualFold(c.Name, tableCaptionUnescape.Replace(f)) && !m.next.colsSpan().has(m.old.firstCol+i) {
				return "#REF!"
			}
		}
	}
	if name == "" && detached {
		return m.table.Name + raw
	}
	return raw
}

func (m tableResize) rebind(ctx context.Context, p *xlsxPackage, updates map[string][]byte) error {
	// A pure width expansion leaves all existing field identities and all
	// A1 addresses intact. The new layout still rebinds whole-table formulas.
	if m.old.lastRow == m.next.lastRow && m.old.colsSpan().inside(m.next.colsSpan()) {
		return nil
	}
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
		raw, held := updates[part]
		var err error
		if !held {
			raw, err = p.readPart(ctx, part)
			if err != nil {
				return err
			}
		}
		if !held && classifyPart(part).role == "sheet" {
			raw, err = p.expandStructureGroups(ctx, raw, scope, axisEdit{Structure: editdoc.Structure{Sheet: m.table.Sheet}})
			if err != nil {
				return err
			}
		}
		before := raw
		raw, err = p.rewriteFormulaBodies(ctx, raw, scope, func(body, scope string, a editdoc.Address, cell bool) (string, error) {
			local := part == m.table.Part || cell && a.Sheet == m.table.Sheet && m.old.contains(a.Row, a.Col)
			dest := a
			if m.table.Totals > 0 && m.heightColumns().has(a.Col) {
				dest.Row = m.movedRow(a.Row)
			}
			body = calculator.RewriteTableReferences(body, func(token string) string {
				return m.tableOperand(token, local, cell && local && !m.next.contains(dest.Row, dest.Col))
			})
			return m.rebindFormula(body, scope)
		})
		if err != nil {
			return err
		}
		if !bytes.Equal(before, raw) {
			updates[part] = raw
		}
	}
	return nil
}
