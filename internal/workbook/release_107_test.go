package workbook

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountingZeroSectionsWithQuestionMarkPlaceholders(t *testing.T) {
	formats := []string{
		`_(* #,##0_);_(* (#,##0);_(* "-"??_);_(@_)`,
		`_(* #,##0.00_);_(* (#,##0.00);_(* "-"??_);_(@_)`,
		`#,##0;[Red](#,##0);"-"??`,
		`#,##0.00;[Red](#,##0.00);"-"??`,
		`0.00;[Red](0.00);\-??`,
		`0;[Red](0);-??`,
		`0.00;[Red](0.00);"-"???_);@`,
		`0.00;[Red](0.00);[Blue]"-"??;@`,
	}
	zeroVariants := []string{"0", "-0", "-0.0000", "+0.0", "-0.00E+00", "0E-10"}
	functions := []string{"SUMIF", "SUMIFS", "INDEX", "VLOOKUP", "XLOOKUP", "LOOKUP", "ROUND", "IF", "SUMPRODUCT", "AVERAGE"}
	for _, format := range formats {
		wb := &Workbook{NumFmts: map[int]string{164: format}, Styles: []StyleInfo{{NumFmtID: 164}}}
		for _, raw := range zeroVariants {
			for _, function := range functions {
				cell := xmlCell{Ref: "A1", Value: raw, HasValue: true, Formula: &formulaXML{Text: function + "(B1)"}}
				got := wb.resolveCell(cell, 1, nil, true, true)
				if got.Display != "-" || got.Raw != raw || !got.HasFormula || got.Formula != "="+function+"(B1)" {
					t.Errorf("format=%q raw=%q formula=%s -> display=%q raw=%q", format, raw, function, got.Display, got.Raw)
				}
			}
		}
		for _, raw := range []string{"1", "-1", "-0.01", "1234.50"} {
			got := wb.formatNumeric(raw, 0)
			if got == "-" || strings.HasPrefix(got, "-0") && raw != "-0.01" {
				t.Errorf("nonzero input unexpectedly lost: fmt=%q raw=%s got=%q", format, raw, got)
			}
		}
	}
	// A dash plus an actual unit/literal must not be simplified to a bare
	// dash, even if the section also uses question-mark alignment positions.
	for _, format := range []string{
		`0;-0;"-"??"元"`, `0;-0;"-"??"万"`, `0;-0;"-"??)`,
		`0;-0;"-"??%`, `0;-0;"-"??\元`,
	} {
		if dash, ok := formatZeroSectionDash(splitFormatSections(format)[2]); ok {
			t.Fatalf("incorrectly dropped literal from %q to %q", format, dash)
		}
	}
	for _, format := range []string{`0.00;[Red](0.00);0.00`, `0.0;0.0;"none"`, `0.00;-0.00;0.00`} {
		if got := formatCustomNumber(0, format, false); got == "-" {
			t.Fatalf("unrelated zero format %s changed to dash", format)
		}
	}
}

func TestRealXLSXAccountingZeroFormatWithAlignmentCharacters(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "accounting-aligned-zero.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	formats := []string{
		`_(* #,##0_);_(* (#,##0);_(* "-"??_);_(@_)`,
		`_(* #,##0.00_);_(* (#,##0.00);_(* "-"??_);_(@_)`,
		`#,##0;[Red](#,##0);"-"??`,
	}
	var styles strings.Builder
	styles.WriteString(`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="3">`)
	for i, format := range formats {
		styles.WriteString(fmt.Sprintf(`<numFmt numFmtId="%d" formatCode="`, 164+i))
		xml.EscapeText(&styles, []byte(format))
		styles.WriteString(`"/>`)
	}
	styles.WriteString(`</numFmts><cellXfs count="5"><xf numFmtId="0"/>`)
	for i := range formats {
		styles.WriteString(fmt.Sprintf(`<xf numFmtId="%d"/>`, 164+i))
	}
	styles.WriteString(`<xf numFmtId="2"/></cellXfs></styleSheet>`)
	var row strings.Builder
	row.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:N1"/><sheetData><row r="1">`)
	functions := []string{"SUMIF(A2:A8,0,B2:B8)", "INDEX(A2:A8,2)", "XLOOKUP(0,B2:B8,A2:A8)", "SUMPRODUCT(A2:A8)", "VLOOKUP(0,A2:B8,2,0)", "ROUND(A2,2)", "LOOKUP(0,A2:A8)", "SUMIFS(A2:A8,B2:B8,0)", "IF(1,0,1)"}
	for i, fx := range functions {
		cell := fmt.Sprintf("%s1", ColumnName(i+1))
		raw := "-0"
		if i%3 == 1 {
			raw = "0"
		}
		if i%3 == 2 {
			raw = "-0.0000E+0"
		}
		row.WriteString(fmt.Sprintf(`<c r="%s" s="%d"><f>`, cell, 1+i%3))
		xml.EscapeText(&row, []byte(fx))
		row.WriteString(`</f><v>` + raw + `</v></c>`)
	}
	row.WriteString(`<c r="J1" t="inlineStr"><is><t>-0</t></is></c>`)
	row.WriteString(`<c r="K1" t="str"><f>IF(1,"-0","x")</f><v>-0</v></c>`)
	row.WriteString(`<c r="L1" s="4"><f>SUM(A2:A8)</f><v>-0</v></c>`)
	row.WriteString(`<c r="M1"><v>-1e-999</v></c>`)
	row.WriteString(`<c r="N1" s="1"><v>23456</v></c>`)
	row.WriteString(`</row></sheetData></worksheet>`)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Results" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              styles.String(),
		"xl/worksheets/sheet1.xml":   row.String(),
	}
	for name, data := range parts {
		writer, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = writer.Write([]byte(data)); e != nil {
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
	sheet, _ := wb.FindSheet("Results")
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 1, 1, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Cells) != 14 {
		t.Fatalf("rows %+v", rows)
	}
	for i, c := range rows[0].Cells {
		switch {
		case i < 9:
			if c.Display != "-" || c.Formula == "" || !c.HasFormula {
				t.Errorf("formula %s: %+v", c.Ref, c)
			}
		case i == 9 || i == 10:
			if c.Display != "-0" || c.Type != "inlineStr" && c.Type != "str" {
				t.Errorf("text %s: %+v", c.Ref, c)
			}
		case i == 11:
			if c.Display != "0.00" {
				t.Errorf("fixed-format %s: %+v", c.Ref, c)
			}
		case i == 12:
			if c.Display != "-1e-999" {
				t.Errorf("underflow %s: %+v", c.Ref, c)
			}
		case i == 13:
			if c.Display != "23,456" {
				t.Errorf("nonzero %s: %+v", c.Ref, c)
			}
		}
	}
}
