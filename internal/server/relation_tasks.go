package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

type relationStartRequest struct {
	WorkbookKey string                   `json:"workbookKey"`
	Book        string                   `json:"book"`
	Mode        string                   `json:"mode"` // prepare or preview
	Left        *reconcileSourceRef      `json:"left,omitempty"`
	Right       *reconcileSourceRef      `json:"right,omitempty"`
	Query       *reconcile.RelationQuery `json:"query,omitempty"`
	ParentID    string                   `json:"parentId,omitempty"`
	OutputMode  string                   `json:"outputMode"`
	KeepID      string                   `json:"keepId,omitempty"`
}

// App.mu owns state/publication. Each queued worker and each published task
// independently owns its input/source/result leases; teardown is outside mu.
type relationTask struct {
	ID, Mode, OutputMode, ParentID string
	Status, Stage, Error           string
	Primary                        *workbook.Workbook
	Created                        time.Time
	Done, Total                    int64
	Source                         *relationSnapshot
	Result                         *sourceLease[json.RawMessage]
	discarded                      bool
	ctx                            context.Context
	cancel                         context.CancelFunc
	done                           chan struct{}
}

type relationTaskResult struct {
	ID          string                    `json:"id"`
	Mode        string                    `json:"mode"`
	OutputMode  string                    `json:"outputMode"`
	ParentID    string                    `json:"parentId"`
	WorkbookKey string                    `json:"workbookKey"`
	Book        string                    `json:"book"`
	Left        reconcile.SourceInfo      `json:"left"`
	Right       reconcile.SourceInfo      `json:"right"`
	Query       reconcile.RelationQuery   `json:"query"`
	Counts      reconcile.RelationCounts  `json:"counts"`
	Preview     reconcile.RelationPreview `json:"preview"`
}

type relationCleanup struct {
	source *relationSnapshot
	result *sourceLease[json.RawMessage]
}

func (c relationCleanup) close() { c.source.close(); c.result.close() }

func decodeRelationRequest(w http.ResponseWriter, r *http.Request, value any) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.RelationRequestBytes))
	if err != nil {
		return err
	}
	return decodeSavedConfig(data, value)
}

func (a *App) handleRelationLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]int{"keys": formats.RelationKeys, "columns": formats.RelationColumns, "previewRows": formats.RelationPreviewRows, "defaultRows": formats.RelationDefaultRows, "maximumRows": formats.RelationOutputRows, "requestBytes": formats.RelationRequestBytes, "resultBytes": formats.RelationResultBytes, "attachments": formats.SourceAttachmentFiles, "viewRows": formats.RelationViewRows, "viewColumns": formats.RelationViewColumns, "initialFields": formats.RelationInitialFields, "snippetUnits": formats.RelationSnippetUnits, "zipRows": formats.RelationZipRows})
}

func (a *App) handleRelationStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req relationStartRequest
	if decodeRelationRequest(w, r, &req) != nil || req.Mode != "prepare" && req.Mode != "preview" || req.Mode == "prepare" && (req.Left == nil || req.Right == nil || req.Query == nil || req.ParentID != "") || req.Mode == "preview" && (req.ParentID == "" || req.Left != nil || req.Right != nil || req.Query != nil) {
		writeError(w, 400, "关联任务参数无效")
		return
	}
	if req.OutputMode == "" {
		req.OutputMode = "left"
	}
	if _, err := (reconcile.RelationCounts{}).Rows(req.OutputMode); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || req.WorkbookKey != primary.Fingerprint || req.Book != sourceBookID(primary) {
		writeError(w, 409, "工作簿已切换，请重新选择关联来源")
		return
	}
	var inputs *relationInputs
	var source *relationSnapshot
	var err error
	accepted := false
	defer func() {
		if !accepted {
			inputs.close()
			source.close()
		}
	}()
	if req.Mode == "prepare" {
		inputs, err = a.pinRelationInputs(primary, [2]reconcileSourceRef{*req.Left, *req.Right}, *req.Query)
	} else {
		source, err = a.borrowRelationTask(req.ParentID, primary)
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &relationTask{ID: randomID(), Mode: req.Mode, OutputMode: req.OutputMode, ParentID: req.ParentID, Primary: primary, Created: time.Now(), Status: "queued", Stage: "等待关联任务", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if job.ParentID == "" {
		job.ParentID = job.ID
	}
	a.mu.Lock()
	if a.wb != primary || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	active, keep := 0, req.KeepID
	var latest *relationTask
	for _, old := range a.relationJobs {
		select {
		case <-old.done:
		default:
			if old.ctx.Err() == nil {
				active++
			}
		}
		if old.Status == "done" && (latest == nil || old.Created.After(latest.Created)) {
			latest = old
		}
	}
	if (keep == "" || a.relationJobs[keep] == nil || a.relationJobs[keep].Status != "done") && latest != nil {
		keep = latest.ID
	}
	if active >= formats.RelationActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "已有关联任务，请等待完成或取消后重试")
		return
	}
	retired := a.pruneRelationTasksLocked(keep, formats.RelationTaskHistory-1)
	if len(a.relationJobs) >= formats.RelationTaskHistory {
		a.mu.Unlock()
		for _, resource := range retired {
			resource.close()
		}
		cancel()
		writeError(w, 429, "正在回收关联任务，请稍后重试")
		return
	}
	a.relationJobs[job.ID] = job
	a.mu.Unlock()
	for _, resource := range retired {
		resource.close()
	}
	accepted = true
	go a.runRelationTask(job, inputs, source)
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": primary.Fingerprint, "book": sourceBookID(primary)})
}

