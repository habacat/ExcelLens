package editfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

type pivotChartSeries struct {
	title, tx, val string
	measure        int
	values         []editdoc.Value
	items          []int
}
type pivotChartData struct {
	sheet, name, part string
	category          string
	labels            [][]string
	series            []pivotChartSeries
	fields            []int
}

func chartKey(sheet, name string) string {
	return strings.ToLower(sheet) + "\x00" + strings.ToLower(name)
}

func chartPivotSource(ctx context.Context, data []byte) (string, bool, error) {
	if !bytes.Contains(data, []byte("pivotSource")) {
		return "", false, nil
	}
	var name strings.Builder
	found := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if e.local() == "pivotSource" && e.stack[len(e.stack)-1].Space == chartNS {
			found = true
		}
		if t, ok := e.token.(xml.CharData); ok && e.local() == "name" && e.parent() == "pivotSource" {
			name.Write(t)
		}
		return nil
	})
	if err != nil || !found {
		return "", found, err
	}
	raw := name.String()
	i := strings.LastIndexByte(raw, '!')
	if i < 1 || i == len(raw)-1 {
		return "", true, fmt.Errorf("透视图表来源无效：%s", raw)
	}
	sheet := strings.Trim(raw[:i], "'")
	if k := strings.LastIndexByte(sheet, ']'); k >= 0 {
		sheet = sheet[k+1:]
	}
	return chartKey(strings.ReplaceAll(sheet, "''", "'"), raw[i+1:]), true, nil
}

func seriesRef(sheet string, r cellRect) string {
	for _, c := range sheet {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c > 127) {
			sheet = "'" + strings.ReplaceAll(sheet, "'", "''") + "'"
			break
		}
	}
	addr := func(row, col int) string { return "$" + workbook.ColumnName(col) + "$" + strconv.Itoa(row) }
	start, end := addr(r.firstRow, r.firstCol), addr(r.lastRow, r.lastCol)
	if start != end {
		start += ":" + end
	}
	return sheet + "!" + start
}

func (p *xlsxPackage) linkedPivotCharts(ctx context.Context) (map[string]bool, error) {
	keys := map[string]bool{}
	for _, part := range p.zip.File {
		if classifyPart(part.Name).role != "chart" {
			continue
		}
		raw, err := p.readPart(ctx, part.Name)
		if err != nil {
			return nil, err
		}
		key, ok, err := chartPivotSource(ctx, raw)
		if err != nil {
			return nil, err
		}
		if ok {
			keys[key] = true
		}
	}
	return keys, nil
}

func chartLines(lines []pivotLine) []int {
	var out []int
	for i, line := range lines {
		if line.kind == "" && line.values && len(line.node.children) == 0 {
			out = append(out, i)
		}
	}
	return out
}

func chartPath(c pivotCube, axis []int, line pivotLine, cap pivotCaptions) ([]string, []int, error) {
	var labels []string
	var items []int
	for i, pos := range line.node.path() {
		d := c.dims[axis[i]]
		v, err := d.label(pos, cap)
		if err != nil {
			return nil, nil, err
		}
		label := v.Raw
		if v.Kind == "number" && v.Style != "" {
			n, _ := strconv.ParseFloat(v.Raw, 64)
			label = d.dateFormat.Display(n, d.date1904)
		}
		labels = append(labels, label)
		items = append(items, d.items[pos].X)
	}
	return labels, items, nil
}

func (d *pivotChartData) bind(p pivotPlan, c pivotCube, rows, cols []pivotLine, grid pivotCanvas, body cellRect, top, left int, cap pivotCaptions) error {
	d.sheet, d.name, d.part = p.sheet, p.table.Name, p.part
	d.fields = slices.Clone(p.cols)
	valuesAt := slices.Index(d.fields, pivotValuesField)
	d.fields = slices.DeleteFunc(d.fields, func(f int) bool { return f == pivotValuesField })
	d.category = seriesRef(p.sheet, cellRect{top, body.firstCol, body.lastRow, max(body.firstCol, left-1)})
	points := chartLines(rows)
	for _, i := range points {
		label, _, err := chartPath(c, p.rows, rows[i], cap)
		if err != nil {
			return err
		}
		if len(label) == 0 {
			label = []string{p.dataCaptionForChart()}
		}
		d.labels = append(d.labels, label)
	}
	for _, j := range chartLines(cols) {
		path, items, err := chartPath(c, p.cols, cols[j], cap)
		if err != nil {
			return err
		}
		if valuesAt >= 0 && valuesAt < len(items) {
			items = slices.Delete(items, valuesAt, valuesAt+1)
		}
		s := pivotChartSeries{title: strings.Join(path, " - "), items: items, measure: cols[j].measure}
		if len(path) == 0 {
			s.title = p.dataCaptionForChart()
		}
		s.tx = seriesRef(p.sheet, cellRect{body.firstRow, left + j, top - 1, left + j})
		s.val = seriesRef(p.sheet, cellRect{top, left + j, body.lastRow, left + j})
		for _, i := range points {
			s.values = append(s.values, grid.cells[editdoc.Address{Sheet: p.sheet, Row: top + i, Col: left + j}])
		}
		d.series = append(d.series, s)
	}
	return nil
}
func (p pivotPlan) dataCaptionForChart() string {
	if p.table.DataCaption == "Values" {
		return "Total"
	}
	return "汇总"
}

