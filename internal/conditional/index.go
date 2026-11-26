// Package conditional owns conditional formatting expression parsing, pure
// evaluation, cost-based strategy planning, sliding-window aggregation, and
// immutable worksheet-scoped conditional statistics disk caches.
package conditional

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"excellens/internal/excelformula"
	"excellens/internal/formats"

	bolt "go.etcd.io/bbolt"
)

type Value struct {
	Number  *big.Rat
	Fault   string
	Unknown bool
}

type Result struct {
	Count, First int
	Value
}

// IndexState classifies the on-disk state of a conditional statistics file.
type IndexState uint8

const (
	StateReady IndexState = iota + 1
	StateMissing
	StateDamaged
)

// Each viewport reuses one read-only transaction on the immutable file.
type indexPlan struct {
	values, issues *bolt.Bucket
	firstIssue     int
	issue          Result
	ordered        bool
}

type Index struct {
	plans []indexPlan
	mu    sync.Mutex
	path  string
	db    *bolt.DB
	tx    *bolt.Tx
}

func (i *Index) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.tx == nil {
		return nil
	}
	err := i.tx.Rollback()
	i.tx = nil
	i.plans = nil
	closeErr := i.db.Close()
	if i.path != "" {
		unpinReader(i.path)
		i.path = ""
	}
	return errors.Join(err, closeErr)
}

func (i *Index) view(id int, read func(*indexPlan) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.tx == nil {
		return errors.New("条件统计读取已结束")
	}
	if id < 0 || id >= len(i.plans) {
		return errors.New("条件统计方案不存在")
	}
	return read(&i.plans[id])
}

var slots [32]chan struct{}

func init() {
	for i := range slots {
		slots[i] = make(chan struct{}, 1)
	}
}

var meta = []byte("metadata")
var errIncomplete = errors.New("条件统计索引不完整")

var (
	ErrMissing        = errors.New("条件统计索引尚未建立")
	ErrDamaged        = errors.New("条件统计索引已损坏")
	ErrDiskLimit      = errors.New("条件统计索引达到磁盘容量上限")
	ErrBudgetExceeded = errors.New("条件统计缓存达到全局容量预算，可在「本地数据」中调高上限或清理闲置缓存")
)

func bucketID(id int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(id)) }
func rowKey(row int) []byte  { return binary.BigEndian.AppendUint32(nil, uint32(row)) }

func isDamagedBoltError(err error) bool {
	return errors.Is(err, errIncomplete) ||
		errors.Is(err, bolt.ErrInvalid) ||
		errors.Is(err, bolt.ErrChecksum) ||
		errors.Is(err, bolt.ErrVersionMismatch) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func openRead(file, key string, plans int) (*Index, error) {
	pinReader(file)
	db, err := bolt.Open(file, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		unpinReader(file)
		return nil, err
	}
	tx, err := db.Begin(false)
	if err != nil {
		_ = db.Close()
		unpinReader(file)
		return nil, err
	}
	idx := &Index{path: file, db: db, tx: tx}
	b := tx.Bucket(meta)
	if b == nil || !bytes.Equal(b.Get([]byte("complete")), []byte(key)) {
		_ = idx.Close()
		return nil, errIncomplete
	}
	idx.plans = make([]indexPlan, plans)
	for id := range idx.plans {
		bucket := tx.Bucket(bucketID(id))
		if bucket == nil {
			_ = idx.Close()
			return nil, errIncomplete
		}
		p := &idx.plans[id]
		p.values, p.issues = bucket.Bucket([]byte("values")), bucket.Bucket([]byte("issues"))
		if p.values == nil || p.issues == nil {
			_ = idx.Close()
			return nil, errIncomplete
		}
		p.ordered = bucket.Get([]byte("ordered")) != nil
		if pos, value := p.issues.Cursor().First(); pos != nil {
			if len(pos) != 4 {
				_ = idx.Close()
				return nil, errIncomplete
			}
			p.firstIssue = int(binary.BigEndian.Uint32(pos))
			p.issue, err = decode(value)
			if err != nil || p.firstIssue != p.issue.First || p.firstIssue < 1 || p.issue.Fault == "" {
				_ = idx.Close()
				return nil, errIncomplete
			}
		}
	}
	return idx, nil
}

// OpenExisting opens a verified read-only statistics index without ever triggering
// a rebuild. Foreground viewport reads use this so damaged or absent caches never
// stall scrolling.
func OpenExisting(file, key string, plans int) (*Index, error) {
	st, err := os.Stat(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrMissing
		}
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("条件统计缓存不是普通文件")
	}
	if st.Size() == 0 {
		return nil, ErrDamaged
	}
	if st.Size() > formats.ConditionalStatsDiskBytes {
		return nil, ErrDiskLimit
	}
	idx, err := openRead(file, key, plans)
	if err != nil {
		if isDamagedBoltError(err) {
			return nil, ErrDamaged
		}
		return nil, err
	}
	touchAccess(file, st)
	return idx, nil
}

