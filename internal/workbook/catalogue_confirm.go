package workbook

import (
	"context"
	"errors"

	"excellens/internal/formats"
)

// ConfirmCatalogue verifies actual worksheet declarations before permitting
// table or structured-name navigation. Callers explicitly prepare the ordinary
// source index and own query cancellation/progress; listing never builds it.
func (w *Workbook) ConfirmCatalogue(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (Catalogue, error) {
	if err := ctx.Err(); err != nil {
		return Catalogue{}, err
	}
	capability, ok := formats.Lookup(w.Format)
	actual, found := w.FindSheet(meta.Name)
	if !ok || !capability.CatalogueMetadata || (w.source != nil && w.Format != "xls") || !found || actual.Path != meta.Path {
		return Catalogue{}, errors.New("此来源尚未支持按工作表确认表对象")
	}
	if idx == nil || idx.SheetPath != meta.Path || w.GetCachedIndex(meta.Path) != idx {
		return Catalogue{}, errors.New("请先准备同一来源工作表的索引，再确认表对象")
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return Catalogue{}, err
	}
	if w.source != nil {
		entry, err := w.loadCatalogueEntry(ctx, meta, func() (worksheetCatalogueEntry, error) {
			return w.loadBIFFCatalogue(ctx, meta, progress)
		})
		if err != nil {
			return Catalogue{}, err
		}
		if err := w.CheckSourceUnchanged(); err != nil {
			return Catalogue{}, err
		}
		return w.readBIFFCatalogue(ctx, meta.Path, entry.biff)
	}
	value, err := w.worksheetDeclarations(ctx, meta, idx, progress)
	if err != nil {
		return Catalogue{}, err
	}
	result, err := w.ReadCatalogue(ctx)
	if err != nil {
		return Catalogue{}, err
	}
	applyWorksheetDeclarations(&result, meta, value)
	return w.resolveCatalogue(ctx, result)
}

func applyWorksheetDeclarations(value *Catalogue, meta SheetMeta, declarations worksheetCatalogueDeclarations) {
	declared := make(map[string]bool, len(declarations.IDs))
	matched := make(map[string]bool, len(declarations.IDs))
	for _, id := range declarations.IDs {
		declared[id] = true
	}
	for i := range value.Items {
		item := &value.Items[i]
		if item.Kind != "table" || item.Sheet != meta.Name || item.Table == nil {
			continue
		}
		item.Table.Verified = false
		if !declared[item.Table.RelationID] {
			item.Status, item.Reason, item.Areas = "invalid", "此关系未出现在工作表的真实表声明中，不能定位", nil
		} else {
			matched[item.Table.RelationID] = true
			if item.Status != "invalid" && item.Status != "unavailable" {
				item.Table.Verified = true
			}
		}
	}
	if len(matched) != len(declared) {
		value.Complete = false
		if len(value.Notices) < 32 {
			value.Notices = append(value.Notices, "部分表声明缺少可读取的表关系或部件")
		}
	}
}

func (w *Workbook) applyKnownWorksheetDeclarations(value *Catalogue, reader *catalogueReader) {
	for _, meta := range w.Sheets {
		if reader.ctx.Err() != nil {
			return
		}
		declarations, found := w.cachedWorksheetCatalogue(meta)
		if !found {
			// Reopening an already prepared index must not require another
			// explicit confirmation when its small source-bound proof is valid.
			// Inspect only indexes already loaded by the owner; listing never
			// opens, maps or builds another worksheet's value index/body.
			w.indexMu.RLock()
			idx := w.indexes[meta.Path]
			w.indexMu.RUnlock()
			part := reader.entries[meta.Path]
			if idx != nil && part != nil && idx.XMLSize >= 0 && uint64(idx.XMLSize) == part.UncompressedSize64 {
				declarations, found = w.loadWorksheetCatalogueCache(meta, idx, part.CRC32)
				if found {
					w.cacheWorksheetCatalogue(meta, declarations)
				}
			}
		}
		if found {
			applyWorksheetDeclarations(value, meta, declarations)
		}
	}
}
