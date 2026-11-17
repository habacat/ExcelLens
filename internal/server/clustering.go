package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/clustering"
	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

type clusterTask = sourceMetadataTask[clusterSnapshot]

type clusterSnapshot struct {
	Source, Field, FieldName, Stamp string
	HeaderRow, Column               int
	Scope                           *sourceview.Scope
	Result                          *clustering.Result
	bytes                           int64
}

func (s *clusterSnapshot) BudgetBytes() int64 { return s.bytes }

type clusterStartRequest struct {
	WorkbookKey string             `json:"workbookKey"`
	Book        string             `json:"book"`
	Source      string             `json:"source"`
	SourceID    string             `json:"sourceId,omitempty"`
	HeaderRow   int                `json:"headerRow"`
	Field       string             `json:"field"`
	Options     clustering.Options `json:"options"`
}

func decodeClusterRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.ClusterRequestBytes))
	if err != nil {
		return err
	}
	return decodeSavedConfig(data, dst)
}

func (a *App) handleClusterStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req clusterStartRequest
	if decodeClusterRequest(w, r, &req) != nil || req.HeaderRow < 1 || req.HeaderRow > formats.ExternalRows || req.Options.Validate() != nil {
		writeError(w, 400, "疑似重复检查参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "聚类来源已切换，请重新读取")
		return
	}
	capability, _ := formats.Lookup(wb.Format)
	if !strings.HasPrefix(req.Source, "scratch:") && !capability.TextClustering {
		writeError(w, 422, "此来源未保留可靠的文本类型，暂不支持聚类")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	var observed clustering.Record
	src, err := a.sourceRequestObserved(r.Context(), wb, req.Source, req.HeaderRow, req.SourceID, func(row int, cell workbook.CellData, numeric bool) error {
		observed = clustering.Record{Row: row, Cell: cell, Numeric: numeric}
		return nil
	})
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			src.close()
		}
	}()
	var field analysis.Field
	for _, f := range src.fields {
		if f.ID == req.Field {
			field = f
			break
		}
	}
	if field.ID == "" {
		writeError(w, 400, "聚类字段必须是当前范围内的原始字段")
		return
	}
	meta := workbook.SheetMeta{}
	var idx *workbook.SheetIndex
	if strings.HasPrefix(req.Source, "wb:") {
		meta, _ = wb.FindSheet(strings.TrimPrefix(req.Source, "wb:"))
		idx = wb.GetCachedIndex(meta.Path)
	}
	snapshot := &clusterSnapshot{Source: req.Source, HeaderRow: req.HeaderRow, Field: field.ID, FieldName: field.Name, Column: field.Column, Stamp: src.stamp()}
	if src.lease != nil {
		snapshot.Scope = src.lease.canonicalScope()
	}
	scopeJSON, err := json.Marshal(snapshot.Scope)
	if err != nil {
		writeError(w, 400, "聚类来源范围无法保存")
		return
	}
	snapshot.bytes = int64(len(scopeJSON)*2+len(snapshot.Source)+len(snapshot.FieldName)+len(snapshot.Stamp)) + 1024
	ctx, cancel := context.WithCancel(context.Background())
	job := &clusterTask{ID: randomID(), Mode: req.Source, Status: "queued", Stage: "等待检查疑似重复", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	if activeMetadataTasks(a.clusterJobs) >= formats.ClusterActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "疑似重复检查正在运行，请等待或取消后重试")
		return
	}
	a.pruneClustersLocked("", formats.ClusterTaskHistory-1)
	releasePin := a.pinWorkbooks(wb)
	a.clusterJobs[job.ID] = job
	a.mu.Unlock()
	accepted = true
	go runSourceMetadataTask(a, job, func() { src.close(); releasePin() }, "疑似重复", func() bool {
		return a.wb == wb && a.clusterJobs[job.ID] == job && (idx == nil || wb.GetCachedIndex(meta.Path) == idx)
	}, func() { a.pruneClustersLocked(job.ID, formats.ClusterTaskHistory) }, func(progress func(int64, int64, string)) (*clusterSnapshot, error) {
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		scan := func(ctx context.Context, visit func(clustering.Record) error) error {
			return src.scan(ctx, []string{req.Field}, func(rec analysis.Record) error {
				record := clustering.Record{Row: rec.Number}
				if observed.Row == rec.Number {
					record = observed
				}
				return visit(record)
			})
		}
		result, err := clustering.Build(job.ctx, filepath.Join(wb.CacheDir, "queries"), src.total, scan, req.Options, func(stage string, done, total int64) { progress(done, total, stage) })
		if err != nil {
			return nil, err
		}
		if err := a.checkClusterSource(job.ctx, wb, snapshot); err != nil {
			return nil, err
		}
		snapshot.Result = result
		snapshot.bytes += result.BudgetBytes()
		if snapshot.bytes > formats.ClusterRetainedBytes {
			return nil, errors.New("聚类结果和来源范围超过快照容量，未发布部分结果")
		}
		return snapshot, nil
	})
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func (a *App) checkClusterSource(ctx context.Context, wb *workbook.Workbook, snapshot *clusterSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.HasPrefix(snapshot.Source, "scratch:") {
		latest, err := a.source(wb, snapshot.Source, snapshot.HeaderRow)
		if err != nil {
			return err
		}
		defer latest.close()
		if latest.stamp() != snapshot.Stamp {
			return errors.New("编辑表内容已变化，请重新检查疑似重复")
		}
	}
	return wb.CheckSourceUnchanged()
}

