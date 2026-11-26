package rowindex

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"os"
	"strings"
)

const sortRunMagic = "ELSORT01"
const sortRunHeader = 40

type sortRun struct {
	file      *QueryFile
	count     int
	maxRecord int64
}

func (s *RowSorter) writeRun(ctx context.Context, count int, write func(func(sortRecord) error) error) (_ *sortRun, err error) {
	f, err := newQueryFile(s.dir, s.quota)
	if err != nil {
		return nil, err
	}
	s.files[f] = struct{}{}
	defer func() {
		if err != nil {
			_ = s.forget(f)
		}
	}()
	if _, err = f.Write(make([]byte, sortRunHeader)); err != nil {
		return nil, err
	}
	checksum := crc32.NewIEEE()
	out := bufio.NewWriterSize(io.MultiWriter(f, checksum), s.bufferBytes)
	written := 0
	var maxRecord int64
	var raw [17]byte
	err = write(func(record sortRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.index < 0 || record.index >= s.sourceRows || len(record.values) != len(s.keys) || written >= count {
			return errors.New("排序段源位置或比较键无效")
		}
		size := sortRecordBytes(record.values)
		if size > s.maxRecord {
			return errors.New("排序段比较键超过内存预算")
		}
		maxRecord = max(maxRecord, size)
		binary.LittleEndian.PutUint32(raw[:4], uint32(size))
		binary.LittleEndian.PutUint32(raw[4:8], uint32(record.index))
		if _, err := out.Write(raw[:8]); err != nil {
			return err
		}
		for _, key := range record.values {
			raw[0] = 0
			if key.IsNumber {
				raw[0] = 1
			}
			if key.Folded == key.Text {
				raw[0] |= 2
			}
			binary.LittleEndian.PutUint64(raw[1:9], math.Float64bits(key.Number))
			binary.LittleEndian.PutUint32(raw[9:13], uint32(len(key.Text)))
			foldedBytes := len(key.Folded)
			if raw[0]&2 != 0 {
				foldedBytes = 0
			}
			binary.LittleEndian.PutUint32(raw[13:17], uint32(foldedBytes))
			if _, err := out.Write(raw[:]); err != nil {
				return err
			}
			if _, err := out.WriteString(key.Text); err != nil {
				return err
			}
			if foldedBytes > 0 {
				if _, err := out.WriteString(key.Folded); err != nil {
					return err
				}
			}
		}
		written++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if written != count {
		return nil, errors.New("排序段记录数不符")
	}
	if err = out.Flush(); err != nil {
		return nil, err
	}
	var header [sortRunHeader]byte
	copy(header[:], sortRunMagic)
	binary.LittleEndian.PutUint32(header[8:12], uint32(s.sourceRows))
	binary.LittleEndian.PutUint32(header[12:16], uint32(count))
	binary.LittleEndian.PutUint32(header[16:20], uint32(len(s.keys)))
	binary.LittleEndian.PutUint32(header[20:24], uint32(maxRecord))
	binary.LittleEndian.PutUint64(header[24:32], uint64(f.size-sortRunHeader))
	binary.LittleEndian.PutUint32(header[32:36], checksum.Sum32())
	binary.LittleEndian.PutUint32(header[36:40], crc32.ChecksumIEEE(header[:36]))
	if _, err = f.file.WriteAt(header[:], 0); err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return &sortRun{file: f, count: count, maxRecord: maxRecord}, nil
}

type sortRunReader struct {
	file       *os.File
	input      *bufio.Reader
	checksum   hash.Hash32
	expected   uint32
	remaining  int
	bytesLeft  int64
	maxRecord  int64
	sourceRows int
	keys       []SortKey
	previous   sortRecord
	started    bool
	buffers    [2][]SortValue
	nextBuffer int
	scratch    [4096]byte
	raw        [17]byte
}

func (s *RowSorter) openRun(run *sortRun) (*sortRunReader, error) {
	f, err := os.Open(run.file.path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sortRunReader, error) { _ = f.Close(); return nil, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	var header [sortRunHeader]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return fail(err)
	}
	count := binary.LittleEndian.Uint32(header[12:16])
	maxRecord := int64(binary.LittleEndian.Uint32(header[20:24]))
	bytes := binary.LittleEndian.Uint64(header[24:32])
	if string(header[:8]) != sortRunMagic || binary.LittleEndian.Uint32(header[8:12]) != uint32(s.sourceRows) ||
		int(count) != run.count || int(count) > s.sourceRows || binary.LittleEndian.Uint32(header[16:20]) != uint32(len(s.keys)) ||
		maxRecord != run.maxRecord || maxRecord > s.maxRecord || maxRecord < int64(8+17*len(s.keys)) ||
		bytes > uint64(s.quota.max) || info.Size() != sortRunHeader+int64(bytes) || info.Size() != run.file.size ||
		binary.LittleEndian.Uint32(header[36:40]) != crc32.ChecksumIEEE(header[:36]) {
		return fail(errors.New("排序段头部或长度校验失败"))
	}
	return &sortRunReader{file: f, input: bufio.NewReaderSize(io.NewSectionReader(f, sortRunHeader, int64(bytes)), s.bufferBytes),
		checksum: crc32.NewIEEE(), expected: binary.LittleEndian.Uint32(header[32:36]), remaining: int(count), bytesLeft: int64(bytes),
		maxRecord: maxRecord, sourceRows: s.sourceRows, keys: s.keys,
		buffers: [2][]SortValue{make([]SortValue, len(s.keys)), make([]SortValue, len(s.keys))}}, nil
}

func (r *sortRunReader) read(raw []byte) error {
	if int64(len(raw)) > r.bytesLeft {
		return errors.New("排序段记录被截断")
	}
	if _, err := io.ReadFull(r.input, raw); err != nil {
		return err
	}
	r.bytesLeft -= int64(len(raw))
	_, _ = r.checksum.Write(raw)
	return nil
}

func (r *sortRunReader) text(ctx context.Context, size uint32) (string, error) {
	// Grow only after the entire record's lengths have passed bounds checks.
	// Builder avoids retaining a second full byte buffer for long key strings.
	var out strings.Builder
	out.Grow(int(size))
	for left := int(size); left > 0; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		chunk := r.scratch[:min(left, len(r.scratch))]
		if err := r.read(chunk); err != nil {
			return "", err
		}
		_, _ = out.Write(chunk)
		left -= len(chunk)
	}
	return out.String(), nil
}

func (r *sortRunReader) next(ctx context.Context) (sortRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return sortRecord{}, false, err
	}
	if r.remaining == 0 {
		if r.bytesLeft != 0 || r.checksum.Sum32() != r.expected {
			return sortRecord{}, false, errors.New("排序段内容校验失败")
		}
		return sortRecord{}, false, nil
	}
	before := r.bytesLeft
	if err := r.read(r.raw[:8]); err != nil {
		return sortRecord{}, false, err
	}
	size, source := int64(binary.LittleEndian.Uint32(r.raw[:4])), int(binary.LittleEndian.Uint32(r.raw[4:8]))
	minimum := int64(8 + 17*len(r.keys))
	if size < minimum || size > r.maxRecord || size > before || source >= r.sourceRows {
		return sortRecord{}, false, errors.New("排序段记录长度或源位置无效")
	}
	values := r.buffers[r.nextBuffer]
	clear(values)
	for k := range values {
		if err := r.read(r.raw[:]); err != nil {
			return sortRecord{}, false, err
		}
		flag := r.raw[0]
		number := math.Float64frombits(binary.LittleEndian.Uint64(r.raw[1:9]))
		textBytes, foldedBytes := binary.LittleEndian.Uint32(r.raw[9:13]), binary.LittleEndian.Uint32(r.raw[13:17])
		used := before - r.bytesLeft
		if flag&^3 != 0 || math.IsNaN(number) || math.IsInf(number, 0) || flag&2 != 0 && foldedBytes != 0 ||
			flag&1 != 0 && (foldedBytes != 0 || textBytes == 0 || flag&2 != 0) ||
			int64(textBytes)+int64(foldedBytes) > size-used-int64((len(values)-k-1)*17) {
			return sortRecord{}, false, errors.New("排序段比较键编码无效")
		}
		text, err := r.text(ctx, textBytes)
		if err != nil {
			return sortRecord{}, false, err
		}
		folded := text
		if flag&2 == 0 {
			folded, err = r.text(ctx, foldedBytes)
			if err != nil {
				return sortRecord{}, false, err
			}
		}
		values[k] = SortValue{Text: text, Folded: folded, Number: number, IsNumber: flag&1 != 0}
	}
	if before-r.bytesLeft != size {
		return sortRecord{}, false, errors.New("排序段记录长度不符")
	}
	record := sortRecord{index: source, values: values}
	if r.started && compareSortRecords(record, r.previous, r.keys) <= 0 {
		return sortRecord{}, false, errors.New("排序段次序无效或源位置重复")
	}
	r.previous, r.started, r.nextBuffer = record, true, r.nextBuffer^1
	r.remaining--
	return record, true, nil
}

