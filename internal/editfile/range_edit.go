package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

func (r cellRect) overlaps(s cellRect) bool {
	return r.firstRow <= s.lastRow && s.firstRow <= r.lastRow && r.firstCol <= s.lastCol && s.firstCol <= r.lastCol
}

func (r cellRect) addresses(sheet string) ([]editdoc.Address, error) {
	height, width := r.lastRow-r.firstRow+1, r.lastCol-r.firstCol+1
	if height < 1 || width < 1 || height > formats.EditBatchCells/width {
		return nil, fmt.Errorf("一次范围编辑最多 %d 格", formats.EditBatchCells)
	}
	result := make([]editdoc.Address, 0, height*width)
	for row := r.firstRow; row <= r.lastRow; row++ {
		for col := r.firstCol; col <= r.lastCol; col++ {
			result = append(result, editdoc.Address{Sheet: sheet, Row: row, Col: col})
		}
	}
	return result, nil
}

func addressRef(a editdoc.Address) string { return workbook.ColumnName(a.Col) + strconv.Itoa(a.Row) }

type gridOwners struct {
	merges                     []cellRect
	arrays                     map[editdoc.Address]sourceFormulaTag
	mergeFrom, mergeTo, insert int
	protected                  bool
}

func sheetOwners(ctx context.Context, data []byte, sheet string) (gridOwners, error) {
	g := gridOwners{arrays: map[editdoc.Address]sourceFormulaTag{}}
	var cell editdoc.Address
	var dynamic bool
	// CT_Worksheet children preceding mergeCells, ECMA-376 18.3.1.99.
	before := []string{"sheetPr", "dimension", "sheetViews", "sheetFormatPr", "cols", "sheetData", "sheetCalcPr", "sheetProtection", "protectedRanges", "scenarios", "autoFilter", "sortState", "dataConsolidate", "customSheetViews"}
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch t := e.token.(type) {
		case xml.StartElement:
			switch {
			case len(e.stack) == 2 && e.local() == "sheetProtection":
				g.protected = isTrue(attribute(t, "", "sheet"))
			case len(e.stack) == 2 && e.local() == "mergeCells":
				g.mergeFrom = e.from
			case e.parent() == "mergeCells" && e.local() == "mergeCell":
				r, ok := parseRect(attribute(t, "", "ref"))
				if !ok {
					return ErrXLSX
				}
				g.merges = append(g.merges, r)
			case e.parent() == "row" && e.local() == "c":
				col, row, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				cell = editdoc.Address{Sheet: sheet, Row: row, Col: col}
				dynamic = attribute(t, "", "cm") != ""
			case e.parent() == "c" && e.local() == "f" && attribute(t, "", "t") == "array":
				r, ok := parseRect(attribute(t, "", "ref"))
				if !ok || r.firstRow != cell.Row || r.firstCol != cell.Col {
					return ErrXLSX
				}
				g.arrays[cell] = sourceFormulaTag{kind: "array", area: r, dynamic: dynamic}
			}
		case xml.EndElement:
			if len(e.stack) == 2 {
				if slices.Contains(before, e.local()) {
					g.insert = e.to
				}
				if e.local() == "mergeCells" {
					g.mergeTo = e.to
				}
			}
		}
		return nil
	})
	return g, err
}

type rangeSheet struct {
	sheet  XLSXSheet
	data   []byte
	owners gridOwners
}

func (p *xlsxPackage) rangeSheet(ctx context.Context, name string) (rangeSheet, error) {
	for _, sheet := range p.layout.Sheets {
		if sheet.Name != name {
			continue
		}
		if sheet.ReadOnly != "" {
			return rangeSheet{}, errors.New(sheet.ReadOnly)
		}
		raw, err := p.readPart(ctx, sheet.Part)
		if err != nil {
			return rangeSheet{}, err
		}
		own, err := sheetOwners(ctx, raw, name)
		return rangeSheet{sheet, raw, own}, err
	}
	return rangeSheet{}, ErrTarget
}

