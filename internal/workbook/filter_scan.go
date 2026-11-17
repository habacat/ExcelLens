package workbook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"excellens/internal/formats"
)

// filterScanner is shared by the original single-pass filter/sort and durable
// record scans. It owns only scan-local scratch and unpublished column caches.
type filterScanner struct {
	w         *Workbook
	idx       *SheetIndex
	file      *os.File
	stream    indexedStream
	columns   []int
	slots     map[int]int
	prepared  []compiledFilterRule
	sortSlots []int
	views     []*cachedColumn
	builders  []*cachedColumn
	missing   map[int]struct{}
	first     int
	last      int
	cached    bool
	cells     []CellData
	exists    []bool
	whole     bool
}

func newFilterScanner(w *Workbook, idx *SheetIndex, rules []FilterRule, keys []SortKey, whole bool) (*filterScanner, error) {
	s := &filterScanner{w: w, idx: idx, whole: whole, slots: make(map[int]int), first: 16385}
	columns := make(map[int]struct{}, len(rules)+len(keys))
	for _, rule := range rules {
		columns[rule.Column] = struct{}{}
	}
	for _, key := range keys {
		columns[key.Column] = struct{}{}
	}
	for column := range columns {
		s.columns = append(s.columns, column)
	}
	sort.Ints(s.columns)
	for slot, column := range s.columns {
		s.slots[column] = slot
	}
	for _, rule := range rules {
		compiled := compiledFilterRule{FilterRule: rule, slot: s.slots[rule.Column], needle: strings.ToLower(strings.TrimSpace(rule.Value))}
		if rule.Operator == "in" {
			compiled.allowed = make(map[string]struct{}, len(rule.Values))
			compiled.allowedExact = make(map[string]struct{}, len(rule.Values))
			for _, value := range rule.Values {
				compiled.allowedExact[value] = struct{}{}
				compiled.allowed[filterFoldKey(value)] = struct{}{}
			}
		}
		if compiled.Operator == "equals" || compiled.Operator == "notEquals" || compiled.Operator == "gt" || compiled.Operator == "gte" || compiled.Operator == "lt" || compiled.Operator == "lte" {
			compiled.numericValue, compiled.hasNumeric = parseFilterNumber(strings.TrimSpace(rule.Value))
		}
		s.prepared = append(s.prepared, compiled)
	}
	for _, key := range keys {
		s.sortSlots = append(s.sortSlots, s.slots[key.Column])
	}
	s.views, s.cached = w.cachedColumns(idx, s.columns)
	if len(s.columns) == 0 {
		s.cached = true // sort-only filtering needs source positions, not cell values
	}
	s.missing = make(map[int]struct{})
	for slot, column := range s.columns {
		if s.views[slot] != nil {
			continue
		}
		s.missing[column] = struct{}{}
		s.first, s.last = min(s.first, column), max(s.last, column)
	}
	if !s.cached {
		var err error
		s.file, err = os.Open(idx.XMLPath)
		if err != nil {
			return nil, err
		}
	}
	s.stream = indexedStream{f: s.file, idx: idx}
	s.cells, s.exists = make([]CellData, len(s.columns)), make([]bool, len(s.columns))
	initialBytes := estimatedColumnBytes(idx.RowCount()) * int64(len(s.missing))
	if whole && !s.cached && len(s.missing) <= 8 && initialBytes <= columnCacheBudget/2 {
		s.builders = make([]*cachedColumn, len(s.columns))
		for slot := range s.builders {
			if s.views[slot] == nil {
				s.builders[slot] = beginColumnCache(idx.RowCount())
			}
		}
	}
	return s, nil
}

func (s *filterScanner) close() {
	if s.file != nil {
		_ = s.file.Close()
	}
}

