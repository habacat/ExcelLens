package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// Each tab has one verified layout and original-cell cache. Calculation
// revisions are immutable after publication and shared across the process.
type editController struct {
	mu       sync.RWMutex
	binding  string
	checked  editfile.XLSXLayout
	cells    *editfile.SourceCellCache
	revision *atomic.Pointer[editCalculationCache]
}

func newEditController() editController {
	return editController{revision: new(atomic.Pointer[editCalculationCache])}
}

func (e *editController) releaseBinding() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checked, e.binding, e.cells = editfile.XLSXLayout{}, "", nil
}

func (e *editController) rememberLayout(binding string, checked editfile.XLSXLayout) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checked, e.binding = checked, binding
}

func (e *editController) withSourceCells(layout editfile.Layout, doc *editdoc.Document) editfile.Layout {
	var index *editfile.CalculationBook
	if entry := e.revision.Load(); entry != nil && entry.stamp == doc.Base.Source.Stamp {
		index = entry.book
	}
	e.mu.Lock()
	if e.cells == nil {
		e.cells = new(editfile.SourceCellCache)
	}
	cells := e.cells
	e.mu.Unlock()
	return layout.WithSourceCells(cells, doc.Binding+"/"+doc.Base.Source.Stamp, index)
}

func editDirectory(subdir string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", i18n.Error("service.editSession.unableLocateEditingCopyDirectory")
	}
	return filepath.Join(base, "ExcelLens", subdir), nil
}

func openEditDocuments() (*checkpoint.Store, error) {
	dir, err := editDirectory("edit-documents")
	if err != nil {
		return nil, err
	}
	return checkpoint.Open(dir)
}

func openEditOutputs() (*editfile.Outputs, error) {
	dir, err := editDirectory("edit-files")
	if err != nil {
		return nil, err
	}
	return editfile.OpenOutputs(dir)
}

func (e *editController) documentEditLayout(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, f *workbook.EditSourceFile) (editfile.Layout, error) {
	if layout.XLSX == nil {
		return layout, nil
	}
	e.mu.RLock()
	cached, key := e.checked, e.binding
	e.mu.RUnlock()
	binding := doc.Binding + "/" + doc.Base.Source.Stamp
	if key == binding && cached.GridEditing {
		layout.XLSX = &cached
		return layout, nil
	}
	if layout.XLSX.GridEditing && (len(layout.XLSX.Pivots) == 0 || layout.XLSX.PivotAxesReady && layout.XLSX.PivotMeasuresReady && layout.XLSX.PivotDatesReady) && (doc.Semantics >= editdoc.GridLayoutSemantics || doc.Structures > 0) {
		return layout, nil
	}
	if f == nil {
		var err error
		f, err = openEditBase(ctx, doc)
		if err != nil {
			return layout, err
		}
		defer f.Close()
	}
	updated, err := editfile.UpgradeGridLayout(ctx, f, *layout.XLSX)
	if err != nil {
		return layout, err
	}
	e.rememberLayout(binding, updated)
	layout.XLSX = &updated
	return layout, nil
}

func (a *App) startEdit(ctx context.Context, store *checkpoint.Store, book string) (editdoc.Key, error) {
	wb := a.currentWorkbook()
	if wb == nil || book != sourceBookID(wb) {
		return editdoc.Key{}, checkpoint.ErrBinding
	}
	format := formats.EditExtension(filepath.Ext(wb.Path))
	if format == "" || (wb.Format != format && !(wb.Format == "xlsx" && formats.EditOOXML(format))) || wb.SourceInfo.Incomplete || wb.ArchiveOrigin != nil {
		return editdoc.Key{}, i18n.Error("service.editSession.currentSourceDoesNotSupport")
	}
	entries, err := listTaskCheckpoints(ctx, store)
	if err != nil {
		return editdoc.Key{}, err
	}
	// Reloading the page can reopen the active immutable structure snapshot.
	// Resolve that path back to its own journal before considering a new draft.
	revisions, err := editDirectory("edit-revisions")
	if err != nil {
		return editdoc.Key{}, err
	}
	dir := filepath.Dir(wb.Path)
	if filepath.Clean(filepath.Dir(dir)) == filepath.Clean(revisions) && editdoc.ValidRequestID(filepath.Base(dir)) {
		for _, e := range entries {
			if e.ID != filepath.Base(dir) || e.Manifest == nil {
				continue
			}
			entry, err := editdoc.EntryFromManifest(*e.Manifest)
			if err != nil || e.Backup {
				return editdoc.Key{}, checkpoint.ErrCorrupt
			}
			return entry.Key, nil
		}
		return editdoc.Key{}, checkpoint.ErrBinding
	}
	var latest editdoc.Entry
	for _, e := range entries {
		if e.Manifest == nil {
			continue
		}
		entry, err := editdoc.EntryFromManifest(*e.Manifest)
		if err == nil && editViewBook(entry.ID, entry.Base) == book && entry.Updated.After(latest.Updated) {
			if e.Backup {
				return editdoc.Key{}, checkpoint.ErrCorrupt
			}
			latest = entry
		}
	}
	if latest.ID != "" {
		return latest.Key, nil
	}
	layout, proof, err := editfile.Inspect(ctx, wb, format)
	if err != nil {
		return editdoc.Key{}, err
	}
	data, err := json.Marshal(layout)
	if err != nil {
		return editdoc.Key{}, err
	}
	path, err := filepath.Abs(wb.Path)
	if err != nil {
		return editdoc.Key{}, err
	}
	if a.currentWorkbook() != wb {
		return editdoc.Key{}, checkpoint.ErrConflict
	}
	base := editdoc.Base{Source: checkpoint.Source{Path: path, Book: book, Fingerprint: wb.Fingerprint, Format: format, Source: format, Stamp: proof}, Name: workbookDisplayName(wb), Layout: data}
	entry, err := retryCheckpointBusy(ctx, func() (editdoc.Entry, error) { return editdoc.Create(ctx, store, base) })
	if err == nil && layout.XLSX != nil {
		a.edits.rememberLayout(entry.Binding+"/"+proof, *layout.XLSX)
	}
	return entry.Key, err
}

