package editfile

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/workbook"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Cache indices identify typed source values. An axis item instead points to
// a position in its pivotField's own, possibly manually ordered, item list.
type pivotDimension struct {
	calendar      bool
	field         pivotFieldXML
	name          string
	values        []pivotValue
	items         []pivotItemXML
	positions     map[int]int
	labelPass     map[int]bool
	allowed       map[string]map[int]bool
	pagePositions []int
	page          bool
	customSort    bool
	date1904      bool
	dateStyle     string
	dateFormat    workbook.CalendarNumberFormat
}

func (p pivotPlan) dimensions(r pivotRecords) (map[int]*pivotDimension, error) {
	dims := map[int]*pivotDimension{}
	locale := language.SimplifiedChinese
	if p.table.DataCaption == "Values" {
		locale = language.English
	}
	collator := collate.New(locale, collate.IgnoreCase)
	for _, axis := range [][]int{p.rows, p.cols, p.pages} {
		for _, f := range axis {
			field := p.table.Fields[f]
			if x, ok := r.removedItems[f]; ok {
				field.Items = slices.DeleteFunc(slices.Clone(field.Items), func(it pivotItemXML) bool { return (it.Type == "" || it.Type == "data") && it.X == x })
				for i := range field.Items {
					it := &field.Items[i]
					if (it.Type == "" || it.Type == "data") && it.X > x {
						it.X--
					}
				}
			}
			d := &pivotDimension{customSort: pivotOn(p.table.CustomListSort), field: field, name: r.fields[f].Name, values: r.dicts[f].values, positions: map[int]int{}, page: slices.Contains(p.pages, f)}
			if remap, ok := r.datePositions[f]; ok {
				d.calendar = true
				d.field.Items = slices.Clone(field.Items)
				d.field.Items = slices.DeleteFunc(d.field.Items, func(it pivotItemXML) bool {
					_, found := remap[it.X]
					return (it.Type == "" || it.Type == "data") && !found
				})
				for i := range d.field.Items {
					it := &d.field.Items[i]
					if it.Type == "" || it.Type == "data" {
						it.X = remap[it.X]
					}
				}
			}
			if r.book != nil {
				d.date1904 = r.book.date1904
				d.dateStyle = r.dateStyles[f]
				d.dateFormat = r.book.pivotDateFormats[d.dateStyle]
			}
			if field.Name != "" {
				d.name = field.Name
			}
			if err := d.orderItems(collator); err != nil {
				return nil, err
			}
			for i := range d.items {
				if r.dicts[f].calculated[d.items[i].X] {
					d.items[i].Calculated = "1"
				}
			}
			if err := d.applySelection(p, r.dicts[f], f); err != nil {
				return nil, err
			}
			if err := p.matchLabels(f, d, collator); err != nil {
				return nil, err
			}
			dims[f] = d
		}
	}
	return dims, nil
}

func (d *pivotDimension) orderItems(c *collate.Collator) error {
	existing := map[int]bool{}
	for _, item := range d.field.Items {
		if item.Type != "" && item.Type != "data" {
			continue
		}
		if item.X < 0 || item.X >= len(d.values) || existing[item.X] {
			return ErrXLSX
		}
		existing[item.X] = true
		d.items = append(d.items, item)
	}
	// New fields are initially sorted. For a previously manual field Excel
	// appends newly discovered items in source order, even after old blanks.
	sortAll := len(d.items) == 0 || d.field.Sort == "ascending" || d.field.Sort == "descending"
	for i := range d.values {
		if !existing[i] {
			d.items = append(d.items, pivotItemXML{X: i})
		}
	}
	if d.calendar {
		slices.SortStableFunc(d.items, func(a, b pivotItemXML) int {
			order := cmp.Compare(a.X, b.X)
			if d.field.Sort == "descending" {
				return -order
			}
			return order
		})
	} else if sortAll {
		var custom map[string]int
		if d.customSort {
			custom = matchingPivotOrder(d.values)
		}
		slices.SortStableFunc(d.items, func(a, b pivotItemXML) int {
			v, w := d.values[a.X], d.values[b.X]
			order := comparePivotItems(c, v, w, d.date1904)
			if custom != nil && v.kind == "s" && w.kind == "s" {
				order = cmp.Compare(custom[strings.ToLower(v.raw)], custom[strings.ToLower(w.raw)])
			}
			if d.field.Sort == "descending" && v.kind != "m" && w.kind != "m" {
				return -order
			}
			return order
		})
	}
	for pos, it := range d.items {
		d.positions[it.X] = pos
	}
	return nil
}

