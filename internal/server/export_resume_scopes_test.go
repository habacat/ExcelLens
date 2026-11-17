package server

import (
	"context"
	"errors"
	"testing"

	"excellens/internal/checkpoint"
)

func TestExportResumeQueuedVisibleAndVisualSelectionOwnRowsAfterClearingView(t *testing.T) {
	for _, scope := range []string{"visible", "selection", "filtered"} {
		t.Run(scope, func(t *testing.T) {
			isolateTestStorage(t)
			a, file := rowViewFixture(t)
			before := printScaleHash(t, file)
			wb := a.currentWorkbook()
			meta, _ := wb.FindSheet("First")
			idx := wb.GetCachedIndex(meta.Path)
			a.filterResults["owned"] = &FilterResult{ID: "owned", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
			viewID := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "owned"}), "done")
			req := exportRequest{Scope: scope, Sheet: "First", FilterID: "owned", ViewID: viewID, ExcludeThroughRow: 6, Format: "csv", Mode: "values", Recoverable: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
			if scope == "selection" {
				req.Start, req.End, req.VisualStart, req.VisualEnd, req.ExcludeThroughRow = "B4", "B1", 2, 1, 1
			}
			releaseSlots := holdRowExportSlots(a)
			defer releaseSlots()
			job := startRowExport(t, a, req)
			featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+viewID+`"}`)
			featureCall(t, a, "POST", "/api/filter/clear", `{"id":"owned"}`)
			releaseSlots()
			got := string(exportResumeDownload(t, a, job))
			want := "001,1.10\r\n,9007199254740993.31\r\n,\r\n,7.70\r\n"
			if scope == "selection" {
				want = "1.10\r\n7.70\r\n9007199254740993.31\r\n"
			} else if scope == "filtered" {
				want = ",7.70\r\n,2.20\r\n001,1.10\r\n,5.50\r\n,9007199254740993.31\r\n,3.30\r\n"
			}
			if got != want || printScaleHash(t, file) != before {
				t.Fatal("captured export depended on cleared transient IDs or changed source rows", scope, got)
			}
		})
	}
}

func TestExportResumeUncertainCancellationIsAnExplicitFailure(t *testing.T) {
	job := &ExportJob{Status: "running"}
	job.fail(errors.Join(context.Canceled, checkpoint.ErrUncertain))
	if job.Status != "failed" || job.Error == "" {
		t.Fatal("uncertain persistence was hidden as an ordinary cancellation", job.Status, job.Error)
	}
}
