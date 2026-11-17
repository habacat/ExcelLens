package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/analysis"
	"excellens/internal/recipe"
	"excellens/internal/workbook"
)

func TestAnalysisComparisonsBindVisibleScopeTreeAndExactExports(t *testing.T) {
	isolateTestStorage(t)
	a, file, q := treeFixture(t)
	before, _ := os.ReadFile(file)
	wb := a.currentWorkbook()
	view := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: "Tree"}), "done")
	captured := captureAnalysisRows(t, a, analysisSourceCaptureRequest{Source: q.Source, Mode: "visible", ViewID: view})
	q.SourceScope = &captured.SourceScope
	q.Metrics = []analysis.Metric{{Field: "C4", Aggregate: "sum", Comparison: "percent_grand"}, {Field: "C4", Aggregate: "average", Comparison: "running_total"}, {Aggregate: "count", Comparison: "running_percent", ComparisonAxis: "columns"}}
	root := completedRecipeAnalysis(t, a, q)
	if root.Result.Grand.Exact != "11.3" || root.Result.Grand.Comparison.Exact != "1" || root.Result.Rows[0].Total.Comparison.Exact != "3/113" || root.Result.MissingFormulaCaches != 1 || root.Result.InvalidNumbers != 1 {
		t.Fatal("comparison escaped the captured visible source or changed native types", root.Result)
	}
	// A child percentage divides by the entire parent path, including children
	// outside Top-N; it is never computed against a visible root subtotal.
	branch := treeResult(t, a, treeStart(t, a, root.ID, []string{"北"}), "done")
	if branch.Grand.Exact != "0.3" || branch.Rows[0].Total.Comparison.Exact != "1/3" || branch.Rows[1].Total.Comparison.Exact != "2/3" || branch.Rows[0].MetricTotals[1].Comparison.Exact != "0.1" || branch.Rows[1].MetricTotals[1].Comparison.Exact != "0.15" {
		t.Fatal(branch)
	}
	// Actual exports go through the same immutable completed job, not a newly
	// submitted query or a rounded number visible in the browser.
	grid := analysis.BuildPivotTable(q, root.Result, root.SourceFields)
	for _, format := range []string{"csv", "xlsx"} {
		response := analysisRequest(t, a, "GET", "/api/analysis/export?id="+root.ID+"&format="+format, "")
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if format == "csv" {
			reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(response.Body.String(), "\ufeff")))
			rows, err := reader.ReadAll()
			if err != nil || !reflect.DeepEqual(rows[0], grid.Headers) || !reflect.DeepEqual(rows[1:], grid.Rows) {
				t.Fatal("csv changed exact comparison values or scope", rows, err)
			}
		}
		exported := filepath.Join(t.TempDir(), "comparison."+format)
		if err := os.WriteFile(exported, response.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		reopened := openTreeTestApp(t, exported)
		book := reopened.currentWorkbook()
		meta := book.Sheets[0]
		index := book.GetCachedIndex(meta.Path)
		rows, err := book.ReadRowsWithOptionsContext(t.Context(), index, 0, len(grid.Rows)+1, 1, len(grid.Headers), workbook.RowReadOptions{FormatValue: true})
		if err != nil || len(rows) != len(grid.Rows)+1 {
			t.Fatal(rows, err)
		}
		for r, row := range rows {
			want := grid.Headers
			if r > 0 {
				want = grid.Rows[r-1]
			}
			for _, cell := range row.Cells {
				if cell.Raw != want[cell.Col-1] {
					t.Fatalf("%s %s raw=%q want=%q", format, cell.Ref, cell.Raw, want[cell.Col-1])
				}
			}
		}
	}
	materialized := decodeResponse[struct{ ID string }](t, reconcilePost(t, a, "/api/analysis/materialize", map[string]any{"jobId": root.ID, "name": "首层精确占比"}))
	workspace, err := a.loadAnalysis(wb)
	if err != nil || workspace.Sheet(materialized.ID) == nil || !reflect.DeepEqual(workspace.Sheet(materialized.ID).Rows, grid.Rows) {
		t.Fatal("result materialization lost exact ratios or states", workspace, err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("comparison or export modified original workbook")
	}
}

