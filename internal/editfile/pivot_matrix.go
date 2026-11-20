package editfile

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

type pivotCanvas struct {
	sheet string
	cells map[editdoc.Address]editdoc.Value
}

func (g pivotCanvas) set(row, col int, v editdoc.Value) {
	g.cells[editdoc.Address{Sheet: g.sheet, Row: row, Col: col}] = v
}
func (g pivotCanvas) text(row, col int, s string) {
	g.set(row, col, editdoc.Value{Kind: "text", Raw: s})
}

type pivotCaptions struct{ row, col, grand, all, multiple, blank, total string }

func (p pivotPlan) captionsForLanguage() pivotCaptions {
	c := pivotCaptions{"行标签", "列标签", "总计", "(全部)", "(多项)", "(空白)", "汇总"}
	if p.table.DataCaption == "Values" {
		c = pivotCaptions{"Row Labels", "Column Labels", "Grand Total", "(All)", "(Multiple Items)", "(blank)", "Total"}
	}
	return c
}
func (p pivotPlan) captions(v objectValues) pivotCaptions {
	c := p.captionsForLanguage()
	if p.table.RowCaption != "" {
		c.row = p.table.RowCaption
	}
	if p.table.ColCaption != "" {
		c.col = p.table.ColCaption
	}
	if p.table.GrandCaption != "" {
		c.grand = p.table.GrandCaption
	} else if len(p.table.DataFields) == 1 && len(p.table.RowFields) > 0 && pivotOn(p.table.ColGrand) {
		if old, err := v.cell(editdoc.Address{Sheet: p.sheet, Row: p.old.lastRow, Col: p.old.firstCol}); err == nil && old.Kind == "text" {
			c.grand = old.Raw
		}
	}
	return c
}
func (d *pivotDimension) label(pos int, c pivotCaptions) (editdoc.Value, error) {
	item := d.items[pos]
	v := d.values[item.X]
	if item.Name != "" {
		return editdoc.Value{Kind: "text", Raw: item.Name}, nil
	}
	switch v.kind {
	case "s":
		return editdoc.Value{Kind: "text", Raw: v.raw}, nil
	case "m":
		return editdoc.Value{Kind: "text", Raw: c.blank}, nil
	case "n":
		return editdoc.Value{Kind: "number", Raw: v.raw}, nil
	case "e":
		return editdoc.Value{Kind: "text", Raw: v.raw}, nil
	case "b":
		if v.raw == "1" || v.raw == "true" {
			return editdoc.Value{Kind: "text", Raw: "TRUE"}, nil
		}
		return editdoc.Value{Kind: "text", Raw: "FALSE"}, nil
	case "d":
		n, err := pivotDateSerial(v.raw, d.date1904)
		if err != nil {
			return editdoc.Value{}, err
		}
		return editdoc.Value{Kind: "number", Raw: strconv.FormatFloat(n, 'g', -1, 64), Style: d.dateStyle}, nil
	default:
		return editdoc.Value{}, ErrXLSX
	}
}
func (d *pivotDimension) subtotalLabel(name, kind string, c pivotCaptions) string {
	if s := d.field.Caption; s != "" {
		return strings.ReplaceAll(s, "*", name)
	}
	if kind == "default" {
		return name + " " + c.total
	}
	return name + " " + pivotMethodName(kind, c.total == "Total")
}
func pivotMethodName(method string, english bool) string {
	switch method {
	case "stdDevp":
		method = "stdDevP"
	case "varp":
		method = "varP"
	case "countNums":
		if english {
			return "Count Numbers"
		}
		return "数值计数"
	}
	if english {
		return map[string]string{"sum": "Sum", "count": "Count", "countA": "Count", "average": "Average", "avg": "Average", "min": "Min", "max": "Max", "product": "Product", "stdDev": "StdDev", "stdDevP": "StdDevp", "var": "Var", "varP": "Varp"}[method]
	}
	return map[string]string{"sum": "求和", "count": "计数", "countA": "计数", "average": "平均值", "avg": "平均值", "min": "最小值", "max": "最大值", "product": "乘积", "stdDev": "标准偏差", "stdDevP": "总体标准偏差", "var": "方差", "varP": "总体方差"}[method]
}
func (p pivotPlan) dataCaption(r pivotRecords) string {
	if len(p.measures) > 0 {
		return p.measures[0].Name
	}
	d := p.table.DataFields[0]
	method := d.Subtotal
	if method == "" {
		method = "sum"
	}
	if d.Fld == p.data && method == p.aggregation {
		return d.Name
	}
	if p.table.DataCaption == "Values" {
		return pivotMethodName(p.aggregation, true) + " of " + r.fields[p.data].Name
	}
	return pivotMethodName(p.aggregation, false) + "项:" + r.fields[p.data].Name
}

