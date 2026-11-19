package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Table definitions use physical column order. IDs identify fields, not column offsets.
type xlsxTable struct {
	Name     string            `json:"name"`
	Sheet    string            `json:"sheet"`
	Part     string            `json:"part"`
	Ref      string            `json:"ref"`
	Headers  int               `json:"headers"`
	Totals   int               `json:"totals"`
	Filtered bool              `json:"filtered,omitempty"`
	Columns  []xlsxTableColumn `json:"columns"`
}
type xlsxTableColumn struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Formula      string `json:"formula,omitempty"`
	TotalFormula string `json:"totalFormula,omitempty"`
	Function     string `json:"function,omitempty"`
	Label        string `json:"label,omitempty"`
	Array        bool   `json:"array,omitempty"`
	TotalArray   bool   `json:"totalArray,omitempty"`
}

func (t xlsxTable) bounds() cellRect { r, _ := parseRect(t.Ref); return r }
func (t xlsxTable) dataRows() (int, int) {
	r := t.bounds()
	return r.firstRow + t.Headers, r.lastRow - t.Totals
}
func (l XLSXLayout) tableAt(a editdoc.Address) (*xlsxTable, int) {
	for i := range l.Tables {
		t := &l.Tables[i]
		r := t.bounds()
		if t.Sheet == a.Sheet && r.contains(a.Row, a.Col) {
			return t, a.Col - r.firstCol
		}
	}
	return nil, 0
}
func (l XLSXLayout) tableLock(a editdoc.Address) string {
	t, col := l.tableAt(a)
	if t == nil {
		return ""
	}
	first, last := t.dataRows()
	switch {
	case a.Row < first:
		return "表格字段标题参与结构化引用"
	case a.Row > last && t.Columns[col].TotalArray, a.Row >= first && a.Row <= last && t.Columns[col].Array:
		return "表格列使用数组公式"
	}
	return ""
}

type tableElement struct{ from, openEnd, endStart, to int }
type columnMarkup struct {
	column, formula, total tableElement
	childStart             int
}

func parseTable(ctx context.Context, data []byte, sheet, part, rel string) (xlsxTable, []columnMarkup, error) {
	item, err := workbook.ParseTableDefinition(data, sheet, part, rel)
	if err != nil {
		return xlsxTable{}, nil, err
	}
	tab := xlsxTable{Name: item.Name, Sheet: sheet, Part: part, Ref: item.Table.Ref, Headers: item.Table.HeaderRows, Totals: item.Table.TotalRows}
	if err := ctx.Err(); err != nil {
		return tab, nil, err
	}
	r, ok := parseRect(tab.Ref)
	if !ok || r.lastCol > formats.ExcelColumns {
		return tab, nil, ErrXLSX
	}
	markup := make([]columnMarkup, len(item.Table.Columns))
	names := map[string]bool{}
	for _, c := range item.Table.Columns {
		name := strings.ToUpper(c.Name)
		if names[name] || xlsxTextEscapes.MatchString(c.Name) {
			return tab, nil, fmt.Errorf("%w：表格列名重复或含转义", ErrXLSX)
		}
		names[name] = true
		tab.Columns = append(tab.Columns, xlsxTableColumn{ID: c.ID, Name: c.Name})
	}
	col := -1
	var expr strings.Builder
	err = walkXML(ctx, data, func(e xmlEvent) error {
		name := e.stack[len(e.stack)-1]
		if len(e.stack) == 1 {
			if el, ok := e.token.(xml.StartElement); ok {
				if name != (xml.Name{Space: mainNS, Local: "table"}) {
					return ErrXLSX
				}
				typ := attribute(el, "", "tableType")
				if typ != "" && typ != "worksheet" || attribute(el, "", "connectionId") != "" {
					return fmt.Errorf("%w：连接或映射表格", ErrXLSX)
				}
			}
		}
		if len(e.stack) == 3 && e.parent() == "autoFilter" && e.local() == "filterColumn" {
			tab.Filtered = true
		}
		if len(e.stack) < 3 || e.stack[1] != (xml.Name{Space: mainNS, Local: "tableColumns"}) || e.stack[2] != (xml.Name{Space: mainNS, Local: "tableColumn"}) {
			return nil
		}
		if len(e.stack) == 3 {
			switch el := e.token.(type) {
			case xml.StartElement:
				col++
				if col >= len(markup) {
					return ErrXLSX
				}
				m := &markup[col]
				m.column = tableElement{from: e.from, openEnd: e.to}
				c := &tab.Columns[col]
				c.Function = attribute(el, "", "totalsRowFunction")
				c.Label = attribute(el, "", "totalsRowLabel")
				if attribute(el, "", "queryTableFieldId") != "" {
					return ErrXLSX
				}
			case xml.EndElement:
				markup[col].column.endStart, markup[col].column.to = e.from, e.to
			}
			return nil
		}
		if len(e.stack) != 4 {
			if e.stack[3].Local == "totalsRowFormula" || e.stack[3].Local == "calculatedColumnFormula" {
				return ErrXLSX
			}
			return nil
		}
		c, m := &tab.Columns[col], &markup[col]
		if _, ok := e.token.(xml.StartElement); ok && m.childStart == 0 {
			m.childStart = e.from
		}
		var elem *tableElement
		var raw *string
		var array *bool
		switch name {
		case xml.Name{Space: mainNS, Local: "calculatedColumnFormula"}:
			elem, raw, array = &m.formula, &c.Formula, &c.Array
		case xml.Name{Space: mainNS, Local: "totalsRowFormula"}:
			elem, raw, array = &m.total, &c.TotalFormula, &c.TotalArray
		case xml.Name{Space: mainNS, Local: "xmlColumnPr"}:
			return fmt.Errorf("%w：XML 映射列", ErrXLSX)
		default:
			return nil
		}
		switch el := e.token.(type) {
		case xml.StartElement:
			flag := attribute(el, "", "array")
			if elem.openEnd != 0 || !plainAttributes(el, "array") || flag != "" && !ruleBool(flag) {
				return ErrXLSX
			}
			*array = isTrue(flag)
			elem.from, elem.openEnd = e.from, e.to
			expr.Reset()
		case xml.CharData:
			if expr.Len()+len(el) >= formats.EditFormulaBytes {
				return ErrXLSX
			}
			expr.Write(el)
		case xml.EndElement:
			if expr.Len() == 0 {
				return ErrXLSX
			}
			*raw = "=" + expr.String()
			elem.endStart, elem.to = e.from, e.to
		}
		return nil
	})
	return tab, markup, err
}

