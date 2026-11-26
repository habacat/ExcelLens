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
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"excellens/internal/conditional"
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
	areas                []conditional.Area
	ruleIndices          []int
	anchorRow, anchorCol int
	notice               string
}
type compiledConditionalRule struct {
	source          SourceConditionalRule
	group           int
	exprs           []*conditionalExpr
	exprNodes       int
	predicateNotice string
	styleNotice     string
}
type conditionalRangeEntry = sourceRangeEntry
type conditionalRangeNode = sourceRangeNode

type SourceConditionalFormats struct {
	statistics []conditional.StatPlan
	statsMu    sync.Mutex
	statsIssue string
	statsRetry time.Time
	groups     []conditionalGroup
	rules      []compiledConditionalRule
	nodes      []conditionalRangeNode
	root       int
	notice     string
	styles     []DifferentialStyle
	sheetName  string
	date1904   bool
	bytes      int64
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
		if rule.predicateNotice != "" || rule.styleNotice != "" {
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
		if a, ok := conditional.ParseCellRef(start); ok {
			b := a
			if ranged {
				b, ok = conditional.ParseCellRef(finish)
				if !ok {
					return nil, errors.New("条件格式终点无效")
				}
			}
			if b.Row < a.Row || b.Col < a.Col {
				return nil, errors.New("条件格式范围顺序无效")
			}
			out = append(out, CellMerge{Row: a.Row, Col: a.Col, EndRow: b.Row, EndCol: b.Col})
			continue
		}
		if ranged {
			if a, ok1 := conditional.ParseColumnRef(start); ok1 {
				b, ok2 := conditional.ParseColumnRef(finish)
				if !ok2 {
					return nil, errors.New("条件格式终点无效")
				}
				if b.Col < a.Col {
					return nil, errors.New("条件格式范围顺序无效")
				}
				out = append(out, CellMerge{Row: 1, Col: a.Col, EndRow: formats.ExcelRows, EndCol: b.Col})
				continue
			}
			if a, ok1 := conditional.ParseRowRef(start); ok1 {
				b, ok2 := conditional.ParseRowRef(finish)
				if !ok2 {
					return nil, errors.New("条件格式终点无效")
				}
				if b.Row < a.Row {
					return nil, errors.New("条件格式范围顺序无效")
				}
				out = append(out, CellMerge{Row: a.Row, Col: 1, EndRow: b.Row, EndCol: formats.ExcelColumns})
				continue
			}
		}
		return nil, errors.New("条件格式起点无效")
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

func (w *Workbook) compileSourceConditionalRule(source SourceConditionalRule, group int, sheetName string) compiledConditionalRule {
	out := compiledConditionalRule{source: source, group: group, predicateNotice: source.Notice}
	if out.predicateNotice != "" {
		return out
	}
	if len(source.Parameters) > 0 {
		out.predicateNotice = "条件格式含尚未解释的参数"
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
			out.predicateNotice = "条件比较方式尚未支持"
			return out
		}
		if len(source.Formulas) != need {
			out.predicateNotice = "条件比较的表达式数量无效"
			return out
		}
		for _, formula := range source.Formulas {
			expr, nodes, notice := w.compileConditionalExpr(formula, sheetName)
			if notice != "" {
				if notice == "条件格式公式尚未求值" {
					out.predicateNotice = "条件比较表达式需要尚未支持的计算"
				} else {
					out.predicateNotice = notice
				}
				out.exprs = nil
				return out
			}
			out.exprs = append(out.exprs, expr)
			out.exprNodes += nodes
		}
	case "expression":
		if len(source.Formulas) != 1 {
			out.predicateNotice = "条件格式公式数量无效"
			return out
		}
		expr, nodes, notice := w.compileConditionalExpr(source.Formulas[0], sheetName)
		if notice != "" {
			out.predicateNotice = notice
			return out
		}
		out.exprs = []*conditionalExpr{expr}
		out.exprNodes = nodes
	case "containsBlanks", "notContainsBlanks", "containsErrors", "notContainsErrors", "containsText", "notContainsText", "beginsWith", "endsWith":
		if len(source.Formulas) > 1 || strings.ContainsAny(source.Text, "*?~") {
			out.predicateNotice = "文本条件的通配符或表达式尚未支持"
		}
	default:
		out.predicateNotice = "该条件格式需要范围统计或尚未支持的规则，不自动扫描整表"
	}
	return out
}

// Standard text/blank/error rules also carry a formula. Validate that formula
// later against the group's anchor; a contradictory custom formula is never
// silently replaced with a convenient interpretation of the type attribute.
func canonicalConditionalFormula(source SourceConditionalRule, row, col int) bool {
	switch source.Type {
	case "containsBlanks", "notContainsBlanks", "containsErrors", "notContainsErrors", "containsText", "notContainsText", "beginsWith", "endsWith":
	default:
		return true
	}
	if len(source.Formulas) == 0 {
		return true
	}
	if len(source.Formulas) != 1 || strings.ContainsAny(source.Text, "*?~") {
		return false
	}
	ref := ColumnName(col) + strconv.Itoa(row)
	text := `"` + strings.ReplaceAll(source.Text, `"`, `""`) + `"`
	runeLen := strconv.Itoa(utf8.RuneCountInString(source.Text))
	utf16Len := strconv.Itoa(len(utf16.Encode([]rune(source.Text))))
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
		expected = []string{"ISERROR(SEARCH(" + text + "," + ref + "))", "NOT(ISNUMBER(SEARCH(" + text + "," + ref + ")))"}
	case "beginsWith":
		expected = []string{"LEFT(" + ref + ",LEN(" + text + "))=" + text, "LEFT(" + ref + "," + runeLen + ")=" + text, "LEFT(" + ref + "," + utf16Len + ")=" + text}
	case "endsWith":
		expected = []string{"RIGHT(" + ref + ",LEN(" + text + "))=" + text, "RIGHT(" + ref + "," + runeLen + ")=" + text, "RIGHT(" + ref + "," + utf16Len + ")=" + text}
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
	if w.Format == "xlsb" {
		return w.loadXLSBConditional(ctx, meta)
	}
	layout, err := w.SourceLayoutContext(ctx, meta, idx)
	if err != nil {
		return nil, err
	}
	index := &SourceConditionalFormats{root: -1, notice: layout.ConditionalNotice, styles: w.differentialStyles, sheetName: meta.Name, date1904: w.Date1904}
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
				index.rules = append(index.rules, w.compileSourceConditionalRule(rule, groupID, meta.Name))
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
	return finishConditionalFormats(index, entries), nil
}

func finishConditionalFormats(index *SourceConditionalFormats, entries []conditionalRangeEntry) *SourceConditionalFormats {
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
			rule.predicateNotice = group.notice
		}
		if rule.predicateNotice == "" && !canonicalConditionalFormula(rule.source, group.anchorRow, group.anchorCol) {
			rule.predicateNotice = "条件格式公式与声明类型不一致或尚未支持"
		}
		if rule.source.DxfID != nil {
			id := *rule.source.DxfID
			if id < 0 || id >= len(index.styles) {
				rule.styleNotice = "条件格式引用的差异样式不可用"
			} else if index.styles[id].Notice != "" {
				rule.styleNotice = index.styles[id].Notice
			}
		}
		group.ruleIndices = append(group.ruleIndices, i)
	}
	for i := range index.groups {
		index.groups[i].areas = groupAreas(index.groups[i].ranges)
	}
	index.nodes, index.root = buildSourceRangeTree(entries)
	index.statistics = index.statisticPlans()
	index.bytes = conditionalIndexBytes(index)
	return index
}

func conditionalIndexBytes(index *SourceConditionalFormats) int64 {
	n := int64(256 + cap(index.nodes)*96 + cap(index.groups)*96 + cap(index.rules)*384 + len(index.notice)*2)
	for _, g := range index.groups {
		n += int64(cap(g.ranges)*32 + cap(g.ruleIndices)*8 + len(g.notice)*2)
	}
	for _, r := range index.rules {
		s := r.source
		n += int64(cap(r.exprs)*16 + r.exprNodes*128 + cap(s.Formulas)*16)
		n += int64(2 * (len(s.Type) + len(s.Text) + len(s.Operator) + len(s.Notice) + len(r.predicateNotice) + len(r.styleNotice)))
		for _, f := range s.Formulas {
			n += int64(len(f)*4 + 64)
		}
		for k, v := range s.Parameters {
			n += int64(2*(len(k)+len(v)) + 96)
		}
	}
	return n
}
