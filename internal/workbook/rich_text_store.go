package workbook

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"excellens/internal/formats"
	"excellens/internal/rowindex"
)

// ELRICH01 is a separate presentation sidecar, not a row/value cache change.
// Sparse sorted keys and records are mapped read-only; a million different
// rich strings need no workbook-wide Go map of fonts or fragment slices.
const richStoreMagic = "ELRICH01"
const richStoreHeader = 32
const richStoreIndexEntry = 16
const richStoreRecordHeader = 44

type richTextStore struct {
	data              []byte
	count, indexStart int
}

func (s *richTextStore) release() { _ = rowindex.UnmapFile(s.data); s.data = nil }

func openRichTextStore(path string, stringsCount int) (*richTextStore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() < richStoreHeader || stat.Size() > formats.MaxRichCacheBytes {
		return nil, errors.New("富文本样式缓存大小无效")
	}
	data, err := rowindex.MapFile(f, stat.Size())
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = rowindex.UnmapFile(data)
		}
	}()
	if string(data[:8]) != richStoreMagic || binary.LittleEndian.Uint32(data[28:32]) != crc32.ChecksumIEEE(data[:28]) {
		return nil, errors.New("富文本样式缓存版本或头校验无效")
	}
	count, start := binary.LittleEndian.Uint64(data[8:16]), binary.LittleEndian.Uint64(data[16:24])
	if stringsCount < 0 || count > uint64(stringsCount) || start < richStoreHeader || start > uint64(len(data)) || count > (uint64(len(data))-start)/richStoreIndexEntry || start+count*richStoreIndexEntry != uint64(len(data)) {
		return nil, errors.New("富文本样式缓存目录范围无效")
	}
	index := data[int(start):]
	if binary.LittleEndian.Uint32(data[24:28]) != crc32.ChecksumIEEE(index) {
		return nil, errors.New("富文本样式缓存目录校验失败")
	}
	previousID, previousEnd := int64(-1), uint64(richStoreHeader)
	for pos := 0; pos < int(count); pos++ {
		entry := index[pos*richStoreIndexEntry : (pos+1)*richStoreIndexEntry]
		id, at, length := binary.LittleEndian.Uint32(entry), binary.LittleEndian.Uint64(entry[4:12]), binary.LittleEndian.Uint32(entry[12:16])
		if int64(id) <= previousID || uint64(id) >= uint64(stringsCount) || at != previousEnd || length == 0 || length > formats.MaxCellRichBytes || at > start || uint64(length)+richStoreRecordHeader > start-at {
			return nil, errors.New("富文本样式缓存记录范围或顺序无效")
		}
		if binary.LittleEndian.Uint32(data[int(at):]) != id || binary.LittleEndian.Uint32(data[int(at)+4:]) != length {
			return nil, errors.New("富文本样式缓存记录身份无效")
		}
		previousID, previousEnd = int64(id), at+richStoreRecordHeader+uint64(length)
	}
	if previousEnd != start {
		return nil, errors.New("富文本样式缓存记录边界不完整")
	}
	s := &richTextStore{data: data, count: int(count), indexStart: int(start)}
	runtime.SetFinalizer(s, (*richTextStore).release)
	valid = true
	return s, nil
}

func (s *richTextStore) lookup(id int, text string) (*RichText, error) {
	return s.lookupWithBudget(id, text, nil)
}

