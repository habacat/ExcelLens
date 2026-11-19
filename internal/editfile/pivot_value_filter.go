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
	Field   int    `xml:"fld,attr"`
	Type    string `xml:"type,attr"`
	ID      uint32 `xml:"id,attr"`
	Order   int    `xml:"evalOrder,attr"`
	Measure *int   `xml:"iMeasureFld,attr"`
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
		if v.Field < 0 || v.Field >= fields || seen[v.Field] || v.Measure == nil || *v.Measure != 0 || len(v.Columns) != 1 || v.Columns[0].Col != 0 {
			return nil, errors.New("此透视表的筛选未绑定到可重算的值字段")
		}
		f := editdoc.PivotValueFilter{Kind: v.Type}
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
	var id uint32
	p.valueFilters = slices.DeleteFunc(p.valueFilters, func(f pivotFilter) bool {
		id = max(id, f.id)
		return !slices.Contains(p.rows, f.field) && !slices.Contains(p.cols, f.field)
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
	if err := validatePivotFilter(f); err != nil {
		return err
	}
	p.valueFilters = append(p.valueFilters, pivotFilter{spec: f, field: k, id: id + 1, order: -1})
	return nil
}

func (f pivotFilter) xml() string {
	mode, top := rankKind(f.spec.Kind)
	kind := mode
	if kind == "" {
		kind = f.spec.Kind
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<filter fld="%d" type="%s" evalOrder="%d" id="%d" iMeasureFld="0"><autoFilter ref="A1"><filterColumn colId="0">`, f.field, kind, f.order, f.id)
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
	if slices.EqualFunc(p.valueFilters, original, func(a, b pivotFilter) bool {
		a.spec.Field, b.spec.Field = "", ""
		return a == b
	}) {
		return raw, nil
	}
	var out strings.Builder
	if len(p.valueFilters) > 0 {
		fmt.Fprintf(&out, `<filters xmlns="%s" count="%d">`, mainNS, len(p.valueFilters))
		for _, f := range p.valueFilters {
			out.WriteString(f.xml())
		}
		out.WriteString("</filters>")
	}
	return replacePivotChild(ctx, raw, "filters", []string{"rowHierarchiesUsage", "colHierarchiesUsage", "extLst"}, out.String())
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
