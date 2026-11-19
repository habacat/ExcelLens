package editfile

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func calculationFixture(t *testing.T, change func(map[string]string)) *CalculationBook {
	t.Helper()
	isolateEditFileStorage(t)
	path := writeEditBook(t, change)
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	layout, _, err := InspectXLSX(t.Context(), f, wb)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCalculationBook(t.Context(), f, layout)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRevisionCalculationUsesSparseRangesAndRebindsDependencies(t *testing.T) {
	b := calculationFixture(t, func(p map[string]string) {
		addFixtureNames(p, `<definedName name="Inputs">'数据'!$C$1:$C$1048576</definedName><definedName name="Rate">0.5</definedName>`)
		p["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><f>SUM(Inputs)*Rate</f><v>0</v></c><c r="B1"><f>IF(A1&gt;0,A1,'数据'!D4)</f><v>3</v></c><c r="C1"><f>COUNTIF('数据'!C:C,"")</f><v>1048576</v></c><c r="D1"><f>INDEX('数据'!A:Z,4,4)</f><v>3</v></c></row></sheetData></worksheet>`
	})
	input := editdoc.Address{Sheet: "数据", Row: 1048576, Col: 3}
	factor := editdoc.Address{Sheet: "数据", Row: 4, Col: 4}
	output := func(col int) editdoc.Address { return editdoc.Address{Sheet: "摘要", Row: 1, Col: col} }
	num := func(s string) editdoc.Value { return editdoc.Value{Kind: "number", Raw: s} }
	edits := map[editdoc.Address]editdoc.Value{input: num("10"), factor: num("8")}
	first, err := b.Calculate(t.Context(), edits, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"5", "5", "1048575", "8"} {
		if got := first.Cells[output(i+1)]; got.State != "calculated" || got.Value != num(want) {
			t.Fatalf("col %d: %+v", i+1, got)
		}
	}
	if first.Visits > 80 {
		t.Fatalf("full column expanded: %d visits", first.Visits)
	}
	t.Logf("four sparse results, including row 1048576: %d cell visits", first.Visits)
	changes := maps.Clone(edits)
	changes[output(1)] = editdoc.Value{Kind: "formula", Raw: "='数据'!D4+1"}
	second, err := b.Calculate(t.Context(), changes, first)
	if err != nil || second.Cells[output(2)].Value != num("9") {
		t.Fatal(second, err)
	}
	reverted, err := b.Calculate(t.Context(), edits, second)
	if err != nil || !maps.Equal(reverted.Cells, first.Cells) {
		t.Fatal("undo", err)
	}
	redone, err := b.Calculate(t.Context(), changes, reverted)
	if err != nil || !maps.Equal(redone.Cells, second.Cells) {
		t.Fatal("redo", err)
	}
}

func TestRevisionCalculationResolvesLongChainsAndCycles(t *testing.T) {
	const rows = 820
	b := calculationFixture(t, func(p map[string]string) {
		var sheet strings.Builder
		fmt.Fprintf(&sheet, `<worksheet xmlns="%s"><sheetData>`, mainNS)
		for r := 1; r < rows; r++ {
			fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d"><f>A%d+1</f></c></row>`, r, r, r+1)
		}
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d"><v>1</v></c></row></sheetData></worksheet>`, rows, rows)
		p["xl/worksheets/sheet2.xml"] = sheet.String()
	})
	start := editdoc.Address{Sheet: "摘要", Row: 1, Col: 1}
	end := editdoc.Address{Sheet: "摘要", Row: rows, Col: 1}
	base, err := b.Calculate(t.Context(), nil, nil)
	if err != nil || base.Cells[start].Value.Raw != "820" {
		t.Fatalf("chain: %+v, %v", base.Cells[start], err)
	}
	change := map[editdoc.Address]editdoc.Value{end: {Kind: "formula", Raw: "=IFERROR(A1,99)"}}
	cycle, err := b.Calculate(t.Context(), change, base)
	if err != nil || cycle.Cells[start].State != "cycle" || cycle.Cells[end].State != "cycle" {
		t.Fatal("cycle", cycle.Cells[start], cycle.Cells[end], err)
	}
	safe := map[editdoc.Address]editdoc.Value{end: {Kind: "formula", Raw: "=IF(FALSE,A1,ROW(A820))"}}
	uncycled, err := b.Calculate(t.Context(), safe, cycle)
	if err != nil || uncycled.Cells[start].Value.Raw != "1639" {
		t.Fatal("resolved cycle", uncycled.Cells[start], err)
	}
}

func TestRevisionCalculationRepairsIncorrectConstantCacheAndDependents(t *testing.T) {
	b := calculationFixture(t, func(p map[string]string) {
		p["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><f>ROUND(-1.125,2)</f><v>-1.12</v></c><c r="B1"><f>A1+2</f><v>0.88</v></c></row></sheetData></worksheet>`
	})
	got, err := b.Calculate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"-1.13", "0.8700000000000001"} {
		a := editdoc.Address{Sheet: "摘要", Row: 1, Col: i + 1}
		if cell := got.Cells[a]; cell.State != "calculated" || cell.Value.Raw != want {
			t.Fatalf("%v: %+v", a, cell)
		}
	}
}
