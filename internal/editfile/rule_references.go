package editfile

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var (
	absoluteListRef    = regexp.MustCompile(`^\$[A-Za-z]{1,3}\$[1-9][0-9]{0,6}(?::\$[A-Za-z]{1,3}\$[1-9][0-9]{0,6})?$`)
	errReferenceSyntax = errors.New("列表来源须为同表单行或单列的绝对地址，如 $D$2:$D$8")
	errReferenceTarget = errors.New("列表来源超出工作表的可编辑范围")
)

func listReference(rule editdoc.Rule) (editdoc.Area, error) {
	ref := strings.TrimPrefix(rule.First, "=")
	if !absoluteListRef.MatchString(ref) {
		return editdoc.Area{}, errReferenceSyntax
	}
	area, err := parseRuleArea(rule.Start.Sheet, strings.ToUpper(strings.ReplaceAll(ref, "$", "")))
	if err != nil || area.Start.Row != area.EndRow && area.Start.Col != area.EndCol {
		return editdoc.Area{}, errReferenceSyntax
	}
	if area.Intersects(rule.Area) {
		return editdoc.Area{}, errors.New("列表来源与规则应用范围重叠")
	}
	if area.EndRow-area.Start.Row+area.EndCol-area.Start.Col+1 > formats.EditReferenceListCells {
		return editdoc.Area{}, fmt.Errorf("列表来源最多 %d 格", formats.EditReferenceListCells)
	}
	return area, nil
}

func (l Layout) containsReference(a editdoc.Area) bool {
	return l.Contains(a.Start) && l.Contains(editdoc.Address{Sheet: a.Start.Sheet, Row: a.EndRow, Col: a.EndCol})
}

func referenceAddresses(area editdoc.Area) iter.Seq[editdoc.Address] {
	return func(yield func(editdoc.Address) bool) {
		for row := area.Start.Row; row <= area.EndRow; row++ {
			for col := area.Start.Col; col <= area.EndCol; col++ {
				if !yield(editdoc.Address{Sheet: area.Start.Sheet, Row: row, Col: col}) {
					return
				}
			}
		}
	}
}

func appendReferences(targets []editdoc.Address, rules []validatedRule) ([]editdoc.Address, error) {
	seen := make(map[editdoc.Address]bool)
	for _, rule := range rules {
		if !rule.Reference {
			continue
		}
		for address := range referenceAddresses(rule.reference) {
			seen[address] = true
			if len(seen) > formats.EditReferenceCells {
				return nil, fmt.Errorf("引用列表合计最多 %d 个来源格", formats.EditReferenceCells)
			}
		}
	}
	if len(seen) == 0 {
		return targets, nil
	}
	for _, addr := range targets {
		delete(seen, addr)
	}
	out := slices.Clone(targets)
	for addr := range seen {
		out = append(out, addr)
	}
	return out, nil
}

func (r validatedRule) referencedChoices(valueAt func(editdoc.Address) editdoc.Value) ([]string, error) {
	return r.listChoices(referenceAddresses(r.reference), valueAt)
}

func (r validatedRule) listChoices(addresses iter.Seq[editdoc.Address], valueAt func(editdoc.Address) editdoc.Value) ([]string, error) {
	var choices []string
	bytes := 0
	for address := range addresses {
		value := valueAt(address)
		if r.nativeList && (value.Kind == "blank" || value.Kind == "missing" || value.Kind == "text" && value.Raw == "") {
			continue
		}
		if value.Kind != "text" || strings.TrimSpace(value.Raw) == "" || strings.ContainsAny(value.Raw, "\t\r\n") || len(value.Raw) > formats.EditRuleTextBytes || validateXLSXText(value.Raw) != nil {
			return nil, fmt.Errorf("列表来源 %s%d 必须是非空的单行文本", workbook.ColumnName(address.Col), address.Row)
		}
		bytes += len(value.Raw)
		if bytes > formats.EditReferenceListBytes {
			return nil, fmt.Errorf("列表来源超出 %d KB 上限", formats.EditReferenceListBytes>>10)
		}
		if len(choices) >= formats.EditReferenceListCells {
			return nil, fmt.Errorf("下拉来源中有值的单元格超过 %d 个", formats.EditReferenceListCells)
		}
		choices = append(choices, value.Raw)
	}
	return choices, nil
}

