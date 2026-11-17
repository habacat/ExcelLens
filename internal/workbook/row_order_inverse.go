package workbook

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"slices"
)

const pairRunMagic = "ELPAIR01"
const pairRunHeader = 16
const pairMergeFanIn = 16

// Build the reverse lookup with bounded sorted runs. A sorted result must not
// reintroduce an O(rows) heap map just to navigate back to a source address.
func (b *rowOrderBuilder) appendInverse(ctx context.Context) error {
	var allFiles []*queryFile
	defer func() {
		for _, file := range allFiles {
			_ = file.discard()
		}
	}()
	observed := crc32.NewIEEE()
	input := bufio.NewReaderSize(io.TeeReader(io.NewSectionReader(b.file.file, orderHeaderBytes, int64(b.count)*4), observed), 64<<10)
	var runs []*queryFile
	for offset := 0; offset < b.count; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := make([]uint64, min(b.resident, b.count-offset))
		for i := range chunk {
			var raw [4]byte
			if _, err := io.ReadFull(input, raw[:]); err != nil {
				return err
			}
			source := binary.LittleEndian.Uint32(raw[:])
			if uint64(source) >= uint64(b.sourceRows) {
				return errors.New("行序源位置校验失败")
			}
			chunk[i] = uint64(source)<<32 | uint64(uint32(offset+i))
		}
		slices.Sort(chunk)
		run, err := b.writePairRun(ctx, len(chunk), func(emit func(uint64) error) error {
			for _, pair := range chunk {
				if err := emit(pair); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		runs, allFiles = append(runs, run), append(allFiles, run)
		offset += len(chunk)
	}
	if observed.Sum32() != b.crc.Sum32() {
		return errors.New("行序原始内容校验失败")
	}
	for len(runs) > pairMergeFanIn {
		next := make([]*queryFile, 0, (len(runs)+pairMergeFanIn-1)/pairMergeFanIn)
		for start := 0; start < len(runs); start += pairMergeFanIn {
			group := runs[start:min(start+pairMergeFanIn, len(runs))]
			count := 0
			for _, run := range group {
				count += int((run.size - pairRunHeader) / 8)
			}
			merged, err := b.writePairRun(ctx, count, func(emit func(uint64) error) error {
				return mergePairRuns(ctx, group, emit)
			})
			if err != nil {
				return err
			}
			allFiles, next = append(allFiles, merged), append(next, merged)
			for _, run := range group {
				if err := run.discard(); err != nil {
					return err
				}
			}
		}
		runs = next
	}
	previous := -1
	count := 0
	err := mergePairRuns(ctx, runs, func(pair uint64) error {
		source := int(pair >> 32)
		if source <= previous || int(uint32(pair)) >= b.count {
			return errors.New("行序包含重复或无效源位置")
		}
		previous = source
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], pair)
		if _, err := b.out.Write(raw[:]); err != nil {
			return err
		}
		count++
		return nil
	})
	if err == nil && count != b.count {
		err = errors.New("行序反向索引数量不符")
	}
	return err
}

func (b *rowOrderBuilder) writePairRun(ctx context.Context, count int, write func(func(uint64) error) error) (_ *queryFile, err error) {
	f, err := newQueryFile(b.dir, b.quota)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.discard()
		}
	}()
	if _, err = f.Write(make([]byte, pairRunHeader)); err != nil {
		return nil, err
	}
	checksum := crc32.NewIEEE()
	out := bufio.NewWriterSize(io.MultiWriter(f, checksum), 64<<10)
	written := 0
	err = write(func(pair uint64) error {
		if written%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], pair)
		if _, err := out.Write(raw[:]); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if written != count {
		return nil, errors.New("行序排序段数量不符")
	}
	if err = out.Flush(); err != nil {
		return nil, err
	}
	var header [pairRunHeader]byte
	copy(header[:], pairRunMagic)
	binary.LittleEndian.PutUint32(header[8:], uint32(count))
	binary.LittleEndian.PutUint32(header[12:], checksum.Sum32())
	if _, err = f.file.WriteAt(header[:], 0); err != nil {
		return nil, err
	}
	if err = f.close(); err != nil {
		return nil, err
	}
	return f, nil
}

type pairReader struct {
	file      *os.File
	input     *bufio.Reader
	checksum  hash.Hash32
	expected  uint32
	remaining int
	previous  uint64
	started   bool
}

func openPairRun(run *queryFile) (*pairReader, error) {
	f, err := os.Open(run.path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*pairReader, error) { _ = f.Close(); return nil, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	var header [pairRunHeader]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return fail(err)
	}
	count := binary.LittleEndian.Uint32(header[8:])
	if string(header[:8]) != pairRunMagic || info.Size() != int64(pairRunHeader)+int64(count)*8 || info.Size() != run.size {
		return fail(errors.New("行序排序段头部或长度无效"))
	}
	return &pairReader{file: f, input: bufio.NewReaderSize(f, 64<<10), checksum: crc32.NewIEEE(), expected: binary.LittleEndian.Uint32(header[12:]), remaining: int(count)}, nil
}

func (r *pairReader) next() (uint64, bool, error) {
	if r.remaining == 0 {
		if r.checksum.Sum32() != r.expected {
			return 0, false, errors.New("行序排序段内容校验失败")
		}
		return 0, false, nil
	}
	var raw [8]byte
	if _, err := io.ReadFull(r.input, raw[:]); err != nil {
		return 0, false, err
	}
	_, _ = r.checksum.Write(raw[:])
	value := binary.LittleEndian.Uint64(raw[:])
	if r.started && value < r.previous {
		return 0, false, errors.New("行序排序段次序无效")
	}
	r.remaining--
	r.previous, r.started = value, true
	return value, true, nil
}

type pairCursor struct {
	value uint64
	run   int
}
type pairHeap []pairCursor

func (h pairHeap) Len() int           { return len(h) }
func (h pairHeap) Less(i, j int) bool { return h[i].value < h[j].value }
func (h pairHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pairHeap) Push(v any)        { *h = append(*h, v.(pairCursor)) }
func (h *pairHeap) Pop() any {
	n := len(*h) - 1
	v := (*h)[n]
	*h = (*h)[:n]
	return v
}

func mergePairRuns(ctx context.Context, runs []*queryFile, emit func(uint64) error) error {
	if len(runs) > pairMergeFanIn {
		return errors.New("行序排序段打开数量超限")
	}
	readers := make([]*pairReader, 0, len(runs))
	defer func() {
		for _, r := range readers {
			_ = r.file.Close()
		}
	}()
	var heads pairHeap
	for i, run := range runs {
		r, err := openPairRun(run)
		if err != nil {
			return err
		}
		readers = append(readers, r)
		value, ok, err := r.next()
		if err != nil {
			return err
		}
		if ok {
			heads = append(heads, pairCursor{value: value, run: i})
		}
	}
	heap.Init(&heads)
	for heads.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		first := heads[0]
		if err := emit(first.value); err != nil {
			return err
		}
		value, ok, err := readers[first.run].next()
		if err != nil {
			return err
		}
		if ok {
			heads[0].value = value
			heap.Fix(&heads, 0)
		} else {
			heap.Pop(&heads)
		}
	}
	return nil
}
