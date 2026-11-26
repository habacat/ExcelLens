package rowindex

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
const DiskRowOffsetBytes = 20

type RowOffset struct {
	Number int
	Start  int64
	End    int64
}

// ODSRowSpan describes a run of identical physical rows in an ODS worksheet.
type ODSRowSpan struct {
	First, Position, Count int
	Start, End             int64
}

// RowIndex provides uniform read-only row positions across resident, mmap and
// ODS run-compressed storage modes.
type RowIndex struct {
	odsRuns  []ODSRowSpan
	odsCount int
	resident []RowOffset
	records  []byte
	mapping  *mappedRowIndex
}

// NewRowIndex takes ownership of sorted source offsets in memory.
func NewRowIndex(rows []RowOffset) RowIndex {
	return RowIndex{resident: rows}
}

// NewODSRowIndex creates a run-compressed ODS row index.
func NewODSRowIndex(runs []ODSRowSpan, count int) RowIndex {
	if runs == nil {
		runs = []ODSRowSpan{}
	}
	return RowIndex{odsRuns: runs, odsCount: count}
}

type mappedRowIndex struct {
	data []byte
}

func (m *mappedRowIndex) release() {
	_ = UnmapFile(m.data)
	m.data = nil
}

func MapIndexRows(path string, start, count int) (RowIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return RowIndex{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return RowIndex{}, err
	}
	if start < 0 || int64(start) > st.Size() || count < 0 || int64(count) > (st.Size()-int64(start))/DiskRowOffsetBytes {
		return RowIndex{}, errors.New("行索引映射范围无效")
	}
	data, err := MapFile(f, st.Size())
	if err != nil {
		return RowIndex{}, err
	}
	m := &mappedRowIndex{data: data}
	// The owner is retained by SheetIndex and every active iterator. No mapping
	// is unmapped while a query still owns its immutable SheetIndex snapshot.
	runtime.SetFinalizer(m, (*mappedRowIndex).release)
	end := start + count*DiskRowOffsetBytes
	return RowIndex{mapping: m, records: data[start:end:end]}, nil
}

func IndexRowsStart(dimension string) int { return 8 + 4 + len(dimension) + 4 + 4 + 8 + 8 }

func (idx *RowIndex) RowsMapped() bool { return idx.mapping != nil }

// ReleaseMapping deterministically releases a file mapping when its owner is
// known to have no live views (for example in test cleanup on Windows).
func (idx *RowIndex) ReleaseMapping() {
	if idx == nil || idx.mapping == nil {
		return
	}
	m := idx.mapping
	idx.mapping = nil
	idx.records = nil
	runtime.SetFinalizer(m, nil)
	m.release()
}

func (idx *RowIndex) ODSRuns() []ODSRowSpan { return idx.odsRuns }

func (idx *RowIndex) ODSRunAt(i int) ODSRowSpan { return idx.odsRunAt(i) }

func (idx *RowIndex) RowCount() int {
	if idx.odsRuns != nil {
		return idx.odsCount
	}
	if idx.RowsMapped() {
		return len(idx.records) / DiskRowOffsetBytes
	}
	return len(idx.resident)
}

func (idx *RowIndex) odsRunAt(i int) ODSRowSpan {
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
	start := i * DiskRowOffsetBytes
	b := idx.records[start : start+DiskRowOffsetBytes]
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

// Truncate returns a shallow view restricted to the first limit indexed rows.
func (idx RowIndex) Truncate(limit int) RowIndex {
	count := max(0, min(limit, idx.RowCount()))
	if idx.odsRuns != nil {
		idx.odsCount = count
		return idx
	}
	if idx.RowsMapped() {
		end := count * DiskRowOffsetBytes
		idx.records = idx.records[:end:end]
	} else {
		idx.resident = idx.resident[:count:count]
	}
	return idx
}

// RowOffsetBuilder accumulates sorted row offsets in memory and spills to a
// temporary file once MaxResidentRowOffsets is exceeded.
type RowOffsetBuilder struct {
	rows    []RowOffset
	file    *os.File
	out     *bufio.Writer
	count   int
	dir     string
	scratch [DiskRowOffsetBytes]byte
}

func NewRowOffsetBuilder(dir string, initialCap int) RowOffsetBuilder {
	return RowOffsetBuilder{rows: make([]RowOffset, 0, initialCap), dir: dir}
}

func (b *RowOffsetBuilder) Count() int          { return b.count }
func (b *RowOffsetBuilder) Rows() []RowOffset   { return b.rows }
func (b *RowOffsetBuilder) SpillFile() *os.File { return b.file }

func (b *RowOffsetBuilder) Close() {
	if b.file != nil {
		name := b.file.Name()
		_ = b.file.Close()
		_ = os.Remove(name)
	}
}

func WriteRowOffset(w io.Writer, data *[DiskRowOffsetBytes]byte, row RowOffset) error {
	binary.LittleEndian.PutUint32(data[:], uint32(row.Number))
	binary.LittleEndian.PutUint64(data[4:], uint64(row.Start))
	binary.LittleEndian.PutUint64(data[12:], uint64(row.End))
	_, err := w.Write(data[:])
	return err
}

func (b *RowOffsetBuilder) Append(row RowOffset) error {
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

func (b *RowOffsetBuilder) write(row RowOffset) error {
	return WriteRowOffset(b.out, &b.scratch, row)
}

func (b *RowOffsetBuilder) Finish() error {
	if b.out != nil {
		return b.out.Flush()
	}
	return nil
}
