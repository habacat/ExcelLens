package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
)

// A saved cache can precede a change to its worksheet headers. Excel matches
// surviving fields by name (and disambiguates duplicate headers with 2, 3…).
// Indices on the worksheet, in the cache and in the layout need to change together.
type pivotSchema struct {
	names                []string
	oldForNew, newForOld []int
	changed              bool
}

func readPivotSchema(values objectValues, source pivotSource, previous []string) (pivotSchema, error) {
	r := source.area
	s := pivotSchema{newForOld: make([]int, len(previous))}
	old := map[string]int{}
	for i, name := range previous {
		s.newForOld[i] = -1
		old[strings.ToLower(name)] = i
	}
	seen := map[string]bool{}
	for c := r.firstCol; c <= r.lastCol; c++ {
		v, err := values.cell(editdoc.Address{Sheet: source.sheet, Row: r.firstRow, Col: c})
		if err != nil {
			return s, err
		}
		if v.Kind != "text" || v.Raw == "" {
			return s, errors.New("透视数据源需要非空文字列标题")
		}
		name := v.Raw
		key := strings.ToLower(name)
		for n := 2; seen[key]; n++ {
			name = v.Raw + strconv.Itoa(n)
			key = strings.ToLower(name)
		}
		seen[key] = true
		s.names = append(s.names, name)
		i, ok := old[key]
		if !ok {
			i = -1
		} else {
			s.newForOld[i] = len(s.names) - 1
		}
		s.oldForNew = append(s.oldForNew, i)
	}
	s.changed = !slices.Equal(previous, s.names)
	return s, nil
}
func (s pivotSchema) index(old int) (int, error) {
	if old < 0 || old >= len(s.newForOld) {
		return -1, ErrXLSX
	}
	return s.newForOld[old], nil
}

