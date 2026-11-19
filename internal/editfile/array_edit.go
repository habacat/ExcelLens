package editfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

func (p *xlsxPackage) rewriteArray(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
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
	anchor := editdoc.Address{Sheet: op.Sheet, Row: r.firstRow, Col: r.firstCol}
	old, exists := s.owners.arrays[anchor]
	clear := op.Kind == "clearArray"
	dynamic := op.Kind == "spillFormula"
	if clear && (!exists || !r.single() && r != old.area) {
		return opc.Receipt{}, errors.New("请选择完整数组或其左上角")
	}
	if dynamic && !r.single() && (!exists || r != old.area) {
		return opc.Receipt{}, errors.New("动态数组从一个起始单元格溢出")
	}
	if dynamic {
		r = anchorRect(anchor)
	}
	for a, other := range s.owners.arrays {
		if a != anchor && r.overlaps(other.area) {
			return opc.Receipt{}, fmt.Errorf("与 %s 的数组重叠", addressRef(a))
		}
	}
	for _, m := range s.owners.merges {
		if m.overlaps(r) {
			return opc.Receipt{}, fmt.Errorf("请先取消 %s 的合并", pivotRectRef(m))
		}
	}
	cells, err := r.addresses(op.Sheet)
	if err != nil {
		return opc.Receipt{}, err
	}
	b, err := NewCalculationBook(ctx, src, p.layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	read := p.readCells
	if p.layout.Native {
		read = p.readOfficeCells
	}
	originals, err := read(ctx, cells)
	if err != nil {
		return opc.Receipt{}, err
	}
	for _, a := range cells {
		c := originals[a]
		owned := exists && old.area.contains(a.Row, a.Col)
		if c.ReadOnly != "" && !(owned && c.ReadOnly == lockArrayMember) {
			return opc.Receipt{}, errors.New(c.ReadOnly)
		}
		if !clear && a != anchor && !owned && c.Value.Kind != "blank" && c.Value.Kind != "missing" && !op.ReplaceContents {
			return opc.Receipt{}, fmt.Errorf("选区内 %s 已有内容，请确认替换", addressRef(a))
		}
	}
	patches := map[editdoc.Address]editdoc.Value{}
	request := map[editdoc.Address]sourceFormulaTag{}
	if clear {
		cells, err = old.area.addresses(op.Sheet)
		if err != nil {
			return opc.Receipt{}, err
		}
		for _, a := range cells {
			patches[a] = editdoc.Value{Kind: "blank"}
		}
	} else {
		body, ok := strings.CutPrefix(op.Formula, "=")
		if !ok || xlsxTextEscapes.MatchString(body) {
			return opc.Receipt{}, errors.New("请输入以等号开始的工作簿公式")
		}
		parsed, err := p.parseFormula(ctx, body, anchor, nil)
		if err != nil {
			return opc.Receipt{}, err
		}
		patches[anchor] = editdoc.Value{Kind: "formula", Raw: parsed.ArrayStorageFormula()}
		request[anchor] = sourceFormulaTag{kind: "array", dynamic: dynamic, area: r}
		if exists && old == request[anchor] && patches[anchor].Raw == b.cells[anchor].value.Raw {
			return opc.Receipt{}, editdoc.ErrNoChange
		}
	}
	calc, err := b.calculateArrays(ctx, patches, nil, request)
	if err != nil {
		return opc.Receipt{}, err
	}
	if !clear && calc.Cells[anchor].State != "calculated" {
		return opc.Receipt{}, fmt.Errorf("%s 的数组公式无法得到确定结果", addressRef(anchor))
	}
	updates := map[string][]byte{}
	if err := p.writeArrayResults(ctx, b, calc, patches, updates); err != nil {
		return opc.Receipt{}, err
	}
	if clear {
		data := s.data
		if next, ok := updates[s.sheet.Part]; ok {
			data = next
		}
		s.data = data
		updates[s.sheet.Part], err = p.rangeCells(ctx, s, cells, func(c cellTarget, _ []byte) (string, bool, error) {
			var out strings.Builder
			cellXML(&out, c.address, c.original.style, editdoc.Value{Kind: "blank"})
			return out.String(), true, nil
		})
		if err != nil {
			return opc.Receipt{}, err
		}
	}
	return p.writeValueStructure(ctx, dst, updates)
}

type arrayCell struct {
	value    editdoc.Value
	formula  string
	ref      string
	dynamic  bool
	metadata int
}

func (c arrayCell) xml(a editdoc.Address, style string) string {
	var out strings.Builder
	if c.value.Kind == "blank" {
		cellXML(&out, a, style, c.value)
		return out.String()
	}
	fmt.Fprintf(&out, `<c xmlns="%s" r="%s"`, mainNS, addressRef(a))
	if style != "" {
		fmt.Fprintf(&out, ` s="%s"`, style)
	}
	raw, kind := c.value.Raw, ""
	switch c.value.Kind {
	case "text":
		kind = "str"
	case "error":
		kind = "e"
	case "boolean":
		kind = "b"
		raw = "0"
		if c.value.Raw == "TRUE" {
			raw = "1"
		}
	}
	if kind != "" {
		fmt.Fprintf(&out, ` t="%s"`, kind)
	}
	if c.dynamic {
		out.WriteString(` cm="1"`)
	}
	if c.metadata != 0 {
		fmt.Fprintf(&out, ` vm="%d"`, c.metadata)
		raw = "#VALUE!"
	}
	out.WriteByte('>')
	if c.formula != "" {
		fmt.Fprintf(&out, `<f t="array" ref="%s">%s</f>`, c.ref, escapedXML(strings.TrimPrefix(c.formula, "=")))
	}
	fmt.Fprintf(&out, "<v>%s</v></c>", escapedXML(raw))
	return out.String()
}

// Both a range operation and an ordinary input edit emit the same caches and
// ownership. Clear released cells before placing results of any growing array.
func (p *xlsxPackage) writeArrayResults(ctx context.Context, b *CalculationBook, calc *Calculation, patches map[editdoc.Address]editdoc.Value, updates map[string][]byte) error {
	projected := make(map[editdoc.Address]arrayCell)
	values := objectValues{book: b, calculation: calc, overlay: maps.Clone(patches)}
	if values.overlay == nil {
		values.overlay = map[editdoc.Address]editdoc.Value{}
	}
	for a := range calc.Arrays {
		if old := b.cells[a].tag; old.kind == "array" {
			cells, err := old.area.addresses(a.Sheet)
			if err != nil {
				return err
			}
			for _, member := range cells {
				projected[member] = arrayCell{value: editdoc.Value{Kind: "blank"}}
			}
		}
	}
	meta := newArrayMetadata(p)
	for _, a := range slices.SortedFunc(maps.Keys(calc.Arrays), compareCalcAddress) {
		output := calc.Arrays[a]
		cells, err := output.area.addresses(a.Sheet)
		if err != nil {
			return err
		}
		for i, member := range cells {
			c := arrayCell{value: output.values[i]}
			if member == a {
				c.formula = calc.Cells[a].Formula.Raw
				c.dynamic = output.dynamic
				c.ref = pivotRectRef(output.area)
				if output.area.single() {
					c.ref = addressRef(a)
				}
			}
			c.metadata = meta.bind(output, c, c.value.Raw == "#SPILL!" && output.failure.kind == 0)
			projected[member] = c
		}
	}
	sheets := map[string][]editdoc.Address{}
	for a, c := range projected {
		sheets[a.Sheet] = append(sheets[a.Sheet], a)
		values.overlay[a] = c.value
	}
	for _, s := range p.layout.Sheets {
		addresses := sheets[s.Name]
		if len(addresses) == 0 {
			continue
		}
		raw, ok := updates[s.Part]
		var err error
		if !ok {
			raw, err = p.readPart(ctx, s.Part)
			if err != nil {
				return err
			}
		}
		updates[s.Part], err = p.rangeCells(ctx, rangeSheet{sheet: s, data: raw}, addresses, func(cell cellTarget, _ []byte) (string, bool, error) {
			c := projected[cell.address]
			if c.value.Kind == "blank" && cell.original.value.Kind == "missing" {
				return "", false, nil
			}
			style := cell.original.style
			if cell.original.value.Kind == "missing" && cell.original.value.Style != "0" {
				style = cell.original.value.Style
			}
			return c.xml(cell.address, style), true, nil
		})
		if err != nil {
			return err
		}
	}
	deps := *calc
	deps.Cells = maps.Clone(calc.Cells)
	for addr := range calc.Arrays {
		delete(deps.Cells, addr)
	}
	values.calculation = &deps
	return p.refreshWithMetadata(ctx, values, updates, meta)
}
