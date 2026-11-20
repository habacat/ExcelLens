package editfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

func newRangeTransfer(op editdoc.Structure) (rangeTransfer, error) {
	m := rangeTransfer{from: op.Sheet, to: op.DestinationSheet, cut: op.Kind == "cutRange"}
	r, ok := parseRect(op.Range)
	dest, valid := parseRect(op.Destination)
	if !ok || !valid || r.lastCol > formats.ExcelColumns || dest.lastCol > formats.ExcelColumns || m.from == "" || m.to == "" {
		return m, ErrTarget
	}
	h, w := r.lastRow-r.firstRow+1, r.lastCol-r.firstCol+1
	dh, dw := dest.lastRow-dest.firstRow+1, dest.lastCol-dest.firstCol+1
	if m.cut || dh <= h && dw <= w {
		dest.lastRow, dest.lastCol = dest.firstRow+h-1, dest.firstCol+w-1
	} else if dh%h != 0 || dw%w != 0 {
		return m, errors.New("请选择一个起点或与复制区域大小匹配的矩形")
	}
	if dest.lastRow > formats.ExcelRows || dest.lastCol > formats.ExcelColumns {
		return m, ErrTarget
	}
	if dest.lastRow-dest.firstRow+1 > formats.EditBatchCells/(dest.lastCol-dest.firstCol+1) {
		return m, fmt.Errorf("一次范围编辑最多 %d 格", formats.EditBatchCells)
	}
	m.source, m.target = r, dest
	return m, nil
}

func (m rangeTransfer) mergeLayout(source, target []cellRect, sameSource bool) ([]cellRect, []cellRect, error) {
	// Excel expands a cut destination across intersecting merges before
	// removing those merges. This can reach another source header on the
	// next row even when none of its cells is in the nominal destination.
	cleared := m.target
	if m.cut {
		for {
			before := cleared
			for _, merge := range target {
				if cleared.overlaps(merge) {
					cleared = cleared.hull(merge)
				}
			}
			if cleared == before {
				break
			}
		}
	}
	var carried, src, dst []cellRect
	for _, r := range source {
		if !m.source.overlaps(r) {
			src = append(src, r)
			continue
		}
		if !m.source.covers(r) {
			return nil, nil, fmt.Errorf("请完整选择合并区域 %s", pivotRectRef(r))
		}
		// Excel unmerges an existing destination before it lays down the
		// clipboard. On the same sheet that merge can also belong to the source.
		if !sameSource || !cleared.overlaps(r) {
			carried = append(carried, r)
		}
		if !m.cut {
			src = append(src, r)
		}
	}
	if m.cut && sameSource {
		target = src
	}
	for _, r := range target {
		if !cleared.overlaps(r) {
			dst = append(dst, r)
			continue
		}
		if !m.cut && !m.target.covers(r) {
			return nil, nil, fmt.Errorf("粘贴会覆盖合并区域 %s 的一部分", pivotRectRef(r))
		}
	}
	h, w := m.source.lastRow-m.source.firstRow+1, m.source.lastCol-m.source.firstCol+1
	for row := m.target.firstRow; row <= m.target.lastRow; row += h {
		for col := m.target.firstCol; col <= m.target.lastCol; col += w {
			for _, r := range carried {
				dst = append(dst, r.offset(row-m.source.firstRow, col-m.source.firstCol))
			}
		}
	}
	if len(dst) > formats.EditXLSXRangeLimit {
		return nil, nil, fmt.Errorf("合并区域数量超过 %d 个", formats.EditXLSXRangeLimit)
	}
	return src, dst, nil
}

type transferCell struct {
	node     sheetCell
	data     []byte
	bindings map[string]string
}

