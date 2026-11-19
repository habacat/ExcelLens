package editfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func addFixtureNames(parts map[string]string, names string) {
	parts["xl/workbook.xml"] = strings.Replace(parts["xl/workbook.xml"], "</sheets>", "</sheets><definedNames>"+names+"</definedNames>", 1)
}

func TestDefinedNamesScopeDependencyAndPreservation(t *testing.T) {
	isolateEditFileStorage(t)
	const names = `<definedName name="_xlnm.Print_Area" localSheetId="0">'数据'!$A$1:$B$12,'数据'!$C$20</definedName>` +
		`<definedName name="_xlnm.Print_Titles" localSheetId="0">'数据'!$1:$2,'数据'!$A:$B</definedName>` +
		`<definedName name="成本" comment="keep &amp; preserve">Edit!$A$1</definedName>` +
		`<definedName name="成本" localSheetId="0">Bridge!$A$1</definedName>` +
		`<definedName name="Alias">成本</definedName>` +
		`<definedName name="Rate">0.25</definedName><definedName name="Label">"one""two"</definedName>`
	path := writeEditBook(t, func(parts map[string]string) {
		structuredFixture(parts)
		addFixtureNames(parts, names)
		part := "xl/worksheets/sheet1.xml"
		parts[part] = strings.Replace(parts[part], "Bridge!A1", "成本*Rate", 1)
		part = "xl/worksheets/sheet6.xml"
		parts[part] = strings.Replace(parts[part], "SUMIF(Edit!B:B,6,Edit!B:B)", "SUM(Alias)", 1)
	})
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wb.ClearIndexes)
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	layout, proof, err := InspectXLSX(t.Context(), f, wb)
	if err != nil {
		t.Fatal(err)
	}
	for _, sheet := range layout.Sheets {
		if sheet.ReadOnly != "" {
			t.Fatalf("%s unexpectedly locked: %s", sheet.Name, sheet.ReadOnly)
		}
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "named-copy.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	addr := editdoc.Address{Sheet: "Edit", Row: 1, Col: 1}
	res, err := WriteXLSX(t.Context(), out, f, layout, proof, map[editdoc.Address]editdoc.Value{addr: {Kind: "text", Raw: "00017"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed["xl/worksheets/sheet6.xml"] || res.Changed["xl/worksheets/sheet7.xml"] {
		t.Fatal(res.Changed)
	}
	if err := VerifyXLSX(t.Context(), out, res); err != nil {
		t.Fatal(err)
	}
	p, err := openXLSXPackage(out, statSize(t, out))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.readPart(t.Context(), "xl/workbook.xml")
	if err != nil || !strings.Contains(string(raw), "<definedNames>"+names+"</definedNames>") {
		t.Fatal("name XML changed", err)
	}
}

func TestDefinedNameBoundaries(t *testing.T) {
	isolateEditFileStorage(t)
	for _, tc := range []struct {
		title, names string
		valid        bool
	}{
		{"print metadata", `<definedName localSheetId="0" name="_xlnm.Print_Area">数据!$A$1:$D$50</definedName>`, true},
		{"external", `<definedName name="External">'[Other.xlsx]数据'!$A$1</definedName>`, false},
		{"dynamic", `<definedName name="Dynamic">OFFSET(数据!$A$1,0,1)</definedName>`, false},
		{"macro", `<definedName name="Macro" xlm="1">数据!$A$1</definedName>`, false},
		{"auto entry", `<definedName name="_xlnm.Auto_Open">数据!$A$1</definedName>`, false},
		{"bad scope", `<definedName name="Scoped" localSheetId="2">数据!$A$1</definedName>`, false},
		{"wrong sheet", `<definedName name="_xlnm.Print_Area" localSheetId="1">数据!$A$1</definedName>`, false},
		{"partial titles", `<definedName name="_xlnm.Print_Titles" localSheetId="0">数据!$A$1:$B$5</definedName>`, false},
		{"cycle", `<definedName name="Alpha">Beta</definedName><definedName name="Beta">Alpha</definedName>`, false},
		{"duplicate constant", `<definedName name="rate">0.1</definedName><definedName name="RATE">0.2</definedName>`, false},
	} {
		t.Run(tc.title, func(t *testing.T) {
			path := writeEditBook(t, func(parts map[string]string) { addFixtureNames(parts, tc.names) })
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			p, err := openXLSXPackage(f, statSize(t, f))
			if err == nil {
				err = p.inspect(t.Context())
			}
			if (err == nil) != tc.valid || tc.valid && p.layout.Structured {
				t.Fatalf("structured=%v err=%v", p.layout.Structured, err)
			}
		})
	}
}
