package workbook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"excellens/internal/formats"
)

const biffCatalogueCacheVersion = 1

// BIFF proofs bind to the actual source and worksheet substream, not to the
// derived cell rows. No SST or value index is read when loading this metadata.
type biffCatalogueCache struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	Sheet       string    `json:"sheet"`
	Offset      int64     `json:"offset"`
	Value       Catalogue `json:"value"`
	Checksum    string    `json:"checksum"`
}

func (c biffCatalogueCache) checksum() string {
	c.Checksum = ""
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (w *Workbook) biffCataloguePath(meta SheetMeta) string {
	sum := sha256.Sum256([]byte(meta.Path))
	return filepath.Join(w.CacheDir, fmt.Sprintf("source-catalogue-biff-v%d-%x.json", biffCatalogueCacheVersion, sum[:12]))
}

func biffCatalogueEntry(meta SheetMeta, value Catalogue) (worksheetCatalogueEntry, bool) {
	if value.NamesIncomplete || len(value.Items) > formats.CatalogueEntries || len(value.Notices) > 32 {
		return worksheetCatalogueEntry{}, false
	}
	ids, sourceIDs := map[string]bool{}, map[string]bool{}
	for _, item := range value.Items {
		if item.Kind != "table" || item.Sheet != meta.Name || item.Scope != "" || item.ScopeID != "" || item.TokenHex != "" || item.ID == "" || ids[item.ID] || len(item.Areas) != 0 || len(item.Name) > formats.CatalogueDefinitionMax || len(item.Definition) > formats.CatalogueDefinitionMax || len(item.Reason) > formats.CatalogueDefinitionMax {
			return worksheetCatalogueEntry{}, false
		}
		ids[item.ID] = true
		if (item.Status != "unresolved" && item.Status != "invalid" && item.Status != "unavailable") || (value.Complete && item.Status != "unresolved") {
			return worksheetCatalogueEntry{}, false
		}
		if item.Table == nil || len(item.Table.Columns) > formats.BIFFColumns {
			return worksheetCatalogueEntry{}, false
		}
		if item.Table.Verified {
			col, row, endCol, endRow, err := biffTableRange(item.Table.Ref)
			if !value.Complete || item.Status != "unresolved" || item.Name == "" || item.Table.SourceID == "" || sourceIDs[item.Table.SourceID] || err != nil || row < 1 || row > endRow || endRow > formats.BIFFRows || col < 1 || col > endCol || endCol > formats.BIFFColumns || item.Table.HeaderRows < 0 || item.Table.HeaderRows > 1 || item.Table.TotalRows < 0 || item.Table.TotalRows > 1 || item.Table.HeaderRows+item.Table.TotalRows > endRow-row+1 || len(item.Table.Columns) != endCol-col+1 {
				return worksheetCatalogueEntry{}, false
			}
			columns, captions := map[string]bool{}, map[string]bool{}
			for _, column := range item.Table.Columns {
				if column.ID == "" || columns[column.ID] || column.Name == "" || len(column.Name) > 1020 || captions[column.Name] {
					return worksheetCatalogueEntry{}, false
				}
				columns[column.ID], captions[column.Name] = true, true
			}
		}
		sourceIDs[item.Table.SourceID] = true
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > formats.CataloguePartBytes {
		return worksheetCatalogueEntry{}, false
	}
	return worksheetCatalogueEntry{biff: &value, bytes: 128 + len(data)*2 + len(value.Items)*256}, true
}

func biffTableRange(ref string) (col, row, endCol, endRow int, err error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 2 {
		return 0, 0, 0, 0, errors.New("BIFF8表对象范围无效")
	}
	col, row, err = ParseCellRef(parts[0])
	if err == nil {
		endCol, endRow, err = ParseCellRef(parts[1])
	}
	return
}

func (w *Workbook) loadBIFFCatalogueCache(meta SheetMeta) (worksheetCatalogueEntry, bool) {
	sheet, err := w.biffSheet(meta)
	if err != nil {
		return worksheetCatalogueEntry{}, false
	}
	f, err := os.Open(w.biffCataloguePath(meta))
	if err != nil {
		return worksheetCatalogueEntry{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, formats.CataloguePartBytes+1))
	if err != nil || len(data) > formats.CataloguePartBytes {
		return worksheetCatalogueEntry{}, false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var cache biffCatalogueCache
	if d.Decode(&cache) != nil || d.Decode(new(any)) != io.EOF || cache.Version != biffCatalogueCacheVersion || cache.Fingerprint != w.Fingerprint || cache.Sheet != meta.Path || cache.Offset != sheet.offset || cache.Checksum != cache.checksum() {
		return worksheetCatalogueEntry{}, false
	}
	return biffCatalogueEntry(meta, cache.Value)
}

func (w *Workbook) saveBIFFCatalogue(meta SheetMeta, value Catalogue) {
	sheet, err := w.biffSheet(meta)
	if err != nil {
		return
	}
	if _, valid := biffCatalogueEntry(meta, value); !valid {
		return
	}
	cache := biffCatalogueCache{Version: biffCatalogueCacheVersion, Fingerprint: w.Fingerprint, Sheet: meta.Path, Offset: sheet.offset, Value: value}
	cache.Checksum = cache.checksum()
	data, err := json.Marshal(cache)
	if err == nil && len(data) <= formats.CataloguePartBytes {
		w.saveCatalogueBytes(w.biffCataloguePath(meta), data)
	}
}

func (w *Workbook) rememberBIFFCatalogue(meta SheetMeta, value Catalogue) {
	w.saveBIFFCatalogue(meta, value)
	if entry, valid := biffCatalogueEntry(meta, value); valid {
		w.cacheCatalogueEntry(meta, entry)
	}
}

func (w *Workbook) loadBIFFCatalogue(ctx context.Context, meta SheetMeta, progress func(int64, int64, string)) (worksheetCatalogueEntry, error) {
	if entry, found := w.loadBIFFCatalogueCache(meta); found {
		return entry, ctx.Err()
	}
	sheet, err := w.openBIFFWorksheet(meta)
	if err != nil {
		return worksheetCatalogueEntry{}, err
	}
	defer sheet.cf.Close()
	sheet.ctx = ctx
	total, last := sheet.reader.end-sheet.start, sheet.start
	if progress != nil {
		progress(0, total, "正在读取旧索引的BIFF8表对象定义")
	}
	capture := newBIFFTableCapture(meta)
	for {
		id, data, err := sheet.next()
		if err != nil {
			return worksheetCatalogueEntry{}, err
		}
		capture.capture(id, data)
		if progress != nil && sheet.reader.pos-last >= 256<<10 {
			progress(sheet.reader.pos-sheet.start, total, "正在读取BIFF8表对象定义")
			last = sheet.reader.pos
		}
		if id == 0x000A {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return worksheetCatalogueEntry{}, err
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return worksheetCatalogueEntry{}, err
	}
	if err := ctx.Err(); err != nil {
		return worksheetCatalogueEntry{}, err
	}
	value := capture.finish()
	entry, valid := biffCatalogueEntry(meta, value)
	if !valid {
		return worksheetCatalogueEntry{}, errors.New("BIFF8表对象目录结果超过容量或结构无效")
	}
	w.saveBIFFCatalogue(meta, value)
	return entry, nil
}

func (w *Workbook) readBIFFCatalogue(ctx context.Context, confirmedSheet string, confirmed *Catalogue) (Catalogue, error) {
	b := newCatalogueBuilder()
	b.value.Complete, b.value.NamesIncomplete = w.source.xlsCatalogue.Complete, w.source.xlsCatalogue.NamesIncomplete
	b.value.Notices = append([]string(nil), w.source.xlsCatalogue.Notices...)
	for _, item := range w.source.xlsCatalogue.Items {
		if !b.add(item) {
			break
		}
	}
	unread := 0
	for _, meta := range w.Sheets {
		if err := ctx.Err(); err != nil {
			return Catalogue{}, err
		}
		var value *Catalogue
		if meta.Path == confirmedSheet {
			value = confirmed
		} else if entry, found := w.cachedCatalogueEntry(meta); found {
			value = entry.biff
		} else if entry, found := w.loadBIFFCatalogueCache(meta); found {
			w.cacheCatalogueEntry(meta, entry)
			value = entry.biff
		}
		if value == nil {
			unread++
			continue
		}
		b.value.Complete = b.value.Complete && value.Complete
		for _, notice := range value.Notices {
			b.notice(notice)
		}
		for _, item := range value.Items {
			// Public callers may change the returned model. Never expose mutable
			// pointers into the shared immutable metadata proof.
			if item.Table != nil {
				table := *item.Table
				table.Columns = append([]CatalogueColumn(nil), table.Columns...)
				item.Table = &table
			}
			if !b.add(item) {
				break
			}
		}
		if b.bytes >= formats.CatalogueBytes {
			break
		}
	}
	if unread > 0 {
		b.notice(fmt.Sprintf("还有%d张BIFF8工作表的表对象未读取；准备索引或显式确认后可查看", unread))
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return Catalogue{}, err
	}
	return w.resolveCatalogue(ctx, b.value)
}
