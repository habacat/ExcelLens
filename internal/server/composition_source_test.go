package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"excellens/internal/analysis"
	"excellens/internal/composition"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

func compositionRef(t *testing.T, a *App, book, source string, header int) sourceview.TableRef {
	t.Helper()
	ref := reconcileRef(t, a, book, source, header)
	wb := a.resolveReconcileBook(book)
	if wb == nil {
		t.Fatal("source not open")
	}
	return sourceview.TableRef{Book: book, Fingerprint: wb.Fingerprint, Source: source, Stamp: ref.Stamp, HeaderRow: header}
}

func compositionCapture(t *testing.T, a *App, definition sourceview.Append) *compositionSnapshot {
	t.Helper()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	snapshot, err := a.captureComposition(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.close)
	return snapshot
}

func compositionRows(t *testing.T, a *App, snapshot *compositionSnapshot, columns ...int) []composition.Record {
	t.Helper()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	var rows []composition.Record
	if err := snapshot.scan(context.Background(), columns, func(row composition.Record) error {
		row.Cells = slices.Clone(row.Cells)
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func compositionDefinition(t *testing.T, a *App) sourceview.Append {
	t.Helper()
	primary := a.currentWorkbook()
	first := compositionRef(t, a, sourceBookID(primary), "wb:"+primary.Sheets[0].Name, 1)
	other := attachReconcileCSV(t, a, "other.csv", "金额,编号\n0.1,0002\n-2.25,0003\n")
	book := a.resolveReconcileBook(other)
	second := compositionRef(t, a, other, "wb:"+book.Sheets[0].Name, 1)
	return sourceview.Append{Version: sourceview.AppendVersion, Name: "追加", Fields: []string{"编号", "金额"}, Inputs: []sourceview.AppendInput{{Ref: first, Columns: []int{1, 2}}, {Ref: second, Columns: []int{2, 1}}}}
}

func TestCompositionOOXMLSourceRetainsNativeTextAmountsFormulaStateAndSparseCoordinates(t *testing.T) {
	a, file := recordFixture(t, 12, "")
	before := printScaleHash(t, file)
	definition := compositionDefinition(t, a)
	definition.Fields = append(definition.Fields, "源公式", "源文本金额")
	definition.Inputs[0].Columns = append(definition.Inputs[0].Columns, 6, 3)
	definition.Inputs[1].Columns = append(definition.Inputs[1].Columns, 0, 0)
	snapshot := compositionCapture(t, a, definition)
	rows := compositionRows(t, a, snapshot, 1, 2, 3, 4)
	if len(rows) != 4 || rows[0].Row != 2 || rows[1].Row != 4 || rows[2].Row != 2 || rows[2].Sequence != 3 || rows[2].Input != 1 {
		t.Fatal("native sparse/source coordinates were replaced by union row numbers", rows)
	}
	if cell := rows[0].Cells[0].Data; cell.Type != "inlineStr" || cell.Raw != "001" || cell.Ref != "A2" {
		t.Fatal("native identifier changed", cell)
	}
	if cell := rows[0].Cells[1].Data; cell.Type != "" || !sourceCellNumeric(a.currentWorkbook(), cell) || cell.Raw != "9007199254740993.31" || cell.Display != "9,007,199,254,740,993.31" {
		t.Fatal("exact native amount changed", cell)
	}
	if cell := rows[0].Cells[2].Data; !cell.HasFormula || !cell.CacheMissing || cell.Formula != "=SUM(B2:B2)" {
		t.Fatal("source formula/cache state lost", cell)
	}
	if cell := rows[0].Cells[3].Data; cell.Raw != "1,234.50" || cell.Type != "inlineStr" || sourceCellNumeric(a.currentWorkbook(), cell) {
		t.Fatal("native text amount was converted to a numeric source value", cell)
	}
	if cell := rows[2].Cells[0]; cell.SourceColumn != 2 || cell.Data.Col != 2 || cell.Data.Raw != "0002" || cell.Data.Ref != "B2" || rows[2].Cells[2].Exists {
		t.Fatal("mapping changed text or invented source data", rows[2])
	}
	if printScaleHash(t, file) != before {
		t.Fatal("composition changed the original workbook")
	}
}

func TestCompositionUsesRealTextHTMLAndBIFFAdaptersWithoutNewValueInterpretation(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,金额\n0001,9007199254740993.31\n0002,-2.25\n"},
		{"source.tsv", "tsv", "编号\t金额\n0001\t9007199254740993.31\n0002\t-2.25\n"},
		{"source.txt", "txt", "编号\t金额\n0001\t9007199254740993.31\n0002\t-2.25\n"},
		{"source.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>0001</td><td>9007199254740993.31</td></tr><tr><td>0002</td><td>-2.25</td></tr></table></html>"},
		{"source.xls", "xls", ""},
	} {
		t.Run(tc.format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body == "" {
				printBIFFSource(t, file, 4)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			definition := compositionDefinition(t, a)
			snapshot := compositionCapture(t, a, definition)
			rows := compositionRows(t, a, snapshot, 1, 2)
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(meta.Path)
			if wb.Format != tc.format || snapshot.data.info[0].Format != tc.format || len(rows) < 3 {
				t.Fatal("wrong real adapter or incomplete rows", wb.Format, rows)
			}
			direct, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2}, 1, 2, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true})
			if err != nil {
				t.Fatal(err)
			}
			for i, cell := range direct[0].Cells {
				if !reflect.DeepEqual(rows[0].Cells[i].Data, cell) {
					t.Fatal("composition forked the common adapter semantics", rows[0].Cells[i], cell)
				}
			}
			if printScaleHash(t, file) != before {
				t.Fatal("input changed")
			}
		})
	}
}

