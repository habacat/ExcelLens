package editfile

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var sheetElements = map[string][]string{
	"":                {"worksheet"},
	"worksheet":       {"sheetPr", "dimension", "sheetViews", "sheetFormatPr", "cols", "sheetData", "dataValidations", "printOptions", "pageMargins", "pageSetup", "headerFooter"},
	"sheetPr":         {"tabColor", "outlinePr", "pageSetUpPr"},
	"sheetViews":      {"sheetView"},
	"sheetView":       {"pane", "selection"},
	"cols":            {"col"},
	"sheetData":       {"row"},
	"row":             {"c"},
	"c":               {"f", "v", "is"},
	"is":              {"t"},
	"dataValidations": {"dataValidation"},
	"dataValidation":  {"formula1", "formula2"},
	"headerFooter":    {"oddHeader", "oddFooter", "evenHeader", "evenFooter", "firstHeader", "firstFooter"},
}

type sheetCell struct {
	array                                    *ArrayOwner
	protected                                bool
	rules                                    []validatedRule
	value                                    editdoc.Value
	cache                                    *editdoc.Value
	style, kind                              string
	from, tagEnd, to                         int
	cacheFrom, cacheTo                       int
	readOnly                                 string
	formula                                  sourceFormulaTag
	formulaFrom, bodyFrom, bodyTo, formulaTo int
}

type cellTarget struct {
	address  editdoc.Address
	original sheetCell
}

type rowTarget struct {
	row      int
	cells    []cellTarget
	next     int
	insert   bool
	from, to int
	closeTag string
	style    string
}

type sheetTargets struct {
	rows                         []rowTarget
	next                         int
	active                       *rowTarget
	dimension                    cellRect
	dimensionStart, dimensionEnd int
	emptyDataStart               int
	emptyDataClose               string
	spanBlocks                   map[int][]xmlPatch
	changedGroups                map[int]bool
	formulaEdit                  bool
}

func newSheetTargets(shape XLSXSheet, cells []editdoc.Address) (*sheetTargets, error) {
	ordered := slices.Clone(cells)
	slices.SortFunc(ordered, func(a, b editdoc.Address) int {
		if a.Row != b.Row {
			return cmp.Compare(a.Row, b.Row)
		}
		return cmp.Compare(a.Col, b.Col)
	})
	targets := &sheetTargets{spanBlocks: map[int][]xmlPatch{}}
	for i, a := range ordered {
		if a.Sheet != shape.Name || a.Row < 1 || a.Row > formats.ExcelRows || a.Col < 1 || a.Col > formats.ExcelColumns || i > 0 && a == ordered[i-1] {
			return nil, ErrTarget
		}
		if len(targets.rows) == 0 || targets.rows[len(targets.rows)-1].row != a.Row {
			targets.rows = append(targets.rows, rowTarget{row: a.Row})
		}
		targets.spanBlocks[(a.Row-1)/16] = nil
		r := &targets.rows[len(targets.rows)-1]
		r.cells = append(r.cells, cellTarget{address: a})
	}
	return targets, nil
}

func (t *sheetTargets) startRow(row, from int, style string) {
	if t == nil {
		return
	}
	for t.next < len(t.rows) && t.rows[t.next].row < row {
		r := &t.rows[t.next]
		r.insert, r.from, r.to = true, from, from
		for i := range r.cells {
			r.cells[i].original.value.Kind = "missing"
		}
		t.next++
	}
	if t.next < len(t.rows) && t.rows[t.next].row == row {
		t.active = &t.rows[t.next]
		t.active.style = style
		t.next++
	}
}

func inheritedStyle(row, col string) string {
	if row != "" {
		return row
	}
	if col != "" {
		return col
	}
	return "0"
}

func (p *xlsxPackage) validStyle(style string) bool {
	if style == "" || style == "0" {
		return true
	}
	_, ok := positiveInt(style, min(p.styles-1, formats.EditXLSXStyleIndex))
	return ok
}

