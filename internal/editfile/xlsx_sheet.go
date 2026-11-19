package editfile

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var xlsxTextEscapes = regexp.MustCompile(`(?i)_x[[:xdigit:]]{4}_`)

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
	var cm string
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
				for _, a := range token.Attr {
					if !nsDeclaration(a) && (a.Name.Space != "" || !slices.Contains([]string{"r", "s", "t", "cm"}, a.Name.Local)) {
						return ErrXLSX
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

func ordinaryFormulaAttributes(el xml.StartElement) bool {
	for _, a := range el.Attr {
		if a.Name == (xml.Name{Local: "ca"}) && ruleBool(a.Value) {
			continue
		}
		if !nsDeclaration(a) && !(a.Name.Space == "" && a.Name.Local == "t" && a.Value == "normal") {
			return false
		}
	}
	return true
}

func readFormulaCache(kind, raw string, stored bool, shared []string) (*editdoc.Value, error) {
	switch kind {
	case "", "n", "b", "e", "s", "str":
	default:
		return nil, fmt.Errorf("%w：公式缓存类型 %s", ErrXLSX, kind)
	}
	if !stored || kind != "str" && strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if kind == "str" {
		return &editdoc.Value{Kind: "text", Raw: raw}, nil
	}
	value, err := xlsxOriginalValue(kind, raw, true, false, shared)
	return &value, err
}

// FormulaForFill parses the source notation before shifting. The batch then
// binds every name and reference in the destination sheet before confirmation.
func (l XLSXLayout) FormulaForFill(ctx context.Context, raw string) (calculator.EditableFormula, error) {
	if !l.Structured {
		return calculator.ParseEditableFormula(ctx, raw, l.HasSheet)
	}
	body, ok := strings.CutPrefix(raw, "=")
	if !ok || xlsxTextEscapes.MatchString(body) {
		return calculator.EditableFormula{}, ErrXLSX
	}
	return calculator.ParseLocalFormula(ctx, body, l.HasSheet, func(string) (bool, bool) { return false, true })
}

func (l XLSXLayout) validateFormula(ctx context.Context, raw string) error {
	if xlsxTextEscapes.MatchString(raw) {
		return fmt.Errorf("公式含保留的 XML 转义形式")
	}
	_, err := calculator.ParseEditableFormula(ctx, raw, l.HasSheet)
	return err
}

func xlsxOriginalValue(kind, raw string, stored, inline bool, shared []string) (editdoc.Value, error) {
	if !stored && !inline && (kind == "" || kind == "n" || kind == "str" || kind == "inlineStr") {
		return editdoc.Value{Kind: "blank"}, nil
	}
	switch kind {
	case "", "n":
		if raw == "" {
			return editdoc.Value{Kind: "blank"}, nil
		}
		if v := (editdoc.Value{Kind: "number", Raw: raw}); v.Valid() {
			return v, nil
		}
	case "d":
		if v := (editdoc.Value{Kind: "date", Raw: raw}); stored && v.Valid() {
			return v, nil
		}
	case "inlineStr":
		if inline && !stored {
			return editdoc.Value{Kind: "text", Raw: raw}, nil
		}
	case "str":
		// Some generators use CT_Cell/v with an explicit string type for
		// literal text. Keep its text identity, including an empty value.
		if stored && !inline {
			return editdoc.Value{Kind: "text", Raw: raw}, nil
		}
	case "s":
		i, err := strconv.Atoi(raw)
		if err == nil && i >= 0 && i < len(shared) && strconv.Itoa(i) == raw {
			return editdoc.Value{Kind: "text", Raw: shared[i]}, nil
		}
	case "b":
		if raw == "1" || raw == "0" {
			v := "FALSE"
			if raw == "1" {
				v = "TRUE"
			}
			return editdoc.Value{Kind: "boolean", Raw: v}, nil
		}
	case "e":
		if strings.HasPrefix(raw, "#") {
			return editdoc.Value{Kind: "error", Raw: raw}, nil
		}
	}
	return editdoc.Value{}, fmt.Errorf("%w：单元格原值或类型无法确认", ErrXLSX)
}

func (l XLSXLayout) validateValue(ctx context.Context, a editdoc.Address, value editdoc.Value, source SourceCell) error {
	return l.validateStoredValue(ctx, a, value, source, validateXLSXNumber)
}

func (l XLSXLayout) validateStoredValue(ctx context.Context, a editdoc.Address, value editdoc.Value, source SourceCell, numberCheck func(string) error) error {
	if !value.Valid() {
		return editdoc.ErrInvalid
	}
	if source.formula.kind == "array" && !source.formula.area.single() && value.Kind != "formula" {
		return errors.New("多格数组的锚点须保留公式")
	}
	switch value.Kind {
	case "formula":
		if l.Structured {
			if xlsxTextEscapes.MatchString(value.Raw) {
				return errors.New("公式含保留的 XML 转义形式")
			}
			p := xlsxPackage{layout: l, names: source.names}
			parsed, err := p.parseFormula(ctx, value.Raw[1:], a, nil)
			if err == nil && source.formula.dynamic && !parsed.Scalar {
				return errors.New("该动态数组公式的结果可能溢出原区域")
			}
			return err
		}
		return l.validateFormula(ctx, value.Raw)
	case "text":
		return validateXLSXText(value.Raw)
	case "number":
		return numberCheck(value.Raw)
	}
	return nil
}

func validateXLSXText(raw string) error {
	units := 0
	for _, r := range raw {
		units++
		if r >= 0x10000 {
			units++
		}
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0xfffe || r == 0xffff {
			return errors.New("文本含无法原样保存到 XLSX 的字符")
		}
	}
	if units > 32767 || xlsxTextEscapes.MatchString(raw) {
		return errors.New("文本超过 XLSX 单格限制或含保留的转义形式")
	}
	return nil
}

func validateXLSXNumber(raw string) error {
	if !(editdoc.Value{Kind: "number", Raw: raw}).Valid() {
		return editdoc.ErrInvalid
	}
	mantissa, _, _ := strings.Cut(strings.ToLower(strings.TrimLeft(raw, "+-")), "e")
	digits := strings.Trim(strings.ReplaceAll(mantissa, ".", ""), "0")
	if len(digits) > 15 {
		return errors.New("数值超出 Excel 可可靠保存的 15 位有效数字或数值范围；可改用文本")
	}
	return validateStoredDouble(raw)
}

// A calculated pivot result, like an Excel formula result, can need all
// binary64 digits to round-trip. Typed decimal inputs retain the 15-digit rule.
func validateStoredDouble(raw string) error {
	f, err := strconv.ParseFloat(raw, 64)
	mantissa, _, _ := strings.Cut(strings.ToLower(raw), "e")
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f == 0 && strings.ContainsAny(mantissa, "123456789") || f != 0 && math.Abs(f) < 0x1p-1022 {
		return errors.New("数值超出 Excel 可保存的数值范围")
	}
	return nil
}
