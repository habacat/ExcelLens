package server

import (
	"context"
	"net/http"
	"os"

	"excellens/internal/checkpoint"
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
		writeError(w, 400, "任务来源请求无效，请刷新后重试")
		return
	}
	expected := a.currentWorkbook()
	if expected == nil && (req.Book != "" || req.WorkbookKey != "") || expected != nil && (req.Book != sourceBookID(expected) || req.WorkbookKey != expected.Fingerprint) {
		writeError(w, 409, "当前文件已切换，请重试")
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
		writeError(w, 422, "无法打开任务原文件："+err.Error())
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
		writeError(w, 422, "任务原文件已移动或删除")
		return
	}
	if !a.installWorkbook(wb, expected, true) {
		writeError(w, 409, "当前文件已切换，未替换当前视图")
		return
	}
	a.closeArchive()
	a.recordRecent(wb)
	writeJSON(w, map[string]any{"opened": true, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "fileName": workbookDisplayName(wb), "fileSize": info.Size(), "sheetCount": len(wb.Sheets), "activeTab": wb.ActiveTab, "sheets": wb.Sheets, "source": wb.SourceInfo})
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
