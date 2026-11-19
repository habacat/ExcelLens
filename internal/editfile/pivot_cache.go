package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// A pivot record either stores its typed value, or an index into the
// corresponding cacheField's sharedItems. Preserve old indices when extending
// a dictionary so persisted field and item references retain their meaning.
type pivotValue struct{ kind, raw string }

func (b *CalculationBook) pivotCell(v editdoc.Value) (pivotValue, error) {
	switch v.Kind {
	case "missing", "blank":
		return pivotValue{"m", ""}, nil
	case "text":
		return pivotValue{"s", v.Raw}, nil
	case "boolean":
		if v.Raw == "TRUE" {
			return pivotValue{"b", "1"}, nil
		}
		return pivotValue{"b", "0"}, nil
	case "number":
		n, err := strconv.ParseFloat(v.Raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return pivotValue{}, ErrXLSX
		}
		if _, ok := b.pivotDateFormats[v.Style]; ok {
			return b.pivotDate(n)
		}
		return pivotValue{"n", strconv.FormatFloat(n, 'g', -1, 64)}, nil
	case "date":
		t, err := editdoc.ISODateTime(v.Raw)
		if err != nil {
			return pivotValue{}, err
		}
		days := calculator.ExcelDateSerial(t, b.date1904)
		seconds := float64(t.Hour()*3600+t.Minute()*60+t.Second()) + float64(t.Nanosecond())/1e9
		return b.pivotDate(float64(days) + seconds/float64(secondsPerDay))
	case "error":
		return pivotValue{"e", v.Raw}, nil
	}
	return pivotValue{}, ErrXLSX
}

func (v pivotValue) write(out *strings.Builder) {
	if v.kind == "m" {
		out.WriteString("<m/>")
		return
	}
	fmt.Fprintf(out, `<%s v="%s"/>`, v.kind, escapedXML(v.raw))
}

type pivotDictionary struct {
	values     []pivotValue
	index      map[pivotValue]int
	calculated map[int]bool
}

func (d *pivotDictionary) add(v pivotValue) int {
	if n, ok := d.index[v]; ok {
		return n
	}
	n := len(d.values)
	d.index[v] = n
	d.values = append(d.values, v)
	return n
}

type pivotRecords struct {
	fields       []cacheFieldXML
	rows         [][]pivotValue
	dicts        []pivotDictionary
	removedItems map[int]int
	items        []pivotCalculated
	dateStyles   []string
	book         *CalculationBook
}

func loadPivotRecords(ctx context.Context, values objectValues, source pivotSource, fields []cacheFieldXML, dimensions map[int]bool) (pivotRecords, error) {
	r := effectivePivotSource(values, source.sheet, source.area)
	var records pivotRecords
	if !source.refreshable {
		return records, errors.New("工作簿禁用了此透视缓存的刷新")
	}
	if len(fields) == 0 || len(fields) != r.lastCol-r.firstCol+1 {
		return records, errors.New("缓存字段数与透视源列数不一致")
	}
	if (r.lastRow - r.firstRow + 1) > formats.EditCalcSourceCells/len(fields) {
		return records, errors.New("透视源超过单次刷新单元格上限")
	}
	records.fields = fields
	records.book = values.book
	records.dateStyles = make([]string, len(fields))
	records.dicts = make([]pivotDictionary, len(fields))
	for i, f := range fields {
		// A present but empty sharedItems element is also used by producers
		// to describe inline numeric records. Index only actual shared items.
		if dimensions[i] || f.Shared != nil && len(f.Shared.Items) > 0 {
			d := &records.dicts[i]
			d.index = map[pivotValue]int{}
			if f.Shared != nil {
				for _, old := range f.Shared.Items {
					v, e := cacheMember(old)
					if e != nil {
						return records, e
					}
					if _, ok := d.index[v]; ok {
						return records, errors.New("透视缓存包含重复的共享项目")
					}
					x := d.add(v)
					if isTrue(old.Calculated) {
						if d.calculated == nil {
							d.calculated = map[int]bool{}
						}
						d.calculated[x] = true
					}
				}
			}
		}
	}
	for row := r.firstRow + 1; row <= r.lastRow; row++ {
		if err := ctx.Err(); err != nil {
			return records, err
		}
		line := make([]pivotValue, len(fields))
		for i := range fields {
			v, err := values.cell(editdoc.Address{Sheet: source.sheet, Row: row, Col: r.firstCol + i})
			if err != nil {
				return records, err
			}
			line[i], err = values.book.pivotCell(v)
			if err != nil {
				return records, err
			}
			if line[i].kind == "d" && records.dateStyles[i] == "" {
				if _, ok := values.book.pivotDateFormats[v.Style]; ok {
					records.dateStyles[i] = v.Style
				}
			}
			if d := &records.dicts[i]; d.index != nil {
				if d.calculated[d.add(line[i])] {
					return records, fmt.Errorf("源字段 %s 的值与计算项目同名", fields[i].Name)
				}
			}
		}
		records.rows = append(records.rows, line)
	}
	return records, nil
}

