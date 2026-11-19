package editfile

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"excellens/internal/workbook"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Cache indices identify typed source values. An axis item instead points to
// a position in its pivotField's own, possibly manually ordered, item list.
type pivotDimension struct {
	field         pivotFieldXML
	name          string
	values        []pivotValue
	items         []pivotItemXML
	positions     map[int]int
	pagePositions []int
	page          bool
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
			d := &pivotDimension{field: field, name: r.fields[f].Name, values: r.dicts[f].values, positions: map[int]int{}, page: slices.Contains(p.pages, f)}
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
			if err := d.applySelection(p, r.dicts[f], f); err != nil {
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
	if sortAll {
		slices.SortStableFunc(d.items, func(a, b pivotItemXML) int {
			v, w := d.values[a.X], d.values[b.X]
			order := comparePivotItems(c, v, w, d.date1904)
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