func (a *App) pruneClustersLocked(keep string, history int) {
	pruneMetadataTasks(a.clusterJobs, keep, metadataTaskLimits{history, formats.ClusterSnapshots, formats.ClusterRetainedBytes}, (*clusterSnapshot).BudgetBytes)
}

func (a *App) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.clusterJobs[r.URL.Query().Get("id")]
	if job == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		writeError(w, 404, "疑似重复任务已失效")
		return
	}
	payload := metadataTaskPayload(job)
	if job.Snapshot != nil {
		payload["counts"] = job.Snapshot.Result.Counts
		payload["groups"] = len(job.Snapshot.Result.Groups)
	}
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) clusterAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.ClusterRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := metadataTaskAction(a.clusterJobs, id, clear)
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"changed": changed})
}
func (a *App) handleClusterCancel(w http.ResponseWriter, r *http.Request) {
	a.clusterAction(w, r, false)
}
func (a *App) handleClusterClear(w http.ResponseWriter, r *http.Request) { a.clusterAction(w, r, true) }

// Caller holds cacheMu.RLock while using the immutable returned snapshot.
func (a *App) clusterSnapshot(ctx context.Context, id string) (*clusterTask, *clusterSnapshot, error) {
	a.mu.RLock()
	job := a.clusterJobs[id]
	if job == nil || job.Status != "done" || job.Snapshot == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		return nil, nil, errors.New("疑似重复结果已失效，请重新检查")
	}
	snapshot, wb := job.Snapshot, job.Workbook
	a.mu.RUnlock()
	if job.Index != nil && wb.GetCachedIndex(job.Meta.Path) != job.Index {
		return nil, nil, errors.New("聚类来源索引已变化，请重新检查")
	}
	if err := a.checkClusterSource(ctx, wb, snapshot); err != nil {
		return nil, nil, err
	}
	a.mu.RLock()
	current := a.wb == wb && a.clusterJobs[id] == job && !job.discarded
	a.mu.RUnlock()
	if !current {
		return nil, nil, errors.New("聚类来源已切换，请重新检查")
	}
	return job, snapshot, nil
}

func clusterEnvelope(job *clusterTask, snapshot *clusterSnapshot) map[string]any {
	return map[string]any{"id": job.ID, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "source": snapshot.Source, "field": snapshot.Field, "fieldName": snapshot.FieldName, "headerRow": snapshot.HeaderRow, "sourceScope": snapshot.Scope, "stamp": snapshot.Stamp}
}

