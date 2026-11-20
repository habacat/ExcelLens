package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// Excel cuts the first populated cell into an empty merge anchor. Direct
// references follow that cell. A reference to the overwritten destination
// becomes #REF!, while references to multi-cell areas keep their boundaries.
func relocatedFormula(body, scope string, from, to editdoc.Address) (string, error) {
	return calculator.RewriteReferences(body, func(label, prefix, raw string) (string, error) {
		sheet := label
		if sheet == "" {
			sheet = scope
		}
		if strings.Contains(raw, ":") || !strings.EqualFold(sheet, to.Sheet) {
			return prefix + raw, nil
		}
		a := axisAddress(raw)
		if a.row == from.Row && a.col == from.Col {
			col := workbook.ColumnName(to.Col)
			if strings.HasPrefix(a.letters, "$") {
				col = "$" + col
			}
			row := strconv.Itoa(to.Row)
			if strings.HasPrefix(a.number, "$") {
				row = "$" + row
			}
			return prefix + col + row, nil
		}
		if a.row == to.Row && a.col == to.Col {
			return prefix + "#REF!", nil
		}
		return prefix + raw, nil
	})
}

func (p *xlsxPackage) mergedReferences(ctx context.Context, data []byte, sheet string, from, to editdoc.Address, changed map[editdoc.Address]editdoc.Value) ([]byte, error) {
	return p.rewriteFormulaBodies(ctx, data, sheet, func(old, scope string, cell editdoc.Address, isCell bool) (string, error) {
		next, err := relocatedFormula(old, scope, from, to)
		if isCell && next != old && err == nil {
			changed[cell] = editdoc.Value{Kind: "formula", Raw: "=" + next}
		}
		return next, err
	})
}

// Pass the containing cell only for a worksheet formula. Workbook names,
// validation expressions, table templates and chart series have their own scope.
func (p *xlsxPackage) rewriteFormulaBodies(ctx context.Context, data []byte, sheet string, rewrite func(string, string, editdoc.Address, bool) (string, error)) ([]byte, error) {
	var ps []xmlPatch
	var body strings.Builder
	var cell editdoc.Address
	var expr, scope string
	var offset, depth int
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "c" && e.parent() == "row" {
				col, row, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				cell = editdoc.Address{Sheet: sheet, Row: row, Col: col}
			}
			switch e.local() {
			case "f", "definedName", "formula", "formula1", "formula2", "totalsRowFormula", "calculatedColumnFormula":
				if t.Name.Space != mainNS && t.Name.Space != chartNS && t.Name.Space != "http://schemas.microsoft.com/office/excel/2006/main" {
					return nil
				}
				expr, scope, offset, depth = e.local(), sheet, e.to, len(e.stack)
				if expr == "definedName" {
					if n, err := strconv.Atoi(attribute(t, "", "localSheetId")); err == nil && n >= 0 && n < len(p.layout.Sheets) {
						scope = p.layout.Sheets[n].Name
					}
				}
				body.Reset()
			}
		case xml.CharData:
			if expr != "" && len(e.stack) == depth {
				body.Write(t)
			}
		case xml.EndElement:
			if expr == "" || e.local() != expr || len(e.stack) != depth {
				return nil
			}
			old := body.String()
			next, err := rewrite(old, scope, cell, expr == "f" && e.parent() == "c")
			if err != nil {
				return err
			}
			if next != old {
				ps = append(ps, xmlPatch{offset, e.from, escapedXML(next)})
			}
			expr = ""
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, data, ps)
}

func (p *xlsxPackage) finishMergedValues(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, updates map[string][]byte, changes map[editdoc.Address]editdoc.Value, from, to editdoc.Address, move bool) (opc.Receipt, error) {
	if len(changes) == 0 {
		return replaceParts(ctx, dst, p.zip, updates)
	}
	if move {
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
			raw, ok := updates[part]
			var err error
			if !ok {
				raw, err = p.readPart(ctx, part)
				if err != nil {
					return opc.Receipt{}, err
				}
			}
			original := raw
			if classifyPart(part).role == "sheet" {
				// A follower can refer to the moved cell even when its master does not.
				raw, err = p.expandStructureGroups(ctx, raw, scope, axisEdit{Structure: editdoc.Structure{Sheet: scope}})
				if err != nil {
					return opc.Receipt{}, err
				}
			}
			rewritten, err := p.mergedReferences(ctx, raw, scope, from, to, changes)
			if err != nil {
				return opc.Receipt{}, err
			}
			if !bytes.Equal(original, rewritten) {
				updates[part] = rewritten
			}
		}
	}
	values, err := newObjectValues(ctx, src, p.layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	values.overlay = changes
	values.calculation, err = values.book.Calculate(ctx, changes, nil)
	if err != nil {
		return opc.Receipt{}, err
	}
	if err = p.refreshDependents(ctx, values, updates); err != nil {
		return opc.Receipt{}, err
	}
	return p.writeValueStructure(ctx, dst, updates)
}
