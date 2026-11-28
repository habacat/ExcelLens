package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"unicode/utf8"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type editRequest struct {
	Trial       *whatIfSpec          `json:"trial,omitempty"`
	Document    editdoc.Key          `json:"document"`
	Book        string               `json:"book"`
	Revision    uint64               `json:"revision,string"`
	Sheet       string               `json:"sheet"`
	Pivot       string               `json:"pivot,omitempty"`
	Field       string               `json:"field,omitempty"`
	Search      string               `json:"search,omitempty"`
	Offset      int                  `json:"offset,omitempty"`
	Ref         string               `json:"ref"`
	End         string               `json:"end"`
	Destination string               `json:"destination,omitempty"`
	VisibleRows []int                `json:"visibleRows,omitempty"`
	Alternate   bool                 `json:"alternate,omitempty"`
	Mode        string               `json:"mode"`
	Step        string               `json:"step"`
	SkipBlank   bool                 `json:"skipBlank"`
	Direct      bool                 `json:"direct,omitempty"`
	Clipboard   *editClipboard       `json:"clipboard"`
	Rule        *editdoc.Rule        `json:"rule"`
	Proof       string               `json:"proof"`
	Operation   editdoc.Operation    `json:"operation"`
	Options     editfile.TextOptions `json:"options"`
	Artifact    string               `json:"artifact"`
	Cells       []editdoc.Address    `json:"cells,omitempty"`
}

var errEditRequestCapacity = i18n.Error("service.edits.editingRequestExceedsCapacityReduce")

type editListItem struct {
	editdoc.Info
	Book   string `json:"book"`
	Format string `json:"format"`
	Busy   bool   `json:"busy"`
	Error  string `json:"error,omitempty"`
}

type editSheet struct {
	Name        string `json:"name"`
	FirstRow    int    `json:"firstRow"`
	FirstCol    int    `json:"firstCol"`
	Rows        int    `json:"rows"`
	Cols        int    `json:"cols"`
	ReadOnly    string `json:"readOnly,omitempty"`
	SourceRules bool   `json:"sourceRules,omitempty"`
}

type editState struct {
	Tables       []editfile.TableRange `json:"tables,omitempty"`
	ChartSheets  []string              `json:"chartSheets,omitempty"`
	Pivots       []editfile.PivotInfo  `json:"pivots,omitempty"`
	Book         string                `json:"book"`
	OriginalBook string                `json:"originalBook"`
	Document     editdoc.Info          `json:"document"`
	Format       string                `json:"format"`
	Sheets       []editSheet           `json:"sheets"`
	Encoding     string                `json:"encoding,omitempty"`
	Structured   bool                  `json:"structured,omitempty"`
	Artifacts    []editArtifact        `json:"artifacts"`
}

func editError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, checkpoint.ErrBusy):
		status, err = http.StatusConflict, i18n.Error("service.edits.editingCopyUseTryAgain")
	case errors.Is(err, checkpoint.ErrConflict):
		status, err = http.StatusConflict, i18n.Error("service.edits.editedRevisionChangedTryAgain")
	case errors.Is(err, checkpoint.ErrUncertain):
		status, err = http.StatusServiceUnavailable, i18n.Error("service.edits.submittingConfirmationTemporarilyUnavailableCan")
	case errors.Is(err, checkpoint.ErrCorrupt):
		err = i18n.Error("service.edits.editedCopyConfirmationRecordsDamaged")
	case errors.Is(err, checkpoint.ErrCapacity), errors.Is(err, editdoc.ErrHistory), errors.Is(err, errEditRequestCapacity):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, editfile.ErrBinding), errors.Is(err, checkpoint.ErrBinding):
		status = http.StatusConflict
	case errors.Is(err, os.ErrNotExist):
		status, err = http.StatusNotFound, i18n.Error("service.edits.editedCopyOriginalFileOr")
	case errors.Is(err, context.Canceled):
		status, err = http.StatusRequestTimeout, i18n.Error("service.edits.operationCancelledCanRefreshCheck")
	}
	writeFailure(w, status, err)
}

func (a *App) handleEditList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	store, err := openEditDocuments()
	if err != nil {
		editError(w, err)
		return
	}
	defer store.Close()
	entries, err := listTaskCheckpoints(r.Context(), store)
	if err != nil {
		editError(w, err)
		return
	}
	items := make([]editListItem, 0, len(entries))
	for _, item := range entries {
		out := editListItem{Info: editdoc.Info{Key: editdoc.Key{ID: item.ID}}, Busy: item.Busy, Error: item.Error}
		if item.Manifest != nil {
			entry, err := editdoc.EntryFromManifest(*item.Manifest)
			if err == nil {
				out.Info, out.Book, out.Format = entry.Info, editViewBook(entry.ID, entry.Base), entry.Base.Source.Format
			} else {
				out.Error = "编辑副本版本或确认记录无效"
			}
		}
		if item.Backup {
			out.Error = "编辑副本确认记录损坏"
		}
		items = append(items, out)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.After(items[j].Updated) })
	writeJSON(w, map[string]any{"items": items})
}

