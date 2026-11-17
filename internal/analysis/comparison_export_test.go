package analysis

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestComparisonExportRetainsRatiosStatesAndExactCumulativeResults(t *testing.T) {
	q := Query{Source: "wb:A", Rows: []string{"C1"}, Columns: []string{"C2"}, Sort: "label_asc", Top: 1, Precision: 3, Metrics: []Metric{{Field: "C3", Aggregate: "average", Comparison: "percent_grand"}, {Field: "C3", Aggregate: "sum", Comparison: "running_total"}, {Field: "C4", Aggregate: "distinct", Comparison: "percent_row"}}}
	r, err := Run(t.Context(), q, sampleFields(), testScan(comparisonRecords()), nil)
	if err != nil {
		t.Fatal(err)
	}
	grid := BuildPivotTable(q, r, sampleFields())
	if len(grid.Headers) != 10 || !strings.Contains(grid.Headers[1], "1=100%") || !strings.Contains(grid.Headers[2], "沿行累计") || grid.Rows[0][1] != "0.3125" || grid.Rows[0][7] != "0.625" || grid.Rows[1][8] != "160" || !strings.Contains(grid.Rows[1][0], "未显示") || !grid.PercentValues[0][1] || grid.PercentValues[0][2] {
		t.Fatal("export changed compared values, units or full-scope totals", grid)
	}
	var output bytes.Buffer
	if err := ExportPivotXLSX(&output, grid); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var sheet, styles string
	for _, entry := range reader.File {
		f, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		_ = f.Close()
		if entry.Name == "xl/worksheets/sheet1.xml" {
			sheet = string(data)
		}
		if entry.Name == "xl/styles.xml" {
			styles = string(data)
		}
	}
	if !strings.Contains(sheet, `<c r="B2" s="2"><v>0.3125</v></c>`) || !strings.Contains(sheet, `<c r="C2"><v>10</v></c>`) || !strings.Contains(styles, `formatCode="0.000%"`) {
		t.Fatal("xlsx lost the exact numeric ratio, percent style or independent amount", sheet, styles)
	}
	// A non-terminating ratio remains a literal exact fraction in the same
	// export path; a zero denominator remains an explicit nonnumeric state.
	q.Columns, q.Top = nil, 0
	q.Metrics = []Metric{{Field: "C3", Aggregate: "sum", Comparison: "percent_grand"}}
	rows := []Record{{Number: 1, Values: map[string]Value{"C1": {Text: "A"}, "C3": {Text: "1", Numeric: true}}}, {Number: 2, Values: map[string]Value{"C1": {Text: "B"}, "C3": {Text: "2", Numeric: true}}}}
	r, err = Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	grid = BuildPivotTable(q, r, sampleFields())
	if grid.Rows[0][1] != "1/3" || !grid.NumericValues[0][1] || numericSafe(grid.Rows[0][1]) {
		t.Fatal("rational comparison was rounded for export", grid)
	}
	rows[1].Values["C3"] = Value{Text: "-1", Numeric: true}
	r, err = Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	grid = BuildPivotTable(q, r, sampleFields())
	if grid.Rows[0][1] != "分母为0" || grid.NumericValues[0][1] || grid.PercentValues[0][1] {
		t.Fatal("denominator state became a zero or blank", grid)
	}
}

func TestComparisonExportRefusesExcelNumericUnderflowAndOverflowWithoutRounding(t *testing.T) {
	for _, value := range []string{"1e-309", "1e308", "0." + strings.Repeat("0", 600) + "1"} {
		if numericSafe(value) {
			t.Fatal("unrepresentable Excel magnitude accepted", value)
		}
	}
	for _, value := range []string{"0", "0.00001", "9.99999999999999e307", "2.22507385850721e-308", "-1234.5"} {
		if !numericSafe(value) {
			t.Fatal("representable numeric value rejected", value)
		}
	}
	q := Query{Source: "wb:A", Rows: []string{"C1"}, Metrics: []Metric{{Field: "C3", Aggregate: "sum", Comparison: "percent_grand"}}, Precision: 10}
	rows := []Record{{Number: 1, Values: map[string]Value{"C1": {Text: "A"}, "C3": {Text: "1e-308", Numeric: true}}}, {Number: 2, Values: map[string]Value{"C1": {Text: "B"}, "C3": {Text: "1e308", Numeric: true}}}}
	r, err := Run(t.Context(), q, sampleFields(), testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	grid := BuildPivotTable(q, r, sampleFields())
	if grid.Rows[0][1] == "" || grid.Rows[0][1] == "0" || numericSafe(grid.Rows[0][1]) {
		t.Fatal("extreme exact ratio silently rounded for export", grid.Rows[0][1])
	}
}
