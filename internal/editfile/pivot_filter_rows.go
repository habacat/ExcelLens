package editfile

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strconv"

	"excellens/internal/formats"
)

// Members are identified in the dictionary of their own pivot field. An
// inner field is ranked separately for each path of outer members.
func filterPath(positions []int) string {
	var key []byte
	for _, pos := range positions {
		key = strconv.AppendInt(key, int64(pos), 10)
		key = append(key, '/')
	}
	return string(key)
}

type rankedMember struct {
	pos   int
	value float64
}

func (f pivotFilter) passing(group map[int]*pivotAggregate, method string) map[int]bool {
	pass := map[int]bool{}
	var ordered []rankedMember
	mode, top := rankKind(f.spec.Kind)
	total := 0.0
	for pos, agg := range group {
		v := agg.value(method)
		if v.Kind != "number" {
			continue
		}
		n, err := strconv.ParseFloat(v.Raw, 64)
		if err != nil {
			continue
		}
		if mode != "" {
			ordered = append(ordered, rankedMember{pos, n})
			continue
		}
		lo, hi := f.spec.Value, f.spec.Second
		ok := false
		switch f.spec.Kind {
		case "valueEqual":
			ok = n == lo
		case "valueNotEqual":
			ok = n != lo
		case "valueGreaterThan":
			ok = n > lo
		case "valueGreaterThanOrEqual":
			ok = n >= lo
		case "valueLessThan":
			ok = n < lo
		case "valueLessThanOrEqual":
			ok = n <= lo
		case "valueBetween":
			ok = n >= lo && n <= hi
		case "valueNotBetween":
			ok = n < lo || n > hi
		}
		if ok {
			pass[pos] = true
		}
	}
	if mode == "" || len(ordered) == 0 {
		return pass
	}
	slices.SortFunc(ordered, func(a, b rankedMember) int {
		n := cmp.Compare(a.value, b.value)
		if n == 0 {
			n = cmp.Compare(a.pos, b.pos)
		}
		if top {
			return -n
		}
		return n
	})
	if mode == "count" {
		cut := ordered[min(len(ordered), int(f.spec.Value))-1].value
		for _, m := range ordered {
			if top && m.value < cut || !top && m.value > cut {
				break
			}
			pass[m.pos] = true
		}
		return pass
	}
	limit := f.spec.Value
	if mode == "percent" {
		for _, member := range ordered {
			total += member.value
		}
		limit = total * limit / 100
	}
	sum := 0.0
	for _, m := range ordered {
		pass[m.pos] = true
		sum += m.value
		if sum >= limit {
			break
		}
	}
	return pass
}

func (p pivotPlan) filteredRows(ctx context.Context, r pivotRecords, dims map[int]*pivotDimension, registerAxes func([]pivotValue)) ([][]pivotValue, error) {
	dates, err := p.dateMasks(ctx, r, dims)
	if err != nil {
		return nil, err
	}
	rows := make([][]pivotValue, 0, len(r.rows))
	source, err := p.measureRecords(ctx, r, dims)
	if err != nil {
		return nil, err
	}
records:
	for _, record := range source {
		for _, mask := range dates {
			if !mask.pass[record[mask.column]] {
				continue records
			}
		}
		visible := true
		for f, d := range dims {
			if f == pivotValuesField {
				continue
			}
			pos := d.positions[r.dicts[f].index[record[f]]]
			if d.labelPass != nil && !d.labelPass[pos] || isTrue(d.items[pos].Hidden) || d.page && len(d.pagePositions) == 1 && pos != d.pagePositions[0] {
				visible = false
				break
			}
		}
		if visible {
			// Value filters hide members on their own axis. Visible members on
			// the opposite axis survive even when all their intersections are blank.
			if len(p.valueFilters) > 0 {
				registerAxes(record)
			}
			rows = append(rows, record)
		}
	}
	filters := slices.Clone(p.valueFilters)
	slices.SortStableFunc(filters, func(a, b pivotFilter) int { return cmp.Compare(a.order, b.order) })
	for _, filter := range filters {
		d := dims[filter.field]
		axis := p.rows
		at := slices.Index(axis, filter.field)
		if at < 0 {
			axis = p.cols
			at = slices.Index(axis, filter.field)
		}
		if d == nil || at < 0 {
			return nil, ErrXLSX
		}
		if int64(len(rows))*int64(at+1) > formats.EditCalcVisits {
			return nil, ErrCalcCapacity
		}
		type member struct {
			parent string
			pos    int
		}
		paths := make([]member, len(rows))
		groups := map[string]map[int]*pivotAggregate{}
		parent := make([]int, at)
		for i, rec := range rows {
			if i%512 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			for j, f := range axis[:at] {
				parent[j] = dims[f].positions[r.dicts[f].index[rec[f]]]
			}
			key := filterPath(parent)
			pos := d.positions[r.dicts[filter.field].index[rec[filter.field]]]
			paths[i] = member{key, pos}
			if groups[key] == nil {
				groups[key] = map[int]*pivotAggregate{}
			}
			a := groups[key][pos]
			if a == nil {
				a = &pivotAggregate{}
				groups[key][pos] = a
			}
			m := p.measureAt(filter.spec.Measure)
			value, err := measureValue(rec, r, m, filter.spec.Measure)
			if err != nil {
				return nil, err
			}
			a.addMeasure(value, len(rec) > len(r.fields))
		}
		d.allowed = make(map[string]map[int]bool, len(groups))
		for key, g := range groups {
			d.allowed[key] = filter.passing(g, p.measureAt(filter.spec.Measure).method())
		}
		next := rows[:0]
		for i, path := range paths {
			if d.allowed[path.parent][path.pos] {
				next = append(next, rows[i])
			}
		}
		rows = next
	}
	return rows, ctx.Err()
}

func pivotMeasure(value pivotValue, r pivotRecords) (pivotValue, error) {
	if value.kind != "d" {
		return value, nil
	}
	n, err := pivotDateSerial(value.raw, r.book.date1904)
	if err != nil || math.IsNaN(n) {
		return pivotValue{}, ErrXLSX
	}
	return pivotValue{"n", strconv.FormatFloat(n, 'g', -1, 64)}, nil
}
