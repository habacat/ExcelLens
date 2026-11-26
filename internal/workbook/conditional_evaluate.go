package workbook

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"excellens/internal/conditional"
	"excellens/internal/excelformula"
	"excellens/internal/formats"
	"excellens/internal/styles"
)

const conditionalWorkNotice = conditional.WorkNotice

type ConditionalFormatResult struct {
	Style      DifferentialStyle `json:"style"`
	Matched    int               `json:"matched,omitempty"`
	Priorities []int             `json:"priorities,omitempty"`
	Notice     string            `json:"notice,omitempty"`
	Pending    bool              `json:"pending,omitempty"`
}

// available=false means not read, rather than a fabricated blank or zero.
// A caller with evidence of an actual blank returns an empty CellData and true.
type ConditionalCellResolver func(row, col int) (cell CellData, available bool, err error)

type ConditionalEvaluator struct {
	engine          *conditional.Engine
	index           *SourceConditionalFormats
	ctx             context.Context
	groups, rules   []int
	marked          []uint64
	resolver        ConditionalCellResolver
	operandResolver conditional.CellResolver
}

func (e *ConditionalEvaluator) SetRangeResolver(r conditional.RangeResolver) {
	e.engine.RangeResolver = r
}

func (index *SourceConditionalFormats) NewEvaluator(ctx context.Context) *ConditionalEvaluator {
	count := 0
	date1904 := false
	if index != nil {
		count = len(index.groups)
		date1904 = index.date1904
	}
	e := &ConditionalEvaluator{
		engine: conditional.NewEngine(ctx, date1904),
		index:  index,
		ctx:    ctx,
		marked: make([]uint64, (count+63)/64),
	}
	e.operandResolver = func(row, col int) (conditional.CellOperand, bool, error) {
		cell, available, err := e.resolver(row, col)
		return toConditionalOperand(cell), available, err
	}
	return e
}

func toConditionalOperand(cell CellData) conditional.CellOperand {
	return conditional.CellOperand{Raw: cell.Raw, Type: cell.Type, CacheMissing: cell.CacheMissing}
}

func groupAreas(ranges []CellMerge) []conditional.Area {
	if len(ranges) == 0 {
		return nil
	}
	areas := make([]conditional.Area, len(ranges))
	for i, r := range ranges {
		areas[i] = conditional.Area{Row: r.Row, Col: r.Col, EndRow: r.EndRow, EndCol: r.EndCol}
	}
	return areas
}

func (e *ConditionalEvaluator) spend(n int) bool {
	return e.engine.Spend(n)
}

func (e *ConditionalEvaluator) query(at int, row, col int) bool {
	if at < 0 {
		return true
	}
	n := &e.index.nodes[at]
	if n.maxRow < row {
		return true
	}
	if n.onlyGroup >= 0 && e.marked[n.onlyGroup/64]&(uint64(1)<<uint(n.onlyGroup%64)) != 0 {
		return true
	}
	if !e.spend(1) {
		return false
	}
	if n.entry.area.Row > row {
		return e.query(n.left, row, col)
	}
	if !e.query(n.left, row, col) {
		return false
	}
	a := n.entry.area
	if a.EndRow >= row && a.Col <= col && a.EndCol >= col {
		group := n.entry.group
		bit := uint64(1) << uint(group%64)
		if e.marked[group/64]&bit == 0 {
			e.marked[group/64] |= bit
			e.groups = append(e.groups, group)
		}
	}
	return e.query(n.right, row, col)
}

func (e *ConditionalEvaluator) Evaluate(row, col int, cell CellData, resolver ConditionalCellResolver) (*ConditionalFormatResult, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	index := e.index
	if index == nil {
		return nil, nil
	}
	if index.notice != "" {
		return &ConditionalFormatResult{Notice: index.notice}, nil
	}
	for _, id := range e.groups {
		e.marked[id/64] &= ^(uint64(1) << uint(id%64))
	}
	e.groups = e.groups[:0]
	e.rules = e.rules[:0]
	if !e.query(index.root, row, col) {
		return &ConditionalFormatResult{Notice: "条件格式超过本次求值预算，原值保留"}, nil
	}
	for _, id := range e.groups {
		if group := &index.groups[id]; len(group.ruleIndices) == 0 && group.notice != "" {
			// An unrecognised block has no reliable priority. It cannot be
			// ignored in favour of another block's apparently valid colors.
			return &ConditionalFormatResult{Notice: group.notice}, nil
		}
		e.rules = append(e.rules, index.groups[id].ruleIndices...)
	}
	if len(e.rules) == 0 {
		return nil, nil
	}
	if !e.spend(len(e.rules) * max(1, conditionalSortCost(len(e.rules)))) {
		return &ConditionalFormatResult{Notice: "条件格式超过本次求值预算，原值保留"}, nil
	}
	sort.Ints(e.rules)
	e.resolver = resolver
	var operand conditional.CellResolver
	if resolver != nil {
		operand = e.operandResolver
	}
	e.engine.ResetCell(row, col, toConditionalOperand(cell), operand)
	out := &ConditionalFormatResult{}
	for _, id := range e.rules {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		rule := &index.rules[id]
		if !e.spend(1) {
			out.Notice = "条件格式超过本次求值预算，低优先级样式未应用"
			break
		}
		e.engine.Pending = false
		matched, notice, err := e.match(rule)
		if err != nil {
			return nil, err
		}
		if notice != "" {
			out.Notice = fmt.Sprintf("条件规则%d：%s；低优先级样式未应用", rule.source.Priority, notice)
			if e.engine.Pending {
				out.Pending = true
			}
			break
		}
		if !matched {
			continue
		}
		if rule.styleNotice != "" {
			out.Notice = fmt.Sprintf("条件规则%d：%s；低优先级样式未应用", rule.source.Priority, rule.styleNotice)
			break
		}
		out.Matched++
		if len(out.Priorities) < formats.ConditionalExplainRules {
			out.Priorities = append(out.Priorities, rule.source.Priority)
		}
		if rule.source.DxfID != nil {
			styles.MergeDifferentialStyle(&out.Style, index.styles[*rule.source.DxfID])
		}
		if rule.source.StopIfTrue {
			break
		}
	}
	if out.Matched == 0 && out.Notice == "" {
		return nil, nil
	}
	return out, nil
}

