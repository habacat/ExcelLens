package analysis

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// PivotTable describes user-facing report output. NumericValues identifies
// exact aggregate cells, avoiding CSV formula injection and XLSX precision loss.
type PivotTable struct {
	Headers       []string
	Rows          [][]string
	NumericValues [][]bool
	PercentValues [][]bool
	Precision     int
}

func BuildPivotTable(q Query, result Result, fields []Field) PivotTable {
	if result.Tree != nil {
		q.Rows = result.RowFields
	}
	labels := make(map[string]string, len(fields)+len(q.Transforms))
	for _, f := range fields {
		labels[f.ID] = f.Name
	}
	for _, t := range q.Transforms {
		labels[t.ID] = t.Name
	}
	name := func(id string) string {
		if id == "" {
			return "记录数"
		}
		if labels[id] != "" {
			return labels[id]
		}
		return id
	}
	metrics := queryMetrics(q)
	metricLabel := func(m Metric) string {
		suffix := ComparisonLabel(m)
		if suffix != "" {
			suffix = " · " + suffix
		}
		if m.Name != "" {
			return m.Name + suffix
		}
		aggregate := map[string]string{"sum": "求和", "count": "计数", "average": "平均", "min": "最小", "max": "最大", "distinct": "去重数"}[m.Aggregate]
		if m.Aggregate == "count" && m.Field == "" {
			return "记录数" + suffix
		}
		return name(m.Field) + " · " + aggregate + suffix
	}
	dims := len(q.Rows)
	if dims == 0 {
		dims = 1
	}
	headers := make([]string, 0, dims+(len(result.ColumnKeys)+1)*len(metrics))
	if len(q.Rows) == 0 {
		headers = append(headers, "分类")
	} else {
		for _, f := range q.Rows {
			headers = append(headers, name(f))
		}
	}
	hasColumns := len(q.Columns) > 0
	if hasColumns {
		for _, column := range result.ColumnKeys {
			if column == "" {
				column = "（空白）"
			}
			for _, metric := range metrics {
				headers = append(headers, column+" · "+metricLabel(metric))
			}
		}
		for _, metric := range metrics {
			headers = append(headers, "合计 · "+metricLabel(metric))
		}
	} else {
		for _, metric := range metrics {
			headers = append(headers, metricLabel(metric))
		}
	}
	rows := make([][]string, 0, len(result.Rows)+1)
	nums := make([][]bool, 0, len(result.Rows)+1)
	percentages := make([][]bool, 0, len(result.Rows)+1)
	for _, r := range result.Rows {
		line := make([]string, 0, len(headers))
		numeric := make([]bool, 0, len(headers))
		percent := make([]bool, dims)
		appendCell := func(cell Cell) {
			text, number, percentage := outputCell(cell)
			line, numeric, percent = append(line, text), append(numeric, number), append(percent, percentage)
		}
		if len(q.Rows) == 0 {
			line = append(line, "全部")
			numeric = append(numeric, false)
		} else {
			line = append(line, r.Keys...)
			for range r.Keys {
				numeric = append(numeric, false)
			}
		}
		if hasColumns {
			for j := range result.ColumnKeys {
				for k := range metrics {
					cell := Cell{}
					if j < len(r.MetricValues) && k < len(r.MetricValues[j]) {
						cell = r.MetricValues[j][k]
					} else if k == 0 && j < len(r.Values) {
						cell = r.Values[j]
					}
					appendCell(cell)
				}
			}
		}
		for k := range metrics {
			cell := Cell{}
			if k < len(r.MetricTotals) {
				cell = r.MetricTotals[k]
			} else if k == 0 {
				cell = r.Total
			}
			appendCell(cell)
		}
		rows = append(rows, line)
		nums = append(nums, numeric)
		percentages = append(percentages, percent)
	}
	footer := make([]string, dims)
	footerNums := make([]bool, dims)
	footerPercent := make([]bool, dims)
	appendFooter := func(cell Cell) {
		text, number, percentage := outputCell(cell)
		footer, footerNums, footerPercent = append(footer, text), append(footerNums, number), append(footerPercent, percentage)
	}
	footer[0] = "总计"
	if result.Truncated {
		footer[0] = "全量总计（含未显示项）"
	}
	if hasColumns {
		for j := range result.ColumnKeys {
			for k := range metrics {
				cell := Cell{}
				if j < len(result.MetricColumnTotals) && k < len(result.MetricColumnTotals[j]) {
					cell = result.MetricColumnTotals[j][k]
				} else if k == 0 && j < len(result.Totals) {
					cell = result.Totals[j]
				}
				appendFooter(cell)
			}
		}
	}
	for k := range metrics {
		cell := Cell{}
		if k < len(result.MetricGrand) {
			cell = result.MetricGrand[k]
		} else if k == 0 {
			cell = result.Grand
		}
		appendFooter(cell)
	}
	rows = append(rows, footer)
	nums = append(nums, footerNums)
	percentages = append(percentages, footerPercent)
	return PivotTable{Headers: headers, Rows: rows, NumericValues: nums, PercentValues: percentages, Precision: q.Precision}
}

