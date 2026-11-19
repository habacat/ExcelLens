package editfile

import (
	"errors"
	"math/big"
	"slices"
	"strings"
	"unicode/utf16"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

var errRule = errors.New("仅支持固定文字列表和固定数值条件的数据规则")

type validatedRule struct {
	editdoc.Rule
	choices    []string
	reference  editdoc.Area
	nativeList bool
	members    []editdoc.Address
	first      *big.Rat
	second     *big.Rat
}

func ruleNumber(raw string) (*big.Rat, bool) {
	if validateXLSXNumber(raw) != nil {
		return nil, false
	}
	return new(big.Rat).SetString(raw)
}

func ListFormula(choices []string) string { return `"` + strings.Join(choices, ",") + `"` }

func compileRule(rule editdoc.Rule) (validatedRule, error) {
	v := validatedRule{Rule: rule}
	if !rule.Valid() {
		return v, errRule
	}
	if rule.Type == "list" {
		if rule.Operator != "" || rule.Second != "" {
			return v, errRule
		}
		if rule.Reference {
			var err error
			v.reference, err = listReference(rule)
			return v, err
		}
		if len(rule.First) < 3 || rule.First[0] != '"' || rule.First[len(rule.First)-1] != '"' {
			return v, errRule
		}
		raw := rule.First[1 : len(rule.First)-1]
		if strings.ContainsAny(raw, "\"\r\n\t") || len(utf16.Encode([]rune(raw))) > formats.EditInlineListUnits || validateXLSXText(raw) != nil {
			return v, errRule
		}
		choices := strings.Split(raw, ",")
		for _, choice := range choices {
			if choice == "" || strings.TrimSpace(choice) != choice {
				return v, errRule
			}
		}
		v.choices = choices
		return v, nil
	}
	switch rule.Type {
	case "whole", "decimal", "date", "textLength":
	default:
		return v, errRule
	}
	var ok bool
	v.first, ok = ruleNumber(rule.First)
	if !ok || !v.validBound(v.first) {
		return v, errRule
	}
	switch rule.Operator {
	case "between", "notBetween":
		v.second, ok = ruleNumber(rule.Second)
		if !ok || !v.validBound(v.second) || v.first.Cmp(v.second) > 0 {
			return v, errRule
		}
	case "equal", "notEqual", "greaterThan", "greaterThanOrEqual", "lessThan", "lessThanOrEqual":
		if rule.Second != "" {
			return v, errRule
		}
	default:
		return v, errRule
	}
	return v, nil
}

func (r validatedRule) validBound(n *big.Rat) bool {
	switch r.Type {
	case "whole":
		return n.IsInt()
	case "textLength":
		return n.IsInt() && n.Sign() >= 0 && n.Cmp(big.NewRat(32767, 1)) <= 0
	case "date":
		return n.IsInt() && n.Sign() >= 0 && n.Cmp(big.NewRat(2958465, 1)) <= 0
	default:
		return true
	}
}

func (r validatedRule) accepts(value editdoc.Value) bool {
	if value.Kind == "blank" || value.Kind == "text" && value.Raw == "" {
		return r.AllowBlank
	}
	if r.Type == "list" {
		return value.Kind == "text" && slices.Contains(r.choices, value.Raw)
	}
	var n *big.Rat
	if r.Type == "textLength" {
		if value.Kind != "text" {
			return false
		}
		n = big.NewRat(int64(len(utf16.Encode([]rune(value.Raw)))), 1)
	} else {
		if value.Kind != "number" {
			return false
		}
		var ok bool
		n, ok = ruleNumber(value.Raw)
		if !ok || !r.validBound(n) {
			return false
		}
	}
	first := n.Cmp(r.first)
	switch r.Operator {
	case "between":
		return first >= 0 && n.Cmp(r.second) <= 0
	case "notBetween":
		return first < 0 || n.Cmp(r.second) > 0
	case "equal":
		return first == 0
	case "notEqual":
		return first != 0
	case "greaterThan":
		return first > 0
	case "greaterThanOrEqual":
		return first >= 0
	case "lessThan":
		return first < 0
	case "lessThanOrEqual":
		return first <= 0
	}
	return false
}

// EffectiveRules retains original declarations and overlays explicit edits to
// the same rectangle. The writer uses the same range identity.
func (l Layout) EffectiveRules(changes []editdoc.Rule) []editdoc.Rule {
	if l.XLSX == nil {
		return nil
	}
	out := slices.Clone(l.XLSX.Rules)
	for _, rule := range changes {
		index := slices.IndexFunc(out, func(r editdoc.Rule) bool { return r.Area == rule.Area })
		if index >= 0 {
			out[index] = rule
		} else {
			out = append(out, rule)
		}
	}
	return out
}

func (l Layout) RuleBase(rule editdoc.Rule, changes []editdoc.Rule) (*editdoc.Rule, error) {
	end := editdoc.Address{Sheet: rule.Start.Sheet, Row: rule.EndRow, Col: rule.EndCol}
	if l.XLSX == nil || !l.Contains(rule.Start) || !l.Contains(end) {
		return nil, ErrTarget
	}
	if l.XLSX.Structured {
		return nil, errors.New("此 XLSX 不支持更改数据规则")
	}
	compiled, err := compileRule(rule)
	if err != nil {
		return nil, err
	}
	if rule.Reference && !l.containsReference(compiled.reference) {
		return nil, errReferenceTarget
	}
	rules := l.EffectiveRules(changes)
	for _, existing := range rules {
		if existing.Area == rule.Area {
			for _, base := range l.XLSX.Rules {
				if base.Area == rule.Area {
					return &base, nil
				}
			}
			return nil, nil
		}
		if existing.Intersects(rule.Area) {
			return nil, errors.New("规则范围与已有规则重叠，请读取并修改原规则范围")
		}
	}
	if len(rules) >= formats.EditValidationRules {
		return nil, errors.New("数据规则数量超过编辑上限")
	}
	return nil, nil
}

func RuleChoices(rule editdoc.Rule) []string {
	v, _ := compileRule(rule)
	return v.choices
}
