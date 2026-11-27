package rowindex

import (
	"context"
	"encoding/binary"
	"errors"
	"iter"
	"math/bits"
	"runtime"
	"sort"
	"sync/atomic"

	"excellens/internal/formats"
)

// A projection selects positions of an existing immutable RowOrder. It does
// not duplicate its source-position vector or rebuild its reverse index.
// Uniform blocks become runs; mixed blocks use bits. Large masks are mapped
// and charged to the same process-wide query quota as their source order.
type RowProjection struct {
	data   *rowProjectionData
	closed atomic.Bool
}

type rowProjectionData struct {
	refs                          atomic.Int64
	base                          *RowOrder
	sourceRows, parentRows, count int
	identity                      bool
	storage                       []byte
	mapped                        bool
	file                          *QueryFile
	payloadBytes, blocks          int
}

func ownRowProjection(data *rowProjectionData) *RowProjection {
	data.refs.Store(1)
	p := &RowProjection{data: data}
	runtime.SetFinalizer(p, (*RowProjection).finalize)
	return p
}
func (p *RowProjection) finalize() { _ = p.Close() }
func (p *RowProjection) Retain() (*RowProjection, error) {
	if p == nil || p.closed.Load() {
		return nil, errors.New("可见行投影已经释放")
	}
	for {
		old := p.data.refs.Load()
		if old <= 0 {
			return nil, errors.New("可见行投影已经释放")
		}
		if p.data.refs.CompareAndSwap(old, old+1) {
			next := &RowProjection{data: p.data}
			runtime.SetFinalizer(next, (*RowProjection).finalize)
			runtime.KeepAlive(p)
			return next, nil
		}
	}
}
func (p *RowProjection) Close() error {
	if p == nil || !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	if p.data.refs.Add(-1) != 0 {
		return nil
	}
	d := p.data
	var err error
	if d.mapped {
		err = UnmapFile(d.storage)
	}
	if d.file != nil {
		err = errors.Join(err, d.file.Discard())
	}
	if d.base != nil {
		err = errors.Join(err, d.base.Close())
	}
	d.storage = nil
	return err
}
func (p *RowProjection) Len() int {
	if p == nil {
		return 0
	}
	return p.data.count
}
func (p *RowProjection) SourceRows() int {
	if p == nil {
		return 0
	}
	return p.data.sourceRows
}

// NewRowProjection retains its own input lease. Close the result after use;
// retain another result lease before sharing it with another owner. Building
// scans existing order positions, never worksheet values or formula results.
func NewRowProjection(ctx context.Context, dir string, idx *RowIndex, maxRow int, order *RowOrder, visibility *AxisVisibility, excludeThrough int, progress func(done, total int)) (*RowProjection, error) {
	return newRowProjection(ctx, dir, idx, maxRow, order, visibility, excludeThrough, progress, formats.RowProjectionMemoryBytes, &processQueryQuota)
}

