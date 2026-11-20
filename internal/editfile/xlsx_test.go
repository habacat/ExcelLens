package editfile

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

const testSheet1 = `<p:worksheet xmlns:p="` + mainNS + `" xmlns:unused="urn:unused"><p:dimension ref="A1:D4"/><p:sheetData>` +
	`<p:row r="1"><p:c r="A1" s="1" t="s"><p:v>0</p:v></p:c><!-- keep -->` +
	`<p:c r="B1" t="s"><p:v>0</p:v></p:c><p:c r="D1"/></p:row>` +
	`<p:row r="2" ht="28" customHeight="1"/>` +
	`<p:row r="4"><p:c r="D4" s="1"><p:v>3</p:v></p:c></p:row></p:sheetData>` +
	`<p:pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/></p:worksheet>`

const fixtureCustomProps = `<Properties xmlns="` + customPropsNS + `" xmlns:vt="` + variantTypeNS + `">` +
	`<property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="Code"><vt:lpwstr>00012</vt:lpwstr></property>` +
	`<property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="3" name="Flag"><vt:bool>true</vt:bool></property></Properties>`

func addFixtureCustomProps(parts map[string]string, content string) {
	parts["docProps/custom.xml"] = content
	parts["[Content_Types].xml"] = strings.Replace(parts["[Content_Types].xml"], "</Types>", `<Override PartName="/docProps/custom.xml" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/></Types>`, 1)
	parts["_rels/.rels"] = strings.Replace(parts["_rels/.rels"], "</Relationships>", `<Relationship Id="rId2" Type="`+relationNS+`/custom-properties" Target="docProps/custom.xml"/></Relationships>`, 1)
}