func decodeEditRequest(w http.ResponseWriter, r *http.Request) (editRequest, error) {
	var req editRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.EditRequestBytes))
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		return req, errEditRequestCapacity
	}
	if err != nil || !utf8.Valid(data) {
		return req, editdoc.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(json.RawMessage)) != io.EOF {
		return req, editdoc.ErrInvalid
	}
	return req, nil
}

func (a *App) handleEditCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	req, err := decodeEditRequest(w, r)
	if err != nil {
		editError(w, err)
		return
	}
	ctx := r.Context()
	command := r.URL.Path[len("/api/edits/"):]
	if command == "base" {
		a.handleEditBase(w, r, req)
		return
	}
	if command == "open" {
		a.handleEditOpen(w, r, req)
		return
	}
	if command == "save" {
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-ctx.Done():
			editError(w, ctx.Err())
			return
		}
	} else {
		release, err := a.acquireQuerySlot(ctx)
		if err != nil {
			editError(w, err)
			return
		}
		defer release()
	}
	unlock := a.lockCacheRead()
	defer unlock()
	if command == "list-areas" {
		// Immutable reader metadata uses its own cache and never holds the
		// document lease while scanning a large worksheet.
		areas, err := a.editListAreas(ctx, req)
		if err != nil {
			editError(w, err)
		} else {
			writeJSON(w, areas)
		}
		return
	}
	store, err := openEditDocuments()
	if err != nil {
		editError(w, err)
		return
	}
	defer store.Close()
	if command == "start" {
		var key editdoc.Key
		key, err = a.startEdit(ctx, store, req.Book)
		if err != nil {
			editError(w, err)
			return
		}
		req.Document, command = key, "state"
	}
	doc, err := retryCheckpointBusy(ctx, func() (*editdoc.Document, error) { return editdoc.Load(ctx, store, req.Document) })
	if err != nil {
		editError(w, err)
		return
	}
	defer doc.Close()
	layout, err := editfile.ParseLayout(doc.Base.Layout, doc.Base.Source.Format)
	// Save can return an existing artifact, and commit can return a confirmed
	// request receipt. Neither requires reading the original workbook again.
	if err == nil && command != "save" && command != "commit" {
		layout, err = a.edits.documentEditLayout(ctx, doc, layout, nil)
		if command == "state" && (errors.Is(err, os.ErrNotExist) || errors.Is(err, editfile.ErrBinding)) {
			err = nil // Previously generated downloads are still available.
		}
	}
	if err != nil {
		editError(w, err)
		return
	}
	layout = a.edits.withSourceCells(layout, doc)
	var body any
	switch command {
	case "state":
		body, err = a.editState(doc, layout)
	case "read":
		body, err = a.editRead(ctx, doc, layout, req)
	case "what-if":
		body, err = a.editWhatIf(ctx, doc, layout, req)
	case "grid":
		body, err = a.editGrid(ctx, doc, layout, req)
	case "input-kinds":
		body, err = editInputKinds(ctx, doc, layout, req)
	case "native-paste":
		body, err = a.nativePaste(ctx, doc, req)
	case "formula-symbols":
		if doc.Revision != req.Revision {
			err = checkpoint.ErrConflict
			break
		}
		if layout.XLSX == nil {
			err = editfile.ErrTarget
			break
		}
		var f *workbook.EditSourceFile
		if f, err = openEditBase(ctx, doc); err == nil {
			body, err = layout.XLSX.FormulaSymbols(ctx, f)
			f.Close()
		}
	case "choices":
		body, err = editChoices(ctx, doc, layout, req)
	case "pivot-items", "pivot-layout":
		body, err = a.editPivotFields(ctx, doc, layout, req, command == "pivot-items")
	case "copy":
		body, err = a.copyEditCells(ctx, doc, layout, req)
	case "preview":
		body, err = a.previewEditCells(ctx, doc, layout, req)
	case "commit":
		body, err = a.editCommit(ctx, doc, layout, req)
	case "save":
		body, err = a.editSave(ctx, doc, layout, req)
	default:
		err = editdoc.ErrInvalid
	}
	if req.Direct && errors.Is(err, editdoc.ErrNoChange) {
		switch command {
		case "commit":
			if req.Operation.Action == "change" {
				body, err = editCommitResult{doc.Info, doc.Revision, false, editViewBook(doc.ID, doc.Base)}, nil
			}
		case "preview":
			body, err = editBatchPreview{}, nil
		}
	}
	if err != nil {
		editError(w, err)
		return
	}
	unlock()
	if err := doc.Close(); err != nil {
		editError(w, err)
		return
	}
	writeJSON(w, body)
}

func (a *App) registerEditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/edits", a.handleEditList)
	for _, path := range []string{"start", "state", "base", "grid", "input-kinds", "formula-symbols", "native-paste", "what-if", "list-areas", "read", "choices", "pivot-items", "pivot-layout", "copy", "preview", "commit", "save", "open"} {
		mux.HandleFunc("/api/edits/"+path, a.handleEditCommand)
	}
	mux.HandleFunc("/api/edits/download", a.handleEditDownload)
}