func TestCompositionScopedRowsPreserveUnindexedBlanksAndIndependentSourceLease(t *testing.T) {
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view})
	definition := compositionDefinition(t, a)
	definition.Inputs[0].Ref.Scope = &captured.SourceScope
	snapshot := compositionCapture(t, a, definition)
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+view+`"}`)
	featureCall(t, a, "POST", "/api/analysis/source/clear", `{"id":"`+captured.ID+`"}`)
	rows := compositionRows(t, a, snapshot, 1, 2)
	if len(rows) != 5 || rows[0].Row != 4 || rows[1].Row != 6 || rows[2].Row != 7 || rows[1].Position != -1 || rows[1].Cells[0].Exists || rows[1].Cells[1].Exists {
		t.Fatal("clearing original owners widened the input or discarded a real blank source row", rows)
	}
}

func TestCompositionSourcePinsSurviveOwnerCloseAndFailedCaptureReleasesEveryPin(t *testing.T) {
	a, file := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	root := a.rootApp()
	pinCount := func() int {
		return root.sourcePins.Total()
	}
	baseline := pinCount()
	snapshot := compositionCapture(t, a, definition)
	borrowed, err := snapshot.retain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(borrowed.close)
	snapshot.close()
	if _, err := snapshot.retain(); err == nil {
		t.Fatal("closed owner created another lease")
	}
	if len(compositionRows(t, a, borrowed, 1)) != 4 || pinCount() != baseline+2 {
		t.Fatal("closing owner reclaimed active source pins")
	}
	borrowed.close()
	if pinCount() != baseline {
		t.Fatal("last independent owner did not release source pins")
	}
	definition.Inputs[1].Ref.Stamp = "stale"
	a.cacheMu.RLock()
	failed, err := a.captureComposition(context.Background(), definition)
	a.cacheMu.RUnlock()
	if err == nil || failed != nil || pinCount() != baseline {
		t.Fatal("failed capture retained pins or accepted changed source", err)
	}
	definition = compositionDefinition(t, a)
	snapshot = compositionCapture(t, a, definition)
	if err := os.WriteFile(file, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	a.cacheMu.RLock()
	err = snapshot.scan(context.Background(), []int{1}, func(composition.Record) error { t.Fatal("changed source emitted a row"); return nil })
	a.cacheMu.RUnlock()
	if err == nil {
		t.Fatal("changed workbook accepted as a complete union")
	}
}

func TestCompositionFrozenScopeKeepsExplicitBlankRowsBeyondUsedSourceRows(t *testing.T) {
	a, _ := rowViewFixture(t)
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Mode: "visible", ViewID: view, ExcludeThroughRow: 10})
	definition := compositionDefinition(t, a)
	definition.Inputs[0].Ref.Scope = &captured.SourceScope
	snapshot := compositionCapture(t, a, definition)
	rows := compositionRows(t, a, snapshot, 1, 2)
	found := false
	for _, row := range rows {
		if row.Input == 0 && row.Row == 10 {
			found = true
			if row.Position != -1 || row.Cells[0].Exists || row.Cells[1].Exists {
				t.Fatal("explicit blank frozen row was assigned a physical source position", row)
			}
		}
	}
	if !found {
		t.Fatal("valid frozen source prefix was truncated to the last used row")
	}
}

func TestCompositionAnalysisSheetIsAStableTextSnapshotWithContentVersion(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	wb := a.currentWorkbook()
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "text", Name: "原有分析表", Columns: []string{"金额", "编号"}, Rows: [][]string{{"0.10", "0001"}, {"=1+2", "0002"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.Inputs[0].Ref = compositionRef(t, a, sourceBookID(wb), "scratch:text", 0)
	definition.Inputs[0].Columns = []int{2, 1}
	snapshot := compositionCapture(t, a, definition)
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error { ws.Sheet("text").Rows[0][0] = "changed"; return nil }); err != nil {
		t.Fatal(err)
	}
	rows := compositionRows(t, a, snapshot, 1, 2)
	if rows[0].Cells[0].Data.Raw != "0001" || rows[0].Cells[1].Data.Raw != "0.10" || rows[1].Cells[1].Data.Raw != "=1+2" || rows[1].Cells[1].Data.HasFormula || rows[0].Cells[1].Data.Type != "s" {
		t.Fatal("later analysis edits changed an accepted snapshot or executed formula text", rows)
	}
}