func TestAnalysisComparisonReportsAndBackupsRequireWorkspaceFour(t *testing.T) {
	a, _ := setupProApp(t)
	q := analysis.Query{Source: "wb:Invest", Rows: []string{"C1"}, Metrics: []analysis.Metric{{Field: "C3", Aggregate: "sum", Comparison: "running_percent"}}}
	reconcilePost(t, a, "/api/analysis/report", map[string]any{"name": "累计占比", "query": q})
	backup := decodeResponse[analysisBackup](t, analysisRequest(t, a, "GET", "/api/analysis/backup", ""))
	if backup.Workspace.Version != 4 || backup.Workspace.Reports[0].Query.Metrics[0].Comparison != "running_percent" {
		t.Fatal(backup)
	}
	for _, version := range []int{1, 2, 3} {
		bad := backup
		bad.Workspace.Version = version
		data, _ := json.Marshal(bad)
		response := analysisRequest(t, a, "POST", "/api/analysis/backup", string(data))
		if response.Code != 400 {
			t.Fatal("downgraded comparison backup accepted", version, response.Code)
		}
	}
	data, _ := json.Marshal(backup)
	if response := analysisRequest(t, a, "POST", "/api/analysis/backup", string(data)); response.Code != 200 {
		t.Fatal("valid version-four backup rejected", response.Code, response.Body.String())
	}
}

func TestAnalysisComparisonRecipeCapturesPreviewsAndReplaysVersionThree(t *testing.T) {
	a, _ := namedViewFixture(t)
	q := analysis.Query{Source: "wb:" + a.currentWorkbook().Sheets[0].Name, HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 4, Metrics: []analysis.Metric{{Field: "C2", Aggregate: "sum", Comparison: "percent_grand"}, {Aggregate: "count", Comparison: "running_total"}}}
	value := saveTestRecipe(t, a, q)
	if value.Version != recipe.ComparisonVersion {
		t.Fatal("capture silently dropped comparison capability", value)
	}
	preview := previewTestRecipe(t, a, value.ID, q.Source)
	job := runTestRecipe(t, a, recipeRunRequest(a, preview, q.Source, map[string]string{"C1": "C1", "C2": "C2"}))
	if job.Status != "done" || job.Query.Metrics[0].Comparison != "percent_grand" || job.Result.Grand.Comparison == nil || job.Result.Grand.Comparison.Exact != "1" {
		t.Fatal("replay changed comparison settings or exact result", job.Status, job.Error, job.Result)
	}
}

func TestAnalysisComparisonTinyFiniteRatioReopensAsExactTextInsteadOfNumericZero(t *testing.T) {
	isolateTestStorage(t)
	fields := []analysis.Field{{ID: "C1", Name: "机构"}, {ID: "C2", Name: "金额"}}
	q := analysis.Query{Source: "wb:extreme", Rows: []string{"C1"}, Sort: "label_asc", Precision: 10, Metrics: []analysis.Metric{{Field: "C2", Aggregate: "sum", Comparison: "percent_grand"}}}
	scan := func(ctx context.Context, needed []string, visit func(analysis.Record) error) error {
		for i, amount := range []string{"1e-308", "9." + strings.Repeat("9", 308)} {
			if err := visit(analysis.Record{Number: i + 2, Values: map[string]analysis.Value{"C1": {Text: []string{"A", "B"}[i]}, "C2": {Text: amount, Numeric: true}}}); err != nil {
				return err
			}
		}
		return nil
	}
	result, err := analysis.Run(t.Context(), q, fields, scan, nil)
	if err != nil || result.Grand.Exact != "10" {
		t.Fatal(result.Grand, err)
	}
	exact := "0." + strings.Repeat("0", 308) + "1"
	if result.Rows[0].Total.Comparison.Exact != exact {
		t.Fatal("fixture did not exercise a finite Excel-underflowing ratio", result.Rows[0].Total.Comparison)
	}
	var data bytes.Buffer
	if err := analysis.ExportPivotXLSX(&data, analysis.BuildPivotTable(q, result, fields)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "extreme-ratio.xlsx")
	if err := os.WriteFile(file, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a := openTreeTestApp(t, file)
	wb := a.currentWorkbook()
	idx := wb.GetCachedIndex(wb.Sheets[0].Path)
	rows, err := wb.ReadRowsWithOptionsContext(t.Context(), idx, 0, 4, 1, 2, workbook.RowReadOptions{FormatValue: true})
	if err != nil || len(rows) != 4 || len(rows[1].Cells) != 2 || rows[1].Cells[1].Raw != exact || rows[1].Cells[1].Type != "inlineStr" {
		t.Fatal("tiny comparison was stored as a lossy numeric value", rows, err)
	}
}
