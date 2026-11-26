package workbook

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func conditionalPresentationFixture(t *testing.T) (*Workbook, *SheetIndex) {
	t.Helper()
	isolateTestStorage(t)
	xml := `<worksheet><sheetData><row r="1"><c r="A1"><v>9007199254740993.31</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c><c r="C1"><v>1</v></c><c r="D1"><f>1+1</f></c></row><row r="3"><c r="A3"><v>2</v></c></row></sheetData><conditionalFormatting sqref="A1:A4"><cfRule type="expression" priority="1" dxfId="0"><formula>$C1&gt;0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="B1:B4"><cfRule type="cellIs" operator="equal" priority="2" dxfId="1"><formula>"001"</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="E1:E4"><cfRule type="containsBlanks" priority="3" dxfId="0"><formula>LEN(TRIM(E1))=0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="F1:F4"><cfRule type="aboveAverage" priority="4" dxfId="0"/></conditionalFormatting>`
	file := sourceMetadataWorkbook(t, xml, conditionalStylesFixture)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Error("conditional presentation changed source", err)
		}
	})
	return w, idx
}

func TestConditionalPresentationIsExplicitAndKeepsSourceValuesTypesStylesAndSparseExtent(t *testing.T) {
	w, idx := conditionalPresentationFixture(t)
	base, err := w.ReadRowsWithOptionsContext(context.Background(), idx, 0, 2, 1, 6, RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range base {
		for _, cell := range row.Cells {
			if cell.Conditional != nil || cell.PresentationOnly {
				t.Fatal("ordinary reads evaluated conditional formats", cell)
			}
		}
	}
	options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true}
	rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{1, 2, 3, 4}, 1, 6, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for _, cell := range row.Cells {
			if cell.Col == 5 && (cell.Conditional == nil || cell.Conditional.Visual.Color != "#FF0033" || !cell.PresentationOnly || cell.Raw != "" || cell.Type != "") {
				t.Fatal("blank conditional cell became data or lost its effect", cell)
			}
			if cell.Col == 6 && (cell.Conditional == nil || cell.Conditional.Notice == "") {
				t.Fatal("unsupported rule disappeared", cell)
			}
		}
	}
	for i, sourceRow := range base {
		presented := rows[0]
		if i == 1 {
			presented = rows[2]
		}
		for _, source := range sourceRow.Cells {
			var actual CellData
			for _, c := range presented.Cells {
				if c.Col == source.Col {
					actual = c
					break
				}
			}
			actual.Conditional = nil
			if !reflect.DeepEqual(actual, source) {
				t.Fatal("presentation overwrote source evidence", actual, source)
			}
		}
	}
	if idx.RowCount() != 2 || idx.MaxRow != 3 || idx.MaxCol != 4 {
		t.Fatal("sparse presentation changed the source extent", idx.RowCount(), idx.MaxRow, idx.MaxCol)
	}
	if rows[0].Cells[0].Conditional == nil || rows[0].Cells[0].Conditional.Visual.Color != "#FF0033" || rows[0].Cells[1].Conditional == nil || rows[0].Cells[1].Type != "inlineStr" || rows[0].Cells[1].Raw != "001" {
		t.Fatal(rows[0])
	}
	if rows[2].Cells[0].Conditional != nil {
		t.Fatal("proven blank reference did not compare as zero", rows[2].Cells[0])
	}
	// Reference C1 is outside this one-column read; it must be read explicitly.
	narrow, err := w.ReadRowsByIndicesWithOptionsContext(context.Background(), idx, []int{0}, 1, 1, options)
	if err != nil || narrow[0].Cells[0].Conditional == nil || narrow[0].Cells[0].Conditional.Notice != "" {
		t.Fatal("out-of-window reference was not resolved", narrow, err)
	}
}

