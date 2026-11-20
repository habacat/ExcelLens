package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"time"

	"excellens/internal/formats"
)

// Cache records contain exactly the database fields. Derived members exist
// only in the calculation view and in the cache definition's groupItems.
func (d pivotCalendarLayout) project(ctx context.Context, raw []byte, fields []cacheFieldXML, r *pivotRecords) ([]byte, error) {
	if len(d.levels) == 0 && d.replacement == nil {
		return raw, nil
	}
	if len(r.rows) > 0 && int64(len(r.rows))*int64(len(fields)+len(d.levels)) > formats.EditCalcSourceCells {
		return nil, ErrCalcCapacity
	}
	physical := r.dicts
	r.fields = slices.Clone(fields)
	r.datePositions = map[int]map[int]int{}
	r.measureSource = map[int]int{}
	r.dicts = append(slices.Clone(physical), make([]pivotDictionary, len(fields)-len(physical))...)
	r.dateStyles = append(r.dateStyles, make([]string, len(fields)-len(physical))...)
	type span struct {
		extent pivotDateExtent
		dates  map[pivotValue]time.Time
	}
	spans := map[int]span{}
	groups := map[int]string{}
	parent := map[int]int{}
	levels := map[int]dateBuckets{}
	for _, g := range d.levels {
		s, ok := spans[g.base]
		if !ok {
			s.dates = map[pivotValue]time.Time{}
			for i, row := range r.rows {
				if i%512 == 0 && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				v := row[g.base]
				if _, exists := s.dates[v]; exists {
					continue
				}
				if v.kind == "m" {
					continue
				}
				if v.kind != "d" {
					return nil, fmt.Errorf("字段 %s 含非日期值，无法按日期分组", fields[g.base].Name)
				}
				t, err := time.Parse(pivotDateLayout, v.raw)
				if err != nil {
					return nil, err
				}
				s.dates[v] = t
				s.extent.add(v.raw)
			}
			if s.extent.first == "" {
				return nil, errors.New("分组字段没有有效日期")
			}
			spans[g.base] = s
		}
		b, err := g.buckets(s.extent.first, s.extent.last, d.english)
		if err != nil {
			return nil, err
		}
		dict := pivotDictionary{index: map[pivotValue]int{}}
		for _, v := range b.values {
			dict.add(v)
		}
		if len(dict.values) != len(b.values) {
			return nil, errors.New("日期分组含重复标签")
		}
		remap := map[int]int{}
		for i, it := range g.old {
			if n, ok := dict.index[pivotValue{"s", it.V}]; ok {
				remap[i] = n
			}
		}
		if len(g.old) > 0 {
			remap[0] = 0
			remap[len(g.old)-1] = len(b.values) - 1
		}
		r.datePositions[g.field] = remap
		r.dicts[g.field] = dict
		if g.field == g.base {
			r.measureSource[g.base] = len(r.fields)
			r.fields = append(r.fields, fields[g.base])
			r.dicts = append(r.dicts, physical[g.base])
			r.dateStyles = append(r.dateStyles, r.dateStyles[g.base])
		} else {
			parent[g.base] = g.field
		}
		levels[g.field] = b
		groups[g.field] = g.xml(b)
	}
	for i, line := range r.rows {
		if i%512 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		row := make([]pivotValue, len(r.fields))
		copy(row, line)
		for _, g := range d.levels {
			b := levels[g.field]
			v := line[g.base]
			index := len(b.values) - 1
			if v.kind != "m" {
				index = b.index(spans[g.base].dates[v])
			}
			row[g.field] = b.values[index]
		}
		for base, j := range r.measureSource {
			row[j] = line[base]
		}
		r.rows[i] = row
	}
	for base, par := range parent {
		if groups[base] == "" {
			groups[base] = fmt.Sprintf(`<fieldGroup xmlns="%s" par="%d"/>`, mainNS, par)
		}
	}
	return d.writeGroups(ctx, raw, groups)
}

func (d pivotCalendarLayout) writeGroups(ctx context.Context, raw []byte, groups map[int]string) ([]byte, error) {
	var ps []xmlPatch
	index, start := -1, 0
	var field xmlEvent
	written := false
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch e.token.(type) {
		case xml.StartElement:
			if e.local() == "cacheField" && e.parent() == "cacheFields" {
				index++
				field = e
				written = false
				if index >= d.physical {
					v, err := setXMLAttribute(raw, e.from, e.to, "databaseField", "0")
					if err != nil {
						return err
					}
					ps = append(ps, v...)
				}
			}
			if e.local() == "fieldGroup" && e.parent() == "cacheField" {
				start = e.from
			}
			if e.local() == "extLst" && e.parent() == "cacheField" && !written && groups[index] != "" {
				ps = append(ps, xmlPatch{e.from, e.from, groups[index]})
				written = true
			}
		case xml.EndElement:
			if e.local() == "fieldGroup" && e.parent() == "cacheField" {
				ps = append(ps, xmlPatch{start, e.to, groups[index]})
				written = true
			}
			if e.local() == "cacheField" && !written && groups[index] != "" {
				if e.from == e.to {
					ps = append(ps, xmlPatch{field.to - 2, field.to, ">" + groups[index] + closingTag(raw[field.from:field.to])})
				} else {
					ps = append(ps, xmlPatch{e.from, e.from, groups[index]})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, ps)
}
