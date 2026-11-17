package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"excellens/internal/workbook"
)

func TestFilteredViewportLocateAndSelectionExcludeFrozenRowsAnywhere(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "frozen.xlsx")
	writeSelectionWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["frozen-view"] = &FilterResult{ID: "frozen-view", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 2, 0, 3, 1)}
	response := decodeResponse[struct {
		TotalRows int
		Rows      []workbook.RowData
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&filterId=frozen-view&excludeThroughRow=1&offset=0&limit=10", ""))
	var numbers []int
	for _, row := range response.Rows {
		numbers = append(numbers, row.Number)
	}
	if response.TotalRows != 3 || !reflect.DeepEqual(numbers, []int{3, 4, 2}) {
		t.Fatal("frozen row appeared in the body", response.TotalRows, numbers)
	}
	for _, test := range []struct {
		row, index int
		found      bool
	}{{2, 3, true}, {3, 1, true}, {4, 2, true}, {1, 1, false}} {
		located := decodeResponse[struct {
			Found       bool
			VisualIndex int
		}](t, featureCall(t, a, "GET", fmt.Sprintf("/api/filter/locate?sheet=First&filterId=frozen-view&excludeThroughRow=1&row=%d", test.row), ""))
		if located.Found != test.found || located.VisualIndex != test.index {
			t.Fatal(test, located)
		}
	}
	bySource := decodeResponse[struct {
		Offset int
		Rows   []workbook.RowData
	}](t, featureCall(t, a, "GET", "/api/sheet?sheet=First&filterId=frozen-view&excludeThroughRow=1&row=2&limit=1", ""))
	if bySource.Offset != 2 || len(bySource.Rows) != 1 || bySource.Rows[0].Number != 2 {
		t.Fatal(bySource)
	}
}

func TestClearingFilterDoesNotReleaseAnActiveMappedSnapshot(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	const count = 262145
	positions := make([]int, count)
	for i := range positions {
		positions[i] = count - i - 1
	}
	rows, err := workbook.NewRowOrder(context.Background(), dir, count, positions)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := New()
	defer a.Close()
	a.filterResults["large"] = &FilterResult{ID: "large", Rows: rows, Sorted: true}
	snapshot := a.filterSnapshot("large")
	if snapshot == nil {
		t.Fatal("snapshot missing")
	}
	defer snapshot.close()
	featureCall(t, a, "POST", "/api/filter/clear", `{"id":"large"}`)
	if rowsPage, err := snapshot.Rows.Page(0, 3); err != nil || !reflect.DeepEqual(rowsPage, []int{count - 1, count - 2, count - 3}) {
		t.Fatal(rowsPage, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("active snapshot storage was removed", len(entries))
	}
	snapshot.close()
	entries, _ = os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("last snapshot did not reclaim storage", len(entries))
	}
}

func TestCacheCleanupProtectsQueryMappingWithAResidentWorkbookIndex(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "resident.xlsx")
	writeSelectionWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	wb := a.currentWorkbook()
	if wb.HasMappedIndexes() {
		t.Fatal("fixture must exercise the resident-index branch")
	}
	const count = 262145
	positions := make([]int, count)
	for i := range positions {
		positions[i] = count - 1 - i
	}
	// A private active-query file is enough to exercise maintenance ownership;
	// it need not be a published filter of this four-row source workbook.
	rows, err := workbook.NewRowOrder(context.Background(), filepath.Join(wb.CacheDir, "queries"), count, positions)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !workbook.QueryFilesInUse(wb.CacheDir) {
		t.Fatal("mapped result not registered")
	}
	response := analysisRequest(t, a, "POST", "/api/cache/clear", "")
	if response.Code != 409 {
		t.Fatal("active query cache was cleared", response.Code, response.Body.String())
	}
	if _, err := os.Stat(wb.CacheDir); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if workbook.QueryFilesInUse(wb.CacheDir) {
		t.Fatal("released query still pins cache")
	}
}
