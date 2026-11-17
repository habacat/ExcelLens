package analysis

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

// preparedFilter eliminates repeated parsing of constants, lowercase
// conversion and map construction for every source record.
type preparedFilter struct {
	Filter
	lower                 string
	set                   map[string]struct{}
	first, second         *big.Rat
	dateFirst, dateSecond time.Time
	dateMode              bool
	timeOnly              bool
}

func compileFilters(filters []Filter) ([]preparedFilter, error) {
	out := make([]preparedFilter, 0, len(filters))
	for _, f := range filters {
		p := preparedFilter{Filter: f, lower: strings.ToLower(f.Value)}
		switch f.Op {
		case "in":
			p.set = make(map[string]struct{})
			// Use '|' or ',' to separate options. Empty is a real option.
			sep := ","
			if strings.Contains(f.Value, "|") {
				sep = "|"
			}
			for _, v := range strings.Split(f.Value, sep) {
				p.set[strings.TrimSpace(v)] = struct{}{}
			}
			if len(p.set) > 128 {
				return nil, fmt.Errorf("多选条件最多128项")
			}
		case "gt", "gte", "lt", "lte":
			if d, ok := Decimal(f.Value); ok {
				p.first = d
			} else if t, err := parseISODate(f.Value); err == nil {
				p.dateFirst = t
				p.dateMode = true
				p.timeOnly = t.Year() == 0
			} else {
				return nil, fmt.Errorf("范围条件需要有效数字、标准日期或时间：%s", f.Value)
			}
		case "between":
			parts := strings.SplitN(f.Value, "..", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("区间请使用 100..200 或 2026-01-01..2026-12-31")
			}
			lo, hi := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if first, ok := Decimal(lo); ok {
				second, yes := Decimal(hi)
				if !yes || first.Cmp(second) > 0 {
					return nil, fmt.Errorf("数字区间上下限无效")
				}
				p.first, p.second = first, second
			} else {
				l, e1 := parseISODate(lo)
				h, e2 := parseISODate(hi)
				if e1 != nil || e2 != nil || l.After(h) || (l.Year() == 0) != (h.Year() == 0) {
					return nil, fmt.Errorf("日期区间上下限无效")
				}
				p.dateFirst, p.dateSecond, p.dateMode = l, h, true
				p.timeOnly = l.Year() == 0
			}
		}
		out = append(out, p)
	}
	return out, nil
}
func parseISODate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04", "15:04:05.999999999", "15:04"} {
		if value, err := time.Parse(layout, s); err == nil {
			return value, nil
		}
	}
	return time.Time{}, fmt.Errorf("需要标准日期或时间")
}
func filterCompare(v Value, p preparedFilter) (int, bool) {
	if p.dateMode {
		for _, s := range []string{v.Text, v.Display} {
			t, err := parseISODate(s)
			if err == nil && (t.Year() == 0) == p.timeOnly {
				if t.Before(p.dateFirst) {
					return -1, true
				}
				if t.After(p.dateFirst) {
					return 1, true
				}
				return 0, true
			}
		}
		return 0, false
	}
	// Text identifiers with leading zeroes are not numeric evidence.
	if !v.Numeric && !IsNumericLiteral(v.Text) {
		return 0, false
	}
	a, ok := AccountingDecimal(v.Text)
	if !ok || p.first == nil {
		return 0, false
	}
	return a.Cmp(p.first), true
}
func matchCompiled(vals map[string]Value, filters []preparedFilter) bool {
	for _, f := range filters {
		v := vals[f.Field]
		a := v.Text
		b := f.Value
		var ok bool
		switch f.Op {
		case "eq":
			ok = a == b
		case "neq":
			ok = a != b
		case "blank":
			ok = a == ""
		case "notblank":
			ok = a != ""
		case "in":
			_, ok = f.set[a]
		case "contains":
			ok = strings.Contains(strings.ToLower(a), f.lower)
		case "notcontains":
			ok = !strings.Contains(strings.ToLower(a), f.lower)
		case "startswith":
			ok = strings.HasPrefix(strings.ToLower(a), f.lower)
		case "endswith":
			ok = strings.HasSuffix(strings.ToLower(a), f.lower)
		case "gt", "gte", "lt", "lte", "between":
			cmp, valid := filterCompare(v, f)
			if valid {
				switch f.Op {
				case "gt":
					ok = cmp > 0
				case "gte":
					ok = cmp >= 0
				case "lt":
					ok = cmp < 0
				case "lte":
					ok = cmp <= 0
				case "between":
					if f.dateMode {
						for _, s := range []string{v.Text, v.Display} {
							if date, err := parseISODate(s); err == nil && (date.Year() == 0) == f.timeOnly {
								ok = !date.Before(f.dateFirst) && !date.After(f.dateSecond)
								break
							}
						}
					} else if n, yes := AccountingDecimal(v.Text); yes {
						ok = cmp >= 0 && n.Cmp(f.second) <= 0
					}
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
