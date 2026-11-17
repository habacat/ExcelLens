package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/analysis"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

func relationServerSource(t *testing.T, a *App, book *workbook.Workbook, source string, columns ...int) reconcile.Source {
	t.Helper()
	ref := reconcileSourceRef{Book: sourceBookID(book), Source: source, HeaderRow: 1}
	if len(source) > 8 && source[:8] == "scratch:" {
		ref.HeaderRow = 0
	}
	result, err := a.buildRelationSource(context.Background(), book, ref, columns)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func relationServerQuery() reconcile.RelationQuery {
	return reconcile.RelationQuery{Keys: []reconcile.Pair{{Left: 1, Right: 1}}, LeftColumns: []int{1, 2}, RightColumns: []int{1, 2}}
}

func TestRelationActualAdaptersKeepSourceValuesTypesAndSparseRows(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"source.csv", "编号,金额\n001,9007199254740993.31\n002,-2.25\n"},
		{"source.tsv", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n"},
		{"source.txt", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n"},
		{"html.xls", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr><tr><td>002</td><td>-2.25</td></tr></table></html>"},
		{"native.xls", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body == "" {
				printBIFFSource(t, file, 4)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			a, meta := recordOpen(t, file)
			book := a.currentWorkbook()
			source := relationServerSource(t, a, book, "wb:"+meta.Name, 1, 2)
			before := printScaleHash(t, file)
			p, err := reconcile.PrepareRelation(context.Background(), filepath.Join(book.CacheDir, "queries"), relationServerQuery(), source, source, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			idx := book.GetCachedIndex(meta.Path)
			count, err := p.Stream(context.Background(), "inner", 10, func(row reconcile.RelationRow) error {
				if row.Left.Number != row.Right.Number || row.Left.Position != row.Right.Position {
					return fmt.Errorf("source coordinates changed: %+v", row)
				}
				rows, err := book.ReadRowsWithOptionsContext(context.Background(), idx, row.Left.Position, 1, 1, 2, workbook.RowReadOptions{FormatValue: true})
				if err != nil || len(rows) != 1 {
					return fmt.Errorf("common source read: %v", err)
				}
				for _, cell := range rows[0].Cells {
					if got := row.Left.Values[cell.Col]; got.Raw != cell.Raw || got.Type != cell.Type || got.Display != cell.Display || got.MissingCache != cell.CacheMissing {
						return fmt.Errorf("relation changed source field %s", cell.Ref)
					}
				}
				return nil
			})
			if err != nil || count != int64(source.Info.Total) || printScaleHash(t, file) != before {
				t.Fatal(count, source.Info.Total, err)
			}
		})
	}
}

func TestRelationOOXMLAndAnalysisSnapshotKeepUncertaintyAndRejectChangedSource(t *testing.T) {
	a, file := recordFixture(t, 80, "")
	book := a.currentWorkbook()
	left := relationServerSource(t, a, book, "wb:"+book.Sheets[0].Name, 1, 2, 3, 6, 80)
	if err := a.editAnalysis(book, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "relation", Name: "右侧", Columns: []string{"编号", "内容"}, Rows: [][]string{{"001", "保持文本"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	right := relationServerSource(t, a, book, "scratch:relation", 1, 2)
	query := relationServerQuery()
	query.LeftColumns = []int{1, 2, 3, 6, 80}
	p, err := reconcile.PrepareRelation(context.Background(), filepath.Join(book.CacheDir, "queries"), query, left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	preview, err := p.Preview(context.Background(), "left")
	if err != nil || len(preview.Rows) != 2 || preview.Rows[1].Status != "invalid_key" || preview.Rows[0].Left.Number != 2 || preview.Rows[1].Left.Number != 4 {
		t.Fatal(preview, err)
	}
	row := preview.Rows[0]
	if row.Left.Values[2].Raw != "9007199254740993.31" || row.Left.Values[3].Type != "inlineStr" || row.Left.Values[3].Raw != "1,234.50" || !row.Left.Values[6].MissingCache || row.Right.Values[2].Raw != "保持文本" {
		t.Fatal("OOXML source interpretation changed", row)
	}
	if err := a.editAnalysis(book, func(ws *analysis.Workspace) error { ws.Sheet("relation").Rows[0][1] = "已修改"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Preview(context.Background(), "left"); err == nil {
		t.Fatal("changed saved source accepted")
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := left.Check(context.Background()); err == nil {
		t.Fatal("changed native source accepted")
	}
}

func TestRelationProjectionDoesNotRelaxExistingComparisonFieldLimit(t *testing.T) {
	a, _ := recordFixture(t, 80, "")
	book := a.currentWorkbook()
	columns := make([]int, 64)
	for i := range columns {
		columns[i] = i + 1
	}
	ref := reconcileSourceRef{Book: sourceBookID(book), Source: "wb:" + book.Sheets[0].Name, HeaderRow: 1}
	if _, err := a.buildReconcileSource(context.Background(), book, ref, columns); err == nil {
		t.Fatal("relation expanded the established comparison capacity")
	}
	if _, err := a.buildRelationSource(context.Background(), book, ref, columns); err != nil {
		t.Fatal("relation rejected its supported projection", err)
	}
}
