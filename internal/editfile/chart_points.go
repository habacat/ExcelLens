package editfile

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"
)

// Keep the producer's cache attributes, extensions and per-point formatting.
// The generated cache supplies only the new count and indexed values.
func mergeChartPoints(ctx context.Context, source []byte, generated string) (string, error) {
	var fresh struct {
		Count struct {
			Value string `xml:"val,attr"`
		} `xml:"ptCount"`
		Points []struct {
			Index int    `xml:"idx,attr"`
			Value string `xml:"v"`
		} `xml:"pt"`
	}
	if err := xml.Unmarshal([]byte(generated), &fresh); err != nil {
		return "", err
	}
	type point struct {
		start, end, valueTag, valueFrom, valueTo int
		emptyValue                               bool
	}
	original := make(map[int]point)
	var ps []xmlPatch
	var first, last, insert, index int
	var current point
	var hasCount, empty bool
	err := walkXML(ctx, source, func(e xmlEvent) error {
		level := len(e.stack)
		switch t := e.token.(type) {
		case xml.StartElement:
			switch {
			case level == 2 && e.local() == "ptCount":
				hasCount = true
				patch, err := setXMLAttribute(source, e.from, e.to, "val", fresh.Count.Value)
				ps = append(ps, patch...)
				return err
			case level == 2 && e.local() == "pt":
				var ok bool
				index, ok = nonnegativeInt(attribute(t, "", "idx"))
				if _, exists := original[index]; !ok || exists {
					return ErrXLSX
				}
				current = point{start: e.from}
				if first == 0 {
					first = e.from
				}
			case level == 3 && e.local() == "v" && e.parent() == "pt":
				current.valueTag, current.valueFrom = e.from, e.to
			case level == 2 && e.local() == "extLst":
				insert = e.from
			}
		case xml.EndElement:
			switch {
			case level == 3 && e.local() == "v" && e.parent() == "pt":
				current.emptyValue = e.from == e.to
				current.valueTo = e.from
			case level == 2 && e.local() == "pt":
				current.end, last = e.to, e.to
				original[index] = current
			case level == 1:
				empty = e.from == e.to
				if insert == 0 {
					insert = e.from
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if first == 0 {
		first, last = insert, insert
	}
	var body strings.Builder
	if !hasCount {
		body.WriteString(`<ptCount xmlns="` + chartNS + `" val="` + fresh.Count.Value + `"/>`)
	}
	for _, p := range fresh.Points {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value := escapedXML(p.Value)
		if old, ok := original[p.Index]; ok && old.valueFrom != 0 {
			if old.emptyValue {
				body.Write(source[old.start : old.valueFrom-2])
				body.WriteString(">" + value + closingTag(source[old.valueTag:old.valueFrom]))
			} else {
				body.Write(source[old.start:old.valueFrom])
				body.WriteString(value)
			}
			body.Write(source[old.valueTo:old.end])
		} else {
			body.WriteString(`<pt xmlns="` + chartNS + `" idx="` + strconv.Itoa(p.Index) + `"><v>` + value + `</v></pt>`)
		}
	}
	if empty {
		return string(source[:len(source)-2]) + ">" + body.String() + closingTag(source), nil
	}
	ps = append(ps, xmlPatch{first, last, body.String()})
	data, err := applyXML(ctx, source, ps)
	return string(data), err
}
