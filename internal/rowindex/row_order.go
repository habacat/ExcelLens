package rowindex

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"iter"
	"os"
	"runtime"
	"sort"
	"sync/atomic"

	"excellens/internal/formats"
)

const ResidentOrderRows = 262144
const orderHeaderBytes = 32
const orderMagic = "ELORD001"

type rowOrderData struct {
	refs       atomic.Int64
	count      int
	sourceRows int
	start      int // >=0: one contiguous interval; no per-row allocation
	rows       []uint32
	inverse    []uint64 // source position in high bits, visual position in low bits
	mapping    []byte
	file       *QueryFile
	increasing bool
}

// RowOrder is one lease on an immutable, unique sequence of source-index
// positions. Retain before handing it to another owner; Close releases only
// this lease. Large results are mapped, while a continuous range is implicit.
// Positions refer to RowIndex records, never to displayed/filtered row nums.
type RowOrder struct {
	data   *rowOrderData
	closed atomic.Bool
}

func ownRowOrder(data *rowOrderData) *RowOrder {
	data.refs.Store(1)
	r := &RowOrder{data: data}
	runtime.SetFinalizer(r, (*RowOrder).finalize)
	return r
}

func (r *RowOrder) finalize() { _ = r.Close() }

func (r *RowOrder) Retain() (*RowOrder, error) {
	if r == nil || r.closed.Load() {
		return nil, errors.New("行序已经释放")
	}
	for {
		old := r.data.refs.Load()
		if old <= 0 {
			return nil, errors.New("行序已经释放")
		}
		if r.data.refs.CompareAndSwap(old, old+1) {
			next := &RowOrder{data: r.data}
			runtime.SetFinalizer(next, (*RowOrder).finalize)
			runtime.KeepAlive(r)
			return next, nil
		}
	}
}

func (r *RowOrder) Close() error {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	if r.data.refs.Add(-1) != 0 {
		return nil
	}
	if len(r.data.mapping) != 0 {
		if err := UnmapFile(r.data.mapping); err != nil {
			return err
		}
		r.data.mapping = nil
	}
	if r.data.file != nil {
		return r.data.file.Discard()
	}
	r.data.rows, r.data.inverse = nil, nil
	return nil
}

func (r *RowOrder) Len() int {
	if r == nil {
		return 0
	}
	return r.data.count
}

func (r *RowOrder) Increasing() bool {
	return r != nil && r.data.increasing
}

// RetainedHeapBytes estimates immutable Go storage for one complete order,
// shared by its leases. Mapped pages are covered by the query file budget;
// this is not a bound on total process or operating-system resident memory.
func (r *RowOrder) RetainedHeapBytes() (int64, error) {
	borrowed, err := r.Retain()
	if err != nil {
		return 0, err
	}
	defer borrowed.Close()
	d := borrowed.data
	return 1024 + int64(cap(d.rows))*5 + int64(cap(d.inverse))*10, nil
}

func (r *RowOrder) SourceRows() int {
	if r == nil {
		return 0
	}
	return r.data.sourceRows
}

func (r *RowOrder) At(i int) int {
	if r == nil || r.closed.Load() || i < 0 || i >= r.data.count {
		panic("invalid row-order position or closed lease")
	}
	d := r.data
	var value int
	if d.start >= 0 {
		value = d.start + i
	} else if d.mapping != nil {
		value = int(binary.LittleEndian.Uint32(d.mapping[orderHeaderBytes+i*4:]))
	} else {
		value = int(d.rows[i])
	}
	runtime.KeepAlive(r)
	return value
}

func (r *RowOrder) inverseAt(i int) uint64 {
	var value uint64
	if r.data.mapping != nil {
		offset := orderHeaderBytes + r.data.count*4 + i*8
		value = binary.LittleEndian.Uint64(r.data.mapping[offset:])
	} else {
		value = r.data.inverse[i]
	}
	runtime.KeepAlive(r)
	return value
}

func (r *RowOrder) Position(source int) (int, bool) {
	set := r.Set()
	at := set.LowerBound(source)
	if at >= set.Len() || set.At(at) != source {
		return 0, false
	}
	if r.data.increasing {
		return at, true
	}
	return int(uint32(r.inverseAt(at))), true
}

// Page copies just one bounded viewport. Iter is for streaming full exports.
func (r *RowOrder) Page(offset, limit int) ([]int, error) {
	if r == nil || r.closed.Load() || offset < 0 || offset > r.Len() || limit < 0 || limit > 10000 {
		return nil, errors.New("行序读取范围无效")
	}
	count := min(limit, r.Len()-offset)
	rows := make([]int, count)
	for i := range rows {
		rows[i] = r.At(offset + i)
	}
	return rows, nil
}

func (r *RowOrder) Iter() iter.Seq[int] {
	return r.IterRange(0, r.Len())
}

