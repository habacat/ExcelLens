package editfile

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// ArrayRevision owns the input, changed spill boundaries and their dependent
// caches. All become visible under one document revision, including on retry.
type ArrayRevision struct {
	book      *CalculationBook
	calc      *Calculation
	patches   map[editdoc.Address]editdoc.Value
	ordinary  map[editdoc.Address]editdoc.Value
	templates map[editdoc.Address]string
	released  map[editdoc.Address]bool
}

// If indexing a large source was deferred, an entered spill still needs a
// verified output shape before it can be accepted as a workbook formula.
func (l XLSXLayout) NeedsArrayCalculation(ctx context.Context, changes []editdoc.Change) bool {
	for _, c := range changes {
		if c.Value.Kind != "formula" {
			continue
		}
		f, err := calculator.ParseLocalFormula(ctx, strings.TrimPrefix(c.Value.Raw, "="), l.HasSheet, func(string) (bool, bool) { return false, true })
		if err == nil && !f.Scalar {
			return true
		}
	}
	return false
}

func (b *CalculationBook) ArrayChange(ctx context.Context, doc *editdoc.Document, plan editdoc.PreparedChanges) (*ArrayRevision, error) {
	patches := doc.Snapshot()
	needs := false
	explicit := map[editdoc.Address]sourceFormulaTag{}
	released := map[editdoc.Address]bool{}
	for _, c := range plan.Changes {
		old := b.cells[c.Address]
		next := c.Value.Apply(patches[c.Address])
		patches[c.Address] = next
		if c.Value.Kind == "" {
			continue
		}
		needs = needs || len(b.arrays) > 0
		if c.Value.Kind == "formula" {
			form, err := b.source.parseFormula(ctx, strings.TrimPrefix(next.Raw, "="), c.Address, nil)
			if err == nil && (!form.Scalar || old.tag.kind == "array" && !old.tag.area.single()) {
				tag := old.tag
				if tag.kind != "array" {
					tag = sourceFormulaTag{kind: "array", dynamic: true, area: anchorRect(c.Address)}
				}
				if t, _ := b.source.layout.tableAt(c.Address); t != nil && tag.dynamic {
					return nil, fmt.Errorf("Excel 表格内不能输入溢出数组，请选择表格外的单元格")
				}
				explicit[c.Address] = tag
				next.Raw = form.ArrayStorageFormula()
				patches[c.Address] = next
				needs = true
			}
		} else if old.tag.kind == "array" && old.tag.dynamic && !old.tag.area.single() {
			cells, err := old.tag.area.addresses(c.Sheet)
			if err != nil {
				return nil, err
			}
			for _, a := range cells {
				if a != c.Address {
					released[a] = true
				}
			}
			needs = true
		}
	}
	if !needs {
		return nil, nil
	}
	inputs := maps.Clone(patches)
	for a := range released {
		if _, set := inputs[a]; !set {
			inputs[a] = editdoc.Value{Kind: "blank"}
		}
	}
	calc, err := b.calculateArrays(ctx, inputs, nil, explicit)
	if err != nil {
		return nil, err
	}
	for a := range b.arrays {
		if input := inputs[a]; input.Kind != "" && input.Kind != "formula" {
			continue
		}
		if _, ok := calc.Arrays[a]; !ok {
			return nil, fmt.Errorf("%s!%s 的数组结果无法完整重算", a.Sheet, addressRef(a))
		}
	}
	for a := range explicit {
		if cell, ok := calc.Cells[a]; !ok || cell.State != "calculated" {
			return nil, fmt.Errorf("%s!%s 的数组公式无法得到确定结果", a.Sheet, addressRef(a))
		}
	}
	ordinary := maps.Clone(patches)
	for a := range calc.Arrays {
		if v, ok := ordinary[a]; ok && v.Kind == "formula" {
			delete(ordinary, a)
			if v.Style != "" && (v.Style != b.cells[a].value.Style || v.NumberStyle != "" || v.BuiltinFormat != 0) {
				ordinary[a] = editdoc.Value{Style: v.Style, NumberStyle: v.NumberStyle, BuiltinFormat: v.BuiltinFormat}
			}
		}
	}
	templates := doc.ColumnFormulas()
	for _, t := range plan.Templates {
		templates[t.Address] = t.Formula
	}
	return &ArrayRevision{b, calc, inputs, ordinary, templates, released}, nil
}

func (r *ArrayRevision) Materialize(ctx context.Context, dir string, doc *editdoc.Document, layout Layout, src *workbook.EditSourceFile) (string, error) {
	return materializeRevision(ctx, dir, doc, layout, src, r.ordinary, r.templates, func(w io.Writer, source *workbook.EditSourceFile) (opc.Receipt, error) {
		p, err := openXLSXPackage(source, source.Size())
		if err != nil {
			return opc.Receipt{}, err
		}
		if layout.XLSX.Native {
			p.layout.Native, p.layout.Structured = true, true
		}
		if err = p.inspect(ctx); err != nil {
			return opc.Receipt{}, err
		}
		if err = p.inspectGrid(ctx); err != nil {
			return opc.Receipt{}, err
		}
		updates := map[string][]byte{}
		for _, s := range p.layout.Sheets {
			var cells []editdoc.Address
			for a := range r.released {
				if a.Sheet == s.Name {
					cells = append(cells, a)
				}
			}
			if len(cells) == 0 {
				continue
			}
			raw, err := p.readPart(ctx, s.Part)
			if err != nil {
				return opc.Receipt{}, err
			}
			updates[s.Part], err = p.rangeCells(ctx, rangeSheet{sheet: s, data: raw}, cells, func(c cellTarget, _ []byte) (string, bool, error) {
				if _, entered := r.ordinary[c.address]; entered {
					return "", false, nil
				}
				var out strings.Builder
				cellXML(&out, c.address, c.original.style, editdoc.Value{Kind: "blank"})
				return out.String(), true, nil
			})
			if err != nil {
				return opc.Receipt{}, err
			}
		}
		if err = p.writeArrayResults(ctx, r.book, r.calc, r.patches, updates); err != nil {
			return opc.Receipt{}, err
		}
		return p.writeValueStructure(ctx, w, updates)
	})
}
