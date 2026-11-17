package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
)

type savedComposition struct {
	ID         string            `json:"id"`
	CaptureID  string            `json:"captureId"`
	Definition sourceview.Append `json:"definition"`
	Created    string            `json:"created"`
	Updated    string            `json:"updated"`
}

type compositionDefinitionStore struct {
	Version  int                `json:"version"`
	Revision int64              `json:"revision"`
	Items    []savedComposition `json:"items"`
}

var errCompositionRevision = errors.New("联合来源目录已被其他页面修改，请刷新后重试")
var errCompositionName = errors.New("已有同名联合来源，请更换名称")
var errCompositionDefinitionMissing = errors.New("联合来源定义已移除，请刷新目录")

func compositionDefinitionsPath() (string, error) {
	return savedConfigPath("compositions", "catalog")
}

func readCompositionDefinitions(path string) (compositionDefinitionStore, error) {
	store := compositionDefinitionStore{Version: 1, Items: []savedComposition{}}
	found, err := readSavedConfig(path, formats.CompositionCatalogBytes, &store)
	if err != nil || !found {
		return store, err
	}
	if store.Version != 1 || store.Revision < 0 || store.Revision >= maxConfigRevision || len(store.Items) > formats.CompositionDefinitions {
		return store, errors.New("联合来源目录版本、修订或数量无效，原文件已保留")
	}
	ids, captures, names := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for i := range store.Items {
		item := &store.Items[i]
		definition, err := item.Definition.Normalized()
		name := strings.ToLower(definition.Name)
		if err != nil || !validSessionID(item.ID) || !validSessionID(item.CaptureID) || ids[item.ID] || captures[item.CaptureID] || names[name] {
			return store, errors.New("联合来源定义无效或重复，原文件已保留")
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Created); err != nil {
			return store, errors.New("联合来源创建时间无效，原文件已保留")
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Updated); err != nil {
			return store, errors.New("联合来源更新时间无效，原文件已保留")
		}
		item.Definition = definition
		ids[item.ID], captures[item.CaptureID], names[name] = true, true, true
	}
	if store.Items == nil {
		store.Items = []savedComposition{}
	}
	return store, nil
}

func findSavedCompositionCapture(path, captureID, targetID string) (savedComposition, int64, bool, error) {
	lock := persistentFileLock(path)
	lock.RLock()
	defer lock.RUnlock()
	store, err := readCompositionDefinitions(path)
	if err != nil {
		return savedComposition{}, 0, false, err
	}
	for _, item := range store.Items {
		if item.CaptureID == captureID {
			if targetID != "" && item.ID != targetID || targetID == "" && item.ID != captureID {
				return savedComposition{}, 0, false, errors.New("此保存请求已关联其他联合来源")
			}
			return item, store.Revision, true, nil
		}
	}
	return savedComposition{}, store.Revision, false, nil
}

func saveCompositionDefinition(path string, value savedComposition, expected int64, replace bool, ops configWriteOps) (int64, error) {
	definition, err := value.Definition.Normalized()
	if err != nil || !validSessionID(value.ID) || !validSessionID(value.CaptureID) {
		return 0, errors.New("联合来源保存参数无效")
	}
	value.Definition = definition
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readCompositionDefinitions(path)
	if err != nil {
		return 0, err
	}
	position := -1
	for i, old := range store.Items {
		if old.CaptureID == value.CaptureID && (old.ID != value.ID || !reflect.DeepEqual(old.Definition, value.Definition)) {
			return 0, errors.New("此保存请求已完成，不能使用不同内容重试")
		}
		if old.ID == value.ID {
			position = i
			if old.CaptureID == value.CaptureID && reflect.DeepEqual(old.Definition, value.Definition) {
				return store.Revision, nil
			}
		}
	}
	if store.Revision != expected {
		return 0, errCompositionRevision
	}
	if replace && position < 0 {
		return 0, errCompositionDefinitionMissing
	}
	if !replace && position >= 0 {
		return 0, errors.New("此保存请求已经完成，请使用新的来源准备结果")
	}
	if position < 0 && len(store.Items) >= formats.CompositionDefinitions {
		return 0, fmt.Errorf("最多保存%d个联合来源，请先删除不用的定义", formats.CompositionDefinitions)
	}
	for _, old := range store.Items {
		if old.ID != value.ID && strings.EqualFold(old.Definition.Name, value.Definition.Name) {
			return 0, errCompositionName
		}
	}
	if store.Revision+1 >= maxConfigRevision {
		return 0, errors.New("联合来源目录修订号达到上限，原文件已保留")
	}
	value.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	if position >= 0 {
		value.Created = store.Items[position].Created
		store.Items[position] = value
	} else {
		value.Created = value.Updated
		store.Items = append(store.Items, value)
	}
	store.Revision++
	if err := writeSavedConfig(path, formats.CompositionCatalogBytes, store, ops); err != nil {
		return 0, err
	}
	return store.Revision, nil
}

