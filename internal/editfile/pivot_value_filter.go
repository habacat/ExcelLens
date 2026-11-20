package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
)

type pivotFilterXML struct {
	String1    string `xml:"stringValue1,attr"`
	String2    string `xml:"stringValue2,attr"`
	Field      int    `xml:"fld,attr"`
	Type       string `xml:"type,attr"`
	ID         uint32 `xml:"id,attr"`
	Order      int    `xml:"evalOrder,attr"`
	Measure    *int   `xml:"iMeasureFld,attr"`
	Extensions []struct {
		URI string `xml:"uri,attr"`
		Day *struct {
			Use string `xml:"useWholeDay,attr"`
		} `xml:"http://schemas.microsoft.com/office/spreadsheetml/2010/11/main pivotFilter"`
	} `xml:"extLst>ext"`
	Columns []struct {
		Col int `xml:"colId,attr"`
		Top *struct {
			Top   string  `xml:"top,attr"`
			Value float64 `xml:"val,attr"`
		} `xml:"top10"`
		Compare []struct {
			Op    string `xml:"operator,attr"`
			Value string `xml:"val,attr"`
		} `xml:"customFilters>customFilter"`
		Dynamic *struct {
			Kind string `xml:"type,attr"`
			Low  string `xml:"val,attr"`
			High string `xml:"maxVal,attr"`
		} `xml:"dynamicFilter"`
	} `xml:"autoFilter>filterColumn"`
}

type pivotFilter struct {
	spec  editdoc.PivotValueFilter
	field int
	id    uint32
	order int
}

// The kind used by the control is also the persisted data-independent mode.
// SpreadsheetML separately encodes top/bottom inside CT_Top10.
func rankKind(k string) (mode string, top bool) {
	for _, s := range []string{"Count", "Percent", "Sum"} {
		if k == "top"+s || k == "bottom"+s {
			return strings.ToLower(s), strings.HasPrefix(k, "top")
		}
	}
	return "", false
}

func comparisons(k string) []string {
	switch k {
	case "valueEqual":
		return []string{"equal"}
	case "valueNotEqual":
		return []string{"notEqual"}
	case "valueGreaterThan":
		return []string{"greaterThan"}
	case "valueGreaterThanOrEqual":
		return []string{"greaterThanOrEqual"}
	case "valueLessThan":
		return []string{"lessThan"}
	case "valueLessThanOrEqual":
		return []string{"lessThanOrEqual"}
	case "valueBetween":
		return []string{"greaterThanOrEqual", "lessThanOrEqual"}
	case "valueNotBetween":
		return []string{"lessThan", "greaterThan"}
	}
	return nil
}

func validatePivotFilter(f editdoc.PivotValueFilter) error {
	if math.IsNaN(f.Value) || math.IsInf(f.Value, 0) || math.IsNaN(f.Second) || math.IsInf(f.Second, 0) {
		return errors.New("筛选阈值必须是有限数值")
	}
	mode, _ := rankKind(f.Kind)
	switch mode {
	case "count":
		if f.Value < 1 || f.Value != math.Trunc(f.Value) || f.Value > math.MaxInt32 {
			return errors.New("项目数量必须是正整数")
		}
	case "percent":
		if f.Value <= 0 || f.Value > 100 {
			return errors.New("百分比须大于 0 且不超过 100")
		}
	case "sum":
		if f.Value <= 0 {
			return errors.New("累计值须大于 0")
		}
	default:
		ops := comparisons(f.Kind)
		if len(ops) == 0 {
			return errors.New("此筛选类型尚不能重算")
		}
		if len(ops) == 2 && f.Second < f.Value {
			return errors.New("上限不能低于下限")
		}
	}
	return nil
}

func (t pivotTableXML) valueFilters(fields int) ([]pivotFilter, error) {
	var filters []pivotFilter
	seen := map[int]bool{}
	for _, v := range t.Filters {
		if isLabelKind(v.Type) || isPivotDateKind(v.Type) {
			continue
		}
		if v.Field < 0 || v.Field >= fields || seen[v.Field] || v.Measure == nil || *v.Measure < 0 || *v.Measure >= len(t.DataFields) || len(v.Columns) != 1 || v.Columns[0].Col != 0 {
			return nil, errors.New("此透视表的筛选未绑定到可重算的值字段")
		}
		f := editdoc.PivotValueFilter{Kind: v.Type, Measure: *v.Measure}
		c := v.Columns[0]
		switch v.Type {
		case "count", "percent", "sum":
			if c.Top == nil {
				return nil, ErrXLSX
			}
			prefix := "top"
			if !pivotOn(c.Top.Top) {
				prefix = "bottom"
			}
			f.Kind = prefix + strings.ToUpper(v.Type[:1]) + v.Type[1:]
			f.Value = c.Top.Value
		default:
			ops := comparisons(v.Type)
			if len(ops) == 0 || len(ops) != len(c.Compare) {
				return nil, errors.New("此透视表包含尚不能重算的标签或日期筛选")
			}
			for i, operand := range c.Compare {
				op := operand.Op
				if op == "" {
					op = "equal"
				}
				if op != ops[i] {
					return nil, ErrXLSX
				}
				n, err := strconv.ParseFloat(operand.Value, 64)
				if err != nil {
					return nil, ErrXLSX
				}
				if i == 0 {
					f.Value = n
				} else {
					f.Second = n
				}
			}
		}
		if err := validatePivotFilter(f); err != nil {
			return nil, err
		}
		seen[v.Field] = true
		filters = append(filters, pivotFilter{spec: f, field: v.Field, id: v.ID, order: v.Order})
	}
	return filters, nil
}

