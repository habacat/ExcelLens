package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/bits"
	"os"

	"excellens/internal/formats"
)

const (
	rowProjectionMagic       = "ELRPR001"
	rowProjectionHeaderBytes = 64
	rowProjectionRecordBytes = 32
	rowProjectionBlockRows   = 4096
	rowProjectionNone        = uint32(0)
	rowProjectionAll         = uint32(1)
	rowProjectionBits        = uint32(2)
)

type rowProjectionBlock struct {
	start, count, visibleStart, visible uint32
	offset                              uint64
	kind                                uint32
}

func decodeRowProjectionBlock(raw []byte) rowProjectionBlock {
	return rowProjectionBlock{binary.LittleEndian.Uint32(raw), binary.LittleEndian.Uint32(raw[4:]), binary.LittleEndian.Uint32(raw[8:]), binary.LittleEndian.Uint32(raw[12:]), binary.LittleEndian.Uint64(raw[16:]), binary.LittleEndian.Uint32(raw[24:])}
}
func encodeRowProjectionBlock(raw []byte, b rowProjectionBlock) {
	clear(raw)
	binary.LittleEndian.PutUint32(raw, b.start)
	binary.LittleEndian.PutUint32(raw[4:], b.count)
	binary.LittleEndian.PutUint32(raw[8:], b.visibleStart)
	binary.LittleEndian.PutUint32(raw[12:], b.visible)
	binary.LittleEndian.PutUint64(raw[16:], b.offset)
	binary.LittleEndian.PutUint32(raw[24:], b.kind)
}

type rowProjectionBuilder struct {
	ctx                                                  context.Context
	dir                                                  string
	sourceRows, parentRows, resident, consumed, selected int
	quota                                                *queryQuota
	memory                                               bytes.Buffer
	file                                                 *queryFile
	blocks                                               []rowProjectionBlock
	payloadBytes                                         uint64
	payloadCRC                                           uint32
	finished                                             bool
}

