package workbook

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

func (f *xlsbFields) areas(limit int) []CellMerge {
	count := int(f.u32())
	if count < 0 || count > limit || count > len(f.data)/16 {
		f.err = errors.New("BIFF12 区域列表长度无效或超限")
		return nil
	}
	out := make([]CellMerge, count)
	for i := range out {
		out[i] = f.area()
	}
	return out
}

// MS-XLSB FRTHeader. Callers admit only the fields valid in their record.
func (f *xlsbFields) futureRangesAndFormulas(maxRanges, maxFormulas int) ([]CellMerge, []xlsbFormula) {
	flags := f.u32()
	if flags&^uint32(6) != 0 {
		f.err = errors.New("BIFF12 扩展记录头含无效字段")
		return nil, nil
	}
	var ranges []CellMerge
	var formulas []xlsbFormula
	if flags&2 != 0 {
		count := int(f.u32())
		if count < 0 || count > maxRanges {
			f.err = errors.New("BIFF12 扩展适用区域超限")
		}
		for i := 0; i < count && f.err == nil; i++ {
			f.u32() // FRTSqref adjustment flags
			ranges = append(ranges, f.areas(maxRanges-len(ranges))...)
		}
	}
	if flags&4 != 0 {
		count := int(f.u32())
		if count < 0 || count > maxFormulas {
			f.err = errors.New("BIFF12 扩展公式数量超限")
		}
		for i := 0; i < count && f.err == nil; i++ {
			f.u32() // FRTFormula flags
			n, extra := int(f.u32()), int(f.u32())
			if n > formats.BIFFFormulaBytes {
				f.err = errors.New("BIFF12 扩展公式长度超限")
			}
			formulas = append(formulas, xlsbFormula{f.take(n), f.take(extra)})
		}
	}
	return ranges, formulas
}

var xlsbCompareOperators = [...]string{"between", "notBetween", "equal", "notEqual", "greaterThan", "lessThan", "greaterThanOrEqual", "lessThanOrEqual"}

