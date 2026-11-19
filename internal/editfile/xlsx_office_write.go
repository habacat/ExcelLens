package editfile

import (
	"context"
	"fmt"
	"io"
	"slices"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func (p *xlsxPackage) writeOffice(ctx context.Context, dst io.Writer, source *workbook.EditSourceFile, proof string, edits map[editdoc.Address]editdoc.Value, result XLSXResult, patches map[string][]xmlPatch) (XLSXResult, error) {
	bySheet := map[string][]editdoc.Address{}
	var changes []editdoc.Change
	for a, v := range edits {
		bySheet[a.Sheet] = append(bySheet[a.Sheet], a)
		changes = append(changes, editdoc.Change{Address: a, Value: v})
	}
	touchedText, editedFormula, hasRules := false, false, false
	for _, sheet := range p.layout.Sheets {
		cells := bySheet[sheet.Name]
		if len(cells) == 0 {
			continue
		}
		w, err := p.officeWindow(ctx, sheet, cells)
		if err != nil {
			return result, fmt.Errorf("工作表 %s：%w", sheet.Name, err)
		}
		for _, r := range w.targets.rows {
			for _, c := range r.cells {
				hasRules = hasRules || len(c.original.rules) > 0
			}
		}
		summary := sheet
		local, saved, text, err := p.worksheetPatches(ctx, w.data, w.targets, edits, &summary, validateXLSXNumber)
		if err != nil {
			return result, err
		}
		for _, patch := range local {
			change, err := w.rebase(patch)
			if err != nil {
				return result, err
			}
			patches[sheet.Part] = append(patches[sheet.Part], change)
		}
		groupEdits, err := p.officeGroupPatches(ctx, sheet, &w, edits)
		if err != nil {
			return result, err
		}
		patches[sheet.Part] = append(patches[sheet.Part], groupEdits...)
		for a, c := range saved {
			result.cells[a] = c
		}
		touchedText = touchedText || text
		editedFormula = editedFormula || w.targets.formulaEdit
	}
	if len(result.cells) != len(edits) {
		return result, ErrTarget
	}
	if hasRules {
		bound := Layout{XLSX: &p.layout}
		var addresses []editdoc.Address
		for _, c := range changes {
			addresses = append(addresses, c.Address)
		}
		batch, err := bound.ReadBatch(ctx, source, addresses, nil)
		if err != nil {
			return result, err
		}
		_, _, err = batch.Validate(ctx, changes, nil)
		if err != nil {
			return result, err
		}
	}
	if touchedText && p.files["xl/sharedStrings.xml"] != nil {
		data, err := p.readPart(ctx, "xl/sharedStrings.xml")
		if err != nil {
			return result, err
		}
		ps, err := sharedCountPatches(ctx, data)
		if err != nil {
			return result, err
		}
		patches["xl/sharedStrings.xml"] = ps
	}
	if len(edits) > 0 {
		result.NativeRecalc = true
		if !p.fullRecalc {
			data, err := p.readPart(ctx, "xl/workbook.xml")
			if err != nil {
				return result, err
			}
			ps, err := calcPropertiesPatches(ctx, data)
			if err != nil {
				return result, err
			}
			patches["xl/workbook.xml"] = ps
		}
		for part, cache := range p.pivotSources {
			if !cache.refreshable {
				continue
			}
			// Native formulas can indirectly feed any cache. Defer pivot refresh
			// until the host finishes calculating, including external add-in formulas.
			data, err := p.readPart(ctx, part)
			if err != nil {
				return result, err
			}
			ps, err := pivotRefreshPatches(ctx, data, false)
			if err != nil {
				return result, err
			}
			patches[part] = ps
			result.PivotCachesPending++
			result.PivotCachesAfterCalc++
		}
	}
	if editedFormula && p.files[calcChainPart] != nil {
		result.removedCalcChain = true
		for _, part := range []string{"xl/_rels/workbook.xml.rels", "[Content_Types].xml"} {
			data, err := p.readPart(ctx, part)
			if err != nil {
				return result, err
			}
			ps, err := calcChainReferencePatch(ctx, data, part)
			if err != nil {
				return result, err
			}
			patches[part] = ps
		}
	}
	return p.writePackage(ctx, dst, source, proof, result, func(part string) ([]xmlPatch, error) { return patches[part], nil })
}

func (p *xlsxPackage) verifyOffice(ctx context.Context, result XLSXResult) error {
	if !result.Layout.Native || !p.layout.Native || result.Layout.MacroEnabled != p.layout.MacroEnabled || !slices.Equal(result.Layout.Sheets, p.layout.Sheets) || result.NativeRecalc && !p.fullRecalc || result.removedCalcChain && p.files[calcChainPart] != nil {
		return ErrBinding
	}
	p.layout.GridEditing = true
	var requested []editdoc.Address
	for a := range result.cells {
		requested = append(requested, a)
	}
	cells, err := p.readOfficeCells(ctx, requested)
	if err != nil {
		return err
	}
	for a, expected := range result.cells {
		if cells[a].Value != expected.Value {
			return fmt.Errorf("工作表 %s 单元格 %s%d 的原值或类型核对失败", a.Sheet, workbook.ColumnName(a.Col), a.Row)
		}
	}
	return nil
}