func (a *App) runRelationTask(job *relationTask, inputs *relationInputs, source *relationSnapshot) {
	var result *sourceLease[json.RawMessage]
	var err error
	defer func() {
		a.mu.Lock()
		if job.ctx.Err() != nil || job.discarded || a.wb != job.Primary || a.relationJobs[job.ID] != job {
			job.Status, job.Stage, job.Error = "cancelled", "已取消", ""
		} else if err != nil {
			job.Status, job.Stage, job.Error = "error", "关联失败", err.Error()
		} else if source == nil || result == nil {
			job.Status, job.Stage, job.Error = "error", "关联失败", "未取得完整关联结果"
		} else {
			job.Source, source = source, nil
			job.Result, result = result, nil
			job.Status, job.Stage = "done", "关联完成"
			job.Done, job.Total = 1, 1
		}
		a.mu.Unlock()
		source.close()
		result.close()
		inputs.close()
		close(job.done)
	}()
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-job.ctx.Done():
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.Lock()
	current := a.wb == job.Primary && a.relationJobs[job.ID] == job && !job.discarded && job.ctx.Err() == nil
	if current {
		job.Status, job.Stage = "running", "准备关联来源"
	}
	a.mu.Unlock()
	if !current {
		return
	}
	if source == nil {
		source, err = a.prepareRelationSnapshot(job.ctx, inputs, func(stage string, done, total int64) {
			a.mu.Lock()
			job.Stage, job.Done, job.Total = stage, done, total
			a.mu.Unlock()
		})
		if err != nil {
			return
		}
	}
	plan := source.data.value.plan
	var preview reconcile.RelationPreview
	preview, err = plan.Preview(job.ctx, job.OutputMode)
	if err != nil {
		return
	}
	info := plan.Sources()
	payload := relationTaskResult{ID: job.ID, Mode: job.Mode, OutputMode: job.OutputMode, ParentID: job.ParentID, WorkbookKey: job.Primary.Fingerprint, Book: sourceBookID(job.Primary), Left: info[0], Right: info[1], Query: plan.Query(), Counts: plan.Counts(), Preview: preview}
	var encoded []byte
	encoded, err = json.Marshal(payload)
	if err != nil {
		return
	}
	if len(encoded) > formats.RelationResultBytes {
		err = errors.New("关联预览超过显示容量，请减少结果字段")
		return
	}
	var release func()
	release, err = a.reserveRelationBytes(int64(len(encoded))*2 + 4096)
	if err != nil {
		return
	}
	result = newSourceLease(json.RawMessage(encoded), release)
	if err = job.Primary.CheckSourceUnchanged(); err != nil {
		return
	}
	err = plan.Check(job.ctx)
}

func retireRelationLocked(job *relationTask) relationCleanup {
	job.cancel()
	job.discarded = true
	resource := relationCleanup{source: job.Source, result: job.Result}
	job.Source, job.Result = nil, nil
	return resource
}