func (s *filterScanner) read(position int) error {
	clear(s.exists)
	for slot, view := range s.views {
		if view != nil {
			s.cells[slot], s.exists[slot] = cacheCell(view, position)
		}
	}
	if !s.cached {
		row, err := s.stream.read(s.w, position, s.first, s.last, s.missing, true, false)
		if err != nil {
			return err
		}
		for _, cell := range row.Cells {
			if slot, found := s.slots[cell.Col]; found && s.views[slot] == nil {
				s.cells[slot], s.exists[slot] = cell, true
			}
		}
		for slot, builder := range s.builders {
			if builder != nil {
				s.builders[slot] = writeColumnCache(builder, position, s.cells[slot], s.exists[slot])
			}
		}
	}
	return nil
}

func (s *filterScanner) matches() bool {
	for _, rule := range s.prepared {
		if !matchCompiledFilter(s.cells[rule.slot], s.exists[rule.slot], rule) {
			return false
		}
	}
	return true
}

func (s *filterScanner) publishColumns() {
	if s.whole {
		for slot, builder := range s.builders {
			s.w.cacheColumn(s.idx, s.columns[slot], builder)
		}
	}
}

func (s *filterScanner) scan(ctx context.Context, start, headerRow int, visit func(int, bool) error, recordDone func(int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for position := start; position < s.idx.RowCount(); position++ {
		if position&31 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := s.read(position); err != nil {
			return err
		}
		header := s.idx.RowAt(position).Number <= headerRow
		if header || s.matches() {
			if err := visit(position, header); err != nil {
				return err
			}
		}
		if recordDone != nil {
			if err := recordDone(position + 1); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.publishColumns()
	return nil
}

func filterOrderDirectory(w *Workbook) string {
	root := w.CacheDir
	if root == "" {
		root = CacheRoot()
	}
	return filepath.Join(root, "queries")
}

func addFilterSortRecord(ctx context.Context, sorter *rowSorter, scanner *filterScanner, keys []SortKey, scratch []sortValue, position int) error {
	clear(scratch)
	var bytes int64
	for k, slot := range scanner.sortSlots {
		if scanner.exists[slot] {
			scratch[k] = makeSortValue(scanner.cells[slot], keys[k].Mode)
		}
		bytes += sortValueBytes(scratch[k])
		if bytes > sorter.maxRecord {
			return errors.New("单行排序比较键过长，请减少排序列或缩小范围；此次排序未发布结果")
		}
	}
	return sorter.add(ctx, position, scratch)
}

func (w *Workbook) filterRowOrder(ctx context.Context, idx *SheetIndex, rules []FilterRule, opts FilterOptions, progress func(int, int)) (*RowOrder, error) {
	keys, err := validatedFilterKeys(idx, rules, opts)
	if err != nil {
		return nil, err
	}
	scanner, err := newFilterScanner(w, idx, rules, keys, true)
	if err != nil {
		return nil, err
	}
	defer scanner.close()
	order, err := newRowOrderBuilder(filterOrderDirectory(w), idx.RowCount(), residentOrderRows, &processQueryQuota)
	if err != nil {
		return nil, err
	}
	defer order.abort()
	var sorter *rowSorter
	if len(keys) > 0 {
		sorter, err = newRowSorter(filterOrderDirectory(w), idx.RowCount(), keys, formats.QuerySortMemoryBytes, &processQueryQuota)
		if err != nil {
			return nil, err
		}
		defer sorter.abort()
	}
	rowKeys := make([]sortValue, len(keys))
	lastReport := time.Now()
	err = scanner.scan(ctx, 0, opts.HeaderRow, func(position int, header bool) error {
		if header || sorter == nil {
			return order.add(ctx, position)
		}
		return addFilterSortRecord(ctx, sorter, scanner, keys, rowKeys, position)
	}, func(next int) error {
		if progress != nil && (next == idx.RowCount() || (next-1)&4095 == 0 && time.Since(lastReport) >= 250*time.Millisecond) {
			progress(next, idx.RowCount())
			lastReport = time.Now()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if sorter != nil {
		clear(rowKeys)
		if err := sorter.finish(ctx, func(position int) error { return order.add(ctx, position) }, opts.Phase); err != nil {
			return nil, err
		}
		if opts.Phase != nil {
			opts.Phase("正在建立行号定位", 0, 0)
		}
	}
	return order.finish(ctx)
}
