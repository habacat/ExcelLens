package workbook

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Rule descriptions are immutable source metadata, not executable scripts.
type SourceConditionalRule struct {
	Type       string            `json:"type"`
	Priority   int               `json:"priority"`
	StopIfTrue bool              `json:"stopIfTrue,omitempty"`
	DxfID      *int              `json:"dxfId,omitempty"`
	Operator   string            `json:"operator,omitempty"`
	Text       string            `json:"text,omitempty"`
	Formulas   []string          `json:"formulas,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Notice     string            `json:"notice,omitempty"`
}

type conditionalGroup struct {
	ranges               []CellMerge
	ruleIndices          []int
	anchorRow, anchorCol int
	notice               string
}
type compiledConditionalRule struct {
	source    SourceConditionalRule
	group     int
	operands  []conditionalOperand
	predicate conditionalPredicate
	notice    string
}
type conditionalRangeEntry = sourceRangeEntry
type conditionalRangeNode = sourceRangeNode

type SourceConditionalFormats struct {
	groups []conditionalGroup
	rules  []compiledConditionalRule
	nodes  []conditionalRangeNode
	root   int
	notice string
	styles []DifferentialStyle
	bytes  int64
}

type ConditionalFormatSummary struct {
	Rules   int    `json:"rules"`
	Ranges  int    `json:"ranges"`
	Pending int    `json:"pending"`
	Notice  string `json:"notice,omitempty"`
}

func (index *SourceConditionalFormats) Summary() ConditionalFormatSummary {
	if index == nil {
		return ConditionalFormatSummary{}
	}
	out := ConditionalFormatSummary{Rules: len(index.rules), Ranges: len(index.nodes), Notice: index.notice}
	for _, rule := range index.rules {
		if rule.notice != "" {
			out.Pending++
		}
	}
	for _, group := range index.groups {
		if len(group.ruleIndices) == 0 && group.notice != "" {
			out.Pending++
		}
	}
	return out
}

func parseConditionalRanges(raw string) ([]CellMerge, error) {
	if len(raw) > formats.ConditionalMetadataBytes {
		return nil, errors.New("条件格式适用范围为空或超限")
	}
	var out []CellMerge
	// Iterate and stop at the capacity boundary. Splitting a hostile sqref
	// into millions of fields/colons would allocate before validating the cap.
	for part := range strings.FieldsSeq(raw) {
		if len(out) >= formats.ConditionalRanges {
			return nil, errors.New("条件格式适用范围为空或超限")
		}
		start, finish, ranged := strings.Cut(part, ":")
		if strings.Contains(finish, ":") {
			return nil, errors.New("条件格式范围无效")
		}
		first, ok := compileConditionalOperand(start)
		if !ok || first.reference == nil {
			return nil, errors.New("条件格式起点无效")
		}
		last := first
		if ranged {
			last, ok = compileConditionalOperand(finish)
			if !ok || last.reference == nil {
				return nil, errors.New("条件格式终点无效")
			}
		}
		a, b := first.reference, last.reference
		if b.row < a.row || b.col < a.col {
			return nil, errors.New("条件格式范围顺序无效")
		}
		out = append(out, CellMerge{Row: a.row, Col: a.col, EndRow: b.row, EndCol: b.col})
	}
	if len(out) == 0 {
		return nil, errors.New("条件格式适用范围为空或超限")
	}
	return out, nil
}

func readConditionalFormula(d *xml.Decoder, start xml.StartElement) (string, bool, error) {
	var text strings.Builder
	complete := len(start.Attr) == 0
	for {
		t, err := d.Token()
		if err != nil {
			return "", false, err
		}
		switch t := t.(type) {
		case xml.CharData:
			if text.Len()+len(t) <= formats.ConditionalFormulaBytes {
				text.Write(t)
			} else {
				complete = false
			}
		case xml.StartElement:
			complete = false
			if err := d.Skip(); err != nil {
				return "", false, err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				if !complete {
					return "", false, nil
				}
				return text.String(), true, nil
			}
		}
	}
}

func readSourceConditionalRule(d *xml.Decoder, start xml.StartElement) (SourceConditionalRule, error) {
	out := SourceConditionalRule{}
	seen := make(map[string]bool, 8)
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if a.Name.Space != "" {
			out.Notice = "条件规则含未知命名空间的属性"
			continue
		}
		if len(a.Value) > formats.ConditionalFormulaBytes || len(a.Name.Local) > formats.ConditionalFormulaBytes {
			out.Notice = "条件格式参数超过读取容量，原文保留在源文件"
			continue
		}
		if seen[a.Name.Local] {
			out.Notice = "条件格式属性重复，未自动解释"
		}
		if len(seen) < formats.ConditionalParameterCount {
			seen[a.Name.Local] = true
		}
		switch a.Name.Local {
		case "type":
			out.Type = a.Value
		case "priority":
			v, err := strconv.Atoi(a.Value)
			if err != nil || v <= 0 || v > 1_000_000_000 {
				out.Notice = "条件格式优先级无效"
			} else {
				out.Priority = v
			}
		case "dxfId":
			v, err := strconv.Atoi(a.Value)
			if err != nil || v < 0 {
				out.Notice = "条件样式编号无效"
			} else {
				out.DxfID = &v
			}
		case "stopIfTrue":
			v, valid := sourceBool([]byte(a.Value))
			if !valid {
				out.Notice = "条件格式停止标记无效"
			} else {
				out.StopIfTrue = v
			}
		case "operator":
			out.Operator = a.Value
		case "text":
			out.Text = a.Value
		default:
			if len(out.Parameters) >= formats.ConditionalParameterCount {
				out.Notice = "条件格式参数数量超过读取容量，原文保留在源文件"
				continue
			}
			if out.Parameters == nil {
				out.Parameters = make(map[string]string)
			}
			out.Parameters[a.Name.Local] = a.Value
		}
	}
	if out.Priority == 0 {
		out.Notice = "条件格式缺少有效优先级"
	}
	for {
		t, err := d.Token()
		if err != nil {
			return out, err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if t.Name.Local == "formula" && t.Name.Space == start.Name.Space {
				value, complete, err := readConditionalFormula(d, t)
				if err != nil {
					return out, err
				}
				if !complete || len(out.Formulas) >= formats.ConditionalFormulaCount {
					out.Notice = "条件格式表达式超限或含未知结构，原式保留在源文件"
				} else {
					out.Formulas = append(out.Formulas, value)
				}
			} else {
				out.Notice = "条件格式含范围统计、图形或扩展结构，尚未求值"
				if err := d.Skip(); err != nil {
					return out, err
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return out, nil
			}
		}
	}
}

func compileSourceConditionalRule(source SourceConditionalRule, group int) compiledConditionalRule {
	out := compiledConditionalRule{source: source, group: group, notice: source.Notice}
	if out.notice != "" {
		return out
	}
	if len(source.Parameters) > 0 {
		out.notice = "条件格式含尚未解释的参数"
		return out
	}
	switch source.Type {
	case "cellIs":
		need := 1
		switch source.Operator {
		case "equal", "notEqual", "greaterThan", "lessThan", "greaterThanOrEqual", "lessThanOrEqual":
		case "between", "notBetween":
			need = 2
		default:
			out.notice = "条件比较方式尚未支持"
			return out
		}
		if len(source.Formulas) != need {
			out.notice = "条件比较的表达式数量无效"
			return out
		}
		for _, formula := range source.Formulas {
			operand, ok := compileConditionalOperand(strings.TrimPrefix(strings.TrimSpace(formula), "="))
			if !ok {
				out.notice = "条件比较表达式需要尚未支持的计算"
				return out
			}
			out.operands = append(out.operands, operand)
		}
	case "expression":
		if len(source.Formulas) != 1 {
			out.notice = "条件格式公式数量无效"
			return out
		}
		var ok bool
		out.predicate, ok = compileConditionalPredicate(source.Formulas[0])
		if !ok {
			out.notice = "条件格式公式尚未求值"
		}
	case "containsBlanks", "notContainsBlanks", "containsErrors", "notContainsErrors", "containsText", "notContainsText", "beginsWith", "endsWith":
		if len(source.Formulas) > 1 || strings.ContainsAny(source.Text, "*?~") {
			out.notice = "文本条件的通配符或表达式尚未支持"
		}
	default:
		out.notice = "该条件格式需要范围统计或尚未支持的规则，不自动扫描整表"
	}
	return out
}

// Standard text/blank/error rules also carry a formula. Validate that formula
// later against the group's anchor; a contradictory custom formula is never
// silently replaced with a convenient interpretation of the type attribute.
func canonicalConditionalFormula(source SourceConditionalRule, row, col int) bool {
	if len(source.Formulas) == 0 {
		return true
	}
	if len(source.Formulas) != 1 || strings.ContainsAny(source.Text, "*?~") {
		return false
	}
	ref := ColumnName(col) + strconv.Itoa(row)
	text := `"` + strings.ReplaceAll(source.Text, `"`, `""`) + `"`
	var expected []string
	switch source.Type {
	case "containsBlanks":
		expected = []string{"LEN(TRIM(" + ref + "))=0"}
	case "notContainsBlanks":
		expected = []string{"LEN(TRIM(" + ref + "))>0"}
	case "containsErrors":
		expected = []string{"ISERROR(" + ref + ")"}
	case "notContainsErrors":
		expected = []string{"NOT(ISERROR(" + ref + "))"}
	case "containsText":
		expected = []string{"NOT(ISERROR(SEARCH(" + text + "," + ref + ")))", "ISNUMBER(SEARCH(" + text + "," + ref + "))"}
	case "notContainsText":
		expected = []string{"ISERROR(SEARCH(" + text + "," + ref + "))"}
	case "beginsWith":
		expected = []string{"LEFT(" + ref + ",LEN(" + text + "))=" + text}
	case "endsWith":
		expected = []string{"RIGHT(" + ref + ",LEN(" + text + "))=" + text}
	default:
		return true
	}
	actual := normalizeConditionalFormula(source.Formulas[0])
	for _, formula := range expected {
		if actual == normalizeConditionalFormula(formula) {
			return true
		}
	}
	return false
}

