package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

// PrintSource is a joint immutable source binding. Neither settings nor geometry
// from another Workbook/index can be supplied to it. Server tasks must retain
// their ordinary Workbook pin while using it, as with other source snapshots.
type PrintSource struct {
	book                                *Workbook
	index                               *SheetIndex
	sheet                               SheetMeta
	path, fingerprint, format, identity string
	settings                            *SourcePrintIndex
	geometry                            *SourcePrintGeometryIndex
	used                                CellMerge
}

func (w *Workbook) ReadPrintSource(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (*PrintSource, error) {
	source := &PrintSource{book: w, index: idx, sheet: meta, path: w.Path, fingerprint: w.Fingerprint, format: w.Format}
	if err := source.Validate(ctx); err != nil {
		return nil, err
	}
	settings, err := w.SourcePrintSettings(ctx, meta, idx, progress)
	if err != nil {
		return nil, err
	}
	geometry, err := w.SourcePrintGeometry(ctx, meta, idx, progress)
	if err != nil {
		return nil, err
	}
	if err := source.Validate(ctx); err != nil {
		return nil, err
	}
	path := filepath.Clean(w.Path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	digest := sha256.Sum256([]byte(w.Fingerprint + "\x00" + path + "\x00" + meta.Path))
	source.identity = hex.EncodeToString(digest[:])
	source.settings, source.geometry = settings, geometry
	if idx.RowCount() > 0 && idx.MaxRow > 0 && idx.MaxCol > 0 {
		source.used = CellMerge{Row: 1, Col: 1, EndRow: idx.MaxRow, EndCol: idx.MaxCol}
	}
	return source, nil
}

func (source *PrintSource) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == nil || source.book == nil || source.index == nil {
		return errors.New("打印来源或已准备索引缺失")
	}
	w := source.book
	actual, found := w.FindSheet(source.sheet.Name)
	if w.Path != source.path || w.Fingerprint != source.fingerprint || w.Format != source.format || !found || actual.Path != source.sheet.Path {
		return errors.New("打印来源身份发生变化，请重新准备预览")
	}
	w.indexMu.RLock()
	prepared := w.indexes[source.sheet.Path]
	w.indexMu.RUnlock()
	if prepared != source.index || source.index.SheetPath != source.sheet.Path {
		return errors.New("打印来源的精确索引已变化，请重新准备预览")
	}
	return w.CheckSourceUnchanged()
}

func (source *PrintSource) Identity() string                    { return source.identity }
func (source *PrintSource) Sheet() string                       { return source.sheet.Name }
func (source *PrintSource) Format() string                      { return source.format }
func (source *PrintSource) UsedRange() CellMerge                { return source.used }
func (source *PrintSource) Settings() SourcePrintMetadata       { return source.settings.Snapshot() }
func (source *PrintSource) Geometry() *SourcePrintGeometryIndex { return source.geometry }

// Retained plans can outlive a metadata LRU entry. Their owners budget the
// bound settings and geometry too, even when several plans share them.
func (source *PrintSource) BudgetBytes() int64 {
	return source.settings.BudgetBytes() + source.geometry.BudgetBytes()
}

// OOXML's explicit print scan verifies the complete merge declarations too.
// BIFF layout reading already rejects invalid or over-capacity merge records.
func (source *PrintSource) MergeCount() (int, bool) {
	return source.settings.merges, source.book.source == nil
}
