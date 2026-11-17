package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
)

type comparisonSnapshotStore struct {
	Version  int                           `json:"version"`
	Revision int64                         `json:"revision"`
	Items    []analysis.ComparisonSnapshot `json:"items"`
}

type comparisonSnapshotSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	File    string `json:"file"`
}

func readComparisonSnapshots(path string) (comparisonSnapshotStore, error) {
	store := comparisonSnapshotStore{Version: 1, Items: []analysis.ComparisonSnapshot{}}
	found, err := readSavedConfig(path, formats.SnapshotStoreBytes, &store)
	if err != nil || !found {
		return store, err
	}
	if store.Version != 1 || store.Revision < 0 || store.Revision >= maxConfigRevision || store.Items == nil || len(store.Items) > formats.SnapshotEntries {
		return store, errors.New("比较快照目录无效，原文件已保留")
	}
	ids := make(map[string]bool)
	for _, item := range store.Items {
		if ids[item.ID] {
			return store, errors.New("比较快照编号重复，原文件已保留")
		}
		ids[item.ID] = true
		if err := analysis.ValidateComparisonSnapshot(item); err != nil {
			return store, err
		}
	}
	return store, nil
}

func snapshotCatalog(store comparisonSnapshotStore) any {
	items := make([]comparisonSnapshotSummary, 0, len(store.Items))
	for _, item := range store.Items {
		items = append(items, comparisonSnapshotSummary{item.ID, item.Name, item.Created, item.File})
	}
	return struct {
		Version  int                         `json:"version"`
		Revision int64                       `json:"revision"`
		Items    []comparisonSnapshotSummary `json:"items"`
	}{store.Version, store.Revision, items}
}

func (a *App) handleComparisonSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Op               string `json:"op"`
		ExpectedRevision *int64 `json:"expectedRevision"`
		ID               string `json:"id"`
		Name             string `json:"name"`
		JobID            string `json:"jobId"`
		WorkbookKey      string `json:"workbookKey"`
		Book             string `json:"book"`
		MetricIndex      int    `json:"metricIndex"`
	}
	var candidate *analysis.ComparisonSnapshot
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.SnapshotRequestBytes))
		if err != nil || decodeSavedConfig(data, &req) != nil || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || *req.ExpectedRevision >= maxConfigRevision {
			writeError(w, 400, "快照参数或修订号无效")
			return
		}
		if req.Op != "capture" && req.Op != "rename" && req.Op != "delete" || req.Op != "delete" && (!analysis.NameValid(req.Name) || len(req.Name) > 240) {
			writeError(w, 400, "请选择有效操作并填写快照名称")
			return
		}
		if req.Op == "capture" {
			wb := a.currentWorkbook()
			if wb == nil || wb.Fingerprint != req.WorkbookKey || sourceBookID(wb) != req.Book {
				writeError(w, 409, "分析来源已切换，请重新打开当前结果")
				return
			}
			job := a.getAnalysisJob(req.JobID)
			if job == nil || job.Workbook != wb {
				writeError(w, 404, "分析结果已过期，请重新运行分析")
				return
			}
			job.mu.RLock()
			if job.Status != "done" {
				job.mu.RUnlock()
				writeError(w, 409, "请先完成分析再保存快照")
				return
			}
			metric, result, err := analysis.ProjectSnapshotMetric(job.Query, job.Result, req.MetricIndex)
			item := analysis.ComparisonSnapshot{Version: 1, ID: randomID(), Name: req.Name, Created: time.Now().UTC().Format(time.RFC3339Nano), AnalysisAt: job.Created.UTC().Format(time.RFC3339Nano),
				WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), File: wb.DisplayName, SourceStamp: job.SourceStamp, Query: job.Query, Fields: job.SourceFields, MetricIndex: req.MetricIndex, Metric: metric, Result: result}
			job.mu.RUnlock()
			if item.File == "" {
				item.File = filepath.Base(wb.Path)
			}
			if err == nil {
				err = analysis.ValidateComparisonSnapshot(item)
			}
			if err != nil {
				writeError(w, 422, err.Error())
				return
			}
			candidate = &item
		}
	}
	path, err := savedConfigPath("snapshots", "comparisons")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Release the file lock before encoding/writing a response to a slow client.
	var response any
	code := 200
	func() {
		lock := persistentFileLock(path)
		lock.Lock()
		defer lock.Unlock()
		store, e := readComparisonSnapshots(path)
		if e != nil {
			err, code = e, 422
			return
		}
		if r.Method == http.MethodGet {
			if id := r.URL.Query().Get("id"); id != "" {
				for _, item := range store.Items {
					if item.ID == id {
						response = item
						return
					}
				}
				err, code = errors.New("快照已不存在，请刷新目录"), 404
				return
			}
			response = snapshotCatalog(store)
			return
		}
		if store.Revision != *req.ExpectedRevision || store.Revision+1 >= maxConfigRevision {
			err, code = errors.New("快照目录已变化，请刷新后重试"), 409
			return
		}
		if candidate != nil {
			if len(store.Items) >= formats.SnapshotEntries {
				err, code = errors.New("快照数量已达上限，请先删除不用的快照"), 422
				return
			}
			store.Items = append(store.Items, *candidate)
		} else {
			index := -1
			for i, item := range store.Items {
				if item.ID == req.ID {
					index = i
					break
				}
			}
			if index < 0 {
				err, code = errors.New("快照已不存在，请刷新目录"), 404
				return
			}
			if req.Op == "delete" {
				store.Items = append(store.Items[:index], store.Items[index+1:]...)
			} else {
				store.Items[index].Name = req.Name
			}
		}
		store.Revision++
		if e := writeSavedConfig(path, formats.SnapshotStoreBytes, store, durableConfigOps); e != nil {
			err, code = e, 500
			return
		}
		response = snapshotCatalog(store)
	}()
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, response)
}

func (a *App) handleSnapshotComparison(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	// Exact comparisons are bounded CPU work and share the analysis slots.
	select {
	case a.analysisSem <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	held := true
	release := func() {
		if held {
			held = false
			<-a.analysisSem
		}
	}
	defer release()
	path, err := savedConfigPath("snapshots", "comparisons")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	lock := persistentFileLock(path)
	lock.RLock()
	store, err := readComparisonSnapshots(path)
	lock.RUnlock()
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	var left, right *analysis.ComparisonSnapshot
	for i := range store.Items {
		item := &store.Items[i]
		if item.ID == r.URL.Query().Get("left") {
			left = item
		}
		if item.ID == r.URL.Query().Get("right") {
			right = item
		}
	}
	if left == nil || right == nil {
		writeError(w, 404, "快照已不存在，请刷新目录")
		return
	}
	comparison, err := analysis.CompareSnapshotsContext(r.Context(), *left, *right)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	data, err := json.Marshal(comparison)
	if err != nil || len(data) > 8*formats.SnapshotBytes {
		writeError(w, 422, "比较结果超出容量，请缩小分析范围后重新保存快照")
		return
	}
	release()
	if r.Context().Err() == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	}
}
