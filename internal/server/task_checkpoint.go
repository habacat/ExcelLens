package server

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

type taskCheckpointRequest struct {
	ID          string `json:"id"`
	Revision    uint64 `json:"revision,string"`
	WorkbookKey string `json:"workbookKey,omitempty"`
	Book        string `json:"book,omitempty"`
}

type taskCheckpointInfo struct {
	ID          string    `json:"id"`
	Revision    uint64    `json:"revision,string"`
	Kind        string    `json:"kind"`
	Driver      string    `json:"driver"`
	Title       string    `json:"title"`
	File        string    `json:"file"`
	Path        string    `json:"path"`
	Book        string    `json:"book"`
	WorkbookKey string    `json:"workbookKey"`
	Sheet       string    `json:"sheet"`
	Query       string    `json:"query"`
	Mode        string    `json:"mode"`
	Scope       string    `json:"scope"`
	Updated     time.Time `json:"updated"`
	Complete    bool      `json:"complete"`
	Busy        bool      `json:"busy"`
	Backup      bool      `json:"backup"`
	Error       string    `json:"error"`
}

// The directory is shared across tabs/processes. Listing only reads bounded
// manifests; it never opens source workbooks, builds indexes or acquires jobs.
func (a *App) handleTaskCheckpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
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
	entries, err := listTaskCheckpoints(r.Context(), store)
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	items := make([]taskCheckpointInfo, 0, len(entries))
	for _, entry := range entries {
		item := taskCheckpointInfo{ID: entry.ID, Busy: entry.Busy, Backup: entry.Backup, Error: entry.Error}
		if m := entry.Manifest; m != nil {
			item.Revision, item.Kind, item.Title, item.Updated, item.Complete = m.Revision, m.Definition.Kind, m.Definition.Title, m.Updated, m.Complete
			item.Driver = m.Definition.Driver
			if len(m.Definition.Sources) > 0 {
				source := m.Definition.Sources[0]
				item.File, item.Path, item.Book, item.WorkbookKey = filepath.Base(source.Path), source.Path, source.Book, source.Fingerprint
				if m.Definition.FileName != "" {
					item.File = m.Definition.FileName
				}
			}
			switch m.Definition.Driver {
			case searchCheckpointDriver:
				spec, err := searchCheckpointSpecFor(*m)
				if err != nil {
					item.Error = err.Error()
				} else {
					item.Sheet, item.Query, item.Mode, item.Scope = spec.Sheet, spec.Query, spec.Mode, spec.Scope
				}
			case filterCheckpointDriver:
				spec, err := filterCheckpointSpecFor(*m)
				if err == nil {
					_, err = filterCheckpointCursor(*m, spec)
				}
				if err != nil {
					item.Error = err.Error()
				} else {
					item.Sheet, item.Mode, item.Scope = spec.Sheet, "filter", "current"
				}
			case exportCheckpointDriver:
				spec, err := exportCheckpointSpecFor(*m)
				var cursor exportTaskCursor
				if err == nil {
					cursor, err = exportCheckpointCursor(*m, spec)
				}
				if err != nil {
					item.Error = err.Error()
				} else {
					item.Sheet, item.Mode, item.Scope = spec.Sheets[0].Name, spec.Options.Format, spec.Scope
					if !cursor.Snapshot {
						item.Error = errExportCaptureIncomplete.Error()
					}
				}
			default:
				item.Error = checkpoint.ErrBinding.Error()
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].Updated.Equal(items[j].Updated) {
			return items[i].Updated.After(items[j].Updated)
		}
		return items[i].ID < items[j].ID
	})
	writeJSON(w, map[string]any{"items": items})
}

func readTaskCheckpointRequest(w http.ResponseWriter, r *http.Request) (taskCheckpointRequest, error) {
	var req taskCheckpointRequest
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CheckpointRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || len(req.ID) != 32 {
		return req, errors.New("任务恢复请求无效")
	}
	return req, nil
}

func (a *App) handleTaskCheckpointResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := readTaskCheckpointRequest(w, r)
	if err != nil || req.Revision == 0 {
		writeError(w, 400, "任务恢复请求无效，请刷新后重试")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "请先打开任务的原始来源")
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
	owned := false
	defer func() {
		if !owned {
			_ = store.Close()
		}
	}()
	entries, err := listTaskCheckpoints(r.Context(), store)
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	for _, entry := range entries {
		if entry.ID != req.ID {
			continue
		}
		if entry.Manifest == nil || entry.Error != "" {
			checkpointHTTPError(w, checkpoint.ErrCorrupt)
			return
		}
		m := *entry.Manifest
		if m.Revision != req.Revision {
			checkpointHTTPError(w, checkpoint.ErrConflict)
			return
		}
		var def checkpoint.Definition
		var queue func(*checkpoint.Lease)
		switch m.Definition.Driver {
		case searchCheckpointDriver:
			var spec searchCheckpointSpec
			spec, err = searchCheckpointSpecFor(m)
			if err == nil && (m.DataBytes > formats.CheckpointSearchBytes || m.Records > formats.CheckpointSearchHits) {
				err = checkpoint.ErrCorrupt
			}
			if err == nil {
				search := searchRequest{Sheet: spec.Sheet, Query: spec.Query, Mode: spec.Mode, Scope: spec.Scope}
				def, err = searchTaskDefinition(wb, search)
				queue = func(lease *checkpoint.Lease) { a.queueCheckpointSearch(w, r, wb, search, store, lease, false) }
			}
		case filterCheckpointDriver:
			var spec filterCheckpointSpec
			spec, err = filterCheckpointSpecFor(m)
			if err == nil {
				_, err = filterCheckpointCursor(m, spec)
			}
			if err == nil {
				def, err = filterTaskDefinition(wb, spec.Sheet, spec.Filter)
				queue = func(lease *checkpoint.Lease) { a.queueCheckpointFilter(w, r, wb, spec.Sheet, store, lease, false) }
			}
		case exportCheckpointDriver:
			def, err = exportCheckpointResumeDefinition(wb, m)
			if err == nil {
				queue = func(lease *checkpoint.Lease) { a.queueCheckpointExport(w, r, wb, store, lease) }
			}
		default:
			err = checkpoint.ErrBinding
		}
		if err != nil {
			checkpointHTTPError(w, err)
			return
		}
		binding, err := checkpoint.DefinitionHash(def)
		if err != nil || binding != m.DefinitionHash {
			checkpointHTTPError(w, checkpoint.ErrBinding)
			return
		}
		lease, err := store.Acquire(r.Context(), req.ID, binding)
		if err != nil {
			checkpointHTTPError(w, err)
			return
		}
		if lease.Snapshot().Revision != req.Revision {
			_ = lease.Close()
			checkpointHTTPError(w, checkpoint.ErrConflict)
			return
		}
		owned = true
		queue(lease)
		return
	}
	writeError(w, 404, "恢复任务不存在")
}

func (a *App) handleTaskCheckpointDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := readTaskCheckpointRequest(w, r)
	if err != nil {
		writeError(w, 400, err.Error())
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
	if err := store.DiscardRevision(req.ID, req.Revision); err != nil {
		checkpointHTTPError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"discarded": true})
}
