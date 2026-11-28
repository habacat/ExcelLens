package server

import (
	"encoding/json"

	"io"
	"net/http"
	"path/filepath"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/i18n"
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
		return store, i18n.Error("service.comparisonSnapshots.comparisonSnapshotDirectoryInvalidOriginal")
	}
	ids := make(map[string]bool)
	for _, item := range store.Items {
		if ids[item.ID] {
			return store, i18n.Error("service.comparisonSnapshots.comparisonSnapshotNumbersDuplicatedOriginal")
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
			writeMessage(w, 400, "service.comparisonSnapshots.invalidSnapshotParameterOrRevision")
			return
		}
		if req.Op != "capture" && req.Op != "rename" && req.Op != "delete" || req.Op != "delete" && (!analysis.NameValid(req.Name) || len(req.Name) > 240) {
			writeMessage(w, 400, "service.comparisonSnapshots.selectValidOperationFillSnapshot")
			return
		}
		if req.Op == "capture" {
			wb := a.currentWorkbook()
			if wb == nil || wb.Fingerprint != req.WorkbookKey || sourceBookID(wb) != req.Book {
				writeMessage(w, 409, "service.comparisonSnapshots.analysisSourceSwitchedReopenCurrent")
				return
			}
			job := a.getAnalysisJob(req.JobID)
			if job == nil || job.Workbook != wb {
				writeMessage(w, 404, "service.comparisonSnapshots.analysisResultsHaveExpiredRun")
				return
			}
			job.mu.RLock()
			if job.Status != "done" {
				job.mu.RUnlock()
				writeMessage(w, 409, "service.comparisonSnapshots.completeAnalysisBeforeSavingSnapshot")
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
				writeFailure(w, 422, err)
				return
			}
			candidate = &item
		}
	}
	path, err := savedConfigPath("snapshots", "comparisons")
	if err != nil {
		writeFailure(w, 500, err)
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
				err, code = i18n.Error("service.comparisonSnapshots.snapshotNoLongerExistsRefresh"), 404
				return
			}
			response = snapshotCatalog(store)
			return
		}
		if store.Revision != *req.ExpectedRevision || store.Revision+1 >= maxConfigRevision {
			err, code = i18n.Error("service.comparisonSnapshots.snapshotDirectoryChangedRefreshTry"), 409
			return
		}
		if candidate != nil {
			if len(store.Items) >= formats.SnapshotEntries {
				err, code = i18n.Error("service.comparisonSnapshots.numberSnapshotsReachedLimitDelete"), 422
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
				err, code = i18n.Error("service.comparisonSnapshots.snapshotNoLongerExistsRefresh"), 404
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
		writeFailure(w, code, err)
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
		writeFailure(w, 500, err)
		return
	}
	lock := persistentFileLock(path)
	lock.RLock()
	store, err := readComparisonSnapshots(path)
	lock.RUnlock()
	if err != nil {
		writeFailure(w, 422, err)
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
		writeMessage(w, 404, "service.comparisonSnapshots.snapshotNoLongerExistsRefresh")
		return
	}
	comparison, err := analysis.CompareSnapshotsContext(r.Context(), *left, *right)
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	data, err := json.Marshal(comparison)
	if err != nil || len(data) > 8*formats.SnapshotBytes {
		writeMessage(w, 422, "service.comparisonSnapshots.comparisonResultsExceedCapacityNarrow")
		return
	}
	release()
	if r.Context().Err() == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	}
}
