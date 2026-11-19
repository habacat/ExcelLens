package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const (
	lockArrayMember  = "数组结果由左上角公式生成"
	lockMergedMember = "合并区域由左上角单元格编辑"
	lockPivotResult  = "透视表生成的结果区域"
	lockPivotHeader  = "透视缓存的源字段标题"
	wpsFilterNS      = "http://www.wps.cn/officeDocument/2017/etCustomData"
)

var (
	errStructuredXLSX  = errors.New("XLSX 含需保留的工作表结构")
	errOfficeWorksheet = errors.New("工作表需要按原部件保留并按坐标编辑")
	errStructuredStyle = errors.New("受保护工作表的格式不能在此更改")
)

type sharedGroup struct {
	area     cellRect
	formula  calculator.EditableFormula
	readOnly string
}

type worksheetStructure struct {
	visitCell func(int, int, sheetCell) error
	// Dependencies are only collected by full package preflight and verification.
	dependencies     map[string]bool
	groups           map[int]sharedGroup
	merges           []cellRect
	arrayRanges      []cellRect
	arrayDynamic     map[cellRect]bool
	arrayThrough     map[int]int
	visitArrayResult func(int, int, sheetCell) error
	tableIDs         map[string]bool
	mergeCount       int
	mergeTotal       string
	filter           cellRect
	hidden           map[int]bool
	filtered         bool
	blocks           []ownedRange
	nativeRules      []validatedRule
	hasRules         bool
	unknownScope     bool
	protected        bool
	window           bool

	drawing, legacyDrawing, printer string
}

func (s *worksheetStructure) readOnly(shape XLSXSheet, c cellTarget, unlocked bool) string {
	if s.protected && !unlocked {
		return "工作表受保护"
	}
	if s.unknownScope {
		return "无法确定工作表扩展的适用范围"
	}
	for _, b := range s.blocks {
		if b.area.contains(c.address.Row, c.address.Col) {
			return b.reason
		}
	}
	if shape.ReadOnly != "" {
		return shape.ReadOnly
	}
	if c.original.readOnly != "" {
		return c.original.readOnly
	}
	if c.original.array != nil && c.original.formula.kind != "array" {
		return lockArrayMember
	}
	for _, area := range s.merges {
		if area.contains(c.address.Row, c.address.Col) && (c.address.Row != area.firstRow || c.address.Col != area.firstCol) {
			return lockMergedMember
		}
	}
	return ""
}

func (s *worksheetStructure) addMerge(area cellRect) error {
	if len(s.merges) >= formats.EditXLSXRangeLimit {
		return fmt.Errorf("%w：保护范围数量超限", ErrXLSX)
	}
	s.merges = append(s.merges, area)
	return nil
}