func TestConditionalDependencyBudgetAndMissingFormulaCacheRemainUnknown(t *testing.T) {
	w, idx := conditionalPresentationFixture(t)
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := w.newConditionalRead(context.Background(), idx, f, RowReadOptions{ConditionalFormats: true})
	if err != nil {
		t.Fatal(err)
	}
	row, err := w.ReadRow(idx, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.dependencyReads = 0
	if err := r.apply(&row, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if row.Cells[0].Conditional == nil || !strings.Contains(row.Cells[0].Conditional.Notice, "读取容量") || row.Cells[0].Conditional.Visual != nil {
		t.Fatal("unread dependency became false/zero or default colors", row)
	}
	r.resetBudget()
	row, err = w.ReadRow(idx, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.remainingBytes = 1
	if err := r.apply(&row, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if row.PresentationNotice == "" || row.Cells[0].Conditional != nil || row.Cells[0].Raw != "9007199254740993.31" {
		t.Fatal("presentation budget dropped source or gave a truncated success", row)
	}
	r.resetBudget()
	r.row = RowData{Number: 3}
	r.first, r.last = 1, 1
	cell, available, err := r.resolve(1, 4)
	if err != nil || !available || !cell.CacheMissing {
		t.Fatal("missing formula cache was replaced", cell, available, err)
	}
	value, notice := r.evaluator.engine.CellValue(toConditionalOperand(cell))
	if notice == "" || value.Kind != "" {
		t.Fatal("missing cache became a valid operand", value, notice)
	}
}

func TestConditionalResolvedDifferencesClearAndInheritEachBorderAndPreserveExactNumbers(t *testing.T) {
	no := false
	empty := ""
	red := "#FF0000"
	none := "none"
	w := &Workbook{VisualStyles: []VisualStyle{{Bold: true, Background: "#FFFF00", BorderTop: "1px solid #112233", BorderRight: "2px dashed #445566"}}}
	cell := CellData{Raw: "9007199254740993.31", Display: "original display", ValueColor: "#008800"}
	p := w.conditionalPresentation(cell, ConditionalFormatResult{Style: DifferentialStyle{Font: RichFont{Bold: &no, Color: red}, Background: &empty, Border: &DifferentialBorders{Top: &DifferentialBorderSide{Color: &red}, Right: &DifferentialBorderSide{Style: &none}}, NumberFormat: &DifferentialNumberFormat{ID: 0, Code: "General"}}})
	if p.Visual.Bold || p.Visual.Background != "" || p.Visual.BorderTop != "1px solid #FF0000" || p.Visual.BorderRight != "" || p.Display == nil || *p.Display != cell.Raw || p.ValueColor == nil || *p.ValueColor != "" || cell.Display != "original display" || cell.ValueColor != "#008800" {
		t.Fatal(p, cell)
	}
	for _, raw := range []string{"1e1000000000", "-1e-999"} {
		p = w.conditionalPresentation(CellData{Raw: raw}, ConditionalFormatResult{Style: DifferentialStyle{NumberFormat: &DifferentialNumberFormat{ID: 166, Code: "0.00"}}})
		if p.Display == nil || *p.Display != raw || p.Notice == "" {
			t.Fatal("unrepresentable display became a fabricated number", raw, p)
		}
	}
	p = w.conditionalPresentation(CellData{Raw: "-0.000"}, ConditionalFormatResult{Style: DifferentialStyle{NumberFormat: &DifferentialNumberFormat{ID: 166, Code: "0.00"}}})
	if p.Display == nil || *p.Display != "0.00" || p.Notice != "" {
		t.Fatal("exact signed zero was not normalized in the derived display", p)
	}
}

func TestConditionalBoundedRangeFormulasEvaluateAndRespectBudgets(t *testing.T) {
	isolateTestStorage(t)
	sheet := `<worksheet><sheetData>` +
		`<row r="1"><c r="A1" t="inlineStr"><is><t>K1</t></is></c><c r="B1"><v>10</v></c><c r="C1"><v>20</v></c><c r="D1"><v>30</v></c><c r="E1" t="inlineStr"><is><t>K1</t></is></c></row>` +
		`<row r="2"><c r="A2" t="inlineStr"><is><t>K2</t></is></c><c r="B2"><v>5</v></c><c r="C2"><v>5</v></c><c r="D2"><v>15</v></c><c r="E2" t="inlineStr"><is><t>K9</t></is></c></row>` +
		`<row r="3"><c r="A3" t="inlineStr"><is><t>K1</t></is></c><c r="B3"><v>50</v></c><c r="C3"><v>60</v></c><c r="D3"><v>110</v></c></row>` +
		`</sheetData>` +
		// Whole-column COUNTIF duplicate check (sparse indexed rows only!)
		`<conditionalFormatting sqref="A1:A5"><cfRule type="expression" priority="1" dxfId="0"><formula>COUNTIF($A:$A,A1)&gt;1</formula></cfRule></conditionalFormatting>` +
		// Same-row SUM cross-foot check (in-memory fast path!)
		`<conditionalFormatting sqref="D1:D3"><cfRule type="expression" priority="2" dxfId="1"><formula>SUM($B1:$C1)&lt;&gt;$D1</formula></cfRule></conditionalFormatting>` +
		// MATCH lookup list check + SUMIF check
		`<conditionalFormatting sqref="B1:B3"><cfRule type="expression" priority="3" dxfId="2"><formula>AND(ISNUMBER(MATCH($A1,$E$1:$E$2,0)), SUMIF($A$1:$A$3,$A1,$B$1:$B$3)&gt;=60)</formula></cfRule></conditionalFormatting>` +
		`</worksheet>`
	file := sourceMetadataWorkbook(t, sheet, conditionalStylesFixture)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := RowReadOptions{FormatValue: true, ConditionalFormats: true}
	rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{1, 2, 3, 4}, 1, 5, opts)
	if err != nil {
		t.Fatal(err)
	}
	cellAt := func(rowIdx, col int) CellData {
		for _, c := range rows[rowIdx].Cells {
			if c.Col == col {
				return c
			}
		}
		return CellData{}
	}
	// Row 1 and Row 3 have duplicate "K1" in column A; Row 2 ("K2") and blank Row 4 must NOT match!
	if c := cellAt(0, 1); c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Visual.Color != "#FF0033" {
		t.Fatalf("duplicate A1 not highlighted: %+v", c)
	}
	if c := cellAt(1, 1); c.Conditional != nil {
		t.Fatalf("unique A2 should not match COUNTIF>1: %+v", c)
	}
	if c := cellAt(2, 1); c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Visual.Color != "#FF0033" {
		t.Fatalf("duplicate A3 not highlighted: %+v", c)
	}
	if c := cellAt(3, 1); c.Conditional != nil {
		t.Fatalf("blank row A4 must not match COUNTIF($A:$A, A4)>1: %+v", c)
	}
	// Row 2 col D: SUM(5,5)=10 <> 15 -> highlighted! Rows 1 and 3 agree -> not highlighted.
	if c := cellAt(0, 4); c.Conditional != nil {
		t.Fatalf("balanced D1 should not match: %+v", c)
	}
	if c := cellAt(1, 4); c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Visual.Background != "#FFFF00" {
		t.Fatalf("unbalanced D2 not highlighted: %+v", c)
	}
	// Col B: K1 is in E1:E2 and SUMIF(K1, B)=10+50=60 >= 60 -> B1 and B3 match, B2 does not.
	if c := cellAt(0, 2); c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Visual.Color != "#008800" {
		t.Fatalf("MATCH+SUMIF B1 not highlighted: %+v", c)
	}
	if c := cellAt(1, 2); c.Conditional != nil {
		t.Fatalf("B2 should not match MATCH+SUMIF: %+v", c)
	}
	if c := cellAt(2, 2); c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Visual.Color != "#008800" {
		t.Fatalf("MATCH+SUMIF B3 not highlighted: %+v", c)
	}
	// Budget exhaustion on range resolution keeps honest notice without guessing false.
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader, err := w.newConditionalRead(context.Background(), idx, f, opts)
	if err != nil {
		t.Fatal(err)
	}
	row1, err := w.ReadRow(idx, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	reader.dependencyReads = 0
	if err := reader.apply(&row1, 1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if row1.Cells[0].Conditional == nil || !strings.Contains(row1.Cells[0].Conditional.Notice, "读取容量") || row1.Cells[0].Conditional.Visual != nil {
		t.Fatalf("exhausted range dependency budget did not keep honest notice: %+v", row1.Cells[0])
	}
	// Uncached formula inside a referenced range keeps honest missing-cache notice.
	uncachedSheet := `<worksheet><sheetData>` +
		`<row r="1"><c r="A1"><v>1</v></c></row>` +
		`<row r="2"><c r="A2"><f>1+1</f></c></row>` +
		`</sheetData><conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><formula>COUNTIF($A$1:$A$2,A1)&gt;0</formula></cfRule></conditionalFormatting></worksheet>`
	uncachedFile := sourceMetadataWorkbook(t, uncachedSheet, conditionalStylesFixture)
	uw, err := OpenWorkbook(uncachedFile)
	if err != nil {
		t.Fatal(err)
	}
	uidx, err := uw.BuildIndex(uw.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	urows, err := uw.ReadRowsByNumbersWithOptionsContext(context.Background(), uidx, []int{1}, 1, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if urows[0].Cells[0].Conditional == nil || !strings.Contains(urows[0].Cells[0].Conditional.Notice, "没有保存") || urows[0].Cells[0].Conditional.Visual != nil {
		t.Fatalf("uncached formula in range became blank/zero: %+v", urows[0].Cells[0])
	}
}