// IterRange visits a bounded portion without scanning an earlier prefix.
// The caller retains its lease for the entire iteration.
func (r *RowOrder) IterRange(offset, count int) iter.Seq[int] {
	return func(yield func(int) bool) {
		if offset < 0 || count < 0 || offset > r.Len() || count > r.Len()-offset {
			panic("invalid row-order iteration range")
		}
		defer runtime.KeepAlive(r)
		for i := offset; i < offset+count; i++ {
			if !yield(r.At(i)) {
				return
			}
		}
	}
}

// RowSet is the source-ordered membership view of a RowOrder. It borrows the
// order's lease; it does not duplicate rows or allocate a source-sized bitset.
type RowSet struct{ order *RowOrder }

func (r *RowOrder) Set() RowSet { return RowSet{order: r} }
func (s RowSet) Len() int       { return s.order.Len() }
func (s RowSet) At(i int) int {
	if s.order == nil || s.order.closed.Load() || i < 0 || i >= s.Len() {
		panic("invalid row-set position or closed lease")
	}
	if s.order.data.increasing {
		return s.order.At(i)
	}
	return int(s.order.inverseAt(i) >> 32)
}
func (s RowSet) LowerBound(source int) int {
	if s.order != nil && s.order.data.start >= 0 {
		return min(s.Len(), max(0, source-s.order.data.start))
	}
	return sort.Search(s.Len(), func(i int) bool { return s.At(i) >= source })
}