func (p *xlsxPackage) preservedWorksheetElement(e xmlEvent, shape XLSXSheet, s *worksheetStructure, seen map[string]bool) (bool, error) {
	if len(e.stack) < 2 || e.stack[1].Space != mainNS {
		return false, nil
	}
	kind := e.stack[1].Local
	if kind == "sheetProtection" || kind == "protectedRanges" {
		if !p.layout.Structured {
			return true, errStructuredXLSX
		}
		if kind == "sheetProtection" {
			if el, ok := e.token.(xml.StartElement); ok {
				flag := attribute(el, "", "sheet")
				if len(e.stack) != 2 || seen[kind] || flag != "" && !ruleBool(flag) {
					return true, fmt.Errorf("%w：工作表保护标志无效", ErrXLSX)
				}
				seen[kind] = true
				s.protected = isTrue(flag)
			}
		}
		// AllowEditRanges may grant extra rights to locked cells after authentication.
		// They do not revoke an unlocked cell's ordinary editing permission.
		return true, nil
	}
	if kind == "tableParts" {
		return true, p.tablePartsElement(e, shape, s, seen)
	}
	isPrinter := kind == "pageSetup" && len(e.stack) == 2
	if isPrinter {
		if el, ok := e.token.(xml.StartElement); ok {
			id := attribute(el, relationNS, "id")
			if id != "" {
				if !seen["sheetData"] || seen[kind] || !printerPart.MatchString(p.links[shape.Part][id]) {
					return true, fmt.Errorf("%w：打印设置关系", ErrXLSX)
				}
				for _, a := range el.Attr {
					if !nsDeclaration(a) && a.Name.Space != "" && a.Name != (xml.Name{Space: relationNS, Local: "id"}) {
						return true, ErrXLSX
					}
				}
				seen[kind], s.printer = true, id
				return true, nil
			}
		}
	}
	if kind != "mergeCells" && kind != "autoFilter" && kind != "drawing" && kind != "legacyDrawing" {
		return false, nil
	}
	if !p.layout.Structured {
		return true, errStructuredXLSX
	}
	if el, ok := e.token.(xml.StartElement); ok && len(e.stack) == 2 {
		if !seen["sheetData"] || seen[kind] {
			return true, ErrXLSX
		}
		seen[kind] = true
		switch kind {
		case "legacyDrawing":
			id := attribute(el, relationNS, "id")
			if !relationAttributes(el, "id") || !noteDrawingPart.MatchString(p.links[shape.Part][id]) {
				return true, fmt.Errorf("%w：批注绘图关系", ErrXLSX)
			}
			s.legacyDrawing = id
			return true, nil
		case "drawing":
			id := attribute(el, relationNS, "id")
			if !relationAttributes(el, "id") || !imageDrawingPart.MatchString(p.links[shape.Part][id]) {
				return true, fmt.Errorf("%w：工作表绘图关系", ErrXLSX)
			}
			s.drawing = id
			return true, nil
		case "mergeCells":
			if !plainAttributes(el, "count") {
				return true, ErrXLSX
			}
			s.mergeTotal = attribute(el, "", "count")
			return true, nil
		case "autoFilter":
			return true, s.startFilter(el)
		}
	}
	switch token := e.token.(type) {
	case xml.StartElement:
		switch kind {
		case "mergeCells":
			rect, ok := parseRect(attribute(token, "", "ref"))
			if len(e.stack) != 3 || token.Name != (xml.Name{Space: mainNS, Local: "mergeCell"}) ||
				!plainAttributes(token, "ref") || !ok || rect.single() {
				return true, fmt.Errorf("%w：合并区域", ErrXLSX)
			}
			s.mergeCount++
			return true, s.addMerge(rect)
		case "autoFilter":
			return true, s.filterChild(e, token)
		default:
			return true, ErrXLSX
		}
	case xml.CharData:
		if len(bytes.TrimSpace(token)) != 0 {
			return true, ErrXLSX
		}
	case xml.EndElement:
		if kind == "mergeCells" && len(e.stack) == 2 {
			n, ok := nonnegativeInt(s.mergeTotal)
			if s.mergeCount == 0 || s.mergeTotal != "" && (!ok || n != s.mergeCount) {
				return true, ErrXLSX
			}
		}
	}
	return true, nil
}

func (s *worksheetStructure) startFilter(el xml.StartElement) error {
	for _, a := range el.Attr {
		if nsDeclaration(a) || a.Name == (xml.Name{Local: "ref"}) || a.Name == (xml.Name{Space: revisionNS, Local: "uid"}) {
			continue
		}
		if a.Name != (xml.Name{Space: wpsFilterNS, Local: "filterBottomFollowUsedRange"}) || !ruleBool(a.Value) {
			return fmt.Errorf("%w：筛选扩展", ErrXLSX)
		}
	}
	ref, ok := parseRect(attribute(el, "", "ref"))
	if !ok {
		return fmt.Errorf("%w：筛选范围", ErrXLSX)
	}
	s.filter = ref
	return nil
}

var filterElements = map[string][]string{
	"autoFilter":    {"extLst", "filterColumn", "sortState"},
	"filterColumn":  {"filters", "customFilters", "top10", "dynamicFilter", "colorFilter", "iconFilter"},
	"filters":       {"filter", "dateGroupItem"},
	"customFilters": {"customFilter"},
	"sortState":     {"sortCondition"},
}

