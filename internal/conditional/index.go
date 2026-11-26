// Package conditional stores immutable, worksheet-scoped conditional statistics.
// Workbooks supply typed grouping keys and source rows without display styles.
package conditional

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
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

// Each viewport reuses one read-only transaction on the immutable file.
type Index struct {
	mu sync.Mutex
	db *bolt.DB
	tx *bolt.Tx
}

func (i *Index) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.tx == nil {
		return nil
	}
	err := i.tx.Rollback()
	i.tx = nil
	return errors.Join(err, i.db.Close())
}
func (i *Index) view(read func(*bolt.Tx) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.tx == nil {
		return errors.New("条件统计读取已结束")
	}
	return read(i.tx)
}

var slots [32]chan struct{}

func init() {
	for i := range slots {
		slots[i] = make(chan struct{}, 1)
	}
}

var meta = []byte("metadata")
var errIncomplete = errors.New("条件统计索引不完整")
var errDiskLimit = errors.New("条件统计索引达到磁盘容量上限")

func bucketID(id int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(id)) }
func rowKey(row int) []byte  { return binary.BigEndian.AppendUint32(nil, uint32(row)) }
func openRead(file, key string, plans int) (*Index, error) {
	db, err := bolt.Open(file, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(meta)
		for id := 0; id < plans; id++ {
			p := tx.Bucket(bucketID(id))
			if p == nil || p.Bucket([]byte("values")) == nil || p.Bucket([]byte("issues")) == nil {
				return errIncomplete
			}
		}
		if b == nil || !bytes.Equal(b.Get([]byte("complete")), []byte(key)) {
			return errIncomplete
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	tx, err := db.Begin(false)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db, tx: tx}, nil
}

// Open publishes a completed temporary database on the same volume. Readers
// that race a cancelled build retry with their own context.
func Open(ctx context.Context, file, key string, plans int, build func(*Writer) error) (*Index, bool, error) {
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
	if st, err := os.Stat(file); err == nil {
		if !st.Mode().IsRegular() {
			return nil, false, errors.New("条件统计缓存不是普通文件")
		}
		if st.Size() > formats.ConditionalStatsDiskBytes {
			return nil, false, errDiskLimit
		}
		if st.Size() > 0 {
			i, err := openRead(file, key, plans)
			if err == nil {
				return i, false, nil
			}
			if !errors.Is(err, errIncomplete) && !errors.Is(err, bolt.ErrInvalid) && !errors.Is(err, bolt.ErrChecksum) && !errors.Is(err, bolt.ErrVersionMismatch) {
				return nil, false, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".conditional-*.tmp")
	if err != nil {
		return nil, false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Close(); err != nil {
		return nil, false, err
	}
	db, err := bolt.Open(tmp, 0600, &bolt.Options{NoSync: true})
	if err != nil {
		return nil, false, err
	}
	err = writeIndex(ctx, db, key, plans, build)
	closeErr := db.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, false, err
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	if err = os.Rename(tmp, file); err != nil {
		if i, reopen := openRead(file, key, plans); reopen == nil {
			return i, false, nil
		}
		return nil, false, err
	}
	i, err := openRead(file, key, plans)
	return i, true, err
}

type Writer struct {
	db           *bolt.DB
	tx           *bolt.Tx
	ctx          context.Context
	rows         int
	sums         []*big.Rat
	absolute     []*big.Rat
	denBits      []int
	unsafePrefix []bool
}

func writeIndex(ctx context.Context, db *bolt.DB, key string, plans int, build func(*Writer) error) error {
	w := &Writer{ctx: ctx, db: db, sums: make([]*big.Rat, plans), absolute: make([]*big.Rat, plans), denBits: make([]int, plans), unsafePrefix: make([]bool, plans)}
	var err error
	w.tx, err = db.Begin(true)
	if err != nil {
		return err
	}
	defer func() {
		if w.tx != nil {
			w.tx.Rollback()
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
		if _, err = b.CreateBucket([]byte("values")); err != nil {
			return err
		}
		if _, err = b.CreateBucket([]byte("issues")); err != nil {
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
	if err = w.tx.Bucket(meta).Put([]byte("complete"), []byte(key)); err != nil {
		return err
	}
	if err = w.commit(); err != nil {
		return err
	}
	return db.Sync()
}
func (w *Writer) commit() error {
	if w.tx.Size() > formats.ConditionalStatsDiskBytes {
		return errDiskLimit
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
	if st.Size() > formats.ConditionalStatsDiskBytes {
		return errDiskLimit
	}
	return nil
}
func (w *Writer) Next() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.rows++
	if w.rows%2048 != 0 {
		return nil
	}
	if err := w.commit(); err != nil {
		return err
	}
	var err error
	w.tx, err = w.db.Begin(true)
	return err
}
func encode(r Result) []byte {
	b := binary.AppendUvarint(nil, uint64(r.Count))
	b = binary.AppendUvarint(b, uint64(r.First))
	if r.Fault != "" {
		if r.Unknown {
			b = append(b, 'u')
		} else {
			b = append(b, 'e')
		}
		return append(b, r.Fault...)
	}
	b = append(b, 'n')
	if r.Number != nil && r.Number.Sign() != 0 {
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
func (w *Writer) Group(id int, key []byte, row, weight int, v Value) error {
	b := w.tx.Bucket(bucketID(id)).Bucket([]byte("values"))
	if len(key) > bolt.MaxKeySize {
		return w.Issue(id, row, Value{Unknown: true, Fault: "条件统计组合键超过容量"})
	}
	old, err := decode(b.Get(key))
	if err != nil {
		return err
	}
	if old.First == 0 {
		old.First = row
	}
	old.Count += weight
	if v.Fault != "" && (old.Fault == "" || v.Unknown && !old.Unknown) {
		old.Value = v
	}
	if v.Number != nil && old.Fault == "" {
		if old.Number == nil {
			old.Number = new(big.Rat)
		}
		if !excelformula.AddFloat(old.Number, v.Number) {
			old.Fault = "#NUM!"
			old.Number = nil
		}
	}
	return b.Put(key, encode(old))
}
func (w *Writer) Issue(id, row int, v Value) error {
	return w.tx.Bucket(bucketID(id)).Bucket([]byte("issues")).Put(rowKey(row), encode(Result{First: row, Value: v}))
}
func (w *Writer) Prefix(id, row int, v Value) error {
	if v.Fault != "" {
		return w.Issue(id, row, v)
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
	return w.tx.Bucket(bucketID(id)).Bucket([]byte("values")).Put(rowKey(row), encode(Result{Value: Value{Number: sum}}))
}
func (i *Index) Lookup(id int, key []byte, match bool) (Result, error) {
	var r Result
	err := i.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketID(id))
		if b == nil {
			return errors.New("条件统计方案不存在")
		}
		var err error
		r, err = decode(b.Bucket([]byte("values")).Get(key))
		if err != nil {
			return err
		}
		pos, v := b.Bucket([]byte("issues")).Cursor().First()
		if pos != nil && (!match || r.First == 0 || bytes.Compare(pos, rowKey(r.First)) <= 0) {
			r, err = decode(v)
		}
		return err
	})
	return r, err
}
func (i *Index) Sum(id, from, to int) (Result, error) {
	var r Result
	err := i.view(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketID(id))
		if b == nil {
			return errors.New("条件统计方案不存在")
		}
		k, v := b.Bucket([]byte("issues")).Cursor().Seek(rowKey(from))
		if k != nil && bytes.Compare(k, rowKey(to)) <= 0 {
			var err error
			r, err = decode(v)
			return err
		}
		at := func(row int) (*big.Rat, error) {
			cur := b.Bucket([]byte("values")).Cursor()
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
		if b.Get([]byte("ordered")) != nil {
			// Floating-point addition is not associative. For a moving window that
			// needs binary rounding, recover only its occupied terms from the index.
			cur := b.Bucket([]byte("values")).Cursor()
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
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	if len(b) == 0 {
		return []byte{0}
	}
	return b
}
