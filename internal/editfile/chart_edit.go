package editfile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

const chartNS = "http://schemas.openxmlformats.org/drawingml/2006/chart"

type objectValues struct {
	book        *CalculationBook
	calculation *Calculation
	overlay     map[editdoc.Address]editdoc.Value
}

func newObjectValues(ctx context.Context, src *workbook.EditSourceFile, layout XLSXLayout) (objectValues, error) {
	b, err := NewCalculationBook(ctx, src, layout)
	if err != nil {
		return objectValues{}, err
	}
	c, err := b.Calculate(ctx, nil, nil)
	return objectValues{book: b, calculation: c}, err
}

func (v objectValues) cell(address editdoc.Address) (editdoc.Value, error) {
	if derived, ok := v.calculation.ArrayMembers[address]; ok {
		return derived, nil
	}
	c, ok := v.book.cells[address]
	if !ok {
		c.value = editdoc.Value{Kind: "blank"}
	}
	if patch, ok := v.overlay[address]; ok {
		c.value = patch.Apply(c.value)
	}
	if c.value.Kind != "formula" {
		return c.value, nil
	}
	state, result := v.calculation.FormulaResult(address, SourceCell{Value: c.value, Cache: c.cache})
	if result == nil {
		return editdoc.Value{}, fmt.Errorf("%s!%s%d 的公式尚未得到结果（%s）", address.Sheet, workbook.ColumnName(address.Col), address.Row, state)
	}
	value := *result
	value.Style = c.value.Style
	return value, nil
}

func replaceParts(ctx context.Context, dst io.Writer, src *zip.Reader, parts map[string][]byte) (opc.Receipt, error) {
	return opc.Rewrite(ctx, dst, src, func(f *zip.File) (opc.Change, error) {
		data, ok := parts[f.Name]
		if !ok {
			return opc.Change{}, nil
		}
		return opc.Change{Write: func(_ io.Reader, w io.Writer) error { _, err := w.Write(data); return err }}, nil
	})
}

func (p *xlsxPackage) chartPartsFor(sheet string) []string {
	var parts []string
	for _, s := range p.layout.Sheets {
		if s.Name != sheet {
			continue
		}
		for _, drawing := range p.links[s.Part] {
			if classifyPart(drawing).role != "drawing" {
				continue
			}
			for _, part := range p.links[drawing] {
				if classifyPart(part).role == "chart" {
					parts = append(parts, part)
				}
			}
		}
	}
	return parts
}