func (t *sheetTargets) wants(col int) bool {
	if t == nil || t.active == nil {
		return false
	}
	_, ok := slices.BinarySearchFunc(t.active.cells, col, func(c cellTarget, v int) int { return cmp.Compare(c.address.Col, v) })
	return ok
}

func (t *sheetTargets) cell(col int, cell sheetCell) {
	if t == nil || t.active == nil {
		return
	}
	r := t.active
	for r.next < len(r.cells) && r.cells[r.next].address.Col < col {
		r.cells[r.next].original = sheetCell{value: editdoc.Value{Kind: "missing"}, from: cell.from, to: cell.from}
		r.next++
	}
	if r.next < len(r.cells) && r.cells[r.next].address.Col == col {
		r.cells[r.next].original = cell
		r.next++
	}
}

func (t *sheetTargets) endRow(data []byte, start, tagEnd, end int) {
	if t == nil || t.active == nil {
		return
	}
	r := t.active
	t.cell(formats.ExcelColumns+1, sheetCell{from: end})
	if tagEnd == end && bytes.HasSuffix(data[start:tagEnd], []byte("/>")) {
		// Expand a source self-closing row without replacing its attributes.
		r.from, r.to, r.closeTag = end-2, end, closingTag(data[start:end])
	}
	t.active = nil
}

