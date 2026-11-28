package server

import (
	"context"
	"errors"

	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
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

var errCompositionRevision = i18n.Error("service.compositionStore.combinedSourceDirectoryModifiedOther")
var errCompositionName = i18n.Error("service.compositionStore.thereAlreadyJointSourceSame")
var errCompositionDefinitionMissing = i18n.Error("service.compositionStore.combinedSourceDefinitionRemovedRefresh")

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
		return store, i18n.Error("service.compositionStore.combinationSourceDirectoryVersionRevision")
	}
	ids, captures, names := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for i := range store.Items {
		item := &store.Items[i]
		definition, err := item.Definition.Normalized()
		name := strings.ToLower(definition.Name)
		if err != nil || !validSessionID(item.ID) || !validSessionID(item.CaptureID) || ids[item.ID] || captures[item.CaptureID] || names[name] {
			return store, i18n.Error("service.compositionStore.combinedSourceDefinitionInvalidOr")
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Created); err != nil {
			return store, i18n.Error("service.compositionStore.invalidCreationTimeCombinedSource")
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Updated); err != nil {
			return store, i18n.Error("service.compositionStore.invalidUpdateTimeCombinedSource")
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
				return savedComposition{}, 0, false, i18n.Error("service.compositionStore.thisSaveRequestAssociatedOther")
			}
			return item, store.Revision, true, nil
		}
	}
	return savedComposition{}, store.Revision, false, nil
}

func saveCompositionDefinition(path string, value savedComposition, expected int64, replace bool, ops configWriteOps) (int64, error) {
	definition, err := value.Definition.Normalized()
	if err != nil || !validSessionID(value.ID) || !validSessionID(value.CaptureID) {
		return 0, i18n.Error("service.compositionStore.invalidSaveParametersJointSources")
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
			return 0, i18n.Error("service.compositionStore.thisSaveRequestCompletedRetry")
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
		return 0, i18n.Error("service.compositionStore.thisSaveRequestCompletedPrepare")
	}
	if position < 0 && len(store.Items) >= formats.CompositionDefinitions {
		return 0, i18n.Error("service.compositionStore.upJointSourcesCanBe", i18n.Args{"compositionDefinitions": formats.CompositionDefinitions})
	}
	for _, old := range store.Items {
		if old.ID != value.ID && strings.EqualFold(old.Definition.Name, value.Definition.Name) {
			return 0, errCompositionName
		}
	}
	if store.Revision+1 >= maxConfigRevision {
		return 0, i18n.Error("service.compositionStore.combinedSourceDirectoryRevisionNumbers")
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
		return 0, i18n.Error("service.compositionStore.combinedSourceDirectoryRevisionNumbers")
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
			return i18n.Error("service.compositionStore.contentAnalysisTableChangedPrepare")
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
	writeFailure(w, status, err)
}

func (a *App) handleCompositionDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	path, err := compositionDefinitionsPath()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readCompositionDefinitions(path)
		lock.RUnlock()
		if err != nil {
			writeFailure(w, 422, err)
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
		writeMessage(w, 400, "service.compositionStore.invalidDirectoryOperationsJointSources")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.compositionStore.workbookSwitchedCombinedSourceNot")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if req.Action == "save" {
		// A lost confirmation is retryable after task eviction or restart. Only
		// the exact previously committed capture may bypass re-preparation.
		item, revision, found, err := findSavedCompositionCapture(path, req.JobID, req.ID)
		if err != nil {
			writeFailure(w, 422, err)
			return
		}
		if found {
			a.mu.RLock()
			current := a.wb == wb && !a.maintenance
			a.mu.RUnlock()
			if !current {
				writeMessage(w, 409, "service.compositionStore.workbookSwitchedSaveConfirmationNot")
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
			writeFailure(w, 409, err)
			return
		}
		defer source.close()
		if err := a.compositionReferencesCurrent(r.Context(), source); err != nil {
			writeFailure(w, 409, err)
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
		writeMessage(w, 409, "service.compositionStore.workbookSwitchedOrSaveOperation")
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
