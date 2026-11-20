package editfile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"excellens/internal/editdoc"
)

// The Values pseudo-field is an axis coordinate, never a cache column.
const pivotValuesField = -2

func (m pivotDataField) method() string {
	if m.Subtotal == "" {
		return "sum"
	}
	return m.Subtotal
}
func (t pivotTableXML) valuesAxis() *editdoc.PivotValuesAxis {
	for i, f := range t.RowFields {
		if f.X == pivotValuesField {
			return &editdoc.PivotValuesAxis{Axis: "rows", Position: i}
		}
	}
	for i, f := range t.ColFields {
		if f.X == pivotValuesField {
			return &editdoc.PivotValuesAxis{Axis: "columns", Position: i}
		}
	}
	return nil
}
func (t pivotTableXML) measureInfo(fields []cacheFieldXML) []editdoc.PivotMeasure {
	var measures []editdoc.PivotMeasure
	for i, m := range t.DataFields {
		measures = append(measures, editdoc.PivotMeasure{Field: fields[m.Fld].Name, Aggregate: m.method(), Caption: m.Name, SourceIndex: &i})
	}
	return measures
}
func validMeasureMethod(v string) bool {
	switch v {
	case "sum", "count", "average", "min", "max", "product", "countNums", "stdDev", "stdDevp", "var", "varp":
		return true
	}
	return false
}
func measureCaption(method, field string, english bool) string {
	name := pivotMethodName(method, english)
	if english {
		return name + " of " + field
	}
	return name + "项:" + field
}
func (p *pivotPlan) bindMeasures(fields []cacheFieldXML, spec []editdoc.PivotMeasure) error {
	if len(spec) == 0 || len(spec) > 256 {
		return errors.New("请选择 1 至 256 个值字段")
	}
	seen := map[string]bool{}
	claimed := make(map[int]bool)
	for _, m := range spec {
		if m.SourceIndex == nil || *m.SourceIndex == -1 {
			continue
		}
		index := *m.SourceIndex
		if index < 0 || index >= len(p.table.DataFields) || claimed[index] || p.table.DataFields[index].Fld != findField(fields, m.Field) {
			return errors.New("值字段与当前透视布局不一致")
		}
		claimed[index] = true
	}
	var next []pivotDataField
	for _, m := range spec {
		i := findField(fields, m.Field)
		if i < 0 || !validMeasureMethod(m.Aggregate) {
			return errors.New("请选择有效的值字段与汇总方式")
		}
		field := pivotDataField{Fld: i, Subtotal: m.Aggregate, Name: m.Caption}
		origin := -1
		if m.SourceIndex != nil {
			origin = *m.SourceIndex
		} else {
			// Older callers identify an existing measure by its caption or method.
			for j, old := range p.table.DataFields {
				if !claimed[j] && old.Fld == i && (m.Caption == old.Name || m.Caption == "" && old.method() == m.Aggregate) {
					origin = j
					claimed[j] = true
					break
				}
			}
		}
		if origin >= 0 {
			old := p.table.DataFields[origin]
			field.Number, field.Show = old.Number, old.Show
			if old.method() == m.Aggregate {
				field.Subtotal = old.Subtotal
				if field.Name == "" && m.SourceIndex == nil {
					field.Name = old.Name
				}
			}
		}
		p.measureOrigins = append(p.measureOrigins, origin)
		if field.Name == "" {
			base := measureCaption(m.Aggregate, m.Field, p.table.DataCaption == "Values")
			field.Name = base
			for n := 2; seen[strings.ToLower(field.Name)] || findField(fields, field.Name) >= 0; n++ {
				field.Name = fmt.Sprintf("%s%d", base, n)
			}
		}
		key := strings.ToLower(field.Name)
		if validateXLSXText(field.Name) != nil || strings.TrimSpace(key) == "" || seen[key] || len(utf16.Encode([]rune(key))) > 255 {
			return errors.New("值字段标题为空、重复或过长")
		}
		if slices.ContainsFunc(fields, func(f cacheFieldXML) bool { return strings.EqualFold(f.Name, field.Name) }) {
			return errors.New("值字段标题与源字段重名")
		}
		seen[key] = true
		next = append(next, field)
	}
	p.measures = next
	return nil
}

