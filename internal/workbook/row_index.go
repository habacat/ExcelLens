package workbook

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"os"
	"runtime"
	"sort"
)

// MaxResidentRowOffsets limits the heap used by a published worksheet index.
// Larger indexes map the same checksummed 20-byte records on disk.
const MaxResidentRowOffsets = 1 << 18
const diskRowOffsetBytes = 20

// RowIndex provides the same read-only row positions for both storage modes.
// A mapped prefix retains its owner so collection cannot unmap a live view.
type RowIndex struct {
	odsRuns  []odsRowSpan
	odsCount int
	resident []RowOffset
	records  []byte
	mapping  *mappedRowIndex
}

// NewRowIndex takes ownership of sorted source offsets in memory.
func NewRowIndex(rows []RowOffset) RowIndex {
	return RowIndex{resident: rows}
}

type mappedRowIndex struct {
	data []byte
}

func (m *mappedRowIndex) release() {
	_ = unmapIndexFile(m.data)
	m.data = nil
}

func mapIndexRows(path string, start, count int) (RowIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return RowIndex{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return RowIndex{}, err
	}
	if start < 0 || int64(start) > st.Size() || count < 0 || int64(count) > (st.Size()-int64(start))/diskRowOffsetBytes {
		return RowIndex{}, errors.New("行索引映射范围无效")
	}
	data, err := mapIndexFile(f, st.Size())
	if err != nil {
		return RowIndex{}, err
	}
	m := &mappedRowIndex{data: data}
	// The owner is retained by SheetIndex and every active iterator. No mapping
	// is unmapped while a query still owns its immutable SheetIndex snapshot.
	runtime.SetFinalizer(m, (*mappedRowIndex).release)
	end := start + count*diskRowOffsetBytes
	return RowIndex{mapping: m, records: data[start:end:end]}, nil
}

func indexRowsStart(dimension string) int { return 8 + 4 + len(dimension) + 4 + 4 + 8 + 8 }

func (idx *RowIndex) RowsMapped() bool { return idx.mapping != nil }

func (idx *RowIndex) RowCount() int {
	if idx.odsRuns != nil {
		return idx.odsCount
	}
	if idx.RowsMapped() {
		return len(idx.records) / diskRowOffsetBytes
	}
	return len(idx.resident)
}

func (idx *RowIndex) odsRunAt(i int) odsRowSpan {
	at := sort.Search(len(idx.odsRuns), func(j int) bool { return idx.odsRuns[j].Position+idx.odsRuns[j].Count > i })
	return idx.odsRuns[at]
}

func (idx *RowIndex) RowAt(i int) RowOffset {
	if idx.odsRuns != nil {
		if i < 0 || i >= idx.odsCount {
			panic("row index out of range")
		}
		r := idx.odsRunAt(i)
		return RowOffset{Number: r.First + i - r.Position, Start: r.Start, End: r.End}
	}
	if !idx.RowsMapped() {
		return idx.resident[i]
	}
	if i < 0 || i >= idx.RowCount() {
		panic("row index out of range")
	}
	start := i * diskRowOffsetBytes
	b := idx.records[start : start+diskRowOffsetBytes]
	row := RowOffset{Number: int(binary.LittleEndian.Uint32(b)), Start: int64(binary.LittleEndian.Uint64(b[4:])), End: int64(binary.LittleEndian.Uint64(b[12:]))}
	runtime.KeepAlive(idx.mapping)
	return row
}

func (idx *RowIndex) RowOffsets() iter.Seq2[int, RowOffset] {
	return func(yield func(int, RowOffset) bool) {
		for i := range idx.RowCount() {
			if !yield(i, idx.RowAt(i)) {
				return
			}
		}
	}
}

// FirstRows limits indexed positions while retaining source coordinates,
// worksheet metadata and shared formulas. Both storage modes share their data.
func (idx SheetIndex) FirstRows(limit int) *SheetIndex {
	count := max(0, min(limit, idx.RowCount()))
	if idx.odsRuns != nil {
		idx.odsCount = count
		return &idx
	}
	if idx.RowsMapped() {
		end := count * diskRowOffsetBytes
		idx.records = idx.records[:end:end]
	} else {
		idx.resident = idx.resident[:count:count]
	}
	return &idx
}

type rowOffsetBuilder struct {
	rows    []RowOffset
	file    *os.File
	out     *bufio.Writer
	count   int
	dir     string
	scratch [diskRowOffsetBytes]byte
}

func (b *rowOffsetBuilder) close() {
	if b.file != nil {
		name := b.file.Name()
		_ = b.file.Close()
		_ = os.Remove(name)
	}
}

func writeRowOffset(w io.Writer, data *[diskRowOffsetBytes]byte, row RowOffset) error {
	binary.LittleEndian.PutUint32(data[:], uint32(row.Number))
	binary.LittleEndian.PutUint64(data[4:], uint64(row.Start))
	binary.LittleEndian.PutUint64(data[12:], uint64(row.End))
	_, err := w.Write(data[:])
	return err
}

func (b *rowOffsetBuilder) append(row RowOffset) error {
	if b.file == nil && b.count >= MaxResidentRowOffsets {
		f, err := os.CreateTemp(b.dir, ".offsets-*.tmp")
		if err != nil {
			return err
		}
		b.file, b.out = f, bufio.NewWriterSize(f, 1<<20)
		for _, old := range b.rows {
			if err := b.write(old); err != nil {
				return err
			}
		}
		b.rows = nil
	}
	if b.file != nil {
		if err := b.write(row); err != nil {
			return err
		}
	} else {
		if len(b.rows) == cap(b.rows) {
			grown := make([]RowOffset, len(b.rows), min(MaxResidentRowOffsets, max(4096, cap(b.rows)*2)))
			copy(grown, b.rows)
			b.rows = grown
		}
		b.rows = append(b.rows, row)
	}
	b.count++
	return nil
}

func (b *rowOffsetBuilder) write(row RowOffset) error {
	return writeRowOffset(b.out, &b.scratch, row)
}

func (b *rowOffsetBuilder) finish() error {
	if b.out != nil {
		return b.out.Flush()
	}
	return nil
}
