package workbook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"excellens/internal/formats"
)

const worksheetCatalogueCacheVersion = 1

type worksheetCatalogueCache struct {
	Version     int                            `json:"version"`
	Fingerprint string                         `json:"fingerprint"`
	Sheet       string                         `json:"sheet"`
	XMLSize     int64                          `json:"xmlSize"`
	SourceCRC32 uint32                         `json:"sourceCRC32"`
	Value       worksheetCatalogueDeclarations `json:"value"`
	Checksum    string                         `json:"checksum"`
}

type worksheetCatalogueEntry struct {
	value worksheetCatalogueDeclarations
	biff  *Catalogue
	bytes int
	stamp uint64
}

type worksheetCatalogueLoad struct {
	done  chan struct{}
	value worksheetCatalogueEntry
	err   error
}

func (c worksheetCatalogueCache) checksum() string {
	c.Checksum = ""
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (w *Workbook) worksheetCataloguePath(meta SheetMeta) string {
	sum := sha256.Sum256([]byte(meta.Path))
	return filepath.Join(w.CacheDir, fmt.Sprintf("source-catalogue-v%d-%x.json", worksheetCatalogueCacheVersion, sum[:12]))
}

func validWorksheetDeclarations(value worksheetCatalogueDeclarations) bool {
	if len(value.IDs) > formats.CatalogueEntries {
		return false
	}
	seen := make(map[string]bool, len(value.IDs))
	bytes := 0
	for _, id := range value.IDs {
		if id == "" || len(id) > formats.CatalogueDefinitionMax || seen[id] {
			return false
		}
		seen[id] = true
		bytes += len(id)
		if bytes > formats.CataloguePartBytes {
			return false
		}
	}
	return true
}

func (w *Workbook) loadWorksheetCatalogueCache(meta SheetMeta, idx *SheetIndex, sourceCRC uint32) (worksheetCatalogueDeclarations, bool) {
	f, err := os.Open(w.worksheetCataloguePath(meta))
	if err != nil {
		return worksheetCatalogueDeclarations{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, formats.CataloguePartBytes+1))
	if err != nil || len(data) > formats.CataloguePartBytes {
		return worksheetCatalogueDeclarations{}, false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var cache worksheetCatalogueCache
	if d.Decode(&cache) != nil || d.Decode(new(any)) != io.EOF || cache.Version != worksheetCatalogueCacheVersion || cache.Fingerprint != w.Fingerprint || cache.Sheet != meta.Path || cache.XMLSize != idx.XMLSize || cache.SourceCRC32 != sourceCRC || cache.Checksum != cache.checksum() || !validWorksheetDeclarations(cache.Value) {
		return worksheetCatalogueDeclarations{}, false
	}
	return cache.Value, true
}

func (w *Workbook) saveWorksheetCatalogue(meta SheetMeta, idx *SheetIndex, sourceCRC uint32, value worksheetCatalogueDeclarations) {
	cache := worksheetCatalogueCache{Version: worksheetCatalogueCacheVersion, Fingerprint: w.Fingerprint, Sheet: meta.Path, XMLSize: idx.XMLSize, SourceCRC32: sourceCRC, Value: value}
	cache.Checksum = cache.checksum()
	data, err := json.Marshal(cache)
	if err != nil || len(data) > formats.CataloguePartBytes {
		return
	}
	w.saveCatalogueBytes(w.worksheetCataloguePath(meta), data)
}

func (w *Workbook) saveCatalogueBytes(path string, data []byte) {
	f, err := os.CreateTemp(w.CacheDir, ".source-catalogue-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err == nil && closed == nil {
		_ = replaceFile(name, path)
	}
}

func (w *Workbook) cachedWorksheetCatalogue(meta SheetMeta) (worksheetCatalogueDeclarations, bool) {
	entry, found := w.cachedCatalogueEntry(meta)
	return entry.value, found && entry.biff == nil
}

func (w *Workbook) cachedCatalogueEntry(meta SheetMeta) (worksheetCatalogueEntry, bool) {
	w.catalogueMu.Lock()
	defer w.catalogueMu.Unlock()
	entry, found := w.catalogueSheets[meta.Path]
	if found {
		w.catalogueClock++
		entry.stamp = w.catalogueClock
		w.catalogueSheets[meta.Path] = entry
	}
	return entry, found
}

func (w *Workbook) cacheWorksheetCatalogue(meta SheetMeta, value worksheetCatalogueDeclarations) {
	bytes := 128 + len(value.IDs)*32
	for _, id := range value.IDs {
		bytes += len(id) * 2
	}
	if bytes > formats.CatalogueSheetCacheBytes || !validWorksheetDeclarations(value) {
		return
	}
	w.cacheCatalogueEntry(meta, worksheetCatalogueEntry{value: value, bytes: bytes})
}

func (w *Workbook) cacheCatalogueEntry(meta SheetMeta, value worksheetCatalogueEntry) {
	if value.bytes < 1 || value.bytes > formats.CatalogueSheetCacheBytes {
		return
	}
	w.catalogueMu.Lock()
	defer w.catalogueMu.Unlock()
	if w.catalogueSheets == nil {
		w.catalogueSheets = make(map[string]worksheetCatalogueEntry)
	}
	if old, found := w.catalogueSheets[meta.Path]; found {
		w.catalogueBytes -= old.bytes
		delete(w.catalogueSheets, meta.Path)
	}
	for len(w.catalogueSheets) > 0 && (len(w.catalogueSheets) >= formats.CatalogueSheetCacheEntries || w.catalogueBytes+value.bytes > formats.CatalogueSheetCacheBytes) {
		oldest, stamp := "", ^uint64(0)
		for path, entry := range w.catalogueSheets {
			if entry.stamp < stamp {
				oldest, stamp = path, entry.stamp
			}
		}
		w.catalogueBytes -= w.catalogueSheets[oldest].bytes
		delete(w.catalogueSheets, oldest)
	}
	w.catalogueClock++
	value.stamp = w.catalogueClock
	w.catalogueSheets[meta.Path] = value
	w.catalogueBytes += value.bytes
}

// Invoked only after the original XML pass and ZIP CRC verification succeed.
// A derived proof cache failure does not fail the ordinary worksheet index.
func (w *Workbook) rememberWorksheetCatalogue(meta SheetMeta, idx *SheetIndex, seed *worksheetCatalogueSeed, sourceCRC uint32) {
	value, err := worksheetDeclarationsFromSeed(context.Background(), idx, seed)
	if err != nil {
		return
	}
	w.saveWorksheetCatalogue(meta, idx, sourceCRC, value)
	w.cacheWorksheetCatalogue(meta, value)
}

func (w *Workbook) worksheetDeclarations(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (worksheetCatalogueDeclarations, error) {
	entry, err := w.loadCatalogueEntry(ctx, meta, func() (worksheetCatalogueEntry, error) {
		value, err := w.loadWorksheetDeclarations(ctx, meta, idx, progress)
		bytes := 128 + len(value.IDs)*32
		for _, id := range value.IDs {
			bytes += len(id) * 2
		}
		return worksheetCatalogueEntry{value: value, bytes: bytes}, err
	})
	return entry.value, err
}

// OOXML declaration proofs and BIFF tables share the same bounded LRU and
// cancellable ownership. IO and callbacks run without holding the map lock.
func (w *Workbook) loadCatalogueEntry(ctx context.Context, meta SheetMeta, load func() (worksheetCatalogueEntry, error)) (worksheetCatalogueEntry, error) {
	for {
		if err := ctx.Err(); err != nil {
			return worksheetCatalogueEntry{}, err
		}
		if value, found := w.cachedCatalogueEntry(meta); found {
			return value, nil
		}
		w.catalogueMu.Lock()
		if entry, found := w.catalogueSheets[meta.Path]; found {
			w.catalogueClock++
			entry.stamp = w.catalogueClock
			w.catalogueSheets[meta.Path] = entry
			w.catalogueMu.Unlock()
			return entry, nil
		}
		if loading := w.catalogueLoading[meta.Path]; loading != nil {
			w.catalogueMu.Unlock()
			select {
			case <-ctx.Done():
				return worksheetCatalogueEntry{}, ctx.Err()
			case <-loading.done:
			}
			if errors.Is(loading.err, context.Canceled) || errors.Is(loading.err, context.DeadlineExceeded) {
				continue
			}
			return loading.value, loading.err
		}
		loading := &worksheetCatalogueLoad{done: make(chan struct{})}
		if w.catalogueLoading == nil {
			w.catalogueLoading = make(map[string]*worksheetCatalogueLoad)
		}
		w.catalogueLoading[meta.Path] = loading
		w.catalogueMu.Unlock()
		value, err := load()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			w.cacheCatalogueEntry(meta, value)
		}
		w.catalogueMu.Lock()
		loading.value, loading.err = value, err
		delete(w.catalogueLoading, meta.Path)
		close(loading.done)
		w.catalogueMu.Unlock()
		return value, err
	}
}

func (w *Workbook) loadWorksheetDeclarations(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (worksheetCatalogueDeclarations, error) {
	reader, err := w.openCatalogueReader(ctx)
	if err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	defer reader.zip.Close()
	part := reader.entries[meta.Path]
	if part == nil || part.UncompressedSize64 != uint64(idx.XMLSize) {
		return worksheetCatalogueDeclarations{}, errors.New("表声明来源部件与工作表索引不匹配")
	}
	if value, found := w.loadWorksheetCatalogueCache(meta, idx, part.CRC32); found {
		return value, ctx.Err()
	}
	if progress != nil {
		progress(0, idx.XMLSize, "正在确认旧索引的表对象声明")
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != idx.XMLSize || stat.Size() < 0 || stat.Size() > maxSheetXMLSize {
		return worksheetCatalogueDeclarations{}, errors.New("表声明的XML缓存大小无效")
	}
	hash := crc32.NewIEEE()
	seed, err := scanWorksheetCatalogue(ctx, io.TeeReader(f, hash), idx.XMLSize, progress)
	if err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	if hash.Sum32() != part.CRC32 {
		return worksheetCatalogueDeclarations{}, errors.New("表声明XML缓存校验失败，请重新建立工作表索引")
	}
	value, err := worksheetDeclarationsFromSeed(ctx, idx, seed)
	if err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	if err := ctx.Err(); err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return worksheetCatalogueDeclarations{}, err
	}
	w.saveWorksheetCatalogue(meta, idx, part.CRC32, value)
	return value, nil
}