func (s pivotSchema) reorder(ctx context.Context, raw []byte, container, child string, replacement func(int, int, []byte) ([]byte, error)) ([]byte, error) {
	var children [][]byte
	var parent xmlEvent
	start, end := 0, 0
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 2 && e.local() == container {
				parent = e
			}
			if len(e.stack) == 3 && e.parent() == container && e.local() == child {
				start = e.from
			}
		case xml.EndElement:
			if len(e.stack) == 3 && e.parent() == container && e.local() == child {
				children = append(children, raw[start:e.to])
			}
			if len(e.stack) == 2 && e.local() == container {
				end = e.from
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(children) != len(s.newForOld) || parent.to == 0 || end < parent.to {
		return nil, ErrXLSX
	}
	ps, err := setXMLAttribute(raw, parent.from, parent.to, "count", strconv.Itoa(len(s.names)))
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	for i, old := range s.oldForNew {
		var orig []byte
		if old >= 0 {
			orig = children[old]
		}
		next, err := replacement(i, old, orig)
		if err != nil {
			return nil, err
		}
		body.Write(next)
	}
	ps = append(ps, xmlPatch{parent.to, end, body.String()})
	return applyXML(ctx, raw, ps)
}

func (s pivotSchema) cache(ctx context.Context, raw []byte, old []cacheFieldXML) ([]cacheFieldXML, []byte, error) {
	if !s.changed {
		return old, raw, nil
	}
	fields := make([]cacheFieldXML, len(s.names))
	next, err := s.reorder(ctx, raw, "cacheFields", "cacheField", func(i, k int, orig []byte) ([]byte, error) {
		if k >= 0 {
			fields[i] = old[k]
		}
		fields[i].Name = s.names[i]
		if k < 0 {
			return []byte(fmt.Sprintf(`<cacheField xmlns="%s" name="%s" numFmtId="0"/>`, mainNS, escapedXML(s.names[i]))), nil
		}
		if old[k].Name == s.names[i] {
			return orig, nil
		}
		end := bytes.IndexByte(orig, '>') + 1
		if end == 0 {
			return nil, ErrXLSX
		}
		ps, e := setXMLAttribute(orig, 0, end, "name", escapedXML(s.names[i]))
		if e != nil {
			return nil, e
		}
		return applyXML(ctx, orig, ps)
	})
	return fields, next, err
}

func (s pivotSchema) table(ctx context.Context, raw []byte) ([]byte, error) {
	if !s.changed {
		return raw, nil
	}
	raw, err := s.reorder(ctx, raw, "pivotFields", "pivotField", func(i, k int, orig []byte) ([]byte, error) {
		if k >= 0 {
			return orig, nil
		}
		return []byte(`<pivotField xmlns="` + mainNS + `" showAll="0"/>`), nil
	})
	if err != nil {
		return nil, err
	}
	var ps []xmlPatch
	var start int
	var drop bool
	err = walkXML(ctx, raw, func(e xmlEvent) error {
		t, isStart := e.token.(xml.StartElement)
		if !isStart {
			if _, end := e.token.(xml.EndElement); end && drop && len(e.stack) == 3 {
				ps = append(ps, xmlPatch{start, e.to, ""})
				drop = false
			}
			return nil
		}
		if t.Name.Space != mainNS {
			return nil
		}
		key := ""
		switch {
		case e.local() == "field" && (e.parent() == "rowFields" || e.parent() == "colFields"):
			key = "x"
		case e.local() == "pageField" && e.parent() == "pageFields", e.local() == "dataField" && e.parent() == "dataFields", e.local() == "filter" && e.parent() == "filters":
			key = "fld"
		case e.local() == "formats" || e.local() == "conditionalFormats" || e.local() == "chartFormats" || e.local() == "autoSortScope":
			return errors.New("透视源字段变化涉及专用格式或筛选，需在 Excel 中重绑")
		default:
			return nil
		}
		k, err := strconv.Atoi(attribute(t, "", key))
		if err != nil {
			return ErrXLSX
		}
		n, err := s.index(k)
		if err != nil {
			return err
		}
		if n < 0 {
			if e.local() == "filter" {
				return errors.New("透视源已移除值筛选字段，请先清除该筛选")
			}
			if e.local() == "dataField" {
				return errors.New("透视源已移除当前值字段")
			}
			start, drop = e.from, true
			return nil
		}
		move, err := setXMLAttribute(raw, e.from, e.to, key, strconv.Itoa(n))
		ps = append(ps, move...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, ps)
}

func (s pivotSchema) info(old PivotInfo) PivotInfo {
	if !s.changed || old.Axes == nil {
		return old
	}
	names := map[string]string{}
	for i, k := range s.oldForNew {
		if k >= 0 {
			names[old.Fields[k]] = s.names[i]
		}
	}
	info := old
	info.Fields = s.names
	axes := &editdoc.PivotAxes{}
	for _, f := range old.Axes.Rows {
		if n := names[f]; n != "" {
			axes.Rows = append(axes.Rows, n)
		}
	}
	for _, f := range old.Axes.Columns {
		if n := names[f]; n != "" {
			axes.Columns = append(axes.Columns, n)
		}
	}
	for _, page := range old.Axes.Pages {
		if n := names[page.Field]; n != "" {
			page.Field = n
			axes.Pages = append(axes.Pages, page)
		}
	}
	info.Data = names[info.Data]
	info.Axes = axes
	info.Row = ""
	if len(axes.Rows) > 0 {
		info.Row = axes.Rows[0]
	}
	if info.Data == "" {
		info.RefreshReason = "透视源已移除当前值字段"
	}
	return info
}

func (b *CalculationBook) CurrentPivotInfo(calc *Calculation, patches map[editdoc.Address]editdoc.Value, info PivotInfo) (PivotInfo, error) {
	r, ok := parseRect(info.SourceRef)
	if !ok {
		return info, ErrXLSX
	}
	s, err := readPivotSchema(objectValues{b, calc, patches}, pivotSource{sheet: info.SourceSheet, area: r}, info.Fields)
	if err != nil {
		return info, err
	}
	return s.info(info), nil
}