func normalizeConditionalFormula(raw string) string {
	var b strings.Builder
	quoted := false
	for _, r := range strings.TrimPrefix(strings.TrimSpace(raw), "=") {
		if r == '"' {
			quoted = !quoted
			b.WriteRune(r)
			continue
		}
		if !quoted {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				continue
			}
			if r >= 'a' && r <= 'z' {
				r -= 32
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (w *Workbook) loadConditionalFormats(ctx context.Context, meta SheetMeta, idx *SheetIndex) (*SourceConditionalFormats, error) {
	layout, err := w.SourceLayoutContext(ctx, meta, idx)
	if err != nil {
		return nil, err
	}
	index := &SourceConditionalFormats{root: -1, notice: layout.ConditionalNotice, styles: w.differentialStyles}
	if len(layout.ConditionalBlocks) == 0 {
		return index, nil
	}
	if w.differentialNotice != "" {
		index.notice = w.differentialNotice
	}
	file, err := os.Open(idx.XMLPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var entries []conditionalRangeEntry
	for _, block := range layout.ConditionalBlocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if block.Start < 0 || block.End > stat.Size() || block.End <= block.Start {
			return nil, errors.New("条件格式缓存偏移无效")
		}
		d := xml.NewDecoder(io.MultiReader(strings.NewReader(layout.conditionalWrapper()), io.NewSectionReader(file, block.Start, block.End-block.Start), strings.NewReader("</worksheet>")))
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "conditionalFormatting" || start.Name.Space != layout.ConditionalMainNamespace {
			return nil, errors.New("条件格式缓存范围与XML不符")
		}
		group := conditionalGroup{}
		seenRange := false
		for _, a := range start.Attr {
			if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
				continue
			}
			if a.Name.Local == "sqref" && a.Name.Space == "" {
				if seenRange {
					index.notice = "条件格式适用范围属性重复"
				}
				seenRange = true
				group.ranges, err = parseConditionalRanges(a.Value)
				if err != nil {
					index.notice = err.Error()
				}
			}
			if a.Name.Space != "" || a.Name.Local != "sqref" && !(a.Name.Local == "pivot" && a.Value == "0") {
				group.notice = "条件格式适用对象含尚未支持的属性"
			}
		}
		if len(group.ranges) == 0 {
			index.notice = "条件格式缺少可靠适用范围"
		} else {
			group.anchorRow, group.anchorCol = group.ranges[0].Row, group.ranges[0].Col
		}
		groupID := len(index.groups)
		firstRule := len(index.rules)
		for _, area := range group.ranges {
			if len(entries) >= formats.ConditionalRanges {
				return nil, errors.New("条件格式适用范围总数超限")
			}
			entries = append(entries, conditionalRangeEntry{area: area, group: groupID})
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			token, err := d.Token()
			if err != nil {
				return nil, fmt.Errorf("条件格式XML损坏：%w", err)
			}
			if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
				break
			}
			if child, ok := token.(xml.StartElement); ok {
				if child.Name.Local != "cfRule" || child.Name.Space != start.Name.Space {
					group.notice = "条件格式含未知子项，未完整还原"
					if err := d.Skip(); err != nil {
						return nil, err
					}
					continue
				}
				if len(index.rules) >= formats.ConditionalRules {
					return nil, errors.New("条件格式规则数量超限")
				}
				rule, err := readSourceConditionalRule(d, child)
				if err != nil {
					return nil, err
				}
				index.rules = append(index.rules, compileSourceConditionalRule(rule, groupID))
			}
		}
		if end, err := d.Token(); err != nil {
			return nil, err
		} else if node, ok := end.(xml.EndElement); !ok || node.Name.Local != "worksheet" {
			return nil, errors.New("条件格式缓存块存在尾随内容")
		}
		if _, err := d.Token(); err != io.EOF {
			return nil, errors.New("条件格式缓存块存在尾随内容")
		}
		if len(index.rules) == firstRule && group.notice == "" {
			group.notice = "条件格式块没有可读取的规则，未完整还原"
		}
		index.groups = append(index.groups, group)
	}
	sort.SliceStable(index.rules, func(i, j int) bool { return index.rules[i].source.Priority < index.rules[j].source.Priority })
	for i := range index.rules {
		rule := &index.rules[i]
		group := &index.groups[rule.group]
		if i > 0 && rule.source.Priority == index.rules[i-1].source.Priority {
			index.notice = "条件格式优先级重复，无法可靠应用"
		}
		if rule.source.Priority <= 0 {
			index.notice = "条件格式缺少可靠优先级"
		}
		if group.notice != "" {
			rule.notice = group.notice
		}
		if rule.notice == "" && !canonicalConditionalFormula(rule.source, group.anchorRow, group.anchorCol) {
			rule.notice = "条件格式公式与声明类型不一致或尚未支持"
		}
		if rule.source.DxfID != nil {
			id := *rule.source.DxfID
			if id < 0 || id >= len(index.styles) {
				rule.notice = "条件格式引用的差异样式不可用"
			} else if index.styles[id].Notice != "" {
				rule.notice = index.styles[id].Notice
			}
		}
		group.ruleIndices = append(group.ruleIndices, i)
	}
	index.nodes, index.root = buildSourceRangeTree(entries)
	index.bytes = conditionalIndexBytes(index)
	return index, nil
}

func conditionalIndexBytes(index *SourceConditionalFormats) int64 {
	n := int64(256 + cap(index.nodes)*96 + cap(index.groups)*96 + cap(index.rules)*384 + len(index.notice)*2)
	for _, g := range index.groups {
		n += int64(cap(g.ranges)*32 + cap(g.ruleIndices)*8 + len(g.notice)*2)
	}
	for _, r := range index.rules {
		s := r.source
		n += int64(cap(r.operands)*128 + cap(s.Formulas)*16)
		n += int64(2 * (len(s.Type) + len(s.Text) + len(s.Operator) + len(s.Notice) + len(r.notice)))
		for _, f := range s.Formulas {
			n += int64(len(f)*4 + 64)
		}
		for k, v := range s.Parameters {
			n += int64(2*(len(k)+len(v)) + 96)
		}
	}
	return n
}