func (w *Workbook) xlsbDataRules(ctx context.Context, meta SheetMeta, b *dataRuleBuilder) error {
	m, err := w.xlsbSheetMeta(ctx, meta)
	if err != nil {
		return err
	}
	for _, record := range m.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch record.Kind {
		case brtBeginDVals, brtBeginDVals14:
			f := xlsbFields{data: record.Data}
			if record.Kind == brtBeginDVals14 {
				f.take(4)
			}
			flags, left, top := f.u16(), f.u32(), f.u32()
			f.u32()
			count := f.u32()
			if f.err != nil {
				return f.err
			}
			closed := flags&1 != 0
			if err := b.addContainer(DataRuleContainer{Namespace: "BIFF12", DisablePrompts: closed,
				Attributes: map[string]string{"count": fmt.Sprint(count), "xWindow": fmt.Sprint(left), "yWindow": fmt.Sprint(top)}}); err != nil {
				return err
			}
			b.index.summary.InputWindowClosed = closed
		case brtDVal, brtDVal14:
			rule, err := w.source.xlsb.decodeDataRule(record.Kind, record.Data)
			if err != nil {
				return err
			}
			if err := b.add(rule); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *xlsbBook) decodeDataRule(kind uint16, data []byte) (SourceDataRule, error) {
	f := xlsbFields{data: data}
	out := SourceDataRule{Namespace: "BIFF12", Status: "described"}
	var formulas []xlsbFormula
	if kind == brtDVal14 {
		out.Ranges, formulas = f.futureRangesAndFormulas(formats.DataRuleRanges, 2)
	}
	flags := f.u32()
	if kind == brtDVal {
		out.Ranges = f.areas(formats.DataRuleRanges)
	}
	types := [...]string{"none", "whole", "decimal", "list", "date", "time", "textLength", "custom"}
	alerts := [...]string{"stop", "warning", "information"}
	imes := [...]string{"noControl", "off", "on", "disabled", "hiragana", "fullKatakana", "halfKatakana", "fullAlpha", "halfAlpha", "fullHangul", "halfHangul"}
	field := func(values []string, i uint32) string {
		if i >= uint32(len(values)) {
			dataRuleIssue(&out, "invalid", "BIFF12 数据规则含无效的枚举")
			return fmt.Sprintf("BIFF12:%d", i)
		}
		return values[i]
	}
	out.Type, out.Operator = field(types[:], flags&15), field(xlsbCompareOperators[:], flags>>20&15)
	out.ErrorStyle, out.IMEMode = field(alerts[:], flags>>4&7), field(imes[:], flags>>10&255)
	out.AllowBlank, out.SuppressDropDown = flags&256 != 0, flags&512 != 0
	out.ShowInputMessage, out.ShowErrorMessage = flags&(1<<18) != 0, flags&(1<<19) != 0
	out.ErrorTitle, out.Error = f.wide(true, 32767), f.wide(true, 32767)
	out.PromptTitle, out.Prompt = f.wide(true, 32767), f.wide(true, 32767)
	if kind == brtDVal {
		formulas = []xlsbFormula{f.formula(), f.formula()}
	}
	if f.err != nil {
		return out, f.err
	}
	if len(out.Ranges) == 0 {
		dataRuleIssue(&out, "invalid", "BIFF12 数据规则缺少适用范围")
		return out, nil
	}
	refs := make([]string, len(out.Ranges))
	for i, area := range out.Ranges {
		refs[i] = xlsbAreaRefText(area)
	}
	out.SqRef = strings.Join(refs, " ")
	row, col := out.Ranges[0].Row-1, out.Ranges[0].Col-1
	targets := []*string{&out.Formula1, &out.Formula2}
	tokens := []*string{&out.Formula1Tokens, &out.Formula2Tokens}
	for i, formula := range formulas {
		if len(formula.tokens) == 0 {
			continue
		}
		text, ok := b.formulaText(formula, row, col, true)
		if !ok {
			*tokens[i] = hex.EncodeToString(formula.tokens)
			dataRuleIssue(&out, "unresolved", "XLSB 数据规则公式无法完整解释")
			continue
		}
		*targets[i] = strings.TrimPrefix(text, "=")
	}
	if out.Type == "list" && out.Formula1 != "" {
		out.ListDefinition = out.Formula1
		if values, ok := dataRuleInlineList(out.Formula1, ","); ok {
			out.InlineList, out.ListSource = values, "literal"
		} else {
			out.ListSource = "expression"
			dataRuleIssue(&out, "unresolved", unresolvedListReference)
		}
	}
	return out, nil
}

func (w *Workbook) loadXLSBConditional(ctx context.Context, meta SheetMeta) (*SourceConditionalFormats, error) {
	m, err := w.xlsbSheetMeta(ctx, meta)
	if err != nil {
		return nil, err
	}
	result := &SourceConditionalFormats{root: -1, styles: w.differentialStyles, sheetName: meta.Name, date1904: w.Date1904}
	var entries []conditionalRangeEntry
	group, groupStart, remaining, rangeCount := -1, 0, 0, 0
	var rule *xlsbConditionalRule
	extensionOwners := make(map[[16]byte]int)
	extensions := make(map[[16]byte]xlsbConditionalRule)
	for _, record := range m.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f := xlsbFields{data: record.Data}
		switch record.Kind {
		case brtBeginConditionalFormatting, brtBeginConditionalFormatting14:
			var ranges []CellMerge
			if record.Kind == brtBeginConditionalFormatting14 {
				ranges, _ = f.futureRangesAndFormulas(formats.ConditionalRanges-rangeCount, 0)
			}
			count, pivot := f.u32(), f.u32()
			if record.Kind == brtBeginConditionalFormatting {
				ranges = f.areas(formats.ConditionalRanges - rangeCount)
			}
			if group != -1 || len(ranges) == 0 || count > formats.ConditionalRules {
				return nil, errors.New("XLSB 条件格式组范围或记录数量无效")
			}
			g := conditionalGroup{ranges: ranges, anchorRow: ranges[0].Row, anchorCol: ranges[0].Col}
			if pivot != 0 {
				g.notice = "此 XLSB 条件格式按透视表切片定位"
			}
			group = len(result.groups)
			result.groups = append(result.groups, g)
			groupStart, remaining = len(result.rules), int(count)
			rangeCount += len(ranges)
		case brtEndConditionalFormatting, brtEndConditionalFormatting14:
			if group < 0 || rule != nil || remaining != 0 {
				return nil, errors.New("XLSB 条件格式组未完整结束")
			}
			if len(result.rules) == groupStart {
				result.groups = result.groups[:group]
			} else {
				for _, area := range result.groups[group].ranges {
					entries = append(entries, conditionalRangeEntry{area, group})
				}
			}
			group = -1
		case brtBeginCFRule, brtBeginCFRule14:
			if group < 0 || rule != nil || remaining == 0 || len(result.rules)+len(extensions) >= formats.ConditionalRules {
				return nil, errors.New("XLSB 条件格式规则位置或数量无效")
			}
			g := result.groups[group]
			value, err := w.decodeXLSBConditionalRule(record.Data, record.Kind == brtBeginCFRule14, g.anchorRow, g.anchorCol)
			if err != nil {
				return nil, err
			}
			rule = &value
		case brtEndCFRule, brtEndCFRule14:
			if group < 0 || rule == nil {
				return nil, errors.New("XLSB 条件格式规则未完整结束")
			}
			remaining--
			if record.Kind == brtEndCFRule14 && rule.Priority == -1 {
				if rule.hasGUID {
					if _, exists := extensions[rule.guid]; exists {
						return nil, errors.New("XLSB 条件格式扩展标识重复")
					}
					extensions[rule.guid] = *rule
				}
			} else {
				if record.Kind == brtEndCFRule && rule.hasGUID {
					if _, exists := extensionOwners[rule.guid]; exists {
						return nil, errors.New("XLSB 条件格式扩展引用重复")
					}
					extensionOwners[rule.guid] = len(result.rules)
				}
				result.rules = append(result.rules, compileSourceConditionalRule(rule.SourceConditionalRule, group, meta.Name))
			}
			rule = nil
		case brtCFRuleExt:
			if rule == nil || rule.Type != "dataBar" || rule.hasGUID {
				return nil, errors.New("XLSB 条件格式扩展位置无效")
			}
			f.take(4)
			copy(rule.guid[:], f.take(16))
			rule.hasGUID = true
		case brtColor, brtColor14, brtCFVO, brtCFVO14, brtBeginDatabar, brtBeginDatabar14, brtBeginIconSet, brtBeginIconSet14, brtCFIcon:
			if rule != nil {
				if err := w.xlsbConditionalParameter(&rule.SourceConditionalRule, record.Kind, &f, result.groups[group]); err != nil {
					return nil, err
				}
			}
		}
		if f.err != nil {
			return nil, f.err
		}
	}
	if group >= 0 || rule != nil {
		return nil, errors.New("XLSB 条件格式缺失终止记录")
	}
	for guid, owner := range extensionOwners {
		compiled := &result.rules[owner]
		if extension, ok := extensions[guid]; ok {
			if compiled.source.Parameters == nil {
				compiled.source.Parameters = make(map[string]string)
			}
			for k, v := range extension.Parameters {
				compiled.source.Parameters["ext14-"+k] = v
			}
		} else {
			compiled.source.Notice = "XLSB 数据条扩展缺失，无法完整还原"
			compiled.predicateNotice = compiled.source.Notice
		}
	}
	return finishConditionalFormats(result, entries), nil
}

