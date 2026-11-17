package workbook

import (
	"archive/zip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryFormulaResultUsesSameZeroNormalization(t *testing.T) {
	formulas := []string{
		`SUMIF(A1:A5,"a",B1:B5)`, `SUMIFS(B1:B5,A1:A5,"a")`,
		`INDEX(B1:B5,MATCH("a",A1:A5,0))`, `XLOOKUP("a",A1:A5,B1:B5)`,
		`VLOOKUP("a",A1:B5,2,FALSE)`, `LOOKUP(2,1/(A1:A5<>""),B1:B5)`,
		`IF(A1>0,0,-0)`, `SUMPRODUCT(A1:A5,B1:B5)`, `ROUND(A1,2)`,
		`AGGREGATE(9,6,B1:B5)`, `SUBTOTAL(9,B1:B5)`, `A1-A1`,
	}
	for _, fx := range formulas {
		for _, tc := range []struct {
			name, raw, display string
			fmtID, style       int
			fmt                string
		}{
			{name: "withoutStyles", raw: "-0", display: "0"},
			{name: "missingXF", raw: "-0E+00", display: "0", style: 17},
			{name: "General", raw: "-0.000", display: "0", fmtID: 0, style: 0},
			{name: "number2", raw: "-0", display: "0.00", fmtID: 2},
			{name: "accounting", raw: "-0", display: "-", fmtID: 44},
			{name: "customDash", raw: "-0", display: "-", fmtID: 164, fmt: `#,##0.00;[Red](#,##0.00);"-"`},
			{name: "customEscapedDash", raw: "-0e-3", display: "-", fmtID: 164, fmt: `#,##0.00;[Red](#,##0.00);\-`},
		} {
			t.Run(strings.Split(fx, "(")[0]+"/"+tc.name, func(t *testing.T) {
				styles := []StyleInfo(nil)
				if tc.name != "withoutStyles" {
					styles = []StyleInfo{{NumFmtID: tc.fmtID}}
				}
				wb := &Workbook{Styles: styles, NumFmts: map[int]string{164: tc.fmt}}
				cell := xmlCell{Ref: "B4", Type: "n", Style: tc.style, Value: tc.raw, HasValue: true, Formula: &formulaXML{Text: fx}}
				got := wb.resolveCell(cell, 2, nil, true, true)
				if got.Display != tc.display || got.Raw != tc.raw || got.Formula != "="+fx || !got.HasFormula || got.CacheMissing {
					t.Fatalf("%s %s: %+v, want display=%q", fx, tc.name, got, tc.display)
				}
			})
		}
	}
}

