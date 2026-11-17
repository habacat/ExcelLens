package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

type pausingExportJournal struct {
	excelExport.ResumeJournal
	after uint64
	pause func()
}

func (j pausingExportJournal) Commit(ctx context.Context, part excelExport.ResumePart, produce func(io.Writer) (excelExport.ResumeState, error)) error {
	if err := j.ResumeJournal.Commit(ctx, part, produce); err != nil {
		return err
	}
	if j.State().Commits == j.after {
		j.pause()
	}
	return nil
}

func exportResumeJob(t *testing.T, a *App, m checkpoint.Manifest) *ExportJob {
	t.Helper()
	wb := a.currentWorkbook()
	var response struct{ JobID, TaskID, Operation string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, 200).Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.exportJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil || response.TaskID != m.ID || response.Operation != "export" {
		t.Fatal("durable export was queued as the wrong operation", response)
	}
	return job
}

func exportResumeDownload(t *testing.T, a *App, job *ExportJob) []byte {
	t.Helper()
	waitRowExport(t, job, "done")
	var status struct {
		TaskID, DownloadURL string
		Size                int64
	}
	response := rowViewCall(t, a, "GET", "/api/export/status?id="+job.ID, "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.DownloadURL == "" || status.TaskID != job.TaskID {
		t.Fatal("completed export has no bound download", response.Code, response.Body.String())
	}
	download := rowViewCall(t, a, "GET", status.DownloadURL, "")
	if download.Code != 200 || int64(download.Body.Len()) != status.Size {
		t.Fatal("durable output download is missing/truncated", download.Code, status.Size, download.Body.Len())
	}
	data, err := os.ReadFile(job.FilePath)
	if err != nil || !bytes.Equal(data, download.Body.Bytes()) {
		t.Fatal("download differs from atomic disk output", err)
	}
	return download.Body.Bytes()
}

func compareResumedOutput(t *testing.T, format string, got, want []byte, created time.Time) {
	t.Helper()
	if format != "xlsx" && format != "zipcsv" {
		if !bytes.Equal(got, want) {
			t.Fatalf("resumed %s changed exact bytes or output row coordinates: got %d want %d", format, len(got), len(want))
		}
		return
	}
	g, w := exportCheckpointParts(t, got), exportCheckpointParts(t, want)
	if format == "zipcsv" {
		var gm, wm map[string]any
		if json.Unmarshal([]byte(g["_ExcelLens_manifest.json"]), &gm) != nil || json.Unmarshal([]byte(w["_ExcelLens_manifest.json"]), &wm) != nil || gm["createdAt"] != created.Format(time.RFC3339) {
			t.Fatal("archive lost its original task creation time")
		}
		wm["createdAt"] = gm["createdAt"]
		if !reflect.DeepEqual(gm, wm) {
			t.Fatal("archive manifest lost its scope", gm, wm)
		}
		delete(g, "_ExcelLens_manifest.json")
		delete(w, "_ExcelLens_manifest.json")
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatal("resumed archive changed complete parts, styles, types or coordinates")
	}
}

func TestExportResumeSixFormatsReopenConfirmedOutputAndDownloadExactly(t *testing.T) {
	for _, format := range []string{"csv", "tsv", "jsonl", "html", "xlsx", "zipcsv"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 12000)
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			wb := a.currentWorkbook()
			meta := wb.Sheets[0]
			idx, err := wb.BuildIndex(meta, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			order, err := workbook.BuildRowOrder(context.Background(), filepath.Join(wb.CacheDir, "queries"), idx.RowCount(), func(ctx context.Context, add func(int) error) error {
				for i := idx.RowCount() - 1; i >= 0; i-- {
					if err := add(i); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			a.filterResults["resumable-order"] = &FilterResult{ID: "resumable-order", SheetPath: meta.Path, Rows: order, Sorted: true}
			req := exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: "resumable-order", Format: format, Mode: "values", BOM: true, ProtectCSV: true, Styles: true}
			selections, release := exportCheckpointPrepareOwned(t, a, req)
			want := exportCheckpointOutput(t, wb, selections, req.options())
			store, lease := exportCheckpointCreate(t, a, req, selections)
			if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
				t.Fatal(err)
			}
			journal, err := newExportCheckpointJournal(context.Background(), wb, lease, selections)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			err = excelExport.Resume(ctx, wb, selections, req.options(), pausingExportJournal{ResumeJournal: journal, after: 1, pause: cancel}, nil)
			cancel()
			if !errors.Is(err, context.Canceled) || journal.State().Commits != 1 {
				t.Fatal("test did not interrupt after a complete output unit", journal.State(), err)
			}
			m := lease.Snapshot()
			if m.Complete || m.Records != 12002 {
				t.Fatal("complete output part incorrectly became a delivered export", m)
			}
			_ = lease.Close()
			_ = store.Close()
			release()
			a.Close()
			b := checkpointOpen(t, file)
			job := exportResumeJob(t, b, m)
			got := exportResumeDownload(t, b, job)
			compareResumedOutput(t, format, got, want, m.Created)
			final := checkpointManifest(t, m.ID)
			if !final.Complete || final.Stage != "done" || final.Revision <= m.Revision || printScaleHash(t, file) != before {
				// A one-sheet archive still confirms final publication separately.
				t.Fatal("resume did not finish a durable publication", final)
			}
			// Download recreation of a completed task does not append new output.
			again := exportResumeJob(t, b, final)
			if second := exportResumeDownload(t, b, again); !bytes.Equal(second, got) || checkpointManifest(t, m.ID).Revision != final.Revision {
				t.Fatal("completed recovery queried or rewrote confirmed output")
			}
		})
	}
}

func TestExportResumeStartUsesOriginalExportAdmissionAndReturnsDurableStatus(t *testing.T) {
	isolateTestStorage(t)
	a, file := rowViewFixture(t)
	before := printScaleHash(t, file)
	wb := a.currentWorkbook()
	req := exportRequest{Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint, Scope: "sheet", Sheet: "First", Format: "jsonl", Mode: "formula", Recoverable: true}
	job := startRowExport(t, a, req)
	data := exportResumeDownload(t, a, job)
	if job.TaskID == "" || !checkpointManifest(t, job.TaskID).Complete || !bytes.Contains(data, []byte("9007199254740993.31")) || printScaleHash(t, file) != before {
		t.Fatal("opt-in export did not preserve durable identity and exact values")
	}
	bad := req
	bad.Book = ""
	checkpointCall(t, a, "/api/export/start", bad, 409)
	release := holdRowExportSlots(a)
	defer release()
	first := startRowExport(t, a, req)
	second := startRowExport(t, a, req)
	checkpointCall(t, a, "/api/export/start", req, http.StatusTooManyRequests)
	first.cancel()
	second.cancel()
	waitRowExport(t, first, "cancelled")
	waitRowExport(t, second, "cancelled")
}