func (b EditBatch) choices(rule validatedRule, doc *editdoc.Document, proposed map[editdoc.Address]editdoc.Value) ([]string, error) {
	valueAt := func(a editdoc.Address) editdoc.Value {
		if value, ok := proposed[a]; ok {
			return value
		}
		base := b.SourceValue(a)
		if base.Kind == "" {
			base.Kind = "missing"
		}
		if doc != nil {
			return doc.ValueAt(a, base)
		}
		return base
	}
	if !rule.nativeList {
		return rule.referencedChoices(valueAt)
	}
	addresses := slices.Clone(rule.members)
	// A previous revision or this batch can add a choice at a formerly empty
	// address, including far below the stored end of a whole-column list.
	if doc != nil {
		for a := range doc.ChangedAddresses() {
			if rule.reference.Contains(a) {
				addresses = append(addresses, a)
			}
		}
	}
	for a := range proposed {
		if rule.reference.Contains(a) {
			addresses = append(addresses, a)
		}
	}
	slices.SortFunc(addresses, func(a, b editdoc.Address) int { return cmp.Or(cmp.Compare(a.Row, b.Row), cmp.Compare(a.Col, b.Col)) })
	return rule.listChoices(slices.Values(slices.Compact(addresses)), valueAt)
}

func (b EditBatch) ListChoices(address editdoc.Address, doc *editdoc.Document) ([]string, error) {
	if reason := b.cells[address].ReadOnly; reason != "" {
		return nil, errors.New(reason)
	}
	for _, rule := range b.rules {
		if rule.Type == "list" && rule.Contains(address) {
			if rule.Reference {
				return b.choices(rule, doc, nil)
			}
			return rule.choices, nil
		}
	}
	return nil, errors.New("当前格没有可选列表")
}

func (l Layout) referenceRules(rules []editdoc.Rule) ([]validatedRule, error) {
	var out []validatedRule
	for _, rule := range rules {
		if !rule.Reference {
			continue
		}
		ref, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		if !l.containsReference(ref.reference) {
			return nil, errReferenceTarget
		}
		out = append(out, ref)
	}
	return out, nil
}

// ReferenceChoices reads just the requested list from the supplied source and
// applies the confirmed editor revision through valueAt.
func (l Layout) ReferenceChoices(ctx context.Context, f *workbook.EditSourceFile, rule editdoc.Rule, valueAt func(editdoc.Address, editdoc.Value) editdoc.Value) ([]string, error) {
	if !rule.Reference {
		return nil, errReferenceSyntax
	}
	compiled, err := compileRule(rule)
	if err != nil {
		return nil, err
	}
	if !l.containsReference(compiled.reference) {
		return nil, errReferenceTarget
	}
	addresses := slices.Collect(referenceAddresses(compiled.reference))
	source, err := l.Read(ctx, f, addresses)
	if err != nil {
		return nil, err
	}
	return compiled.referencedChoices(func(a editdoc.Address) editdoc.Value { return valueAt(a, source[a].Value) })
}

// CheckRuleReference additionally checks the aggregate bound for the proposed
// rule table. Other lists were already confirmed in this source revision.
func (l Layout) CheckRuleReference(ctx context.Context, f *workbook.EditSourceFile, rule editdoc.Rule, overlays []editdoc.Rule, valueAt func(editdoc.Address, editdoc.Value) editdoc.Value) error {
	if !rule.Reference {
		return nil
	}
	rules, err := l.referenceRules(l.EffectiveRules(append(slices.Clone(overlays), rule)))
	if err != nil {
		return err
	}
	if _, err := appendReferences(nil, rules); err != nil {
		return err
	}
	_, err = l.ReferenceChoices(ctx, f, rule, valueAt)
	return err
}