// numericSafe guards Excel's 15-significant-digit numeric precision limit.
// Longer decimals are stored as inline strings, never silently rounded.
var decimalString = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?$`)

var excelNumericMin, _ = Decimal("2.2250738585072014e-308")
var excelNumericMax, _ = Decimal("9.99999999999999e307")

func numericSafe(s string) bool {
	if !decimalString.MatchString(s) {
		return false
	}
	significant := 0
	started := false
	mantissa := s
	if exponent := strings.IndexAny(s, "eE"); exponent >= 0 {
		mantissa = s[:exponent]
	}
	for _, r := range mantissa {
		if r >= '1' && r <= '9' {
			started = true
		}
		if r >= '0' && r <= '9' && started {
			significant++
		}
	}
	if significant > 15 {
		return false
	}
	value, ok := Decimal(s)
	if !ok {
		return false
	}
	value.Abs(value)
	// A ratio can have few significant digits and still underflow/overflow
	// Excel. Keep it as exact text rather than writing a numeric zero/infinity.
	return value.Sign() == 0 || value.Cmp(excelNumericMin) >= 0 && value.Cmp(excelNumericMax) <= 0
}
func xmlValue(s string) string {
	// XML 1.0 disallows most control characters, but allows tabs/newlines.
	var b strings.Builder
	for _, r := range s {
		if r == 9 || r == 10 || r == 13 || r >= 32 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF {
			b.WriteRune(r)
		}
	}
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(b.String()))
	return escaped.String()
}
func xlsxCol(col int) string {
	var out string
	for col > 0 {
		col--
		out = string(rune('A'+col%26)) + out
		col /= 26
	}
	return out
}
func zipWrite(z *zip.Writer, name, data string) error {
	f, err := z.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, data)
	return err
}
func ExportPivotXLSX(w io.Writer, grid PivotTable) error {
	if len(grid.Headers) == 0 || len(grid.Headers) > 16384 || len(grid.Rows) > 1_048_575 {
		return fmt.Errorf("报表尺寸超过Excel限制")
	}
	zw := zip.NewWriter(w)
	const contentType = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`
	if err := zipWrite(zw, "[Content_Types].xml", contentType); err != nil {
		return err
	}
	if err := zipWrite(zw, "_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`); err != nil {
		return err
	}
	if err := zipWrite(zw, "xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`); err != nil {
		return err
	}
	if err := zipWrite(zw, "xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="透视结果" sheetId="1" r:id="rId1"/></sheets><calcPr calcMode="manual"/></workbook>`); err != nil {
		return err
	}
	precision := grid.Precision
	if precision < 0 || precision > 10 {
		return fmt.Errorf("导出显示精度无效")
	}
	percentFormat := "0"
	if precision > 0 {
		percentFormat += "." + strings.Repeat("0", precision)
	}
	styles := `<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="` + percentFormat + `%"/></numFmts><fonts count="2"><font><sz val="11"/><name val="Aptos"/></font><font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Aptos"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF294C61"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs></styleSheet>`
	if err := zipWrite(zw, "xl/styles.xml", styles); err != nil {
		return err
	}
	sheet, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Grow(256 << 10)
	flush := func() error {
		if b.Len() == 0 {
			return nil
		}
		_, e := io.WriteString(sheet, b.String())
		b.Reset()
		return e
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="16"/><sheetData>`)
	writeRow := func(rowNo int, values []string, numeric, percentages []bool, header bool) {
		fmt.Fprintf(&b, `<row r="%d">`, rowNo)
		for i, v := range values {
			if v == "" {
				continue
			}
			ref := xlsxCol(i+1) + fmt.Sprint(rowNo)
			if !header && i < len(numeric) && numeric[i] && numericSafe(v) {
				style := ""
				if i < len(percentages) && percentages[i] {
					style = ` s="2"`
				}
				fmt.Fprintf(&b, `<c r="%s"%s><v>%s</v></c>`, ref, style, v)
			} else {
				style := ""
				if header {
					style = ` s="1"`
				}
				fmt.Fprintf(&b, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlValue(v))
			}
		}
		b.WriteString(`</row>`)
	}
	writeRow(1, grid.Headers, nil, nil, true)
	if len(grid.NumericValues) != len(grid.Rows) || len(grid.PercentValues) != 0 && len(grid.PercentValues) != len(grid.Rows) {
		return fmt.Errorf("导出数值类型或百分比行数不匹配")
	}
	for i, row := range grid.Rows {
		if len(row) != len(grid.Headers) {
			return fmt.Errorf("导出第%d行的列数不匹配", i+2)
		}
		if !utf8.ValidString(strings.Join(row, "")) {
			return fmt.Errorf("导出含有无效UTF-8")
		}
		var percentages []bool
		if len(grid.PercentValues) > 0 {
			percentages = grid.PercentValues[i]
		}
		writeRow(i+2, row, grid.NumericValues[i], percentages, false)
		if b.Len() > 256<<10 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	b.WriteString(`</sheetData>`)
	if len(grid.Headers) <= 16384 && len(grid.Rows) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, xlsxCol(len(grid.Headers)), len(grid.Rows)+1)
	}
	b.WriteString(`</worksheet>`)
	if err := flush(); err != nil {
		return err
	}
	return zw.Close()
}