// Each row field owns a label column. Compact parents share that column with
// their child. Row layout can also be mixed per field in a saved pivot.
func (c pivotCube) rowOffsets(axis []int) []int {
	out := make([]int, len(axis))
	for i := 1; i < len(axis); i++ {
		out[i] = out[i-1]
		if !pivotOn(c.dims[axis[i-1]].field.Compact) || !pivotOn(c.dims[axis[i-1]].field.Outline) {
			out[i]++
		}
	}
	return out
}

func (p pivotPlan) render(ctx context.Context, r pivotRecords, values objectValues) ([]byte, map[editdoc.Address]editdoc.Value, error) {
	c, err := p.cube(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	p.rows, p.cols = p.displayAxes()
	multi := p.valuesAxis != nil
	valuesOnlyCol := multi && slices.Equal(p.cols, []int{pivotValuesField})
	rows, err := c.lines(ctx, c.rowRoot, p.rows, true, pivotOn(p.table.ColGrand))
	if err != nil {
		return nil, nil, err
	}
	cols, err := c.lines(ctx, c.colRoot, p.cols, false, pivotOn(p.table.RowGrand))
	if err != nil {
		return nil, nil, err
	}
	if len(p.labelFilters)+len(p.dates) > 0 {
		if len(rows) == 0 {
			rows = []pivotLine{{node: c.rowRoot}}
		}
		if len(cols) == 0 {
			cols = []pivotLine{{node: c.colRoot}}
		}
	}
	if len(rows) == 0 || len(cols) == 0 {
		return nil, nil, errors.New("透视筛选没有可显示的项目")
	}
	if int64(len(rows))*int64(len(p.rows))+int64(len(cols))*int64(len(p.cols)) > formats.EditCalcVisits {
		return nil, nil, errors.New("透视行列项目超过单次刷新容量")
	}
	offsets := c.rowOffsets(p.rows)
	dataCol := 0
	if len(offsets) > 0 {
		dataCol = offsets[len(offsets)-1] + 1
	} else if !multi && len(p.cols) > 0 {
		dataCol = 1
	}
	dataRow := 1 + len(p.cols)
	if valuesOnlyCol {
		dataRow = 1
	}
	pg := pageGrid(len(p.pages), p.table.Wrap, isTrue(p.table.Over))
	body := p.old
	if pg.rows > 0 {
		body.firstRow = max(body.firstRow, pg.rows+2)
	}
	body.lastRow = body.firstRow + dataRow + len(rows) - 1
	body.lastCol = body.firstCol + dataCol + len(cols) - 1
	if body.lastRow > formats.ExcelRows || body.lastCol > formats.ExcelColumns {
		return nil, nil, ErrTarget
	}
	grid := pivotCanvas{p.sheet, map[editdoc.Address]editdoc.Value{}}
	for _, area := range append(slices.Clone(p.oldPages), p.old, body) {
		if err := grid.cover(ctx, area); err != nil {
			return nil, nil, err
		}
	}
	cap := p.captions(values)
	for i, f := range p.pages {
		area := pg.area(body, i)
		if err := grid.cover(ctx, area); err != nil {
			return nil, nil, err
		}
		d := c.dims[f]
		grid.text(area.firstRow, area.firstCol, d.name)
		label := cap.all
		switch len(d.pagePositions) {
		case 0:
		case 1:
			v, err := d.label(d.pagePositions[0], cap)
			if err != nil {
				return nil, nil, err
			}
			grid.set(area.firstRow, area.lastCol, v)
			continue
		default:
			label = cap.multiple
		}
		grid.text(area.firstRow, area.lastCol, label)
	}
	caption := p.dataCaption(r)
	firstRow, firstCol := body.firstRow, body.firstCol
	top, left := firstRow+dataRow, firstCol+dataCol
	headers := pivotOn(p.table.Headers)
	if len(p.cols) > 0 && !valuesOnlyCol {
		if !multi {
			row := firstRow
			if len(p.rows) == 0 {
				row = top
			}
			grid.text(row, firstCol, caption)
		}
		if headers {
			if pivotOn(p.table.Compact) {
				grid.text(firstRow, left, cap.col)
			} else {
				for i, f := range p.cols {
					if left+i <= body.lastCol {
						grid.text(firstRow, left+i, c.dims[f].name)
					}
				}
			}
		}
	} else if !multi {
		grid.text(firstRow, left, caption)
	}
	if headers && len(p.rows) > 0 {
		for i, f := range p.rows {
			if i > 0 && offsets[i] == offsets[i-1] {
				continue
			}
			h := c.dims[f].name
			if (f != pivotValuesField || len(p.rows) > 1) && pivotOn(c.dims[f].field.Compact) && pivotOn(p.table.Compact) {
				h = cap.row
			}
			grid.text(top-1, firstCol+offsets[i], h)
		}
	}
	if err := c.writeAxisLabels(grid, rows, p.rows, offsets, true, top, firstCol, cap); err != nil {
		return nil, nil, err
	}
	headerStart := firstRow + 1
	if valuesOnlyCol {
		headerStart = firstRow
	}
	if err := c.writeAxisLabels(grid, cols, p.cols, nil, false, headerStart, left, cap); err != nil {
		return nil, nil, err
	}
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !row.values {
			continue
		}
		for j, col := range cols {
			if !col.values {
				continue
			}
			measure := max(row.measure, col.measure)
			if p.formats.output != nil && p.formats.byMeasure[measure] >= 0 {
				p.formats.output[editdoc.Address{Sheet: p.sheet, Row: top + i, Col: left + j}] = p.formats.byMeasure[measure]
			}
			method := p.measureAt(measure).method()
			custom := ""
			for _, line := range []pivotLine{row, col} {
				if line.kind != "" && line.kind != "default" && line.kind != "grand" {
					if custom != "" && custom != line.kind {
						method = ""
						break
					}
					custom = line.kind
					method = line.kind
					if method == "count" {
						method = "countNums"
					}
				}
			}
			if method == "" {
				continue
			}
			group := c.groups[pivotGroup{row.node, col.node, measure}]
			if group != nil && group.count > 0 {
				grid.set(top+i, left+j, group.value(method))
			} else if p.table.MissingCaption != "" {
				grid.text(top+i, left+j, p.table.MissingCaption)
			}
		}
	}
	if p.chart != nil {
		if err := p.chart.bind(p, c, rows, cols, grid, body, top, left, cap); err != nil {
			return nil, nil, err
		}
	}
	raw, err := p.layoutXML(ctx, c, rows, cols, body, dataRow, dataCol, pg, caption)
	return raw, grid.cells, err
}