func (p *pivotPlan) configureMeasures(fields []cacheFieldXML, change *editdoc.Structure, axes *editdoc.PivotAxes) error {
	p.measures = slices.Clone(p.table.DataFields)
	if change != nil && change.Kind == "configurePivot" {
		spec := change.Measures
		if spec == nil && change.Data != "" {
			spec = []editdoc.PivotMeasure{{Field: change.Data, Aggregate: change.Aggregate}}
		}
		if spec != nil {
			if err := p.bindMeasures(fields, spec); err != nil {
				return err
			}
		}
		if change.ValuesAxis != nil {
			a := *change.ValuesAxis
			p.valuesAxis = &a
		}
	}
	for _, m := range p.measures {
		if !validMeasureMethod(m.method()) {
			return errors.New("此值字段的汇总方式不受支持")
		}
	}
	p.data, p.aggregation = p.measures[0].Fld, p.measures[0].method()
	if change != nil && change.Kind == "configurePivot" && len(p.measures) > 1 && change.ValuesAxis == nil && p.valuesAxis != nil {
		n := len(axes.Columns)
		if p.valuesAxis.Axis == "rows" {
			n = len(axes.Rows)
		}
		p.valuesAxis.Position = min(p.valuesAxis.Position, n)
	}
	if len(p.measures) == 1 {
		p.valuesAxis = nil
		return nil
	}
	if p.valuesAxis == nil {
		p.valuesAxis = &editdoc.PivotValuesAxis{Axis: "columns", Position: len(axes.Columns)}
	}
	n := len(axes.Columns)
	if p.valuesAxis.Axis == "rows" {
		n = len(axes.Rows)
	} else if p.valuesAxis.Axis != "columns" {
		return errors.New("值区域需要放在行或列字段中")
	}
	if p.valuesAxis.Position < 0 || p.valuesAxis.Position > n {
		return errors.New("值区域位置超出当前布局")
	}
	return nil
}
func (p pivotPlan) displayAxes() ([]int, []int) {
	rows, cols := p.rows, p.cols
	if v := p.valuesAxis; v != nil {
		if v.Axis == "rows" {
			rows = slices.Insert(slices.Clone(rows), v.Position, pivotValuesField)
		} else {
			cols = slices.Insert(slices.Clone(cols), v.Position, pivotValuesField)
		}
	}
	return rows, cols
}

func (p pivotPlan) measureAt(i int) pivotDataField {
	if len(p.measures) == 0 {
		return pivotDataField{Fld: p.data, Subtotal: p.aggregation}
	}
	return p.measures[i]
}
func measureValue(row []pivotValue, r pivotRecords, m pivotDataField, index int) (pivotValue, error) {
	if len(row) > len(r.fields)+1 {
		return row[len(r.fields)+1+index], nil
	}
	f := m.Fld
	if raw, ok := r.measureSource[f]; ok {
		f = raw
	}
	return pivotMeasure(row[f], r)
}
func (p pivotPlan) measureRecords(ctx context.Context, r pivotRecords, dims map[int]*pivotDimension) ([][]pivotValue, error) {
	if len(p.measures) <= 1 || len(r.items) == 0 {
		return p.calculatedRows(ctx, r, dims)
	}
	var result [][]pivotValue
	for i, m := range p.measures {
		p.data, p.aggregation = m.Fld, m.method()
		rows, err := p.calculatedRows(ctx, r, dims)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			result = slices.Clone(rows)
			for j := len(r.rows); j < len(result); j++ {
				result[j] = append(slices.Clone(result[j]), make([]pivotValue, len(p.measures))...)
			}
		}
		if len(result) != len(rows) {
			return nil, ErrXLSX
		}
		for j := len(r.rows); j < len(rows); j++ {
			result[j][len(r.fields)+1+i] = rows[j][m.Fld]
		}
	}
	return result, nil
}
