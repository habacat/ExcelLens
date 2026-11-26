package rowindex

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
	"unsafe"

	"excellens/internal/sourceview"
)

type SortKey = sourceview.SortKey

type SortValue struct {
	Text     string
	Folded   string
	Number   float64
	IsNumber bool
}

// A narrow row record indexes a tightly-packed, contiguous comparison-key
// buffer. One-key sorts no longer reserve four unused keys per row.
type sortItem struct {
	index  int
	offset int
}

const sortMergeFanIn = 16

const sortItemMemory = int64(unsafe.Sizeof(sortItem{}))
const sortValueMemory = int64(unsafe.Sizeof(SortValue{}))
const sortReaderMemory int64 = 8 << 10 // scratch, reader and heap metadata

type sortRecord struct {
	index  int
	values []SortValue
}

func ExactNumericRat(value string) (*big.Rat, bool) {
	s := strings.TrimSpace(value)
	negative := false
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		negative = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "￥", "")
	s = strings.ReplaceAll(s, "¥", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "元", "")
	s = strings.ReplaceAll(s, "人民币", "")
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, false
	}
	if negative {
		r.Neg(r)
	}
	return r, true
}

func CompareSortNumeric(a, b string, af, bf float64) int {
	// Percent scaling can introduce a second floating-point rounding. Resolve
	// close scaled values exactly as well as ordinary float ties.
	percent := strings.HasSuffix(strings.TrimSpace(a), "%") || strings.HasSuffix(strings.TrimSpace(b), "%")
	magnitude := math.Max(math.Abs(af), math.Abs(bf))
	closeScaled := percent && math.Abs(af-bf) <= 8*(math.Nextafter(magnitude, math.Inf(1))-magnitude)
	if af < bf && !closeScaled {
		return -1
	}
	if af > bf && !closeScaled {
		return 1
	}
	if a == b {
		return 0
	}
	ar, aok := ExactNumericRat(a)
	br, bok := ExactNumericRat(b)
	if aok && bok {
		if strings.HasSuffix(strings.TrimSpace(a), "%") {
			ar.Quo(ar, big.NewRat(100, 1))
		}
		if strings.HasSuffix(strings.TrimSpace(b), "%") {
			br.Quo(br, big.NewRat(100, 1))
		}
		return ar.Cmp(br)
	}
	return 0
}

// The same comparator is used for resident chunks, run validation and merge
// heads. Categories stay in place when descending: blanks last, numbers before
// text, then the chosen direction within a category. Source order breaks ties.
func compareSortRecords(a, b sortRecord, keys []SortKey) int {
	for k, key := range keys {
		x, y := a.values[k], b.values[k]
		if x.Text == "" || y.Text == "" {
			if x.Text != y.Text {
				if x.Text == "" {
					return 1
				}
				return -1
			}
			continue
		}
		if x.IsNumber != y.IsNumber {
			if x.IsNumber {
				return -1
			}
			return 1
		}
		var cmp int
		if x.IsNumber {
			cmp = CompareSortNumeric(x.Text, y.Text, x.Number, y.Number)
		} else if key.Mode == "natural" {
			cmp = CompareNaturalText(x.Folded, y.Folded)
		} else {
			cmp = strings.Compare(x.Folded, y.Folded)
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
func CompareNaturalText(a, b string) int {
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

func SortValueBytes(key SortValue) int64 {
	n := int64(len(key.Text))
	if key.Folded != key.Text {
		n += int64(len(key.Folded))
	}
	return n
}

func sortRecordBytes(values []SortValue) int64 {
	n := int64(8 + len(values)*17) // record length, source index, key metadata
	for _, key := range values {
		n += SortValueBytes(key)
	}
	return n
}

func sortValueStorageBytes(key SortValue) int64 {
	// Cover allocation size classes as well as the literal UTF-8 bytes.
	charge := func(n int) int64 {
		if n == 0 {
			return 0
		}
		return int64(n) + int64(n)/4 + 16
	}
	n := charge(len(key.Text))
	if key.Folded != key.Text {
		n += charge(len(key.Folded))
	}
	return n
}

type RowSorter struct {
	dir         string
	sourceRows  int
	keys        []SortKey
	budget      int64
	maxRecord   int64
	bufferBytes int
	quota       *QueryQuota
	items       []sortItem
	values      []SortValue
	stringBytes int64
	count       int
	last        int
	runs        []*sortRun
	files       map[*QueryFile]struct{}
	prepared    bool
	finished    bool
}

func NewRowSorter(dir string, sourceRows int, keys []SortKey, budget int64, quota *QueryQuota) (*RowSorter, error) {
	if quota == nil {
		quota = &processQueryQuota
	}
	if sourceRows < 0 || len(keys) == 0 || len(keys) > 5 || budget < 32<<10 {
		return nil, errors.New("排序容量无效")
	}
	buffer := int(min(int64(64<<10), budget/64))
	perRow := sortItemMemory + int64(len(keys))*sortValueMemory
	// Two runs must always fit, including both reader key buffers and I/O.
	maxRecord := budget/5 - int64(buffer)*2 - perRow*2 - sortReaderMemory/2 - int64(len(keys))*64
	return &RowSorter{dir: dir, sourceRows: sourceRows, keys: append([]SortKey(nil), keys...), budget: budget, maxRecord: maxRecord,
		bufferBytes: buffer, quota: quota, last: -1, files: make(map[*QueryFile]struct{})}, nil
}

func (s *RowSorter) MaxRecord() int64 { return s.maxRecord }

func (s *RowSorter) Abort() {
	for file := range s.files {
		_ = file.Discard()
		delete(s.files, file)
	}
}

func (s *RowSorter) forget(file *QueryFile) error {
	err := file.Discard()
	delete(s.files, file) // failed deletions remain charged in the quota registry
	return err
}

func (s *RowSorter) chunkRecord(item sortItem) sortRecord {
	return sortRecord{index: item.index, values: s.values[item.offset : item.offset+len(s.keys)]}
}

// Explicit capacity growth accounts for old and new backing arrays together.
// A chunk spills before either string storage or metadata reaches the budget.
func (s *RowSorter) grow(incoming int64) bool {
	perRow := sortItemMemory + int64(len(s.keys))*sortValueMemory
	capacity := min(s.sourceRows, int(s.budget/3/perRow), max(64, cap(s.items)*2))
	if capacity <= cap(s.items) || int64(capacity+cap(s.items))*perRow+s.stringBytes+incoming > s.budget {
		return false
	}
	items := make([]sortItem, len(s.items), capacity)
	values := make([]SortValue, len(s.values), capacity*len(s.keys))
	copy(items, s.items)
	copy(values, s.values)
	s.items, s.values = items, values
	return true
}

func (s *RowSorter) Add(ctx context.Context, position int, values []SortValue) error {
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

func (s *RowSorter) sortChunk(ctx context.Context) (err error) {
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

func (s *RowSorter) spill(ctx context.Context) error {
	if len(s.items) == 0 {
		return ctx.Err()
	}
	if err := s.sortChunk(ctx); err != nil {
		return err
	}
	if !s.prepared {
		if err := s.quota.CleanStale(ctx, s.dir); err != nil {
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

func (s *RowSorter) groupSize(runs []*sortRun) int {
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

func (s *RowSorter) Finish(ctx context.Context, emit func(int) error, phase func(string, int, int)) error {
	return s.finishRecords(ctx, func(record sortRecord) error { return emit(record.index) }, phase)
}

func (s *RowSorter) finishRecords(ctx context.Context, emit func(sortRecord) error, phase func(string, int, int)) error {
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
