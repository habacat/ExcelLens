package editfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

const (
	fixturePivot      = "xl/pivotTables/pivotTable1.xml"
	fixturePivotCache = "xl/pivotCache/pivotCacheDefinition1.xml"
)

func structuredFixture(parts map[string]string) {
	insert := func(part, closing, fragment string) {
		parts[part] = strings.Replace(parts[part], closing, fragment+closing, 1)
	}
	addPart := func(name, text string) {
		parts[name] = text
		if kind := classifyPart(name); kind.role != "rels" {
			insert("[Content_Types].xml", "</Types>", fmt.Sprintf(`<Override PartName="/%s" ContentType="%s"/>`, name, kind.mime))
		}
	}
	addSheet := func(id int, name, xml string) {
		base := fmt.Sprintf("worksheets/sheet%d.xml", id)
		link := fmt.Sprintf("sheet%d", id)
		addPart("xl/"+base, xml)
		insert("xl/workbook.xml", "</sheets>", fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="%s"/>`, name, id, link))
		insert("xl/_rels/workbook.xml.rels", "</Relationships>", fmt.Sprintf(`<Relationship Id="%s" Type="%s/worksheet" Target="%s"/>`, link, relationNS, base))
	}
	sheet := func(rows, extras string) string {
		return `<worksheet xmlns="` + mainNS + `"><sheetData>` + rows + `</sheetData>` + extras + `</worksheet>`
	}
	relation := func(id, kind, target string) string {
		return fmt.Sprintf(`<Relationship Id="%s" Type="%s/%s" Target="%s"/>`, id, relationNS, kind, target)
	}
	relations := func(id, kind, target string) string {
		return `<Relationships xmlns="` + packageNS + `">` + relation(id, kind, target) + `</Relationships>`
	}

	parts["xl/worksheets/sheet1.xml"] = sheet(`<row r="1"><c r="A1" t="inlineStr"><is><t>Amount</t></is></c></row><row r="2"><c r="A2"><f>Bridge!A1</f><v>10</v></c></row>`, "")
	addSheet(3, "Bridge", sheet(`<row r="1"><c r="A1"><f>Input!A1</f><v>10</v></c></row>`, ""))
	addSheet(4, "Input", sheet(`<row r="1"><c r="A1"><v>10</v></c></row>`, ""))
	addSheet(5, "Edit", sheet(
		`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><f t="shared" si="0" ref="B1:B2">SUM(A:A)</f><v>6</v></c><c r="D1"><v>7</v></c><c r="F1"><v>8</v></c></row>`+
			`<row r="2"><c r="B2"><f t="shared" si="0"/><v>6</v></c></row>`,
		`<mergeCells count="1"><mergeCell ref="D1:E1"/></mergeCells>`))
	addSheet(6, "Dependent", sheet(`<row r="1"><c r="A1"><f>SUMIF(Edit!B:B,6,Edit!B:B)</f><v>12</v></c></row>`, ""))
	addSheet(7, "Separate", sheet(`<row r="1"><c r="A1"><f>1+2</f><v>3</v></c></row>`, ""))

	addPart(fixturePivotCache, `<pivotCacheDefinition xmlns="`+mainNS+`"><cacheSource type="worksheet"><worksheetSource sheet="数据" ref="A1:A2"/></cacheSource><cacheFields count="1"><cacheField name="Amount"/></cacheFields></pivotCacheDefinition>`)
	addPart(fixturePivot, `<pivotTableDefinition xmlns="`+mainNS+`" name="Totals" cacheId="1" dataCaption="Values"><location ref="A1" firstHeaderRow="1" firstDataRow="1" firstDataCol="1"/><pivotFields count="1"><pivotField/></pivotFields></pivotTableDefinition>`)
	addPart("xl/worksheets/_rels/sheet2.xml.rels", relations("p", "pivotTable", "../pivotTables/pivotTable1.xml"))
	addPart("xl/pivotTables/_rels/pivotTable1.xml.rels", relations("c", "pivotCacheDefinition", "../pivotCache/pivotCacheDefinition1.xml"))
	insert("xl/_rels/workbook.xml.rels", "</Relationships>", relation("p", "pivotCacheDefinition", "pivotCache/pivotCacheDefinition1.xml"))
	insert("xl/workbook.xml", "</workbook>", `<pivotCaches><pivotCache cacheId="1" r:id="p"/></pivotCaches>`)
	addPart(calcChainPart, `<calcChain xmlns="`+mainNS+`"><c i="3" r="A1"/><c i="1" r="A2"/><c i="5" r="B1"/><c r="B2"/><c i="6" r="A1"/><c i="7" r="A1"/></calcChain>`)
	insert("xl/_rels/workbook.xml.rels", "</Relationships>", relation("chain", "calcChain", "calcChain.xml"))
}

func TestXLSXStructuredLiteralPreservesSharedFormulasAndOwnedRanges(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, structuredFixture)
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
	layout, proof, err := InspectXLSX(t.Context(), src, wb)
	if err != nil || !layout.Structured {
		t.Fatal(layout, err)
	}
	for _, sheet := range layout.Sheets {
		if sheet.ReadOnly != "" {
			t.Fatalf("%s unexpectedly locked: %s", sheet.Name, sheet.ReadOnly)
		}
	}
	pos := func(sheet string, row, col int) editdoc.Address {
		return editdoc.Address{Sheet: sheet, Row: row, Col: col}
	}
	edited := pos("Edit", 1, 1)
	edits := map[editdoc.Address]editdoc.Value{edited: {Kind: "text", Raw: "0000123"}}
	locks := map[editdoc.Address]string{
		pos("数据", 1, 1): lockPivotHeader, pos(layout.Sheets[1].Name, 1, 1): lockPivotResult, pos("Edit", 1, 5): lockMergedMember,
	}
	for addr, want := range locks {
		batch, err := (Layout{XLSX: &layout}).ReadBatch(t.Context(), src, []editdoc.Address{edited, addr}, nil)
		if err != nil {
			t.Fatal(err)
		}
		source := batch.cells[addr]
		if source.ReadOnly != want {
			t.Fatal(addr, source)
		}
		_, kept, err := batch.Validate(t.Context(), []editdoc.Change{
			{Address: edited, Value: edits[edited]}, {Address: addr, Value: edits[edited]},
		}, nil)
		if err == nil || len(kept) != 0 || !strings.Contains(err.Error(), want) {
			t.Fatal(addr, kept, err)
		}
	}
	if _, err := WriteXLSX(t.Context(), &strings.Builder{}, src, layout, proof, map[editdoc.Address]editdoc.Value{pos("Edit", 1, 5): edits[edited]}, nil); err == nil {
		t.Fatal("writer independently accepted merged cell")
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "copy.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	result, err := WriteXLSX(t.Context(), out, src, layout, proof, edits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 4 || !result.Changed["xl/worksheets/sheet5.xml"] || !result.Changed["xl/worksheets/sheet6.xml"] ||
		!result.Changed["xl/workbook.xml"] || !result.Changed["xl/sharedStrings.xml"] || result.FormulasPending != 3 {
		t.Fatal(result.Changed, result.FormulasPending)
	}
	if err := VerifyXLSX(t.Context(), out, result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Parts[calcChainPart]; !ok || result.Changed[calcChainPart] {
		t.Fatal("literal edit did not preserve the calculation chain")
	}
	p, err := openXLSXPackage(out, statSize(t, out))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.readPart(t.Context(), "xl/worksheets/sheet5.xml")
	if err != nil || !strings.Contains(string(raw), `<f t="shared" si="0"/`) ||
		!strings.Contains(string(raw), `<mergeCell ref="D1:E1"/>`) {
		t.Fatal("shared or merge XML lost", err)
	}
}

func TestXLSXStructuredPreflightRefusesUnprovenDependencies(t *testing.T) {
	isolateEditFileStorage(t)
	for _, tc := range []struct{ name, part, from, to string }{
		{"named pivot source", fixturePivotCache, `ref="A1:A2"`, `name="DataSet"`},
		{"different cache", fixturePivot, `cacheId="1"`, `cacheId="2"`},
		{"external pivot records", "xl/pivotTables/_rels/pivotTable1.xml.rels", `Target="../`, `TargetMode="External" Target="https://example.invalid/`},
		{"indirect source", "xl/worksheets/sheet1.xml", "Bridge!A1", `INDIRECT(Edit!A1)`},
		{"orphaned shared formula", "xl/worksheets/sheet5.xml", `si="0"/`, `si="1"/`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEditBook(t, func(parts map[string]string) {
				structuredFixture(parts)
				parts[tc.part] = strings.Replace(parts[tc.part], tc.from, tc.to, 1)
			})
			wb, err := workbook.OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			defer wb.ClearIndexes()
			src, err := workbook.OpenEditSource(path)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			if _, _, err := InspectXLSX(t.Context(), src, wb); !errors.Is(err, ErrXLSX) {
				t.Fatalf("preflight accepted unverified dependency: %v", err)
			}
		})
	}
}