// Existing chartFormat entries bind formatting slots to cache members and
// can outlive a temporarily hidden series. Excel allocates new slots only
// when the visible series count exceeds the previous pool.
func (d pivotChartData) formats(ctx context.Context, raw []byte) ([]byte, error) {
	var def struct {
		Formats []struct {
			Chart  int `xml:"chart,attr"`
			Format int `xml:"format,attr"`
		} `xml:"chartFormats>chartFormat"`
	}
	if err := xml.Unmarshal(raw, &def); err != nil {
		return nil, err
	}
	if len(def.Formats) == 0 {
		return raw, nil
	}
	slots := map[int]int{}
	for _, f := range def.Formats {
		slots[f.Chart] = max(slots[f.Chart], f.Format+1)
	}
	var b strings.Builder
	added := 0
	for _, chart := range slices.Sorted(maps.Keys(slots)) {
		for i := slots[chart]; i < len(d.series); i++ {
			series := d.series[i]
			added++
			fmt.Fprintf(&b, `<chartFormat xmlns="%s" chart="%d" format="%d" series="1"><pivotArea type="data" outline="0" fieldPosition="0"><references count="%d"><reference field="4294967294" count="1" selected="0"><x v="%d"/></reference>`, mainNS, chart, i, 1+len(series.items), series.measure)
			fields := slices.Clone(d.fields[:len(series.items)])
			slices.Sort(fields)
			for _, field := range fields {
				x := series.items[slices.Index(d.fields, field)]
				fmt.Fprintf(&b, `<reference field="%d" count="1" selected="0"><x v="%d"/></reference>`, field, x)
			}
			b.WriteString("</references></pivotArea></chartFormat>")
		}
	}
	if added == 0 {
		return raw, nil
	}
	return appendPartChildren(ctx, raw, "chartFormats", "", b.String(), added)
}

func (d pivotChartData) categories() string {
	depth := 1
	for _, p := range d.labels {
		depth = max(depth, len(p))
	}
	if depth == 1 {
		values := make([]editdoc.Value, len(d.labels))
		for i, path := range d.labels {
			values[i] = editdoc.Value{Kind: "text", Raw: path[0]}
		}
		return chartReference("str", d.category, "", values)
	}
	var out strings.Builder
	fmt.Fprintf(&out, `<multiLvlStrRef xmlns="%s"><f>%s</f><multiLvlStrCache><ptCount val="%d"/>`, chartNS, escapedXML(d.category), len(d.labels))
	for level := depth - 1; level >= 0; level-- {
		out.WriteString("<lvl>")
		for i, path := range d.labels {
			if level >= len(path) || i > 0 && level < depth-1 && len(d.labels[i-1]) > level && slices.Equal(path[:level+1], d.labels[i-1][:level+1]) {
				continue
			}
			fmt.Fprintf(&out, `<pt idx="%d"><v>%s</v></pt>`, i, escapedXML(path[level]))
		}
		out.WriteString("</lvl>")
	}
	out.WriteString("</multiLvlStrCache></multiLvlStrRef>")
	return out.String()
}

