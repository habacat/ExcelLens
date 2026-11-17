package workbook

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const oracleHTMLDocumentFixture = `<html><head><style>
.title {font-family:宋体;font-size:18pt;}.center {text-align:center;}.blue {background-color:#e1f5ff;border-width:.5pt;border-style:solid;border-color:#000000;}
</style></head><body><table name="report-title">
<tr><td colspan="14"><br></td></tr>
<tr><td colspan="13"><p class="center"><span class="title"><b>三栏式明细账</b></span></p>
</td><td><br></td></tr>
<tr><td></td><td>期间从：</td><td><p><b>=&quot;2024-01&quot;</b></p></td></tr>
<tr><td></td><td>期间至：</td><td>=&quot;2024-06&quot;</td></tr>
<tr><td></td><td>段组合：</td><td colspan="11">=&quot;00001234.1002010101&quot;</td></tr>
<tr><td></td><td></td><td colspan="11">银行存款</td></tr>
</table><p><br></p><table id="column-heads">
<tr><td rowspan="2"></td><td rowspan="2" class="blue"><p class="center"><b>日期</b></p></td><td rowspan="2">凭证编号</td><td rowspan="2">摘要</td><td rowspan="2">本币种</td><td colspan="2">借方</td><td colspan="2">贷方</td><td rowspan="2">方向</td><td colspan="2">余额</td><td rowspan="2"></td></tr>
<tr><td>原币</td><td>本位币</td><td>原币</td><td>本位币</td><td>原币</td><td>本位币</td></tr>
</table><p><br></p><table name="transactions">
<tr><td></td><td></td><td></td><td><p><b>期初余额</b></p></td><td></td><td></td><td></td><td></td><td></td><td>借</td><td>1,864.75</td><td>1,864.75</td><td></td></tr>
<tr><td></td><td>2024-01-15</td><td>000000000000000001</td><td>资金下拨</td><td>CNY</td><td>38,403.75</td><td>38,403.75</td><td></td><td></td><td>借</td><td>40,268.50</td><td>40,268.50</td><td></td></tr>
<tr><td></td><td></td><td></td><td>本日合计</td><td></td><td>38,403.75</td><td>38,403.75</td><td>0.00</td><td>0.00</td></tr>
</table><p><br></p></body></html>`