func writeEditBook(t *testing.T, change func(map[string]string)) string {
	t.Helper()
	parts := map[string]string{
		"_rels/.rels":                `<Relationships xmlns="` + packageNS + `"><Relationship Id="rId1" Type="` + relationNS + `/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<workbook xmlns="` + mainNS + `" xmlns:r="` + relationNS + `"><workbookProtection/><sheets><sheet name="数据" sheetId="1" r:id="rId1"/><sheet name="摘要" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="` + packageNS + `"><Relationship Id="rId1" Type="` + relationNS + `/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="` + relationNS + `/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="` + relationNS + `/styles" Target="styles.xml"/><Relationship Id="rId4" Type="` + relationNS + `/sharedStrings" Target="sharedStrings.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="` + mainNS + `" count="3" uniqueCount='1'><si><t>00120 &amp; 中</t></si></sst>`,
		"xl/styles.xml":              `<styleSheet xmlns="` + mainNS + `"><fonts count="1"><font/></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf fontId="0" fillId="0" borderId="0" numFmtId="0"/></cellStyleXfs><cellXfs count="2"><xf fontId="0" fillId="0" borderId="0" numFmtId="0" xfId="0"/><xf fontId="0" fillId="0" borderId="0" numFmtId="2" xfId="0"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   testSheet1,
		"xl/worksheets/sheet2.xml":   `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row></sheetData></worksheet>`,
	}
	var types strings.Builder
	types.WriteString(`<Types xmlns="` + contentNS + `"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	for name := range parts {
		if c := classifyPart(name); c.role != "rels" {
			fmt.Fprintf(&types, `<Override PartName="/%s" ContentType="%s"/>`, name, c.mime)
		}
	}
	types.WriteString(`</Types>`)
	parts["[Content_Types].xml"] = types.String()
	if change != nil {
		change(parts)
	}
	path := filepath.Join(t.TempDir(), "test.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	var names []string
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, parts[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFormulaNumberFormatReusesXFWithoutChangingProtectionOrAlignment(t *testing.T) {
	const base = `<xf numFmtId="0" fontId="3" borderId="2" applyAlignment="1"><alignment horizontal="right"/><protection locked="0"/></xf>`
	const percent = `<xf numFmtId="10" applyNumberFormat="1"/>`
	styles := `<styleSheet xmlns="` + mainNS + `"><cellXfs count="2">` + base + percent + `</cellXfs></styleSheet>`
	addr := editdoc.Address{Sheet: "数据", Row: 2, Col: 1}
	v := editdoc.Value{Kind: "formula", Raw: "=B2+1", Style: "0", NumberStyle: "1"}
	for pass := 0; pass < 2; pass++ {
		path := writeEditBook(t, func(parts map[string]string) { parts["xl/styles.xml"] = styles })
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := openXLSXPackage(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if err = p.readStyles(t.Context(), []byte(styles)); err != nil {
			t.Fatal(err)
		}
		edits, patches, err := p.materializeNumberStyles(t.Context(), map[editdoc.Address]editdoc.Value{addr: v})
		if err != nil || edits[addr].Style != "2" || edits[addr].NumberStyle != "" {
			t.Fatal(edits, err)
		}
		if pass == 1 {
			if len(patches) != 0 {
				t.Fatal("reopening the saved workbook added a duplicate style")
			}
			continue
		}
		var dst strings.Builder
		if err = writeXMLPatches(t.Context(), &dst, []byte(styles), patches); err != nil {
			t.Fatal(err)
		}
		styles = dst.String()
		if !strings.Contains(styles, base+percent) || strings.Count(styles, `<alignment horizontal="right"/><protection locked="0"/>`) != 2 || !strings.Contains(styles, `<cellXfs count="3">`) {
			t.Fatal(styles)
		}
	}
}

func TestXLSXEditsPreservePartsTypesAndCoordinates(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		addFixtureCustomProps(parts, fixtureCustomProps)
		for _, dir := range []string{"_rels/", "xl/", "xl/_rels/", "xl/worksheets/", "xl/theme/"} {
			parts[dir] = ""
		}
	})
	w, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.ClearIndexes)
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := w.CheckEditSource(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	layout, proof, err := InspectXLSX(t.Context(), f, w)
	if err != nil {
		t.Fatal(err)
	}
	a := func(r, c int) editdoc.Address { return editdoc.Address{Sheet: "数据", Row: r, Col: c} }
	edits := map[editdoc.Address]editdoc.Value{
		a(1, 1): {Kind: "text", Raw: "  =000123<&\r\n保留  "},
		a(1, 3): {Kind: "text", Raw: "00009876543210987654321"},
		a(2, 2): {Kind: "number", Raw: "1234567890.12345"},
		a(3, 3): {Kind: "boolean", Raw: "FALSE"},
		a(4, 4): {Kind: "blank"},
	}
	original, err := ReadXLSXCells(t.Context(), f, layout, []editdoc.Address{a(1, 1), a(2, 2), a(3, 3), a(4, 4)})
	if err != nil || original[a(1, 1)].Value.Raw != "00120 & 中" || original[a(2, 2)].Value.Kind != "missing" || original[a(3, 3)].Value.Kind != "missing" || original[a(4, 4)].Value.Raw != "3" {
		t.Fatal(original, err)
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "copy.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := WriteXLSX(t.Context(), out, f, layout, proof, edits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 2 || !result.Changed["xl/worksheets/sheet1.xml"] || !result.Changed["xl/sharedStrings.xml"] {
		t.Fatal(result.Changed)
	}
	if err := VerifyXLSX(t.Context(), out, result); err != nil {
		t.Fatal(err)
	}
	zipCopy, err := openXLSXPackage(out, statSize(t, out))
	if err != nil {
		t.Fatal(err)
	}
	properties, err := zipCopy.readPart(t.Context(), "docProps/custom.xml")
	if err != nil || string(properties) != fixtureCustomProps {
		t.Fatal("custom properties did not round-trip", err)
	}
	for _, entry := range zipCopy.zip.File {
		if entry.Mode().IsDir() {
			t.Fatalf("unexpected directory record in copy: %s", entry.Name)
		}
	}
	updated, err := zipCopy.readPart(t.Context(), "xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<!-- keep --><p:c r="B1" t="s"><p:v>0</p:v></p:c>`, `<p:row r="2" ht="28" customHeight="1">`, `xmlns:unused="urn:unused"`} {
		if !bytes.Contains(updated, []byte(want)) {
			t.Fatalf("lost untouched XML %s", want)
		}
	}
	if !bytes.Contains(updated, []byte(`<t xml:space="preserve">  =000123`)) || !bytes.Contains(updated, []byte(`<t>00009876543210987654321</t>`)) {
		t.Fatal("inline text needs xml:space only where boundary whitespace is significant")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workbook.OpenWorkbook(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.ClearIndexes)
	idx, err := reopened.BuildIndex(reopened.Sheets[0], nil, t.Context().Done())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.ReadRowsByNumbersWithOptionsContext(t.Context(), idx, []int{1, 2, 3, 4}, 1, 4, workbook.RowReadOptions{})
	if err != nil || len(rows) != 4 {
		t.Fatal(rows, err)
	}
	cell := rows[0].Cells[0]
	if cell.Raw != edits[a(1, 1)].Raw || cell.Style != 1 || cell.Type != "inlineStr" || cell.HasFormula || rows[0].Cells[1].Raw != "00120 & 中" || rows[1].Cells[0].Raw != "1234567890.12345" || rows[2].Cells[0].Type != "b" {
		t.Fatal(rows)
	}
	input, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(input)) != proof {
		t.Fatal("source modified", err)
	}
}

func statSize(t *testing.T, f *os.File) int64 {
	t.Helper()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestXLSXPreflightRejectsUnsupportedDependencies(t *testing.T) {
	isolateEditFileStorage(t)
	for _, test := range []struct {
		name  string
		alter func(map[string]string)
	}{
		{"array formula", func(p map[string]string) {
			p["xl/worksheets/sheet1.xml"] = strings.Replace(testSheet1, "<p:v>3</p:v>", `<p:f t="array" ref="D4:D5">A1</p:f><p:v>3</p:v>`, 1)
		}},
		{"referenced validation list", func(p map[string]string) {
			p["xl/worksheets/sheet1.xml"] = strings.Replace(testSheet1, `<p:pageMargins`, `<p:dataValidations count="1"><p:dataValidation type="list" sqref="A1"><p:formula1>$D$1:$D$4</p:formula1></p:dataValidation></p:dataValidations><p:pageMargins`, 1)
		}},
		{"workbook protection", func(p map[string]string) {
			p["xl/workbook.xml"] = strings.Replace(p["xl/workbook.xml"], `<workbookProtection/>`, `<workbookProtection lockStructure="1"/>`, 1)
		}},
		{"unknown part", func(p map[string]string) { p["xl/charts/chart1.xml"] = "<chart/>" }},
		{"linked custom property", func(p map[string]string) {
			addFixtureCustomProps(p, strings.Replace(fixtureCustomProps, `name="Code"`, `name="Code" linkTarget="ExistingName"`, 1))
		}},
		{"misdeclared custom properties", func(p map[string]string) {
			addFixtureCustomProps(p, fixtureCustomProps)
			p["[Content_Types].xml"] = strings.Replace(p["[Content_Types].xml"], "application/vnd.openxmlformats-officedocument.custom-properties+xml", "application/xml", 1)
		}},
		{"part shadowed by directory", func(p map[string]string) { p["XL/workbook.xml/"] = "" }},
		{"directory under part", func(p map[string]string) { p["xl/workbook.xml/nested/"] = "" }},
		{"noncanonical directory", func(p map[string]string) { p["xl/./worksheets/"] = "" }},
		{"external link", func(p map[string]string) {
			p["_rels/.rels"] = strings.Replace(p["_rels/.rels"], `Target="xl/workbook.xml"`, `TargetMode="External" Target="xl/workbook.xml"`, 1)
		}},
		{"mixed rich and plain string", func(p map[string]string) {
			p["xl/sharedStrings.xml"] = strings.Replace(p["xl/sharedStrings.xml"], "<si>", "<si><r><t>run</t></r>", 1)
		}},
		{"undeclared markup", func(p map[string]string) {
			p["xl/worksheets/sheet1.xml"] = strings.Replace(testSheet1, "<p:pageMargins", "<unused:extension/><p:pageMargins", 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeEditBook(t, test.alter)
			w, err := workbook.OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(w.ClearIndexes)
			f, err := workbook.OpenEditSource(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, _, err := InspectXLSX(t.Context(), f, w); !errors.Is(err, ErrXLSX) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