func (p *xlsxPackage) loadTables(ctx context.Context) error {
	p.layout.Tables = nil
	for _, s := range p.layout.Sheets {
		var ids []string
		for id, part := range p.links[s.Part] {
			if tablePart.MatchString(part) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			part := p.links[s.Part][id]
			data, err := p.readPart(ctx, part)
			if err != nil {
				return err
			}
			table, _, err := parseTable(ctx, data, s.Name, part, id)
			if err != nil {
				return fmt.Errorf("%s：%w", part, err)
			}
			p.layout.Tables = append(p.layout.Tables, table)
		}
	}
	if !p.layout.validTables() {
		return fmt.Errorf("%w：表格名称、范围或列定义无效", ErrXLSX)
	}
	return nil
}

func (l XLSXLayout) validTables() bool {
	if len(l.Tables) == 0 {
		return true
	}
	if !l.Structured || len(l.Tables) > formats.EditOfficeParts {
		return false
	}
	names, parts := map[string]bool{}, map[string]bool{}
	areas := map[string][]cellRect{}
	for _, t := range l.Tables {
		key := strings.ToUpper(t.Name)
		r, ok := parseRect(t.Ref)
		if !ok || !l.HasSheet(t.Sheet) || !tablePart.MatchString(t.Part) || parts[t.Part] || key == "" || names[key] || t.Headers < 0 || t.Headers > 1 || t.Totals < 0 || t.Totals > 1 || r.lastCol > formats.ExcelColumns || r.lastCol-r.firstCol+1 != len(t.Columns) || r.lastRow-r.firstRow+1 < t.Headers+t.Totals {
			return false
		}
		parts[t.Part], names[key] = true, true
		ids, fields := map[string]bool{}, map[string]bool{}
		for _, col := range t.Columns {
			name := strings.ToUpper(col.Name)
			if id, err := strconv.ParseUint(col.ID, 10, 32); err != nil || id == 0 || ids[col.ID] || name == "" || fields[name] {
				return false
			}
			ids[col.ID], fields[name] = true, true
			for _, raw := range []string{col.Formula, col.TotalFormula} {
				if raw != "" && !(editdoc.Value{Kind: "formula", Raw: raw}).Valid() {
					return false
				}
			}
		}
		for _, other := range areas[t.Sheet] {
			if r.firstRow <= other.lastRow && other.firstRow <= r.lastRow && r.firstCol <= other.lastCol && other.firstCol <= r.lastCol {
				return false
			}
		}
		areas[t.Sheet] = append(areas[t.Sheet], r)
	}
	return true
}

// The coordinate-window strategy normally defers worksheet scans. Table
// formulas on other sheets also need proof of the sheet-to-table binding.
func (p *xlsxPackage) checkNativeTableBindings(ctx context.Context) error {
	sheets := map[string]bool{}
	for _, t := range p.layout.Tables {
		sheets[t.Sheet] = true
	}
	for _, sheet := range p.layout.Sheets {
		if !sheets[sheet.Name] {
			continue
		}
		var s worksheetStructure
		seen := map[string]bool{}
		err := officeWalk(ctx, p.files[sheet.Part], func(e xmlEvent) error {
			if len(e.stack) >= 2 && e.stack[1] == (xml.Name{Space: mainNS, Local: "tableParts"}) {
				return p.tablePartsElement(e, sheet, &s, seen)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for id, target := range p.links[sheet.Part] {
			if tablePart.MatchString(target) && !s.tableIDs[id] {
				return fmt.Errorf("%w：表格未绑定工作表", ErrXLSX)
			}
		}
	}
	return nil
}

func (p *xlsxPackage) tablePartsElement(e xmlEvent, sheet XLSXSheet, s *worksheetStructure, seen map[string]bool) error {
	switch el := e.token.(type) {
	case xml.StartElement:
		if len(e.stack) == 2 {
			if seen["tableParts"] || !plainAttributes(el, "count") {
				return ErrXLSX
			}
			seen["tableParts"] = true
			s.tableIDs = map[string]bool{}
			n := 0
			for _, part := range p.links[sheet.Part] {
				if tablePart.MatchString(part) {
					n++
				}
			}
			if n == 0 || attribute(el, "", "count") != "" && attribute(el, "", "count") != strconv.Itoa(n) {
				return fmt.Errorf("%w：工作表表格数量", ErrXLSX)
			}
			return nil
		}
		id := attribute(el, relationNS, "id")
		if len(e.stack) != 3 || el.Name != (xml.Name{Space: mainNS, Local: "tablePart"}) || !relationAttributes(el, "id") || !tablePart.MatchString(p.links[sheet.Part][id]) || s.tableIDs[id] {
			return fmt.Errorf("%w：工作表的表格关系无效", ErrXLSX)
		}
		s.tableIDs[id] = true
	case xml.CharData:
		if len(bytes.TrimSpace(el)) != 0 {
			return ErrXLSX
		}
	}
	return nil
}