func (c transferCell) place(ctx context.Context, addr editdoc.Address, origin editdoc.Address, cut bool, dest sheetCell, bindings map[string]string) (string, bool, error) {
	if c.node.value.Kind == "missing" {
		if dest.value.Kind == "missing" && dest.value.Style == c.node.value.Style {
			return "", false, nil
		}
		var out strings.Builder
		cellXML(&out, addr, c.node.value.Style, editdoc.Value{Kind: "blank"})
		return out.String(), true, nil
	}
	node := c.node
	raw := c.data[node.from:node.to]
	ps, err := setXMLAttribute(raw, 0, node.tagEnd-node.from, "r", addressRef(addr))
	if err != nil {
		return "", false, err
	}
	extra, err := transferredBindings(raw, node.tagEnd-node.from, c.bindings, bindings)
	if err != nil {
		return "", false, err
	}
	ps = append(ps, extra...)
	if !cut && node.value.Kind == "formula" {
		expr, err := copyRangeFormula(node.value.Raw, addr.Row-origin.Row, addr.Col-origin.Col)
		if err != nil {
			return "", false, err
		}
		if expr != node.value.Raw {
			ps = append(ps, xmlPatch{node.bodyFrom - node.from, node.bodyTo - node.from, escapedXML(strings.TrimPrefix(expr, "="))})
		}
	}
	out, err := applyXML(ctx, raw, ps)
	return string(out), true, err
}

func (p *xlsxPackage) transferSheet(ctx context.Context, name string, area cellRect) (rangeSheet, error) {
	s, err := p.rangeSheet(ctx, name)
	if err != nil {
		return s, err
	}
	if err = p.checkTransferOwners(ctx, s, area); err != nil {
		return s, err
	}
	s.data, err = p.expandStructureGroups(ctx, s.data, name, axisEdit{Structure: editdoc.Structure{Sheet: name}})
	return s, err
}

func (p *xlsxPackage) transferRange(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure, prior *xlsxPackage) (opc.Receipt, error) {
	var empty opc.Receipt
	m, err := newRangeTransfer(op)
	if err != nil {
		return empty, err
	}
	same := prior == nil && m.from == m.to
	if same && m.source == m.target {
		return empty, editdoc.ErrNoChange
	}
	sourceBook := prior
	if sourceBook == nil {
		sourceBook = p
	}
	source, err := sourceBook.transferSheet(ctx, m.from, m.source)
	if err != nil {
		return empty, err
	}
	target := source
	if !same {
		target, err = p.transferSheet(ctx, m.to, m.target)
	} else {
		err = p.checkTransferOwners(ctx, target, m.target)
	}
	if err != nil {
		return empty, err
	}
	srcMerges, dstMerges, err := m.mergeLayout(source.owners.merges, target.owners.merges, same)
	if err != nil {
		return empty, err
	}
	updates := map[string][]byte{}
	roots := map[editdoc.Address]editdoc.Value{}
	sourceAddrs, err := m.source.addresses(m.from)
	if err != nil {
		return empty, err
	}
	destAddrs, err := m.target.addresses(m.to)
	if err != nil {
		return empty, err
	}
	if m.cut {
		updates[source.sheet.Part], updates[target.sheet.Part] = source.data, target.data
		if err = m.rebindParts(ctx, p, updates, roots); err != nil {
			return empty, err
		}
		source.data, target.data = updates[source.sheet.Part], updates[target.sheet.Part]
	}
	cells := make(map[editdoc.Address]transferCell, len(sourceAddrs))
	_, err = sourceBook.rangeCells(ctx, source, sourceAddrs, func(c cellTarget, raw []byte) (string, bool, error) {
		if err := transferCellAllowed(c); err != nil {
			return "", false, err
		}
		cells[c.address] = transferCell{c.original, raw, source.owners.namespaces.at(c.address.Row)}
		return "", false, nil
	})
	if err != nil {
		return empty, err
	}
	if prior != nil {
		if err = p.checkTransferStyles(ctx, prior, cells); err != nil {
			return empty, err
		}
	}
	if m.cut {
		for _, a := range sourceAddrs {
			roots[a] = editdoc.Value{}
		}
		if !same {
			raw, err := p.rangeCells(ctx, source, sourceAddrs, func(c cellTarget, _ []byte) (string, bool, error) { return "", c.original.value.Kind != "missing", nil })
			if err != nil {
				return empty, err
			}
			updates[source.sheet.Part], err = transferMerges(ctx, source, raw, srcMerges)
			if err != nil {
				return empty, err
			}
		}
	}
	write := map[editdoc.Address]bool{}
	for _, a := range destAddrs {
		write[a] = true
		roots[a] = editdoc.Value{}
	}
	if same && m.cut {
		for _, a := range sourceAddrs {
			write[a] = true
		}
	}
	h, w := m.source.lastRow-m.source.firstRow+1, m.source.lastCol-m.source.firstCol+1
	raw, err := p.rangeCells(ctx, target, slices.Collect(maps.Keys(write)), func(c cellTarget, _ []byte) (string, bool, error) {
		if err := transferCellAllowed(c); err != nil {
			return "", false, err
		}
		a := c.address
		if !m.target.contains(a.Row, a.Col) {
			return "", c.original.value.Kind != "missing", nil
		}
		origin := editdoc.Address{Sheet: m.from, Row: m.source.firstRow + (a.Row-m.target.firstRow)%h, Col: m.source.firstCol + (a.Col-m.target.firstCol)%w}
		return cells[origin].place(ctx, a, origin, m.cut, c.original, target.owners.namespaces.at(a.Row))
	})
	if err != nil {
		return empty, err
	}
	updates[target.sheet.Part], err = transferMerges(ctx, target, raw, dstMerges)
	if err != nil {
		return empty, err
	}
	return p.finishCellStructure(ctx, dst, src, updates, roots)
}