func (a *App) pruneRelationTasksLocked(keep string, limit int) []relationCleanup {
	var retired []relationCleanup
	for len(a.relationJobs) > limit {
		var oldest *relationTask
		for _, job := range a.relationJobs {
			if job.ID == keep {
				continue
			}
			select {
			case <-job.done:
			default:
				continue
			}
			if oldest == nil || job.Created.Before(oldest.Created) {
				oldest = job
			}
		}
		if oldest == nil {
			break
		}
		retired = append(retired, retireRelationLocked(oldest))
		delete(a.relationJobs, oldest.ID)
	}
	return retired
}

func (a *App) clearRelationTasksLocked() ([]*relationTask, []relationCleanup) {
	jobs := make([]*relationTask, 0, len(a.relationJobs))
	resources := make([]relationCleanup, 0, len(a.relationJobs))
	for id, job := range a.relationJobs {
		jobs = append(jobs, job)
		resources = append(resources, retireRelationLocked(job))
		delete(a.relationJobs, id)
	}
	return jobs, resources
}

func waitRelationTasks(jobs []*relationTask, resources []relationCleanup) {
	for _, resource := range resources {
		resource.close()
	}
	for _, job := range jobs {
		<-job.done
	}
}

func (a *App) borrowRelationTask(id string, primary *workbook.Workbook) (*relationSnapshot, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	job := a.relationJobs[id]
	if job == nil || job.Status != "done" || job.Source == nil || job.ctx.Err() != nil || job.discarded || job.Primary != primary || a.wb != primary {
		return nil, errors.New("关联预检尚未完成或已失效，请重新运行")
	}
	return job.Source.retain()
}

func (a *App) handleRelationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.relationJobs[r.URL.Query().Get("id")]
	if job == nil || job.discarded || a.wb != job.Primary {
		a.mu.RUnlock()
		writeError(w, 404, "关联任务已失效")
		return
	}
	out := map[string]any{"jobId": job.ID, "mode": job.Mode, "outputMode": job.OutputMode, "parentId": job.ParentID, "status": job.Status, "stage": job.Stage, "error": job.Error, "done": fmt.Sprint(job.Done), "total": fmt.Sprint(job.Total), "workbookKey": job.Primary.Fingerprint, "book": sourceBookID(job.Primary)}
	a.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) relationAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.RelationRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	job := a.relationJobs[id]
	var retired relationCleanup
	if job != nil {
		job.cancel()
		if clear || job.Status == "done" || job.Status == "error" || job.Status == "cancelled" {
			retired = retireRelationLocked(job)
			delete(a.relationJobs, id)
		}
	}
	a.mu.Unlock()
	retired.close()
	writeJSON(w, map[string]bool{"changed": job != nil})
}

func (a *App) handleRelationCancel(w http.ResponseWriter, r *http.Request) {
	a.relationAction(w, r, false)
}
func (a *App) handleRelationClear(w http.ResponseWriter, r *http.Request) {
	a.relationAction(w, r, true)
}

func (a *App) handleRelationResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	select {
	case a.querySem <- struct{}{}:
	case <-r.Context().Done():
		writeError(w, 408, "关联结果读取已取消")
		return
	}
	a.cacheMu.RLock()
	workDone := false
	defer func() {
		if !workDone {
			a.cacheMu.RUnlock()
			<-a.querySem
		}
	}()
	a.mu.RLock()
	job := a.relationJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.Source == nil || job.Result == nil || job.discarded || job.ctx.Err() != nil || a.wb != job.Primary {
		a.mu.RUnlock()
		writeError(w, 404, "关联结果已失效")
		return
	}
	source, err := job.Source.retain()
	if err != nil {
		a.mu.RUnlock()
		writeError(w, 409, err.Error())
		return
	}
	result, err := job.Result.retain()
	primary := job.Primary
	a.mu.RUnlock()
	defer source.close()
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer result.close()
	if err := primary.CheckSourceUnchanged(); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err := source.data.value.plan.Check(r.Context()); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	// The independently retained JSON owns every response byte. A slow client
	// must not hold a query slot/cache lock while receiving its immutable body.
	source.close()
	a.cacheMu.RUnlock()
	<-a.querySem
	workDone = true
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(result.data.value)
}
