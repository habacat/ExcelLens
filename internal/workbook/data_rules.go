package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// SourceDataRule describes saved criteria; it does not assert that any cell is
// valid and is never an executable expression. Missing/unreadable BIFF formulas
// keep tokens, rather than a fabricated formula string.
type SourceDataRule struct {
	ID               int               `json:"id"`
	Namespace        string            `json:"namespace,omitempty"`
	SqRef            string            `json:"sqref"`
	Ranges           []CellMerge       `json:"ranges"`
	Type             string            `json:"type"`
	Operator         string            `json:"operator"`
	ErrorStyle       string            `json:"errorStyle"`
	IMEMode          string            `json:"imeMode"`
	AllowBlank       bool              `json:"allowBlank"`
	SuppressDropDown bool              `json:"suppressDropDown"`
	ShowInputMessage bool              `json:"showInputMessage"`
	ShowErrorMessage bool              `json:"showErrorMessage"`
	PromptTitle      string            `json:"promptTitle,omitempty"`
	Prompt           string            `json:"prompt,omitempty"`
	ErrorTitle       string            `json:"errorTitle,omitempty"`
	Error            string            `json:"error,omitempty"`
	Formula1         string            `json:"formula1,omitempty"`
	Formula2         string            `json:"formula2,omitempty"`
	Formula1Tokens   string            `json:"formula1Tokens,omitempty"`
	Formula2Tokens   string            `json:"formula2Tokens,omitempty"`
	ListDefinition   string            `json:"listDefinition,omitempty"`
	RawXML           string            `json:"rawXml,omitempty"`
	RecordHex        string            `json:"recordHex,omitempty"`
	ContinuationHex  []string          `json:"continuationHex,omitempty"`
	InlineList       []string          `json:"inlineList,omitempty"`
	ListSource       string            `json:"listSource,omitempty"`
	Status           string            `json:"status"`
	Reason           string            `json:"reason,omitempty"`
	Attributes       map[string]string `json:"attributes,omitempty"`
}

// RequiresListLookup distinguishes a well-formed list expression from a rule
// that the source reader marked ambiguous for another reason.
func (r SourceDataRule) RequiresListLookup() bool {
	return r.Type == "list" && r.ListSource == "expression" && r.Status == "unresolved" && r.Reason == unresolvedListReference
}

type DataRuleSummary struct {
	Rules               int                 `json:"rules"`
	Ranges              int                 `json:"ranges"`
	Unresolved          int                 `json:"unresolved"`
	UnknownScopes       int                 `json:"unknownScopes"`
	Complete            bool                `json:"complete"`
	DisablePrompts      bool                `json:"disablePrompts"`
	InputWindowClosed   bool                `json:"inputWindowClosed,omitempty"`
	Notices             []string            `json:"notices,omitempty"`
	Containers          []DataRuleContainer `json:"containers,omitempty"`
	PromptSettingsKnown bool                `json:"promptSettingsKnown"`
}

