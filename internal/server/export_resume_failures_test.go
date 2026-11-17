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
	"strings"
	"testing"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
)

func exportResumePaused(t *testing.T, a *App) checkpoint.Manifest {
	t.Helper()
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
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
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	_ = lease.Close()
	_ = store.Close()
	return m
}

func TestExportResumeRejectsChangedSourceCacheOutputAndStaleRevision(t *testing.T) {
	for _, change := range []string{"path", "source", "cache", "missing-cache", "output", "revision"} {
		t.Run(change, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 9000)
			a := checkpointOpen(t, file)
			m := exportResumePaused(t, a)
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(wb.Sheets[0].Path)
			request := taskCheckpointRequest{ID: m.ID, Revision: m.Revision, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}
			if change == "revision" {
				request.Revision--
				checkpointCall(t, a, "/api/tasks/resume", request, http.StatusConflict)
				return
			}
			if change == "output" {
				base, _ := os.UserConfigDir()
				path := filepath.Join(base, "ExcelLens", "task-checkpoints", m.ID, "blocks")
				data, _ := os.ReadFile(path)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				checkpointCall(t, a, "/api/tasks/resume", request, http.StatusUnprocessableEntity)
				return
			}
			path := file
			if change == "cache" || change == "missing-cache" {
				path = idx.XMLPath
			}
			if change == "missing-cache" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data, _ := os.ReadFile(path)
				info, _ := os.Stat(path)
				if change == "path" {
					path = filepath.Join(t.TempDir(), "same-content.csv")
				} else {
					data = []byte(strings.Replace(string(data), "keep", "KEEP", 1))
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if change == "path" {
					if err := a.OpenPath(path); err != nil {
						t.Fatal(err)
					}
					other := a.currentWorkbook()
					request.Book, request.WorkbookKey = sourceBookID(other), other.Fingerprint
					checkpointCall(t, a, "/api/tasks/resume", request, http.StatusConflict)
					return
				}
			}
			job := exportResumeJob(t, a, m)
			waitRowExport(t, job, "failed")
			if job.FilePath != "" || checkpointManifest(t, m.ID).Revision != m.Revision {
				t.Fatal("bad source/cache published output or changed the confirmed cursor")
			}
		})
	}
}

func TestExportResumeEmptyAndFormulaSafetyUseOriginalExportSemantics(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	for _, format := range []string{"csv", "tsv", "jsonl", "html", "xlsx", "zipcsv"} {
		req := exportRequest{Scope: "selection", Sheet: "First", Start: "A6", End: "B6", Format: format, Mode: "values", BOM: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint, Recoverable: true}
		want := exportCheckpointOutput(t, wb, exportCheckpointPrepare(t, a, req), req.options())
		job := startRowExport(t, a, req)
		got := exportResumeDownload(t, a, job)
		compareResumedOutput(t, format, got, want, checkpointManifest(t, job.TaskID).Created)
	}
	req := exportRequest{Scope: "selection", Sheet: "First", Start: "A1", End: "B2", Format: "xlsx", Mode: "formula", Recoverable: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
	checkpointCall(t, a, "/api/export/start", req, http.StatusBadRequest)
}

func TestExportResumeJournalRejectsForgedTransitionWithoutConfirmingBody(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: "First", Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
	_, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	journal, err := newExportCheckpointJournal(context.Background(), wb, lease, selections)
	if err != nil {
		t.Fatal(err)
	}
	before := lease.Snapshot()
	err = journal.Commit(context.Background(), excelExport.ResumePart{Before: journal.State(), Count: selections[0].RowCount()}, func(out io.Writer) (excelExport.ResumeState, error) {
		_, err := out.Write([]byte("invented output"))
		after := journal.State()
		after.Row = 1
		return after, err
	})
	if err == nil || lease.Snapshot().Revision != before.Revision {
		t.Fatal("forged cursor was acknowledged", err)
	}
	data, _ := json.Marshal(journal.State())
	if bytes.Contains(data, []byte(`"row":"1"`)) {
		t.Fatal("failed transition changed the in-memory confirmed state")
	}
}
