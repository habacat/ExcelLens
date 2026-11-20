package editfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

type tableResize struct {
	table     xlsxTable
	old, next cellRect
	columns   []xlsxTableColumn
	markup    []columnMarkup
}

func (r cellRect) minus(b cellRect) []cellRect {
	if !r.overlaps(b) {
		return []cellRect{r}
	}
	x := r.intersection(b)
	var result []cellRect
	for _, a := range []cellRect{
		{r.firstRow, r.firstCol, x.firstRow - 1, r.lastCol}, {x.lastRow + 1, r.firstCol, r.lastRow, r.lastCol},
		{x.firstRow, r.firstCol, x.lastRow, x.firstCol - 1}, {x.firstRow, x.lastCol + 1, x.lastRow, r.lastCol},
	} {
		if a.firstRow <= a.lastRow && a.firstCol <= a.lastCol {
			result = append(result, a)
		}
	}
	return result
}
func (m tableResize) body(r cellRect) cellRect {
	r.firstRow += m.table.Headers
	r.lastRow -= m.table.Totals
	return r
}
func (m tableResize) totalsLane() cellRect {
	return cellRect{min(m.old.lastRow, m.next.lastRow), m.heightColumns().lo, max(m.old.lastRow, m.next.lastRow), m.heightColumns().hi}
}

func (m tableResize) physicalSource(a editdoc.Address) editdoc.Address {
	if m.table.Totals == 0 || m.old.lastRow == m.next.lastRow || !m.totalsLane().contains(a.Row, a.Col) {
		return a
	}
	a.Row = m.originalRow(a.Row)
	return a
}

func (p *xlsxPackage) resizeTable(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure, input *tableEntry) (opc.Receipt, error) {
	var none opc.Receipt
	t, next, err := p.layout.resizeTarget(op)
	if err != nil {
		return none, err
	}
	m := tableResize{table: t, old: t.bounds(), next: next}
	s, err := p.rangeSheet(ctx, t.Sheet)
	if err != nil {
		return none, err
	}
	if s.owners.protected {
		return none, errors.New("工作表受保护，无法调整表格范围")
	}
	if err = m.checkNotes(ctx, p, s); err != nil {
		return none, err
	}
	added := next.minus(m.old)
	touched := slices.Clone(added)
	if t.Totals > 0 && next.lastRow != m.old.lastRow {
		touched = append(touched, m.totalsLane())
	}
	for _, band := range touched {
		for _, merged := range s.owners.merges {
			if merged.overlaps(band) {
				return none, fmt.Errorf("表格范围与合并区域 %s 重叠", pivotRectRef(merged))
			}
		}
		for _, a := range s.owners.arrays {
			if a.area.overlaps(band) {
				return none, fmt.Errorf("表格范围与数组 %s 重叠", pivotRectRef(a.area))
			}
		}
		for _, pivot := range p.pivotRanges[t.Sheet] {
			if pivot.area.overlaps(band) {
				return none, errors.New(pivot.reason)
			}
		}
	}
	for _, col := range t.Columns {
		if col.Array || col.TotalArray {
			return none, errors.New("表格包含数组计算列，无法调整其范围")
		}
	}
	s.data, err = p.expandStructureGroups(ctx, s.data, t.Sheet, axisEdit{Structure: editdoc.Structure{Sheet: t.Sheet}})
	if err != nil {
		return none, err
	}
	addrs := map[editdoc.Address]bool{}
	for _, band := range touched {
		items, err := band.addresses(t.Sheet)
		if err != nil {
			return none, err
		}
		for _, a := range items {
			addrs[a] = true
		}
	}
	if len(addrs) > formats.EditBatchCells {
		return none, fmt.Errorf("本次调整涉及超过 %d 个单元格", formats.EditBatchCells)
	}
	addresses := slices.Collect(maps.Keys(addrs))
	// Capture the logical headers before editing any cells. Retained fields
	// always use their table definitions, including headerless tables.
	headers := map[int]editdoc.Value{}
	if t.Headers > 0 && next.colsSpan() != m.old.colsSpan() {
		var headerCells []editdoc.Address
		for col := next.firstCol; col <= next.lastCol; col++ {
			if col < m.old.firstCol || col > m.old.lastCol {
				headerCells = append(headerCells, editdoc.Address{Sheet: t.Sheet, Row: next.firstRow, Col: col})
			}
		}
		if len(headerCells) > 0 {
			_, err = p.rangeCells(ctx, s, headerCells, func(c cellTarget, _ []byte) (string, bool, error) {
				v := c.original.value
				if v.Kind == "formula" && c.original.cache != nil {
					v = *c.original.cache
					v.Style = c.original.value.Style
				}
				headers[c.address.Col] = v
				return "", false, nil
			})
			if err != nil {
				return none, err
			}
		}
		if input != nil {
			for _, change := range input.changes {
				if change.Row == next.firstRow && !m.old.colsSpan().has(change.Col) {
					v := change.Value
					if v.Style == "" {
						v.Style = headers[change.Col].Style
					}
					headers[change.Col] = v
				}
			}
		}
	}
	var label func(string, int) string
	for col, v := range headers {
		if v.Kind != "number" {
			continue
		}
		if label == nil {
			label, err = workbook.XLSXNumberLabels(p.files["xl/styles.xml"], p.date1904)
			if err != nil {
				return none, err
			}
		}
		style, _ := strconv.Atoi(v.Style)
		v.Raw, v.Kind = label(v.Raw, style), "text"
		headers[col] = v
	}
	m.columns, err = resizedColumns(t, next, headers)
	if err != nil {
		return none, err
	}
	if input != nil && t.Headers > 0 {
		// New field captions have already been converted to text and made
		// unique. The normal data-entry validator protects these headers.
		input.changes = slices.DeleteFunc(input.changes, func(c editdoc.Change) bool {
			return c.Row == next.firstRow && !m.old.contains(c.Row, c.Col) && next.contains(c.Row, c.Col)
		})
		if input.anchor != nil && input.anchor.Row == next.firstRow {
			input.anchor = nil
		}
	}
	updates := map[string][]byte{s.sheet.Part: s.data}
	updates[t.Part], err = p.resizeDefinition(ctx, t, next, m.columns)
	if err != nil {
		return none, err
	}
	if err = m.rebind(ctx, p, updates); err != nil {
		return none, err
	}
	definition, markup, err := parseTable(ctx, updates[t.Part], t.Sheet, t.Part, "")
	if err != nil {
		return none, err
	}
	m.columns = definition.Columns
	m.markup = markup
	s.data = updates[s.sheet.Part]
	if t.Filtered && (!m.old.colsSpan().inside(next.colsSpan()) || next.lastRow < m.old.lastRow) {
		before, err := p.readPart(ctx, t.Part)
		if err != nil {
			return none, err
		}
		s.data, err = m.filterVisibility(ctx, p, s, before)
		if err != nil {
			return none, err
		}
	}
	if len(addresses) > 0 {
		s.data, err = m.resizeCells(ctx, p, s, addresses, input != nil, updates)
		if err != nil {
			return none, err
		}
	}
	updates[s.sheet.Part], err = m.resizeRules(ctx, s.data)
	if err != nil {
		return none, err
	}
	roots := map[editdoc.Address]editdoc.Value{}
	for _, a := range addresses {
		roots[a] = editdoc.Value{}
	}
	if input != nil && len(input.changes) > 0 {
		if err = input.apply(ctx, p, updates, roots); err != nil {
			return none, err
		}
	}
	return p.finishCellStructure(ctx, dst, src, updates, roots)
}