func (p *xlsxPackage) checkRangeOwners(s rangeSheet, r cellRect) error {
	if s.owners.protected {
		return errors.New("此工作表受保护，无法更改区域结构")
	}
	for _, t := range p.layout.Tables {
		if t.Sheet == s.sheet.Name && t.bounds().overlaps(r) {
			return fmt.Errorf("区域与 Excel 表格 %s 重叠", t.Name)
		}
	}
	for _, owner := range p.pivotRanges[s.sheet.Name] {
		if owner.area.overlaps(r) {
			return errors.New(owner.reason)
		}
	}
	return nil
}

// Apply coordinate-local XML replacements through the same sparse row slots
// as ordinary cell edits. Existing rows, other cells, and unknown parts retain
// their original bytes. The callback may leave any requested cell untouched.
func (p *xlsxPackage) rangeCells(ctx context.Context, s rangeSheet, addresses []editdoc.Address, render func(cellTarget, []byte) (string, bool, error)) ([]byte, error) {
	t, err := newSheetTargets(s.sheet, addresses)
	if err != nil {
		return nil, err
	}
	data := s.data
	if _, err = p.scanSheet(ctx, data, s.sheet, t, nil, nil, nil); err != nil {
		return nil, err
	}
	var ps []xmlPatch
	dimension := t.dimension
	for _, row := range t.rows {
		var added strings.Builder
		for _, cell := range row.cells {
			value, change, err := render(cell, data)
			if err != nil {
				return nil, err
			}
			if !change {
				continue
			}
			if cell.original.value.Kind == "missing" {
				r, c := cell.address.Row, cell.address.Col
				dimension = cellRect{firstUsed(dimension.firstRow, r), firstUsed(dimension.firstCol, c), max(dimension.lastRow, r), max(dimension.lastCol, c)}
				block := (r - 1) / 16
				ps = append(ps, t.spanBlocks[block]...)
				delete(t.spanBlocks, block)
			}
			if row.insert || row.closeTag != "" {
				added.WriteString(value)
			} else {
				ps = append(ps, xmlPatch{cell.original.from, cell.original.to, value})
			}
		}
		if added.Len() != 0 {
			value := ">" + added.String() + row.closeTag
			if row.insert {
				value = fmt.Sprintf(`<row xmlns="%s" r="%d">%s</row>`, mainNS, row.row, added.String())
			}
			ps = append(ps, xmlPatch{row.from, row.to, value})
		}
	}
	if len(ps) > 0 && t.emptyDataClose != "" {
		ps = append(ps, xmlPatch{t.emptyDataStart, t.emptyDataStart, ">"}, xmlPatch{t.emptyDataStart, t.emptyDataStart + 2, t.emptyDataClose})
	}
	if t.dimensionEnd != 0 && dimension != t.dimension {
		more, err := setXMLAttribute(data, t.dimensionStart, t.dimensionEnd, "ref", pivotRectRef(dimension))
		if err != nil {
			return nil, err
		}
		ps = append(ps, more...)
	}
	return applyXML(ctx, s.data, ps)
}

func (g gridOwners) writeMerges(ctx context.Context, data []byte, merges []cellRect) ([]byte, error) {
	var out strings.Builder
	if len(merges) > 0 {
		fmt.Fprintf(&out, `<mergeCells xmlns="%s" count="%d">`, mainNS, len(merges))
		for _, m := range merges {
			fmt.Fprintf(&out, `<mergeCell ref="%s"/>`, pivotRectRef(m))
		}
		out.WriteString("</mergeCells>")
	}
	from, to := g.mergeFrom, g.mergeTo
	if to == 0 {
		from, to = g.insert, g.insert
	}
	return applyXML(ctx, data, []xmlPatch{{from, to, out.String()}})
}