func (a *App) editState(doc *editdoc.Document, layout editfile.Layout) (editState, error) {
	out := editState{Document: doc.Info, Book: editViewBook(doc.ID, doc.Base), OriginalBook: editViewBook(doc.ID, doc.OriginalBase()), Format: doc.Base.Source.Format, Artifacts: []editArtifact{}}
	if layout.Text != nil {
		l := layout.Text
		out.Encoding = l.Encoding
		out.Sheets = []editSheet{{Name: l.Sheet, FirstRow: 1, FirstCol: 1, Rows: l.Rows, Cols: l.Columns}}
	} else {
		out.Tables = layout.XLSX.TableRanges()
		out.Structured = layout.XLSX.Structured
		out.ChartSheets, out.Pivots = layout.XLSX.ChartSheets, layout.XLSX.Pivots
		for _, s := range layout.XLSX.Sheets {
			out.Sheets = append(out.Sheets, editSheet{Name: s.Name, FirstRow: 1, FirstCol: 1, Rows: formats.ExcelRows, Cols: formats.ExcelColumns, ReadOnly: s.ReadOnly, SourceRules: s.Rules > 0 || layout.XLSX.Native})
		}
	}
	outputs, err := openEditOutputs()
	if err != nil {
		return out, err
	}
	defer outputs.Close()
	items, err := outputs.List(doc.ID)
	if err != nil {
		return out, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Revision != items[j].Revision {
			return items[i].Revision > items[j].Revision
		}
		return items[i].Key < items[j].Key
	})
	for _, item := range items {
		out.Artifacts = append(out.Artifacts, a.editArtifact(doc.ID, item))
	}
	return out, nil
}

func openEditBase(ctx context.Context, doc *editdoc.Document) (*workbook.EditSourceFile, error) {
	origin := doc.OriginalBase()
	if origin.Source.Book != doc.Base.Source.Book {
		file, err := openVerifiedEditSource(ctx, doc.ID, origin)
		if err != nil {
			return nil, err
		}
		_ = file.Close()
	}
	return openVerifiedEditSource(ctx, doc.ID, doc.Base)
}

func openVerifiedEditSource(ctx context.Context, id string, base editdoc.Base) (*workbook.EditSourceFile, error) {
	source, err := editLocation(id, base)
	if err != nil {
		return nil, err
	}
	f, err := workbook.OpenEditSource(source.Path)
	if err != nil {
		return nil, err
	}
	if err := editfile.VerifySource(ctx, f, base.Source.Stamp); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// Browser uploads used as durable edit bases must survive temporary-file cleanup.
func editBasePaths() ([]string, error) {
	dir, err := editDirectory("edit-documents")
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	store, err := checkpoint.Open(dir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	items, err := listTaskCheckpoints(ctx, store)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.Manifest == nil {
			return nil, i18n.Error("service.editSession.editedCopySourceCannotBe")
		}
		entry, err := editdoc.EntryFromManifest(*item.Manifest)
		if err != nil {
			return nil, err
		}
		source, err := editLocation(entry.ID, entry.Base)
		if err != nil {
			return nil, err
		}
		paths = append(paths, source.Path, entry.Base.Source.Path)
		paths = append(paths, restoredUploadCandidate(entry.Base.Source.Path))
	}
	return paths, nil
}
