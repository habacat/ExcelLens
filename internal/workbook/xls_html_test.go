package workbook

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

const officeHTMLFixture = `<!DOCTYPE HTML><HTML xmlns:x="urn:schemas-microsoft-com:office:excel"><head>
<meta charset=utf-8><style>td { color: black; } /* <table>fake</table> */</style>
<!--[if gte mso 9]><xml><x:ExcelWorkbook><x:ExcelWorksheets><x:ExcelWorksheet><x:Name>财务台账</x:Name></x:ExcelWorksheet></x:ExcelWorksheets></x:ExcelWorkbook></xml><![endif]-->
<script>if (1 < 2) { const fake = '<table><tr><td>not a row</td></tr></table>'; }</script></head>
<body><TABLE><col width=80><TR><TH rowspan='2'>凭证</TH><TH colspan=2 title="1 > 0">金额</TH></TR>
<TR><TD>借方</TD><TD>贷方</TD></TR>
<TR><TD x:str>00001234567890123456789</TD><TD x:num>1,234.500001</TD><TD></TD></TR>
<TR><TD x:num="44927.">2023/1/1</TD><TD x:fmla="=1+2" x:num>3.00</TD><TD>A&amp;B&lt;原文&gt;<br>跨行<font>文本</font>&nbsp;&amp;lt;</TD></TR>
<TR><TD>=1+2</TD><TD>-0.000001</TD><TD>1 < 2</TD></TR>
<![if supportMisalignedColumns]><tr style='display:none'><td width=100></td></tr><![endif]>
</TABLE></body></HTML>`

func TestOfficeHTMLXLSMergedCoordinatesTextAndReadOnlyCache(t *testing.T) {
	isolateTestStorage(t)
	for _, encoding := range []string{"utf8", "utf8bom", "utf16be"} {
		t.Run(encoding, func(t *testing.T) {
			data := []byte(officeHTMLFixture)
			if encoding == "utf8bom" {
				data = append([]byte{0xef, 0xbb, 0xbf}, data...)
			} else if encoding == "utf16be" {
				data = []byte{0xfe, 0xff}
				for _, unit := range utf16.Encode([]rune(officeHTMLFixture)) {
					data = binary.BigEndian.AppendUint16(data, unit)
				}
			}
			wb, idx := openIndexText(t, "office-export.xls", data)
			if wb.Format != "xls-html" || wb.Sheets[0].Name != "财务台账" || !wb.SourceInfo.ReadOnly {
				t.Fatalf("source %+v sheets %+v", wb.SourceInfo, wb.Sheets)
			}
			if idx.MaxRow != 5 || idx.MaxCol != 3 {
				t.Fatalf("shape %d x %d, want 5 x 3", idx.MaxRow, idx.MaxCol)
			}
			for _, c := range []struct {
				row, col int
				want     string
			}{{1, 1, "凭证"}, {1, 2, "金额"}, {1, 3, ""}, {2, 1, ""}, {2, 2, "借方"}, {2, 3, "贷方"},
				{3, 1, "00001234567890123456789"}, {3, 2, "1,234.500001"}, {3, 3, ""},
				{4, 1, "2023/1/1"}, {4, 2, "3.00"}, {4, 3, "A&B<原文>\n跨行文本\u00a0&lt;"},
				{5, 1, "=1+2"}, {5, 2, "-0.000001"}, {5, 3, "1 < 2"}} {
				if got := cellValue(t, wb, idx, c.row, c.col); got != c.want {
					t.Fatalf("R%dC%d = %q, want %q", c.row, c.col, got, c.want)
				}
			}
			hidden, err := wb.SourceHiddenColumns(wb.Sheets[0], idx)
			if err != nil || len(hidden) != 0 {
				t.Fatalf("HTML was sent to the BIFF reader: %v", err)
			}
			matches, _, err := wb.SearchSheet(idx, wb.Sheets[0].Name, "00001234567890123456789", "value", 10, nil, nil)
			if err != nil || len(matches) != 1 || matches[0].Ref != "A3" {
				t.Fatalf("search: %+v %v", matches, err)
			}
			reopened, err := OpenWorkbook(wb.Path)
			if err != nil || reopened.GetCachedIndex(reopened.Sheets[0].Path) == nil {
				t.Fatalf("cache reopen: %v", err)
			}
			if got := cellValue(t, reopened, idx, 4, 1); got != "2023/1/1" {
				t.Fatalf("stored date display was replaced by its HTML x:num serial: %q", got)
			}
		})
	}
}

func TestHTMLXLSMultipleTablesAndOptionalCellEndTags(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "multiple.xls")
	if err := os.WriteFile(path, []byte(`<html><table name="台账"><tr><td>0001<td>2.50<tr><td>0002<td>3.50</table><table name="台账"><tr><td>最后一表</table></html>`), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "multiple" {
		t.Fatalf("tables %+v", wb.Sheets)
	}
	for i, want := range []string{"0001", "0002", "最后一表"} {
		idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := cellValue(t, wb, idx, i+1, 1); got != want {
			t.Fatalf("table %d: %q", i, got)
		}
	}
}

func TestHTMLXLSRejectsAmbiguousOrUnboundedTables(t *testing.T) {
	isolateTestStorage(t)
	for _, text := range []string{
		`<table><tr><td>` + strings.Repeat(`<span>`, 65) + `text` + strings.Repeat(`</span>`, 65) + `</td></tr></table>`,
		`<html><body>no table</body></html>`,
		`<table><tr><td>missing table end`,
		`<table><tr><td><table><tr><td>nested</td></tr></table></td></tr></table>`,
		`<table><tr><td colspan="16385">too wide</td></tr></table>`,
		`<table><tr><td rowspan="1000000001">too tall</td></tr></table>`,
		`<table><tr><td colspan="0">invalid span</td></tr></table>`,
	} {
		path := filepath.Join(t.TempDir(), "bad.xls")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		wb, err := OpenWorkbook(path)
		if err == nil {
			_, err = wb.BuildIndex(wb.Sheets[0], nil, nil)
		}
		if err == nil || !strings.Contains(err.Error(), "HTML") {
			t.Fatalf("invalid table accepted: %s: %v", text, err)
		}
	}
}