// NewRowOrder copies caller-owned positions through the same bounded builder
// used by filtering and external sorting. Mutating the input cannot alter it.
func NewRowOrder(ctx context.Context, dir string, sourceRows int, positions []int) (*RowOrder, error) {
	return BuildRowOrder(ctx, dir, sourceRows, func(ctx context.Context, add func(int) error) error {
		for _, position := range positions {
			if err := add(position); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

// BuildRowOrder consumes positions without constructing a whole-table slice.
// It shares the existing checked builder, inverse index and process disk quota.
// An upstream error never publishes a partial order, even after spilling.
func BuildRowOrder(ctx context.Context, dir string, sourceRows int, scan func(context.Context, func(int) error) error) (*RowOrder, error) {
	if scan == nil {
		return nil, errors.New("行序来源读取器缺失")
	}
	b, err := NewRowOrderBuilder(dir, sourceRows, ResidentOrderRows, &processQueryQuota)
	if err != nil {
		return nil, err
	}
	defer b.Abort()
	var admission error
	err = scan(ctx, func(position int) error {
		if admission == nil {
			admission = ctx.Err()
			if admission == nil {
				admission = b.Add(ctx, position)
			}
		}
		return admission
	})
	if err != nil || admission != nil {
		return nil, errors.Join(err, admission)
	}
	return b.Finish(ctx)
}

type RowOrderBuilder struct {
	dir        string
	sourceRows int
	resident   int
	quota      *QueryQuota
	count      int
	first      int
	last       int
	contiguous bool
	increasing bool
	rows       []uint32
	file       *QueryFile
	out        *bufio.Writer
	crc        hash.Hash32
	finished   bool
}

func NewRowOrderBuilder(dir string, sourceRows, resident int, quota *QueryQuota) (*RowOrderBuilder, error) {
	if quota == nil {
		quota = &processQueryQuota
	}
	if sourceRows < 0 || sourceRows > formats.ExternalRows || resident < 1 {
		return nil, errors.New("行序容量无效")
	}
	return &RowOrderBuilder{dir: dir, sourceRows: sourceRows, resident: resident, quota: quota, first: -1, last: -1, contiguous: true, increasing: true}, nil
}

func (b *RowOrderBuilder) Abort() {
	if b.file != nil {
		_ = b.file.Discard()
		b.file = nil
	}
}

func (b *RowOrderBuilder) spill(ctx context.Context) error {
	if err := b.quota.CleanStale(ctx, b.dir); err != nil {
		return err
	}
	f, err := newQueryFile(b.dir, b.quota)
	if err != nil {
		return err
	}
	b.file = f
	if _, err := f.Write(make([]byte, orderHeaderBytes)); err != nil {
		return err
	}
	b.crc = crc32.NewIEEE()
	b.out = bufio.NewWriterSize(io.MultiWriter(f, b.crc), 64<<10)
	for i := 0; i < b.count; i++ {
		if i%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		value := b.first + i
		if b.rows != nil {
			value = int(b.rows[i])
		}
		if err := b.writePosition(value); err != nil {
			return err
		}
	}
	b.rows = nil
	return nil
}

func (b *RowOrderBuilder) writePosition(position int) error {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(position))
	_, err := b.out.Write(raw[:])
	return err
}

func (b *RowOrderBuilder) Add(ctx context.Context, position int) error {
	if b.finished || position < 0 || position >= b.sourceRows || b.count >= b.sourceRows {
		return errors.New("行序源位置无效或重复")
	}
	if b.count == 0 {
		b.first, b.last, b.count = position, position, 1
		return nil
	}
	if position == b.last {
		return errors.New("行序不能包含重复源位置")
	}
	if position < b.last {
		b.increasing = false
	}
	if b.contiguous && position == b.last+1 {
		b.count++
		b.last = position
		return nil
	}
	if b.contiguous {
		if b.count >= b.resident {
			if err := b.spill(ctx); err != nil {
				return err
			}
		} else {
			b.rows = make([]uint32, b.count, min(b.resident, max(4096, b.count+1)))
			for i := range b.rows {
				b.rows[i] = uint32(b.first + i)
			}
		}
		b.contiguous = false
	}
	if b.file == nil && b.count >= b.resident {
		if err := b.spill(ctx); err != nil {
			return err
		}
	}
	if b.file != nil {
		if err := b.writePosition(position); err != nil {
			return err
		}
	} else {
		b.rows = append(b.rows, uint32(position))
	}
	b.count++
	b.last = position
	return nil
}

func (b *RowOrderBuilder) Finish(ctx context.Context) (*RowOrder, error) {
	if b.finished {
		return nil, errors.New("行序已经发布")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := &rowOrderData{count: b.count, sourceRows: b.sourceRows, start: -1, increasing: b.increasing}
	if b.contiguous {
		d.start = max(b.first, 0)
		b.finished = true
		return ownRowOrder(d), nil
	}
	if b.file == nil {
		d.rows = append([]uint32(nil), b.rows...)
		if !b.increasing {
			d.inverse = make([]uint64, len(d.rows))
			for i, source := range d.rows {
				d.inverse[i] = uint64(source)<<32 | uint64(uint32(i))
			}
			sort.Slice(d.inverse, func(i, j int) bool { return d.inverse[i] < d.inverse[j] })
			for i := 1; i < len(d.inverse); i++ {
				if d.inverse[i]>>32 == d.inverse[i-1]>>32 {
					return nil, errors.New("行序不能包含重复源位置")
				}
			}
		}
		b.finished = true
		return ownRowOrder(d), nil
	}
	if err := b.out.Flush(); err != nil {
		return nil, err
	}
	if !b.increasing {
		if err := b.appendInverse(ctx); err != nil {
			return nil, err
		}
	}
	if err := b.out.Flush(); err != nil {
		return nil, err
	}
	var header [orderHeaderBytes]byte
	copy(header[:], orderMagic)
	binary.LittleEndian.PutUint32(header[8:], uint32(b.sourceRows))
	binary.LittleEndian.PutUint32(header[12:], uint32(b.count))
	if b.increasing {
		binary.LittleEndian.PutUint32(header[16:], 1)
	}
	binary.LittleEndian.PutUint32(header[24:], b.crc.Sum32())
	binary.LittleEndian.PutUint32(header[28:], crc32.ChecksumIEEE(header[:28]))
	if _, err := b.file.file.WriteAt(header[:], 0); err != nil {
		return nil, err
	}
	if err := b.file.file.Sync(); err != nil {
		return nil, err
	}
	if err := b.file.Close(); err != nil {
		return nil, err
	}
	mapping, err := mapOrderFile(ctx, b.file.path, b.sourceRows, b.count, b.increasing)
	if err != nil {
		return nil, err
	}
	d.mapping, d.file = mapping, b.file
	b.file = nil // ownership transferred only after full validation
	b.finished = true
	return ownRowOrder(d), nil
}

func mapOrderFile(ctx context.Context, path string, sourceRows, count int, increasing bool) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	width, flag := int64(12), uint32(0)
	if increasing {
		width, flag = 4, 1
	}
	if sourceRows < 0 || sourceRows > formats.ExternalRows || count < 0 || count > sourceRows || info.Size() != orderHeaderBytes+int64(count)*width {
		return nil, errors.New("行序文件长度或容量无效")
	}
	var header [orderHeaderBytes]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, err
	}
	if string(header[:8]) != orderMagic || binary.LittleEndian.Uint32(header[8:]) != uint32(sourceRows) ||
		binary.LittleEndian.Uint32(header[12:]) != uint32(count) || binary.LittleEndian.Uint32(header[16:]) != flag ||
		binary.LittleEndian.Uint32(header[20:]) != 0 || binary.LittleEndian.Uint32(header[28:]) != crc32.ChecksumIEEE(header[:28]) {
		return nil, errors.New("行序文件头校验失败")
	}
	checksum := crc32.NewIEEE()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buffer)
		_, _ = checksum.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if checksum.Sum32() != binary.LittleEndian.Uint32(header[24:]) {
		return nil, errors.New("行序文件内容校验失败")
	}
	return MapFile(f, info.Size())
}