func newRowProjectionBuilder(ctx context.Context, dir string, sourceRows, parentRows, resident int, quota *queryQuota) *rowProjectionBuilder {
	b := &rowProjectionBuilder{ctx: ctx, dir: dir, sourceRows: sourceRows, parentRows: parentRows, resident: resident, quota: quota}
	b.memory.Write(make([]byte, rowProjectionHeaderBytes))
	return b
}
func (b *rowProjectionBuilder) abort() {
	if b.file != nil {
		_ = b.file.discard()
		b.file = nil
	}
}
func (b *rowProjectionBuilder) append(raw []byte) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.file == nil && b.memory.Len()+len(raw) > b.resident {
		if err := b.quota.cleanStale(b.ctx, b.dir); err != nil {
			return err
		}
		file, err := newQueryFile(b.dir, b.quota)
		if err != nil {
			return err
		}
		b.file = file
		if _, err := b.file.Write(b.memory.Bytes()); err != nil {
			return err
		}
		b.memory = bytes.Buffer{}
	}
	if b.file != nil {
		_, err := b.file.Write(raw)
		return err
	}
	_, err := b.memory.Write(raw)
	return err
}
func (b *rowProjectionBuilder) addBlock(count int, words []uint64) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.finished || count < 1 || count > rowProjectionBlockRows || b.consumed+count > b.parentRows || len(words) < (count+63)/64 || count < rowProjectionBlockRows && b.consumed+count != b.parentRows {
		return errors.New("可见行位图块范围无效")
	}
	wordCount := (count + 63) / 64
	if count%64 != 0 && words[wordCount-1]>>uint(count%64) != 0 {
		return errors.New("可见行位图尾部含越界位")
	}
	selected := 0
	for _, word := range words[:wordCount] {
		selected += bits.OnesCount64(word)
	}
	block := rowProjectionBlock{start: uint32(b.consumed), count: uint32(count), visibleStart: uint32(b.selected), visible: uint32(selected)}
	switch selected {
	case 0:
		block.kind = rowProjectionNone
	case count:
		block.kind = rowProjectionAll
	default:
		block.kind, block.offset = rowProjectionBits, b.payloadBytes
		var raw [rowProjectionBlockRows / 8]byte
		for i, word := range words[:wordCount] {
			binary.LittleEndian.PutUint64(raw[i*8:], word)
		}
		data := raw[:wordCount*8]
		if err := b.append(data); err != nil {
			return err
		}
		b.payloadCRC = crc32.Update(b.payloadCRC, crc32.IEEETable, data)
		b.payloadBytes += uint64(len(data))
	}
	if n := len(b.blocks); n > 0 && block.kind != rowProjectionBits && b.blocks[n-1].kind == block.kind {
		last := &b.blocks[n-1]
		last.count += block.count
		last.visible += block.visible
	} else {
		if len(b.blocks) >= (b.parentRows+rowProjectionBlockRows-1)/rowProjectionBlockRows || (len(b.blocks)+1)*64+b.resident+64<<10 > formats.RowProjectionBuildBytes {
			return errors.New("可见行位图目录超过工作容量")
		}
		b.blocks = append(b.blocks, block)
	}
	b.consumed += count
	b.selected += selected
	return nil
}
func (b *rowProjectionBuilder) finish() ([]byte, bool, *queryFile, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, false, nil, err
	}
	if b.finished || b.consumed != b.parentRows {
		return nil, false, nil, errors.New("可见行位图尚未完整生成或已经发布")
	}
	var raw [rowProjectionRecordBytes]byte
	directoryCRC := uint32(0)
	for _, block := range b.blocks {
		encodeRowProjectionBlock(raw[:], block)
		if err := b.append(raw[:]); err != nil {
			return nil, false, nil, err
		}
		directoryCRC = crc32.Update(directoryCRC, crc32.IEEETable, raw[:])
	}
	var head [rowProjectionHeaderBytes]byte
	copy(head[:], rowProjectionMagic)
	binary.LittleEndian.PutUint32(head[8:], uint32(b.sourceRows))
	binary.LittleEndian.PutUint32(head[12:], uint32(b.parentRows))
	binary.LittleEndian.PutUint32(head[16:], uint32(b.selected))
	binary.LittleEndian.PutUint32(head[20:], uint32(len(b.blocks)))
	binary.LittleEndian.PutUint64(head[24:], b.payloadBytes)
	binary.LittleEndian.PutUint32(head[40:], b.payloadCRC)
	binary.LittleEndian.PutUint32(head[44:], directoryCRC)
	binary.LittleEndian.PutUint32(head[60:], crc32.ChecksumIEEE(head[:60]))
	if b.file == nil {
		copy(b.memory.Bytes()[:rowProjectionHeaderBytes], head[:])
		storage := b.memory.Bytes()
		if err := validateRowProjectionStorage(b.ctx, storage, b.sourceRows, b.parentRows, b.selected); err != nil {
			return nil, false, nil, err
		}
		b.finished = true
		return storage, false, nil, nil
	}
	if n, err := b.file.file.WriteAt(head[:], 0); err != nil {
		return nil, false, nil, err
	} else if n != len(head) {
		return nil, false, nil, io.ErrShortWrite
	}
	if err := b.file.file.Sync(); err != nil {
		return nil, false, nil, err
	}
	if err := b.file.close(); err != nil {
		return nil, false, nil, err
	}
	storage, err := mapRowProjectionFile(b.ctx, b.file.path, b.sourceRows, b.parentRows, b.selected)
	if err != nil {
		return nil, false, nil, err
	}
	file := b.file
	b.file = nil
	b.finished = true
	return storage, true, file, nil
}

