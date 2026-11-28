package server

import (
	"context"
	"net/http"
	"os"

	"excellens/internal/checkpoint"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func taskDefinitionForWorkbook(wb *workbook.Workbook, m checkpoint.Manifest) (checkpoint.Definition, error) {
	switch m.Definition.Driver {
	case searchCheckpointDriver:
		spec, err := searchCheckpointSpecFor(m)
		if err != nil {
			return checkpoint.Definition{}, err
		}
		return searchTaskDefinition(wb, searchRequest{Sheet: spec.Sheet, Query: spec.Query, Mode: spec.Mode, Scope: spec.Scope, Full: spec.Full})
	case filterCheckpointDriver:
		spec, err := filterCheckpointSpecFor(m)
		if err != nil {
			return checkpoint.Definition{}, err
		}
		return filterTaskDefinition(wb, spec.Sheet, spec.Filter)
	case exportCheckpointDriver:
		return exportCheckpointResumeDefinition(wb, m)
	}
	return checkpoint.Definition{}, checkpoint.ErrBinding
}

// Open a source named by a bounded, expected-revision task definition. Listing
// never calls this. The candidate is verified before the current Workbook or
// any of its jobs are changed, then installed through the shared lifecycle.
func (a *App) handleTaskCheckpointOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := readTaskCheckpointRequest(w, r)
	if err != nil || req.Revision == 0 {
		writeMessage(w, 400, "service.taskCheckpointOpen.taskSourceRequestInvalidRefresh")
		return
	}
	expected := a.currentWorkbook()
	if expected == nil && (req.Book != "" || req.WorkbookKey != "") || expected != nil && (req.Book != sourceBookID(expected) || req.WorkbookKey != expected.Fingerprint) {
		writeMessage(w, 409, "service.taskCheckpointOpen.currentFileSwitchedTryAgain")
		return
	}
	if !a.admitCheckpoint(w) {
		return
	}
	defer func() { <-a.rootApp().recoverySem }()
	store, err := openTaskCheckpoints()
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	defer store.Close()
	m, err := loadTaskOpenManifest(r.Context(), store, req)
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	if len(m.Definition.Sources) != 1 {
		checkpointHTTPError(w, checkpoint.ErrBinding)
		return
	}
	source := m.Definition.Sources[0]
	source, err = locatedSource(source)
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	wb, release, err := a.openPinnedWorkbook(source.Path)
	if err != nil {
		writeMessage(w, 422, "service.taskCheckpointOpen.unableOpenOriginalTaskFile", i18n.Args{"detail": err})
		return
	}
	defer release()
	if m.Definition.FileName != "" {
		wb.SetDisplayName(m.Definition.FileName)
	}
	def, err := taskDefinitionForWorkbook(wb, m)
	if err == nil {
		var binding string
		binding, err = taskBinding(r.Context(), wb, def, m, true)
		if err == nil && binding != m.DefinitionHash {
			err = checkpoint.ErrBinding
		}
	}
	if err == nil {
		err = wb.CheckSourceUnchanged()
	}
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	if _, err := loadTaskOpenManifest(r.Context(), store, req); err != nil {
		checkpointHTTPError(w, err)
		return
	}
	if err := r.Context().Err(); err != nil {
		checkpointHTTPError(w, err)
		return
	}
	info, err := os.Stat(wb.Path)
	if err != nil {
		writeMessage(w, 422, "service.taskCheckpointOpen.originalTaskFileMovedOr")
		return
	}
	if !a.installWorkbook(wb, expected, true) {
		writeMessage(w, 409, "service.taskCheckpointOpen.currentFileSwitchedNoReplacement")
		return
	}
	a.closeArchive()
	a.recordRecent(wb)
	writeJSON(w, openedWorkbook(wb, workbookDisplayName(wb), info.Size()))
}

func loadTaskOpenManifest(ctx context.Context, store *checkpoint.Store, req taskCheckpointRequest) (checkpoint.Manifest, error) {
	entries, err := listTaskCheckpoints(ctx, store)
	if err != nil {
		return checkpoint.Manifest{}, err
	}
	for _, entry := range entries {
		if entry.ID != req.ID {
			continue
		}
		if entry.Error != "" || entry.Manifest == nil {
			return checkpoint.Manifest{}, checkpoint.ErrCorrupt
		}
		if entry.Manifest.Revision != req.Revision {
			return checkpoint.Manifest{}, checkpoint.ErrConflict
		}
		if entry.Busy {
			return checkpoint.Manifest{}, checkpoint.ErrBusy
		}
		return *entry.Manifest, nil
	}
	return checkpoint.Manifest{}, os.ErrNotExist
}
