package workbook

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unsafe"
)

const sortMergeFanIn = 16

const sortItemMemory = int64(unsafe.Sizeof(sortItem{}))
const sortValueMemory = int64(unsafe.Sizeof(sortValue{}))
const sortReaderMemory int64 = 8 << 10 // scratch, reader and heap metadata

type sortRecord struct {
	index  int
	values []sortValue
}

// The same comparator is used for resident chunks, run validation and merge
// heads. Categories stay in place when descending: blanks last, numbers before
// text, then the chosen direction within a category. Source order breaks ties.
func compareSortRecords(a, b sortRecord, keys []SortKey) int {
	for k, key := range keys {
		x, y := a.values[k], b.values[k]
		if x.text == "" || y.text == "" {
			if x.text != y.text {
				if x.text == "" {
					return 1
				}
				return -1
			}
			continue
		}
		if x.isNumber != y.isNumber {
			if x.isNumber {
				return -1
			}
			return 1
		}
		var cmp int
		if x.isNumber {
			cmp = compareSortNumeric(x.text, y.text, x.number, y.number)
		} else if key.Mode == "natural" {
			cmp = compareNaturalText(x.folded, y.folded)
		} else {
			cmp = strings.Compare(x.folded, y.folded)
		}
		if key.Descending {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp
		}
	}
	if a.index < b.index {
		return -1
	}
	if a.index > b.index {
		return 1
	}
	return 0
}

