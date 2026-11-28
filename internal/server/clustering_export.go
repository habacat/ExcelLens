package server

import (
	"encoding/csv"

	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) handleClusterExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID      string `json:"jobId"`
		ProtectCSV bool   `json:"protectCSV"`
	}
	if decodeClusterRequest(w, r, &req) != nil {
		writeMessage(w, 400, "service.clusteringExport.invalidExportParametersCandidateList")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	owner, snapshot, err := a.clusterSnapshot(r.Context(), req.JobID)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	job, err := a.newExportJob(owner.Workbook, "csv")
	if err != nil {
		a.writeExportStartError(w, err)
		return
	}
	unpin := a.pinWorkbooks(owner.Workbook)
	go func() {
		defer close(job.done)
		defer unpin()
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-job.ctx.Done():
			job.fail(job.ctx.Err())
			return
		}
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		check := func() error {
			if owner.Index != nil && owner.Workbook.GetCachedIndex(owner.Meta.Path) != owner.Index {
				return i18n.Error("service.clusteringExport.clusteringSourceIndexChangedRecheck")
			}
			return a.checkClusterSource(job.ctx, owner.Workbook, snapshot)
		}
		if err := check(); err != nil {
			job.fail(err)
			return
		}
		job.mu.Lock()
		job.Status, job.Stage = "running", "stage.task.exportSuspectedDuplicateCandidateList"
		job.mu.Unlock()
		name := safeExportBase(job.wb.DisplayName) + "_疑似重复_" + job.ID[:6] + ".csv"
		var rows int64
		err := publishExportFile(job, name, func(file *os.File) error {
			if _, err := file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
				return err
			}
			writer := csv.NewWriter(file)
			rawTitle := "原值"
			if req.ProtectCSV {
				rawTitle += "（文本保护）"
			}
			if err := writer.Write([]string{"源文件", "来源表", "字段", "范围记录数", "候选组", "候选类型", "比较键", rawTitle, "读取类型", "出现次数", "公式来源次数", "源位置样例（最多8项，非全部位置）"}); err != nil {
				return err
			}
			for _, group := range snapshot.Result.Groups {
				for _, variant := range group.Variants {
					if err := job.ctx.Err(); err != nil {
						return err
					}
					refs := make([]string, len(variant.Rows))
					for i, row := range variant.Rows {
						refs[i] = workbook.ColumnName(snapshot.Column) + strconv.Itoa(row)
					}
					kind := "原文相同"
					if group.Kind == "similar" {
						kind = "疑似重复"
					}
					values := []string{workbookDisplayName(job.wb), snapshot.Source, snapshot.FieldName, strconv.FormatInt(snapshot.Result.Counts.Scanned, 10),
						strconv.Itoa(group.ID + 1), kind, group.Key, variant.Raw, variant.Type, strconv.FormatInt(variant.Count, 10), strconv.FormatInt(variant.FormulaCount, 10), strings.Join(refs, "；")}
					if req.ProtectCSV {
						for i, value := range values {
							values[i] = excelExport.ProtectCSVText(value)
						}
					}
					if err := writer.Write(values); err != nil {
						return err
					}
					rows++
				}
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				return err
			}
			return check()
		})
		if err != nil {
			job.fail(err)
			return
		}
		job.mu.Lock()
		job.Status, job.Stage, job.Progress = "done", "stage.task.candidateListHasBeenExported", 100
		job.DoneRows, job.TotalRows, job.Finished = rows, rows, time.Now()
		job.mu.Unlock()
	}()
	writeJSON(w, map[string]string{"jobId": job.ID})
}