func (s *worksheetStructure) filterChild(e xmlEvent, el xml.StartElement) error {
	if el.Name.Space != mainNS || !slices.Contains(filterElements[e.parent()], e.local()) {
		return fmt.Errorf("%w：筛选元素 %s", ErrXLSX, e.local())
	}
	for _, a := range el.Attr {
		if !nsDeclaration(a) && a.Name.Space != "" {
			return ErrXLSX
		}
	}
	if e.local() == "filterColumn" {
		s.filtered = true
		n, ok := nonnegativeInt(attribute(el, "", "colId"))
		if !ok || n > s.filter.lastCol-s.filter.firstCol {
			return ErrXLSX
		}
	}
	return nil
}

func (p *xlsxPackage) finishWorksheetStructure(shape XLSXSheet, s *worksheetStructure, seen map[string]bool, declared cellRect) error {
	for _, area := range s.arrayRanges {
		if seen["dimension"] && !declared.covers(area) {
			return fmt.Errorf("%w：数组公式超出工作表范围", ErrXLSX)
		}
	}
	for id, target := range p.links[shape.Part] {
		if tablePart.MatchString(target) && !s.tableIDs[id] {
			return fmt.Errorf("%w：表格关系未绑定工作表", ErrXLSX)
		}
		if imageDrawingPart.MatchString(target) && s.drawing != id {
			return fmt.Errorf("%w：绘图关系未绑定工作表", ErrXLSX)
		}
		if noteDrawingPart.MatchString(target) && s.legacyDrawing != id || printerPart.MatchString(target) && s.printer != id {
			return fmt.Errorf("%w：工作表未绑定关系 %s", ErrXLSX, target)
		}
	}

	return nil
}

type sourceFormulaTag struct {
	kind    string
	group   int
	area    cellRect
	dynamic bool
}

func (p *xlsxPackage) sourceFormulaTag(el xml.StartElement, cm string, row, col int) (sourceFormulaTag, error) {
	var tag sourceFormulaTag
	if ordinaryFormulaAttributes(el) && cm == "" {
		return tag, nil
	}
	tag.kind = attribute(el, "", "t")
	if tag.kind != "shared" && tag.kind != "array" {
		return tag, fmt.Errorf("%w：公式类型或属性", ErrXLSX)
	}
	if !p.layout.Structured {
		return tag, errStructuredXLSX
	}
	var ok bool
	switch tag.kind {
	case "shared":
		tag.group, ok = nonnegativeInt(attribute(el, "", "si"))
		if !ok || cm != "" || !plainAttributes(el, "t", "si", "ref", "ca") {
			return tag, fmt.Errorf("%w：共享公式组", ErrXLSX)
		}
	case "array":
		if !plainAttributes(el, "t", "ref", "aca", "ca") {
			return tag, fmt.Errorf("%w：数组公式属性", ErrXLSX)
		}
		tag.dynamic = cm != ""
	}
	ref := attribute(el, "", "ref")
	if ref == "" && tag.kind == "shared" {
		return tag, nil
	}
	tag.area, ok = parseRect(ref)
	if !ok || tag.area.firstRow != row || tag.area.firstCol != col {
		return tag, fmt.Errorf("%w：公式锚点或覆盖范围", ErrXLSX)
	}
	return tag, nil
}

