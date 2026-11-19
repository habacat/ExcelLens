package editfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func formulaFixture(t *testing.T) string {
	t.Helper()
	return writeEditBook(t, func(p map[string]string) {
		p["xl/workbook.xml"] = strings.Replace(p["xl/workbook.xml"], "</workbook>", `<calcPr calcId="12345" calcMode="manual" calcCompleted="1"/></workbook>`, 1)
		p["xl/worksheets/sheet1.xml"] = strings.Replace(testSheet1, `<p:c r="D1"/>`, `<p:c r="C1" t="str"><p:f>""</p:f><p:v/></p:c><p:c r="D1"><p:f>D4+1</p:f><p:v>4</p:v></p:c>`, 1)
		p["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1">` +
			`<c r="A1" t="s"><f>IF('数据'!D4&gt;0,"00120","00000")</f><v>0</v></c>` +
			`<c r="B1" t="e"><f>1/0</f><v>#DIV/0!</v></c><c r="C1" t="n"><f>1+1</f><v/></c>` +
			`<c r="D1" t="b"><f>'数据'!D1&gt;0</f><v>1</v></c></row></sheetData></worksheet>`
		p[calcChainPart] = `<calcChain xmlns="` + mainNS + `"><c r="C1" i="1"/><c r="D1"/><c r="A1" i="2"/><c r="B1"/><c r="C1"/><c r="D1"/></calcChain>`
		p["[Content_Types].xml"] = strings.Replace(p["[Content_Types].xml"], "</Types>", `<Override PartName="/xl/calcChain.xml" ContentType="`+classifyPart(calcChainPart).mime+`"/></Types>`, 1)
		p["xl/_rels/workbook.xml.rels"] = strings.Replace(p["xl/_rels/workbook.xml.rels"], "</Relationships>", `<Relationship Id="rId5" Type="`+relationNS+`/calcChain" Target="calcChain.xml"/></Relationships>`, 1)
	})
}

func TestXLSXFormulaEditInvalidatesEntireBookAndClearsCalcChain(t *testing.T) {
	isolateEditFileStorage(t)
	path := formulaFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	layout, proof, err := InspectXLSX(t.Context(), f, w)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Sheets[0].Formulas != 2 || layout.Sheets[1].Formulas != 4 || layout.Sheets[1].Cached != 3 || layout.Sheets[1].SharedCached != 1 {
		t.Fatal(layout)
	}
	address := func(name string, col int) editdoc.Address { return editdoc.Address{Sheet: name, Row: 1, Col: col} }
	emptyText, emptyNumber := address("数据", 3), address("摘要", 3)
	before, err := ReadXLSXCells(t.Context(), f, layout, []editdoc.Address{emptyText, emptyNumber})
	if err != nil || before[emptyText].Cache == nil || before[emptyText].Cache.Kind != "text" || before[emptyText].Cache.Raw != "" || before[emptyNumber].Cache != nil {
		t.Fatal(before, err)
	}
	for _, tc := range []struct {
		name    string
		edits   map[editdoc.Address]editdoc.Value
		pending int
	}{
		{"pristine", nil, 1},
		{"edited", map[editdoc.Address]editdoc.Value{
			{Sheet: "数据", Row: 4, Col: 4}: {Kind: "number", Raw: "8.25"},
			address("数据", 2):              {Kind: "formula", Raw: `=SUM('摘要'!$A1:A$1)+1`},
		}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := os.Create(filepath.Join(t.TempDir(), "copy.xlsx"))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			result, err := WriteXLSX(t.Context(), out, f, layout, proof, tc.edits, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.FormulasPending != tc.pending || !result.removedCalcChain || result.Changed["xl/styles.xml"] {
				t.Fatalf("pending=%d changed=%v", result.FormulasPending, result.Changed)
			}
			if err := VerifyXLSX(t.Context(), out, result); err != nil {
				t.Fatal(err)
			}
			p, err := openXLSXPackage(out, statSize(t, out))
			if err != nil || p.files[calcChainPart] != nil {
				t.Fatal("retained obsolete chain", err)
			}
			for name, substring := range map[string]string{"xl/workbook.xml": `calcId="12345" calcMode="manual"`, "xl/worksheets/sheet1.xml": `<!-- keep -->`} {
				data, err := p.readPart(t.Context(), name)
				if err != nil || !bytes.Contains(data, []byte(substring)) {
					t.Fatalf("lost unchanged XML %s in %s: %v", substring, name, err)
				}
			}
			reopened, err := workbook.OpenWorkbook(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.ClearIndexes()
			var missing int
			for _, sheet := range reopened.Sheets {
				index, err := reopened.BuildIndex(sheet, nil, t.Context().Done())
				if err != nil {
					t.Fatal(err)
				}
				row, err := reopened.ReadRowAll(index, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, cell := range row.Cells {
					if !cell.HasFormula {
						continue
					}
					if cell.CacheMissing {
						missing++
						if cell.Raw != "" || cell.Type != "" || !strings.HasPrefix(cell.Formula, "=") {
							t.Fatalf("stale typed cache at %s!%s: %+v", sheet.Name, cell.Ref, cell)
						}
					}
					if sheet.Name == "数据" && cell.Ref == "B1" && cell.Formula != tc.edits[address("数据", 2)].Raw {
						t.Fatal("explicit formula did not round-trip", cell)
					}
				}
			}
			if missing != tc.pending {
				t.Fatalf("normal worksheet reader reported %d missing caches, want %d", missing, tc.pending)
			}
		})
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal(fmt.Errorf("formula fixture source changed: %v", err))
	}
}
