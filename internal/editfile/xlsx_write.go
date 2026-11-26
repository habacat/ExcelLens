package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
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

func ReadXLSXCells(ctx context.Context, f *workbook.EditSourceFile, layout XLSXLayout, cells []editdoc.Address) (map[editdoc.Address]SourceCell, error) {
	if !layout.Valid() || f.Size() != layout.Size {
		return nil, ErrBinding
	}
	p, err := openXLSXPackage(f, f.Size())
	if err == nil && layout.Native {
		p.layout.Native, p.layout.Structured = true, true
		if err = p.inspect(ctx); err != nil {
			return nil, err
		}
		if !p.layout.Native || p.layout.MacroEnabled != layout.MacroEnabled {
			return nil, ErrBinding
		}
		p.layout = layout
		values, err := p.readOfficeCells(ctx, cells)
		if err != nil {
			return nil, err
		}
		return values, f.Check()
	}
	if err == nil {
		if layout.Structured {
			err = p.loadWorkbook(ctx)
			if err == nil {
				err = p.inspectLinkedParts(ctx, true)
			}
		} else {
			err = p.metadata(ctx)
		}
	}
	if err != nil {
		return nil, err
	}
	if p.layout.Structured && !layout.Structured {
		return nil, ErrBinding
	}
	p.layout = layout
	values, err := p.readCells(ctx, cells)
	if err != nil {
		return nil, err
	}
	return values, f.Check()
}

func (p *xlsxPackage) readCells(ctx context.Context, cells []editdoc.Address) (map[editdoc.Address]SourceCell, error) {
	bySheet := map[string][]editdoc.Address{}
	for _, a := range cells {
		bySheet[a.Sheet] = append(bySheet[a.Sheet], a)
	}
	values := make(map[editdoc.Address]SourceCell, len(cells))
	for _, shape := range p.layout.Sheets {
		requested := bySheet[shape.Name]
		if len(requested) == 0 {
			continue
		}
		targets, _, err := p.findTargets(ctx, shape, requested, nil)
		if err != nil {
			return nil, err
		}
		for _, row := range targets.rows {
			for _, c := range row.cells {
				if len(c.original.value.Raw) > formats.EditCellBytes {
					return nil, fmt.Errorf("单元格原文超过编辑容量")
				}
				if c.original.cache != nil && len(c.original.cache.Raw) > formats.EditCellBytes {
					return nil, fmt.Errorf("公式原保存结果超过编辑容量")
				}
				values[c.address] = SourceCell{Array: c.original.array, Value: c.original.value, Cache: c.original.cache, ReadOnly: c.original.readOnly, protected: c.original.protected, formula: c.original.formula, names: p.names}
			}
		}
	}
	if len(values) != len(cells) {
		return nil, ErrTarget
	}
	return values, nil
}

func (p *xlsxPackage) findTargets(ctx context.Context, shape XLSXSheet, requested []editdoc.Address, rules *xlsxRuleScan) (*sheetTargets, []byte, error) {
	targets, err := newSheetTargets(shape, requested)
	if err != nil {
		return nil, nil, err
	}
	data, err := p.readPart(ctx, shape.Part)
	if err != nil {
		return nil, nil, err
	}
	actual, err := p.scanSheet(ctx, data, shape, targets, rules, nil, nil)
	if err == nil && actual != shape {
		err = ErrBinding
	}
	return targets, data, err
}

type xmlPatch struct {
	from, to int
	data     string
}

func cellXML(out *strings.Builder, a editdoc.Address, style string, v editdoc.Value) {
	fmt.Fprintf(out, `<c xmlns="%s" r="%s%d"`, mainNS, workbook.ColumnName(a.Col), a.Row)
	if style != "" {
		fmt.Fprintf(out, ` s="%s"`, style)
	}
	switch v.Kind {
	case "blank":
		out.WriteString("/>")
	case "text":
		out.WriteString(` t="inlineStr"><is><t`)
		if strings.Trim(v.Raw, " \t\r\n") != v.Raw {
			out.WriteString(` xml:space="preserve"`)
		}
		out.WriteByte('>')
		_ = xml.EscapeText(out, []byte(v.Raw))
		out.WriteString(`</t></is></c>`)
	case "date":
		fmt.Fprintf(out, ` t="d"><v>%s</v></c>`, v.Raw)
	case "number":
		fmt.Fprintf(out, `><v>%s</v></c>`, v.Raw)
	case "boolean":
		n := 0
		if v.Raw == "TRUE" {
			n = 1
		}
		fmt.Fprintf(out, ` t="b"><v>%d</v></c>`, n)
	case "error":
		out.WriteString(` t="e"><v>`)
		_ = xml.EscapeText(out, []byte(v.Raw))
		out.WriteString(`</v></c>`)
	case "formula":
		out.WriteString(`><f>`)
		_ = xml.EscapeText(out, []byte(v.Raw[1:]))
		out.WriteString(`</f></c>`)
	}
}