func (r *pivotRecords) xmlRecords(ctx context.Context, original []byte) ([]byte, error) {
	var root xmlEvent
	first, last := -1, 0
	hasDefault := false
	err := walkXML(ctx, original, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 1 {
				if t.Name != (xml.Name{Space: mainNS, Local: "pivotCacheRecords"}) {
					return ErrXLSX
				}
				root = e
				hasDefault = attribute(t, "", "xmlns") == mainNS
			} else if len(e.stack) == 2 {
				if t.Name != (xml.Name{Space: mainNS, Local: "r"}) && t.Name != (xml.Name{Space: mainNS, Local: "extLst"}) {
					return ErrXLSX
				}
				if first < 0 {
					first = e.from
					last = e.from
				}
			}
		case xml.EndElement:
			if len(e.stack) == 2 && e.local() == "r" {
				last = e.to
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ps, err := setXMLAttribute(original, root.from, root.to, "count", strconv.Itoa(len(r.rows)))
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	for _, row := range r.rows {
		if hasDefault {
			out.WriteString("<r>")
		} else {
			fmt.Fprintf(&out, `<r xmlns="%s">`, mainNS)
		}
		for i, v := range row {
			if d := r.dicts[i]; d.index != nil {
				fmt.Fprintf(&out, `<x v="%d"/>`, d.index[v])
			} else {
				v.write(&out)
			}
		}
		out.WriteString("</r>")
	}
	if first < 0 {
		first, last = root.to, root.to
		if original[root.to-2] == '/' {
			first = root.to - 2
			out.WriteString(closingTag(original[root.from:root.to]))
			ps = append(ps, xmlPatch{first, first, ">"})
		}
	}
	ps = append(ps, xmlPatch{first, last, out.String()})
	return applyXML(ctx, original, ps)
}

func (r *pivotRecords) sharedItems(field int) string {
	d := r.dicts[field]
	values := slices.Values(d.values)
	if d.index == nil {
		// Inline fields also carry type flags and bounds. Base those on the
		// refreshed records without allocating a dictionary or changing <r>.
		values = func(yield func(pivotValue) bool) {
			for _, row := range r.rows {
				if !yield(row[field]) {
					return
				}
			}
		}
	}
	kinds := map[string]bool{}
	minN, maxN := math.Inf(1), math.Inf(-1)
	integers := true
	var minDate, maxDate string
	wholeDays := true
	for v := range values {
		kinds[v.kind] = true
		if v.kind == "d" {
			if minDate == "" || v.raw < minDate {
				minDate = v.raw
			}
			maxDate = max(maxDate, v.raw)
			wholeDays = wholeDays && strings.HasSuffix(v.raw, "T00:00:00")
		}
		if v.kind == "n" {
			n, _ := strconv.ParseFloat(v.raw, 64)
			minN, maxN = math.Min(minN, n), math.Max(maxN, n)
			integers = integers && math.Trunc(n) == n
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<sharedItems xmlns="%s"`, mainNS)
	if d.index != nil {
		fmt.Fprintf(&out, ` count="%d"`, len(d.values))
	}
	// Excel groups errors and Booleans with strings for these field flags.
	// Each shared item still retains its own SpreadsheetML value type.
	textGroup := kinds["s"] || kinds["b"] || kinds["e"]
	if !textGroup {
		out.WriteString(` containsString="0"`)
	}
	if kinds["m"] {
		out.WriteString(` containsBlank="1"`)
	}
	if kinds["d"] {
		if wholeDays {
			last, err := time.Parse(pivotDateLayout, maxDate)
			if err == nil && maxDate < "9999-12-31" {
				maxDate = last.AddDate(0, 0, 1).Format(pivotDateLayout)
			}
		}
		fmt.Fprintf(&out, ` containsDate="1" minDate="%s" maxDate="%s"`, minDate, maxDate)
	}
	if !textGroup && !kinds["n"] {
		out.WriteString(` containsNonDate="0"`)
	}
	if kinds["n"] && !kinds["d"] {
		fmt.Fprintf(&out, ` containsNumber="1" minValue="%g" maxValue="%g"`, minN, maxN)
		if integers {
			out.WriteString(` containsInteger="1"`)
		}
	}
	if !kinds["m"] && !textGroup {
		out.WriteString(` containsSemiMixedTypes="0"`)
	}
	if textGroup && (kinds["d"] || kinds["n"]) || kinds["d"] && kinds["n"] {
		out.WriteString(` containsMixedTypes="1"`)
	}
	out.WriteByte('>')
	for x, v := range d.values {
		if d.calculated[x] {
			fmt.Fprintf(&out, `<%s v="%s" f="1"/>`, v.kind, escapedXML(v.raw))
		} else {
			v.write(&out)
		}
	}
	out.WriteString("</sharedItems>")
	return out.String()
}

func (r *pivotRecords) definition(ctx context.Context, data []byte) ([]byte, error) {
	var ps []xmlPatch
	index, start := -1, 0
	var field xmlEvent
	var had bool
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch e.token.(type) {
		case xml.StartElement:
			switch e.local() {
			case "pivotCacheDefinition":
				for _, kv := range [][2]string{{"recordCount", strconv.Itoa(len(r.rows))}, {"invalid", "0"}, {"refreshOnLoad", "0"}} {
					v, err := setXMLAttribute(data, e.from, e.to, kv[0], kv[1])
					if err != nil {
						return err
					}
					ps = append(ps, v...)
				}
			case "cacheField":
				if e.parent() == "cacheFields" {
					index++
					field = e
					had = false
				}
			case "sharedItems":
				if e.parent() == "cacheField" {
					start = e.from
					had = true
				}
			case "extLst":
				if e.parent() == "cacheField" && !had && index >= 0 && index < len(r.dicts) {
					ps = append(ps, xmlPatch{e.from, e.from, r.sharedItems(index)})
					had = true
				}
			}
		case xml.EndElement:
			if index < 0 || index >= len(r.dicts) {
				return nil
			}
			if e.local() == "sharedItems" && e.parent() == "cacheField" {
				ps = append(ps, xmlPatch{start, e.to, r.sharedItems(index)})
			}
			if e.local() == "cacheField" && e.parent() == "cacheFields" && !had {
				body := r.sharedItems(index)
				if e.from == e.to && data[field.to-2] == '/' {
					ps = append(ps, xmlPatch{field.to - 2, field.to, ">" + body + closingTag(data[field.from:field.to])})
				} else {
					ps = append(ps, xmlPatch{e.from, e.from, body})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, data, ps)
}