func (p *xlsxPackage) refreshCharts(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, sheet string) (opc.Receipt, error) {
	parts := p.chartPartsFor(sheet)
	if len(parts) == 0 {
		return opc.Receipt{}, errors.New("此工作表没有关联图表")
	}
	var pivotTargets []string
	for _, part := range parts {
		raw, err := p.readPart(ctx, part)
		if err != nil {
			return opc.Receipt{}, err
		}
		key, pivot, err := chartPivotSource(ctx, raw)
		if err != nil {
			return opc.Receipt{}, err
		}
		if !pivot {
			continue
		}
		index := slices.IndexFunc(p.layout.Pivots, func(info PivotInfo) bool { return chartKey(info.Sheet, info.Name) == key })
		if index < 0 {
			return opc.Receipt{}, fmt.Errorf("无法在本簿定位透视图表来源")
		}
		target := p.layout.Pivots[index].Part
		if !slices.Contains(pivotTargets, target) {
			pivotTargets = append(pivotTargets, target)
		}
	}
	values, err := newObjectValues(ctx, src, p.layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	if len(pivotTargets) > 0 {
		return p.refreshCacheRoots(ctx, dst, pivotTargets, nil, values, parts)
	}
	updates := map[string][]byte{}
	for _, part := range parts {
		data, err := p.readPart(ctx, part)
		if err != nil {
			return opc.Receipt{}, err
		}
		out, err := p.chartCaches(ctx, data, values)
		if err != nil {
			return opc.Receipt{}, fmt.Errorf("%s：%w", part, err)
		}
		if !bytes.Equal(out, data) {
			updates[part] = out
		}
	}
	return replaceParts(ctx, dst, p.zip, updates)
}

func (p *xlsxPackage) chartCaches(ctx context.Context, data []byte, values objectValues) ([]byte, error) {
	return p.updateChartCaches(ctx, data, values, nil)
}

// A pivot refresh visits formula and chart dependents. Only a series that
// reads a changed coordinate needs a new cache; other series retain their XML.
func (p *xlsxPackage) updateChartCaches(ctx context.Context, data []byte, values objectValues, affected map[string][]editdoc.Address) ([]byte, error) {
	var patches []xmlPatch
	var ref, formula, format string
	var depth, from, to int
	err := walkXML(ctx, data, func(e xmlEvent) error {
		name := e.stack[len(e.stack)-1]
		if name.Space != chartNS {
			return nil
		}
		local := name.Local
		switch t := e.token.(type) {
		case xml.StartElement:
			switch local {
			case "externalData", "multiLvlStrRef", "pivotSource":
				return fmt.Errorf("图表的 %s 数据来源还不能按固定序列重建", local)
			case "numRef", "strRef":
				ref = local
				depth = len(e.stack)
				formula = ""
				format = ""
				from, to = 0, 0
			case "numCache", "strCache":
				if depth > 0 && len(e.stack) == depth+1 {
					from = e.from
				}
			}
		case xml.CharData:
			if depth > 0 && local == "f" && len(e.stack) == depth+1 {
				formula += string(t)
			}
			if depth > 0 && local == "formatCode" {
				format += string(t)
			}
		case xml.EndElement:
			if depth > 0 && (local == "numCache" || local == "strCache") && len(e.stack) == depth+1 {
				to = e.to
			}
			if depth > 0 && local == ref && len(e.stack) == depth {
				if from == 0 {
					from, to = e.from, e.from
				}
				sheet, area, err := p.chartRange(formula)
				if err != nil {
					return err
				}
				if affected != nil && !slices.ContainsFunc(affected[sheet], func(a editdoc.Address) bool { return area.contains(a.Row, a.Col) }) {
					depth = 0
					return nil
				}
				body, err := chartCache(ref, sheet, area, format, values)
				if err != nil {
					return err
				}
				if from != to {
					body, err = mergeChartPoints(ctx, data[from:to], body)
					if err != nil {
						return err
					}
				}
				patches = append(patches, xmlPatch{from, to, body})
				depth = 0
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, data, patches)
}

func (p *xlsxPackage) chartRange(formula string) (string, cellRect, error) {
	i := strings.LastIndexByte(formula, '!')
	if i < 1 {
		return "", cellRect{}, fmt.Errorf("图表序列不是本簿固定区域：%s", formula)
	}
	sheet := formula[:i]
	if len(sheet) > 1 && sheet[0] == '\'' && sheet[len(sheet)-1] == '\'' {
		sheet = strings.ReplaceAll(sheet[1:len(sheet)-1], "''", "'")
	}
	for _, s := range p.layout.Sheets {
		if strings.EqualFold(s.Name, sheet) {
			sheet = s.Name
			break
		}
	}
	r, ok := parseRect(strings.ToUpper(strings.ReplaceAll(formula[i+1:], "$", "")))
	if !ok || !p.layout.HasSheet(sheet) || r.firstCol != r.lastCol && r.firstRow != r.lastRow {
		return "", cellRect{}, fmt.Errorf("图表序列不是本簿单行或单列区域：%s", formula)
	}
	return sheet, r, nil
}

func chartCache(ref, sheet string, r cellRect, format string, values objectValues) (string, error) {
	count := max(r.lastCol-r.firstCol, r.lastRow-r.firstRow) + 1
	if count > formats.EditCalcSourceCells {
		return "", ErrCalcCapacity
	}
	var out strings.Builder
	tag := strings.TrimSuffix(ref, "Ref") + "Cache"
	fmt.Fprintf(&out, `<%s xmlns="%s">`, tag, chartNS)
	if ref == "numRef" && format != "" {
		fmt.Fprintf(&out, "<formatCode>%s</formatCode>", escapedXML(format))
	}
	fmt.Fprintf(&out, `<ptCount val="%d"/>`, count)
	for k := 0; k < count; k++ {
		a := editdoc.Address{Sheet: sheet, Row: min(r.firstRow+k, r.lastRow), Col: min(r.firstCol+k, r.lastCol)}
		v, err := values.cell(a)
		if err != nil {
			return "", err
		}
		if v.Kind == "blank" || v.Kind == "missing" {
			continue
		}
		if ref == "numRef" && v.Kind != "number" {
			if v.Kind == "error" || v.Kind == "text" {
				continue
			}
			return "", fmt.Errorf("图表数值序列含 %s", v.Kind)
		}
		if ref == "strRef" && v.Kind != "text" {
			return "", fmt.Errorf("图表文本序列含需按格式显示的 %s", v.Kind)
		}
		fmt.Fprintf(&out, `<pt idx="%s"><v>%s</v></pt>`, strconv.Itoa(k), escapedXML(v.Raw))
	}
	fmt.Fprintf(&out, "</%s>", tag)
	return out.String(), nil
}
