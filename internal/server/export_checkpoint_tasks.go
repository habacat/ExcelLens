package server

import (
	"encoding/json"
	"net/http"
	"os"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

// The ordinary run owns its export slot/cache read lock and captured rows.
// Durable storage adds no task slots or source file write permissions.
func (a *App) runNewCheckpointExport(req exportRequest, selections []excelExport.Selection, job *ExportJob) {
	store, err := openTaskCheckpoints()
	if err != nil {
		job.fail(err)
		return
	}
	lease, err := createExportCheckpoint(job.ctx, store, job.wb, req, selections)
	if err != nil {
		_ = store.Close()
		job.fail(err)
		return
	}
	run := &taskCheckpointRun{store: store, lease: lease, release: a.pinWorkbooks(job.wb)}
	defer run.close()
	job.mu.Lock()
	job.TaskID, job.checkpoint, job.Stage = lease.Snapshot().ID, run, "保存导出范围"
	job.mu.Unlock()
	if err := captureExportCheckpoint(job.ctx, job.wb, lease, selections); err != nil {
		job.fail(err)
		return
	}
	a.finishCheckpointExport(job, lease)
}

func (a *App) queueCheckpointExport(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, store *checkpoint.Store, lease *checkpoint.Lease) {
	spec, err := exportCheckpointSpecFor(lease.Snapshot())
	if err != nil || r.Context().Err() != nil {
		_ = lease.Close()
		_ = store.Close()
		if err == nil {
			err = r.Context().Err()
		}
		checkpointHTTPError(w, err)
		return
	}
	a.cacheMu.RLock()
	release := a.pinWorkbooks(wb)
	job, err := a.newExportJob(wb, spec.Options.Format)
	a.cacheMu.RUnlock()
	if err != nil {
		release()
		_ = lease.Close()
		_ = store.Close()
		a.writeExportStartError(w, err)
		return
	}
	run := &taskCheckpointRun{store: store, lease: lease, release: release}
	job.mu.Lock()
	job.TaskID, job.checkpoint = lease.Snapshot().ID, run
	job.mu.Unlock()
	go func() {
		defer close(job.done)
		defer run.close()
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-job.ctx.Done():
			job.fail(job.ctx.Err())
			return
		}
		job.mu.Lock()
		job.Status, job.Stage = "running", "恢复导出"
		job.mu.Unlock()
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		a.finishCheckpointExport(job, lease)
	}()
	writeJSON(w, map[string]any{"jobId": job.ID, "taskId": job.TaskID, "recoverable": true, "operation": "export"})
}

func (a *App) finishCheckpointExport(job *ExportJob, lease *checkpoint.Lease) {
	input, err := readExportCheckpointSelections(job.ctx, job.wb, lease)
	if err != nil {
		job.fail(err)
		return
	}
	defer input.close()
	journal, err := newExportCheckpointJournal(job.ctx, job.wb, lease, input.selections)
	if err != nil {
		job.fail(err)
		return
	}
	progress := func(done, total int64, stage string) {
		job.mu.Lock()
		job.DoneRows, job.TotalRows, job.Stage = done, total, stage
		if total > 0 {
			job.Progress = float64(done) / float64(total) * 100
		}
		job.mu.Unlock()
	}
	var total int64
	for _, s := range input.selections {
		total += s.RowCount()
	}
	progress(excelExport.ResumeDoneRows(journal.State(), input.selections), total, "恢复已确认进度")
	if err := excelExport.Resume(job.ctx, job.wb, input.selections, journal.spec.Options, journal, progress); err != nil {
		job.fail(err)
		return
	}
	progress(total, total, "组装导出文件")
	name := sourceExportName(journal.SourceName(), journal.spec.Scope, journal.spec.Sheets[0].Name, job.ID, journal.spec.Options.Format)
	err = publishExportFile(job, name, func(tmp *os.File) error {
		if err := excelExport.AssembleResume(job.ctx, tmp, job.wb, input.selections, journal.spec.Options, journal); err != nil {
			return err
		}
		return checkExportCheckpointSources(job.ctx, job.wb, journal.manifest.Definition, journal.spec, input.selections)
	})
	if err != nil {
		job.fail(err)
		return
	}
	if !journal.manifest.Complete {
		data, _ := json.Marshal(journal.cursor)
		_, err := advanceTaskCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: journal.manifest.Revision, Stage: "done", Cursor: data, Complete: true}, nil)
		if err != nil {
			job.fail(err)
			return
		}
	}
	job.complete(total)
}

func exportCheckpointResumeDefinition(wb *workbook.Workbook, m checkpoint.Manifest) (checkpoint.Definition, error) {
	spec, err := exportCheckpointSpecFor(m)
	if err != nil {
		return checkpoint.Definition{}, err
	}
	cursor, err := exportCheckpointCursor(m, spec)
	if err != nil {
		return checkpoint.Definition{}, err
	}
	if !cursor.Snapshot {
		return checkpoint.Definition{}, errExportCaptureIncomplete
	}
	return m.Definition, nil
}
