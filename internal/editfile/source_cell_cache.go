package editfile

import (
	"context"
	"sync"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

// SourceCellCache holds only original cells from one digest-verified editor
// base. Draft values, formula results and structural revisions never share it.
type SourceCellCache struct {
	mu      sync.Mutex
	key     string
	cells   map[editdoc.Address]SourceCell
	names   map[nameKey]nameTarget
	bytes   int
	formats *entryFormatTable
}

func (l Layout) WithSourceCells(cache *SourceCellCache, binding string, index *CalculationBook) Layout {
	l.cache, l.cacheKey, l.index = cache, binding, index
	return l
}

func (c *SourceCellCache) read(ctx context.Context, f *workbook.EditSourceFile, l Layout, addresses []editdoc.Address) (map[editdoc.Address]SourceCell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[editdoc.Address]SourceCell, len(addresses))
	var missing []editdoc.Address
	c.mu.Lock()
	if c.key != l.cacheKey {
		c.key, c.cells, c.names, c.bytes = l.cacheKey, make(map[editdoc.Address]SourceCell), nil, 0
		c.formats = nil
	}
	for _, a := range addresses {
		if _, duplicate := result[a]; duplicate {
			c.mu.Unlock()
			return nil, ErrTarget
		}
		v, ok := c.cells[a]
		result[a] = v
		if !ok {
			missing = append(missing, a)
		}
	}
	c.mu.Unlock()
	if len(missing) == 0 {
		return result, f.Check()
	}
	fresh, err := ReadXLSXCells(ctx, f, *l.XLSX, missing)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for a, v := range fresh {
		result[a] = v
		if c.key != l.cacheKey || len(v.rules) != 0 {
			continue
		}
		if _, exists := c.cells[a]; exists {
			continue
		}
		size := 320 + len(v.Value.Raw) + len(v.ReadOnly) + len(a.Sheet)
		if v.Cache != nil {
			size += len(v.Cache.Raw)
		}
		if size > 8192 {
			continue
		}
		if c.bytes+size > 2<<20 {
			clear(c.cells)
			c.bytes = 0
		}
		if c.names == nil {
			c.names = v.names
		}
		v.names = c.names
		c.cells[a] = v
		c.bytes += size
	}
	return result, nil
}

func (l Layout) entryStyleTable(ctx context.Context, f *workbook.EditSourceFile) (entryFormatTable, error) {
	c := l.cache
	if c != nil {
		c.mu.Lock()
		table, key := c.formats, c.key
		c.mu.Unlock()
		if key == l.cacheKey && table != nil {
			return *table, ctx.Err()
		}
	}
	p, err := openXLSXPackage(f, f.Size())
	if err != nil {
		return entryFormatTable{}, err
	}
	table, err := entryFormats(ctx, p)
	if err == nil && c != nil && len(table.data)+32*len(table.xfs) <= 2<<20 {
		c.mu.Lock()
		if c.key == l.cacheKey {
			c.formats = &table
		}
		c.mu.Unlock()
	}
	return table, err
}