func TestHTMLDocumentSectionsKeepExcelCoordinatesAndNestedStyles(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "ledger.xls")
	if err := os.WriteFile(path, []byte(oracleHTMLDocumentFixture), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "ledger" || idx.MaxRow != 14 || idx.MaxCol != 14 {
		t.Fatalf("wrong document layout: %+v / %d x %d", wb.Sheets, idx.MaxRow, idx.MaxCol)
	}
	for _, point := range []struct {
		row, col int
		value    string
	}{{2, 1, "三栏式明细账"}, {3, 3, "2024-01"}, {5, 3, "00001234.1002010101"}, {7, 1, ""}, {8, 2, "日期"}, {9, 6, "原币"}, {10, 1, ""}, {11, 4, "期初余额"}, {12, 3, "000000000000000001"}, {12, 6, "38,403.75"}, {12, 7, "38,403.75"}, {13, 8, "0.00"}, {14, 1, ""}} {
		if got := cellValue(t, wb, idx, point.row, point.col); got != point.value {
			t.Fatalf("R%dC%d = %q, want %q", point.row, point.col, got, point.value)
		}
	}
	rows, err := wb.ReadRows(idx, 0, 14, 1, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows, preview) {
		t.Fatal("preview differs from complete rows")
	}
	cell := func(row, col int) CellData {
		for _, c := range rows[row-1].Cells {
			if c.Col == col {
				return c
			}
		}
		t.Fatalf("cell R%dC%d missing", row, col)
		return CellData{}
	}
	title := wb.VisualStyles[cell(2, 1).Style]
	if title.FontSize != 18 || title.FontName != "宋体" || !title.Bold || title.Horizontal != "center" {
		t.Fatalf("nested title style %+v", title)
	}
	header := wb.VisualStyles[cell(8, 2).Style]
	if !header.Bold || header.Horizontal != "center" || header.Background != "#e1f5ff" || header.BorderBottom == "" {
		t.Fatalf("header %+v", header)
	}
	period := cell(3, 3)
	if !period.HasFormula || period.Formula != `="2024-01"` || period.Raw != "2024-01" {
		t.Fatalf("literal formula %+v", period)
	}
	merges := wb.HTMLMerges(wb.Sheets[0])
	found := false
	for _, r := range merges {
		if r == (HTMLMerge{Row: 8, Col: 2, EndRow: 9, EndCol: 2}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("header merge missing: %+v", merges)
	}
	wb.SetDisplayName("原始账簿.xls")
	if wb.Sheets[0].Name != "原始账簿" {
		t.Fatal(wb.Sheets)
	}
	old, _, err := fingerprintFile(wb.Path, "xls-html-v2-styles")
	if err != nil || old == wb.Fingerprint {
		t.Fatal("legacy per-table cache reused", err)
	}
}

func TestHTMLDeclaredWorksheetsAndDocumentParagraphs(t *testing.T) {
	isolateTestStorage(t)
	for _, tc := range []struct {
		name, html string
		names      []string
		rows       []int
		bad        bool
	}{
		{"paragraphs", `<html><head><title>not a cell</title></head><body><p>开头标题</p><table name="not-a-sheet"><tr><td>data</td></tr></table><p>尾注</p></body></html>`, []string{"paragraphs"}, []int{3}, false},
		{"one", `<html><!--<x:ExcelWorksheet><x:Name>真实表名</x:Name></x:ExcelWorksheet>--><table><tr><td>one</td></tr></table><table><tr><td>two</td></tr></table></html>`, []string{"真实表名"}, []int{2}, false},
		{"two", `<html><!--<x:ExcelWorksheet><x:Name>甲</x:Name></x:ExcelWorksheet><x:ExcelWorksheet><x:Name>乙</x:Name></x:ExcelWorksheet>--><table><tr><td>one</td></tr></table><table><tr><td>two</td></tr></table></html>`, []string{"甲", "乙"}, []int{1, 1}, false},
		{"ambiguous", `<html><!--<x:ExcelWorksheet><x:Name>甲</x:Name></x:ExcelWorksheet><x:ExcelWorksheet><x:Name>乙</x:Name></x:ExcelWorksheet>--><table><tr><td>one</td></tr></table><table><tr><td>two</td></tr></table><table><tr><td>three</td></tr></table></html>`, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+".xls")
			if err := os.WriteFile(path, []byte(tc.html), 0600); err != nil {
				t.Fatal(err)
			}
			wb, err := OpenWorkbook(path)
			if tc.bad {
				if err == nil {
					t.Fatal("ambiguous sheet mapping invented")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(wb.Sheets) != len(tc.names) {
				t.Fatal(wb.Sheets)
			}
			for i, name := range tc.names {
				if wb.Sheets[i].Name != name {
					t.Fatal(wb.Sheets)
				}
				idx, err := wb.BuildIndex(wb.Sheets[i], nil, nil)
				if err != nil || idx.MaxRow != tc.rows[i] {
					t.Fatal(idx, err)
				}
			}
		})
	}
}

func TestHTMLOnlyConstantQuotedFormulasHaveKnownValues(t *testing.T) {
	for _, raw := range []string{`=SUM(A1:A2)`, `="a"&"b"`, `="x" trailing`, `="a"b"`} {
		if _, ok := htmlLiteralValue(raw); ok {
			t.Fatalf("executable expression accepted: %s", raw)
		}
	}
	for raw, want := range map[string]string{`="00001"`: "00001", `="a""b"`: "a\"b", `=""`: ""} {
		if value, ok := htmlLiteralValue(raw); !ok || value != want {
			t.Fatal(raw, value, ok)
		}
	}
	if htmlDefaultSheetName(strings.Repeat("中文", 40)+".xls") == "" {
		t.Fatal("sheet name lost")
	}
}
