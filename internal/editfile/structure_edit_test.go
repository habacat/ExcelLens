package editfile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestStructureReferencesAndSharedMembers(t *testing.T) {
	layout := XLSXLayout{Sheets: []XLSXSheet{{Name: "Data"}, {Name: "Other"}}}
	a, err := newAxisEdit(editdoc.Structure{Kind: "insertRows", Sheet: "Data", At: 2, Count: 2}, layout)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ sheet, formula, want string }{
		{"Other", `SUM(Data!$A$2:$A5,"Data!A2",A2,LOG10(A1),Data!$2:$4)`, `SUM(Data!$A$4:$A7,"Data!A2",A2,LOG10(A1),Data!$4:$6)`},
		{"Data", `SUM($B$1:B4,SheetTable[Rate],Other!C5)`, `SUM($B$1:B6,SheetTable[Rate],Other!C5)`},
	}
	for _, c := range cases {
		v, err := a.formula(c.formula, c.sheet)
		if err != nil || v != c.want {
			t.Fatalf("%s: %s %v", c.formula, v, err)
		}
	}
	if _, err = a.formula("SUM(Data:Other!A1)", "Data"); err == nil {
		t.Fatal("3D reference reinterpreted")
	}
	p := xlsxPackage{layout: layout}
	data := []byte(`<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><f t="shared" si="0" ref="A1:A2">Data!B1</f></c></row><row r="2"><c r="A2"><f t="shared" si="0"/></c></row></sheetData></worksheet>`)
	full, err := p.expandStructureGroups(t.Context(), data, "Other", a)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := a.editPart(t.Context(), full, "sheet", "Other", layout)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(moved, []byte("shared")) || !bytes.Contains(moved, []byte(">Data!B1<")) || !bytes.Contains(moved, []byte(">Data!B4<")) {
		t.Fatal(string(moved))
	}
}

func TestDeletedNameAndCrossSheetReferenceRemainExcelErrors(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		book := parts["xl/workbook.xml"]
		parts["xl/workbook.xml"] = strings.Replace(book, "</sheets>", "</sheets><definedNames><definedName name=\"DeletedRate\">'摘要'!#REF!</definedName></definedNames>", 1)
		parts["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><f>IFERROR(DeletedRate,LOG10(100))</f></c><c r="B1"><f>'摘要'!#REF!+1</f></c></row></sheetData></worksheet>`
	})
	book, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	layout, _, err := Inspect(t.Context(), book, "xlsx")
	if err != nil {
		t.Fatal(err)
	}
	src, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	values, err := newObjectValues(t.Context(), src, *layout.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []editdoc.Value{{Kind: "number", Raw: "2"}, {Kind: "error", Raw: "#REF!"}} {
		got, err := values.cell(editdoc.Address{Sheet: "数据", Row: 1, Col: i + 1})
		if err != nil || got.Kind != want.Kind || got.Raw != want.Raw {
			t.Fatalf("column %d: %+v %v", i+1, got, err)
		}
	}
}

