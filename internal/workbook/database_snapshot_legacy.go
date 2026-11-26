package workbook

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"slices"

	"excellens/internal/formats"
)

// RetainLegacyDatabaseSnapshot preserves the older bytes as acquired for this
// save. Versions before v2 had no acquisition digest or column type metadata.
func (w *Workbook) RetainLegacyDatabaseSnapshot(ctx context.Context) (*Workbook, error) {
	if w.Format != "db-online" || w.source.database.manifest.Version != 1 {
		return nil, errors.New("当前工作簿没有待保存的旧版数据库快照")
	}
	if err := w.checkLegacyDatabaseManifest(); err != nil {
		return nil, err
	}
	saved, err := ListDatabaseSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range saved {
		if item.LegacyKey == w.Fingerprint {
			return OpenDatabaseSnapshot(ctx, DatabaseSnapshotPath(item.Key))
		}
	}
	meta := w.source.database.manifest
	meta.Version, meta.Nonce, meta.LegacyKey, meta.Consistency = 2, rand.Text(), w.Fingerprint, "unknown"
	meta.Tables = slices.Clone(meta.Tables)
	snapshot, err := startDatabaseSnapshot(meta)
	if err != nil {
		return nil, err
	}
	defer snapshot.Discard()
	for i, sheet := range w.Sheets {
		sourceRows, sourceIndex := w.cachePaths(sheet.Path)
		rows, index := snapshot.workbook.cachePaths(sheet.Path)
		rowsPart, err := copyDatabasePart(ctx, sourceRows, rows, formats.DatabaseSnapshotBytes-snapshot.bytes)
		if err != nil {
			return nil, err
		}
		snapshot.bytes += rowsPart.Bytes
		indexPart, err := copyDatabasePart(ctx, sourceIndex, index, formats.DatabaseSnapshotBytes-snapshot.bytes)
		if err != nil {
			return nil, err
		}
		snapshot.bytes += indexPart.Bytes
		def, records, err := snapshot.workbook.legacyDatabaseDef(ctx, rows, index, sheet.Path)
		if err != nil {
			return nil, err
		}
		snapshot.manifest.Tables[i].Evidence = &databaseTableEvidence{DatabaseTableDefinition: def,
			Records: records, Rows: rowsPart, Index: indexPart}
		snapshot.next++
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := w.checkLegacyDatabaseManifest(); err != nil {
		return nil, err
	}
	return snapshot.Commit(ctx)
}

func copyDatabasePart(ctx context.Context, src, dest string, remaining int64) (databasePart, error) {
	st, err := os.Lstat(src)
	if err != nil {
		return databasePart{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > remaining {
		return databasePart{}, errors.New("旧版快照文件无效或超过 8 GB 容量")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return databasePart{}, err
	}
	defer f.Close()
	part, err := readDatabasePart(ctx, src, st, f)
	if err != nil {
		return databasePart{}, err
	}
	return part, errors.Join(f.Sync(), f.Close())
}

func (w *Workbook) legacyDatabaseDef(ctx context.Context, rows, index, path string) (DatabaseTableDefinition, int, error) {
	def := DatabaseTableDefinition{Query: "SELECT *", Ordering: "unspecified"}
	idx, err := loadIndex(index, rows, path)
	if err != nil {
		return def, 0, err
	}
	if idx.RowIndex.RowsMapped() {
		// Nobody else holds this provisional read-only mapping. Release it
		// before publishing the staging directory, including on Windows.
		defer idx.RowIndex.ReleaseMapping()
	}
	if err := ctx.Err(); err != nil {
		return def, 0, err
	}
	f, err := os.Open(rows)
	if err != nil {
		return def, 0, err
	}
	defer f.Close()
	var buf []byte
	header, err := w.readExternalRow(f, idx, 0, 1, idx.MaxCol, nil, RowReadOptions{}, &buf)
	if err != nil || header.Number != 1 || len(header.Cells) != idx.MaxCol || idx.RowCount() != idx.MaxRow {
		return def, 0, errors.New("旧版快照缺少完整的字段或行索引")
	}
	def.Columns = make([]DatabaseColumn, len(header.Cells))
	for i, c := range header.Cells {
		def.Columns[i] = DatabaseColumn{Name: c.Raw}
	}
	return def, idx.RowCount() - 1, nil
}
