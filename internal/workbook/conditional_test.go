package workbook

import (
	"bytes"
	"context"
	"encoding/json"
	"excellens/internal/conditional"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"excellens/internal/formats"
)

const conditionalStylesFixture = `<styleSheet><fonts count="1"><font><name val="宋体"/><sz val="11"/><b/><u/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="4"><dxf><font><name val="Arial"/><color rgb="FFFF0033"/><b val="0"/><u val="none"/></font><border><top><color rgb="FF123456"/></top></border></dxf><dxf><font><b/><i/></font><fill><patternFill patternType="solid"><fgColor rgb="FFFFFF00"/></patternFill></fill><border><bottom style="thin"><color rgb="FF0000FF"/></bottom><right style="none"/></border></dxf><dxf><font><color rgb="FF008800"/></font><alignment horizontal="left" wrapText="0"/><numFmt numFmtId="166" formatCode="0.000"/></dxf><dxf><fill><patternFill patternType="none"/></fill><font><strike val="0"/></font></dxf></dxfs></styleSheet>`

func conditionalFixture(t *testing.T, rules string) (*Workbook, *SheetIndex, *SourceConditionalFormats) {
	t.Helper()
	isolateTestStorage(t)
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>9007199254740993.31</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c></row></sheetData>`+rules+`</worksheet>`, conditionalStylesFixture)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, _ := os.ReadFile(file)
		if !bytes.Equal(before, after) {
			t.Error("conditional metadata changed source")
		}
	})
	return w, idx, index
}

func evaluateConditional(t *testing.T, index *SourceConditionalFormats, row, col int, cell CellData, resolver ConditionalCellResolver) *ConditionalFormatResult {
	t.Helper()
	out, err := index.NewEvaluator(context.Background()).Evaluate(row, col, cell, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConditionalRangeCapacityRejectsTheWholeInputWithoutPartialAreas(t *testing.T) {
	boundary := strings.Repeat("A1 ", formats.ConditionalRanges)
	if ranges, err := parseConditionalRanges(boundary); err != nil || len(ranges) != formats.ConditionalRanges {
		t.Fatal("valid range-count boundary rejected", len(ranges), err)
	}
	for _, input := range []string{boundary + "A2", "A1:" + strings.Repeat(":", 1<<20), "", "A1:", ":B2", "A1:A2:A3", "B2:A1"} {
		if ranges, err := parseConditionalRanges(input); err == nil || len(ranges) != 0 {
			t.Fatal("invalid sqref returned a truncated success", len(input), len(ranges), err)
		}
	}
	if ranges, err := parseConditionalRanges("$A$1:$B$2\t\nC3\u2003D4"); err != nil || len(ranges) != 3 || ranges[0].EndCol != 2 || ranges[2].Row != 4 {
		t.Fatal("range iterator changed whitespace or absolute references", ranges, err)
	}
}

func TestNativeConditionalPriorityExactNumbersFalsePropertiesAndStopIfTrueShareOneSourceIndex(t *testing.T) {
	rules := `<conditionalFormatting sqref="A1:A1000000"><cfRule type="expression" dxfId="2" priority="3" stopIfTrue="1"><formula>TRUE()</formula></cfRule><cfRule type="cellIs" dxfId="0" priority="1" operator="greaterThan"><formula>9007199254740993.30</formula></cfRule><cfRule type="expression" dxfId="1" priority="2"><formula>A1&gt;0</formula></cfRule><cfRule type="expression" dxfId="3" priority="4"><formula>TRUE()</formula></cfRule></conditionalFormatting>`
	w, idx, index := conditionalFixture(t, rules)
	if index.Summary().Rules != 4 || index.Summary().Ranges != 1 || index.Summary().Pending != 0 || index.Summary().Notice != "" {
		t.Fatal(index.Summary())
	}
	if len(w.differentialStyles) != 4 || w.differentialNotice != "" {
		t.Fatal(w.differentialNotice)
	}
	for i, s := range w.differentialStyles {
		if s.Notice != "" {
			t.Fatalf("DXF %d was not complete: %s", i, s.Notice)
		}
	}
	row, err := w.ReadRow(idx, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	original := row.Cells[0]
	out := evaluateConditional(t, index, 1, 1, original, nil)
	if out == nil || out.Notice != "" || out.Matched != 3 || !reflect.DeepEqual(out.Priorities, []int{1, 2, 3}) || out.Style.Font.Color != "#FF0033" || out.Style.Font.Bold == nil || *out.Style.Font.Bold || out.Style.Font.Italic == nil || !*out.Style.Font.Italic || out.Style.Font.Underline != "none" || out.Style.Background == nil || *out.Style.Background != "#FFFF00" {
		t.Fatal(out)
	}
	if out.Style.Border.Top.Style != nil || out.Style.Border.Top.Color == nil || *out.Style.Border.Top.Color != "#123456" || out.Style.Border.Right.Style == nil || *out.Style.Border.Right.Style != "none" || out.Style.NumberFormat == nil || out.Style.NumberFormat.Code != "0.000" || out.Style.Wrap == nil || *out.Style.Wrap {
		t.Fatal("absent, false and clear properties collapsed", out.Style)
	}
	if row.Cells[0] != original || original.Raw != "9007199254740993.31" || original.Style != 0 {
		t.Fatal("conditional format changed source evidence")
	}
	*out.Style.Font.Bold = true
	*out.Style.Background = "changed"
	*out.Style.Border.Top.Color = "changed"
	out.Style.NumberFormat.Code = "changed"
	again := evaluateConditional(t, index, 1, 1, original, nil)
	if *again.Style.Font.Bold || *again.Style.Background != "#FFFF00" || *again.Style.Border.Top.Color != "#123456" || again.Style.NumberFormat.Code != "0.000" {
		t.Fatal("evaluated result mutated source palette")
	}
	near := evaluateConditional(t, index, 2, 1, CellData{Raw: "9007199254740993.30"}, nil)
	if near == nil || near.Style.Font.Bold == nil || !*near.Style.Font.Bold || near.Style.Font.Color != "#008800" {
		t.Fatal("floating point collapsed nearby exact values", near)
	}
}

func TestConditionalUnknownRulesAndUncachedReferencesDoNotBecomeFalseOrZero(t *testing.T) {
	rules := `<conditionalFormatting sqref="A1:A8"><cfRule type="aboveAverage" dxfId="0" priority="1"/><cfRule type="expression" dxfId="1" priority="2"><formula>TRUE()</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="B1:B8"><cfRule type="cellIs" dxfId="999" operator="greaterThan" priority="3"><formula>100</formula></cfRule><cfRule type="expression" dxfId="1" priority="4"><formula>TRUE()</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="C1:C8"><cfRule type="cellIs" dxfId="0" operator="greaterThan" priority="5"><formula>0</formula></cfRule></conditionalFormatting>`
	_, _, index := conditionalFixture(t, rules)
	unknown := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
	if unknown == nil || unknown.Notice == "" || unknown.Matched != 0 || !unknown.Style.Empty() {
		t.Fatal("unknown predicate was treated as false", unknown)
	}
	falseRule := evaluateConditional(t, index, 1, 2, CellData{Raw: "1"}, nil)
	if falseRule == nil || falseRule.Notice != "" || falseRule.Style.Background == nil {
		t.Fatal("unused unsupported DXF blocked a proven false predicate", falseRule)
	}
	trueRule := evaluateConditional(t, index, 2, 2, CellData{Raw: "200"}, nil)
	if trueRule == nil || trueRule.Notice == "" || !trueRule.Style.Empty() {
		t.Fatal("missing DXF silently became a default style", trueRule)
	}
	missing := evaluateConditional(t, index, 1, 3, CellData{Raw: "", HasFormula: true, CacheMissing: true}, nil)
	if missing == nil || !strings.Contains(missing.Notice, "没有保存") || !missing.Style.Empty() {
		t.Fatal("missing cache became blank/zero", missing)
	}
}

func TestConditionalReferencesUseTheFirstAreaAnchorAndKeepAbsoluteAxes(t *testing.T) {
	rules := `<conditionalFormatting sqref="B5:C8"><cfRule type="expression" dxfId="0" priority="1"><formula>$A5&gt;0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="E5:E8 G5:G8"><cfRule type="expression" dxfId="1" priority="2"><formula>E5&gt;0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="I5:I8"><cfRule type="expression" dxfId="2" priority="3"><formula>$A$5&gt;0</formula></cfRule></conditionalFormatting>`
	_, _, index := conditionalFixture(t, rules)
	var calls [][2]int
	resolver := func(row, col int) (CellData, bool, error) {
		calls = append(calls, [2]int{row, col})
		return CellData{Raw: "1"}, true, nil
	}
	for _, col := range []int{2, 3, 9} {
		if out := evaluateConditional(t, index, 6, col, CellData{Raw: "source", Type: "inlineStr"}, resolver); out == nil || out.Notice != "" || out.Matched != 1 {
			t.Fatal(out)
		}
	}
	if !reflect.DeepEqual(calls, [][2]int{{6, 1}, {6, 1}, {5, 1}}) {
		t.Fatal("relative/absolute reference drift", calls)
	}
	if out := evaluateConditional(t, index, 6, 7, CellData{Raw: "1"}, nil); out == nil || out.Notice != "" || out.Style.Background == nil {
		t.Fatal("disjoint range used a different implicit anchor", out)
	}
	if out := evaluateConditional(t, index, 6, 2, CellData{Raw: "0"}, nil); out == nil || !strings.Contains(out.Notice, "尚未读取") {
		t.Fatal("unread reference treated as blank", out)
	}
	if out := evaluateConditional(t, index, 6, 2, CellData{Raw: "0"}, func(int, int) (CellData, bool, error) { return CellData{}, false, nil }); out == nil || !strings.Contains(out.Notice, "尚未读取") {
		t.Fatal(out)
	}
	if out := evaluateConditional(t, index, 6, 2, CellData{Raw: "0"}, func(int, int) (CellData, bool, error) { return CellData{}, true, nil }); out != nil {
		t.Fatal("proven blank did not compare as zero", out)
	}
}

func TestConditionalTextBlanksErrorsAndNativeTypesDoNotInterpretAmounts(t *testing.T) {
	rules := `<conditionalFormatting sqref="A1:A8"><cfRule type="cellIs" operator="equal" dxfId="0" priority="1"><formula>"001"</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="B1:B8"><cfRule type="containsText" text="K" dxfId="0" priority="2"><formula>NOT(ISERROR(SEARCH("K",B1)))</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="C1:C8"><cfRule type="containsBlanks" dxfId="0" priority="3"><formula>LEN(TRIM(C1))=0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="D1:D8"><cfRule type="containsErrors" dxfId="0" priority="4"><formula>ISERROR(D1)</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="E1:E8"><cfRule type="containsText" text="K" dxfId="0" priority="5"><formula>NOT(ISERROR(SEARCH("different",E1)))</formula></cfRule></conditionalFormatting>`
	_, _, index := conditionalFixture(t, rules)
	for _, tc := range []struct {
		col             int
		cell            CellData
		matched, notice bool
	}{
		{1, CellData{Raw: "001", Type: "inlineStr"}, true, false}, {1, CellData{Raw: "1", Type: "n"}, false, false},
		{2, CellData{Raw: "AKZ", Type: "s"}, true, false}, {2, CellData{Raw: "000123", Type: "s"}, false, false}, {2, CellData{Raw: "123", Type: "n"}, false, true},
		{3, CellData{Raw: "   ", Type: "inlineStr"}, true, false}, {3, CellData{Raw: "\t", Type: "inlineStr"}, false, false}, {3, CellData{Raw: "　", Type: "inlineStr"}, false, false}, {3, CellData{Raw: "0"}, false, false},
		{4, CellData{Raw: "#N/A", Type: "e"}, true, false}, {4, CellData{Raw: "#N/A", Type: "inlineStr"}, false, false}, {4, CellData{CacheMissing: true, HasFormula: true}, false, true},
		{5, CellData{Raw: "K", Type: "inlineStr"}, false, true},
	} {
		out := evaluateConditional(t, index, 1, tc.col, tc.cell, nil)
		if (out != nil && out.Matched > 0) != tc.matched || (out != nil && out.Notice != "") != tc.notice {
			t.Fatalf("col=%d cell=%#v: %#v", tc.col, tc.cell, out)
		}
	}
}

func TestConditionalDecimalsNeverRoundOrExpandScientificExponents(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		cmp  int
	}{
		{"9007199254740993.31", "9007199254740993.30", 1}, {"0.00000000000000000001", "1e-20", 0}, {"-0", "+0.00", 0}, {"1e1000000000", "9e999999999", 1}, {"1e-1000000000", "0", 1}, {"-1e1000000000", "-2e1000000000", 1}, {"12300", "1.23e4", 0},
	} {
		a, ok := parseConditionalDecimal(tc.a)
		if !ok {
			t.Fatal(tc.a)
		}
		b, ok := parseConditionalDecimal(tc.b)
		if !ok {
			t.Fatal(tc.b)
		}
		got := compareConditionalDecimal(a, b)
		if got != tc.cmp {
			t.Fatal(tc, got)
		}
	}
	for _, value := range []string{"1,000", "￥1.00", "(1.00)", "1/2", "1e999999999999999999999", "NaN", "Inf"} {
		if _, ok := parseConditionalDecimal(value); ok {
			t.Fatal("non-native numeric token accepted", value)
		}
	}
}

func TestConditionalConcurrentLoadsShareIndexAndBadMetadataDoesNotPoisonFutureReads(t *testing.T) {
	w, idx, _ := conditionalFixture(t, `<conditionalFormatting sqref="A1:A8"><cfRule type="expression" dxfId="0" priority="1"><formula>TRUE()</formula></cfRule></conditionalFormatting>`)
	w.conditionalCache = nil
	w.conditionalBytes = 0
	var wg sync.WaitGroup
	got := make(chan *SourceConditionalFormats, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
			if err != nil {
				t.Error(err)
				return
			}
			out, err := index.NewEvaluator(context.Background()).Evaluate(1, 1, CellData{Raw: "1"}, nil)
			if err != nil || out == nil || out.Notice != "" {
				t.Error(out, err)
			}
			got <- index
		}()
	}
	wg.Wait()
	close(got)
	var first *SourceConditionalFormats
	for index := range got {
		if first != nil && first != index {
			t.Fatal("same source was decoded twice")
		}
		first = index
	}
	if w.conditionalBytes <= 0 || w.conditionalBytes > formats.ConditionalCacheBytes {
		t.Fatal("invalid cache accounting", w.conditionalBytes)
	}
	original, err := os.ReadFile(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.Replace(original, []byte("<conditionalFormatting"), []byte("<conditionalFORMATTING"), 1)
	w.conditionalCache = nil
	w.conditionalBytes = 0
	if err := os.WriteFile(idx.XMLPath, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx); err == nil {
		t.Fatal("corrupt block became valid metadata")
	}
	if err := os.WriteFile(idx.XMLPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx); err != nil || index.Summary().Rules != 1 {
		t.Fatal("failed load poisoned retry", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.SourceConditionalFormats(ctx, w.Sheets[0], idx); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestConditionalCacheWorkAndTextBudgetsAreBoundedAndUncertainStateSurvives(t *testing.T) {
	_, _, index := conditionalFixture(t, `<conditionalFormatting sqref="A1:XFD1048576"><cfRule type="cellIs" operator="greaterThan" dxfId="0" priority="1"><formula>0</formula></cfRule></conditionalFormatting>`)
	if len(index.nodes) != 1 || index.bytes > 10000 {
		t.Fatal("worksheet range expanded into cells", len(index.nodes), index.bytes)
	}
	e := index.NewEvaluator(context.Background())
	e.engine.Work = 0
	out, err := e.Evaluate(1, 1, CellData{Raw: "1"}, nil)
	if err != nil || out == nil || out.Notice == "" {
		t.Fatal(out, err)
	}
	e = index.NewEvaluator(context.Background())
	e.engine.TextBytes = 0
	out, err = e.Evaluate(1, 1, CellData{Raw: "1"}, nil)
	if err != nil || out == nil || out.Notice == "" {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.NewEvaluator(ctx).Evaluate(1, 1, CellData{Raw: "1"}, nil); err != context.Canceled {
		t.Fatal(err)
	}
	w := &Workbook{}
	for i := 0; i < 20; i++ {
		w.cacheConditionalLocked(fmt.Sprint(i), &SourceConditionalFormats{bytes: 4 << 20})
	}
	if len(w.conditionalCache) != 8 || w.conditionalBytes != formats.ConditionalCacheBytes {
		t.Fatal("cache did not evict by byte cost", len(w.conditionalCache), w.conditionalBytes)
	}
	w.cacheConditionalLocked("oversized", &SourceConditionalFormats{bytes: formats.ConditionalCacheBytes + 1})
	if len(w.conditionalCache) != 8 || w.conditionalBytes != formats.ConditionalCacheBytes {
		t.Fatal("oversized index retained")
	}
}

func TestMalformedConditionalMetadataKeepsItsCauseAndDoesNotGuessPriorityOrFormula(t *testing.T) {
	for _, tc := range []struct {
		xml    string
		global bool
	}{
		{`<conditionalFormatting sqref="A1"><cfRule type="expression" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting>`, true},
		{`<conditionalFormatting sqref="A1"><cfRule type="expression" dxfId="0" priority="1"><formula>TRUE()</formula></cfRule><cfRule type="expression" dxfId="1" priority="1"><formula>FALSE()</formula></cfRule></conditionalFormatting>`, true},
		{`<conditionalFormatting sqref="A0"><cfRule type="expression" dxfId="0" priority="1"><formula>TRUE()</formula></cfRule></conditionalFormatting>`, true},
		{`<conditionalFormatting sqref="A1"><cfRule type="expression" dxfId="0" priority="1"><formula>` + strings.Repeat("x", formats.ConditionalFormulaBytes+1) + `</formula></cfRule></conditionalFormatting>`, false},
		{`<conditionalFormatting sqref="A1"><cfRule type="expression" dxfId="0" priority="1"><formula><extra>TRUE()</extra></formula></cfRule></conditionalFormatting>`, false},
	} {
		_, _, index := conditionalFixture(t, tc.xml)
		summary := index.Summary()
		if (summary.Notice != "") != tc.global || !tc.global && summary.Pending == 0 {
			t.Fatal(summary)
		}
		out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
		if out == nil || out.Notice == "" || !out.Style.Empty() {
			t.Fatal("malformed metadata was applied", out)
		}
		if _, err := json.Marshal(summary); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConditionalComplexFormulasLogicalArithmeticTextDateAndCellIs(t *testing.T) {
	rules := strings.Join([]string{
		// Zebra striping + lower-priority style must both apply when evaluated.
		`<conditionalFormatting sqref="A1:A6">`,
		`<cfRule type="expression" dxfId="0" priority="1"><formula>MOD(ROW(),2)=0</formula></cfRule>`,
		`<cfRule type="expression" dxfId="1" priority="2"><formula>AND($A1&gt;0, OR($B1="异常", ABS($A1-$C1)&gt;0.01))</formula></cfRule>`,
		`</conditionalFormatting>`,
		// Lazy IF + IFERROR catching #DIV/0! without swallowing missing cache.
		`<conditionalFormatting sqref="D1:D6">`,
		`<cfRule type="expression" dxfId="0" priority="3"><formula>IF($C1=0, FALSE, $A1/$C1&gt;0.5)</formula></cfRule>`,
		`<cfRule type="expression" dxfId="1" priority="4"><formula>IFERROR($A1/$C1&gt;1, FALSE)</formula></cfRule>`,
		`</conditionalFormatting>`,
		// Text, ISBLANK vs empty string, SEARCH returning positive integer as truthy and #VALUE! as falsy.
		`<conditionalFormatting sqref="E1:E6">`,
		`<cfRule type="expression" dxfId="0" priority="5"><formula>AND(NOT(ISBLANK(E1)), SEARCH("ab*z", TRIM(E1)))</formula></cfRule>`,
		`<cfRule type="expression" dxfId="1" priority="6"><formula>LEFT(E1,2)&amp;RIGHT(E1,1)="XYZ"</formula></cfRule>`,
		`</conditionalFormatting>`,
		// Precedence (-2^2 = 4, -(2^2) = -4), date functions, same-sheet qualifier.
		`<conditionalFormatting sqref="F1:F6">`,
		`<cfRule type="expression" dxfId="0" priority="7"><formula>AND(-2^2=4, -(2^2)=-4, YEAR(Data!$F1)=2026, WEEKDAY(Data!$F1,2)&gt;5, 'Data'!$F1&gt;=DATE(2026,1,1))</formula></cfRule>`,
		`</conditionalFormatting>`,
		// cellIs with expression operand and reversed between bounds.
		`<conditionalFormatting sqref="G1:G6">`,
		`<cfRule type="cellIs" operator="greaterThan" dxfId="0" priority="8"><formula>$C1*1.1</formula></cfRule>`,
		`<cfRule type="cellIs" operator="between" dxfId="1" priority="9"><formula>10</formula><formula>5</formula></cfRule>`,
		`</conditionalFormatting>`,
		// Cross-sheet reference remains honestly unevaluated.
		`<conditionalFormatting sqref="H1:H2">`,
		`<cfRule type="expression" dxfId="0" priority="10"><formula>OtherSheet!$A1&gt;0</formula></cfRule>`,
		`</conditionalFormatting>`,
	}, "")
	_, _, index := conditionalFixture(t, rules)
	if summary := index.Summary(); summary.Rules != 10 || summary.Pending != 1 || summary.Notice != "" {
		for _, r := range index.rules {
			if r.predicateNotice != "" || r.styleNotice != "" {
				t.Logf("pending rule priority=%d formulas=%v predicate=%s style=%s", r.source.Priority, r.source.Formulas, r.predicateNotice, r.styleNotice)
			}
		}
		t.Fatalf("unexpected summary: %+v", summary)
	}
	cells := map[[2]int]CellData{
		{2, 1}: {Raw: "10.05"}, {2, 2}: {Raw: "正常", Type: "inlineStr"}, {2, 3}: {Raw: "10.00"},
		{3, 1}: {Raw: "10.00"}, {3, 2}: {Raw: "异常", Type: "inlineStr"}, {3, 3}: {Raw: "10.00"},
		{4, 1}: {Raw: "10"}, {4, 3}: {Raw: "0"},
		{5, 1}: {Raw: "", HasFormula: true, CacheMissing: true}, {5, 3}: {Raw: "2"},
	}
	resolver := func(row, col int) (CellData, bool, error) {
		return cells[[2]int{row, col}], true, nil
	}
	// Row 2 Col A: MOD(2,2)=0 matches priority 1 AND ABS(10.05-10.00)>0.01 matches priority 2 (no blocking notice).
	r2a := evaluateConditional(t, index, 2, 1, cells[[2]int{2, 1}], resolver)
	if r2a == nil || r2a.Notice != "" || !reflect.DeepEqual(r2a.Priorities, []int{1, 2}) || r2a.Style.Background == nil || *r2a.Style.Background != "#FFFF00" {
		t.Fatalf("row 2 col A failed: %+v", r2a)
	}
	// Row 3 Col A: MOD(3,2)=1 false, priority 2 matches via $B3="异常".
	r3a := evaluateConditional(t, index, 3, 1, cells[[2]int{3, 1}], resolver)
	if r3a == nil || r3a.Notice != "" || !reflect.DeepEqual(r3a.Priorities, []int{2}) {
		t.Fatalf("row 3 col A failed: %+v", r3a)
	}
	// Row 4 Col D: $C4=0 -> IF short-circuits to FALSE, IFERROR catches #DIV/0! -> FALSE (no match, no notice).
	if r4d := evaluateConditional(t, index, 4, 4, CellData{}, resolver); r4d != nil {
		t.Fatalf("row 4 col D should not match: %+v", r4d)
	}
	// Row 5 Col D: $A5 has CacheMissing -> IFERROR must NOT swallow missing-cache notice.
	r5d := evaluateConditional(t, index, 5, 4, CellData{}, resolver)
	if r5d == nil || !strings.Contains(r5d.Notice, "没有保存") {
		t.Fatalf("missing cache swallowed by IF/IFERROR: %+v", r5d)
	}
	// Col E: SEARCH wildcard, blank vs empty string, concatenation.
	if out := evaluateConditional(t, index, 1, 5, CellData{Raw: "  AB123z  ", Type: "s"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{5}) {
		t.Fatalf("wildcard SEARCH failed: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 5, CellData{Raw: "XY99Z", Type: "s"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{6}) {
		t.Fatalf("LEFT/RIGHT concat failed: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 5, CellData{}, nil); out != nil {
		t.Fatalf("blank cell should not match Col E: %+v", out)
	}
	// Col F: 2026-01-03 is Saturday -> Excel serial 46025 (1900 date system).
	if out := evaluateConditional(t, index, 1, 6, CellData{Raw: "46025"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{7}) {
		t.Fatalf("date/precedence/same-sheet formula failed: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 6, CellData{Raw: "46027"}, nil); out != nil {
		t.Fatalf("weekday Monday should not match >5: %+v", out)
	}
	// Col G: cellIs > $C2*1.1 (11) and reversed between (10, 5) matching 7.
	if out := evaluateConditional(t, index, 2, 7, CellData{Raw: "7"}, resolver); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{9}) {
		t.Fatalf("reversed between failed: %+v", out)
	}
	if out := evaluateConditional(t, index, 2, 7, CellData{Raw: "12"}, resolver); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{8}) {
		t.Fatalf("cellIs expression operand failed: %+v", out)
	}
	// Col H: cross-sheet ref keeps honest notice.
	if out := evaluateConditional(t, index, 1, 8, CellData{Raw: "1"}, nil); out == nil || !strings.Contains(out.Notice, "跨工作表") {
		t.Fatalf("cross-sheet ref did not report honest notice: %+v", out)
	}
}

func TestConditionalOOXMLCompatibilityDXFPatternFillSqrefAndCanonicalFormulas(t *testing.T) {
	isolateTestStorage(t)
	styles := `<styleSheet><fonts count="1"><font><name val="宋体"/><sz val="11"/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="2"><dxf><fill><patternFill><bgColor rgb="FFFFC000"/></patternFill></fill></dxf><dxf><fill><patternFill patternType="solid"><fgColor auto="1"/><bgColor rgb="FFFFC7CE"/></patternFill></fill></dxf></dxfs></styleSheet>`
	rules := strings.Join([]string{
		`<conditionalFormatting sqref="A:B"><cfRule type="beginsWith" text="abc" dxfId="0" priority="1"><formula>LEFT(A1,3)="abc"</formula></cfRule></conditionalFormatting>`,
		`<conditionalFormatting sqref="2:3"><cfRule type="notContainsText" text="err" dxfId="1" priority="2"><formula>NOT(ISNUMBER(SEARCH("err",A2)))</formula></cfRule></conditionalFormatting>`,
		`<conditionalFormatting sqref="D1:D2"><cfRule type="endsWith" text="元" dxfId="0" priority="3"><formula>RIGHT(D1,1)="元"</formula></cfRule></conditionalFormatting>`,
	}, "")
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>ABC123</t></is></c></row></sheetData>`+rules+`</worksheet>`, styles)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	if summary := index.Summary(); summary.Pending != 0 || summary.Notice != "" {
		t.Fatalf("expected clean OOXML compatibility, got %+v", summary)
	}
	out1 := evaluateConditional(t, index, 1, 1, CellData{Raw: "ABC123", Type: "inlineStr"}, nil)
	if out1 == nil || out1.Notice != "" || out1.Style.Background == nil || *out1.Style.Background != "#FFC000" {
		t.Fatalf("omitted patternType solid bgColor or whole-col sqref failed: %+v", out1)
	}
	out2 := evaluateConditional(t, index, 2, 10, CellData{Raw: "ok", Type: "inlineStr"}, nil)
	if out2 == nil || out2.Notice != "" || out2.Style.Background == nil || *out2.Style.Background != "#FFC7CE" {
		t.Fatalf("whole-row sqref or NOT(ISNUMBER(SEARCH)) failed: %+v", out2)
	}
	out3 := evaluateConditional(t, index, 1, 4, CellData{Raw: "100元", Type: "inlineStr"}, nil)
	if out3 == nil || out3.Notice != "" || out3.Style.Background == nil || *out3.Style.Background != "#FFC000" {
		t.Fatalf("RIGHT literal length endsWith failed: %+v", out3)
	}
}