type savedCell struct {
	Value editdoc.Value
	Style string
}

func (p *xlsxPackage) worksheetPatches(ctx context.Context, data []byte, targets *sheetTargets, edits map[editdoc.Address]editdoc.Value, output *XLSXSheet, numberCheck func(string) error) ([]xmlPatch, map[editdoc.Address]savedCell, bool, error) {
	var patches []xmlPatch
	verified := map[editdoc.Address]savedCell{}
	var textChanged bool
	expanded := targets.dimension
	if targets.emptyDataClose != "" && len(targets.rows) > 0 {
		patches = append(patches, xmlPatch{targets.emptyDataStart, targets.emptyDataStart, ">"})
	}
	for _, row := range targets.rows {
		var chunk strings.Builder
		if row.insert {
			fmt.Fprintf(&chunk, `<row xmlns="%s" r="%d">`, mainNS, row.row)
		} else if row.closeTag != "" {
			chunk.WriteByte('>')
		}
		for _, target := range row.cells {
			edit := edits[target.address]
			original := target.original
			if original.value.Kind == "missing" {
				r, c := target.address.Row, target.address.Col
				block := (r - 1) / 16
				patches = append(patches, targets.spanBlocks[block]...)
				delete(targets.spanBlocks, block)
				output.FirstRow = firstUsed(output.FirstRow, r)
				output.FirstCol = firstUsed(output.FirstCol, c)
				output.Rows = max(output.Rows, r)
				output.Cols = max(output.Cols, c)
				expanded.firstRow = firstUsed(expanded.firstRow, r)
				expanded.firstCol = firstUsed(expanded.firstCol, c)
				expanded.lastRow = max(expanded.lastRow, r)
				expanded.lastCol = max(expanded.lastCol, c)
			}
			if err := p.layout.validateStoredValue(ctx, target.address, edit, SourceCell{formula: original.formula, names: p.names}, numberCheck); err != nil {
				return nil, nil, false, err
			}
			if err := p.layout.checkCellEdit(target.address, original.readOnly, original.protected, original.value.Style, p.logicalEdit(target.address, edit)); err != nil {
				return nil, nil, false, err
			}
			v := edit.Apply(original.value)
			style := original.style
			if edit.Style != "" {
				style = edit.Style
			} else if original.value.Kind == "missing" && v.Style != "0" {
				style = v.Style
			}
			if edit.Kind == "" && original.value.Kind != "missing" {
				stylePatches, err := removeXMLAttributes(data, original.from, original.tagEnd, "s")
				if err != nil {
					return nil, nil, false, err
				}
				end := original.tagEnd - 1
				if data[end-1] == '/' {
					end--
				}
				patches = append(patches, stylePatches...)
				patches = append(patches, xmlPatch{end, end, ` s="` + style + `"`})
				verified[target.address] = savedCell{v, style}
				continue
			}
			if original.value.Kind == "formula" || v.Kind == "formula" {
				targets.formulaEdit = true
			}
			if original.formula.kind == "shared" {
				if targets.changedGroups == nil {
					targets.changedGroups = map[int]bool{}
				}
				targets.changedGroups[original.formula.group] = true
			}
			if original.value.Kind == "formula" {
				output.Formulas--
			}
			if v.Kind == "formula" {
				output.Formulas++
			}
			if v.Kind == "missing" {
				v.Kind = "blank"
			}
			textChanged = textChanged || original.kind == "s" || original.value.Kind == "text" || v.Kind == "text"
			verified[target.address] = savedCell{v, style}
			if original.formula.kind == "array" && v.Kind == "formula" {
				// Keep the array range, metadata index, namespace and all formula flags.
				var expression strings.Builder
				_ = xml.EscapeText(&expression, []byte(v.Raw[1:]))
				patches = append(patches, xmlPatch{original.bodyFrom, original.bodyTo, expression.String()})
				ps, err := clearFormulaResult(data, original)
				if err != nil {
					return nil, nil, false, err
				}
				patches = append(patches, ps...)
				continue
			}
			if row.insert || row.closeTag != "" {
				cellXML(&chunk, target.address, style, v)
				continue
			}
			cellXML(&chunk, target.address, style, v)
			patches = append(patches, xmlPatch{original.from, original.to, chunk.String()})
			chunk.Reset()
		}
		if row.insert {
			chunk.WriteString(`</row>`)
		} else if row.closeTag != "" {
			chunk.WriteString(row.closeTag)
		}
		if row.insert || row.closeTag != "" {
			patches = append(patches, xmlPatch{row.from, row.to, chunk.String()})
		}
	}
	if targets.emptyDataClose != "" && len(targets.rows) > 0 {
		patches = append(patches, xmlPatch{targets.emptyDataStart, targets.emptyDataStart + 2, targets.emptyDataClose})
	}
	if targets.dimensionEnd > 0 && expanded != targets.dimension {
		ref := workbook.ColumnName(expanded.firstCol) + strconv.Itoa(expanded.firstRow)
		if !expanded.single() {
			ref += ":" + workbook.ColumnName(expanded.lastCol) + strconv.Itoa(expanded.lastRow)
		}
		ps, err := setXMLAttribute(data, targets.dimensionStart, targets.dimensionEnd, "ref", ref)
		if err != nil {
			return nil, nil, false, err
		}
		patches = append(patches, ps...)
	}
	return patches, verified, textChanged, nil
}

