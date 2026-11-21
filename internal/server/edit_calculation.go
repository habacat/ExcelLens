package server

import (
	"context"
	"errors"
	"sync"

	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

// At most one immutable source index and one revision are retained across
// browser sessions. The document lease serializes reads of the same draft.
type editCalculationCache struct {
	stamp                 string
	book                  *editfile.CalculationBook
	document              editdoc.Key
	revision              uint64
	view                  *editfile.Calculation
	patches               map[editdoc.Address]editdoc.Value
	indexErr              error
	pivotMu               sync.Mutex
	pivotPart, pivotField string
	pivotItems            *editfile.PivotItemList
}

func (e *editController) calculationFor(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, f *workbook.EditSourceFile) (*editfile.Calculation, error) {
	entry, err := e.calculationRevision(ctx, doc, layout, f, false)
	if err != nil {
		return nil, err
	}
	return entry.view, nil
}

// A reader keeps the source index and the projection that was calculated from it
// together, even if another tab replaces the application's most recent revision.
func (e *editController) calculationRevision(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, f *workbook.EditSourceFile, needIndex bool) (*editCalculationCache, error) {
	if layout.XLSX == nil {
		return &editCalculationCache{view: &editfile.Calculation{}}, nil
	}
	entry := e.revision.Load()
	stamp := doc.Base.Source.Stamp
	if entry != nil && entry.stamp == stamp && entry.document == doc.Key && entry.revision == doc.Revision {
		return entry, nil
	}
	patches := doc.Snapshot()
	hasFormulas := layout.XLSX.Native
	for _, sheet := range layout.XLSX.Sheets {
		hasFormulas = hasFormulas || sheet.Formulas > 0
	}
	for _, patch := range patches {
		hasFormulas = hasFormulas || patch.Kind == "formula"
	}
	if !needIndex && !hasFormulas && len(layout.XLSX.Generated) == 0 {
		return &editCalculationCache{view: &editfile.Calculation{}}, nil
	}
	if entry == nil || entry.stamp != stamp {
		book, err := editfile.NewCalculationBook(ctx, f, *layout.XLSX)
		if err != nil && !errors.Is(err, editfile.ErrCalcCapacity) && !errors.Is(err, editfile.ErrCalcSettings) {
			return nil, err
		}
		entry = &editCalculationCache{stamp: stamp, book: book, indexErr: err}
	}
	view := &editfile.Calculation{}
	if entry.book != nil {
		var err error
		var previous *editfile.Calculation
		if entry.document == doc.Key {
			previous = entry.view
		}
		view, err = entry.book.Calculate(ctx, patches, previous)
		if err != nil && !errors.Is(err, editfile.ErrCalcCapacity) {
			return nil, err
		}
	}
	if entry.book == nil || view == nil {
		// No partial result is published when a revision exceeds capacity.
		// This also covers sources that explicitly request a full rebuild.
		view = &editfile.Calculation{Affected: map[string]bool{}}
		for _, sheet := range layout.XLSX.Sheets {
			if !layout.XLSX.Native || layout.XLSX.RecalcOnOpen || len(patches) > 0 {
				view.Affected[sheet.Name] = true
			}
		}
		changed := layout.XLSX.Affected(doc.ChangedSheets())
		for _, area := range layout.XLSX.Generated {
			if changed[area.Input] {
				view.Generated = append(view.Generated, area)
			}
		}
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	entry = &editCalculationCache{stamp: stamp, book: entry.book, indexErr: entry.indexErr, document: doc.Key, revision: doc.Revision, view: view, patches: patches}
	e.revision.Store(entry)
	return entry, nil
}

func (e *editCalculationCache) pivotPage(ctx context.Context, info editfile.PivotInfo, field, query string, offset int) (editfile.PivotChoices, error) {
	e.pivotMu.Lock()
	defer e.pivotMu.Unlock()
	if e.pivotItems == nil || e.pivotPart != info.Part || e.pivotField != field {
		items, err := e.book.NewPivotItemList(e.view, e.patches, info, field)
		if err != nil {
			return editfile.PivotChoices{}, err
		}
		e.pivotPart, e.pivotField, e.pivotItems = info.Part, field, items
	}
	return e.pivotItems.Page(ctx, query, offset)
}