// Check verifies whether file is present, readable, and matches key and plans.
// Zero-byte or corrupted files are reported as StateDamaged so background tasks
// rebuild them cleanly before foreground reads depend on them.
func Check(file, key string, plans int) (IndexState, error) {
	idx, err := OpenExisting(file, key, plans)
	if err == nil {
		_ = idx.Close()
		return StateReady, nil
	}
	if errors.Is(err, ErrMissing) {
		return StateMissing, nil
	}
	if errors.Is(err, ErrDamaged) {
		return StateDamaged, nil
	}
	return StateMissing, err
}

// Open publishes a completed temporary database on the same volume without a
// cross-workbook budget cap. Call Build to enforce global cache capacity budgets.
func Open(ctx context.Context, file, key string, plans int, build func(*Writer) error) (*Index, bool, error) {
	return Build(ctx, file, key, plans, build, nil)
}

// Build verifies an existing index or builds and atomically publishes a new one
// while enforcing single-file and optional cross-workbook cumulative capacity budgets.
func Build(ctx context.Context, file, key string, plans int, build func(*Writer) error, policy *BudgetPolicy) (*Index, bool, error) {
	slot := slots[crc32.ChecksumIEEE([]byte(file))%uint32(len(slots))]
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-slot }()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if idx, err := OpenExisting(file, key, plans); err == nil {
		return idx, false, nil
	} else if !errors.Is(err, ErrMissing) && !errors.Is(err, ErrDamaged) {
		return nil, false, err
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, false, err
	}
	pinBuild(file)
	defer unpinBuild(file)

	cacheMu.Lock()
	f, err := os.CreateTemp(dir, ".conditional-*.tmp")
	if err == nil {
		activeBuilds[cacheFileKey(f.Name())]++
	}
	cacheMu.Unlock()
	if err != nil {
		return nil, false, err
	}
	tmp := f.Name()
	defer func() {
		_ = os.Remove(tmp)
		unpinBuild(tmp)
	}()
	if err = f.Close(); err != nil {
		return nil, false, err
	}
	maxBytes := formats.ConditionalStatsDiskBytes
	if policy != nil && policy.BudgetBytes > 0 && policy.BudgetBytes < maxBytes {
		maxBytes = policy.BudgetBytes
	}
	db, err := bolt.Open(tmp, 0600, &bolt.Options{NoSync: true})
	if err != nil {
		return nil, false, err
	}
	err = writeIndex(ctx, db, key, plans, maxBytes, build)
	closeErr := db.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if policy != nil && policy.BudgetBytes > 0 && errors.Is(err, ErrDiskLimit) && maxBytes == policy.BudgetBytes && policy.BudgetBytes < formats.ConditionalStatsDiskBytes {
			return nil, false, ErrBudgetExceeded
		}
		return nil, false, err
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return nil, false, err
	}
	if err = publishIndex(ctx, tmp, file, st.Size(), policy); err != nil {
		if ctx.Err() == nil {
			if idx, reopen := OpenExisting(file, key, plans); reopen == nil {
				return idx, false, nil
			}
		}
		return nil, false, err
	}
	idx, err := OpenExisting(file, key, plans)
	return idx, true, err
}

// Keep the budget decision and publication atomic across independent sheet builders.
func publishIndex(ctx context.Context, tmp, file string, size int64, policy *BudgetPolicy) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if policy != nil && policy.BudgetBytes > 0 {
		effective := *policy
		if effective.Dir == "" {
			effective.Dir = filepath.Dir(file)
		}
		if _, err := enforceBudgetLocked(effective, size, filepath.Base(file)); err != nil {
			return err
		}
	}
	return os.Rename(tmp, file)
}