func TestConditionalEdgeCasesDateOverflowInvalidUTF8GroupNoticeAndDeterminism(t *testing.T) {
	isolateTestStorage(t)
	// 1. Invalid UTF-8 bytes in formula must never panic in decodeUTF8At.
	if expr, _, notice := (*Workbook)(nil).compileConditionalExpr("\xff=1", "Sheet1"); expr != nil || notice == "" {
		t.Fatalf("invalid UTF-8 formula should fail cleanly without panic: expr=%v notice=%q", expr, notice)
	}

	// 2. Date serials beyond Go's 292-year time.Duration limit (up to 9999-12-31)
	//    and negative fractional serials (-0.5 -> #NUM!).
	styles := `<styleSheet><fonts count="1"><font><name val="宋体"/><sz val="11"/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="2"><dxf><fill><patternFill patternType="solid"><bgColor rgb="FFCCFFCC"/></patternFill></fill></dxf><dxf><fill><patternFill patternType="solid"><bgColor rgb="FFFFCCCC"/></patternFill></fill></dxf></dxfs></styleSheet>`
	rules := strings.Join([]string{
		`<conditionalFormatting sqref="A1:A3"><cfRule type="expression" dxfId="0" priority="1"><formula>DATE(9999,12,31)=2958465</formula></cfRule></conditionalFormatting>`,
		`<conditionalFormatting sqref="B1:B1"><cfRule type="expression" dxfId="0" priority="2"><formula>ISERROR(YEAR(-0.5))</formula></cfRule></conditionalFormatting>`,
		// 3. Unicode simple-fold wildcard equivalence (Kelvin sign U+212A matches ASCII 'k').
		`<conditionalFormatting sqref="C1:C1"><cfRule type="expression" dxfId="0" priority="3"><formula>ISNUMBER(SEARCH("k*",C1))</formula></cfRule></conditionalFormatting>`,
		// 4. Unknown conditionalFormatting attribute sets group.notice; even when the
		//    expression compiles and evaluates to FALSE, it must fail closed and block lower priorities.
		`<conditionalFormatting sqref="D1:D1" unknownAttr="1"><cfRule type="expression" dxfId="0" priority="4"><formula>D1&gt;100</formula></cfRule></conditionalFormatting>`,
		`<conditionalFormatting sqref="D1:D1"><cfRule type="cellIs" operator="greaterThan" dxfId="1" priority="5"><formula>0</formula></cfRule></conditionalFormatting>`,
		// 5. Deterministic row-major error selection in SUMIFS across repeated runs.
		`<conditionalFormatting sqref="E1:E1"><cfRule type="expression" dxfId="0" priority="6"><formula>ISNA(SUMIFS($F$1:$F$4,$G$1:$G$4,"hit"))</formula></cfRule></conditionalFormatting>`,
	}, "")
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>`+rules+`</worksheet>`, styles)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}

	if out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{1}) {
		t.Fatalf("DATE(9999,12,31) overflowed or failed: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 2, CellData{Raw: "1"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{2}) {
		t.Fatalf("YEAR(-0.5) should return #NUM! error: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 3, CellData{Raw: "\u212Aelvin", Type: "s"}, nil); out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{3}) {
		t.Fatalf("Unicode simple-fold wildcard failed for Kelvin sign: %+v", out)
	}
	if out := evaluateConditional(t, index, 1, 4, CellData{Raw: "10"}, nil); out == nil || out.Matched != 0 || !strings.Contains(out.Notice, "尚未支持的属性") {
		t.Fatalf("group.notice must fail closed and block lower priority even when expr is false: %+v", out)
	}

	resolver := func(row, col int) (CellData, bool, error) {
		if col == 7 && row >= 1 && row <= 4 {
			return CellData{Raw: "hit", Type: "s"}, true, nil
		}
		if col == 6 {
			switch row {
			case 1:
				return CellData{Raw: "#N/A", Type: "e"}, true, nil
			case 2:
				return CellData{Raw: "#DIV/0!", Type: "e"}, true, nil
			case 3:
				return CellData{Raw: "#VALUE!", Type: "e"}, true, nil
			case 4:
				return CellData{Raw: "#REF!", Type: "e"}, true, nil
			}
		}
		return CellData{}, true, nil
	}
	for iter := 0; iter < 25; iter++ {
		out := evaluateConditional(t, index, 1, 5, CellData{Raw: "1"}, resolver)
		if out == nil || out.Notice != "" || !reflect.DeepEqual(out.Priorities, []int{6}) {
			t.Fatalf("SUMIFS error order was non-deterministic on iter %d: %+v", iter, out)
		}
	}
}

func TestConditionalFormulasExcelBoundaryResults(t *testing.T) {
	// Expected results come from an isolated Excel workbook, with A1 left empty.
	for _, tc := range []struct {
		formula  string
		date1904 bool
	}{
		{`AND(DATE(1900,2,29)=60,DATE(1900,3,0)=60,DATE(1900,1,60)=60,DATE(1900,2,30)=61)`, false},
		{`AND(WEEKDAY(1)=1,WEEKDAY(59)=3,WEEKDAY(60)=4,WEEKDAY(61)=5)`, false},
		{`AND(DATE(1904,1,1)=0,WEEKDAY(0)=6,ISERROR(DATE(1904,0,32)))`, true},
		{`AND(ISERROR(DATE(9999,13,0)),DATE(9999,12,31)=2958465)`, false},
		{`AND(A1,TRUE,ISERROR(OR(A1)),IF("TRUE",TRUE,FALSE),NOT("FALSE"))`, false},
		{`AND(FIND("","abc",4)=4,ISERROR(SEARCH("","abc",4)),ISERROR(SEARCH("*","")))`, false},
		{`AND(SEARCH("ab~","abc")=1,SEARCH("~a","~a")=2,SEARCH("a~~","xa~")=2)`, false},
	} {
		t.Run(tc.formula, func(t *testing.T) {
			index := SourceConditionalFormats{date1904: tc.date1904}
			e := index.NewEvaluator(context.Background())
			e.engine.Row, e.engine.Col = 1, 1
			expr, _, notice := (*Workbook)(nil).compileConditionalExpr(tc.formula, "Sheet1")
			if notice != "" {
				t.Fatal(notice)
			}
			result, notice, err := e.engine.Eval(expr, 1, 1, nil)
			if err != nil || notice != "" || result.Kind != "boolean" || !result.Boolean {
				t.Fatalf("Excel returned TRUE, got %+v, notice %q, error %v", result, notice, err)
			}
		})
	}
}

func TestConditionalLimitsPreserveUnknownResults(t *testing.T) {
	group := &conditionalGroup{anchorRow: 1, anchorCol: 1}
	for _, formula := range []string{
		`IFERROR(SEARCH("*z", A1), 0)`,
		`IFERROR(COUNTIF(A1,"*z"), 0)`,
		`IFERROR(MATCH("*z",A1,0), 0)`,
	} {
		e := (&SourceConditionalFormats{}).NewEvaluator(context.Background())
		e.engine.Row, e.engine.Col, e.engine.Work = 1, 1, 18
		e.engine.Cell = conditional.CellOperand{Raw: "ordinary worksheet text", Type: "s"}
		node, _, notice := (*Workbook)(nil).compileConditionalExpr(formula, "Sheet1")
		if notice != "" {
			t.Fatal(notice)
		}
		_, notice, err := e.engine.Eval(node, group.anchorRow, group.anchorCol, group.areas)
		if err != nil || notice != conditionalWorkNotice {
			t.Errorf("%s must retain the work-limit notice, got %q, %v", formula, notice, err)
		}
	}

	e := (&SourceConditionalFormats{}).NewEvaluator(context.Background())
	e.engine.Row, e.engine.Col = 1, 2
	e.engine.Resolver = func(row, col int) (conditional.CellOperand, bool, error) {
		return conditional.CellOperand{Raw: "0.9"}, true, nil
	}
	node, _, _ := (*Workbook)(nil).compileConditionalExpr("IFERROR(PRODUCT(A1:A750),0)", "Sheet1")
	if _, notice, err := e.engine.Eval(node, group.anchorRow, group.anchorCol, group.areas); err != nil || !strings.Contains(notice, "容量") {
		t.Errorf("bounded exact product lost its limit: %q, %v", notice, err)
	}
	node, _, _ = (*Workbook)(nil).compileConditionalExpr("ISERROR(PRODUCT(1E300,1E300,0))", "Sheet1")
	if result, notice, err := e.engine.Eval(node, group.anchorRow, group.anchorCol, group.areas); err != nil || notice != "" || !result.Boolean {
		t.Errorf("Excel numeric overflow lost: %+v, %q, %v", result, notice, err)
	}
}
