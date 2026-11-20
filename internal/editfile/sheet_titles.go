package editfile

import (
	"context"
	"encoding/xml"
	"slices"
	"strconv"
	"strings"
)

// HeadingPairs partitions TitlesOfParts into groups. Match worksheet titles
// by their actual ordered names so localized heading labels remain intact.
func (a *sheetCollectionEdit) appTitles(ctx context.Context, raw []byte) ([]byte, error) {
	type field struct {
		value               string
		open, end, from, to int
	}
	var titles, groups []field
	var vector xmlEvent
	var fieldStart xmlEvent
	var content strings.Builder
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if len(e.stack) < 3 {
			return nil
		}
		category := e.stack[1].Local
		if category != "HeadingPairs" && category != "TitlesOfParts" {
			return nil
		}
		switch t := e.token.(type) {
		case xml.StartElement:
			if category == "TitlesOfParts" && e.local() == "vector" {
				vector = e
			}
			if e.local() == "lpstr" || e.local() == "lpwstr" || e.local() == "i4" {
				fieldStart = e
				content.Reset()
			}
		case xml.CharData:
			content.Write(t)
		case xml.EndElement:
			f := field{content.String(), fieldStart.from, e.to, fieldStart.to, e.from}
			if category == "HeadingPairs" && e.local() == "i4" {
				groups = append(groups, f)
			}
			if category == "TitlesOfParts" && (e.local() == "lpstr" || e.local() == "lpwstr") {
				titles = append(titles, f)
			}
		}
		return nil
	})
	if err != nil || len(titles) == 0 {
		return raw, err
	}
	sheetNames := make([]string, len(a.p.layout.Sheets))
	for i, s := range a.p.layout.Sheets {
		sheetNames[i] = s.Name
	}
	var ps []xmlPatch
	offset, total := 0, len(titles)
	for _, g := range groups {
		n, err := strconv.Atoi(g.value)
		if err != nil || n < 0 || n > len(titles)-offset {
			return raw, nil
		}
		items := titles[offset : offset+n]
		offset += n
		names := make([]string, n)
		for i, it := range items {
			names[i] = it.value
		}
		change := 0
		if slices.Equal(names, sheetNames) {
			cell := items[a.index]
			if a.op.Kind == "deleteSheet" {
				ps = append(ps, xmlPatch{cell.open, cell.end, ""})
				change--
			} else {
				child := string(raw[cell.open:cell.from]) + escapedXML(a.op.Name) + string(raw[cell.to:cell.end])
				ps = append(ps, xmlPatch{cell.end, cell.end, child})
				change++
			}
		} else if n > 0 {
			named := false
			for _, it := range items {
				v := strings.SplitN(it.value, "!", 2)
				if len(v) == 2 && slices.Contains(sheetNames, strings.Trim(v[0], "'")) {
					named = true
				}
				if _, ok := a.p.names[nameKey{"", strings.ToUpper(it.value)}]; ok {
					named = true
				}
				if a.op.Kind == "deleteSheet" && len(v) == 2 && (v[0] == a.op.Sheet || v[0] == "'"+strings.ReplaceAll(a.op.Sheet, "'", "''")+"'") {
					ps = append(ps, xmlPatch{it.open, it.end, ""})
					change--
				}
			}
			if named && a.op.Kind == "copySheet" && len(a.newTitles) > 0 {
				last := items[n-1]
				var body strings.Builder
				for _, name := range a.newTitles {
					body.Write(raw[last.open:last.from])
					body.WriteString(escapedXML(name))
					body.Write(raw[last.to:last.end])
					change++
				}
				ps = append(ps, xmlPatch{last.end, last.end, body.String()})
			}
		}
		if change != 0 {
			total += change
			ps = append(ps, xmlPatch{g.from, g.to, strconv.Itoa(n + change)})
		}
	}
	if offset != len(titles) || len(ps) == 0 {
		return raw, nil
	}
	extra, err := setXMLAttribute(raw, vector.from, vector.to, "size", strconv.Itoa(total))
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, append(ps, extra...))
}
