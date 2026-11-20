package editfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestResizeOrdinaryReferencesAndRemovedFields(t *testing.T) {
	old, _ := parseRect("B2:D6")
	m := tableResize{old: old, next: cellRect{2, 2, 9, 4}, table: xlsxTable{Name: "Fees", Sheet: "Data", Headers: 1, Totals: 1, Columns: []xlsxTableColumn{{Name: "Item"}, {Name: "Qty"}, {Name: "Cost"}}}}
	got, err := m.rebindFormula(`SUM(Data!$C$3:C5,Data!B6,Data!B7:B8,"Data!C3:C5")`, "Other")
	if err != nil || got != `SUM(Data!$C$3:C8,Data!B9,Data!B6:B7,"Data!C3:C5")` {
		t.Fatal(got, err)
	}
	m.next.lastCol = 3
	if m.tableOperand("Fees[[#Totals],[Cost]]", false, false) != "#REF!" || m.tableOperand("Fees[Qty]", false, false) != "Fees[Qty]" {
		t.Fatal("removed field was rebound to a different column")
	}
}

// Inputs come from Excel's saved worksheet XML before and after ListObject.Resize.
func TestNativeTableResizeReferenceMatrix(t *testing.T) {
	path := os.Getenv("EXCELLENS_TABLE_RESIZE_MATRIX")
	if path == "" {
		t.Skip("native table resize matrix not supplied")
	}
	var matrix map[string]map[string]string
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	count := 0
	for key, exprs := range matrix {
		totals := 0
		ref, ok := strings.CutPrefix(key, "False")
		if !ok {
			totals = 1
			ref = strings.TrimPrefix(key, "True")
		}
		next, ok := parseRect(ref)
		if !ok {
			t.Fatal(key)
		}
		old := cellRect{2, 2, 5 + totals, 4}
		if totals == 1 && next.lastRow < old.lastRow {
			next.lastRow++
		}
		m := tableResize{old: old, next: next, table: xlsxTable{Sheet: "Data", Headers: 1, Totals: totals}}
		for before, want := range exprs {
			got, err := m.rebindFormula(before, "Look")
			if err != nil || got != want {
				t.Fatalf("%s: %s => %s, want %s (%v)", key, before, got, want, err)
			}
			count++
		}
	}
	if count != 11880 {
		t.Fatalf("incomplete native matrix: %d", count)
	}
}

func TestTableGrowthWritesInputCalculatedCellsTotalsAndCrossSheetCaches(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		addTableFixture(parts)
		xml := parts["xl/tables/table1.xml"]
		xml = strings.Replace(xml, `name="Cost"`, `name="Cost" dataDxfId="0"`, 1)
		parts["xl/tables/table1.xml"] = strings.Replace(xml, "<tableColumns", `<autoFilter ref="A1:B4"/><sortState ref="A2:B4"><sortCondition ref="B2:B4"/></sortState><tableColumns`, 1)
		parts["xl/styles.xml"] = strings.Replace(parts["xl/styles.xml"], "</styleSheet>", `<dxfs count="1"><dxf><numFmt numFmtId="10" formatCode="0.00%"/></dxf></dxfs></styleSheet>`, 1)
		for _, ref := range []string{"A2", "A3", "A4"} {
			xml = parts["xl/worksheets/sheet1.xml"]
			parts["xl/worksheets/sheet1.xml"] = strings.Replace(xml, `<c r="`+ref+`">`, `<c r="`+ref+`" s="1">`, 1)
		}
	})
	src, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	p, err := openStructurePackage(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	result := filepath.Join(dir, "table.xlsx")
	out, err := os.Create(result)
	if err != nil {
		t.Fatal(err)
	}
	addr := func(sheet string, row, col int) editdoc.Address {
		return editdoc.Address{Sheet: sheet, Row: row, Col: col}
	}
	input := tableEntry{dir: dir, changes: []editdoc.Change{
		{Address: addr("数据", 5, 1), Value: editdoc.Value{Kind: "number", Raw: "7"}},
		{Address: addr("数据", 6, 1), Value: editdoc.Value{Kind: "number", Raw: "9"}},
	}}
	_, err = p.resizeTable(t.Context(), out, src, editdoc.Structure{Kind: "resizeTable", Name: "Sales", Sheet: "数据", Range: "A1:B7"}, &input)
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	f, err := workbook.OpenEditSource(result)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	next, err := openStructurePackage(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	def, err := next.readPart(t.Context(), next.layout.Tables[0].Part)
	if err != nil || next.layout.Tables[0].Ref != "A1:B7" || !strings.Contains(string(def), `<autoFilter ref="A1:B6"`) {
		t.Fatal(string(def), err)
	}
	if !strings.Contains(string(def), `<sortState ref="A2:B4"><sortCondition ref="B2:B4"/>`) {
		t.Fatal("resizing changed the last applied sort", string(def))
	}
	targets := []editdoc.Address{addr("数据", 5, 2), addr("数据", 6, 2), addr("数据", 7, 2), addr("摘要", 3, 4), addr("数据", 5, 1)}
	var cells map[editdoc.Address]SourceCell
	if next.layout.Native {
		cells, err = next.readOfficeCells(t.Context(), targets)
	} else {
		cells, err = next.readCells(t.Context(), targets)
	}
	if err != nil {
		t.Fatal(err)
	}
	// SUBTOTAL excludes the manually hidden row; SUM on the other sheet includes it.
	for i, want := range []string{"14", "18", "44", "50"} {
		c := cells[targets[i]]
		if c.Cache == nil || c.Cache.Kind != "number" || c.Cache.Raw != want {
			t.Fatalf("%v: %v want %s", targets[i], c.Cache, want)
		}
	}
	styles, err := entryFormats(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if styles.number(cells[targets[0]].Value.Style) != 10 || styles.number(cells[targets[4]].Value.Style) != 0 {
		t.Fatal("new data must inherit the declared column format and preserve ordinary destination formats")
	}
}

func TestTableTailBoundaries(t *testing.T) {
	table := xlsxTable{Sheet: "Data", Name: "Sales", Ref: "B2:E6", Headers: 1, Totals: 1}
	l := XLSXLayout{Tables: []xlsxTable{table}}
	c := func(row, col int, kind string) editdoc.Change {
		return editdoc.Change{Address: editdoc.Address{Sheet: "Data", Row: row, Col: col}, Value: editdoc.Value{Kind: kind}}
	}
	for _, in := range [][]editdoc.Change{{c(6, 3, "text")}, {c(8, 3, "text")}, {c(7, 3, "blank")}, {c(4, 3, "number"), c(7, 3, "text")}} {
		g, err := l.TableTail(in)
		if err != nil || g != nil {
			t.Fatal(in, g, err)
		}
	}
	g, err := l.TableTail([]editdoc.Change{c(7, 3, "number"), c(8, 3, "number")})
	if err != nil || g == nil || g.rowOffset != -1 || g.bounds.lastRow != 8 {
		t.Fatal(g, err)
	}
}
