package editfile

import (
	"context"
	"errors"
	"slices"
	"time"

	"excellens/internal/editdoc"
)

func (p *pivotPlan) configureDates(fields []cacheFieldXML, levels []pivotDateLevel, change *editdoc.Structure, today time.Time) error {
	current, err := p.table.dateFilters(len(fields), p.date1904)
	if err != nil {
		return err
	}
	onAxis := func(i int) bool { return slices.Contains(p.rows, i) || slices.Contains(p.cols, i) }
	active := func(i int) bool {
		if onAxis(i) {
			return true
		}
		return slices.ContainsFunc(levels, func(g pivotDateLevel) bool { return g.base == i && onAxis(g.field) })
	}
	multiple := pivotOn(p.table.MultipleFilters)
	for _, f := range current {
		if !active(f.field) {
			continue
		}
		if change != nil && (change.Kind == "pivotLabelFilter" && change.LabelFilter != nil && change.LabelFilter.Kind != "clear" && fields[f.field].Name == change.LabelFilter.Field ||
			!multiple && change.Kind == "pivotValueFilter" && change.ValueFilter != nil && change.ValueFilter.Kind != "clear" && fields[f.field].Name == change.ValueFilter.Field) {
			continue
		}
		f.setPeriod(today, p.date1904)
		p.dates = append(p.dates, f)
	}
	if change == nil || change.Kind != "pivotDateFilter" {
		return nil
	}
	if change.DateFilter == nil {
		return ErrTarget
	}
	spec := *change.DateFilter
	field := findField(fields, spec.Field)
	if field < 0 || !active(field) || !isSourceField(fields[field]) {
		return errors.New("请选择当前行、列或日期分组对应的日期字段")
	}
	p.dates = slices.DeleteFunc(p.dates, func(f pivotDateFilter) bool { return f.field == field })
	if spec.Kind == "clear" {
		return nil
	}
	if !isPivotDateKind(spec.Kind) {
		return errors.New("请选择日期筛选条件")
	}
	if !eligibleDateField(fields[field]) {
		return errors.New("日期筛选需要日期字段")
	}
	f := pivotDateFilter{spec: spec, field: field, order: -1, id: p.table.nextFilterID()}
	if ops, ok := dateComparisons[spec.Kind]; ok {
		f.low, err = requestDateNumber(spec.Start, spec.WholeDay, p.date1904)
		if err != nil {
			return err
		}
		if ops.second != "" {
			f.high, err = requestDateNumber(spec.End, spec.WholeDay, p.date1904)
			if err != nil {
				return err
			}
			if f.low > f.high {
				return errors.New("结束日期不能早于开始日期")
			}
		} else if spec.End != "" {
			return ErrTarget
		}
	} else {
		if spec.Start != "" || spec.End != "" || spec.WholeDay {
			return errors.New("相对日期条件无需指定边界")
		}
		f.setPeriod(today, p.date1904)
	}
	p.labelFilters = slices.DeleteFunc(p.labelFilters, func(f pivotLabel) bool { return f.field == field })
	if !multiple {
		p.valueFilters = slices.DeleteFunc(p.valueFilters, func(f pivotFilter) bool { return f.field == field })
	}
	p.dates = append(p.dates, f)
	return nil
}

func eligibleDateField(f cacheFieldXML) bool {
	if !isSourceField(f) || f.Shared == nil {
		return false
	}
	found := isTrue(f.Shared.Date)
	for _, v := range f.Shared.Items {
		switch v.XMLName.Local {
		case "d":
			found = true
		case "m":
		default:
			return false
		}
	}
	return found
}

type pivotDateMask struct {
	column int
	pass   map[pivotValue]bool
}

// A grouped caption can represent both matching and nonmatching records.
// Filter the original timestamps before building axis members or subtotals.
func (p pivotPlan) dateMasks(ctx context.Context, r pivotRecords, dims map[int]*pivotDimension) ([]pivotDateMask, error) {
	var masks []pivotDateMask
	for _, f := range p.dates {
		column := f.field
		if original, ok := r.measureSource[column]; ok {
			column = original
		}
		mask := pivotDateMask{column: column, pass: make(map[pivotValue]bool)}
		for i, v := range r.dicts[column].values {
			if i%512 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if v.kind != "d" {
				continue
			}
			t, err := time.Parse(pivotDateLayout, v.raw)
			if err != nil {
				return nil, err
			}
			if f.matches(t, p.date1904) {
				mask.pass[v] = true
			}
		}
		if d := dims[column]; d != nil {
			prior := d.labelPass
			d.labelPass = make(map[int]bool, len(d.items))
			for i, it := range d.items {
				d.labelPass[i] = (prior == nil || prior[i]) && mask.pass[d.values[it.X]]
			}
		}
		masks = append(masks, mask)
	}
	return masks, nil
}
