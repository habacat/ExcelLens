package workbook

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"excellens/internal/formats"
)

type ConditionalFormatResult struct {
	Style      DifferentialStyle `json:"style"`
	Matched    int               `json:"matched,omitempty"`
	Priorities []int             `json:"priorities,omitempty"`
	Notice     string            `json:"notice,omitempty"`
}

// available=false means not read, rather than a fabricated blank or zero.
// A caller with evidence of an actual blank returns an empty CellData and true.
type ConditionalCellResolver func(row, col int) (cell CellData, available bool, err error)

type ConditionalEvaluator struct {
	index           *SourceConditionalFormats
	ctx             context.Context
	work, textBytes int
	groups, rules   []int
	marked          []uint64
	row, col        int
	cell            CellData
	current         conditionalValue
	currentReady    bool
	currentNotice   string
	resolver        ConditionalCellResolver
}

func (index *SourceConditionalFormats) NewEvaluator(ctx context.Context) *ConditionalEvaluator {
	count := 0
	if index != nil {
		count = len(index.groups)
	}
	return &ConditionalEvaluator{index: index, ctx: ctx, work: formats.ConditionalEvalWork, textBytes: formats.ConditionalEvalTextBytes, marked: make([]uint64, (count+63)/64)}
}

func (e *ConditionalEvaluator) spend(n int) bool {
	if n < 0 || n > e.work {
		e.work = 0
		return false
	}
	e.work -= n
	return true
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
	e.row, e.col, e.cell, e.resolver = row, col, cell, resolver
	e.currentReady = false
	e.currentNotice = ""
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
		matched, notice, err := e.match(rule)
		if err != nil {
			return nil, err
		}
		if notice != "" {
			out.Notice = fmt.Sprintf("条件规则%d：%s；低优先级样式未应用", rule.source.Priority, notice)
			break
		}
		if !matched {
			continue
		}
		if rule.notice != "" {
			out.Notice = fmt.Sprintf("条件规则%d：%s；低优先级样式未应用", rule.source.Priority, rule.notice)
			break
		}
		out.Matched++
		if len(out.Priorities) < formats.ConditionalExplainRules {
			out.Priorities = append(out.Priorities, rule.source.Priority)
		}
		if rule.source.DxfID != nil {
			mergeDifferentialStyle(&out.Style, index.styles[*rule.source.DxfID])
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

func (e *ConditionalEvaluator) value(cell CellData) (conditionalValue, string) {
	if cell.CacheMissing {
		return conditionalValue{}, "所引用公式没有保存计算结果"
	}
	if len(cell.Raw) > formats.ConditionalOperandBytes || len(cell.Raw) > e.textBytes {
		return conditionalValue{}, "条件操作数超过本次文本容量"
	}
	e.textBytes -= len(cell.Raw)
	switch cell.Type {
	case "e":
		return conditionalValue{kind: "error", text: cell.Raw}, ""
	case "s", "str", "inlineStr", "text", "string":
		return conditionalText(cell.Raw), ""
	case "b":
		switch strings.ToUpper(cell.Raw) {
		case "TRUE", "1":
			return conditionalValue{kind: "boolean", boolean: true}, ""
		case "FALSE", "0":
			return conditionalValue{kind: "boolean"}, ""
		}
		return conditionalValue{}, "布尔源值无效"
	case "", "n":
		if cell.Raw == "" {
			return conditionalValue{kind: "blank"}, ""
		}
		number, ok := parseConditionalDecimal(cell.Raw)
		if !ok {
			return conditionalValue{}, "数值源值无效，未按文本或零替代"
		}
		return conditionalValue{kind: "number", number: number}, ""
	default:
		return conditionalValue{}, "条件规则遇到尚未支持的源类型"
	}
}

func (e *ConditionalEvaluator) currentValue() (conditionalValue, string) {
	if !e.currentReady {
		e.current, e.currentNotice = e.value(e.cell)
		e.currentReady = true
	}
	return e.current, e.currentNotice
}

func (e *ConditionalEvaluator) operand(value conditionalOperand, group *conditionalGroup) (conditionalValue, string, error) {
	if value.reference == nil {
		return value.value, "", nil
	}
	ref := value.reference
	row, col := ref.row, ref.col
	if !ref.absoluteRow {
		row += e.row - group.anchorRow
	}
	if !ref.absoluteCol {
		col += e.col - group.anchorCol
	}
	if row < 1 || row > formats.ExcelRows || col < 1 || col > formats.ExcelColumns {
		return conditionalValue{kind: "error", text: "#REF!"}, "", nil
	}
	if row == e.row && col == e.col {
		v, notice := e.currentValue()
		return v, notice, nil
	}
	if e.resolver == nil {
		return conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	cell, available, err := e.resolver(row, col)
	if err != nil {
		var unavailable *conditionalDependencyUnavailable
		if errors.As(err, &unavailable) {
			return conditionalValue{}, unavailable.reason, nil
		}
		return conditionalValue{}, "", err
	}
	if !available {
		return conditionalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	v, notice := e.value(cell)
	return v, notice, nil
}

func (e *ConditionalEvaluator) match(rule *compiledConditionalRule) (bool, string, error) {
	s := rule.source
	group := &e.index.groups[rule.group]
	// Unsupported predicates may match; they cannot be treated as a false
	// result allowing lower-priority colors to masquerade as the source.
	if len(rule.operands) == 0 && s.Type == "cellIs" || rule.notice != "" && s.Type != "cellIs" && s.Type != "expression" {
		return false, rule.notice, nil
	}
	switch s.Type {
	case "cellIs":
		left, notice := e.currentValue()
		if notice != "" {
			return false, notice, nil
		}
		right, notice, err := e.operand(rule.operands[0], group)
		if notice != "" || err != nil {
			return false, notice, err
		}
		cmp, valid := compareConditionalValues(left, right)
		if !valid {
			return false, "", nil
		}
		if s.Operator != "between" && s.Operator != "notBetween" {
			return matchConditionalComparison(cmp, s.Operator), "", nil
		}
		other, notice, err := e.operand(rule.operands[1], group)
		if notice != "" || err != nil {
			return false, notice, err
		}
		upper, valid := compareConditionalValues(left, other)
		if !valid {
			return false, "", nil
		}
		match := cmp >= 0 && upper <= 0
		if s.Operator == "notBetween" {
			match = !match
		}
		return match, "", nil
	case "expression":
		if rule.notice != "" && rule.predicate.left.value.kind == "" && rule.predicate.left.reference == nil {
			return false, rule.notice, nil
		}
		predicate := rule.predicate
		left, notice, err := e.operand(predicate.left, group)
		if notice != "" || err != nil {
			return false, notice, err
		}
		if !predicate.comparison {
			switch left.kind {
			case "number":
				return left.number.digits != "0", "", nil
			case "boolean":
				return left.boolean, "", nil
			case "blank", "error":
				return false, "", nil
			}
			return false, "文本条件公式结果不能默认为布尔值", nil
		}
		right, notice, err := e.operand(predicate.right, group)
		if notice != "" || err != nil {
			return false, notice, err
		}
		cmp, valid := compareConditionalValues(left, right)
		return valid && matchConditionalComparison(cmp, predicate.operator), "", nil
	case "containsErrors", "notContainsErrors":
		if e.cell.CacheMissing {
			return false, "所引用公式没有保存计算结果", nil
		}
		match := e.cell.Type == "e"
		if s.Type == "notContainsErrors" {
			match = !match
		}
		return match, "", nil
	case "containsBlanks", "notContainsBlanks":
		value, notice := e.currentValue()
		if notice != "" {
			return false, notice, nil
		}
		if value.kind == "error" {
			return false, "", nil
		}
		match := value.kind == "blank" || value.kind == "text" && strings.Trim(value.text, " ") == ""
		if s.Type == "notContainsBlanks" {
			match = !match
		}
		return match, "", nil
	case "containsText", "notContainsText", "beginsWith", "endsWith":
		value, notice := e.currentValue()
		if notice != "" {
			return false, notice, nil
		}
		if value.kind == "error" {
			return s.Type == "notContainsText", "", nil
		}
		if value.kind == "blank" {
			value = conditionalText("")
		}
		if value.kind != "text" {
			return false, "文本条件需要尚未支持的数值或布尔转文本语义", nil
		}
		needle := filterFoldKey(s.Text)
		matched := false
		switch s.Type {
		case "containsText":
			matched = strings.Contains(value.folded, needle)
		case "notContainsText":
			matched = !strings.Contains(value.folded, needle)
		case "beginsWith":
			matched = strings.HasPrefix(value.folded, needle)
		case "endsWith":
			matched = strings.HasSuffix(value.folded, needle)
		}
		return matched, "", nil
	}
	return false, rule.notice, nil
}

// First (higher-priority) property wins. Copy containers before adding lower
// properties so evaluated cells never mutate the immutable workbook palette.
func mergeDifferentialStyle(dst *DifferentialStyle, src DifferentialStyle) {
	a, b := &dst.Font, src.Font
	if a.Name == "" {
		a.Name = b.Name
	}
	if a.Size == 0 {
		a.Size = b.Size
	}
	if a.Color == "" && !a.AutoColor {
		a.Color, a.AutoColor = b.Color, b.AutoColor
	}
	if a.Bold == nil {
		a.Bold = copyConditionalProperty(b.Bold)
	}
	if a.Italic == nil {
		a.Italic = copyConditionalProperty(b.Italic)
	}
	if a.Strike == nil {
		a.Strike = copyConditionalProperty(b.Strike)
	}
	if a.Underline == "" {
		a.Underline = b.Underline
	}
	if a.Vertical == "" {
		a.Vertical = b.Vertical
	}
	if dst.Background == nil {
		dst.Background = copyConditionalProperty(src.Background)
	}
	if dst.NumberFormat == nil {
		dst.NumberFormat = copyConditionalProperty(src.NumberFormat)
	}
	if dst.Horizontal == nil {
		dst.Horizontal = copyConditionalProperty(src.Horizontal)
	}
	if dst.Vertical == nil {
		dst.Vertical = copyConditionalProperty(src.Vertical)
	}
	if dst.Wrap == nil {
		dst.Wrap = copyConditionalProperty(src.Wrap)
	}
	if dst.Rotation == nil {
		dst.Rotation = copyConditionalProperty(src.Rotation)
	}
	if src.Border != nil {
		if dst.Border == nil {
			dst.Border = &DifferentialBorders{}
		}
		for _, side := range []struct {
			target **DifferentialBorderSide
			source *DifferentialBorderSide
		}{{&dst.Border.Top, src.Border.Top}, {&dst.Border.Bottom, src.Border.Bottom}, {&dst.Border.Left, src.Border.Left}, {&dst.Border.Right, src.Border.Right}} {
			if side.source == nil {
				continue
			}
			if *side.target == nil {
				*side.target = &DifferentialBorderSide{}
			}
			a, b := *side.target, side.source
			if a.Style == nil {
				a.Style = copyConditionalProperty(b.Style)
			}
			if a.Color == nil && !a.AutoColor {
				a.Color, a.AutoColor = copyConditionalProperty(b.Color), b.AutoColor
			}
		}
	}
}

func copyConditionalProperty[T any](source *T) *T {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
