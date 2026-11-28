package server

import (
	"io"
	"net/http"
	"os"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/reconcile"
)

func (a *App) handleRelationExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID      string `json:"jobId"`
		Mode       string `json:"mode"`
		Maximum    int64  `json:"maximum,string"`
		ProtectCSV *bool  `json:"protectCSV,omitempty"`
		Format     string `json:"format,omitempty"`
	}
	if decodeRelationRequest(w, r, &req) != nil {
		writeMessage(w, 400, "service.relationExport.associatedExportParametersInvalid")
		return
	}
	format, validFormat := formats.RelationExportFormat(req.Format)
	if !validFormat {
		writeMessage(w, 400, "service.relationExport.associatedExportFormatInvalid")
		return
	}
	if req.Maximum == 0 {
		req.Maximum = formats.RelationDefaultRows
	}
	if req.Maximum < 1 || req.Maximum > formats.RelationOutputRows {
		writeMessage(w, 400, "service.relationExport.associatedExtractionLimitExceedsCapacity")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	snapshot, err := a.borrowRelationTask(req.JobID, primary)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			snapshot.Close()
		}
	}()
	plan := snapshot.Value().plan
	total, err := plan.Counts().Rows(req.Mode)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	if total > req.Maximum {
		writeMessage(w, 413, "service.relationExport.thereTotalRelatedResultsExceeding", i18n.Args{"total": total, "maximum": req.Maximum})
		return
	}
	job, err := a.newExportJob(primary, format)
	if err != nil {
		a.writeExportStartError(w, err)
		return
	}
	accepted = true
	go func() {
		defer close(job.done)
		defer snapshot.Close()
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-job.ctx.Done():
			job.fail(job.ctx.Err())
			return
		}
		releaseScan, admissionErr := a.backgroundQuery(job.ctx)
		if admissionErr != nil {
			job.fail(job.ctx.Err())
			return
		}
		defer releaseScan()
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		job.mu.Lock()
		job.Status, job.Stage, job.TotalRows = "running", "stage.task.prepareTheCompleteAssociatedResults", total
		job.mu.Unlock()
		name := safeExportBase(primary.DisplayName) + "_关联结果_" + time.Now().Format("20060102_150405") + "_" + job.ID[:6] + ".csv"
		csvName := name
		if format == "zipcsv" {
			name += ".zip"
		}
		var rows int64
		writeCSV := func(file io.Writer) error {
			if err := primary.CheckSourceUnchanged(); err != nil {
				return err
			}
			if _, err := file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
				return err
			}
			var protect func(string) string
			if req.ProtectCSV == nil || *req.ProtectCSV {
				protect = excelExport.ProtectCSVText
			}
			writer, err := reconcile.NewRelationCSVWriter(file, plan, protect)
			if err != nil {
				return err
			}
			_, err = plan.Stream(job.ctx, req.Mode, req.Maximum, func(row reconcile.RelationRow) error {
				if err := writer.Write(row); err != nil {
					return err
				}
				rows++
				if rows == 1 || rows&4095 == 0 || rows == total {
					job.mu.Lock()
					job.Stage, job.DoneRows = "stage.task.outputTheCompleteAssociatedResults", rows
					if total > 0 {
						job.Progress = float64(rows) / float64(total) * 100
					}
					job.mu.Unlock()
				}
				return nil
			})
			if err != nil {
				return err
			}
			if err := primary.CheckSourceUnchanged(); err != nil {
				return err
			}
			return writer.Flush()
		}
		err := publishExportFile(job, name, func(file *os.File) error {
			if format == "zipcsv" {
				return excelExport.WriteCSVArchive(job.ctx, file, csvName, writeCSV)
			}
			return writeCSV(file)
		})
		if err != nil {
			job.fail(err)
			return
		}
		job.mu.Lock()
		job.Status, job.Stage, job.Progress = "done", "stage.task.completeCorrelationResultsHaveExported", 100
		job.DoneRows, job.TotalRows, job.Finished = rows, rows, time.Now()
		job.mu.Unlock()
	}()
	writeJSON(w, map[string]string{"jobId": job.ID})
}
