package workbook

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func withoutRichMetadata(rows []RowData) []RowData {
	copyRows := append([]RowData{}, rows...)
	for i := range copyRows {
		copyRows[i].Cells = append([]CellData{}, copyRows[i].Cells...)
		for j := range copyRows[i].Cells {
			copyRows[i].Cells[j].RichText = nil
		}
	}
	return copyRows
}

func presentationFixture(t *testing.T) (*Workbook, *SheetIndex) {
	t.Helper()
	isolateTestStorage(t)
	w, err := OpenWorkbook(richWorkbookFile(t, sharedRichFixture))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRichStore(w.richShared) })
	index := syntheticIndex(t, `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><r><rPr><b val="0"/></rPr><t>中😀</t></r><r><t>文</t></r></is></c><c r="C1" t="inlineStr"><is><t>普通</t></is></c><c r="D1"><f>1+1</f><v>2</v></c><c r="E1" t="inlineStr"><is><extra><t>隐藏元数据</t></extra><r><rPr><i/></rPr><t>实际</t></r></is></c></row>`, `<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="inlineStr"><is><r><rPr><u/></rPr><t><![CDATA[<source>]]></t></r><rPh><t>注音</t></rPh></is></c><c r="D2"><f>1+1</f></c></row>`)
	return w, index
}

