package workbook

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const visualStylesFixture = `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <numFmts count="1"><numFmt numFmtId="165" formatCode="0.00"/></numFmts>
  <fonts count="3">
    <font><sz val="11"/><color theme="1"/><name val="宋体"/></font>
    <font><b/><i val="0"/><u/><sz val="12"/><color rgb="FF123456"/><name val="微软雅黑"/></font>
    <font><sz val="10"/><color theme="4" tint="0.5"/><name val="Arial"/></font>
  </fonts>
  <fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>
    <fill><patternFill patternType="solid"><fgColor rgb="FFFFCC33"/><bgColor indexed="64"/></patternFill></fill></fills>
  <borders count="2"><border><left/><right/><top/><bottom/></border>
    <border><left style="thin"><color indexed="8"/></left><right/><top style="double"><color rgb="FF445566"/></top><bottom/></border></borders>
  <cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/>
    <xf numFmtId="165" fontId="1" fillId="2" borderId="1" applyFont="1" applyFill="1" applyAlignment="1"><alignment horizontal="center" vertical="top" wrapText="1"/></xf>
    <xf numFmtId="0" fontId="2" fillId="1" borderId="0"/></cellXfs>
</styleSheet>`

func TestVisualStylesFromActualOOXML(t *testing.T) {
	file := filepath.Join(t.TempDir(), "styled.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              visualStylesFixture,
		"xl/theme/theme1.xml":        `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:themeElements><a:clrScheme><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk1><a:srgbClr val="000000"/></a:dk1><a:accent1><a:srgbClr val="204060"/></a:accent1></a:clrScheme></a:themeElements></a:theme>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><dimension ref="A1:C1"/><sheetData><row r="1"><c r="A1" s="1"><v>123.4</v></c><c r="B1" s="2"><v>5</v></c></row></sheetData></worksheet>`,
	}
	for name, data := range parts {
		p, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Write([]byte(data)); e != nil {
			t.Fatal(e)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.VisualStyles) != 3 {
		t.Fatalf("style count %d", len(wb.VisualStyles))
	}
	zero := wb.VisualStyles[0]
	if zero.Color != "#000000" {
		t.Errorf("default themed black=%s", zero.Color)
	}
	cell := wb.VisualStyles[1]
	if cell.Color != "#123456" || cell.Background != "#FFCC33" || cell.FontName != "微软雅黑" || cell.FontSize != 12 || !cell.Bold || cell.Italic || !cell.Underline || !cell.Wrap || cell.Horizontal != "center" || cell.Vertical != "top" {
		t.Fatalf("incorrect visual style: %+v", cell)
	}
	if cell.BorderTop != "2.25px double #445566" || cell.BorderLeft != "0.55px solid #000000" {
		t.Fatalf("borders incorrectly resolved: %+v", cell)
	}
	if wb.VisualStyles[2].Color == "" || wb.VisualStyles[2].Color == "#204060" {
		t.Fatalf("theme tint missing: %+v", wb.VisualStyles[2])
	}
	if wb.Styles[1].NumFmtID != 165 || wb.NumFmts[165] != "0.00" {
		t.Fatal("numeric format regressed")
	}
	b, err := json.Marshal(wb.VisualStyles)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"background":"#FFCC33"`) {
		t.Fatalf("palette serialization: %s", b)
	}
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := wb.ReadRow(idx, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Cells) != 2 || row.Cells[0].Style != 1 || row.Cells[0].Display != "123.40" || row.Cells[1].Style != 2 {
		t.Fatalf("cell reads changed: %+v", row)
	}
}

func TestNumberFormatFontColor(t *testing.T) {
	values := sectionTextColors(165, `#,##0.00;[Red](#,##0.00);[Blue]"-"`)
	if values[0] != "" || values[1] != "#FF0000" || values[2] != "#0000FF" {
		t.Fatalf("format color directives: %#v", values)
	}
	wb := &Workbook{Styles: []StyleInfo{{SectionColors: values}}}
	for _, tc := range []struct{ raw, want string }{{"-19.5", "#FF0000"}, {"0", "#0000FF"}, {"10", ""}} {
		if got := wb.numericTextColor(tc.raw, 0); got != tc.want {
			t.Errorf("%s color = %s, want %s", tc.raw, got, tc.want)
		}
	}
	if got := sectionTextColors(40, "")[1]; got != "#FF0000" {
		t.Errorf("builtin red negative: %s", got)
	}
}

func TestVisualColorTolerance(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FF112233", "#112233"}, {"00AABBCC", "#AABBCC"}, {"#aAbBcC", "#AABBCC"}, {"broken", ""}, {"12345", ""},
	} {
		if got := parseHexColor(tc.in); got != tc.want {
			t.Errorf("%s => %s, want %s", tc.in, got, tc.want)
		}
	}
	if got := tintHex("#000000", 1); got != "#FFFFFF" {
		t.Errorf("positive tint: %s", got)
	}
	if got := tintHex("#FFFFFF", -1); got != "#000000" {
		t.Errorf("negative tint: %s", got)
	}
	if got := tintHex("#112233", 0); got != "#112233" {
		t.Errorf("zero tint: %s", got)
	}
}