func TestStructureWritePhysicalRowsColumnsAndRemoval(t *testing.T) {
	isolateEditFileStorage(t)
	source := writeEditBook(t, nil)
	wb, err := workbook.OpenWorkbook(source)
	if err != nil {
		t.Fatal(err)
	}
	input, err := workbook.OpenEditSource(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	proof, err := input.SHA256(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind              string
		at, count, before int
		ref, want         string
	}{
		{"insertColumns", 2, 1, 0, "E4", "3"}, {"moveColumns", 4, 1, 1, "A4", "3"}, {"deleteRows", 4, 1, 0, "D4", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var out bytes.Buffer
			receipt, err := RewriteStructure(t.Context(), &out, input, editdoc.Structure{Kind: tc.kind, Sheet: wb.Sheets[0].Name, At: tc.at, Count: tc.count, Before: tc.before})
			if err != nil {
				t.Fatal(err)
			}
			archive, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if err = receipt.Verify(t.Context(), archive); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(t.TempDir(), "saved.xlsx")
			if err = os.WriteFile(name, out.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			book, err := workbook.OpenWorkbook(name)
			if err != nil {
				t.Fatal(err)
			}
			l, _, err := Inspect(t.Context(), book, "xlsx")
			if err != nil {
				t.Fatal(err)
			}
			f, err := workbook.OpenEditSource(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			col, row, _ := workbook.ParseCellRef(tc.ref)
			a := editdoc.Address{Sheet: wb.Sheets[0].Name, Row: row, Col: col}
			values, err := l.Read(t.Context(), f, []editdoc.Address{a})
			if err != nil || values[a].Value.Raw != tc.want {
				t.Fatalf("%s: %+v %v", tc.ref, values, err)
			}
		})
	}
	if now, err := input.SHA256(t.Context()); err != nil || now != proof {
		t.Fatal("source changed")
	}
}

func TestChartCacheReadsTypedPointsInOrder(t *testing.T) {
	p := xlsxPackage{layout: XLSXLayout{Sheets: []XLSXSheet{{Name: "A B"}}}}
	values := objectValues{book: &CalculationBook{cells: map[editdoc.Address]calcCell{}}, calculation: &Calculation{}, overlay: map[editdoc.Address]editdoc.Value{}}
	values.overlay[editdoc.Address{Sheet: "A B", Row: 2, Col: 1}] = editdoc.Value{Kind: "number", Raw: "7"}
	values.overlay[editdoc.Address{Sheet: "A B", Row: 4, Col: 1}] = editdoc.Value{Kind: "number", Raw: "2"}
	data := []byte(`<c:chartSpace xmlns:c="` + chartNS + `"><c:numRef><c:f>'A B'!$A$2:$A$4</c:f><c:numCache><c:formatCode>0.00</c:formatCode><c:ptCount val="1"/></c:numCache></c:numRef></c:chartSpace>`)
	out, err := p.chartCaches(context.Background(), data, values)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Cache struct {
			Format string `xml:"formatCode"`
			Count  struct {
				N int `xml:"val,attr"`
			} `xml:"ptCount"`
			Points []struct {
				I int    `xml:"idx,attr"`
				V string `xml:"v"`
			} `xml:"pt"`
		} `xml:"numRef>numCache"`
	}
	if err = xml.Unmarshal(out, &raw); err != nil || raw.Cache.Count.N != 3 || raw.Cache.Format != "0.00" || len(raw.Cache.Points) != 2 || raw.Cache.Points[1].I != 2 || raw.Cache.Points[0].V != "7" {
		t.Fatalf("%s %v", out, err)
	}
	_, err = p.chartCaches(t.Context(), bytes.ReplaceAll(data, []byte("$A$4"), []byte("$B$4")), values)
	if err == nil {
		t.Fatal("2D chart range accepted")
	}
}

func TestStructureCarriesWholeObjectsAndExtendedRuleTargets(t *testing.T) {
	layout := XLSXLayout{Sheets: []XLSXSheet{{Name: "Data"}}}
	move, err := newAxisEdit(editdoc.Structure{Kind: "moveColumns", Sheet: "Data", At: 2, Count: 1, Before: 4}, layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []struct{ role, tag string }{{"pivotTable", "location"}, {"sheet", "mergeCell"}} {
		xml := []byte(`<root xmlns="` + mainNS + `"><` + obj.tag + ` ref="A1:D5"/></root>`)
		if _, err := move.editPart(t.Context(), xml, obj.role, "Data", layout); err == nil {
			t.Fatalf("split %s", obj.tag)
		}
	}
	move, err = newAxisEdit(editdoc.Structure{Kind: "insertRows", Sheet: "Data", At: 2, Count: 1}, layout)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`<worksheet xmlns="` + mainNS + `" xmlns:x14="` + x14NS + `" xmlns:xm="http://schemas.microsoft.com/office/excel/2006/main"><extLst><ext><x14:dataValidation><x14:formula1><xm:f>Data!A2</xm:f></x14:formula1><xm:sqref>B2 D4:D6</xm:sqref></x14:dataValidation></ext></extLst></worksheet>`)
	out, err := move.editPart(t.Context(), data, "sheet", "Data", layout)
	if err != nil || !bytes.Contains(out, []byte(">Data!A3<")) || !bytes.Contains(out, []byte(">B3 D5:D7<")) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestChartDependentRetainsUnrelatedSeriesAndPointFormatting(t *testing.T) {
	p := xlsxPackage{layout: XLSXLayout{Sheets: []XLSXSheet{{Name: "Data"}}}}
	v := objectValues{book: &CalculationBook{cells: map[editdoc.Address]calcCell{}}, calculation: &Calculation{}, overlay: map[editdoc.Address]editdoc.Value{}}
	a := editdoc.Address{Sheet: "Data", Row: 1, Col: 1}
	v.overlay[a] = editdoc.Value{Kind: "number", Raw: "73"}
	v.overlay[editdoc.Address{Sheet: "Data", Row: 3, Col: 1}] = editdoc.Value{Kind: "number", Raw: "2"}
	cache := `<c:numCache><c:formatCode>0</c:formatCode><c:ptCount val="3"/><c:pt idx="0" formatCode="0.00"><c:v>19</c:v></c:pt><c:pt idx="1"><c:v>5</c:v></c:pt><c:extLst><c:ext uri="kept"/></c:extLst></c:numCache>`
	ref := func(area string) string { return `<c:numRef><c:f>Data!` + area + `</c:f>` + cache + `</c:numRef>` }
	stable := ref("$B$1:$B$3")
	source := []byte(`<c:chartSpace xmlns:c="` + chartNS + `">` + ref("$A$1:$A$3") + stable + `</c:chartSpace>`)
	out, err := p.updateChartCaches(t.Context(), source, v, map[string][]editdoc.Address{"Data": {a}})
	if err != nil || !bytes.Contains(out, []byte(stable)) || !bytes.Contains(out, []byte(`formatCode="0.00"><c:v>73</c:v>`)) || bytes.Count(out, []byte(`uri="kept"`)) != 2 || !bytes.Contains(out, []byte(`idx="2"><v>2</v>`)) {
		t.Fatalf("%s %v", out, err)
	}
}