func chartReference(kind, formula, format string, values []editdoc.Value) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<%sRef xmlns="%s"><f>%s</f><%sCache>`, kind, chartNS, escapedXML(formula), kind)
	if kind == "num" {
		if format == "" {
			format = "General"
		}
		fmt.Fprintf(&b, "<formatCode>%s</formatCode>", escapedXML(format))
	}
	fmt.Fprintf(&b, `<ptCount val="%d"/>`, len(values))
	for i, v := range values {
		if kind == "num" && v.Kind != "number" || v.Kind == "" || v.Kind == "blank" {
			continue
		}
		fmt.Fprintf(&b, `<pt idx="%d"><v>%s</v></pt>`, i, escapedXML(v.Raw))
	}
	fmt.Fprintf(&b, "</%sCache></%sRef>", kind, kind)
	return b.String()
}

func editSeries(ctx context.Context, raw []byte, data pivotChartSeries, cat string, id int, clone bool, key string) ([]byte, error) {
	var ps []xmlPatch
	var role, format string
	var from, to int
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.parent() == "ser" {
				switch e.local() {
				case "idx", "order":
					v, err := setXMLAttribute(raw, e.from, e.to, "val", strconv.Itoa(id))
					ps = append(ps, v...)
					return err
				case "tx", "cat", "val":
					role, from, format = e.local(), e.to, ""
				}
			} else if clone && e.local() == "uniqueId" {
				h := sha256.Sum256([]byte(key + "\x00" + data.title + "\x00" + strconv.Itoa(id)))
				u := fmt.Sprintf("{%X-%X-%X-%X-%X}", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
				v, err := setXMLAttribute(raw, e.from, e.to, "val", u)
				ps = append(ps, v...)
				return err
			}
		case xml.CharData:
			if role == "val" && e.local() == "formatCode" {
				format += string(t)
			}
		case xml.EndElement:
			if e.parent() == "ser" && role != "" && e.local() == role {
				to = e.from
				var replace string
				switch role {
				case "tx":
					replace = chartReference("str", data.tx, "", []editdoc.Value{{Kind: "text", Raw: data.title}})
				case "cat":
					replace = cat
				case "val":
					replace = chartReference("num", data.val, format, data.values)
				}
				ps = append(ps, xmlPatch{from, to, replace})
				role = ""
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, ps)
}

func (d pivotChartData) update(ctx context.Context, raw []byte) ([]byte, error) {
	type series struct {
		from, to int
		title    string
	}
	var existing []series
	parent := ""
	var active *series
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if e.stack[len(e.stack)-1].Space != chartNS {
			return nil
		}
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "ser" && strings.HasSuffix(e.parent(), "Chart") {
				if parent != "" && parent != e.parent() {
					return fmt.Errorf("组合透视图表需要逐轴分配系列")
				}
				parent = e.parent()
				active = &series{from: e.from}
			}
		case xml.CharData:
			if active != nil && e.local() == "v" && slices.ContainsFunc(e.stack, func(n xml.Name) bool { return n.Space == chartNS && n.Local == "tx" }) {
				active.title += string(t)
			}
		case xml.EndElement:
			if active != nil && e.local() == "ser" {
				active.to = e.to
				existing = append(existing, *active)
				active = nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("透视图表缺少可更新的系列")
	}
	cat := d.categories()
	var ps []xmlPatch
	var appended strings.Builder
	for i, s := range d.series {
		match := slices.IndexFunc(existing, func(old series) bool { return old.title == s.title })
		clone := match < 0
		if clone {
			match = min(i, len(existing)-1)
		}
		old := existing[match]
		next, err := editSeries(ctx, raw[old.from:old.to], s, cat, i, clone, d.name)
		if err != nil {
			return nil, err
		}
		if i < len(existing) {
			ps = append(ps, xmlPatch{existing[i].from, existing[i].to, string(next)})
		} else {
			appended.Write(next)
		}
	}
	for i := len(d.series); i < len(existing); i++ {
		ps = append(ps, xmlPatch{existing[i].from, existing[i].to, ""})
	}
	if appended.Len() > 0 {
		last := existing[len(existing)-1]
		ps = append(ps, xmlPatch{last.to, last.to, appended.String()})
	}
	return applyXML(ctx, raw, ps)
}

func (p *xlsxPackage) writePivotCharts(ctx context.Context, models map[string]*pivotChartData, updates map[string][]byte) error {
	for _, f := range p.zip.File {
		if classifyPart(f.Name).role != "chart" {
			continue
		}
		data, err := p.readPart(ctx, f.Name)
		if err != nil {
			return err
		}
		key, pivot, err := chartPivotSource(ctx, data)
		if err != nil {
			return err
		}
		if !pivot || models[key] == nil {
			continue
		}
		model := models[key]
		if table, ok := updates[model.part]; ok {
			updates[model.part], err = model.formats(ctx, table)
			if err != nil {
				return err
			}
		}
		next, err := models[key].update(ctx, data)
		if err != nil {
			return fmt.Errorf("%s：%w", f.Name, err)
		}
		if !bytes.Equal(next, data) {
			updates[f.Name] = next
		}
	}
	return nil
}
