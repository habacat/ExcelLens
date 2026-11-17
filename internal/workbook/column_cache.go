package workbook

import "sync"

// Query-only column store: immutable, short-lived, bounded RAM cache keyed by
// the *validated* sheet index. It never writes transformed values back to XLSX.
// It stores the visible filter value, numeric raw differences and cell kind, so
// rebuilding a filter with another predicate cannot change the data semantics.
const (
	columnCacheBudget         = 96 << 20
	columnCachePerColumnLimit = 64 << 20
)

type columnCacheKey struct {
	idx    *SheetIndex
	column int
}
type cachedColumn struct {
	values      []string
	codes       []uint32
	dictionary  []string
	lookup      map[string]uint32
	lookupBytes int64
	raw         map[int]string // sparse: numeric raw values differing from display
	rawBytes    int64
	// 0: no source cell; 1: other value cell; 2: text; 3: numeric cell. This preserves
	// blank-vs-missing and numeric-identifier semantics on repeated queries.
	kind  []byte
	bytes int64
	used  uint64
}

func (v *cachedColumn) length() int {
	if v.codes != nil {
		return len(v.codes)
	}
	return len(v.values)
}
func (v *cachedColumn) valueAt(row int) string {
	if v.codes != nil {
		return v.dictionary[v.codes[row]]
	}
	return v.values[row]
}
func estimatedColumnBytes(rows int) int64 {
	if rows >= 1024 {
		return int64(rows) * 5
	}
	return int64(rows) * 17
}

type columnCacheState struct {
	mu    sync.Mutex
	items map[columnCacheKey]*cachedColumn
	size  int64
	tick  uint64
}

func (w *Workbook) cachedColumns(idx *SheetIndex, columns []int) ([]*cachedColumn, bool) {
	c := &w.columnCache
	c.mu.Lock()
	defer c.mu.Unlock()
	views := make([]*cachedColumn, len(columns))
	allCached := true
	for slot, col := range columns {
		v := c.items[columnCacheKey{idx: idx, column: col}]
		if v == nil || v.length() != idx.RowCount() {
			allCached = false
			continue
		}
		views[slot] = v
	}
	c.tick++
	for _, v := range views {
		if v != nil {
			v.used = c.tick
		}
	}
	return views, allCached
}

func (w *Workbook) cacheColumn(idx *SheetIndex, col int, v *cachedColumn) {
	if v == nil || v.length() != idx.RowCount() || len(v.kind) != idx.RowCount() {
		return
	}
	if v.lookup != nil {
		v.lookup = nil
		v.bytes -= v.lookupBytes
		v.lookupBytes = 0
	}
	if v.bytes <= 0 || v.bytes > columnCachePerColumnLimit {
		return
	}
	c := &w.columnCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = make(map[columnCacheKey]*cachedColumn)
	}
	key := columnCacheKey{idx: idx, column: col}
	if old := c.items[key]; old != nil {
		// An existing fully published column is already immutable and valid.
		return
	}
	for c.size+v.bytes > columnCacheBudget && len(c.items) > 0 {
		var oldest columnCacheKey
		var tick uint64 = ^uint64(0)
		for k, item := range c.items {
			if item.used < tick {
				oldest = k
				tick = item.used
			}
		}
		c.size -= c.items[oldest].bytes
		delete(c.items, oldest)
	}
	c.tick++
	v.used = c.tick
	c.items[key] = v
	c.size += v.bytes
}

func (w *Workbook) clearColumnCache() {
	c := &w.columnCache
	c.mu.Lock()
	c.items = nil
	c.size = 0
	c.tick = 0
	c.mu.Unlock()
}

func beginColumnCache(rows int) *cachedColumn {
	// Slice headers themselves cost 16 bytes per value on Windows x64.
	estimated := estimatedColumnBytes(rows)
	if rows <= 0 || estimated > columnCachePerColumnLimit {
		return nil
	}
	if rows >= 1024 {
		if estimated+80 > columnCachePerColumnLimit {
			return nil
		}
		return &cachedColumn{codes: make([]uint32, rows), kind: make([]byte, rows), dictionary: []string{""}, lookup: map[string]uint32{"": 0}, lookupBytes: 64, bytes: estimated + 80}
	}
	return &cachedColumn{values: make([]string, rows), kind: make([]byte, rows), bytes: estimated}
}

func writeColumnCache(cache *cachedColumn, row int, cell CellData, exists bool) *cachedColumn {
	if cache == nil {
		return nil
	}
	if !exists {
		return cache
	}
	value := filterValue(cell)
	if cache.codes != nil {
		code, ok := cache.lookup[value]
		if !ok {
			if cache.bytes+int64(len(value))+80 > columnCachePerColumnLimit {
				return nil
			}
			code = uint32(len(cache.dictionary))
			cache.lookup[value] = code
			cache.dictionary = append(cache.dictionary, value)
			cache.bytes += int64(len(value)) + 80
			cache.lookupBytes += 64
		}
		cache.codes[row] = code
		if len(cache.dictionary) > 4096 {
			flatBytes := int64(len(cache.kind))*17 + cache.rawBytes
			for _, text := range cache.dictionary {
				flatBytes += int64(len(text))
			}
			// Check before expanding the full column, not after a large
			// allocation that would immediately be discarded.
			if flatBytes > columnCachePerColumnLimit {
				return nil
			}
			cache.values = make([]string, len(cache.kind))
			cache.bytes = flatBytes
			for i := 0; i <= row; i++ {
				cache.values[i] = cache.dictionary[cache.codes[i]]
			}
			cache.codes = nil
			cache.dictionary = nil
			cache.lookup = nil
			cache.lookupBytes = 0
		}
	} else {
		if cache.bytes+int64(len(value)) > columnCachePerColumnLimit {
			return nil
		}
		cache.bytes += int64(len(value))
		cache.values[row] = value
	}
	if isTextCell(cell) {
		cache.kind[row] = 2
	} else if cell.Type == "" || cell.Type == "n" {
		cache.kind[row] = 3
		if cell.Raw != "" && cell.Raw != value {
			additional := int64(len(cell.Raw)) + 64
			if cache.raw == nil {
				additional += 64
			}
			if cache.bytes+additional > columnCachePerColumnLimit {
				return nil
			}
			if cache.raw == nil {
				cache.raw = make(map[int]string)
				cache.bytes += 64
				cache.rawBytes += 64
			}
			cache.raw[row] = cell.Raw
			cache.bytes += int64(len(cell.Raw)) + 64
			cache.rawBytes += int64(len(cell.Raw)) + 64
		}
	} else {
		cache.kind[row] = 1
	}
	if cache.bytes > columnCachePerColumnLimit {
		return nil
	}
	return cache
}

// In a cache hit we materialize only the fields used by matchCompiledFilter
// and sortValue, never a formula or style. Numeric raw differences are retained
// so a warm query sorts dates/rounded amounts identically to a cold scan.
func cacheCell(v *cachedColumn, row int) (CellData, bool) {
	if v.kind[row] == 0 {
		return CellData{}, false
	}
	cell := CellData{Display: v.valueAt(row)}
	if v.kind[row] == 2 {
		cell.Type = "s"
	} else if v.kind[row] == 3 {
		cell.Type = "n"
		cell.Raw = cell.Display
		if raw, ok := v.raw[row]; ok {
			cell.Raw = raw
		}
	} else {
		cell.Type = "cachedValue"
	}
	return cell, true
}