func (p *xlsxPackage) checkSourceFormula(ctx context.Context, s *worksheetStructure, tag sourceFormulaTag, body, sheet string, row, col int) error {
	if s.window {
		if body == "" && tag.kind != "shared" && tag.kind != "dataTable" {
			return fmt.Errorf("%w：源公式缺少原文", ErrXLSX)
		}
		return nil
	}
	if !p.layout.Structured {
		err := p.layout.validateFormula(ctx, "="+body)
		if err != nil && ctx.Err() == nil {
			if _, wider := calculator.XLSXFormulaDependencies(ctx, body, p.layout.HasSheet); wider == nil && !xlsxTextEscapes.MatchString(body) {
				return errStructuredXLSX
			}
		}
		return err
	}
	master := tag.area.firstRow != 0
	if tag.kind == "shared" && !master {
		if body != "" || !s.groups[tag.group].area.contains(row, col) {
			return fmt.Errorf("%w：共享公式从格缺少有效锚点", ErrXLSX)
		}
		return nil
	}
	if body == "" || xlsxTextEscapes.MatchString(body) {
		return fmt.Errorf("%w：源公式缺少可核对的原文", ErrXLSX)
	}
	var parsed calculator.EditableFormula
	if s.dependencies != nil || tag.dynamic || tag.kind == "shared" {
		var err error
		parsed, err = p.parseFormula(ctx, body, editdoc.Address{Sheet: sheet, Row: row, Col: col}, s.dependencies)
		if err != nil && ctx.Err() == nil && len(p.layout.Tables) > 0 && !p.layout.Native && tag.kind == "" {
			// Preserve opaque source expressions through the existing coordinate
			// writer. Newly entered formulas still require a local binding.
			return errOfficeWorksheet
		}
		if err != nil {
			return err
		}

	}
	switch tag.kind {
	case "shared":
		if _, exists := s.groups[tag.group]; exists {
			return fmt.Errorf("%w：共享公式锚点重复", ErrXLSX)
		}
		if len(s.groups) >= formats.EditXLSXRangeLimit {
			return fmt.Errorf("%w：共享公式组数量超限", ErrXLSX)
		}
		if s.groups == nil {
			s.groups = map[int]sharedGroup{}
		}
		group := sharedGroup{area: tag.area, formula: parsed}
		if _, err := parsed.Shift(tag.area.lastRow-row, tag.area.lastCol-col); err != nil {
			group.readOnly = err.Error()
		}
		s.groups[tag.group] = group
	case "array":
		if len(s.arrayRanges) >= formats.EditXLSXRangeLimit {
			return fmt.Errorf("%w：数组公式范围数量超限", ErrXLSX)
		}
		s.arrayRanges = append(s.arrayRanges, tag.area)
		if tag.dynamic {
			if s.arrayDynamic == nil {
				s.arrayDynamic = map[cellRect]bool{}
			}
			s.arrayDynamic[tag.area] = true
		}
		if !tag.area.single() {
			if s.arrayThrough == nil {
				s.arrayThrough = map[int]int{}
			}
			for c := tag.area.firstCol; c <= tag.area.lastCol; c++ {
				if s.arrayThrough[c] >= row {
					return fmt.Errorf("%w：数组范围重叠", ErrXLSX)
				}
				s.arrayThrough[c] = tag.area.lastRow
			}
		}
	}
	return nil
}

func (p *xlsxPackage) parseFormula(ctx context.Context, body string, a editdoc.Address, deps map[string]bool) (calculator.EditableFormula, error) {
	sheet := a.Sheet
	useSheet := func(name string) bool {
		for _, candidate := range p.layout.Sheets {
			if strings.EqualFold(candidate.Name, name) {
				if deps != nil && sheet != candidate.Name {
					deps[candidate.Name] = true
				}
				return true
			}
		}
		return false
	}
	return calculator.ParseLocalFormula(ctx, body, useSheet, func(name string) (bool, bool) {
		if ref, _, ok := p.layout.tableReference(name, a); ok {
			useSheet(ref.Sheet)
			return ref.Top == ref.Bottom && ref.Left == ref.Right, true
		}
		target, ok := p.names[nameKey{sheet, name}]
		if !ok {
			target, ok = p.names[nameKey{"", name}]
		}
		for _, dependency := range target.sheets {
			if deps != nil && dependency != sheet {
				deps[dependency] = true
			}
		}
		return target.scalar, ok
	})
}

type sheetDependencies map[string]map[string]bool