func (w *Workbook) xlsbConditionalParameter(rule *SourceConditionalRule, kind uint16, f *xlsbFields, group conditionalGroup) error {
	switch kind {
	case brtColor, brtColor14:
		if kind == brtColor14 {
			f.take(4)
		}
		color := w.resolveColor(xlsbColor(f), w.IndexedColors)
		xlsbCFParameter(rule, "color", color)
	case brtCFVO, brtCFVO14:
		var formulas []xlsbFormula
		if kind == brtCFVO14 {
			_, formulas = f.futureRangesAndFormulas(0, 1)
		}
		typ, value := f.u32(), f.f64()
		saveGTE, gte := f.u32(), f.u32()
		tokenBytes := f.u32()
		parameter := strconv.FormatUint(uint64(typ), 10)
		if tokenBytes != 0 || len(formulas) != 0 {
			if kind == brtCFVO {
				formulas = []xlsbFormula{f.formula()}
			}
			if len(formulas) != 1 || tokenBytes != uint32(len(formulas[0].tokens)) {
				return errors.New("XLSB 条件格式阈值公式长度不一致")
			}
			text, _ := w.source.xlsb.formulaText(formulas[0], group.anchorRow-1, group.anchorCol-1, true)
			parameter += ":" + text
		} else if typ != 2 && typ != 3 && typ != 8 && typ != 9 {
			parameter += ":" + xlsbNumber(value)
		}
		if saveGTE == 1 && rule.Type == "iconSet" {
			parameter += fmt.Sprintf(":gte=%t", gte == 1)
		}
		xlsbCFParameter(rule, "threshold", parameter)
	case brtBeginDatabar, brtBeginDatabar14:
		if kind == brtBeginDatabar14 {
			f.take(4)
		}
		xlsbCFParameter(rule, "minLength", strconv.Itoa(int(f.u8())))
		xlsbCFParameter(rule, "maxLength", strconv.Itoa(int(f.u8())))
		xlsbCFParameter(rule, "showValue", strconv.FormatBool(f.u8() == 1))
		if kind == brtBeginDatabar14 {
			xlsbCFParameter(rule, "direction", strconv.Itoa(int(f.u8())))
			xlsbCFParameter(rule, "axisPosition", strconv.Itoa(int(f.u8())))
			flags := f.u16()
			xlsbCFParameter(rule, "border", strconv.FormatBool(flags&1 != 0))
			xlsbCFParameter(rule, "gradient", strconv.FormatBool(flags&2 != 0))
			xlsbCFParameter(rule, "customNegativeFill", strconv.FormatBool(flags&4 != 0))
			xlsbCFParameter(rule, "customNegativeBorder", strconv.FormatBool(flags&8 != 0))
		}
	case brtBeginIconSet, brtBeginIconSet14:
		if kind == brtBeginIconSet14 {
			f.take(4)
		}
		xlsbCFParameter(rule, "iconSet", strconv.Itoa(int(int32(f.u32()))))
		flags := f.u16()
		xlsbCFParameter(rule, "custom", strconv.FormatBool(flags&1 != 0))
		xlsbCFParameter(rule, "showValue", strconv.FormatBool(flags&2 == 0))
		xlsbCFParameter(rule, "reverse", strconv.FormatBool(flags&4 == 0))
	case brtCFIcon:
		f.take(4)
		xlsbCFParameter(rule, "icon", fmt.Sprintf("%d:%d", int32(f.u32()), int32(f.u32())))
	}
	return f.err
}

