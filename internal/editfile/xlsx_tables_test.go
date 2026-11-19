package editfile

import (
	"errors"
	"os"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func addTableFixture(parts map[string]string) {
	const pt = "xl/tables/table1.xml"
	parts["[Content_Types].xml"] = strings.Replace(parts["[Content_Types].xml"], "</Types>", `<Override PartName="/`+pt+`" ContentType="`+spreadMIME+`table+xml"/></Types>`, 1)
	parts["xl/worksheets/_rels/sheet1.xml.rels"] = `<Relationships xmlns="` + packageNS + `"><Relationship Id="table" Type="` + relationNS + `/table" Target="../tables/table1.xml"/></Relationships>`
	parts[pt] = `<table xmlns="` + mainNS + `" id="3" displayName="Sales" ref="A1:B5" totalsRowCount="1"><tableColumns count="2"><tableColumn id="5" name="Qty"/><tableColumn id="9" name="Cost" totalsRowFunction="sum"><calculatedColumnFormula>Sales[[#This Row],[Qty]]*2</calculatedColumnFormula></tableColumn></tableColumns></table>`
	row := func(ref, n string) string {
		return `<row r="` + ref + `"><c r="A` + ref + `"><v>` + n + `</v></c><c r="B` + ref + `"><f>Sales[[#This Row],[Qty]]*2</f></c></row>`
	}
	parts["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="` + mainNS + `" xmlns:r="` + relationNS + `"><sheetData><row r="1"><c r="A1" t="str"><v>Qty</v></c><c r="B1" t="str"><v>Cost</v></c></row>` + row("2", "2") + strings.Replace(row("3", "3"), `r="3"`, `r="3" hidden="1"`, 1) + row("4", "4") + `<row r="5"><c r="B5"><f>SUBTOTAL(109,Sales[Cost])</f></c></row></sheetData><tableParts count="1"><tablePart r:id="table"/></tableParts></worksheet>`
	parts["xl/styles.xml"] = strings.Replace(parts["xl/styles.xml"], `numFmtId="2" xfId="0"/>`, `numFmtId="2" xfId="0" applyProtection="0"><protection locked="false"/></xf>`, 1)
	parts["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><cols><col min="2" max="2" style="1"/></cols><sheetData><row r="2" s="1" customFormat="1"><c r="A2"/><c r="C2" s="1"/></row><row r="3"><c r="D3"><f>SUM(Sales[Cost])</f></c></row></sheetData><sheetProtection sheet="1" password="A123"/></worksheet>`
}

func TestTableGridCalculatedColumnAndEffectiveProtection(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, addTableFixture)
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
	ctx := t.Context()
	layout, proof, err := InspectXLSX(ctx, src, wb)
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{XLSX: &layout}
	doc := &editdoc.Document{}
	a := func(s string, r, c int) editdoc.Address { return editdoc.Address{Sheet: s, Row: r, Col: c} }
	seed := a("数据", 3, 2)
	anchor := a("数据", 2, 2)
	total := a("数据", 5, 2)
	cross := a("摘要", 3, 4)
	plan, err := l.PrepareChanges(ctx, src, []editdoc.Change{{Address: seed, Value: editdoc.Value{Kind: "formula", Raw: "=[@Qty]*3"}}}, doc)
	if err != nil || len(plan.Changes) != 3 || len(plan.Templates) != 1 || plan.Templates[0].Address != anchor || plan.Templates[0].Formula != "=Sales[[#This Row],[Qty]]*3" {
		t.Fatal(plan, err)
	}
	edits := map[editdoc.Address]editdoc.Value{}
	for _, c := range plan.Changes {
		edits[c.Address] = c.Value
	}
	calc, err := NewCalculationBook(ctx, src, layout)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := calc.Calculate(ctx, edits, nil)
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[editdoc.Address]string{anchor: "6", seed: "9", total: "18", cross: "27"} {
		if result := rev.Cells[addr]; result.State != "calculated" || result.Value.Raw != want {
			t.Fatal(addr, result, want)
		}
	}
	out, err := os.CreateTemp(t.TempDir(), "*.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	edits[total] = editdoc.Value{Kind: "text", Raw: "R&D 五号"}
	result, err := writeXLSX(ctx, out, src, layout, proof, edits, nil, map[editdoc.Address]string{anchor: plan.Templates[0].Formula})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyXLSX(ctx, out, result); err != nil {
		t.Fatal(err)
	}
	c := result.Layout.Tables[0].Columns[1]
	if c.Function != "" || c.Label != "R&D 五号" || c.Formula != plan.Templates[0].Formula {
		t.Fatal(c)
	}
	for _, test := range []struct {
		address editdoc.Address
		locked  bool
	}{{a("摘要", 2, 1), true}, {a("摘要", 2, 3), false}, {a("摘要", 2, 4), false}, {a("摘要", 15, 2), false}, {a("摘要", 15, 3), true}, {a("数据", 1, 1), true}} {
		batch, err := l.ReadBatch(ctx, src, []editdoc.Address{test.address}, nil)
		if err != nil {
			t.Fatal(err)
		}
		change := []editdoc.Change{{Address: test.address, Value: editdoc.Value{Kind: "number", Raw: "7"}}}
		_, _, err = batch.Validate(ctx, change, doc)
		if (err != nil) != test.locked {
			t.Fatal(test, err)
		}
		change[0].Value = editdoc.Value{Style: "1"}
		if _, _, err = batch.Validate(ctx, change, doc); err == nil {
			t.Fatal("changed protected or header style", test)
		}
	}
	// The style-only operation must never replace a calculated cell or its column template.
	style := map[editdoc.Address]editdoc.Value{seed: {Style: "1"}}
	styled, err := os.CreateTemp(t.TempDir(), "*.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer styled.Close()
	result, err = WriteXLSX(ctx, styled, src, layout, proof, style, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyXLSX(ctx, styled, result); err != nil {
		t.Fatal(err)
	}
	if !equalTables(layout.Tables, result.Layout.Tables) || result.cells[seed].Value.Kind != "formula" {
		t.Fatal(result.cells[seed])
	}
}

func TestTableBindingsRejectAmbiguousOrMissingOwners(t *testing.T) {
	isolateEditFileStorage(t)
	for _, alter := range []func(map[string]string){
		func(p map[string]string) {
			p["xl/worksheets/sheet1.xml"] = strings.Replace(p["xl/worksheets/sheet1.xml"], `<tableParts count="1"><tablePart r:id="table"/></tableParts>`, "", 1)
		},
		func(p map[string]string) {
			p["xl/tables/table1.xml"] = strings.Replace(p["xl/tables/table1.xml"], `name="Cost"`, `name="qty"`, 1)
		},
		func(p map[string]string) {
			p["xl/tables/table1.xml"] = strings.Replace(p["xl/tables/table1.xml"], `id="3"`, `id="3" tableType="queryTable"`, 1)
		},
	} {
		path := writeEditBook(t, func(p map[string]string) { addTableFixture(p); alter(p) })
		wb, err := workbook.OpenWorkbook(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(wb.ClearIndexes)
		_, _, err = Inspect(t.Context(), wb, "xlsx")
		if !errors.Is(err, ErrXLSX) {
			t.Fatal(err)
		}
	}
}

func TestWholeTableColumnPasteRecognizesUniformFormulas(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, addTableFixture)
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
	ctx := t.Context()
	layout, proof, err := InspectXLSX(ctx, src, wb)
	if err != nil {
		t.Fatal(err)
	}
	driver := Layout{XLSX: &layout}
	anchor := editdoc.Address{Sheet: "数据", Row: 2, Col: 2}
	changes := []editdoc.Change{
		{Address: editdoc.Address{Sheet: anchor.Sheet, Row: 4, Col: 2}, Value: editdoc.Value{Kind: "number", Raw: "7"}},
		{Address: anchor, Value: editdoc.Value{Kind: "number", Raw: "1"}},
		{Address: editdoc.Address{Sheet: anchor.Sheet, Row: 3, Col: 2}, Value: editdoc.Value{Kind: "formula", Raw: "=A3*9"}},
	}
	for i, seed := range []editdoc.Value{{Kind: "number", Raw: "1"}, {Kind: "formula", Raw: "=A2*4"}, {Kind: "formula", Raw: "=A2*9"}} {
		changes[1].Value = seed
		if i == 2 {
			changes[0].Value = editdoc.Value{Kind: "formula", Raw: "=A4*9"}
		}
		plan, err := driver.PrepareChanges(ctx, src, changes, &editdoc.Document{})
		want := ""
		if i == 2 {
			want = seed.Raw
		}
		if err != nil || len(plan.Templates) != 1 || plan.Templates[0].Formula != want || len(plan.Changes) != 3 {
			t.Fatal(plan, err)
		}
		edits := map[editdoc.Address]editdoc.Value{}
		for _, c := range plan.Changes {
			edits[c.Address] = c.Value
		}
		out, err := os.CreateTemp(t.TempDir(), "*.xlsx")
		if err != nil {
			t.Fatal(err)
		}
		result, err := writeXLSX(ctx, out, src, layout, proof, edits, nil, map[editdoc.Address]string{anchor: want})
		if err == nil {
			err = VerifyXLSX(ctx, out, result)
		}
		out.Close()
		if err != nil || result.Layout.Tables[0].Columns[1].Formula != want {
			t.Fatal(want, err)
		}
	}
}

func TestTableReferenceCaptionsAndTotalsLabels(t *testing.T) {
	l := XLSXLayout{Tables: []xlsxTable{{Name: "Sales", Sheet: "Data", Ref: "A1:C5", Headers: 1, Totals: 1, Columns: []xlsxTableColumn{{Name: "Item"}, {Name: "#Fee"}, {Name: "A]Z"}}}}}
	a := editdoc.Address{Sheet: "Data", Row: 3, Col: 1}
	for _, test := range []struct {
		raw, canonical           string
		top, left, bottom, right int
	}{
		{"[@'#Fee]", "Sales[[#This Row],['#Fee]]", 3, 2, 3, 2},
		{"Sales[[#Data],[Item]:[A']Z]]", "Sales[[#Data],[Item]:[A']Z]]", 2, 1, 4, 3},
		{"Sales[#Totals]", "Sales[#Totals]", 5, 1, 5, 3},
	} {
		ref, stored, ok := l.tableReference(test.raw, a)
		if !ok || stored != test.canonical || ref.Top != test.top || ref.Left != test.left || ref.Bottom != test.bottom || ref.Right != test.right {
			t.Fatal(test, ref, stored)
		}
	}
	for _, bad := range []string{"Other[Item]", "Sales[unknown]", "Sales[[Item],[A']Z]]", "[external.xlsx]Page!A1"} {
		if _, _, ok := l.tableReference(bad, a); ok {
			t.Fatal(bad)
		}
	}
	for number, want := range map[float64]string{1e7: "10000000", 1e11: "1E+11", 1e-9: "0.000000001", 1e-10: "1E-10", 12.3456789012345: "12.3456789", 0.00001234567891234: "1.23457E-05"} {
		if got := totalNumberLabel(number); got != want {
			t.Errorf("%v = %s, want %s", number, got, want)
		}
	}
}
