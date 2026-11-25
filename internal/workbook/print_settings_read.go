package workbook

import (
	"context"
	"errors"
	"io"
	"path"

	"excellens/internal/formats"
)

// SourcePrintSettings reads only explicit metadata from this prepared source
// sheet. Opening/indexing/scrolling do not call it. It never schedules printing,
// executes a header/name expression or contacts a printer settings relationship.
func (w *Workbook) SourcePrintSettings(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (*SourcePrintIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := formats.Lookup(w.Format)
	actual, found := w.FindSheet(meta.Name)
	if !ok || !source.PrintMetadata || !found || actual.Path != meta.Path {
		return nil, errors.New("此来源不支持读取源打印设置，或工作表身份不匹配")
	}
	w.indexMu.RLock()
	prepared := w.indexes[meta.Path]
	w.indexMu.RUnlock()
	if idx == nil || idx != prepared || idx.SheetPath != meta.Path {
		return nil, errors.New("请先准备同一来源工作表索引，再读取打印设置")
	}
	state := sheetMetadataState[SourcePrintIndex]{&w.printMu, &w.printCache, &w.printLoading, &w.printClock, &w.printBytes}
	return readSheetMetadata(ctx, meta.Path, state, sheetMetadataLimits{formats.PrintCacheBytes, formats.PrintCacheSheets}, (*SourcePrintIndex).BudgetBytes, w.CheckSourceUnchanged, func() (*SourcePrintIndex, error) {
		b := newPrintMetadataBuilder(meta.Name, w.Format)
		if w.Format == "xlsb" {
			if err := w.readXLSBPrintSettings(ctx, meta, b); err != nil {
				return nil, err
			}
		} else if w.source != nil {
			if err := w.readPrintNames(ctx, meta, nil, b); err != nil {
				return nil, err
			}
			if err := w.readBIFFPrintSettings(ctx, meta, b, progress); err != nil {
				return nil, err
			}
		} else {
			reader, err := w.openCatalogueReader(ctx)
			if err != nil {
				return nil, err
			}
			defer reader.zip.Close()
			if err := w.readPrintNames(ctx, meta, reader, b); err != nil {
				return nil, err
			}
			if err := reader.readVerifiedSheetXML(meta, idx, func(input io.Reader, total int64) error {
				return scanPrintSettingsXML(ctx, input, total, b, progress)
			}); err != nil {
				return nil, err
			}
			if err := readPrintReferences(meta, reader, b); err != nil {
				return nil, err
			}
		}
		return b.finish()
	})
}

func readPrintReferences(meta SheetMeta, reader *catalogueReader, b *printMetadataBuilder) error {
	if len(b.value.References) == 0 {
		return nil
	}
	part := path.Join(path.Dir(meta.Path), "_rels", path.Base(meta.Path)+".rels")
	var relations []relationship
	var err error
	if reader.entries[part] == nil {
		err = errors.New("打印设置关系部件不存在")
	} else {
		var data []byte
		data, err = reader.part(part)
		if err == nil {
			relations, err = catalogueRelationshipsXML(data)
		}
	}
	if reader.ctx.Err() != nil {
		return reader.ctx.Err()
	}
	for at := range b.value.References {
		ref := &b.value.References[at]
		ref.Status, ref.Reason = "unresolved", "打印设置关系未完整读取"
		if err != nil {
			ref.Reason = err.Error()
		} else {
			for _, rel := range relations {
				if rel.ID != ref.ID {
					continue
				}
				ref.Target, ref.TargetMode = rel.Target, rel.TargetMode
				kind := "printerSettings"
				if ref.Kind == "headerFooterDrawing" {
					kind = "vmlDrawing"
				}
				if !catalogueRelationType(rel.Type, kind) || !catalogueInternalRelation(rel) {
					ref.Reason = "外部或未知打印设置关系未访问"
				} else if target, targetErr := catalogueRelationTarget(meta.Path, rel.Target); targetErr != nil {
					ref.Reason = targetErr.Error()
				} else if reader.entries[target] == nil {
					ref.Reason = "打印设置关系的内部目标部件不存在"
				} else {
					ref.Status, ref.Reason = "referenced", "仅确认内部目标存在，未读取或执行打印机配置/页眉图形"
				}
				break
			}
		}
		b.notice(ref.Reason)
	}
	return nil
}