func deleteCompositionDefinition(path, id string, expected int64, ops configWriteOps) (int64, error) {
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readCompositionDefinitions(path)
	if err != nil {
		return 0, err
	}
	if store.Revision != expected {
		return 0, errCompositionRevision
	}
	position := -1
	for i, item := range store.Items {
		if item.ID == id {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, errCompositionDefinitionMissing
	}
	if store.Revision+1 >= maxConfigRevision {
		return 0, errors.New("联合来源目录修订号达到上限，原文件已保留")
	}
	store.Items = append(store.Items[:position], store.Items[position+1:]...)
	store.Revision++
	if err := writeSavedConfig(path, formats.CompositionCatalogBytes, store, ops); err != nil {
		return 0, err
	}
	return store.Revision, nil
}

func (a *App) compositionReferencesCurrent(ctx context.Context, source *compositionSnapshot) error {
	if err := source.data.plan.CheckSources(ctx); err != nil {
		return err
	}
	for i, info := range source.data.info {
		if !strings.HasPrefix(info.Ref.Source, "scratch:") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		workspace, err := a.loadAnalysis(source.data.books[i])
		if err != nil {
			return err
		}
		sheet := workspace.Sheet(strings.TrimPrefix(info.Ref.Source, "scratch:"))
		if sheet == nil || analysisSheetStamp(sheet) != info.Ref.Stamp {
			return errors.New("分析表内容已变化，请重新准备联合来源后保存")
		}
	}
	return ctx.Err()
}

func compositionStoreError(w http.ResponseWriter, err error) {
	status := 500
	if errors.Is(err, errCompositionRevision) || errors.Is(err, errCompositionName) {
		status = 409
	} else if errors.Is(err, errCompositionDefinitionMissing) {
		status = 404
	}
	writeError(w, status, err.Error())
}

func (a *App) handleCompositionDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	path, err := compositionDefinitionsPath()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readCompositionDefinitions(path)
		lock.RUnlock()
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		writeJSON(w, store)
		return
	}
	var req struct {
		Action           string `json:"action"`
		ID               string `json:"id,omitempty"`
		JobID            string `json:"jobId,omitempty"`
		Book             string `json:"book"`
		WorkbookKey      string `json:"workbookKey"`
		ExpectedRevision *int64 `json:"expectedRevision"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CompositionCatalogRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || req.Action != "save" && req.Action != "delete" || req.ID != "" && !validSessionID(req.ID) || req.Action == "delete" && req.ID == "" || req.Action == "save" && !validSessionID(req.JobID) {
		writeError(w, 400, "联合来源目录操作无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，联合来源未保存")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if req.Action == "save" {
		// A lost confirmation is retryable after task eviction or restart. Only
		// the exact previously committed capture may bypass re-preparation.
		item, revision, found, err := findSavedCompositionCapture(path, req.JobID, req.ID)
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		if found {
			a.mu.RLock()
			current := a.wb == wb && !a.maintenance
			a.mu.RUnlock()
			if !current {
				writeError(w, 409, "工作簿已切换，保存确认未接受")
				return
			}
			writeJSON(w, map[string]any{"id": item.ID, "revision": revision})
			return
		}
	}
	var value savedComposition
	if req.Action == "save" {
		source, err := a.borrowCompositionTask(req.JobID, wb)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer source.close()
		if err := a.compositionReferencesCurrent(r.Context(), source); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		value = savedComposition{ID: req.ID, CaptureID: req.JobID, Definition: source.data.plan.Definition()}
		if value.ID == "" {
			value.ID = req.JobID
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		writeError(w, 409, "工作簿已切换或保存已取消")
		return
	}
	var revision int64
	if req.Action == "save" {
		revision, err = saveCompositionDefinition(path, value, *req.ExpectedRevision, req.ID != "", durableConfigOps)
	} else {
		revision, err = deleteCompositionDefinition(path, req.ID, *req.ExpectedRevision, durableConfigOps)
		value.ID = req.ID
	}
	if err != nil {
		compositionStoreError(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": value.ID, "revision": revision})
}
