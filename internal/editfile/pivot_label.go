package editfile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"golang.org/x/text/collate"
)

type pivotLabel struct {
	spec  editdoc.PivotLabelFilter
	field int
	id    uint32
	order int
}

func isLabelKind(kind string) bool {
	switch kind {
	case "captionEqual", "captionNotEqual", "captionBeginsWith", "captionNotBeginsWith", "captionEndsWith", "captionNotEndsWith", "captionContains", "captionNotContains", "captionGreaterThan", "captionGreaterThanOrEqual", "captionLessThan", "captionLessThanOrEqual", "captionBetween", "captionNotBetween":
		return true
	}
	return false
}

func (t pivotTableXML) labels(n int) ([]pivotLabel, error) {
	var labels []pivotLabel
	for _, f := range t.Filters {
		if !isLabelKind(f.Type) {
			continue
		}
		if f.Field < 0 || f.Field >= n || f.Measure != nil || len(f.Columns) != 1 || f.Columns[0].Col != 0 {
			return nil, ErrXLSX
		}
		labels = append(labels, pivotLabel{editdoc.PivotLabelFilter{Kind: f.Type, Value: f.String1, Second: f.String2}, f.Field, f.ID, f.Order})
	}
	return labels, nil
}
func (p *pivotPlan) configureLabels(fields []cacheFieldXML, change *editdoc.Structure) error {
	var err error
	p.labelFilters, err = p.table.labels(len(fields))
	if err != nil {
		return err
	}
	p.labelFilters = slices.DeleteFunc(p.labelFilters, func(f pivotLabel) bool { return !slices.Contains(p.rows, f.field) && !slices.Contains(p.cols, f.field) })
	if change == nil {
		return nil
	}
	if change.Kind == "pivotValueFilter" && change.ValueFilter != nil && change.ValueFilter.Kind != "clear" && !pivotOn(p.table.MultipleFilters) {
		p.labelFilters = slices.DeleteFunc(p.labelFilters, func(f pivotLabel) bool { return fields[f.field].Name == change.ValueFilter.Field })
	}
	if change.Kind != "pivotLabelFilter" {
		return nil
	}
	if change.LabelFilter == nil {
		return ErrTarget
	}
	s := *change.LabelFilter
	i := findField(fields, s.Field)
	if !slices.Contains(p.rows, i) && !slices.Contains(p.cols, i) {
		return errors.New("标签筛选需要当前行或列字段")
	}
	p.labelFilters = slices.DeleteFunc(p.labelFilters, func(f pivotLabel) bool { return f.field == i })
	if s.Kind == "clear" {
		return nil
	}
	if !isLabelKind(s.Kind) || s.Value == "" || len(s.Value) > 1024 || len(s.Second) > 1024 || validateXLSXText(s.Value) != nil || validateXLSXText(s.Second) != nil || strings.HasSuffix(s.Kind, "Between") && s.Second == "" {
		return errors.New("请填写有效的标签筛选条件")
	}
	if !pivotOn(p.table.MultipleFilters) {
		p.valueFilters = slices.DeleteFunc(p.valueFilters, func(f pivotFilter) bool { return f.field == i })
	}
	p.labelFilters = append(p.labelFilters, pivotLabel{s, i, p.table.nextFilterID(), -1})
	return nil
}
func (p pivotPlan) matchLabels(index int, d *pivotDimension, c *collate.Collator) error {
	for _, f := range p.labelFilters {
		if f.field != index {
			continue
		}
		d.labelPass = map[int]bool{}
		caption := p.captionsForLanguage()
		var wildcard func(calculator.FormulaValue) bool
		if strings.ContainsAny(f.spec.Value, "*?~") {
			pattern, op, ok := f.textPattern()
			if ok {
				var err error
				wildcard, err = calculator.FilterCriterion(op, pattern)
				if err != nil {
					return err
				}
			}
		}
		for pos, it := range d.items {
			v, err := d.label(pos, caption)
			if err != nil {
				return err
			}
			label := v.Raw
			if it.Name == "" && d.values[it.X].kind == "d" {
				n, _ := pivotDateSerial(d.values[it.X].raw, d.date1904)
				label = d.dateFormat.Display(n, d.date1904)
			}
			if wildcard != nil {
				d.labelPass[pos] = wildcard(calculator.TextValue(label))
			} else {
				d.labelPass[pos] = f.matches(label, c)
			}
		}
	}
	return nil
}
func (f pivotLabel) matches(label string, c *collate.Collator) bool {
	s := f.spec
	cmp := c.CompareString(label, s.Value)
	a, b := strings.ToLower(label), strings.ToLower(s.Value)
	switch s.Kind {
	case "captionBeginsWith", "captionNotBeginsWith":
		return strings.HasPrefix(a, b) == (s.Kind == "captionBeginsWith")
	case "captionEndsWith", "captionNotEndsWith":
		return strings.HasSuffix(a, b) == (s.Kind == "captionEndsWith")
	case "captionContains", "captionNotContains":
		return strings.Contains(a, b) == (s.Kind == "captionContains")
	case "captionEqual":
		return cmp == 0
	case "captionNotEqual":
		return cmp != 0
	case "captionGreaterThan":
		return cmp > 0
	case "captionGreaterThanOrEqual":
		return cmp >= 0
	case "captionLessThan":
		return cmp < 0
	case "captionLessThanOrEqual":
		return cmp <= 0
	default:
		return (cmp >= 0 && c.CompareString(label, s.Second) <= 0) == (s.Kind == "captionBetween")
	}
}
func (f pivotLabel) textPattern() (string, string, bool) {
	s := f.spec
	switch s.Kind {
	case "captionEqual", "captionNotEqual", "captionContains", "captionNotContains", "captionBeginsWith", "captionNotBeginsWith", "captionEndsWith", "captionNotEndsWith":
	default:
		return "", "", false
	}
	v := s.Value
	op := "equal"
	if strings.Contains(s.Kind, "Not") {
		op = "notEqual"
	}
	if strings.HasSuffix(s.Kind, "Contains") || strings.HasSuffix(s.Kind, "EndsWith") {
		v = "*" + v
	}
	if strings.HasSuffix(s.Kind, "Contains") || strings.HasSuffix(s.Kind, "BeginsWith") {
		v += "*"
	}
	return v, op, true
}
func (f pivotLabel) xml() string {
	s := f.spec
	var out strings.Builder
	fmt.Fprintf(&out, `<filter fld="%d" type="%s" evalOrder="%d" id="%d" stringValue1="%s"`, f.field, s.Kind, f.order, f.id, escapedXML(s.Value))
	if strings.HasSuffix(s.Kind, "Between") {
		fmt.Fprintf(&out, ` stringValue2="%s"`, escapedXML(s.Second))
	}
	out.WriteString(`><autoFilter ref="A1"><filterColumn colId="0">`)
	if s.Kind == "captionEqual" {
		fmt.Fprintf(&out, `<filters><filter val="%s"/></filters>`, escapedXML(s.Value))
	} else {
		ops := comparisons("value" + strings.TrimPrefix(s.Kind, "caption"))
		v := s.Value
		if pattern, op, ok := f.textPattern(); ok {
			ops = []string{op}
			v = pattern
		}
		out.WriteString("<customFilters")
		if s.Kind == "captionBetween" {
			out.WriteString(` and="1"`)
		}
		out.WriteString(">")
		for i, op := range ops {
			if i > 0 {
				v = s.Second
			}
			out.WriteString("<customFilter")
			if op != "equal" {
				fmt.Fprintf(&out, ` operator="%s"`, op)
			}
			fmt.Fprintf(&out, ` val="%s"/>`, escapedXML(v))
		}
		out.WriteString("</customFilters>")
	}
	out.WriteString("</filterColumn></autoFilter></filter>")
	return out.String()
}