func firstUsed(current, added int) int {
	if current == 0 {
		return added
	}
	return min(current, added)
}

// Values are internal numeric flags or canonical A1 addresses.
func setXMLAttribute(data []byte, from, to int, name, value string) ([]xmlPatch, error) {
	patches, err := removeXMLAttributes(data, from, to, name)
	if err != nil {
		return nil, err
	}
	pos := to - 1
	if data[pos-1] == '/' {
		pos--
	}
	return append(patches, xmlPatch{pos, pos, " " + name + "=\"" + value + "\""}), nil
}

// The optional SST totals cease to be authoritative if text cells are replaced
// with inline strings. Drop only those attributes, leaving all shared entries,
// namespaces and the rest of the XML exactly as stored in the original ZIP.
func sharedCountPatches(ctx context.Context, data []byte) ([]xmlPatch, error) {
	var result []xmlPatch
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if e.parent() != "" {
			return nil
		}
		if _, ok := e.token.(xml.StartElement); !ok {
			return nil
		}
		var err error
		result, err = removeXMLAttributes(data, e.from, e.to, "count", "uniqueCount")
		return err
	})
	return result, err
}

func removeXMLAttributes(data []byte, from, to int, names ...string) ([]xmlPatch, error) {
	tag := data[from:to]
	pos := 1
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }
	for pos < len(tag) && !space(tag[pos]) && tag[pos] != '/' && tag[pos] != '>' {
		pos++
	}
	var result []xmlPatch
	for pos < len(tag) && space(tag[pos]) {
		start := pos
		for pos < len(tag) && space(tag[pos]) {
			pos++
		}
		attr := pos
		for pos < len(tag) && !space(tag[pos]) && tag[pos] != '=' && tag[pos] != '/' && tag[pos] != '>' {
			pos++
		}
		name := string(tag[attr:pos])
		if name == "" {
			break
		}
		for pos < len(tag) && tag[pos] != '\'' && tag[pos] != '"' {
			pos++
		}
		if pos == len(tag) {
			return nil, ErrXLSX
		}
		end := bytes.IndexByte(tag[pos+1:], tag[pos])
		if end < 0 {
			return nil, ErrXLSX
		}
		pos += end + 2
		if slices.Contains(names, name) {
			result = append(result, xmlPatch{from: from + start, to: from + pos})
		}
	}
	return result, nil
}

type XLSXResult struct {
	NativeRecalc bool
	Layout       XLSXLayout
	opc.Receipt
	FormulasPending      int
	PivotCachesPending   int
	PivotCachesAfterCalc int
	cells                map[editdoc.Address]savedCell
	removedCalcChain     bool
	addedParts           map[string][]byte
}

