package workbook

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"os"
	"runtime"
)

// Keep at most 6 MiB of row offsets on the Go heap. Larger indexes use the
// same checksummed 20-byte records, mapped read-only after publication.
const residentRowOffsets = 262144
const diskRowOffsetBytes = 20

type mappedRowIndex struct {
	data  []byte
	start int
	count int
}

func (m *mappedRowIndex) release() {
	_ = unmapIndexFile(m.data)
	m.data = nil
}

func mapIndexRows(path string, start, count int) (*mappedRowIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if start < 0 || count < 0 || int64(count) > (st.Size()-int64(start))/diskRowOffsetBytes {
		return nil, errors.New("行索引映射范围无效")
	}
	data, err := mapIndexFile(f, st.Size())
	if err != nil {
		return nil, err
	}
	m := &mappedRowIndex{data: data, start: start, count: count}
	// The owner is retained by SheetIndex and every active iterator. No mapping
	// is unmapped while a query still owns its immutable SheetIndex snapshot.
	runtime.SetFinalizer(m, (*mappedRowIndex).release)
	return m, nil
}

func indexRowsStart(dimension string) int { return 8 + 4 + len(dimension) + 4 + 4 + 8 + 8 }

func (idx *SheetIndex) RowCount() int {
	if idx.mappedRows != nil {
		return idx.mappedRows.count
	}
	if idx.rowSpool != nil {
		return idx.spooledRows
	}
	return len(idx.ResidentRows)
}

func (idx *SheetIndex) RowAt(i int) RowOffset {
	if idx.mappedRows == nil {
		return idx.ResidentRows[i]
	}
	m := idx.mappedRows
	if i < 0 || i >= m.count {
		panic("row index out of range")
	}
	start := m.start + i*diskRowOffsetBytes
	b := m.data[start : start+diskRowOffsetBytes]
	row := RowOffset{Number: int(binary.LittleEndian.Uint32(b)), Start: int64(binary.LittleEndian.Uint64(b[4:])), End: int64(binary.LittleEndian.Uint64(b[12:]))}
	runtime.KeepAlive(m)
	return row
}

func (idx *SheetIndex) RowOffsets() iter.Seq2[int, RowOffset] {
	return func(yield func(int, RowOffset) bool) {
		for i := 0; i < idx.RowCount(); i++ {
			if !yield(i, idx.RowAt(i)) {
				return
			}
		}
	}
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

func writeRowOffset(w io.Writer, row RowOffset) error {
	var data [diskRowOffsetBytes]byte
	binary.LittleEndian.PutUint32(data[:], uint32(row.Number))
	binary.LittleEndian.PutUint64(data[4:], uint64(row.Start))
	binary.LittleEndian.PutUint64(data[12:], uint64(row.End))
	_, err := w.Write(data[:])
	return err
}

func (b *rowOffsetBuilder) append(row RowOffset) error {
	if b.file == nil && b.count >= residentRowOffsets {
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
			grown := make([]RowOffset, len(b.rows), min(residentRowOffsets, max(4096, cap(b.rows)*2)))
			copy(grown, b.rows)
			b.rows = grown
		}
		b.rows = append(b.rows, row)
	}
	b.count++
	return nil
}

func (b *rowOffsetBuilder) write(row RowOffset) error {
	binary.LittleEndian.PutUint32(b.scratch[:], uint32(row.Number))
	binary.LittleEndian.PutUint64(b.scratch[4:], uint64(row.Start))
	binary.LittleEndian.PutUint64(b.scratch[12:], uint64(row.End))
	_, err := b.out.Write(b.scratch[:])
	return err
}

func (b *rowOffsetBuilder) finish() error {
	if b.out != nil {
		return b.out.Flush()
	}
	return nil
}