// ASCII digit runs compare without integer/float conversion or loss of leading
// zeros. Equal runs (2 and 0002) continue at the suffix; full ties keep source
// order. Other text uses the existing case-folded lexical ordering.
func compareNaturalText(a, b string) int {
	for i, j := 0, 0; ; {
		if i == len(a) || j == len(b) {
			return strings.Compare(a[i:], b[j:])
		}
		if a[i] >= '0' && a[i] <= '9' && b[j] >= '0' && b[j] <= '9' {
			ai, bj := i, j
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			for ai < i && a[ai] == '0' {
				ai++
			}
			for bj < j && b[bj] == '0' {
				bj++
			}
			if i-ai != j-bj {
				if i-ai < j-bj {
					return -1
				}
				return 1
			}
			if cmp := strings.Compare(a[ai:i], b[bj:j]); cmp != 0 {
				return cmp
			}
			continue
		}
		if a[i] != b[j] {
			if a[i] < b[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
}

func sortValueBytes(key sortValue) int64 {
	n := int64(len(key.text))
	if key.folded != key.text {
		n += int64(len(key.folded))
	}
	return n
}

func sortRecordBytes(values []sortValue) int64 {
	n := int64(8 + len(values)*17) // record length, source index, key metadata
	for _, key := range values {
		n += sortValueBytes(key)
	}
	return n
}

func sortValueStorageBytes(key sortValue) int64 {
	// Cover allocation size classes as well as the literal UTF-8 bytes.
	charge := func(n int) int64 {
		if n == 0 {
			return 0
		}
		return int64(n) + int64(n)/4 + 16
	}
	n := charge(len(key.text))
	if key.folded != key.text {
		n += charge(len(key.folded))
	}
	return n
}

type rowSorter struct {
	dir         string
	sourceRows  int
	keys        []SortKey
	budget      int64
	maxRecord   int64
	bufferBytes int
	quota       *queryQuota
	items       []sortItem
	values      []sortValue
	stringBytes int64
	count       int
	last        int
	runs        []*sortRun
	files       map[*queryFile]struct{}
	prepared    bool
	finished    bool
}

func newRowSorter(dir string, sourceRows int, keys []SortKey, budget int64, quota *queryQuota) (*rowSorter, error) {
	if sourceRows < 0 || len(keys) == 0 || len(keys) > 5 || budget < 32<<10 || quota == nil {
		return nil, errors.New("排序容量无效")
	}
	buffer := int(min(int64(64<<10), budget/64))
	perRow := sortItemMemory + int64(len(keys))*sortValueMemory
	// Two runs must always fit, including both reader key buffers and I/O.
	maxRecord := budget/5 - int64(buffer)*2 - perRow*2 - sortReaderMemory/2 - int64(len(keys))*64
	return &rowSorter{dir: dir, sourceRows: sourceRows, keys: append([]SortKey(nil), keys...), budget: budget, maxRecord: maxRecord,
		bufferBytes: buffer, quota: quota, last: -1, files: make(map[*queryFile]struct{})}, nil
}

func (s *rowSorter) abort() {
	for file := range s.files {
		_ = file.discard()
		delete(s.files, file)
	}
}

func (s *rowSorter) forget(file *queryFile) error {
	err := file.discard()
	delete(s.files, file) // failed deletions remain charged in the quota registry
	return err
}

func (s *rowSorter) chunkRecord(item sortItem) sortRecord {
	return sortRecord{index: item.index, values: s.values[item.offset : item.offset+len(s.keys)]}
}

// Explicit capacity growth accounts for old and new backing arrays together.
// A chunk spills before either string storage or metadata reaches the budget.
func (s *rowSorter) grow(incoming int64) bool {
	perRow := sortItemMemory + int64(len(s.keys))*sortValueMemory
	capacity := min(s.sourceRows, int(s.budget/3/perRow), max(64, cap(s.items)*2))
	if capacity <= cap(s.items) || int64(capacity+cap(s.items))*perRow+s.stringBytes+incoming > s.budget {
		return false
	}
	items := make([]sortItem, len(s.items), capacity)
	values := make([]sortValue, len(s.values), capacity*len(s.keys))
	copy(items, s.items)
	copy(values, s.values)
	s.items, s.values = items, values
	return true
}

func (s *rowSorter) add(ctx context.Context, position int, values []sortValue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.finished || position <= s.last || position >= s.sourceRows || len(values) != len(s.keys) {
		return errors.New("排序源位置或比较键无效")
	}
	rowBytes := sortRecordBytes(values)
	if rowBytes > s.maxRecord {
		return errors.New("单行排序比较键过长，请减少排序列；此次排序未发布结果")
	}
	var stringsSize int64
	for _, key := range values {
		stringsSize += sortValueStorageBytes(key)
	}
	metadata := int64(cap(s.items)) * (sortItemMemory + int64(len(s.keys))*sortValueMemory)
	if len(s.items) > 0 && metadata+s.stringBytes+stringsSize > s.budget {
		if err := s.spill(ctx); err != nil {
			return err
		}
	}
	if len(s.items) == cap(s.items) && !s.grow(stringsSize) {
		if err := s.spill(ctx); err != nil {
			return err
		}
		if cap(s.items) == 0 && !s.grow(stringsSize) {
			return errors.New("排序比较键超过内存预算")
		}
	}
	s.items = append(s.items, sortItem{index: position, offset: len(s.values)})
	s.values = append(s.values, values...)
	s.stringBytes += stringsSize
	s.last = position
	s.count++
	return nil
}

type sortCancelled struct{ err error }

func (s *rowSorter) sortChunk(ctx context.Context) (err error) {
	// sort.Slice has no cancellation hook. Only this private sentinel is
	// recovered; programming panics still propagate normally.
	defer func() {
		if failure := recover(); failure != nil {
			if cancelled, ok := failure.(sortCancelled); ok {
				err = cancelled.err
			} else {
				panic(failure)
			}
		}
	}()
	comparisons := 0
	sort.Slice(s.items, func(i, j int) bool {
		comparisons++
		if comparisons&4095 == 0 && ctx.Err() != nil {
			panic(sortCancelled{ctx.Err()})
		}
		return compareSortRecords(s.chunkRecord(s.items[i]), s.chunkRecord(s.items[j]), s.keys) < 0
	})
	return ctx.Err()
}

func (s *rowSorter) spill(ctx context.Context) error {
	if len(s.items) == 0 {
		return ctx.Err()
	}
	if err := s.sortChunk(ctx); err != nil {
		return err
	}
	if !s.prepared {
		if err := s.quota.cleanStale(ctx, s.dir); err != nil {
			return err
		}
		s.prepared = true
	}
	run, err := s.writeRun(ctx, len(s.items), func(emit func(sortRecord) error) error {
		for _, item := range s.items {
			if err := emit(s.chunkRecord(item)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.runs = append(s.runs, run)
	clear(s.values) // do not retain previous chunk strings through spare capacity
	s.items, s.values, s.stringBytes = s.items[:0], s.values[:0], 0
	return nil
}

func (s *rowSorter) groupSize(runs []*sortRun) int {
	var memory int64
	for i, run := range runs {
		if i == sortMergeFanIn {
			return i
		}
		// Each reader alternates two reusable key buffers to validate order.
		memory += 2*(run.maxRecord+run.maxRecord/4+int64(len(s.keys))*(sortValueMemory+32)+sortItemMemory) + int64(s.bufferBytes) + sortReaderMemory
		if memory+int64(s.bufferBytes) > s.budget {
			return i
		}
	}
	return len(runs)
}

func (s *rowSorter) finish(ctx context.Context, emit func(int) error, phase func(string, int, int)) error {
	return s.finishRecords(ctx, func(record sortRecord) error { return emit(record.index) }, phase)
}

func (s *rowSorter) finishRecords(ctx context.Context, emit func(sortRecord) error, phase func(string, int, int)) error {
	if s.finished {
		return errors.New("排序已经结束")
	}
	s.finished = true
	if phase != nil {
		phase("正在整理排序结果", 0, s.count)
	}
	if len(s.runs) == 0 {
		if err := s.sortChunk(ctx); err != nil {
			return err
		}
		for _, item := range s.items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(s.chunkRecord(item)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.spill(ctx); err != nil {
		return err
	}
	s.items, s.values = nil, nil
	for s.groupSize(s.runs) < len(s.runs) {
		var next []*sortRun
		for start := 0; start < len(s.runs); {
			groupCount := s.groupSize(s.runs[start:])
			if groupCount < 2 && len(s.runs)-start > 1 {
				return errors.New("排序归并比较键超过内存预算")
			}
			if groupCount == 1 {
				next = append(next, s.runs[start])
				break
			}
			group := s.runs[start : start+groupCount]
			count := 0
			for _, run := range group {
				count += run.count
			}
			if phase != nil {
				phase("正在归并排序段", start, len(s.runs))
			}
			run, err := s.writeRun(ctx, count, func(emit func(sortRecord) error) error { return s.mergeRuns(ctx, group, emit) })
			if err != nil {
				return err
			}
			next = append(next, run)
			for _, old := range group {
				if err := s.forget(old.file); err != nil {
					return err
				}
			}
			start += groupCount
		}
		s.runs = next
	}
	count := 0
	lastReport := time.Now()
	err := s.mergeRuns(ctx, s.runs, func(record sortRecord) error {
		if err := emit(record); err != nil {
			return err
		}
		count++
		if phase != nil && (count == s.count || count&4095 == 0 && time.Since(lastReport) >= 250*time.Millisecond) {
			phase("正在归并排序结果", count, s.count)
			lastReport = time.Now()
		}
		return nil
	})
	if err == nil && count != s.count {
		err = errors.New("排序结果行数不符")
	}
	return err
}