func conditionalSortCost(n int) int {
	cost := 0
	for n > 1 {
		cost++
		n >>= 1
	}
	return cost
}

func (e *ConditionalEvaluator) match(rule *compiledConditionalRule) (bool, string, error) {
	s := rule.source
	group := &e.index.groups[rule.group]
	// Unsupported predicates or malformed blocks may match; they cannot be
	// treated as false allowing lower-priority colors to masquerade as source.
	if rule.predicateNotice != "" {
		return false, rule.predicateNotice, nil
	}
	switch s.Type {
	case "cellIs":
		need := 1
		if s.Operator == "between" || s.Operator == "notBetween" {
			need = 2
		}
		if len(rule.exprs) < need {
			return false, "条件比较的表达式数量无效", nil
		}
		left, notice := e.engine.CurrentValue()
		if notice != "" {
			return false, notice, nil
		}
		right, notice, err := e.engine.Eval(rule.exprs[0], group.anchorRow, group.anchorCol, group.areas)
		if notice != "" || err != nil {
			return false, notice, err
		}
		cmp, valid := conditional.CompareValues(left, right)
		if !valid {
			return false, "", nil
		}
		if s.Operator != "between" && s.Operator != "notBetween" {
			return excelformula.MatchesComparison(cmp, s.Operator), "", nil
		}
		other, notice, err := e.engine.Eval(rule.exprs[1], group.anchorRow, group.anchorCol, group.areas)
		if notice != "" || err != nil {
			return false, notice, err
		}
		upper, valid := conditional.CompareValues(left, other)
		if !valid {
			return false, "", nil
		}
		// Excel allows reversed bounds in between/notBetween (e.g. 10 and 5).
		if boundCmp, ok := conditional.CompareValues(right, other); ok && boundCmp > 0 {
			cmp, upper = upper, cmp
		}
		match := cmp >= 0 && upper <= 0
		if s.Operator == "notBetween" {
			match = !match
		}
		return match, "", nil
	case "expression":
		if len(rule.exprs) < 1 {
			return false, "条件格式公式数量无效", nil
		}
		val, notice, err := e.engine.Eval(rule.exprs[0], group.anchorRow, group.anchorCol, group.areas)
		if notice != "" || err != nil {
			return false, notice, err
		}
		switch val.Kind {
		case "number":
			return conditional.IsNonZero(val), "", nil
		case "boolean":
			return val.Boolean, "", nil
		case "blank", "error":
			return false, "", nil
		}
		return false, "文本条件公式结果不能默认为布尔值", nil
	case "containsErrors", "notContainsErrors":
		if e.engine.Cell.CacheMissing {
			return false, "所引用公式没有保存计算结果", nil
		}
		match := e.engine.Cell.Type == "e"
		if s.Type == "notContainsErrors" {
			match = !match
		}
		return match, "", nil
	case "containsBlanks", "notContainsBlanks":
		value, notice := e.engine.CurrentValue()
		if notice != "" {
			return false, notice, nil
		}
		if value.Kind == "error" {
			return false, "", nil
		}
		match := value.Kind == "blank" || (value.Kind == "text" && strings.Trim(value.Text, " ") == "")
		if s.Type == "notContainsBlanks" {
			match = !match
		}
		return match, "", nil
	case "containsText", "notContainsText", "beginsWith", "endsWith":
		value, notice := e.engine.CurrentValue()
		if notice != "" {
			return false, notice, nil
		}
		if value.Kind == "error" {
			return s.Type == "notContainsText", "", nil
		}
		if value.Kind == "blank" {
			value = conditional.TextValue("")
		}
		if value.Kind != "text" {
			return false, "文本条件需要尚未支持的数值或布尔转文本语义", nil
		}
		needle := excelformula.FoldString(s.Text)
		matched := false
		switch s.Type {
		case "containsText":
			matched = strings.Contains(value.Folded, needle)
		case "notContainsText":
			matched = !strings.Contains(value.Folded, needle)
		case "beginsWith":
			matched = strings.HasPrefix(value.Folded, needle)
		case "endsWith":
			matched = strings.HasSuffix(value.Folded, needle)
		}
		return matched, "", nil
	}
	return false, "该条件格式需要范围统计或尚未支持的规则，不自动扫描整表", nil
}