type DataRuleContainer struct {
	Namespace      string            `json:"namespace,omitempty"`
	DisablePrompts bool              `json:"disablePrompts"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

type DataRulePage struct {
	Items []SourceDataRule `json:"items"`
	Total int              `json:"total"`
	// False means an unread definition or invalid source scope prevents a
	// caller from treating an empty match list as proof of no validation.
	ScopeComplete bool `json:"scopeComplete"`
}

// SourceDataRuleIndex owns immutable metadata and a compressed source-range
// tree. Exported pages are deep copies; one million covered rows cost one area.
type SourceDataRuleIndex struct {
	rules          []SourceDataRule
	nodes          []sourceRangeNode
	root           int
	maxRow, maxCol int
	summary        DataRuleSummary
	bytes          int64
}

func (index *SourceDataRuleIndex) Summary() DataRuleSummary {
	value := index.summary
	value.Notices = slices.Clone(value.Notices)
	value.Containers = slices.Clone(value.Containers)
	for at := range value.Containers {
		value.Containers[at].Attributes = cloneDataRuleAttributes(value.Containers[at].Attributes)
	}
	return value
}

func cloneDataRule(value SourceDataRule) SourceDataRule {
	value.Ranges = slices.Clone(value.Ranges)
	value.InlineList = slices.Clone(value.InlineList)
	value.ContinuationHex = slices.Clone(value.ContinuationHex)
	value.Attributes = cloneDataRuleAttributes(value.Attributes)
	return value
}

func cloneDataRuleAttributes(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// Page preserves source declaration order and bounds the response separately
// from the retained index. A caller cannot request the whole metadata at once.
func (index *SourceDataRuleIndex) Page(offset, count int) (DataRulePage, error) {
	return index.page(nil, offset, count)
}

func (index *SourceDataRuleIndex) page(ids []int, offset, count int) (DataRulePage, error) {
	total := len(index.rules)
	if ids != nil {
		total = len(ids)
	}
	if offset < 0 || offset > total || count < 1 || count > formats.DataRulePageRows {
		return DataRulePage{}, errors.New("数据规则分页范围无效")
	}
	out := DataRulePage{Total: total, ScopeComplete: index.summary.Complete && index.summary.UnknownScopes == 0, Items: make([]SourceDataRule, 0, min(count, total-offset))}
	for at := offset; at < total && at-offset < count; at++ {
		id := at
		if ids != nil {
			id = ids[at]
		}
		out.Items = append(out.Items, cloneDataRule(index.rules[id]))
	}
	return out, nil
}

// At finds applicable source rules, not cell validity. Overlapping ranges in
// one rule occur once, while overlapping separate rules remain separate.
func (index *SourceDataRuleIndex) At(ctx context.Context, row, col, offset, count int) (DataRulePage, error) {
	if row < 1 || row > index.maxRow || col < 1 || col > index.maxCol {
		return DataRulePage{}, errors.New("数据规则查询超出源格式坐标范围")
	}
	ids, err := index.idsAt(ctx, row, col)
	if err != nil {
		return DataRulePage{}, err
	}
	return index.page(ids, offset, count)
}

func (index *SourceDataRuleIndex) idsAt(ctx context.Context, row, col int) ([]int, error) {
	seen := make([]bool, len(index.rules))
	var visit func(int) error
	visit = func(at int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if at < 0 || index.nodes[at].maxRow < row {
			return nil
		}
		node := index.nodes[at]
		if node.onlyGroup >= 0 && seen[node.onlyGroup] {
			return nil
		}
		if err := visit(node.left); err != nil {
			return err
		}
		area := node.entry.area
		if area.Row <= row && row <= area.EndRow && area.Col <= col && col <= area.EndCol {
			seen[node.entry.group] = true
		}
		if area.Row <= row {
			return visit(node.right)
		}
		return nil
	}
	if err := visit(index.root); err != nil {
		return nil, err
	}
	ids := make([]int, 0)
	for id, found := range seen {
		if found {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type dataRuleBuilder struct {
	index        *SourceDataRuleIndex
	encodedBytes int
}

func newDataRuleBuilder(source formats.Source) *dataRuleBuilder {
	return &dataRuleBuilder{index: &SourceDataRuleIndex{root: -1, maxRow: source.MaxRows, maxCol: source.MaxColumns, summary: DataRuleSummary{Complete: true, PromptSettingsKnown: true}}}
}

func (b *dataRuleBuilder) addContainer(value DataRuleContainer) error {
	data, err := json.Marshal(value)
	if err != nil || len(b.index.summary.Containers) >= formats.DataRuleContainers || len(data) > formats.DataRuleMetadataBytes-b.encodedBytes {
		return errors.New("数据规则来源属性超过元数据读取容量")
	}
	b.encodedBytes += len(data)
	if len(b.index.summary.Containers) == 0 {
		b.index.summary.DisablePrompts = value.DisablePrompts
	} else if b.index.summary.DisablePrompts != value.DisablePrompts {
		b.index.summary.PromptSettingsKnown = false
	}
	b.index.summary.Containers = append(b.index.summary.Containers, value)
	return nil
}

func (b *dataRuleBuilder) notice(text string) {
	if !slices.Contains(b.index.summary.Notices, text) && len(b.index.summary.Notices) < formats.DataRuleNoticeCount {
		b.index.summary.Notices = append(b.index.summary.Notices, text)
	}
	b.index.summary.Complete = false
}

func dataRuleIssue(rule *SourceDataRule, status, text string) {
	if rule.Status == "invalid" || rule.Status == "unresolved" && status != "invalid" {
		return
	}
	rule.Status, rule.Reason = status, text
}

func (b *dataRuleBuilder) add(rule SourceDataRule) error {
	if len(b.index.rules) >= formats.DataRuleCount || len(rule.Ranges) > formats.DataRuleRanges-b.index.summary.Ranges {
		return errors.New("数据规则或源区域数量超过读取容量，未发布部分规则")
	}
	rule.ID = len(b.index.rules) + 1
	data, err := json.Marshal(rule)
	if err != nil || len(data) > formats.DataRuleMetadataBytes-b.encodedBytes {
		return errors.New("数据规则元数据超过读取容量，未发布部分规则")
	}
	b.encodedBytes += len(data)
	b.index.summary.Ranges += len(rule.Ranges)
	if len(rule.Ranges) == 0 {
		b.index.summary.UnknownScopes++
	}
	if rule.Status != "described" {
		b.index.summary.Unresolved++
	}
	b.index.rules = append(b.index.rules, rule)
	return nil
}

func (b *dataRuleBuilder) finish() *SourceDataRuleIndex {
	entries := make([]sourceRangeEntry, 0, b.index.summary.Ranges)
	for id, rule := range b.index.rules {
		for _, area := range rule.Ranges {
			entries = append(entries, sourceRangeEntry{area: area, group: id})
		}
	}
	b.index.nodes, b.index.root = buildSourceRangeTree(entries)
	b.index.summary.Rules = len(b.index.rules)
	// Encoded strings can share backing storage. Account conservatively for
	// both decoded and escaped representations, plus maps and tree nodes.
	b.index.bytes = int64(512 + b.encodedBytes*3 + cap(b.index.rules)*512 + cap(b.index.nodes)*96)
	for _, container := range b.index.summary.Containers {
		b.index.bytes += int64(256 + len(container.Attributes)*128)
	}
	return b.index
}

var dataRuleTypes = []string{"none", "whole", "decimal", "list", "date", "time", "textLength", "custom"}
var dataRuleOperators = []string{"between", "notBetween", "equal", "notEqual", "greaterThan", "lessThan", "greaterThanOrEqual", "lessThanOrEqual"}
var dataRuleErrorStyles = []string{"stop", "warning", "information"}
var dataRuleIMEModes = []string{"noControl", "on", "off", "disabled", "hiragana", "fullKatakana", "halfKatakana", "fullAlpha", "halfAlpha", "fullHangul", "halfHangul"}

func validateDataRule(rule *SourceDataRule) {
	if !slices.Contains(dataRuleTypes, rule.Type) || !slices.Contains(dataRuleErrorStyles, rule.ErrorStyle) || !slices.Contains(dataRuleIMEModes, rule.IMEMode) {
		dataRuleIssue(rule, "invalid", "数据规则类型、错误级别或输入模式无效，源定义已保留")
	}
	if rule.Type != "none" && rule.Type != "list" && rule.Type != "custom" && !slices.Contains(dataRuleOperators, rule.Operator) {
		dataRuleIssue(rule, "invalid", "数据规则条件运算符无效，源定义已保留")
	}
	if len(rule.Ranges) == 0 {
		dataRuleIssue(rule, "invalid", "数据规则缺少可靠的适用源区域")
	}
	if rule.Type != "none" && strings.TrimSpace(rule.Formula1) == "" && rule.Formula1Tokens == "" && ((rule.Type != "list" && rule.Type != "custom") || rule.ListDefinition == "") {
		dataRuleIssue(rule, "invalid", "数据规则缺少第一个条件定义")
	}
	if rule.Type != "none" && rule.Type != "list" && rule.Type != "custom" && (rule.Operator == "between" || rule.Operator == "notBetween") && strings.TrimSpace(rule.Formula2) == "" && rule.Formula2Tokens == "" {
		dataRuleIssue(rule, "invalid", "区间数据规则缺少第二个条件定义")
	}
}

func dataRuleRanges(raw string, maxRows, maxCols int) ([]CellMerge, error) {
	if len(raw) > formats.DataRuleTextBytes {
		return nil, errors.New("数据规则适用区域文字超过读取容量")
	}
	var out []CellMerge
	for part := range strings.FieldsSeq(raw) {
		if len(out) >= formats.DataRuleRanges {
			return nil, errors.New("数据规则适用源区域数量超过读取容量")
		}
		first, last, ranged := strings.Cut(part, ":")
		col, row, err := dataRuleCell(first, maxRows, maxCols)
		if err != nil {
			return nil, err
		}
		endCol, endRow := col, row
		if ranged {
			endCol, endRow, err = dataRuleCell(last, maxRows, maxCols)
		}
		if err != nil || row > endRow || col > endCol {
			return nil, errors.New("数据规则适用源区域无效，未采用部分区域")
		}
		out = append(out, CellMerge{Row: row, Col: col, EndRow: endRow, EndCol: endCol})
	}
	if len(out) == 0 {
		return nil, errors.New("数据规则适用源区域为空")
	}
	return out, nil
}

func dataRuleCell(raw string, maxRows, maxCols int) (col, row int, err error) {
	// ParseCellRef is intentionally permissive for interactive input. A saved
	// sqref must instead be a single well-formed A1 address, without signs or
	// embedded dollar signs that would make source boundaries ambiguous.
	text := strings.TrimPrefix(raw, "$")
	at := 0
	for at < len(text) && (text[at] >= 'A' && text[at] <= 'Z' || text[at] >= 'a' && text[at] <= 'z') {
		at++
	}
	if at == 0 || at > 3 {
		return 0, 0, errors.New("数据规则源地址无效")
	}
	digits := strings.TrimPrefix(text[at:], "$")
	if digits == "" || digits[0] == '0' || len(digits) > 7 {
		return 0, 0, errors.New("数据规则源行号无效")
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return 0, 0, errors.New("数据规则源行号无效")
		}
	}
	col, row, err = ParseCellRef(raw)
	if err != nil || col < 1 || col > maxCols || row < 1 || row > maxRows {
		return 0, 0, errors.New("数据规则源地址超出格式容量")
	}
	return
}

func dataRuleInlineList(formula string, separator string) ([]string, bool) {
	text := strings.TrimSpace(formula)
	if strings.HasPrefix(text, "=") {
		text = strings.TrimSpace(text[1:])
	}
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return nil, false
	}
	var content strings.Builder
	for at := 1; at < len(text)-1; at++ {
		if text[at] == '"' {
			at++
			if at >= len(text)-1 || text[at] != '"' {
				return nil, false
			}
		}
		content.WriteByte(text[at])
	}
	if content.Len() > formats.DataRuleTextBytes {
		return nil, false
	}
	items := strings.SplitN(content.String(), separator, formats.DataRuleListItems+1)
	if len(items) > formats.DataRuleListItems {
		return nil, false
	}
	return items, true
}

func dataRuleScopeText(areas []CellMerge) string {
	var text strings.Builder
	for at, area := range areas {
		if at > 0 {
			text.WriteByte(' ')
		}
		text.WriteString(ColumnName(area.Col) + strconv.Itoa(area.Row))
		if area.Col != area.EndCol || area.Row != area.EndRow {
			text.WriteString(":" + ColumnName(area.EndCol) + strconv.Itoa(area.EndRow))
		}
	}
	return text.String()
}

func dataRuleAttributeKey(space, local string) string {
	if space == "" {
		return local
	}
	return fmt.Sprintf("{%s}%s", space, local)
}
