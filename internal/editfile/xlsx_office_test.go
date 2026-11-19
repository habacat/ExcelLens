package editfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestStylesUseActualCellFormatsAndPreserveExtensions(t *testing.T) {
	p := xlsxPackage{}
	body := `<styleSheet xmlns="` + mainNS + `" xmlns:v="urn:vendor"><cellXfs count="1"><xf/><xf/></cellXfs><extLst><ext uri="vendor"><v:theme><cellXfs><xf/></cellXfs></v:theme></ext></extLst></styleSheet>`
	if err := p.readStyles(t.Context(), []byte(body)); err != nil || !p.validStyle("1") || p.validStyle("2") || !p.layout.Structured {
		t.Fatal(p.styles, err)
	}
	for _, bad := range []string{
		strings.Replace(body, "</cellXfs>", "</cellXfs><cellXfs/>", 1),
		strings.Replace(body, "<xf/>", "<v:choice><xf/></v:choice>", 1),
		strings.Replace(body, "</styleSheet>", "</extLst>", 1),
	} {
		if err := p.readStyles(t.Context(), []byte(bad)); err == nil {
			t.Fatal("ambiguous style index accepted")
		}
	}
}

func TestExtendedValidationUsesLocalRangesAndSparseCrossSheetChoices(t *testing.T) {
	isolateEditFileStorage(t)
	const extURI = "{CCE6A557-97BC-4B89-ADB6-D9C93CAAB3DF}"
	path := writeEditBook(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="` + mainNS + `" xmlns:x14="` + x14NS + `" xmlns:xm="http://schemas.microsoft.com/office/excel/2006/main"><sheetData/><extLst><ext uri="` + extURI + `"><x14:dataValidations count="2"><x14:dataValidation type="list"><x14:formula1><xm:f>'摘要'!$A:$A</xm:f></x14:formula1><xm:sqref>B2 D4:D6</xm:sqref></x14:dataValidation><x14:dataValidation type="custom"><x14:formula1><xm:f>LEN(E3)&gt;2</xm:f></x14:formula1><xm:sqref>E3</xm:sqref></x14:dataValidation></x14:dataValidations></ext></extLst></worksheet>`
		parts["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="3"><c r="A3" t="inlineStr"><is><t>First</t></is></c></row><row r="12"><c r="A12" s="1"/></row><row r="20000"><c r="A20000" t="inlineStr"><is><t>Second</t></is></c></row></sheetData></worksheet>`
	})
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wb.ClearIndexes)
	src, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	l, _, err := InspectXLSX(t.Context(), src, wb)
	if err != nil || !l.Native {
		t.Fatal(l, err)
	}
	a := func(r, c int) editdoc.Address { return editdoc.Address{Sheet: "数据", Row: r, Col: c} }
	target, other, custom, gap := a(2, 2), a(5, 4), a(3, 5), a(3, 2)
	remote := editdoc.Address{Sheet: "摘要", Row: 1048576, Col: 1}
	b, err := (Layout{XLSX: &l}).ReadBatch(t.Context(), src, []editdoc.Address{target, other, custom, gap, remote}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []editdoc.Address{target, other} {
		choices, err := b.ListChoices(address, nil)
		if err != nil || !slices.Equal(choices, []string{"First", "Second"}) || b.cells[address].ReadOnly != "" {
			t.Fatal(address, choices, err)
		}
	}
	if b.cells[custom].ReadOnly == "" || b.cells[gap].ReadOnly != "" || b.cells[gap].RuleAt(gap) != nil {
		t.Fatal("validation scope escaped its source ranges")
	}
	value := editdoc.Value{Kind: "text", Raw: "Third"}
	change := editdoc.Change{Address: target, Value: value}
	if _, _, err = b.Validate(t.Context(), []editdoc.Change{change}, nil); err == nil {
		t.Fatal("out-of-list edit committed")
	}
	if _, _, err = b.Validate(t.Context(), []editdoc.Change{change, {Address: remote, Value: value}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinateWindowKeepsOwnersOutsideSelectedRows(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="` + mainNS + `"><dimension ref="A1:F40"/><sheetData><row r="1"><c r="A1"><v>5</v></c><c r="B1"><f t="shared" ca="1" si="9" ref="B1:B40">A1+1</f><v>6</v></c><c r="D1"><f t="array" ref="D1:D19">A1:A19+1</f><v>6</v></c></row><row r="19"><c r="D19"><v>1</v></c></row><row r="33"><c r="B33"><f t="shared" si="9"/><v>1</v></c></row><row r="40"><c r="B40"><f t="shared" si="9"/><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1:A40"><cfRule type="expression" priority="1"><formula>A1&gt;3</formula></cfRule></conditionalFormatting><dataValidations><dataValidation type="list" sqref="F20"><formula1>"a,b"</formula1></dataValidation></dataValidations></worksheet>`
	})
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	defer wb.ClearIndexes()
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l, proof, err := InspectXLSX(t.Context(), f, wb)
	if err != nil || !l.Native {
		t.Fatal(l.Native, err)
	}
	addr := func(row, col int) editdoc.Address { return editdoc.Address{Sheet: "数据", Row: row, Col: col} }
	follower, member, list, gap := addr(33, 2), addr(19, 4), addr(20, 6), addr(21, 5)
	cells, err := ReadXLSXCells(t.Context(), f, l, []editdoc.Address{follower, member, list, gap})
	if err != nil || cells[follower].Value.Raw != "=A33+1" || cells[member].ReadOnly == "" || cells[gap].Value.Kind != "missing" {
		t.Fatal(cells, err)
	}
	edits := map[editdoc.Address]editdoc.Value{follower: {Kind: "formula", Raw: "=21+21"}, list: {Kind: "text", Raw: "invalid"}, gap: {Kind: "number", Raw: "17"}}
	if _, err = WriteXLSX(t.Context(), &strings.Builder{}, f, l, proof, edits, nil); err == nil {
		t.Fatal("writer accepted a value outside the list")
	}
	edits[list] = editdoc.Value{Kind: "text", Raw: "b"}
	out, err := os.Create(filepath.Join(t.TempDir(), "copy.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	saved, err := WriteXLSX(t.Context(), out, f, l, proof, edits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyXLSX(t.Context(), out, saved); err != nil {
		t.Fatal(err)
	}
	z, err := openXLSXPackage(out, statSize(t, out))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := z.readPart(t.Context(), "xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A40+1</f>", "A1+1</f>", "21+21</f>", `ref="D1:D19"`, `<conditionalFormatting sqref="A1:A40">`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(string(raw), `t="shared"`) {
		t.Error("partially expanded shared group")
	}
}

func TestMacroContainerBindingAndLocalCalculation(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		parts["[Content_Types].xml"] = strings.Replace(parts["[Content_Types].xml"], spreadMIME+"sheet.main+xml", macroBookMIME, 1)
		parts["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><f>数据!D4+1</f><v>4</v></c></row></sheetData></worksheet>`
		parts["xl/styles.xml"] = strings.Replace(parts["xl/styles.xml"], "</styleSheet>", `<dxfs count="1"><dxf/></dxfs><tableStyles><tableStyle xmlns:xr9="http://schemas.microsoft.com/office/spreadsheetml/2016/revision9" xr9:uid="{CE62DAA0-AEBC-4F90-AE11-B59C73ADA181}" name="Theme" count="1"><tableStyleElement type="firstRowStripe" dxfId="0"/></tableStyle></tableStyles><extLst><ext uri="{EB79DEF2-80B8-43e5-95BD-54CBDDF9020C}"><x:slicerStyles xmlns:x="`+x14NS+`" defaultSlicerStyle="Light1"/></ext></extLst></styleSheet>`, 1)
		parts["xl/sharedStrings.xml"] = strings.Replace(parts["xl/sharedStrings.xml"], "<t>00120 &amp; 中</t>", `<r><t>001</t></r><r><t>20</t></r><rPh sb="0" eb="1"><t>guide</t></rPh><phoneticPr fontId="0"/>`, 1)
	})
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	defer wb.ClearIndexes()
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = InspectXLSX(t.Context(), f, wb)
	f.Close()
	if err == nil {
		t.Fatal("XLSX accepted macro-enabled main part")
	}
	macro := filepath.Join(filepath.Dir(path), "book.xlsm")
	if err = os.Rename(path, macro); err != nil {
		t.Fatal(err)
	}
	wb, err = workbook.OpenWorkbook(macro)
	if err != nil {
		t.Fatal(err)
	}
	defer wb.ClearIndexes()
	f, err = workbook.OpenEditSource(macro)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	layout, _, err := InspectXLSX(t.Context(), f, wb)
	if err != nil || !layout.MacroEnabled {
		t.Fatal(err)
	}
	a := editdoc.Address{Sheet: "数据", Row: 1, Col: 1}
	v, err := ReadXLSXCells(t.Context(), f, layout, []editdoc.Address{a})
	if err != nil || v[a].Value.Raw != "00120" {
		t.Fatal(v, err)
	}
	b, err := NewCalculationBook(t.Context(), f, layout)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Calculate(t.Context(), map[editdoc.Address]editdoc.Value{{Sheet: "数据", Row: 4, Col: 4}: {Kind: "number", Raw: "8"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Cells[editdoc.Address{Sheet: "摘要", Row: 1, Col: 1}]
	if got.State != "calculated" || got.Value.Raw != "9" {
		t.Fatal(got)
	}
}