func newRowProjection(ctx context.Context, dir string, idx *RowIndex, maxRow int, order *RowOrder, visibility *AxisVisibility, excludeThrough int, progress func(int, int), resident int, quota *QueryQuota) (*RowProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if idx == nil || order == nil || visibility == nil || excludeThrough < 0 || excludeThrough > visibility.Maximum() || resident < rowProjectionHeaderBytes || resident > formats.RowProjectionMemoryBytes || quota == nil {
		return nil, errors.New("可见行投影的来源或容量无效")
	}
	base, err := order.Retain()
	if err != nil {
		return nil, err
	}
	defer func() {
		if base != nil {
			_ = base.Close()
		}
	}()
	if base.SourceRows() != idx.RowCount() {
		return nil, errors.New("行序与源索引不匹配")
	}
	if maxRow < 0 || maxRow > visibility.Maximum() {
		return nil, errors.New("可见行投影的源区域超出格式范围")
	}
	d := &rowProjectionData{sourceRows: base.SourceRows(), parentRows: base.Len()}
	if progress != nil {
		progress(0, base.Len())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if visibility.Len() == 0 || excludeThrough == visibility.Maximum() || base.Len() == 0 {
		return ownRowProjection(d), nil
	}
	if len(visibility.hidden) == 0 && excludeThrough == 0 {
		d.base, d.identity, d.count = base, true, base.Len()
		base = nil
		return ownRowProjection(d), nil
	}
	b := newRowProjectionBuilder(ctx, dir, d.sourceRows, d.parentRows, resident, quota)
	defer b.abort()
	var words [rowProjectionBlockRows / 64]uint64
	for start := 0; start < base.Len(); start += rowProjectionBlockRows {
		count := min(rowProjectionBlockRows, base.Len()-start)
		clear(words[:])
		for i := 0; i < count; i++ {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			position := base.At(start + i)
			if position < 0 || position >= idx.RowCount() {
				return nil, errors.New("可见行投影包含无效源位置")
			}
			number := idx.RowAt(position).Number
			if number < 1 || number > visibility.Maximum() {
				return nil, errors.New("可见行投影的源坐标超出格式范围")
			}
			if number > excludeThrough && visibility.Visible(number) {
				words[i/64] |= uint64(1) << uint(i%64)
			}
		}
		if err := b.addBlock(count, words[:]); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(start+count, base.Len())
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.count = b.selected
	if d.count == 0 {
		return ownRowProjection(d), nil
	}
	if d.count == d.parentRows {
		d.identity = true
	} else {
		storage, mapped, file, err := b.finish()
		if err != nil {
			return nil, err
		}
		d.storage, d.mapped, d.file = storage, mapped, file
		d.payloadBytes, d.blocks = int(b.payloadBytes), len(b.blocks)
	}
	d.base = base
	base = nil
	return ownRowProjection(d), nil
}

func (p *RowProjection) block(i int) rowProjectionBlock {
	start := rowProjectionHeaderBytes + p.data.payloadBytes + i*rowProjectionRecordBytes
	return decodeRowProjectionBlock(p.data.storage[start : start+rowProjectionRecordBytes])
}
func (p *RowProjection) bitmap(block rowProjectionBlock) []byte {
	start := rowProjectionHeaderBytes + int(block.offset)
	return p.data.storage[start : start+int((block.count+63)/64)*8]
}
func (p *RowProjection) At(position int) int {
	if p == nil || p.closed.Load() || position < 0 || position >= p.Len() {
		panic("invalid visible row position or closed lease")
	}
	defer runtime.KeepAlive(p)
	if p.data.identity {
		return p.data.base.At(position)
	}
	i := sort.Search(p.data.blocks, func(i int) bool { b := p.block(i); return int(b.visibleStart+b.visible) > position })
	b := p.block(i)
	rank := position - int(b.visibleStart)
	if b.kind == rowProjectionAll {
		return p.data.base.At(int(b.start) + rank)
	}
	start := int(b.start)
	for raw := p.bitmap(b); len(raw) >= 8; raw = raw[8:] {
		value := binary.LittleEndian.Uint64(raw)
		count := bits.OnesCount64(value)
		if rank >= count {
			rank -= count
			start += 64
			continue
		}
		for rank > 0 {
			value &= value - 1
			rank--
		}
		return p.data.base.At(start + bits.TrailingZeros64(value))
	}
	panic("invalid visible row bitmap")
}

// Position reuses the parent's source-coordinate reverse lookup, then ranks
// that position in the mask. A source row hidden from this view is not found.
func (p *RowProjection) Position(sourcePosition int) (int, bool) {
	if p == nil || p.closed.Load() {
		return 0, false
	}
	defer runtime.KeepAlive(p)
	if p.data.count == 0 {
		return 0, false
	}
	position, found := p.data.base.Position(sourcePosition)
	if !found {
		return p.Len(), false
	}
	if p.data.identity {
		return position, true
	}
	i := sort.Search(p.data.blocks, func(i int) bool { b := p.block(i); return int(b.start+b.count) > position })
	b := p.block(i)
	local := position - int(b.start)
	at := int(b.visibleStart)
	if b.kind == rowProjectionNone {
		return at, false
	}
	if b.kind == rowProjectionAll {
		return at + local, true
	}
	raw := p.bitmap(b)
	prefix := raw[:local/64*8]
	for len(prefix) >= 8 {
		at += bits.OnesCount64(binary.LittleEndian.Uint64(prefix))
		prefix = prefix[8:]
	}
	value := binary.LittleEndian.Uint64(raw[local/64*8:])
	bit := uint(local % 64)
	at += bits.OnesCount64(value & ((uint64(1) << bit) - 1))
	return at, value&(uint64(1)<<bit) != 0
}
func (p *RowProjection) Page(offset, limit int) ([]int, error) {
	if p == nil || p.closed.Load() || offset < 0 || offset > p.Len() || limit < 0 || limit > 10000 {
		return nil, errors.New("可见行投影读取范围无效")
	}
	rows := make([]int, min(limit, p.Len()-offset))
	for i := range rows {
		rows[i] = p.At(offset + i)
	}
	return rows, nil
}
func (p *RowProjection) Iter() iter.Seq[int] {
	return p.IterRange(0, p.Len())
}

// IterRange seeks to the first visible block and streams only the requested
// ranks. Large selections do not repeatedly rank/select every bitmap word,
// and a small tail selection never scans the entire projection prefix.
func (p *RowProjection) IterRange(offset, count int) iter.Seq[int] {
	return func(yield func(int) bool) {
		if p == nil || p.closed.Load() || offset < 0 || count < 0 || offset > p.Len() || count > p.Len()-offset {
			panic("invalid visible row iteration range or closed lease")
		}
		defer runtime.KeepAlive(p)
		if count == 0 {
			return
		}
		if p.data.identity {
			for value := range p.data.base.IterRange(offset, count) {
				if !yield(value) {
					return
				}
			}
			return
		}
		end := offset + count
		first := sort.Search(p.data.blocks, func(i int) bool { b := p.block(i); return int(b.visibleStart+b.visible) > offset })
		for i := first; i < p.data.blocks; i++ {
			b := p.block(i)
			if int(b.visibleStart) >= end {
				return
			}
			if b.kind == rowProjectionNone {
				continue
			}
			if b.kind == rowProjectionAll {
				start := max(offset-int(b.visibleStart), 0)
				n := min(end-int(b.visibleStart), int(b.visible)) - start
				for value := range p.data.base.IterRange(int(b.start)+start, n) {
					if !yield(value) {
						return
					}
				}
				continue
			}
			skip := max(offset-int(b.visibleStart), 0)
			left := min(end-int(b.visibleStart), int(b.visible)) - skip
			raw := p.bitmap(b)
			for word := 0; len(raw) >= 8; word++ {
				value := binary.LittleEndian.Uint64(raw)
				raw = raw[8:]
				bitsInWord := bits.OnesCount64(value)
				if skip >= bitsInWord {
					skip -= bitsInWord
					continue
				}
				for skip > 0 {
					value &= value - 1
					skip--
				}
				for ; value != 0 && left > 0; value &= value - 1 {
					if !yield(p.data.base.At(int(b.start) + word*64 + bits.TrailingZeros64(value))) {
						return
					}
					left--
				}
				if left == 0 {
					break
				}
			}
		}
	}
}
