package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Combine cache removals and sparse edits for this sheet only. A replacement
// cell already has no cache, so its original XML must never be patched twice.
func (p *xlsxPackage) formulaCachePatches(ctx context.Context, data []byte, shape XLSXSheet, edits map[editdoc.Address]editdoc.Value, dirty bool, groups map[int]bool, changes []xmlPatch) ([]xmlPatch, error) {
	structure := worksheetStructure{}
	materializedBytes := 0
	if dirty {
		structure.visitArrayResult = func(row, col int, c sheetCell) error {
			var blank strings.Builder
			cellXML(&blank, editdoc.Address{Sheet: shape.Name, Row: row, Col: col}, c.style, editdoc.Value{Kind: "blank"})
			changes = append(changes, xmlPatch{c.from, c.to, blank.String()})
			return nil
		}
	}
	actual, err := p.scanSheet(ctx, data, shape, nil, nil, func(row, col int, cell sheetCell) error {
		if edit := edits[editdoc.Address{Sheet: shape.Name, Row: row, Col: col}]; edit.Kind != "" || !dirty && cell.cache != nil {
			return nil
		}
		if cell.formula.kind == "shared" && groups[cell.formula.group] {
			group := structure.groups[cell.formula.group]
			raw, err := group.formula.Shift(row-group.area.firstRow, col-group.area.firstCol)
			if err != nil {
				return err
			}
			var body strings.Builder
			body.WriteString(`<f xmlns="` + mainNS + `">`)
			_ = xml.EscapeText(&body, []byte(raw[1:]))
			body.WriteString("</f>")
			materializedBytes += body.Len()
			if materializedBytes > formats.EditXLSXPartBytes {
				return fmt.Errorf("%w：展开共享公式超出工作表容量", ErrXLSX)
			}
			changes = append(changes, xmlPatch{cell.formulaFrom, cell.formulaTo, body.String()})
		}
		ps, err := clearFormulaResult(data, cell)
		changes = append(changes, ps...)
		return err
	}, &structure)
	if err == nil && actual != shape {
		err = ErrBinding
	}
	return changes, err
}

func clearFormulaResult(data []byte, cell sheetCell) ([]xmlPatch, error) {
	var patches []xmlPatch
	if cell.kind != "" {
		var err error
		patches, err = removeXMLAttributes(data, cell.from, cell.tagEnd, "t", "vm")
		if err != nil {
			return nil, err
		}
	}
	if cell.cacheTo > cell.cacheFrom {
		patches = append(patches, xmlPatch{from: cell.cacheFrom, to: cell.cacheTo})
	}
	return patches, nil
}

func VerifyXLSX(ctx context.Context, file *os.File, result XLSXResult) error {
	st, err := file.Stat()
	if err != nil {
		return err
	}
	p, err := openXLSXPackage(file, st.Size())
	if err == nil {
		if result.Layout.Native {
			p.layout.Native, p.layout.Structured = true, true
		}
		err = p.inspect(ctx)
	}
	if err != nil {
		return err
	}
	if len(p.zip.File) != len(result.Parts) || len(p.layout.Sheets) != len(result.Layout.Sheets) {
		return ErrXLSX
	}
	if err := result.Receipt.Verify(ctx, p.zip); err != nil {
		return err
	}
	if !equalTables(p.layout.Tables, result.Layout.Tables) {
		return fmt.Errorf("%w：表格列定义在生成后不一致", ErrBinding)
	}
	if result.Layout.Native || p.layout.Native {
		return p.verifyOffice(ctx, result)
	}
	if p.layout.Structured && !result.Layout.Structured {
		return ErrBinding
	}
	if result.Layout.Structured {
		p.layout.Structured = true
		p.layout.GridEditing = result.Layout.GridEditing
		if err := p.inspectWorksheets(ctx); err != nil {
			return err
		}
		if !slices.Equal(p.layout.Sheets, result.Layout.Sheets) || !slices.Equal(p.layout.Rules, result.Layout.Rules) {
			return ErrBinding
		}
	}
	if result.FormulasPending > 0 && !p.fullRecalc || result.removedCalcChain && p.files[calcChainPart] != nil {
		return fmt.Errorf("%w：副本重算属性或计算链不正确", ErrXLSX)
	}
	bySheet := map[string][]editdoc.Address{}
	requested := make([]editdoc.Address, 0, len(result.cells))
	for a := range result.cells {
		requested = append(requested, a)
	}
	bound := Layout{XLSX: &result.Layout}
	refs, err := bound.referenceRules(result.Layout.Rules)
	if err != nil {
		return err
	}
	requested, err = appendReferences(requested, refs)
	if err != nil {
		return err
	}
	for _, a := range requested {
		bySheet[a.Sheet] = append(bySheet[a.Sheet], a)
	}
	readValues := map[editdoc.Address]editdoc.Value{}
	for i, shape := range result.Layout.Sheets {
		checked := p.layout.Sheets[i]
		if checked.Name != shape.Name || checked.Part != shape.Part {
			return ErrXLSX
		}
		if len(bySheet[shape.Name]) == 0 && (result.Layout.Structured || shape.Formulas == 0 && shape.Rules == 0 && !result.Changed[shape.Part]) {
			continue
		}
		var rules xlsxRuleScan
		targets, _, err := p.findTargets(ctx, shape, bySheet[shape.Name], &rules)
		if err != nil {
			return err
		}
		var expected []editdoc.Rule
		for _, rule := range result.Layout.Rules {
			if rule.Start.Sheet == shape.Name {
				expected = append(expected, rule)
			}
		}
		for index, node := range rules.nodes {
			if index >= len(expected) || expected[index] != node.rule {
				return fmt.Errorf("%w：数据规则重读不匹配", ErrXLSX)
			}
		}
		for _, row := range targets.rows {
			for _, c := range row.cells {
				if expected, changed := result.cells[c.address]; changed && (savedCell{c.original.value, c.original.style}) != expected {
					return fmt.Errorf("XLSX 单元格重读不匹配：%s!%s%s", shape.Name, workbook.ColumnName(c.address.Col), strconv.Itoa(c.address.Row))
				}
				if len(refs) != 0 {
					readValues[c.address] = c.original.value
				}
			}
		}
	}
	for _, ref := range refs {
		if _, err := ref.referencedChoices(func(a editdoc.Address) editdoc.Value { return readValues[a] }); err != nil {
			return fmt.Errorf("XLSX 列表来源重读失败：%w", err)
		}
	}
	return nil
}