func (a *App) handleClusterGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	offset, err := dataRuleQueryInt(r, "offset")
	if err != nil {
		writeError(w, 400, "候选分页位置无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job, snapshot, err := a.clusterSnapshot(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	total := len(snapshot.Result.Groups)
	if offset > total {
		writeError(w, 400, "候选分页位置超出结果")
		return
	}
	items := make([]map[string]any, 0, formats.ClusterPageGroups)
	for _, group := range snapshot.Result.Groups[offset:min(total, offset+formats.ClusterPageGroups)] {
		label, shortened := catalogueListLabel(group.Key)
		items = append(items, map[string]any{"id": group.ID, "key": label, "shortened": shortened, "kind": group.Kind, "count": group.Count, "variants": len(group.Variants)})
	}
	payload := clusterEnvelope(job, snapshot)
	payload["items"], payload["offset"], payload["next"], payload["total"] = items, offset, offset+len(items), total
	payload["counts"], payload["options"] = snapshot.Result.Counts, snapshot.Result.Options
	writeJSON(w, payload)
}

func (a *App) handleClusterDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	groupID, e1 := dataRuleQueryInt(r, "group")
	offset, e2 := dataRuleQueryInt(r, "offset")
	if e1 != nil || e2 != nil {
		writeError(w, 400, "候选组或原文分页无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job, snapshot, err := a.clusterSnapshot(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if groupID >= len(snapshot.Result.Groups) {
		writeError(w, 404, "候选组不存在")
		return
	}
	group := snapshot.Result.Groups[groupID]
	if offset > len(group.Variants) {
		writeError(w, 400, "候选原文分页超出结果")
		return
	}
	payload := clusterEnvelope(job, snapshot)
	payload["group"], payload["key"], payload["kind"], payload["count"] = group.ID, group.Key, group.Kind, group.Count
	payload["variants"] = group.Variants[offset:min(len(group.Variants), offset+formats.ClusterPageVariants)]
	payload["offset"], payload["next"], payload["total"] = offset, offset+min(formats.ClusterPageVariants, len(group.Variants)-offset), len(group.Variants)
	writeJSON(w, payload)
}

type clusterChoice struct {
	Group          int    `json:"group"`
	Representative string `json:"representative"`
}

func (a *App) handleClusterConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID, WorkbookKey, Book string
		Choices               []clusterChoice
	}
	if decodeClusterRequest(w, r, &req) != nil || len(req.Choices) == 0 || len(req.Choices) > formats.TextMappingEntries {
		writeError(w, 400, "请明确选择候选组及代表原文")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job, snapshot, err := a.clusterSnapshot(r.Context(), req.ID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if req.WorkbookKey != job.Workbook.Fingerprint || req.Book != sourceBookID(job.Workbook) {
		writeError(w, 409, "确认来源已切换")
		return
	}
	seen, values := make(map[int]bool), make(map[string]string)
	for _, choice := range req.Choices {
		if choice.Group < 0 || choice.Group >= len(snapshot.Result.Groups) || seen[choice.Group] {
			writeError(w, 400, "候选组无效或重复")
			return
		}
		seen[choice.Group] = true
		group := snapshot.Result.Groups[choice.Group]
		found := false
		for _, value := range group.Variants {
			found = found || value.Raw == choice.Representative
		}
		if !found || group.Kind != "similar" {
			writeError(w, 400, "代表值必须是疑似重复组中已有的原文")
			return
		}
		for _, value := range group.Variants {
			if value.Raw == choice.Representative {
				continue
			}
			if previous, exists := values[value.Raw]; exists && previous != choice.Representative {
				writeError(w, 400, "同一原文对应了不同代表值，请重新核对")
				return
			}
			values[value.Raw] = choice.Representative
		}
	}
	step := analysis.Transform{ID: "D_map_" + randomID(), Name: "文本核对", Source: snapshot.Field, Op: "map_text"}
	for from, to := range values {
		step.Mapping = append(step.Mapping, analysis.TextMapping{From: from, To: to})
	}
	sort.Slice(step.Mapping, func(i, j int) bool { return step.Mapping[i].From < step.Mapping[j].From })
	if err := analysis.ValidateTextMapping(step); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	payload := clusterEnvelope(job, snapshot)
	payload["transform"], payload["confirmedGroups"] = step, len(req.Choices)
	writeJSON(w, payload)
}

func (a *App) handleClusterResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	groupID, e1 := dataRuleQueryInt(r, "group")
	variant, e2 := dataRuleQueryInt(r, "variant")
	sample, e3 := dataRuleQueryInt(r, "sample")
	if e1 != nil || e2 != nil || e3 != nil {
		writeError(w, 400, "候选源坐标无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job, snapshot, err := a.clusterSnapshot(r.Context(), q.Get("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if q.Get("workbookKey") != job.Workbook.Fingerprint || q.Get("book") != sourceBookID(job.Workbook) {
		writeError(w, 409, "定位来源已切换")
		return
	}
	groups := snapshot.Result.Groups
	if groupID >= len(groups) || variant >= len(groups[groupID].Variants) || sample >= len(groups[groupID].Variants[variant].Rows) {
		writeError(w, 404, "候选源坐标不存在")
		return
	}
	row := groups[groupID].Variants[variant].Rows[sample]
	payload := clusterEnvelope(job, snapshot)
	payload["row"], payload["column"], payload["ref"] = row, snapshot.Column, workbook.ColumnName(snapshot.Column)+strconv.Itoa(row)
	writeJSON(w, payload)
}
