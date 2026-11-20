package editfile

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

var pivotDateFilterExtension = xlsxExtension{"{0605FD5F-26C8-4aeb-8148-2DB25E43C511}", x15NS, "pivotFilter", []string{"useWholeDay"}}

func (t pivotTableXML) dateFilters(fields int, date1904 bool) ([]pivotDateFilter, error) {
	var filters []pivotDateFilter
	for _, v := range t.Filters {
		if !isPivotDateKind(v.Type) {
			continue
		}
		if v.Field < 0 || v.Field >= fields || v.Measure != nil || len(v.Columns) != 1 || v.Columns[0].Col != 0 {
			return nil, ErrXLSX
		}
		f := pivotDateFilter{field: v.Field, id: v.ID, order: v.Order}
		f.spec.Kind = v.Type
		for _, ext := range v.Extensions {
			if !strings.EqualFold(ext.URI, pivotDateFilterExtension.uri) || ext.Day == nil || !ruleBool(ext.Day.Use) {
				return nil, ErrXLSX
			}
			f.spec.WholeDay = isTrue(ext.Day.Use)
		}
		col := v.Columns[0]
		if ops, ok := dateComparisons[v.Type]; ok {
			if len(col.Compare) != 1 && ops.second == "" || len(col.Compare) != 2 && ops.second != "" || col.Dynamic != nil || col.Top != nil {
				return nil, ErrXLSX
			}
			for i, op := range col.Compare {
				want := ops.first
				if i > 0 {
					want = ops.second
				}
				if op.Op != want && !(op.Op == "" && want == "equal") {
					return nil, ErrXLSX
				}
				n, err := strconv.ParseFloat(op.Value, 64)
				if err != nil {
					return nil, ErrXLSX
				}
				text, err := filterDateText(n, date1904, f.spec.WholeDay)
				if err != nil {
					return nil, err
				}
				if i == 0 {
					f.low, f.spec.Start = n, text
				} else {
					f.high, f.spec.End = n, text
				}
			}
		} else {
			dyn := col.Dynamic
			if dyn == nil || dyn.Kind != v.Type || len(col.Compare) != 0 || col.Top != nil {
				return nil, ErrXLSX
			}
			if dyn.Low != "" || dyn.High != "" {
				var e1, e2 error
				f.low, e1 = strconv.ParseFloat(dyn.Low, 64)
				f.high, e2 = strconv.ParseFloat(dyn.High, 64)
				if e1 != nil || e2 != nil || math.IsNaN(f.low) || math.IsNaN(f.high) || math.IsInf(f.low, 0) || math.IsInf(f.high, 0) {
					return nil, ErrXLSX
				}
			}
		}
		filters = append(filters, f)
	}
	return filters, nil
}

func (f pivotDateFilter) xml() string {
	var out strings.Builder
	fmt.Fprintf(&out, `<filter fld="%d" type="%s" evalOrder="%d" id="%d"><autoFilter ref="A1"><filterColumn colId="0">`, f.field, f.spec.Kind, f.order, f.id)
	number := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	if ops, ok := dateComparisons[f.spec.Kind]; ok {
		out.WriteString("<customFilters")
		if f.spec.Kind == "dateBetween" {
			out.WriteString(` and="1"`)
		}
		out.WriteByte('>')
		write := func(op string, n float64) {
			out.WriteString("<customFilter")
			if op != "equal" {
				fmt.Fprintf(&out, ` operator="%s"`, op)
			}
			fmt.Fprintf(&out, ` val="%s"/>`, number(n))
		}
		write(ops.first, f.low)
		if ops.second != "" {
			write(ops.second, f.high)
		}
		out.WriteString("</customFilters>")
	} else {
		fmt.Fprintf(&out, `<dynamicFilter type="%s"`, f.spec.Kind)
		if m, q := datePeriod(f.spec.Kind); m == 0 && q == 0 {
			fmt.Fprintf(&out, ` val="%s" maxVal="%s"`, number(f.low), number(f.high))
		}
		out.WriteString("/>")
	}
	out.WriteString("</filterColumn></autoFilter>")
	if f.spec.WholeDay {
		fmt.Fprintf(&out, `<extLst><ext uri="%s"><x15:pivotFilter xmlns:x15="%s" useWholeDay="1"/></ext></extLst>`, pivotDateFilterExtension.uri, x15NS)
	}
	out.WriteString("</filter>")
	return out.String()
}