func (s *richTextStore) lookupWithBudget(id int, text string, budget *richReadBudget) (*RichText, error) {
	if s == nil {
		return nil, nil
	}
	defer runtime.KeepAlive(s)
	if id < 0 || uint64(id) > math.MaxUint32 {
		return nil, errors.New("富文本共享字符串标识无效")
	}
	lo, hi := 0, s.count
	for lo < hi {
		mid := lo + (hi-lo)/2
		entry := s.data[s.indexStart+mid*richStoreIndexEntry:]
		if binary.LittleEndian.Uint32(entry) < uint32(id) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == s.count {
		return nil, nil
	}
	entry := s.data[s.indexStart+lo*richStoreIndexEntry:]
	if binary.LittleEndian.Uint32(entry) != uint32(id) {
		return nil, nil
	}
	at, length := int(binary.LittleEndian.Uint64(entry[4:12])), int(binary.LittleEndian.Uint32(entry[12:16]))
	if budget != nil && !budget.admit(length) {
		return richReadLimitNotice(), nil
	}
	record := s.data[at : at+richStoreRecordHeader+length]
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(record[:8])
	_, _ = checksum.Write(record[12:richStoreRecordHeader])
	_, _ = checksum.Write(record[richStoreRecordHeader:])
	if checksum.Sum32() != binary.LittleEndian.Uint32(record[8:12]) {
		return nil, errors.New("富文本样式缓存记录校验失败")
	}
	digest := sha256.Sum256([]byte(text))
	if !bytes.Equal(record[12:richStoreRecordHeader], digest[:]) {
		return nil, errors.New("富文本样式缓存与原文不一致")
	}
	decoder := json.NewDecoder(bytes.NewReader(record[richStoreRecordHeader:]))
	decoder.DisallowUnknownFields()
	var out RichText
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("富文本样式缓存记录无效：%w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("富文本样式缓存记录含多余内容")
	}
	if err := ValidateRichText(text, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type richTextStoreBuilder struct {
	dir           string
	file, index   *os.File
	out, indexOut *bufio.Writer
	position      int64
	count         int
	last          int64
	indexCRC      uint32
}

func (b *richTextStoreBuilder) close() {
	for _, f := range []*os.File{b.file, b.index} {
		if f != nil {
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
		}
	}
}

func (b *richTextStoreBuilder) begin() error {
	if b.file != nil {
		return nil
	}
	f, err := os.CreateTemp(b.dir, ".rich-strings-*.tmp")
	if err != nil {
		return err
	}
	b.file = f
	index, err := os.CreateTemp(b.dir, ".rich-index-*.tmp")
	if err != nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		b.file = nil
		return err
	}
	b.index = index
	b.out, b.indexOut = bufio.NewWriterSize(f, 1<<20), bufio.NewWriterSize(index, 1<<20)
	b.position = richStoreHeader
	b.last = -1
	_, err = b.out.Write(make([]byte, richStoreHeader))
	return err
}

func (b *richTextStoreBuilder) append(id int, text string, value *RichText) error {
	if value == nil {
		return nil
	}
	if id < 0 || uint64(id) > math.MaxUint32 {
		return errors.New("富文本共享字符串标识超限")
	}
	if err := ValidateRichText(text, value); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > formats.MaxCellRichBytes {
		return errors.New("单格富文本样式缓存超限，原文未修改")
	}
	if err := b.begin(); err != nil {
		return err
	}
	if int64(id) <= b.last {
		return errors.New("富文本共享字符串标识重复或乱序")
	}
	if int64(len(encoded))+richStoreRecordHeader > b.maxRemaining() {
		return errors.New("富文本样式缓存超过2GiB，原文未修改")
	}
	var header [richStoreRecordHeader]byte
	binary.LittleEndian.PutUint32(header[:], uint32(id))
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(encoded)))
	digest := sha256.Sum256([]byte(text))
	copy(header[12:richStoreRecordHeader], digest[:])
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(header[:8])
	_, _ = checksum.Write(header[12:richStoreRecordHeader])
	_, _ = checksum.Write(encoded)
	binary.LittleEndian.PutUint32(header[8:12], checksum.Sum32())
	if _, err := b.out.Write(header[:]); err != nil {
		return err
	}
	if _, err := b.out.Write(encoded); err != nil {
		return err
	}
	var entry [richStoreIndexEntry]byte
	binary.LittleEndian.PutUint32(entry[:], uint32(id))
	binary.LittleEndian.PutUint64(entry[4:12], uint64(b.position))
	binary.LittleEndian.PutUint32(entry[12:16], uint32(len(encoded)))
	if _, err := b.indexOut.Write(entry[:]); err != nil {
		return err
	}
	b.indexCRC = crc32.Update(b.indexCRC, crc32.IEEETable, entry[:])
	b.position += int64(len(header) + len(encoded))
	b.last = int64(id)
	b.count++
	return nil
}

func (b *richTextStoreBuilder) maxRemaining() int64 {
	return formats.MaxRichCacheBytes - b.position - int64(b.count+1)*richStoreIndexEntry
}

func (b *richTextStoreBuilder) finish(path string, stringsCount int) (*richTextStore, error) {
	if b.file == nil {
		return nil, nil
	}
	if b.last >= int64(stringsCount) {
		return nil, errors.New("富文本缓存包含越界共享字符串")
	}
	if err := b.indexOut.Flush(); err != nil {
		return nil, err
	}
	if err := b.out.Flush(); err != nil {
		return nil, err
	}
	if _, err := b.index.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.Copy(b.file, b.index); err != nil {
		return nil, err
	}
	var header [richStoreHeader]byte
	copy(header[:8], richStoreMagic)
	binary.LittleEndian.PutUint64(header[8:16], uint64(b.count))
	binary.LittleEndian.PutUint64(header[16:24], uint64(b.position))
	binary.LittleEndian.PutUint32(header[24:28], b.indexCRC)
	binary.LittleEndian.PutUint32(header[28:32], crc32.ChecksumIEEE(header[:28]))
	if _, err := b.file.WriteAt(header[:], 0); err != nil {
		return nil, err
	}
	if err := b.file.Sync(); err != nil {
		return nil, err
	}
	if err := b.file.Close(); err != nil {
		return nil, err
	}
	// Another opener may have published the same immutable fingerprint cache.
	// Prefer its verified file rather than replacing a live Windows mapping.
	if existing, err := openRichTextStore(path, stringsCount); err == nil {
		return existing, nil
	}
	staged, err := openRichTextStore(b.file.Name(), stringsCount)
	if err != nil {
		return nil, err
	}
	runtime.SetFinalizer(staged, nil)
	staged.release()
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(b.dir) {
		return nil, errors.New("富文本样式缓存发布目录不一致")
	}
	if err := os.Rename(b.file.Name(), path); err != nil {
		if existing, readErr := openRichTextStore(path, stringsCount); readErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return openRichTextStore(path, stringsCount)
}