type sortCursor struct {
	record sortRecord
	run    int
}

type sortHeadHeap struct {
	heads []sortCursor
	keys  []SortKey
}

func (h sortHeadHeap) Len() int { return len(h.heads) }
func (h sortHeadHeap) Less(i, j int) bool {
	return compareSortRecords(h.heads[i].record, h.heads[j].record, h.keys) < 0
}
func (h sortHeadHeap) Swap(i, j int)   { h.heads[i], h.heads[j] = h.heads[j], h.heads[i] }
func (h *sortHeadHeap) Push(value any) { h.heads = append(h.heads, value.(sortCursor)) }
func (h *sortHeadHeap) Pop() any {
	n := len(h.heads) - 1
	value := h.heads[n]
	h.heads[n] = sortCursor{}
	h.heads = h.heads[:n]
	return value
}

func (s *RowSorter) mergeRuns(ctx context.Context, runs []*sortRun, emit func(sortRecord) error) error {
	if len(runs) > sortMergeFanIn || s.groupSize(runs) != len(runs) {
		return errors.New("排序归并句柄或比较键预算超限")
	}
	readers := make([]*sortRunReader, 0, len(runs))
	defer func() {
		for _, reader := range readers {
			_ = reader.file.Close()
		}
	}()
	heads := sortHeadHeap{keys: s.keys}
	for i, run := range runs {
		reader, err := s.openRun(run)
		if err != nil {
			return err
		}
		readers = append(readers, reader)
		record, ok, err := reader.next(ctx)
		if err != nil {
			return err
		}
		if ok {
			heads.heads = append(heads.heads, sortCursor{record: record, run: i})
		}
	}
	heap.Init(&heads)
	for heads.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		first := heads.heads[0]
		if err := emit(first.record); err != nil {
			return err
		}
		record, ok, err := readers[first.run].next(ctx)
		if err != nil {
			return err
		}
		if ok {
			heads.heads[0].record = record
			heap.Fix(&heads, 0)
		} else {
			heap.Pop(&heads)
		}
	}
	return nil
}
