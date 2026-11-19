package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"strconv"
)

// filterColumn.colId is relative to the autofilter's old left edge. When
// deleting the last criterion Excel also releases its hidden data rows.
func (a axisEdit) filterColumns(ctx context.Context, data []byte) ([]byte, error) {
	var area cellRect
	var flags xmlEvent
	var ps, hidden []xmlPatch
	var removed, kept int
	skip := -1
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if t.Name.Space != mainNS {
				return nil
			}
			switch e.local() {
			case "sheetPr":
				flags = e
			case "autoFilter":
				area, _ = parseRect(attribute(t, "", "ref"))
			case "filterColumn":
				id, ok := nonnegativeInt(attribute(t, "", "colId"))
				if !ok {
					return ErrXLSX
				}
				col := a.point(area.firstCol + id)
				if col == 0 {
					skip = e.from
					removed++
					return nil
				}
				kept++
				left, _, err := a.interval(area.firstCol, area.lastCol)
				if err != nil {
					return err
				}
				next := col - left
				if next != id {
					part, err := setXMLAttribute(data, e.from, e.to, "colId", strconv.Itoa(next))
					if err != nil {
						return err
					}
					ps = append(ps, part...)
				}
			}
		case xml.EndElement:
			if e.local() == "filterColumn" && skip >= 0 {
				ps = append(ps, xmlPatch{skip, e.to, ""})
				skip = -1
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if removed > 0 {
		if kept > 0 {
			return nil, errors.New("删除筛选字段后需重新应用其余筛选条件")
		}
		if flags.to != 0 {
			drop, err := removeXMLAttributes(data, flags.from, flags.to, "filterMode")
			if err != nil {
				return nil, err
			}
			ps = append(ps, drop...)
		}
		err = walkXML(ctx, data, func(e xmlEvent) error {
			if t, ok := e.token.(xml.StartElement); ok && t.Name == (xml.Name{Space: mainNS, Local: "row"}) && isTrue(attribute(t, "", "hidden")) {
				row, _ := strconv.Atoi(attribute(t, "", "r"))
				if row > area.firstRow && row <= area.lastRow {
					drop, err := removeXMLAttributes(data, e.from, e.to, "hidden")
					if err != nil {
						return err
					}
					hidden = append(hidden, drop...)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		ps = append(ps, hidden...)
	}
	return applyXML(ctx, data, ps)
}