func (p *xlsxPackage) rewriteMerge(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
	r, ok := parseRect(op.Range)
	if !ok {
		return opc.Receipt{}, ErrTarget
	}
	s, err := p.rangeSheet(ctx, op.Sheet)
	if err != nil {
		return opc.Receipt{}, err
	}
	if err = p.checkRangeOwners(s, r); err != nil {
		return opc.Receipt{}, err
	}
	var keep []cellRect
	removed := 0
	for _, m := range s.owners.merges {
		if !r.overlaps(m) {
			keep = append(keep, m)
			continue
		}
		if op.Kind == "mergeCells" && !r.covers(m) {
			return opc.Receipt{}, fmt.Errorf("请完整选择已合并区域 %s", pivotRectRef(m))
		}
		removed++
	}
	if op.Kind == "unmergeCells" {
		if removed == 0 {
			return opc.Receipt{}, editdoc.ErrNoChange
		}
		next, err := s.owners.writeMerges(ctx, s.data, keep)
		if err != nil {
			return opc.Receipt{}, err
		}
		return replaceParts(ctx, dst, p.zip, map[string][]byte{s.sheet.Part: next})
	}
	if r.single() || removed == 1 && slices.Contains(s.owners.merges, r) {
		return opc.Receipt{}, editdoc.ErrNoChange
	}
	for _, a := range s.owners.arrays {
		if a.area.overlaps(r) {
			return opc.Receipt{}, errors.New("数组公式范围不能合并")
		}
	}
	addresses, err := r.addresses(s.sheet.Name)
	if err != nil {
		return opc.Receipt{}, err
	}
	// Read the whole selection before touching the archive. Excel moves the first
	// nonempty source (in row-major order) if the top-left cell is empty.
	read := p.readCells
	if p.layout.Native {
		read = p.readOfficeCells
	}
	contents, err := read(ctx, addresses)
	if err != nil {
		return opc.Receipt{}, err
	}
	var first editdoc.Address
	var occupied []editdoc.Address
	for _, a := range addresses {
		c := contents[a]
		if c.ReadOnly != "" && c.ReadOnly != lockMergedMember {
			return opc.Receipt{}, errors.New(c.ReadOnly)
		}
		if c.Value.Kind != "blank" && c.Value.Kind != "missing" {
			occupied = append(occupied, a)
			if len(occupied) == 1 {
				first = a
			}
		}
	}
	if len(occupied) > 1 && !op.ReplaceContents {
		return opc.Receipt{}, fmt.Errorf("合并后只保留 %s 的内容，请确认后再合并", addressRef(first))
	}
	anchor := addresses[0]
	changes := map[editdoc.Address]editdoc.Value{}
	for _, a := range occupied {
		if a != anchor {
			changes[a] = editdoc.Value{Kind: "blank"}
		}
	}
	move := len(occupied) > 0 && first != anchor
	if move {
		changes[anchor] = contents[first].Value
	}
	next := s.data
	if len(changes) > 0 {
		if s.sheet.Formulas > 0 && bytes.Contains(next, []byte("shared")) {
			next, err = p.expandStructureGroups(ctx, next, s.sheet.Name, axisEdit{Structure: op})
			if err != nil {
				return opc.Receipt{}, err
			}
			s.data = next
		}
		var original string
		if move {
			// Capture the source XML to retain rich strings, the data type, the
			// number format, and the original formula expression when relocated.
			_, err = p.rangeCells(ctx, s, []editdoc.Address{first}, func(c cellTarget, src []byte) (string, bool, error) {
				cell := src[c.original.from:c.original.to]
				ps, e := setXMLAttribute(cell, 0, c.original.tagEnd-c.original.from, "r", addressRef(anchor))
				if e != nil {
					return "", false, e
				}
				b, e := applyXML(ctx, cell, ps)
				original = string(b)
				return "", false, e
			})
			if err != nil {
				return opc.Receipt{}, err
			}
		}
		next, err = p.rangeCells(ctx, s, addresses, func(c cellTarget, _ []byte) (string, bool, error) {
			if _, change := changes[c.address]; !change {
				return "", false, nil
			}
			if c.address == anchor && move {
				return original, true, nil
			}
			var b strings.Builder
			cellXML(&b, c.address, c.original.style, editdoc.Value{Kind: "blank"})
			return b.String(), true, nil
		})
		if err != nil {
			return opc.Receipt{}, err
		}
	}
	owners, err := sheetOwners(ctx, next, op.Sheet)
	if err != nil {
		return opc.Receipt{}, err
	}
	next, err = owners.writeMerges(ctx, next, append(keep, r))
	if err != nil {
		return opc.Receipt{}, err
	}
	updates := map[string][]byte{s.sheet.Part: next}
	return p.finishMergedValues(ctx, dst, src, updates, changes, first, anchor, move)
}