// Excel's built-in month and weekday lists are used when a field first
// enters an axis. Already saved manual order takes precedence.
var pivotCustomOrders = func() []map[string]int {
	lists := []string{
		"jan feb mar apr may jun jul aug sep oct nov dec",
		"january february march april may june july august september october november december",
		"sun mon tue wed thu fri sat", "sunday monday tuesday wednesday thursday friday saturday",
		"一月 二月 三月 四月 五月 六月 七月 八月 九月 十月 十一月 十二月",
		"1月 2月 3月 4月 5月 6月 7月 8月 9月 10月 11月 12月",
		"星期日 星期一 星期二 星期三 星期四 星期五 星期六",
		"周日 周一 周二 周三 周四 周五 周六",
	}
	var result []map[string]int
	for _, words := range lists {
		order := map[string]int{}
		for i, v := range strings.Fields(words) {
			order[v] = i
		}
		result = append(result, order)
	}
	return result
}()

func matchingPivotOrder(values []pivotValue) map[string]int {
	for _, order := range pivotCustomOrders {
		if slices.ContainsFunc(values, func(v pivotValue) bool { _, ok := order[strings.ToLower(v.raw)]; return v.kind == "s" && !ok }) {
			continue
		}
		return order
	}
	return nil
}

func (v pivotValue) sortGroup() int {
	switch v.kind {
	case "n", "d":
		return 0
	case "s":
		return 1
	case "b":
		return 2
	case "e":
		return 3
	default:
		return 4
	}
}
func comparePivotItems(c *collate.Collator, a, b pivotValue, date1904 bool) int {
	if order := cmp.Compare(a.sortGroup(), b.sortGroup()); order != 0 {
		return order
	}
	if a.sortGroup() == 0 && (a.kind == "n" || b.kind == "n") {
		number := func(v pivotValue) float64 {
			if v.kind == "d" {
				n, _ := pivotDateSerial(v.raw, date1904)
				return n
			}
			n, _ := strconv.ParseFloat(v.raw, 64)
			return n
		}
		return cmp.Compare(number(a), number(b))
	}
	return c.CompareString(a.raw, b.raw)
}

func (d *pivotDimension) applySelection(p pivotPlan, dict pivotDictionary, f int) error {
	selected := map[pivotValue]bool{}
	for _, item := range p.selections[f] {
		v := pivotValue{item.Type, item.Value}
		if _, ok := dict.index[v]; !ok || selected[v] {
			return fmt.Errorf("分页字段 %s 的项目已变化或重复", d.name)
		}
		selected[v] = true
	}
	if len(selected) > 1 && len(selected) == len(d.items) {
		clear(selected)
	}
	rowFilter := len(p.rows) > 0 && p.rows[0] == f && !p.keepFilter
	wasPage := slices.ContainsFunc(p.table.PageFields, func(page pivotPageXML) bool { return page.Field == f })
	for pos := range d.items {
		it := &d.items[pos]
		v := d.values[it.X]
		switch {
		case d.page:
			it.Hidden = ""
			if selected[v] {
				d.pagePositions = append(d.pagePositions, pos)
			} else if len(selected) > 1 {
				it.Hidden = "1"
			}
		case rowFilter:
			it.Hidden = ""
			if p.filter != "" && p.filter != v.raw {
				it.Hidden = "1"
			}
		case wasPage:
			it.Hidden = ""
		}
	}
	return nil
}
func (d *pivotDimension) repeated() bool {
	for _, ext := range d.field.Repeat {
		if isTrue(ext.Labels) {
			return true
		}
	}
	return false
}
func (d *pivotDimension) subtotalTypes() []string {
	var types []string
	for _, it := range d.field.Items {
		if it.Type != "" && it.Type != "data" {
			types = append(types, it.Type)
		}
	}
	if len(types) == 0 && pivotOn(d.field.Subtotal) {
		return []string{"default"}
	}
	return types
}
