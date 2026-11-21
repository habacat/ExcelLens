package server

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func startRowExport(t *testing.T, a *App, req exportRequest) *ExportJob {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// This helper uses the public route so queued exports exercise admission,
	// provenance checks and the same final publication as user downloads.
	response := rowViewCall(t, a, "POST", "/api/export/start", string(data))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var started struct{ JobID string }
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.exports[started.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("missing export task")
	}
	return job
}

func waitRowExport(t testing.TB, job *ExportJob, status string) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(3 * time.Minute):
		t.Fatal("export timeout")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != status {
		t.Fatalf("export status %s, want %s: %s", job.Status, status, job.Error)
	}
}

func holdRowExportSlots(a *App) func() {
	for i := 0; i < cap(a.exportSem); i++ {
		a.exportSem <- struct{}{}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := 0; i < cap(a.exportSem); i++ {
				<-a.exportSem
			}
		})
	}
}

func TestVisibleAndFullFilteredExportsRetainDifferentImmutableSetsAcrossClear(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	before, _ := os.ReadFile(file)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["export-order"] = &FilterResult{ID: "export-order", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "export-order"}), "done")
	release := holdRowExportSlots(a)
	defer release()
	visible := startRowExport(t, a, exportRequest{Scope: "visible", Sheet: "First", FilterID: "export-order", ViewID: id, ExcludeThroughRow: 1, Format: "csv", Mode: "values"})
	full := startRowExport(t, a, exportRequest{Scope: "filtered", Sheet: "First", FilterID: "export-order", ViewID: id, Format: "csv", Mode: "values"})
	featureCall(t, a, "POST", "/api/filter/clear", `{"id":"export-order"}`)
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	release()
	waitRowExport(t, visible, "done")
	waitRowExport(t, full, "done")
	visibleCSV, err := os.ReadFile(visible.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(visibleCSV) != "001,1.10\r\n,7.70\r\n,9007199254740993.31\r\n" {
		t.Fatal("visible export leaked hidden rows or lost frozen/sorted order", string(visibleCSV))
	}
	fullCSV, err := os.ReadFile(full.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(fullCSV) != ",7.70\r\n,2.20\r\n001,1.10\r\n,5.50\r\n,9007199254740993.31\r\n,3.30\r\n" {
		t.Fatal("complete match set was changed by visibility", string(fullCSV))
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("export's final release leaked source/view pins", pins)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("export modified source file")
	}
}

func TestVisibleSelectionExportReopensSparseRowsAndCancelledQueueReleasesSnapshot(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["stream"] = &FilterResult{ID: "stream", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "stream"}), "done")
	job := startRowExport(t, a, exportRequest{Scope: "visible", Sheet: "First", FilterID: "stream", ViewID: id, ExcludeThroughRow: 6, Format: "xlsx", Mode: "values", Styles: true})
	waitRowExport(t, job, "done")
	out, err := workbook.OpenWorkbook(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := out.BuildIndex(out.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := out.ReadRows(index, 0, index.RowCount(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	numbers := []int{}
	for _, row := range rows {
		numbers = append(numbers, row.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2, 4}) || rows[0].Cells[0].Type != "inlineStr" || rows[0].Cells[0].Raw != "001" || rows[1].Cells[0].Raw != "9007199254740993.31" || rows[2].Cells[0].Raw != "7.70" {
		t.Fatal("XLSX stream lost types/blank source row mapping", rows)
	}
	selection := startRowExport(t, a, exportRequest{Scope: "selection", Sheet: "First", FilterID: "stream", ViewID: id, Start: "B4", End: "B1", VisualStart: 2, VisualEnd: 1, ExcludeThroughRow: 1, Format: "csv", Mode: "values"})
	waitRowExport(t, selection, "done")
	data, err := os.ReadFile(selection.FilePath)
	if err != nil || string(data) != "1.10\r\n7.70\r\n9007199254740993.31\r\n" {
		t.Fatal("reversed cross-pane export differs from copy", string(data), err)
	}
	release := holdRowExportSlots(a)
	defer release()
	cancelled := startRowExport(t, a, exportRequest{Scope: "visible", Sheet: "First", FilterID: "stream", ViewID: id, Format: "csv", Mode: "values"})
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	featureCall(t, a, "POST", "/api/filter/clear", `{"id":"stream"}`)
	featureCall(t, a, "POST", "/api/export/cancel", `{"id":"`+cancelled.ID+`"}`)
	waitRowExport(t, cancelled, "cancelled")
	if cancelled.FilePath != "" {
		t.Fatal("cancelled queued export published an output", cancelled.FilePath)
	}
	status := decodeResponse[struct{ DownloadURL string }](t, featureCall(t, a, "GET", "/api/export/status?id="+cancelled.ID, ""))
	if status.DownloadURL != "" {
		t.Fatal("cancelled export exposed partial download")
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 {
		t.Fatal("cancelled queued export retained source snapshot", pins)
	}
}

func TestVisibleRowStreamXLSXFreezesConditionsAtOriginalCoordinates(t *testing.T) {
	a, _, _ := conditionalServerFixture(t)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: "Data", Change: &rowViewChange{Action: "range", First: 2, Last: 2, Hidden: true}}), "done")
	job := startRowExport(t, a, exportRequest{Scope: "selection", Sheet: "Data", ViewID: id, Start: "A1", End: "C3", Format: "xlsx", Mode: "values", Styles: true})
	waitRowExport(t, job, "done")
	w, err := workbook.OpenWorkbook(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := w.ReadRows(idx, 0, idx.RowCount(), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Number != 1 || rows[1].Number != 2 {
		t.Fatal("hidden source row entered compact output", rows)
	}
	cells := map[string]workbook.CellData{}
	for _, row := range rows {
		for _, cell := range row.Cells {
			cells[cell.Ref] = cell
		}
	}
	first, second := cells["A1"], cells["A2"]
	if first.Raw != "9007199254740993.31" || second.Raw != "9007199254740993.30" || cells["B1"].Raw != "001" || cells["B1"].Type != "inlineStr" {
		t.Fatal("source-reference stream changed original values/types", cells)
	}
	if w.VisualStyles[first.Style].Color != "#FF0033" || w.VisualStyles[first.Style].Bold || !w.VisualStyles[second.Style].Bold {
		t.Fatal("conditional priority/source coordinates changed", first, second, w.VisualStyles)
	}
	for _, ref := range []string{"C1", "C2"} {
		cell, ok := cells[ref]
		if !ok || w.VisualStyles[cell.Style].Background != "#FFFF00" {
			t.Fatal("conditional blank/source reference not frozen", ref, cell)
		}
	}
}

func TestQueuedVisibleCSVUsesCapturedValuesAfterLiveSourceChange(t *testing.T) {
	a, file := namedViewFixture(t)
	wb := a.currentWorkbook()
	sheet := wb.Sheets[0].Name
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: sheet, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: true}}), "done")
	release := holdRowExportSlots(a)
	defer release()
	job := startRowExport(t, a, exportRequest{Scope: "visible", Sheet: sheet, ViewID: id, Format: "csv", Mode: "values"})
	if err := os.WriteFile(file, []byte("changed,source\nnever,exported\n"), 0600); err != nil {
		t.Fatal(err)
	}
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	release()
	waitRowExport(t, job, "done")
	data, err := os.ReadFile(job.FilePath)
	if err != nil || string(data) != "项目,金额,编号\r\nitem10,9007199254740993.31,001\r\nitem1,9007199254740993.30,003\r\n" {
		t.Fatal("queued snapshot read later live-source contents", string(data), err)
	}
}

func TestEmptyVisibleExportAndInvalidViewDoNotFallbackToWholeSheet(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Change: &rowViewChange{Action: "hide-all"}}), "done")
	job := startRowExport(t, a, exportRequest{Scope: "visible", Sheet: "First", ViewID: id, Format: "csv", Mode: "values"})
	waitRowExport(t, job, "done")
	data, err := os.ReadFile(job.FilePath)
	if err != nil || len(data) != 0 || job.TotalRows != 0 {
		t.Fatal("empty visible view exported full source rows", string(data), job.TotalRows, err)
	}
	request := exportRequest{Scope: "visible", Sheet: "First", ViewID: "missing", Format: "csv", Mode: "values"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if response := rowViewCall(t, a, "POST", "/api/export/start", string(encoded)); response.Code == 200 {
		t.Fatal("missing visibility fell back to whole source")
	}
}