func xlsbCFParameter(rule *SourceConditionalRule, name, value string) {
	if rule.Parameters == nil {
		rule.Parameters = make(map[string]string)
	}
	rule.Parameters[fmt.Sprintf("%s-%d", name, len(rule.Parameters))] = value
}

type xlsbConditionalRule struct {
	SourceConditionalRule
	guid    [16]byte
	hasGUID bool
}

func (w *Workbook) decodeXLSBConditionalRule(data []byte, extended bool, row, col int) (xlsbConditionalRule, error) {
	f := xlsbFields{data: data}
	var formulas []xlsbFormula
	if extended {
		_, formulas = f.futureRangesAndFormulas(0, 2)
	}
	kind, template, style, priority := f.u32(), f.u32(), f.u32(), int(int32(f.u32()))
	parameter := f.u32()
	f.take(8)
	flags := f.u16()
	lengths := [3]uint32{f.u32(), f.u32(), f.u32()}
	out := xlsbConditionalRule{SourceConditionalRule: SourceConditionalRule{Priority: priority, StopIfTrue: flags&2 != 0}}
	if extended {
		f.take(4) // unused
		copy(out.guid[:], f.take(16))
		out.hasGUID = f.u32() == 1
	}
	// Excel omits the unused NULL string on CFRule14 data-bar extensions.
	if !extended || len(f.data) != 0 || template == 8 {
		out.Text = f.wide(true, 32767)
	}
	if style != ^uint32(0) && priority != -1 {
		base := w.source.xlsb.baseDXFCount
		id := -1
		if extended && uint64(style) < uint64(len(w.differentialStyles)-base) {
			id = base + int(style)
		} else if !extended && uint64(style) < uint64(base) {
			id = int(style)
		}
		out.DxfID = &id
	}
	switch kind {
	case 1:
		out.Type = "cellIs"
		if parameter >= 1 && parameter <= uint32(len(xlsbCompareOperators)) {
			out.Operator = xlsbCompareOperators[parameter-1]
		}
	case 2:
		out.Type = map[uint32]string{1: "expression", 7: "uniqueValues", 9: "containsBlanks", 10: "notContainsBlanks", 11: "containsErrors", 12: "notContainsErrors", 25: "aboveAverage", 26: "aboveAverage", 27: "duplicateValues", 29: "aboveAverage", 30: "aboveAverage"}[template]
		if template == 8 {
			out.Type = map[uint32]string{0: "containsText", 1: "notContainsText", 2: "beginsWith", 3: "endsWith"}[parameter]
		}
		if template >= 15 && template <= 24 {
			out.Type = "timePeriod"
			xlsbCFParameter(&out.SourceConditionalRule, "period", strconv.Itoa(int(template)))
		}
		if out.Type == "aboveAverage" {
			xlsbCFParameter(&out.SourceConditionalRule, "aboveAverage", strconv.FormatBool(flags&4 != 0))
			xlsbCFParameter(&out.SourceConditionalRule, "equalAverage", strconv.FormatBool(template == 29 || template == 30))
			xlsbCFParameter(&out.SourceConditionalRule, "stdDev", strconv.Itoa(int(parameter)))
		}
	case 3:
		out.Type = "colorScale"
	case 4:
		out.Type = "dataBar"
	case 5:
		out.Type = "top10"
		xlsbCFParameter(&out.SourceConditionalRule, "rank", strconv.Itoa(int(parameter)))
		xlsbCFParameter(&out.SourceConditionalRule, "bottom", strconv.FormatBool(flags&8 != 0))
		xlsbCFParameter(&out.SourceConditionalRule, "percent", strconv.FormatBool(flags&16 != 0))
	case 6:
		out.Type = "iconSet"
	}
	if out.Type == "" {
		out.Type, out.Notice = fmt.Sprintf("BIFF12 %d/%d", kind, template), "XLSB 条件格式模板无法解释"
	}
	for _, length := range lengths {
		if length == 0 {
			continue
		}
		var parsed xlsbFormula
		if extended {
			if len(formulas) == 0 {
				return out, errors.New("XLSB 扩展条件规则缺少公式")
			}
			parsed, formulas = formulas[0], formulas[1:]
		} else {
			parsed = f.formula()
		}
		if f.err != nil {
			return out, f.err
		}
		// MS-XLSB BrtBeginCFRule uses nonzero cbFmla fields as presence
		// flags. CFRule14 additionally requires the token length to agree.
		if len(parsed.tokens) == 0 || (extended && uint32(len(parsed.tokens)) != length) {
			return out, errors.New("XLSB 条件格式公式长度不一致")
		}
		text, ok := w.source.xlsb.formulaText(parsed, row-1, col-1, true)
		if !ok {
			out.Notice = "XLSB 条件格式公式无法解释"
		} else if strings.HasPrefix(text, "=(") && strings.HasSuffix(text, ")") {
			text = "=" + text[2:len(text)-1]
		}
		out.Formulas = append(out.Formulas, text)
	}
	if len(formulas) != 0 {
		return out, errors.New("XLSB 扩展条件规则含无关联公式")
	}
	if out.Notice == "" && (out.Type == "dataBar" || out.Type == "colorScale" || out.Type == "iconSet") {
		out.Notice = "条件格式含图形和范围统计，当前网格未求值"
	}
	return out, f.err
}