func (p *xlsxPackage) inspectWorksheets(ctx context.Context) error {
	checked := slices.Clone(p.layout.Sheets)
	var rules []editdoc.Rule
	var generated []GeneratedArea
	addArea := func(sheet string, r cellRect, input string, pivot bool) {
		a := editdoc.Area{Start: editdoc.Address{Sheet: sheet, Row: r.firstRow, Col: r.firstCol}, EndRow: r.lastRow, EndCol: r.lastCol}
		generated = append(generated, GeneratedArea{Area: a, Input: input, Pivot: pivot})
	}
	dependencies := make(sheetDependencies, len(checked))
	locks := make(map[string]string)
	for i, s := range checked {
		data, err := p.readPart(ctx, s.Part)
		if err != nil {
			return err
		}
		var rs xlsxRuleScan
		structure := worksheetStructure{}
		if p.layout.Structured {
			structure.dependencies = map[string]bool{}
		}
		s, err = p.scanSheet(ctx, data, s, nil, &rs, nil, &structure)
		if err != nil {
			return fmt.Errorf("工作表 %s：%w", s.Name, err)
		}
		checked[i] = s
		for _, node := range rs.nodes {
			rules = append(rules, node.rule)
		}
		dependencies[s.Name] = structure.dependencies
		for _, rect := range structure.arrayRanges {
			if !rect.single() {
				addArea(s.Name, rect, s.Name, false)
			}
		}
		for _, rangeOwner := range p.pivotRanges[s.Name] {
			if rangeOwner.pivot != "" {
				addArea(s.Name, rangeOwner.area, p.pivotSources[p.pivotOutputs[rangeOwner.pivot]].sheet, true)
			}
		}
		if reason := p.locks[s.Name]; reason != "" {
			locks[s.Name] = reason
		}
	}
	// Pivot output is also a downstream input to formulas and other pivots.
	for part, cache := range p.pivotOutputs {
		out, src := p.pivotSheets[part], p.pivotSources[cache].sheet
		if out != src && src != "" {
			if dependencies[out] == nil {
				dependencies[out] = map[string]bool{}
			}
			dependencies[out][src] = true
		}
	}
	var protectSources func(string)
	protectSources = func(sheet string) {
		for dep := range dependencies[sheet] {
			if locks[dep] == "" {
				locks[dep] = lockDependency
				protectSources(dep)
			}
		}
	}
	for _, sheet := range checked {
		if locks[sheet.Name] != "" {
			protectSources(sheet.Name)
		}
	}
	for i := range checked {
		checked[i].ReadOnly = locks[checked[i].Name]
	}
	p.layout.Sheets, p.layout.Rules = checked, rules
	p.layout.Generated = generated
	p.layout.Inputs = map[string][]string{}
	for sheet, upstream := range dependencies {
		for input := range upstream {
			p.layout.Inputs[sheet] = append(p.layout.Inputs[sheet], input)
		}
		slices.Sort(p.layout.Inputs[sheet])
	}
	if !p.layout.Valid() {
		return ErrXLSX
	}
	return nil
}

func (l XLSXLayout) checkCellEdit(a editdoc.Address, locked string, protected bool, value editdoc.Value) error {
	if !l.Structured {
		return nil
	}
	if locked != "" {
		return fmt.Errorf("%s%d 只读（%s）", workbook.ColumnName(a.Col), a.Row, locked)
	}
	if protected && value.Style != "" {
		return errStructuredStyle
	}
	return nil
}

// Affected isolates the conservative sheet-level dependency closure for a
// revision; an unrelated sheet keeps its trustworthy source result.
func (l XLSXLayout) Affected(changed map[string]bool) map[string]bool {
	if !l.Structured {
		if len(changed) > 0 {
			for _, sheet := range l.Sheets {
				changed[sheet.Name] = true
			}
		}
		return changed
	}
	for more := true; more; {
		more = false
		for sheet, inputs := range l.Inputs {
			if changed[sheet] {
				continue
			}
			for _, input := range inputs {
				if changed[input] {
					changed[sheet] = true
					more = true
					break
				}
			}
		}
	}
	return changed
}
