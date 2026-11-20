package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"excellens/internal/editdoc"
)

// Column IDs belong to the original fields even when their physical offsets
// change. Keep the producer's whole XML element for every retained field.
func resizedColumns(t xlsxTable, next cellRect, headers map[int]editdoc.Value) ([]xlsxTableColumn, error) {
	old := t.bounds()
	maxID := uint64(0)
	used := map[string]bool{}
	for i, c := range t.Columns {
		id, _ := strconv.ParseUint(c.ID, 10, 32)
		maxID = max(maxID, id)
		if col := old.firstCol + i; col >= next.firstCol && col <= next.lastCol {
			used[strings.ToUpper(c.Name)] = true
		}
	}
	columns := make([]xlsxTableColumn, 0, next.lastCol-next.firstCol+1)
	blank := 1
	var previousDuplicate string
	for col := next.firstCol; col <= next.lastCol; col++ {
		if col >= old.firstCol && col <= old.lastCol {
			previousDuplicate = ""
			columns = append(columns, t.Columns[col-old.firstCol])
			continue
		}
		v := headers[col]
		name := v.Raw
		if v.Kind == "error" || v.Kind == "formula" {
			return nil, fmt.Errorf("请先将新增字段的标题改为文字")
		}
		if name == "" && previousDuplicate != "" {
			name = previousDuplicate
		}
		if name == "" {
			for {
				name = "列" + strconv.Itoa(blank)
				blank++
				if !used[strings.ToUpper(name)] {
					break
				}
			}
		}
		if len(utf16.Encode([]rune(name))) > 255 || xlsxTextEscapes.MatchString(name) {
			return nil, fmt.Errorf("表格字段标题过长或含有无法保存的转义")
		}
		base := name
		for suffix := 2; used[strings.ToUpper(name)]; suffix++ {
			name = base + strconv.Itoa(suffix)
		}
		previousDuplicate = ""
		if base != name {
			previousDuplicate = base
		}
		if len(utf16.Encode([]rune(name))) > 255 {
			return nil, fmt.Errorf("重复的表格字段标题过长")
		}
		used[strings.ToUpper(name)] = true
		maxID++
		if maxID > 1<<32-1 {
			return nil, ErrXLSX
		}
		columns = append(columns, xlsxTableColumn{Name: name, ID: strconv.FormatUint(maxID, 10)})
	}
	return columns, nil
}

func tableXMLArea(data []byte, e xmlEvent, r cellRect) ([]xmlPatch, error) {
	return setXMLAttribute(data, e.from, e.to, "ref", pivotRectRef(r))
}

func (p *xlsxPackage) resizeDefinition(ctx context.Context, t xlsxTable, next cellRect, columns []xlsxTableColumn) ([]byte, error) {
	raw, err := p.readPart(ctx, t.Part)
	if err != nil {
		return nil, err
	}
	_, markup, err := parseTable(ctx, raw, t.Sheet, t.Part, "")
	if err != nil {
		return nil, err
	}
	old := t.bounds()
	var patches []xmlPatch
	filter := next
	filter.lastRow -= t.Totals
	var cutDepth, cutStart, fieldsStart int
	err = walkXML(ctx, raw, func(e xmlEvent) error {
		if cutDepth != 0 {
			if _, ok := e.token.(xml.EndElement); ok && len(e.stack) == cutDepth {
				patches = append(patches, xmlPatch{cutStart, e.to, ""})
				cutDepth = 0
			}
			return nil
		}
		if e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		var more []xmlPatch
		var err error
		switch el := e.token.(type) {
		case xml.StartElement:
			switch e.local() {
			case "table":
				more, err = tableXMLArea(raw, e, next)
			case "autoFilter":
				more, err = tableXMLArea(raw, e, filter)
			case "filterColumn":
				id, ok := nonnegativeInt(attribute(el, "", "colId"))
				if !ok {
					return ErrXLSX
				}
				col := old.firstCol + id
				if col < next.firstCol || col > next.lastCol {
					cutDepth, cutStart = len(e.stack), e.from
				} else if old.firstCol != next.firstCol {
					more, err = setXMLAttribute(raw, e.from, e.to, "colId", strconv.Itoa(col-next.firstCol))
				}
			case "tableColumns":
				if old.colsSpan() != next.colsSpan() {
					fieldsStart = e.to
					more, err = setXMLAttribute(raw, e.from, e.to, "count", strconv.Itoa(len(columns)))
				}
			}
		case xml.EndElement:
			if e.local() == "tableColumns" && fieldsStart != 0 {
				var b strings.Builder
				for i, col := range columns {
					j := next.firstCol + i - old.firstCol
					if j >= 0 && j < len(markup) {
						node := markup[j].column
						b.Write(raw[node.from:node.to])
						continue
					}
					fmt.Fprintf(&b, `<tableColumn xmlns="%s" id="%s" name="%s"/>`, mainNS, col.ID, escapedXML(col.Name))
				}
				more = []xmlPatch{{fieldsStart, e.from, b.String()}}
			}
		}
		patches = append(patches, more...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, patches)
}