func rowProjectionSizeBound(parentRows int) int64 {
	return rowProjectionHeaderBytes + int64((parentRows+63)/64)*8 + int64((parentRows+rowProjectionBlockRows-1)/rowProjectionBlockRows)*rowProjectionRecordBytes
}
func mapRowProjectionFile(ctx context.Context, path string, sourceRows, parentRows, selected int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if sourceRows < 0 || sourceRows > formats.ExternalRows || parentRows < 0 || parentRows > sourceRows || selected < 0 || selected > parentRows || info.Size() < rowProjectionHeaderBytes || info.Size() > rowProjectionSizeBound(parentRows) {
		return nil, errors.New("可见行投影文件长度或来源无效")
	}
	data, err := mapIndexFile(f, info.Size())
	if err != nil {
		return nil, err
	}
	if err := validateRowProjectionStorage(ctx, data, sourceRows, parentRows, selected); err != nil {
		_ = unmapIndexFile(data)
		return nil, err
	}
	return data, nil
}
func rowProjectionCRC(ctx context.Context, data []byte, want uint32) error {
	sum := uint32(0)
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := min(len(data), 64<<10)
		sum = crc32.Update(sum, crc32.IEEETable, data[:size])
		data = data[size:]
	}
	if sum != want {
		return errors.New("可见行投影内容校验失败")
	}
	return ctx.Err()
}
func validateRowProjectionStorage(ctx context.Context, data []byte, sourceRows, parentRows, selected int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sourceRows < 0 || sourceRows > formats.ExternalRows || parentRows < 0 || parentRows > sourceRows || selected < 0 || selected > parentRows || len(data) < rowProjectionHeaderBytes || int64(len(data)) > rowProjectionSizeBound(parentRows) {
		return errors.New("可见行投影文件长度或容量无效")
	}
	head := data[:rowProjectionHeaderBytes]
	if string(head[:8]) != rowProjectionMagic || binary.LittleEndian.Uint32(head[8:]) != uint32(sourceRows) || binary.LittleEndian.Uint32(head[12:]) != uint32(parentRows) || binary.LittleEndian.Uint32(head[16:]) != uint32(selected) || binary.LittleEndian.Uint64(head[32:]) != 0 || binary.LittleEndian.Uint64(head[48:]) != 0 || binary.LittleEndian.Uint32(head[56:]) != 0 || binary.LittleEndian.Uint32(head[60:]) != crc32.ChecksumIEEE(head[:60]) {
		return errors.New("可见行投影文件头校验失败")
	}
	blocks := uint64(binary.LittleEndian.Uint32(head[20:]))
	payload := binary.LittleEndian.Uint64(head[24:])
	if blocks > uint64((parentRows+rowProjectionBlockRows-1)/rowProjectionBlockRows) || payload > uint64((parentRows+63)/64)*8 || uint64(len(data)) != rowProjectionHeaderBytes+payload+blocks*rowProjectionRecordBytes {
		return errors.New("可见行投影目录或位图长度无效")
	}
	directory := rowProjectionHeaderBytes + int(payload)
	if err := rowProjectionCRC(ctx, data[rowProjectionHeaderBytes:directory], binary.LittleEndian.Uint32(head[40:])); err != nil {
		return err
	}
	if err := rowProjectionCRC(ctx, data[directory:], binary.LittleEndian.Uint32(head[44:])); err != nil {
		return err
	}
	var consumed, visible uint32
	var offset uint64
	for i := uint64(0); i < blocks; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := directory + int(i)*rowProjectionRecordBytes
		raw := data[start : start+rowProjectionRecordBytes]
		b := decodeRowProjectionBlock(raw)
		if b.start != consumed || b.count == 0 || uint64(b.start)+uint64(b.count) > uint64(parentRows) || b.start%rowProjectionBlockRows != 0 || b.count%rowProjectionBlockRows != 0 && int(b.start+b.count) != parentRows || b.visibleStart != visible || b.visible > b.count || binary.LittleEndian.Uint32(raw[28:]) != 0 {
			return errors.New("可见行投影区间或计数不一致")
		}
		switch b.kind {
		case rowProjectionNone:
			if b.visible != 0 || b.offset != 0 {
				return errors.New("全隐藏块无效")
			}
		case rowProjectionAll:
			if b.visible != b.count || b.offset != 0 {
				return errors.New("全显示块无效")
			}
		case rowProjectionBits:
			bytes := uint64((b.count+63)/64) * 8
			if b.count > rowProjectionBlockRows || b.visible == 0 || b.visible == b.count || b.offset != offset || bytes > payload-offset {
				return errors.New("混合可见位图块无效")
			}
			count := 0
			for word := uint32(0); word < (b.count+63)/64; word++ {
				at := rowProjectionHeaderBytes + int(offset) + int(word)*8
				value := binary.LittleEndian.Uint64(data[at:])
				if word == (b.count+63)/64-1 && b.count%64 != 0 && value>>uint(b.count%64) != 0 {
					return errors.New("可见行位图含越界位")
				}
				count += bits.OnesCount64(value)
			}
			if uint32(count) != b.visible {
				return errors.New("可见行位图计数不一致")
			}
			offset += bytes
		default:
			return errors.New("可见行投影块类型无效")
		}
		consumed += b.count
		visible += b.visible
	}
	if consumed != uint32(parentRows) || visible != uint32(selected) || offset != payload {
		return errors.New("可见行投影未覆盖完整来源")
	}
	return ctx.Err()
}
