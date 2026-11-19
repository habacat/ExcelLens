package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func (d *pivotDimension) itemsXML() string {
	var out strings.Builder
	subs := d.subtotalTypes()
	fmt.Fprintf(&out, `<items xmlns="%s" count="%d">`, mainNS, len(d.items)+len(subs))
	for _, it := range d.items {
		fmt.Fprintf(&out, `<item x="%d"`, it.X)
		if isTrue(it.Calculated) {
			out.WriteString(` f="1"`)
		}
		if isTrue(it.Hidden) {
			out.WriteString(` h="1"`)
		}
		if !pivotOn(it.Detail) {
			out.WriteString(` sd="0"`)
		}
		if it.Name != "" {
			fmt.Fprintf(&out, ` n="%s"`, escapedXML(it.Name))
		}
		out.WriteString("/>")
	}
	for _, typ := range subs {
		fmt.Fprintf(&out, `<item t="%s"/>`, escapedXML(typ))
	}
	out.WriteString("</items>")
	return out.String()
}
func pivotFieldsXML(tag string, indices []int) string {
	if len(indices) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, `<%s xmlns="%s" count="%d">`, tag, mainNS, len(indices))
	for _, i := range indices {
		fmt.Fprintf(&s, `<field x="%d"/>`, i)
	}
	fmt.Fprintf(&s, "</%s>", tag)
	return s.String()
}
func (p pivotPlan) axesXML(c pivotCube, rows, cols []pivotLine) string {
	var s strings.Builder
	s.WriteString(pivotFieldsXML("rowFields", p.rows))
	s.WriteString(axisItemsXML("rowItems", rows))
	s.WriteString(pivotFieldsXML("colFields", p.cols))
	s.WriteString(axisItemsXML("colItems", cols))
	if len(p.pages) > 0 {
		fmt.Fprintf(&s, `<pageFields xmlns="%s" count="%d">`, mainNS, len(p.pages))
		for _, i := range p.pages {
			fmt.Fprintf(&s, `<pageField fld="%d" hier="-1"`, i)
			if a := c.dims[i].pagePositions; len(a) == 1 {
				fmt.Fprintf(&s, ` item="%d"`, a[0])
			}
			s.WriteString("/>")
		}
		s.WriteString("</pageFields>")
	}
	return s.String()
}

func pivotRectRef(area cellRect) string {
	return fmt.Sprintf("%s%d:%s%d", workbook.ColumnName(area.firstCol), area.firstRow, workbook.ColumnName(area.lastCol), area.lastRow)
}