func closingTag(open []byte) string {
	name := strings.FieldsFunc(string(open[1:len(open)-2]), func(c rune) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' })[0]
	return "</" + name + ">"
}

func (p *xlsxPackage) scanSheet(ctx context.Context, data []byte, shape XLSXSheet, targets *sheetTargets, rules *xlsxRuleScan, visitFormula func(row, col int, cell sheetCell) error, structure *worksheetStructure) (XLSXSheet, error) {
	if rules == nil {
		rules = &xlsxRuleScan{}
	}
	rules.sheet = shape.Name
	seen := map[string]bool{}
	var declaredFirstRow, declaredFirstCol, declaredLastRow, declaredLastCol int
	var row, col, rowStart, rowTagEnd, sheetDataStart int
	var rowStyle string
	columnStyles := map[int]string{}
	var cell sheetCell
	var stored, inline, text, formula bool
	var cm, vm string
	var formulaTag sourceFormulaTag
	var raw, expression strings.Builder
	summary := XLSXSheet{Name: shape.Name, Part: shape.Part, ReadOnly: shape.ReadOnly}
	if structure == nil {
		structure = &worksheetStructure{}
	}
	err := walkXML(ctx, data, func(e xmlEvent) error {
		local := e.local()
		if p.layout.Native {
			if handled, err := p.officeSheetElement(e, shape, structure); handled {
				return err
			}
		}
		if handled, err := p.preservedWorksheetElement(e, shape, structure, seen); handled {
			return err
		}
		switch token := e.token.(type) {
		case xml.StartElement:
			if !p.layout.Native && token.Name == (xml.Name{Space: mainNS, Local: "dataValidation"}) && len(strings.Fields(attribute(token, "", "sqref"))) > 1 {
				return errOfficeWorksheet
			}
			if !p.layout.Native && len(e.stack) == 2 && token.Name.Space == mainNS && slices.Contains([]string{"extLst", "hyperlinks", "phoneticPr", "conditionalFormatting", "dataConsolidate", "cellWatches", "rowBreaks", "colBreaks", "ignoredErrors"}, local) {
				return errOfficeWorksheet
			}
			if token.Name.Space != mainNS || !mainAttributes(token) || !slices.Contains(sheetElements[e.parent()], local) {
				return fmt.Errorf("%w：工作表元素 %s", ErrXLSX, local)
			}
			if e.parent() == "worksheet" {
				if seen[local] {
					return ErrXLSX
				}
				seen[local] = true
			}
			switch local {
			case "sheetPr":
				if isTrue(attribute(token, "", "filterMode")) {
					if !p.layout.Structured {
						return errStructuredXLSX
					}
					// Stored filter settings and row visibility are preserved during edits.
				}
			case "dataValidations":
				if !seen["sheetData"] || rules.insert != 0 {
					return errRule
				}
			case "col":
				style := inheritedStyle(attribute(token, "", "style"), "")
				start, firstOK := positiveInt(attribute(token, "", "min"), formats.ExcelColumns)
				end, lastOK := positiveInt(attribute(token, "", "max"), formats.ExcelColumns)
				if seen["sheetData"] || !firstOK || !lastOK || end < start || !p.validStyle(style) {
					return fmt.Errorf("%w：列样式范围无效", ErrXLSX)
				}
				for c := start; c <= end; c++ {
					if columnStyles[c] != "" {
						return fmt.Errorf("%w：列样式范围重叠", ErrXLSX)
					}
					columnStyles[c] = style
				}
			case "dimension":
				ref := attribute(token, "", "ref")
				first, last, interval := strings.Cut(ref, ":")
				if !interval {
					last = first
				}
				var err error
				declaredFirstCol, declaredFirstRow, err = workbook.ParseCellRef(first)
				if err != nil {
					return ErrXLSX
				}
				declaredLastCol, declaredLastRow, err = workbook.ParseCellRef(last)
				if err != nil || declaredFirstRow > declaredLastRow || declaredFirstCol > declaredLastCol || declaredLastRow > formats.ExcelRows || declaredLastCol > formats.ExcelColumns {
					return ErrXLSX
				}
				if targets != nil {
					targets.dimension = cellRect{declaredFirstRow, declaredFirstCol, declaredLastRow, declaredLastCol}
					targets.dimensionStart, targets.dimensionEnd = e.from, e.to
				}
			case "sheetData":
				sheetDataStart = e.from
			case "row":
				number, ok := positiveInt(attribute(token, "", "r"), formats.ExcelRows)
				if !ok || number <= row {
					return fmt.Errorf("%w：行坐标不明确", ErrXLSX)
				}
				row, col, rowStart, rowTagEnd = number, 0, e.from, e.to
				if structure.hidden != nil && isTrue(attribute(token, "", "hidden")) {
					structure.hidden[row] = true
				}
				rowStyle = ""
				customFormat, style := attribute(token, "", "customFormat"), attribute(token, "", "s")
				if !p.validStyle(style) || !slices.Contains([]string{"", "0", "1", "false", "true"}, customFormat) {
					return fmt.Errorf("%w：行样式无效", ErrXLSX)
				}
				if customFormat == "1" || customFormat == "true" {
					rowStyle = inheritedStyle(style, "")
				}
				targets.startRow(row, e.from, rowStyle)
				if targets != nil && attribute(token, "", "spans") != "" {
					block := (row - 1) / 16
					if _, needed := targets.spanBlocks[block]; needed {
						ps, err := removeXMLAttributes(data, e.from, e.to, "spans")
						if err != nil {
							return err
						}
						targets.spanBlocks[block] = append(targets.spanBlocks[block], ps...)
					}
				}
			case "c":
				cm = attribute(token, "", "cm")
				vm = attribute(token, "", "vm")
				for _, a := range token.Attr {
					if !nsDeclaration(a) && (a.Name.Space != "" || !slices.Contains([]string{"r", "s", "t", "cm", "vm"}, a.Name.Local)) {
						return ErrXLSX
					}
					if a.Name == (xml.Name{Local: "vm"}) && (p.rich == nil || p.rich.code(vm) == "" || attribute(token, "", "t") != "e") {
						return fmt.Errorf("%w：单元格值元数据 %s", ErrXLSX, vm)
					}
					if a.Name == (xml.Name{Local: "cm"}) && (cm != "1" || !p.dynamic || !p.layout.Structured) {
						return fmt.Errorf("%w：单元格元数据", ErrXLSX)
					}
				}
				ref := attribute(token, "", "r")
				c, r, err := workbook.ParseCellRef(ref)
				if err != nil || r != row || c <= col || c > formats.ExcelColumns || ref != workbook.ColumnName(c)+strconv.Itoa(r) {
					return fmt.Errorf("%w：单元格坐标不明确", ErrXLSX)
				}
				col = c
				cell = sheetCell{style: attribute(token, "", "s"), kind: attribute(token, "", "t"), from: e.from, tagEnd: e.to}
				if !p.validStyle(cell.style) {
					return fmt.Errorf("%w：%s 样式不存在", ErrXLSX, ref)
				}
				raw.Reset()
				expression.Reset()
				stored, inline, text, formula = false, false, false, false
			case "f":
				if formula || stored || inline {
					return ErrXLSX
				}
				var err error
				formulaTag, err = p.sourceFormulaTag(token, cm, row, col)
				if err != nil {
					return err
				}
				formula = true
				cell.formula, cell.formulaFrom, cell.bodyFrom = formulaTag, e.from, e.to
			case "v":
				if stored || inline {
					return ErrXLSX
				}
				stored = true
				cell.cacheFrom = e.from
			case "is":
				if formula || stored || inline || cell.kind != "inlineStr" {
					return ErrXLSX
				}
				inline = true
			case "t":
				if text {
					return ErrXLSX
				}
				text = true
			}
		case xml.CharData:
			if local == "f" {
				if expression.Len()+len(token) >= formats.EditFormulaBytes {
					return fmt.Errorf("%w：公式原文超出编辑容量", ErrXLSX)
				}
				expression.Write(token)
			} else if local == "v" || local == "t" {
				raw.Write(token)
			} else if local != "formula1" && local != "formula2" && e.parent() != "headerFooter" && len(bytes.TrimSpace(token)) > 0 {
				return ErrXLSX
			}
		case xml.EndElement:
			switch local {
			case "f":
				cell.bodyTo, cell.formulaTo = e.from, e.to
			case "v":
				cell.cacheTo = e.to
			case "c":
				cell.to = e.to
				if formula {
					if structure.arrayThrough[col] >= row {
						return fmt.Errorf("%w：数组结果中含独立公式", ErrXLSX)
					}
					body := strings.TrimSpace(expression.String())
					if err := p.checkSourceFormula(ctx, structure, formulaTag, body, shape.Name, row, col); err != nil {
						return fmt.Errorf("%w：%s%d %w", ErrXLSX, workbook.ColumnName(col), row, err)
					}
					cell.value = editdoc.Value{Kind: "formula"}
					if body != "" {
						cell.value.Raw = "=" + body
					}
					if formulaTag.kind == "shared" {
						group, known := structure.groups[formulaTag.group]
						if p.layout.Native && !known && targets.wants(col) {
							return fmt.Errorf("%w：共享公式从格缺少有效锚点", ErrXLSX)
						}
						cell.readOnly = group.readOnly
						if body == "" && (targets.wants(col) || structure.visitCell != nil) && cell.readOnly == "" {
							var err error
							cell.value.Raw, err = group.formula.Shift(row-group.area.firstRow, col-group.area.firstCol)
							if err != nil {
								return err
							}
						}
					}
					cache, err := readFormulaCache(cell.kind, raw.String(), stored, p.shared)
					if err != nil {
						return err
					}
					if vm != "" && cache != nil {
						cache.Raw = p.rich.code(vm)
					}
					cell.cache = cache
					summary.Formulas++
					if cache != nil {
						summary.Cached++
						if cell.kind == "s" {
							summary.SharedCached++
						}
					}
					if visitFormula != nil {
						if err := visitFormula(row, col, cell); err != nil {
							return err
						}
					}
				} else {
					if cm != "" {
						return fmt.Errorf("%w：动态数组元数据需绑定公式", ErrXLSX)
					}
					var v editdoc.Value
					var err error
					if cell.kind == "str" && structure.arrayThrough[col] >= row {
						v = editdoc.Value{Kind: "blank"}
						if stored {
							v = editdoc.Value{Kind: "text", Raw: raw.String()}
						}
					} else {
						v, err = xlsxOriginalValue(cell.kind, raw.String(), stored, inline, p.shared)
					}
					if err != nil {
						return err
					}
					if vm != "" {
						v.Raw = p.rich.code(vm)
					}
					cell.value = v
					if structure.visitArrayResult != nil && structure.arrayThrough[col] >= row && v.Kind != "blank" {
						if err := structure.visitArrayResult(row, col, cell); err != nil {
							return err
						}
					}
				}
				// CT_Cell gives an allocated cell without @s style 0. The
				// row and column defaults apply when a new cell is created.
				cell.value.Style = inheritedStyle(cell.style, "")
				if structure.visitCell != nil {
					if err := structure.visitCell(row, col, cell); err != nil {
						return err
					}
				}
				if summary.FirstRow == 0 {
					summary.FirstRow = row
				}
				if summary.FirstCol == 0 || col < summary.FirstCol {
					summary.FirstCol = col
				}
				summary.Rows, summary.Cols = row, max(summary.Cols, col)
				targets.cell(col, cell)
			case "row":
				targets.endRow(data, rowStart, rowTagEnd, e.from)
			case "sheetData":
				pos := e.from
				if targets != nil && e.from == e.to && bytes.HasSuffix(data[sheetDataStart:e.to], []byte("/>")) {
					pos = e.to - 2
					targets.emptyDataStart = pos
					targets.emptyDataClose = closingTag(data[sheetDataStart:e.to])
				}
				targets.startRow(formats.ExcelRows+1, pos, "")
			}
		}
		if err := rules.visit(e); err != nil {
			return fmt.Errorf("%w：%w", ErrXLSX, err)
		}
		return nil
	})
	if err != nil {
		return summary, err
	}
	if !seen["sheetData"] || seen["dimension"] && summary.Rows > 0 && (summary.FirstRow < declaredFirstRow || summary.FirstCol < declaredFirstCol || summary.Rows > declaredLastRow || summary.Cols > declaredLastCol) {
		return summary, fmt.Errorf("%w：工作表范围不一致", ErrXLSX)
	}
	if err := p.finishWorksheetStructure(shape, structure, seen, cellRect{declaredFirstRow, declaredFirstCol, declaredLastRow, declaredLastCol}); err != nil {
		return summary, err
	}
	if targets != nil {
		if structure.hasRules {
			if err := p.readOfficeRules(ctx, data, shape.Name, structure); err != nil {
				return summary, err
			}
		}
		for _, r := range targets.rows {
			for i := range r.cells {
				c := &r.cells[i]
				if c.original.value.Kind == "missing" {
					c.original.value.Style = inheritedStyle(r.style, columnStyles[c.address.Col])
				}
				if p.layout.Structured {
					c.original.protected = structure.protected
					for _, rect := range structure.arrayRanges {
						if rect.contains(c.address.Row, c.address.Col) {
							c.original.array = &ArrayOwner{Range: pivotRectRef(rect), Dynamic: structure.arrayDynamic[rect]}
							break
						}
					}
					c.original.readOnly = structure.readOnly(shape, *c, p.unlockedStyles[c.original.value.Style])
					if reason := p.layout.tableLock(c.address); reason != "" {
						c.original.readOnly = reason
					}
					for _, rule := range structure.nativeRules {
						if rule.Contains(c.address) {
							c.original.rules = append(c.original.rules, rule)
						}
					}
					for _, owner := range p.pivotRanges[shape.Name] {
						if owner.area.contains(c.address.Row, c.address.Col) {
							c.original.readOnly = owner.reason
							break
						}
					}
				} else {
					c.original.readOnly = ""
				}
			}
		}
	}
	summary.Rules = len(rules.nodes)
	return summary, nil
}