func (p *pivotPlan) configureValueFilter(fields []cacheFieldXML, change *editdoc.Structure) error {
	// A field moved out of both axes can no longer have a value filter.
	if p.measureOrigins != nil {
		for i := range p.valueFilters {
			f := &p.valueFilters[i]
			f.spec.Measure = slices.Index(p.measureOrigins, f.spec.Measure)
		}
	}
	p.valueFilters = slices.DeleteFunc(p.valueFilters, func(f pivotFilter) bool {
		return f.spec.Measure < 0 || !slices.Contains(p.rows, f.field) && !slices.Contains(p.cols, f.field)
	})
	if change == nil || change.Kind != "pivotValueFilter" {
		return nil
	}
	if change.ValueFilter == nil {
		return errors.New("请选择值筛选字段和条件")
	}
	f := *change.ValueFilter
	k := findField(fields, f.Field)
	if !slices.Contains(p.rows, k) && !slices.Contains(p.cols, k) {
		return errors.New("值筛选需要当前行或列字段")
	}
	p.valueFilters = slices.DeleteFunc(p.valueFilters, func(old pivotFilter) bool { return old.field == k })
	if f.Kind == "clear" {
		return nil
	}
	if f.Measure < 0 || f.Measure >= max(1, len(p.measures)) {
		return errors.New("请选择有效的值筛选度量")
	}
	if err := validatePivotFilter(f); err != nil {
		return err
	}
	p.valueFilters = append(p.valueFilters, pivotFilter{spec: f, field: k, id: p.table.nextFilterID(), order: -1})
	return nil
}

func (f pivotFilter) xml() string {
	mode, top := rankKind(f.spec.Kind)
	kind := mode
	if kind == "" {
		kind = f.spec.Kind
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<filter fld="%d" type="%s" evalOrder="%d" id="%d" iMeasureFld="%d"><autoFilter ref="A1"><filterColumn colId="0">`, f.field, kind, f.order, f.id, f.spec.Measure)
	if mode != "" {
		out.WriteString("<top10")
		if !top {
			out.WriteString(` top="0"`)
		}
		if mode == "percent" {
			out.WriteString(` percent="1"`)
		}
		fmt.Fprintf(&out, ` val="%g" filterVal="%g"/>`, f.spec.Value, f.spec.Value)
	} else {
		out.WriteString("<customFilters")
		if f.spec.Kind == "valueBetween" {
			out.WriteString(` and="1"`)
		}
		out.WriteByte('>')
		for i, op := range comparisons(kind) {
			value := f.spec.Value
			if i == 1 {
				value = f.spec.Second
			}
			out.WriteString("<customFilter")
			if op != "equal" {
				fmt.Fprintf(&out, ` operator="%s"`, op)
			}
			fmt.Fprintf(&out, ` val="%g"/>`, value)
		}
		out.WriteString("</customFilters>")
	}
	out.WriteString("</filterColumn></autoFilter></filter>")
	return out.String()
}

func (p pivotPlan) writeValueFilters(ctx context.Context, raw []byte) ([]byte, error) {
	original, err := p.table.valueFilters(len(p.table.Fields))
	if err != nil {
		return nil, err
	}
	oldLabels, err := p.table.labels(len(p.table.Fields))
	if err != nil {
		return nil, err
	}
	oldDates, err := p.table.dateFilters(len(p.table.Fields), p.date1904)
	if err != nil {
		return nil, err
	}
	if slices.EqualFunc(p.dates, oldDates, func(a, b pivotDateFilter) bool { a.spec.Field, b.spec.Field = "", ""; return a == b }) && slices.EqualFunc(p.labelFilters, oldLabels, func(a, b pivotLabel) bool { a.spec.Field, b.spec.Field = "", ""; return a == b }) && slices.EqualFunc(p.valueFilters, original, func(a, b pivotFilter) bool {
		a.spec.Field, b.spec.Field = "", ""
		return a == b
	}) {
		return raw, nil
	}
	return p.patchFilters(ctx, raw)
}

// Replace a direct child or insert it before the first following schema
// sibling. Inherited namespace declarations on unrelated children survive.
func replacePivotChild(ctx context.Context, raw []byte, local string, following []string, body string) ([]byte, error) {
	start, end, insert := -1, -1, -1
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if len(e.stack) == 2 && e.stack[1].Space == mainNS {
			switch e.token.(type) {
			case xml.StartElement:
				if e.local() == local {
					if start >= 0 {
						return ErrXLSX
					}
					start = e.from
				}
				if insert < 0 && slices.Contains(following, e.local()) {
					insert = e.from
				}
			case xml.EndElement:
				if e.local() == local {
					end = e.to
				}
			}
		}
		if _, end := e.token.(xml.EndElement); end && len(e.stack) == 1 && insert < 0 {
			insert = e.from
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if start < 0 {
		start, end = insert, insert
	}
	if start < 0 || end < start {
		return nil, ErrXLSX
	}
	return applyXML(ctx, raw, []xmlPatch{{start, end, body}})
}
