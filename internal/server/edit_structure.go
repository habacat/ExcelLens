package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

func (a *App) editPivotFields(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest, items bool) (any, error) {
	if req.Revision != doc.Revision {
		return nil, checkpoint.ErrConflict
	}
	if layout.XLSX == nil {
		return nil, editfile.ErrTarget
	}
	i := slices.IndexFunc(layout.XLSX.Pivots, func(p editfile.PivotInfo) bool { return p.Sheet == req.Sheet && p.Name == req.Pivot })
	if i < 0 {
		return nil, editfile.ErrTarget
	}
	file, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entry, err := a.calculationRevision(ctx, doc, layout, file, true)
	if err != nil {
		return nil, err
	}
	if entry.indexErr != nil {
		return nil, entry.indexErr
	}
	info, err := entry.book.CurrentPivotInfo(entry.view, entry.patches, layout.XLSX.Pivots[i])
	if err != nil {
		return nil, err
	}
	if !items {
		return info, file.Check()
	}
	result, err := entry.pivotPage(ctx, info, req.Field, req.Search, req.Offset)
	if err != nil {
		return nil, err
	}
	return result, file.Check()
}

func commitEditStructure(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, op editdoc.Operation) (uint64, error) {
	return doc.CommitStructure(ctx, op, func(change editdoc.Structure) (editdoc.Base, error) {
		f, err := openEditBase(ctx, doc)
		if err != nil {
			return editdoc.Base{}, err
		}
		defer f.Close()
		return verifiedRevision(ctx, doc, f, func(dir string) (string, error) {
			return editfile.MaterializeStructure(ctx, dir, doc, layout, f, change)
		})
	})
}

func verifiedRevision(ctx context.Context, doc *editdoc.Document, f *workbook.EditSourceFile, write func(string) (string, error)) (editdoc.Base, error) {
	dir, err := editDirectory("edit-revisions")
	if err != nil {
		return editdoc.Base{}, err
	}
	path, err := write(filepath.Join(dir, doc.ID))
	if err != nil {
		return editdoc.Base{}, err
	}
	base, err := inspectEditSnapshot(ctx, path, doc.Base)
	if err == nil {
		err = f.Check()
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return base, err
}

func inspectEditSnapshot(ctx context.Context, path string, previous editdoc.Base) (editdoc.Base, error) {
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		return editdoc.Base{}, err
	}
	layout, proof, err := editfile.Inspect(ctx, wb, previous.Source.Format)
	if err != nil {
		return editdoc.Base{}, err
	}
	base := previous
	base.Layout, err = json.Marshal(layout)
	base.Source.Path, base.Source.Book, base.Source.Fingerprint, base.Source.Stamp = path, sourceBookID(wb), wb.Fingerprint, proof
	return base, err
}