type Writer struct {
	values, issues         []*bolt.Bucket
	groups                 []map[string]*groupedValue
	groupBytes, groupLimit int
	dirty                  bool
	db                     *bolt.DB
	tx                     *bolt.Tx
	ctx                    context.Context
	maxBytes               int64
	rows                   int
	sums                   []*big.Rat
	absolute               []*big.Rat
	denBits                []int
	unsafePrefix           []bool
}

func writeIndex(ctx context.Context, db *bolt.DB, key string, plans int, maxBytes int64, build func(*Writer) error) error {
	if maxBytes <= 0 {
		maxBytes = formats.ConditionalStatsDiskBytes
	}
	w := &Writer{values: make([]*bolt.Bucket, plans), issues: make([]*bolt.Bucket, plans), groups: make([]map[string]*groupedValue, plans), groupLimit: formats.ConditionalStatsGroupBufferBytes, dirty: true, ctx: ctx, db: db, maxBytes: maxBytes, sums: make([]*big.Rat, plans), absolute: make([]*big.Rat, plans), denBits: make([]int, plans), unsafePrefix: make([]bool, plans)}
	var err error
	w.tx, err = db.Begin(true)
	if err != nil {
		return err
	}
	defer func() {
		if w.tx != nil {
			_ = w.tx.Rollback()
		}
	}()
	if _, err = w.tx.CreateBucket(meta); err != nil {
		return err
	}
	for i := 0; i < plans; i++ {
		b, err := w.tx.CreateBucket(bucketID(i))
		if err != nil {
			return err
		}
		if w.values[i], err = b.CreateBucket([]byte("values")); err != nil {
			return err
		}
		if w.issues[i], err = b.CreateBucket([]byte("issues")); err != nil {
			return err
		}
		w.sums[i] = new(big.Rat)
		w.absolute[i] = new(big.Rat)
	}
	if err = build(w); err != nil {
		return err
	}
	if err = w.ctx.Err(); err != nil {
		return err
	}
	if err = w.flushGroups(); err != nil {
		return err
	}
	if err = w.tx.Bucket(meta).Put([]byte("complete"), []byte(key)); err != nil {
		return err
	}
	if err = w.commit(); err != nil {
		return err
	}
	return db.Sync()
}

func (w *Writer) commit() error {
	if w.tx.Size() > w.maxBytes {
		return ErrDiskLimit
	}
	err := w.tx.Commit()
	w.tx = nil
	if err != nil {
		return err
	}
	// Page allocation happens during Commit and may preallocate extra space.
	st, err := os.Stat(w.db.Path())
	if err != nil {
		return err
	}
	if st.Size() > w.maxBytes {
		return ErrDiskLimit
	}
	return nil
}

func (w *Writer) nextRow() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.rows++
	if w.rows%2048 != 0 || !w.dirty {
		return nil
	}
	return w.rotate()
}

func (w *Writer) rotate() error {
	if err := w.commit(); err != nil {
		return err
	}
	var err error
	w.tx, err = w.db.Begin(true)
	if err != nil {
		return err
	}
	for id := range w.values {
		plan := w.tx.Bucket(bucketID(id))
		w.values[id] = plan.Bucket([]byte("values"))
		w.issues[id] = plan.Bucket([]byte("issues"))
	}
	w.dirty = false
	return nil
}

// All v1 records, including buffered groups, share this wire header.
func recordHeader(count, first int, fault string, unknown bool) []byte {
	b := binary.AppendUvarint(nil, uint64(count))
	b = binary.AppendUvarint(b, uint64(first))
	if fault != "" {
		if unknown {
			b = append(b, 'u')
		} else {
			b = append(b, 'e')
		}
		return append(b, fault...)
	}
	return append(b, 'n')
}

func encode(r Result) []byte {
	b := recordHeader(r.Count, r.First, r.Fault, r.Unknown)
	if r.Fault == "" && r.Number != nil && r.Number.Sign() != 0 {
		b = append(b, r.Number.RatString()...)
	}
	return b
}

func decode(b []byte) (Result, error) {
	if b == nil {
		return Result{}, nil
	}
	count, n := binary.Uvarint(b)
	if n <= 0 {
		return Result{}, errors.New("条件统计计数损坏")
	}
	b = b[n:]
	first, n := binary.Uvarint(b)
	if n <= 0 || len(b) <= n || count > formats.ExcelRows || first > formats.ExcelRows {
		return Result{}, errors.New("条件统计位置损坏")
	}
	b = b[n:]
	r := Result{Count: int(count), First: int(first)}
	switch b[0] {
	case 'e', 'u':
		r.Fault = string(b[1:])
		r.Unknown = b[0] == 'u'
	case 'n':
		if len(b) > 1 {
			var ok bool
			r.Number, ok = new(big.Rat).SetString(string(b[1:]))
			if !ok {
				return Result{}, errors.New("条件统计数值损坏")
			}
		}
	default:
		return Result{}, errors.New("条件统计值无效")
	}
	return r, nil
}