func transferCellAllowed(c cellTarget) error {
	if reason := c.original.readOnly; reason != "" && reason != lockMergedMember {
		return fmt.Errorf("%s!%s：%s", c.address.Sheet, addressRef(c.address), reason)
	}
	return nil
}

func transferMerges(ctx context.Context, s rangeSheet, data []byte, merges []cellRect) ([]byte, error) {
	if slices.Equal(s.owners.merges, merges) {
		return data, nil
	}
	owners, err := sheetOwners(ctx, data, s.sheet.Name)
	if err != nil {
		return nil, err
	}
	return owners.writeMerges(ctx, data, merges)
}

func (p *xlsxPackage) finishCellStructure(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, updates map[string][]byte, roots map[editdoc.Address]editdoc.Value) (opc.Receipt, error) {
	var empty opc.Receipt
	// Bind formulas, names, and array barriers against the finished layout.
	// An overlay on the old dependency graph would still resolve moved names
	// at their original addresses, even if all cell formulas had been patched.
	next, err := openXLSXPackage(src, src.Size())
	if err != nil {
		return empty, err
	}
	next.revisedParts = updates
	next.layout.Native, next.layout.Structured = p.layout.Native, p.layout.Structured
	if next.layout.Native {
		if err = next.checkCalculationSize(); err != nil {
			return empty, err
		}
	}
	if err = next.inspect(ctx); err != nil {
		return empty, err
	}
	if err = next.inspectGrid(ctx); err != nil {
		return empty, err
	}
	book, err := next.buildCalculationBook(ctx)
	if err != nil {
		return empty, err
	}
	for a := range roots {
		book.missing = append(book.missing, a)
	}
	_, rebind := updates["xl/workbook.xml"]
	for _, table := range next.layout.Tables {
		if _, ok := updates[table.Part]; ok {
			rebind = true
			break
		}
	}
	if rebind {
		for a := range book.formulas {
			book.missing = append(book.missing, a)
		}
	}
	calc, err := book.Calculate(ctx, nil, nil)
	if err != nil {
		return empty, err
	}
	if err = next.markTransferPivotSources(ctx, roots, calc, updates); err != nil {
		return empty, err
	}
	if err = next.writeArrayResults(ctx, book, calc, roots, updates); err != nil {
		return empty, err
	}
	return p.writeValueStructure(ctx, dst, updates)
}

// Structural copies on the same undo branch share the original string table
// and cell formats. A newly composed format can belong to a discarded branch.
func (p *xlsxPackage) checkTransferStyles(ctx context.Context, old *xlsxPackage, cells map[editdoc.Address]transferCell) error {
	if !slices.Equal(old.shared, p.shared) {
		return errors.New("工作簿字符串表已变化，请重新复制")
	}
	for _, c := range cells {
		if !p.validStyle(c.node.value.Style) {
			return errors.New("工作簿样式已变化，请重新复制")
		}
	}
	if p.files["xl/styles.xml"] != nil && old.files["xl/styles.xml"] != nil {
		a, err := p.readPart(ctx, "xl/styles.xml")
		if err != nil {
			return err
		}
		b, err := old.readPart(ctx, "xl/styles.xml")
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return errors.New("工作簿样式已变化，请重新复制")
		}
	}
	return nil
}