func (p pivotPlan) layoutXML(ctx context.Context, c pivotCube, rows, cols []pivotLine, area cellRect, dataRow, dataCol int, pg pivotPageGrid, caption string) ([]byte, error) {
	raw := p.xml
	var patches []xmlPatch
	set := func(e xmlEvent, name, value string) error {
		var ps []xmlPatch
		var err error
		if value == "" {
			ps, err = removeXMLAttributes(raw, e.from, e.to, name)
		} else {
			ps, err = setXMLAttribute(raw, e.from, e.to, name, escapedXML(value))
		}
		patches = append(patches, ps...)
		return err
	}
	field := -1
	var fieldStart xmlEvent
	var hasItems bool
	var itemsStart, section int
	wroteAxes := false
	axes := p.axesXML(c, rows, cols)
	insertAxes := func(at int) {
		if !wroteAxes {
			patches = append(patches, xmlPatch{at, at, axes})
			wroteAxes = true
		}
	}
	isAxis := func(n string) bool {
		switch n {
		case "rowFields", "rowItems", "colFields", "colItems", "pageFields":
			return true
		}
		return false
	}
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch e.token.(type) {
		case xml.StartElement:
			if e.parent() == "pivotTableDefinition" && isAxis(e.local()) {
				section = e.from
				insertAxes(e.from)
				return nil
			}
			switch e.local() {
			case "location":
				if e.parent() != "pivotTableDefinition" {
					return nil
				}
				ref := pivotRectRef(area)
				for _, a := range [][2]string{{"ref", ref}, {"firstHeaderRow", "1"}, {"firstDataRow", strconv.Itoa(dataRow)}, {"firstDataCol", strconv.Itoa(dataCol)}} {
					if err := set(e, a[0], a[1]); err != nil {
						return err
					}
				}
				for _, a := range []struct {
					name string
					n    int
				}{{"rowPageCount", pg.rows}, {"colPageCount", pg.cols}} {
					v := ""
					if a.n > 0 {
						v = strconv.Itoa(a.n)
					}
					if err := set(e, a.name, v); err != nil {
						return err
					}
				}
			case "pivotField":
				if e.parent() != "pivotFields" {
					return nil
				}
				field++
				fieldStart = e
				hasItems = false
				axis := ""
				switch {
				case slices.Contains(p.rows, field):
					axis = "axisRow"
				case slices.Contains(p.cols, field):
					axis = "axisCol"
				case slices.Contains(p.pages, field):
					axis = "axisPage"
				}
				if err := set(e, "axis", axis); err != nil {
					return err
				}
				measure := ""
				if slices.ContainsFunc(p.valueFilters, func(f pivotFilter) bool { return f.field == field }) {
					measure = "1"
				}
				if err := set(e, "measureFilter", measure); err != nil {
					return err
				}
				data := ""
				if field == p.data {
					data = "1"
				}
				if err := set(e, "dataField", data); err != nil {
					return err
				}
				multiple := ""
				if d := c.dims[field]; d != nil && d.page && len(d.pagePositions) > 1 {
					multiple = "1"
				}
				if err := set(e, "multipleItemSelectionAllowed", multiple); err != nil {
					return err
				}
			case "items":
				if e.parent() == "pivotField" {
					hasItems = true
					itemsStart = e.from
				}
			case "extLst":
				// items precedes per-field extensions in CT_PivotField.
				if e.parent() == "pivotField" && !hasItems && c.dims[field] != nil {
					patches = append(patches, xmlPatch{e.from, e.from, c.dims[field].itemsXML()})
					hasItems = true
				}
			case "dataFields":
				if e.parent() == "pivotTableDefinition" {
					insertAxes(e.from)
				}
			case "dataField":
				if e.parent() != "dataFields" {
					return nil
				}
				for _, a := range [][2]string{{"name", caption}, {"fld", strconv.Itoa(p.data)}, {"subtotal", p.aggregation}} {
					if err := set(e, a[0], a[1]); err != nil {
						return err
					}
				}
			}
		case xml.EndElement:
			if e.parent() == "pivotTableDefinition" && isAxis(e.local()) {
				patches = append(patches, xmlPatch{section, e.to, ""})
				return nil
			}
			switch e.local() {
			case "items":
				if e.parent() == "pivotField" && c.dims[field] != nil {
					patches = append(patches, xmlPatch{itemsStart, e.to, c.dims[field].itemsXML()})
				}
			case "pivotField":
				if e.parent() == "pivotFields" && c.dims[field] != nil && !hasItems {
					body := c.dims[field].itemsXML()
					if e.from == e.to {
						patches = append(patches, xmlPatch{fieldStart.to - 2, fieldStart.to, ">" + body + closingTag(raw[fieldStart.from:fieldStart.to])})
					} else {
						patches = append(patches, xmlPatch{e.from, e.from, body})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	data, err := applyXML(ctx, raw, patches)
	if err != nil {
		return nil, err
	}
	return p.writeValueFilters(ctx, data)
}

func (p *xlsxPackage) pivotSheet(ctx context.Context, sheet XLSXSheet, edits map[editdoc.Address]editdoc.Value, plans []pivotPlan) ([]byte, error) {
	addresses := make([]editdoc.Address, 0, len(edits))
	for a := range edits {
		addresses = append(addresses, a)
	}
	var t *sheetTargets
	var data []byte
	var window officeWindow
	var err error
	if p.layout.Native {
		window, err = p.officeWindow(ctx, sheet, addresses)
		if err == nil {
			t, data = window.targets, window.data
		}
	} else {
		t, data, err = p.findTargets(ctx, sheet, addresses, nil)
	}
	if err != nil {
		return nil, err
	}
	// A merge's anchor can be an editable blank. A PivotTable cannot occupy
	// any part of that merge, including the anchor by itself.
	if err := walkXML(ctx, data, func(e xmlEvent) error {
		start, ok := e.token.(xml.StartElement)
		if !ok || start.Name != (xml.Name{Space: mainNS, Local: "mergeCell"}) {
			return nil
		}
		area, ok := parseRect(attribute(start, "", "ref"))
		if !ok {
			return ErrXLSX
		}
		first, _ := slices.BinarySearchFunc(t.rows, area.firstRow, func(r rowTarget, n int) int { return r.row - n })
		for _, r := range t.rows[first:] {
			if r.row > area.lastRow {
				break
			}
			col, _ := slices.BinarySearchFunc(r.cells, area.firstCol, func(c cellTarget, n int) int { return c.address.Col - n })
			if col < len(r.cells) && r.cells[col].address.Col <= area.lastCol {
				return errors.New("透视结果将覆盖合并区域 " + pivotRectRef(area))
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, row := range t.rows {
		for i := range row.cells {
			c := &row.cells[i]
			a := c.address
			inside := false
			for _, plan := range plans {
				if plan.sheet == a.Sheet && plan.owns(a.Row, a.Col) {
					inside = true
					break
				}
			}
			if c.original.protected {
				return nil, errors.New("透视结果所在工作表受保护")
			}
			if inside && (c.original.readOnly == lockPivotResult || c.original.readOnly == lockPivotHeader || c.original.readOnly == lockPivotInput) {
				c.original.readOnly = ""
			}
			if !inside {
				if table, _ := p.layout.tableAt(a); table != nil {
					return nil, fmt.Errorf("透视结果将覆盖 Excel 表格 %s", table.Name)
				}
			}
			if !inside && (c.original.value.Kind != "blank" && c.original.value.Kind != "missing" || c.original.readOnly != "") {
				return nil, fmt.Errorf("透视结果将覆盖 %s!%s%d", a.Sheet, workbook.ColumnName(a.Col), a.Row)
			}
		}
	}
	ps, _, _, err := p.worksheetPatches(ctx, data, t, edits, &sheet, validateStoredDouble)
	if err != nil {
		return nil, err
	}
	if p.layout.Native {
		for i, patch := range ps {
			ps[i], err = window.rebase(patch)
			if err != nil {
				return nil, err
			}
		}
		data, err = p.readPart(ctx, sheet.Part)
		if err != nil {
			return nil, err
		}
	}
	return applyXML(ctx, data, ps)
}
