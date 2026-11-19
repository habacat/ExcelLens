package editdoc

import (
	"cmp"
	"slices"
	"unicode/utf8"

	"excellens/internal/formats"
)

type Area struct {
	Start  Address `json:"start"`
	EndRow int     `json:"endRow"`
	EndCol int     `json:"endCol"`
}

func (a Area) Valid() bool {
	return validAddress(a.Start) && a.EndRow >= a.Start.Row && a.EndRow <= formats.ExcelRows && a.EndCol >= a.Start.Col && a.EndCol <= formats.ExcelColumns
}

func (a Area) Contains(cell Address) bool {
	return a.Start.Sheet == cell.Sheet && a.Start.Row <= cell.Row && a.EndRow >= cell.Row && a.Start.Col <= cell.Col && a.EndCol >= cell.Col
}

func (a Area) Intersects(b Area) bool {
	return a.Start.Sheet == b.Start.Sheet && a.Start.Row <= b.EndRow && a.EndRow >= b.Start.Row && a.Start.Col <= b.EndCol && a.EndCol >= b.Start.Col
}

type Rule struct {
	Area
	Type       string `json:"type"`
	Operator   string `json:"operator,omitempty"`
	First      string `json:"first"`
	Second     string `json:"second,omitempty"`
	AllowBlank bool   `json:"allowBlank"`
	Reference  bool   `json:"reference,omitempty"`
}

func (r Rule) Valid() bool {
	return r.Area.Valid() && (!r.Reference || r.Type == "list") && len(r.Type) > 0 && len(r.Type) <= 16 && len(r.Operator) <= 20 && len(r.First) > 0 && len(r.First) <= formats.EditRuleTextBytes && len(r.Second) <= formats.EditRuleTextBytes && utf8.ValidString(r.First) && utf8.ValidString(r.Second)
}

type rulePatch struct {
	Area
	Before *Rule `json:"before,omitempty"`
	After  *Rule `json:"after,omitempty"`
}

func (d *Document) RuleChanges() []Rule {
	rules := make([]Rule, 0, len(d.rules))
	for _, r := range d.rules {
		rules = append(rules, r)
	}
	slices.SortFunc(rules, func(a, b Rule) int {
		return cmp.Or(cmp.Compare(a.Start.Sheet, b.Start.Sheet), cmp.Compare(a.Start.Row, b.Start.Row), cmp.Compare(a.Start.Col, b.Start.Col))
	})
	return rules
}

func (d *Document) Dirty() bool {
	return len(d.value) != 0 || len(d.rules) != 0 || len(d.templates) != 0
}