func (w *Writer) recordIssue(id, row int, v Value) error {
	w.dirty = true
	return w.issues[id].Put(rowKey(row), encode(Result{First: row, Value: v}))
}

func (w *Writer) addPrefix(id, row int, v Value) error {
	if v.Fault != "" {
		return w.recordIssue(id, row, v)
	}
	if v.Number == nil || v.Number.Sign() == 0 {
		return nil
	}
	// The difference of exact prefixes reproduces ordered binary addition
	// when every partial sum is representable on the same binary grid.
	if !w.unsafePrefix[id] {
		x := v.Number
		den := x.Denom()
		w.denBits[id] = max(w.denBits[id], den.BitLen()-1)
		total := w.absolute[id]
		total.Add(total, new(big.Rat).Abs(x))
		if den.BitLen() != int(den.TrailingZeroBits())+1 || total.Num().BitLen()+w.denBits[id]-(total.Denom().BitLen()-1) > 53 {
			w.unsafePrefix[id] = true
			if err := w.tx.Bucket(bucketID(id)).Put([]byte("ordered"), []byte{1}); err != nil {
				return err
			}
		}
	}
	sum := w.sums[id]
	sum.Add(sum, v.Number)
	w.dirty = true
	return w.values[id].Put(rowKey(row), encode(Result{Value: Value{Number: sum}}))
}

func (i *Index) Lookup(id int, key []byte, match bool) (Result, error) {
	var r Result
	err := i.view(id, func(p *indexPlan) error {
		var err error
		r, err = decode(p.values.Get(key))
		if err != nil {
			return err
		}
		if p.firstIssue > 0 && (!match || r.First == 0 || p.firstIssue <= r.First) {
			r = p.issue
		}
		return err
	})
	return r, err
}

func (i *Index) Sum(id, from, to int) (Result, error) {
	var r Result
	err := i.view(id, func(p *indexPlan) error {
		if p.firstIssue > 0 && p.firstIssue <= to {
			if p.firstIssue >= from {
				r = p.issue
				return nil
			}
			k, v := p.issues.Cursor().Seek(rowKey(from))
			if k != nil && bytes.Compare(k, rowKey(to)) <= 0 {
				var err error
				r, err = decode(v)
				return err
			}
		}
		at := func(row int) (*big.Rat, error) {
			cur := p.values.Cursor()
			k, v := cur.Seek(rowKey(row))
			if k == nil {
				k, v = cur.Last()
			} else if !bytes.Equal(k, rowKey(row)) {
				_, v = cur.Prev()
			}
			x, err := decode(v)
			if x.Number == nil {
				x.Number = new(big.Rat)
			}
			return x.Number, err
		}
		lo, err := at(from - 1)
		if err != nil {
			return err
		}
		if p.ordered {
			// Floating-point addition is not associative. For a moving window that
			// needs binary rounding, recover only its occupied terms from the index.
			cur := p.values.Cursor()
			sum := new(big.Rat)
			limit := rowKey(to)
			work := 0
			for k, v := cur.Seek(rowKey(from)); k != nil && bytes.Compare(k, limit) <= 0; k, v = cur.Next() {
				work++
				if work > formats.ConditionalRangeCells {
					return errors.New("滚动求和的精度复核超过单次计算容量")
				}
				next, err := decode(v)
				if err != nil {
					return err
				}
				if next.Number == nil {
					next.Number = new(big.Rat)
				}
				term := new(big.Rat).Sub(next.Number, lo)
				lo = next.Number
				if !excelformula.AddFloat(sum, term) {
					r.Fault = "#NUM!"
					return nil
				}
			}
			r.Number = sum
			return nil
		}
		hi, err := at(to)
		if err != nil {
			return err
		}
		r.Number = hi.Sub(hi, lo)
		return nil
	})
	return r, err
}

// Tuple preserves field boundaries for compound keys and empty fields.
func Tuple(parts []string) []byte {
	var b []byte
	for _, s := range parts {
		b = appendKeyPart(b, s)
	}
	if len(b) == 0 {
		return []byte{0}
	}
	return b
}