func TestNumericZeroVsNonzeroAndTextFidelity(t *testing.T) {
	wb := &Workbook{Styles: []StyleInfo{{NumFmtID: 164}}, NumFmts: map[int]string{164: `0.00;(0.00);"-"`}}
	samples := []struct{ in, want string }{
		{"-0", "-"}, {"-0.00", "-"}, {"-0E+12", "-"}, {"0.000e-42", "-"},
		{"-0.0000001", "(0.00)"}, {"-1e-15", "(0.00)"}, {"0.01", "0.01"},
		{"-0.01", "(0.01)"}, {"-1.25", "(1.25)"}, {"+0", "-"},
	}
	for _, tc := range samples {
		if got := wb.formatNumeric(tc.in, 0); got != tc.want {
			t.Errorf("format(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"-1e-400", "-3.1e-400", "NaN", "Infinity"} {
		if got := wb.formatNumeric(in, 0); got != in {
			t.Errorf("nonzero/invalid changed %q into %q", in, got)
		}
	}
	for _, typ := range []string{"s", "inlineStr", "str", "e"} {
		wb.Shared = []string{"-0"}
		c := xmlCell{Ref: "A1", Type: typ, Value: "-0", HasValue: true, Inline: "-0"}
		if typ == "s" {
			c.Value = "0"
		}
		got := wb.resolveCell(c, 1, nil, true, true)
		if got.Display != "-0" {
			t.Errorf("text/error of type %q was normalized: %+v", typ, got)
		}
	}
	if got := formatCustomNumber(math.Copysign(0, -1), `0.00;(0.00);"-"`, false); got != "-" {
		t.Fatal(got)
	}
}

func TestReadTabColorsFromWorksheetMetadata(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tabs.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Red" sheetId="1" r:id="rId1"/><sheet name="Theme" sheetId="2" r:id="rId2"/><sheet name="Plain" sheetId="3" r:id="rId3"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetPr><tabColor rgb="FFF05A24"/></sheetPr><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetPr><tabColor theme="4" tint="0.5"/></sheetPr><sheetData/></worksheet>`,
		"xl/worksheets/sheet3.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>3</v></c></row></sheetData><sheetPr><tabColor rgb="FF000000"/></sheetPr></worksheet>`,
		"xl/theme/theme1.xml":        `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:themeElements><a:clrScheme><a:accent1><a:srgbClr val="204060"/></a:accent1></a:clrScheme></a:themeElements></a:theme>`,
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
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 3 {
		t.Fatalf("sheets=%+v", wb.Sheets)
	}
	if wb.Sheets[0].TabColor != "#F05A24" {
		t.Errorf("rgb=%s", wb.Sheets[0].TabColor)
	}
	if wb.Sheets[1].TabColor != "#709FCF" {
		t.Errorf("theme+tint=%s", wb.Sheets[1].TabColor)
	}
	if wb.Sheets[2].TabColor != "" {
		t.Errorf("late metadata incorrectly read=%s", wb.Sheets[2].TabColor)
	}
	data, _ := json.Marshal(wb.Sheets)
	if !strings.Contains(string(data), `"tabColor":"#F05A24"`) {
		t.Fatalf("missing JSON tab color: %s", data)
	}
}

func TestBorderWidthsRefinedButStillDistinct(t *testing.T) {
	cases := []struct{ style, want string }{
		{"hair", "0.35px dotted #000000"}, {"thin", "0.55px solid #000000"},
		{"dashed", "0.55px dashed #000000"}, {"medium", "1.15px solid #000000"},
		{"thick", "1.75px solid #000000"}, {"double", "2.25px double #000000"},
	}
	for _, tc := range cases {
		if got := borderCSS(styleSideXML{Style: tc.style}, "#000000"); got != tc.want {
			t.Errorf("%s=%q want %q", tc.style, got, tc.want)
		}
	}
}

// This exercises the complete actual XLSX -> workbook -> row index -> cell
// decoding -> display path. A unit test of formatNumeric alone would miss an
// alternate read path leaking -0, exactly the bug exposed by INDEX formulas.
func TestRealXLSXFormulaCachedZerosDisplayWithoutNegativeZero(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "cached-formula-zeros.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Results" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0.00;[Red](#,##0.00);&quot;-&quot;"/></numFmts><cellXfs count="4"><xf numFmtId="0"/><xf numFmtId="44"/><xf numFmtId="164"/><xf numFmtId="2"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml": `<worksheet><dimension ref="A1:L2"/><sheetData>
<row r="1">
<c r="A1"><f>INDEX(A2:A5,MATCH(0,B2:B5,0))</f><v>-0</v></c>
<c r="B1" s="1"><f>SUMIF(B2:B5,"x",A2:A5)</f><v>-0</v></c>
<c r="C1" s="2"><f>SUMIFS(A2:A5,B2:B5,"x")</f><v>-0.00E+00</v></c>
<c r="D1" s="3"><f>XLOOKUP(0,A2:A5,B2:B5)</f><v>-0</v></c>
<c r="E1" s="77"><f>VLOOKUP(0,A2:B5,2,0)</f><v>-0.000</v></c>
<c r="F1" s="2"><v>-0.01</v></c>
<c r="G1" t="inlineStr"><is><t>-0</t></is></c>
<c r="H1" t="str"><f>IF(1,"-0","1")</f><v>-0</v></c>
<c r="I1" s="2"><f>LOOKUP(0,A2:A5)</f><v>0</v></c>
<c r="J1" s="1"><f>ROUND(A2,2)</f><v>-0</v></c>
<c r="K1" s="2"><f>IF(1,0,1)</f><v>-1e-15</v></c>
<c r="L1" s="1"><f>SUMPRODUCT(A2:A5)</f></c>
</row><row r="2"><c r="A2"><v>12</v></c></row></sheetData></worksheet>`,
	}
	for name, data := range parts {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(data)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	if e := f.Close(); e != nil {
		t.Fatal(e)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	sheet, ok := wb.FindSheet("Results")
	if !ok {
		t.Fatal("Results not found")
	}
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 1, 1, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Cells) != 12 {
		t.Fatalf("rows: %+v", rows)
	}
	want := []struct {
		display, raw     string
		formula, missing bool
	}{
		{"0", "-0", true, false}, {"-", "-0", true, false},
		{"-", "-0.00E+00", true, false}, {"0.00", "-0", true, false},
		{"0", "-0.000", true, false}, {"(0.01)", "-0.01", false, false},
		{"-0", "-0", false, false}, {"-0", "-0", true, false},
		{"-", "0", true, false}, {"-", "-0", true, false},
		{"(0.00)", "-1e-15", true, false}, {"", "", true, true},
	}
	for i, cell := range rows[0].Cells {
		expect := want[i]
		if cell.Display != expect.display || cell.Raw != expect.raw || cell.HasFormula != expect.formula || cell.CacheMissing != expect.missing {
			t.Errorf("%s: display=%q raw=%q hasFormula=%v cacheMissing=%v expected=%+v", ColumnName(i+1), cell.Display, cell.Raw, cell.HasFormula, cell.CacheMissing, expect)
		}
	}
}

func TestAccountingBuiltInFractionDigits(t *testing.T) {
	for _, tc := range []struct {
		formatID int
		in, want string
	}{
		{41, "1234.5", "1,235"}, {42, "-12.5", "(13)"},
		{43, "1234.5", "1,234.50"}, {44, "-12.5", "(12.50)"},
		{41, "-0", "-"}, {42, "-0", "-"}, {43, "-0", "-"}, {44, "-0", "-"},
	} {
		wb := &Workbook{Styles: []StyleInfo{{NumFmtID: tc.formatID}}}
		if got := wb.formatNumeric(tc.in, 0); got != tc.want {
			t.Errorf("built-in %d value %q => %q, want %q", tc.formatID, tc.in, got, tc.want)
		}
	}
}
