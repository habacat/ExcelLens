package server

import (
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

// filterSpec records the successful query, independently from editable dialog
// controls. It contains no transient row positions or executable expressions.
type filterSpec sourceview.FilterSpec

func newFilterSpec(rules []workbook.FilterRule, options workbook.FilterOptions) filterSpec {
	spec := filterSpec{Rules: make([]workbook.FilterRule, len(rules)), HeaderRow: options.HeaderRow, SortColumn: options.SortColumn, Descending: options.Descending, SortKeys: append([]workbook.SortKey{}, options.SortKeys...)}
	for i, rule := range rules {
		spec.Rules[i] = rule
		spec.Rules[i].Values = append([]string(nil), rule.Values...)
	}
	return spec
}

func (spec filterSpec) active() bool {
	return len(spec.Rules) > 0 || len(spec.SortKeys) > 0 || spec.SortColumn > 0
}

func (spec filterSpec) options() workbook.FilterOptions {
	return workbook.FilterOptions{HeaderRow: spec.HeaderRow, SortColumn: spec.SortColumn, Descending: spec.Descending, SortKeys: spec.SortKeys}
}