func TestStructuredSharedArrayAndSparseEdits(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		structuredFixture(parts)
		k := "xl/worksheets/sheet5.xml"
		parts[k] = strings.Replace(parts[k], "<sheetData>", `<dimension ref="A1:F3"/><cols><col min="10" max="10" style="1"/></cols><sheetData>`, 1)
		parts[k] = strings.Replace(parts[k], `<row r="2">`, `<row r="2" spans="1:6">`, 1)
		parts[k] = strings.Replace(parts[k], "</sheetData>", `<row r="3"><c r="A3" s="1"><f t="array" ref="A3:C3">A1:C1*2</f><v>14</v></c><c r="B3" t="str"><v>old</v></c><c r="C3"><v>3</v></c></row></sheetData>`, 1)
		parts["xl/worksheets/sheet7.xml"] = `<m:worksheet xmlns:m="` + mainNS + `"><m:dimension ref="C4"/><m:sheetData/></m:worksheet>`
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
	if err != nil {
		t.Fatal(err)
	}
	addr := func(sheet string, r, c int) editdoc.Address { return editdoc.Address{Sheet: sheet, Row: r, Col: c} }
	edit := func(r, c int) editdoc.Address { return addr("Edit", r, c) }
	before, err := ReadXLSXCells(t.Context(), f, l, []editdoc.Address{edit(1, 2), edit(2, 2), edit(3, 2), edit(2, 10)})
	if err != nil || before[edit(2, 2)].Value.Raw != "=SUM(A:A)" || before[edit(3, 2)].ReadOnly != lockArrayMember || before[edit(2, 10)].Value.Style != "1" {
		t.Fatal(before, err)
	}
	for _, spec := range []struct {
		name   string
		target editdoc.Address
	}{
		{"master", edit(1, 2)}, {"member", edit(2, 2)},
	} {
		t.Run(spec.name, func(t *testing.T) {
			edits := map[editdoc.Address]editdoc.Value{
				edit(1, 4):  {Kind: "formula", Raw: "=1+2"},
				spec.target: {Kind: "formula", Raw: "=SUMIF(A:A,1,$B:$B)"},
				edit(3, 1):  {Kind: "formula", Raw: "=A1:C1*3"}, edit(2, 10): {Kind: "number", Raw: "7"},
				addr("Separate", 1, 1): {Kind: "text", Raw: "0009"}, addr("Separate", 1048576, 16384): {Kind: "formula", Raw: "=1+1"},
			}
			out, err := os.Create(filepath.Join(t.TempDir(), "grid.xlsx"))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			result, err := WriteXLSX(t.Context(), out, f, l, proof, edits, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyXLSX(t.Context(), out, result); err != nil {
				t.Fatal(err)
			}
			if !result.removedCalcChain || !result.Changed["[Content_Types].xml"] || !result.Changed["xl/_rels/workbook.xml.rels"] {
				t.Fatal(result.Changed)
			}
			p, err := openXLSXPackage(out, statSize(t, out))
			if err != nil {
				t.Fatal(err)
			}
			sh, err := p.readPart(t.Context(), "xl/worksheets/sheet5.xml")
			if err != nil {
				t.Fatal(err)
			}
			for _, token := range []string{`t="shared"`, ` spans=`, `<v>old</v>`, `<c r="C3"><v>3`} {
				if strings.Contains(string(sh), token) {
					t.Fatalf("stale %s in %s", token, sh)
				}
			}
			for _, token := range []string{`ref="A1:J3"`, `ref="A3:C3">A1:C1*3</f>`, `>SUM(A:A)</f>`} {
				if !strings.Contains(string(sh), token) {
					t.Fatalf("missing %s in %s", token, sh)
				}
			}
			empty, _ := p.readPart(t.Context(), "xl/worksheets/sheet7.xml")
			if !strings.Contains(string(empty), `ref="A1:XFD1048576"`) || !strings.Contains(string(empty), "</m:sheetData>") {
				t.Fatalf("self-closing source: %s", empty)
			}
		})
	}
	for _, c := range []editdoc.Change{
		{Address: edit(3, 2), Value: editdoc.Value{Kind: "number", Raw: "2"}},
		{Address: edit(3, 1), Value: editdoc.Value{Kind: "blank"}},
		{Address: edit(2, 10), Value: editdoc.Value{Kind: "formula", Raw: `=INDIRECT(A1)`}},
	} {
		b, err := (Layout{XLSX: &l}).ReadBatch(t.Context(), f, []editdoc.Address{c.Address}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = b.Validate(t.Context(), []editdoc.Change{c}, nil)
		if err == nil {
			t.Fatal("committed", c)
		}
	}
}