func WriteXLSX(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, layout XLSXLayout, proof string, edits map[editdoc.Address]editdoc.Value, ruleEdits []editdoc.Rule) (XLSXResult, error) {
	return writeXLSX(ctx, dst, src, layout, proof, edits, ruleEdits, nil)
}

func writeXLSX(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, layout XLSXLayout, proof string, edits map[editdoc.Address]editdoc.Value, ruleEdits []editdoc.Rule, templates map[editdoc.Address]string) (XLSXResult, error) {
	result := XLSXResult{Layout: layout, cells: map[editdoc.Address]savedCell{}}
	if !layout.Valid() || src.Size() != layout.Size {
		return result, ErrBinding
	}
	p, err := openXLSXPackage(src, src.Size())
	if err == nil {
		if layout.Native {
			p.layout.Native, p.layout.Structured = true, true
		}
		err = p.inspect(ctx)
	}
	if err != nil {
		return result, err
	}
	patches, err := p.tablePatches(ctx, edits, templates, &result)
	if err != nil {
		return result, err
	}
	edits, styles, err := p.materializeNumberStyles(ctx, edits)
	if err != nil {
		return result, err
	}
	result.addedParts, err = p.installEntryStyles(ctx, styles, patches)
	if err != nil {
		return result, err
	}
	if p.layout.Native || layout.Native {
		if !p.layout.Native || !layout.Native || layout.MacroEnabled != p.layout.MacroEnabled || !slices.Equal(p.layout.Sheets, layout.Sheets) || len(ruleEdits) != 0 {
			return result, ErrBinding
		}
		p.layout = layout
		return p.writeOffice(ctx, dst, src, proof, edits, result, patches)
	}
	if len(p.layout.Sheets) != len(layout.Sheets) || p.layout.Structured && !layout.Structured {
		return result, ErrBinding
	}
	if layout.Structured {
		p.layout.Structured = true
		p.layout.GridEditing = layout.GridEditing
		if layout.checkedSource != "" && layout.checkedSource == proof && layout.checkedLayout == layout.signature() {
			if err = VerifySource(ctx, src, proof); err != nil {
				return result, err
			}
			p.layout = layout
		} else if err = p.inspectWorksheets(ctx); err != nil {
			return result, err
		}
		if !slices.Equal(p.layout.Sheets, layout.Sheets) || !slices.Equal(p.layout.Rules, layout.Rules) {
			return result, ErrBinding
		}
	}
	result.Layout.Sheets = slices.Clone(layout.Sheets)
	bound := Layout{XLSX: &layout}
	rulesBySheet := map[string][]editdoc.Rule{}
	for i, rule := range ruleEdits {
		if _, err := bound.RuleBase(rule, ruleEdits[:i]); err != nil {
			return result, err
		}
		rulesBySheet[rule.Start.Sheet] = append(rulesBySheet[rule.Start.Sheet], rule)
	}
	result.Layout.Rules = bound.EffectiveRules(ruleEdits)
	ruleCounts := map[string]int{}
	for _, rule := range result.Layout.Rules {
		ruleCounts[rule.Start.Sheet]++
	}
	bySheet := map[string][]editdoc.Address{}
	for a, value := range edits {
		if !p.validStyle(value.Style) {
			return result, fmt.Errorf("%w：目标样式不存在", ErrXLSX)
		}
		bySheet[a.Sheet] = append(bySheet[a.Sheet], a)
	}
	dirty := len(edits) > 0 || len(ruleEdits) > 0
	affected := map[string]bool{}
	for sheet := range bySheet {
		affected[sheet] = true
	}
	affected = p.layout.Affected(affected)
	type invalidatedSheet struct {
		shape  XLSXSheet
		dirty  bool
		groups map[int]bool
	}
	formulaEdit := false
	invalidate := map[string]invalidatedSheet{}
	var touchesText bool
	afterCalc := map[string]bool{}
	for _, s := range layout.Sheets {
		if s.Formulas > 0 {
			afterCalc[s.Name] = true
		}
	}
	for a, edit := range edits {
		if edit.Kind == "formula" {
			afterCalc[a.Sheet] = true
		}
	}
	for _, sheet := range p.pivotSheets {
		afterCalc[sheet] = true
	}
	for part, source := range p.pivotSources {
		if !affected[source.sheet] {
			continue
		}
		if !source.refreshable {
			return result, fmt.Errorf("%w：透视缓存禁止刷新", ErrXLSX)
		}
		data, err := p.readPart(ctx, part)
		if err != nil {
			return result, err
		}
		// Excel can refresh pivots before recalculating source formulas on open.
		// Keep that cache pending until the user refreshes after calculation.
		ps, err := pivotRefreshPatches(ctx, data, !afterCalc[source.sheet])
		if err != nil {
			return result, err
		}
		patches[part] = ps
		result.PivotCachesPending++
		if afterCalc[source.sheet] {
			result.PivotCachesAfterCalc++
		}
	}
	for i, shape := range layout.Sheets {
		if shape.Name != p.layout.Sheets[i].Name || shape.Part != p.layout.Sheets[i].Part {
			return result, ErrBinding
		}
		output := &result.Layout.Sheets[i]
		output.Rules = ruleCounts[shape.Name]
		var groups map[int]bool
		if len(bySheet[shape.Name]) > 0 || len(rulesBySheet[shape.Name]) > 0 {
			var ruleScan xlsxRuleScan
			targets, data, err := p.findTargets(ctx, shape, bySheet[shape.Name], &ruleScan)
			if err != nil {
				return result, err
			}
			changes, values, text, err := p.worksheetPatches(ctx, data, targets, edits, output, validateXLSXNumber)
			if err != nil {
				return result, err
			}
			rulePatches, err := ruleScan.patches(data, rulesBySheet[shape.Name])
			if err != nil {
				return result, err
			}
			patches[shape.Part] = append(changes, rulePatches...)
			groups = targets.changedGroups
			formulaEdit = formulaEdit || targets.formulaEdit
			for position, cell := range values {
				result.cells[position] = cell
			}
			touchesText = touchesText || text
		}
		recalc := dirty
		if layout.Structured {
			recalc = affected[shape.Name]
			if recalc && shape.ReadOnly != "" {
				return result, ErrBinding
			}
		}
		if recalc {
			output.Cached, output.SharedCached = 0, 0
			touchesText = touchesText || shape.SharedCached > 0
		}
		result.FormulasPending += output.Formulas - output.Cached
		if shape.Formulas > 0 && (recalc || !layout.Structured && shape.Formulas != shape.Cached) {
			invalidate[shape.Part] = invalidatedSheet{shape, recalc, groups}
		}
	}
	if len(result.cells) != len(edits) {
		return result, ErrTarget
	}
	if shared := p.files["xl/sharedStrings.xml"]; touchesText && shared != nil {
		data, err := p.readPart(ctx, shared.Name)
		if err != nil {
			return result, err
		}
		counts, err := sharedCountPatches(ctx, data)
		if err != nil {
			return result, err
		}
		patches[shared.Name] = counts
	}
	if result.FormulasPending > 0 && !p.fullRecalc {
		data, err := p.readPart(ctx, "xl/workbook.xml")
		if err != nil {
			return result, err
		}
		patches["xl/workbook.xml"], err = calcPropertiesPatches(ctx, data)
		if err != nil {
			return result, err
		}
	}
	// A literal-only edit does not change the formula coordinates in the chain.
	result.removedCalcChain = p.files[calcChainPart] != nil && (formulaEdit || !layout.Structured && (dirty || result.FormulasPending > 0))
	if result.removedCalcChain {
		for _, name := range []string{"[Content_Types].xml", "xl/_rels/workbook.xml.rels"} {
			data, err := p.readPart(ctx, name)
			if err != nil {
				return result, err
			}
			ps, err := calcChainReferencePatch(ctx, data, name)
			if err != nil {
				return result, err
			}
			patches[name] = append(patches[name], ps...)
		}
	}
	return p.writePackage(ctx, dst, src, proof, result, func(part string) ([]xmlPatch, error) {
		changes := patches[part]
		if sheet, ok := invalidate[part]; ok {
			data, err := p.readPart(ctx, part)
			if err != nil {
				return nil, err
			}
			return p.formulaCachePatches(ctx, data, sheet.shape, edits, sheet.dirty, sheet.groups, changes)
		}
		return changes, nil
	})
}