func (c pivotCube) writeAxisLabels(g pivotCanvas, lines []pivotLine, axis, offsets []int, rowAxis bool, top, left int, cap pivotCaptions) error {
	if len(axis) == 0 {
		return nil
	}
	valuesDepth := slices.Index(axis, pivotValuesField)
	var prev []int
	for i, line := range lines {
		path := line.node.path()
		write := func(j int, v editdoc.Value) {
			if rowAxis {
				g.set(top+i, left+offsets[j], v)
			} else {
				g.set(top+j, left+i, v)
			}
		}
		if line.kind == "grand" {
			label := cap.grand
			if valuesDepth >= 0 {
				caption := c.dims[pivotValuesField].values[line.measure].raw
				if cap.total == "Total" {
					label = "Total " + caption
				} else {
					label = caption + "汇总"
				}
			}
			write(0, editdoc.Value{Kind: "text", Raw: label})
			continue
		}
		if line.kind == "blank" {
			prev = path
			continue
		}
		common := pivotCommonPrefix(prev, path)
		for j, pos := range path {
			d := c.dims[axis[j]]
			last := j == len(path)-1
			if !last && rowAxis && offsets[j] == offsets[j+1] {
				continue
			}
			if !last && j < common && !d.repeated() {
				continue
			}
			if !last && line.kind != "" && !d.repeated() {
				continue
			}
			v, err := d.label(pos, cap)
			if err != nil {
				return err
			}
			if last && line.kind != "" {
				label := v.Raw
				if v.Kind == "number" && v.Style != "" {
					n, _ := strconv.ParseFloat(v.Raw, 64)
					label = d.dateFormat.Display(n, d.date1904)
				}
				if valuesDepth >= len(path) && line.kind == "default" {
					label += " " + c.dims[pivotValuesField].values[line.measure].raw
				} else {
					label = d.subtotalLabel(label, line.kind, cap)
				}
				v = editdoc.Value{Kind: "text", Raw: label}
			}
			write(j, v)
		}
		prev = path
	}
	return nil
}