func TestPresentationReadIsExplicitAndPreservesExactValuesTypesCoordinatesAndFormulaStates(t *testing.T) {
	w, index := presentationFixture(t)
	ctx := context.Background()
	plain, err := w.ReadRowsContext(ctx, index, 0, 2, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range plain {
		for _, cell := range row.Cells {
			if cell.RichText != nil {
				t.Fatal("ordinary scans decoded font metadata")
			}
		}
	}
	styled, err := w.ReadRowsWithOptionsContext(ctx, index, 0, 2, 1, 5, RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, withoutRichMetadata(styled)) {
		t.Fatal("presentation altered source evidence", plain, styled)
	}
	if styled[0].Cells[0].Raw != "001" || styled[0].Cells[0].Type != "s" || styled[0].Cells[0].RichText == nil || !*styled[0].Cells[0].RichText.Runs[0].Font.Bold {
		t.Fatal(styled[0].Cells[0])
	}
	inline := styled[0].Cells[1]
	if inline.Raw != "中😀文" || inline.RichText == nil || len(inline.RichText.Runs) != 2 || inline.RichText.Runs[0].End != 3 || inline.RichText.Runs[0].Font.Bold == nil || *inline.RichText.Runs[0].Font.Bold {
		t.Fatal(inline)
	}
	if styled[0].Cells[2].RichText != nil || styled[0].Cells[3].Formula != "=1+1" || styled[0].Cells[4].Raw != "实际" || styled[0].Cells[4].RichText == nil {
		t.Fatal(styled[0])
	}
	if styled[1].Cells[1].Raw != "<source>" || styled[1].Cells[1].RichText == nil || !styled[1].Cells[2].CacheMissing {
		t.Fatal(styled[1])
	}
}

func TestPresentationFastStrictSparseAndStreamedReadersAgree(t *testing.T) {
	w, index := presentationFixture(t)
	ctx := context.Background()
	options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true}
	file, err := os.Open(index.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	all, err := w.ReadRowsWithOptionsContext(ctx, index, 0, 2, 1, 5, options)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < index.RowCount(); i++ {
		strict, err := w.readRowFromFileWithOptions(file, index, i, 1, 5, nil, options)
		if err != nil || !reflect.DeepEqual(strict, all[i]) {
			t.Fatal("strict/fast rich text mismatch", i, strict, all[i], err)
		}
	}
	options.Columns = []int{1, 5}
	sparse, err := w.ReadRowsByIndicesWithOptionsContext(ctx, index, []int{1, 0}, 1, 5, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(sparse) != 2 || sparse[0].Number != 2 || sparse[1].Number != 1 || len(sparse[1].Cells) != 2 || sparse[1].Cells[1].Ref != "E1" || sparse[1].Cells[1].RichText == nil {
		t.Fatal(sparse)
	}
	var streamed []RowData
	if err := w.VisitRowsWithOptionsContext(ctx, index, slices.Values([]int{1, 0}), 1, 5, options, func(row RowData) error { streamed = append(streamed, row); return nil }); err != nil || !reflect.DeepEqual(streamed, sparse) {
		t.Fatal("stream and sparse presentation differ", streamed, sparse, err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.ReadRowsWithOptionsContext(ctx, index, 0, 2, 1, 5, options); err != context.Canceled {
		t.Fatal("cancelled metadata read proceeded", err)
	}
}

func TestInlineMetadataNeverBecomesAValueInSearchOrPresentation(t *testing.T) {
	for _, body := range []string{
		`<is><extra><t>错误值</t></extra><t>原值</t></is>`,
		`<is><r><rPr><unknown><t>错误值</t></unknown></rPr><t>原值</t></r></is>`,
		`<is><t><![CDATA[原值]]></t><rPh><t>错误值</t></rPh></is>`,
	} {
		value, ok := inlineStringValue([]byte(body))
		if !ok || value != "原值" {
			t.Fatal("inline fast value promoted metadata", body, value, ok)
		}
		text, _, err := styledText(t, &Workbook{}, body)
		if err != nil || text != value {
			t.Fatal("styled and ordinary value differ", text, value, err)
		}
	}
}

func TestBrokenSharedPresentationRecordLeavesOrdinaryValuesAndReportsCellNotice(t *testing.T) {
	w, index := presentationFixture(t)
	path := w.sharedRichPath()
	closeRichStore(w.richShared)
	w.richShared = nil
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[richStoreHeader+richStoreRecordHeader+10] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	w.richShared, err = openRichTextStore(path, len(w.Shared))
	if err != nil {
		t.Fatal(err)
	}
	styled, err := w.ReadRowsWithOptionsContext(context.Background(), index, 0, 1, 1, 1, RowReadOptions{FormatValue: true, RichText: true})
	if err != nil || styled[0].Cells[0].Raw != "001" || styled[0].Cells[0].RichText == nil || !strings.Contains(styled[0].Cells[0].RichText.Notice, "校验失败") {
		t.Fatal(styled, err)
	}
	plain, err := w.ReadRowsContext(context.Background(), index, 0, 1, 1, 1)
	if err != nil || plain[0].Cells[0].Raw != "001" || plain[0].Cells[0].RichText != nil {
		t.Fatal("presentation corruption hid ordinary source", plain, err)
	}
}

func TestPresentationBudgetIsSharedAcrossResidentRowsAndResetForTheNextRead(t *testing.T) {
	w, index := presentationFixture(t)
	first, err := w.richShared.lookup(0, w.Shared[0])
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(first)
	options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, richBudget: &richReadBudget{remaining: len(encoded)}}
	rows, err := w.ReadRowsWithOptionsContext(context.Background(), index, 0, 2, 1, 5, options)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Cells[0].RichText.Notice != "" || rows[0].Cells[1].Raw != "中😀文" || !strings.Contains(rows[0].Cells[1].RichText.Notice, "容量") || !strings.Contains(rows[1].Cells[0].RichText.Notice, "容量") {
		t.Fatal(rows)
	}
	if rows[0].Cells[2].RichText != nil {
		t.Fatal("budget limit invented formatting for an ordinary cell")
	}
	fresh, err := w.ReadRowsWithOptionsContext(context.Background(), index, 0, 1, 1, 5, RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true})
	if err != nil || fresh[0].Cells[1].RichText.Notice != "" {
		t.Fatal("another read inherited the previous budget", fresh, err)
	}
}

func TestLateRichRowFallbackRestoresOnlyItsOwnBudget(t *testing.T) {
	w, _ := presentationFixture(t)
	first, _ := w.richShared.lookup(0, w.Shared[0])
	encoded, _ := json.Marshal(first)
	index := syntheticIndex(t, `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"></c></row>`)
	rows, err := w.ReadRowsWithOptionsContext(context.Background(), index, 0, 1, 1, 2, RowReadOptions{FormatValue: true, RichText: true, richBudget: &richReadBudget{remaining: len(encoded)}})
	if err != nil || len(rows) != 1 || rows[0].Cells[0].RichText == nil || rows[0].Cells[0].RichText.Notice != "" || rows[0].Cells[0].RichText.Runs[0].Font.Bold == nil || !*rows[0].Cells[0].RichText.Runs[0].Font.Bold {
		t.Fatal("fallback consumed its presentation budget twice", rows, err)
	}
}
