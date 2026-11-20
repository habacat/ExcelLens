package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// IDs are shared by value, label and date filters. Existing IDs can be sparse
// after native filters are deleted. Allocate only when a new filter is applied.
func (t pivotTableXML) nextFilterID() uint32 {
	var highest uint32
	for _, f := range t.Filters {
		highest = max(highest, f.ID)
	}
	if highest < ^uint32(0) {
		return highest + 1
	}
	used := make(map[uint32]bool, len(t.Filters))
	for _, f := range t.Filters {
		used[f.ID] = true
	}
	var id uint32
	for used[id] {
		id++
	}
	return id
}

// Keep Excel's filter names, descriptions, extension attributes and saved
// evaluation order. A refresh changes only the cached relative-date bounds or
// a remapped measure. New criteria are appended in their assigned ID order.
func (p pivotPlan) patchFilters(ctx context.Context, raw []byte) ([]byte, error) {
	type entry struct {
		body    string
		measure *int
		date    *pivotDateFilter
	}
	current := map[uint32]entry{}
	for i := range p.valueFilters {
		f := &p.valueFilters[i]
		current[f.id] = entry{body: f.xml(), measure: &f.spec.Measure}
	}
	for _, f := range p.labelFilters {
		current[f.id] = entry{body: f.xml()}
	}
	for i := range p.dates {
		f := &p.dates[i]
		current[f.id] = entry{body: f.xml(), date: f}
	}
	count := len(current)
	if count != len(p.valueFilters)+len(p.labelFilters)+len(p.dates) {
		return nil, ErrXLSX
	}
	following := []string{"rowHierarchiesUsage", "colHierarchiesUsage", "extLst"}
	if count == 0 {
		return replacePivotChild(ctx, raw, "filters", following, "")
	}
	newXML := func(values bool) string {
		var out strings.Builder
		for _, id := range slices.Sorted(maps.Keys(current)) {
			if (current[id].measure != nil) != values {
				continue
			}
			out.WriteString(strings.Replace(current[id].body, "<filter ", `<filter xmlns="`+mainNS+`" `, 1))
		}
		return out.String()
	}
	if len(p.table.Filters) == 0 {
		body := fmt.Sprintf(`<filters xmlns="%s" count="%d">%s%s</filters>`, mainNS, count, newXML(false), newXML(true))
		return replacePivotChild(ctx, raw, "filters", following, body)
	}
	var patches []xmlPatch
	var active entry
	var id uint32
	var start, firstChild int
	set := func(e xmlEvent, name, value string) error {
		tag := e.token.(xml.StartElement)
		if attribute(tag, "", name) == value {
			return nil
		}
		ps, err := setXMLAttribute(raw, e.from, e.to, name, value)
		patches = append(patches, ps...)
		return err
	}
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if len(e.stack) < 2 || e.stack[1] != (xml.Name{Space: mainNS, Local: "filters"}) {
			return nil
		}
		switch tag := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 2 {
				firstChild = e.to
				return set(e, "count", strconv.Itoa(count))
			}
			if len(e.stack) == 3 && e.local() == "filter" {
				n, err := strconv.ParseUint(attribute(tag, "", "id"), 10, 32)
				if err != nil {
					return ErrXLSX
				}
				id, start = uint32(n), e.from
				active = current[id]
				if active.measure != nil {
					return set(e, "iMeasureFld", strconv.Itoa(*active.measure))
				}
			}
			if e.local() == "dynamicFilter" && len(e.stack) == 6 && active.date != nil {
				d := active.date
				if m, q := datePeriod(d.spec.Kind); m == 0 && q == 0 {
					for _, b := range []struct {
						name  string
						value float64
					}{{"val", d.low}, {"maxVal", d.high}} {
						if n, err := strconv.ParseFloat(attribute(tag, "", b.name), 64); err == nil && n == b.value {
							continue
						}
						if err := set(e, b.name, strconv.FormatFloat(b.value, 'f', -1, 64)); err != nil {
							return err
						}
					}
				}
			}
		case xml.EndElement:
			if len(e.stack) == 3 && e.local() == "filter" {
				if active.body == "" {
					patches = append(patches, xmlPatch{start, e.to, ""})
				}
				delete(current, id)
			}
			if len(e.stack) == 2 && len(current) > 0 {
				// Excel places the newest date or label condition first, and
				// evaluates value filters after the visible members are selected.
				if v := newXML(false); v != "" {
					patches = append(patches, xmlPatch{firstChild, firstChild, v})
				}
				if v := newXML(true); v != "" {
					patches = append(patches, xmlPatch{e.from, e.from, v})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, patches)
}
