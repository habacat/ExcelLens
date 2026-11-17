package workbook

import (
	"bytes"
	"context"
	"encoding/json"
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
	if unknown == nil || unknown.Notice == "" || unknown.Matched != 0 || !unknown.Style.empty() {
		t.Fatal("unknown predicate was treated as false", unknown)
	}
	falseRule := evaluateConditional(t, index, 1, 2, CellData{Raw: "1"}, nil)
	if falseRule == nil || falseRule.Notice != "" || falseRule.Style.Background == nil {
		t.Fatal("unused unsupported DXF blocked a proven false predicate", falseRule)
	}
	trueRule := evaluateConditional(t, index, 2, 2, CellData{Raw: "200"}, nil)
	if trueRule == nil || trueRule.Notice == "" || !trueRule.Style.empty() {
		t.Fatal("missing DXF silently became a default style", trueRule)
	}
	missing := evaluateConditional(t, index, 1, 3, CellData{Raw: "", HasFormula: true, CacheMissing: true}, nil)
	if missing == nil || !strings.Contains(missing.Notice, "没有保存") || !missing.Style.empty() {
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
	e.work = 0
	out, err := e.Evaluate(1, 1, CellData{Raw: "1"}, nil)
	if err != nil || out == nil || out.Notice == "" {
		t.Fatal(out, err)
	}
	e = index.NewEvaluator(context.Background())
	e.textBytes = 0
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
		if out == nil || out.Notice == "" || !out.Style.empty() {
			t.Fatal("malformed metadata was applied", out)
		}
		if _, err := json.Marshal(summary); err != nil {
			t.Fatal(err)
		}
	}
}