func (m tableResize) resizeCells(ctx context.Context, p *xlsxPackage, s rangeSheet, addresses []editdoc.Address, appendInput bool, updates map[string][]byte) ([]byte, error) {
	oldBody, newBody := m.body(m.old), m.body(m.next)
	cells := make(map[editdoc.Address]transferCell, len(addresses))
	_, err := p.rangeCells(ctx, s, addresses, func(c cellTarget, raw []byte) (string, bool, error) {
		// A direct entry below a total goes into the new data slot. Leave an
		// existing footer intact unless the user explicitly resizes the table.
		if appendInput && m.table.Totals > 0 && c.address.Row > m.old.lastRow && c.original.value.Kind != "missing" && c.original.value.Kind != "blank" {
			return "", false, fmt.Errorf("%s 已有内容，请先调整表格范围或移开表尾数据", addressRef(c.address))
		}
		cells[c.address] = transferCell{c.original, raw, s.owners.namespaces.at(c.address.Row)}
		return "", false, nil
	})
	if err != nil {
		return nil, err
	}
	styles, err := m.dataStyles(ctx, p, s, cells, updates)
	if err != nil {
		return nil, err
	}
	return p.rangeCells(ctx, s, addresses, func(c cellTarget, _ []byte) (string, bool, error) {
		a := c.address
		origin := m.physicalSource(a)
		cell := cells[origin]
		if m.table.Headers > 0 && a.Row == m.next.firstRow && !m.old.contains(a.Row, a.Col) {
			return resizedCell(a, c.original.value.Style, editdoc.Value{Kind: "text", Raw: m.columns[a.Col-m.next.firstCol].Name}), true, nil
		}
		v := cell.node.value
		if newBody.contains(a.Row, a.Col) && !oldBody.contains(a.Row, a.Col) && a.Col >= m.old.firstCol && a.Col <= m.old.lastCol {
			col := m.columns[a.Col-m.next.firstCol]
			style := styles[a]
			if (v.Kind == "missing" || v.Kind == "blank") && col.Formula != "" {
				expr, err := copyRangeFormula(col.Formula, a.Row-oldBody.firstRow, 0)
				if err != nil {
					return "", false, err
				}
				if style == "" {
					style = v.Style
				}
				return resizedCell(a, style, editdoc.Value{Kind: "formula", Raw: expr}), true, nil
			}
			if style != "" && v.Style != style {
				if v.Kind == "missing" {
					v.Kind = "blank"
				}
				if v.Kind == "blank" || v.Kind == "number" || v.Kind == "boolean" || v.Kind == "date" {
					return resizedCell(a, style, v), true, nil
				}
				// Keep rich runs, shared strings and existing formula markup.
				raw, _, err := cell.place(ctx, a, origin, true, c.original, s.owners.namespaces.at(a.Row))
				if err != nil {
					return "", false, err
				}
				return cellWithStyle(ctx, raw, style)
			}
		}
		if origin == a {
			return "", false, nil
		}
		return cell.place(ctx, a, origin, true, c.original, s.owners.namespaces.at(a.Row))
	})
}

func resizedCell(a editdoc.Address, style string, v editdoc.Value) string {
	var b strings.Builder
	if style == "0" {
		style = ""
	}
	cellXML(&b, a, style, v)
	return b.String()
}
