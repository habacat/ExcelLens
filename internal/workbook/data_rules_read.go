package workbook

import (
	"context"
	"errors"
	"io"

	"excellens/internal/formats"
)

type dataRuleCacheEntry = sheetMetadataEntry[SourceDataRuleIndex]
type dataRuleLoad = sheetMetadataLoad[SourceDataRuleIndex]

func (w *Workbook) dataRuleCacheState() sheetMetadataState[SourceDataRuleIndex] {
	return sheetMetadataState[SourceDataRuleIndex]{&w.dataRuleMu, &w.dataRuleCache, &w.dataRuleLoading, &w.dataRuleClock, &w.dataRuleBytes}
}

// SourceDataRules is opt-in metadata IO. Ordinary opening, indexing, values and
// scrolling never invoke it. Only the caller's already prepared source index
// is accepted; neither another sheet nor another Workbook is prepared here.
func (w *Workbook) SourceDataRules(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (*SourceDataRuleIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := formats.Lookup(w.Format)
	actual, found := w.FindSheet(meta.Name)
	if !ok || !source.DataRuleMetadata || !found || actual.Path != meta.Path {
		return nil, errors.New("此来源不支持读取数据规则，或工作表身份不匹配")
	}
	w.indexMu.RLock()
	prepared := w.indexes[meta.Path]
	w.indexMu.RUnlock()
	if idx == nil || idx != prepared || idx.SheetPath != meta.Path {
		return nil, errors.New("请先准备同一来源工作表的索引，再读取数据规则")
	}
	return readSheetMetadata(ctx, meta.Path, w.dataRuleCacheState(), sheetMetadataLimits{formats.DataRuleCacheBytes, formats.DataRuleCacheSheets}, (*SourceDataRuleIndex).BudgetBytes, w.CheckSourceUnchanged, func() (*SourceDataRuleIndex, error) {
		return w.loadDataRules(ctx, meta, idx, source, progress)
	})
}

func (w *Workbook) cacheDataRulesLocked(key string, index *SourceDataRuleIndex) {
	cacheSheetMetadataLocked(key, index, w.dataRuleCacheState(), sheetMetadataLimits{formats.DataRuleCacheBytes, formats.DataRuleCacheSheets}, (*SourceDataRuleIndex).BudgetBytes)
}

func (w *Workbook) loadDataRules(ctx context.Context, meta SheetMeta, idx *SheetIndex, source formats.Source, progress func(int64, int64, string)) (*SourceDataRuleIndex, error) {
	b := newDataRuleBuilder(source)
	if w.Format == "xlsb" {
		if err := w.xlsbDataRules(ctx, meta, b); err != nil {
			return nil, err
		}
		return b.finish(), nil
	}
	if w.source != nil {
		if err := w.readBIFFDataRules(ctx, meta, b, progress); err != nil {
			return nil, err
		}
		return b.finish(), nil
	}
	if err := w.readVerifiedSheetXML(ctx, meta, idx, func(input io.Reader, total int64) error {
		return scanDataRuleXML(ctx, input, total, b, progress)
	}); err != nil {
		return nil, err
	}
	return b.finish(), nil
}
