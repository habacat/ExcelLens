package workbook

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"excellens/internal/formats"
)

func projectionStorageFixture(t *testing.T) []byte {
	t.Helper()
	const n = 5000
	b := newRowProjectionBuilder(context.Background(), t.TempDir(), n, n, formats.RowProjectionMemoryBytes, &queryQuota{max: 1 << 20})
	defer b.abort()
	for start := 0; start < n; start += rowProjectionBlockRows {
		count := min(rowProjectionBlockRows, n-start)
		words := make([]uint64, rowProjectionBlockRows/64)
		for i := 0; i < count; i += 2 {
			words[i/64] |= uint64(1) << uint(i%64)
		}
		if err := b.addBlock(count, words); err != nil {
			t.Fatal(err)
		}
	}
	data, mapped, file, err := b.finish()
	if err != nil || mapped || file != nil {
		t.Fatal(mapped, file, err)
	}
	return data
}

func repairProjectionCRC(data []byte) {
	payload := int(binary.LittleEndian.Uint64(data[24:]))
	directory := rowProjectionHeaderBytes + payload
	binary.LittleEndian.PutUint32(data[40:], crc32.ChecksumIEEE(data[rowProjectionHeaderBytes:directory]))
	binary.LittleEndian.PutUint32(data[44:], crc32.ChecksumIEEE(data[directory:]))
	binary.LittleEndian.PutUint32(data[60:], crc32.ChecksumIEEE(data[:60]))
}

func TestRowProjectionCodecRejectsCorruptionTruncationAndSemanticallyInvalidRechecksummedMasks(t *testing.T) {
	valid := projectionStorageFixture(t)
	if err := validateRowProjectionStorage(context.Background(), valid, 5000, 5000, 2500); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func([]byte){
		func(d []byte) { d[0] = 'X' },
		func(d []byte) { binary.LittleEndian.PutUint32(d[8:], 5001) },
		func(d []byte) { d[32] = 1 },
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			d[directory+28] = 1
		},
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			binary.LittleEndian.PutUint32(d[directory+8:], 1)
		},
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			binary.LittleEndian.PutUint32(d[directory+4:], 1000)
		},
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			binary.LittleEndian.PutUint64(d[directory+16:], 8)
		},
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			binary.LittleEndian.PutUint32(d[directory+24:], 7)
		},
		func(d []byte) {
			directory := rowProjectionHeaderBytes + int(binary.LittleEndian.Uint64(d[24:]))
			binary.LittleEndian.PutUint32(d[directory+12:], 2047)
		},
		func(d []byte) {
			payload := int(binary.LittleEndian.Uint64(d[24:]))
			d[rowProjectionHeaderBytes+payload-1] |= 128
			directory := rowProjectionHeaderBytes + payload
			binary.LittleEndian.PutUint32(d[16:], 2501)
			binary.LittleEndian.PutUint32(d[directory+rowProjectionRecordBytes+12:], 453)
		},
	} {
		data := slices.Clone(valid)
		mutation(data)
		repairProjectionCRC(data)
		selected := int(binary.LittleEndian.Uint32(data[16:]))
		if err := validateRowProjectionStorage(context.Background(), data, 5000, 5000, selected); err == nil {
			t.Fatal("invalid rechecksummed mask accepted")
		}
	}
	for _, n := range []int{0, 8, 63, len(valid) - 1} {
		if err := validateRowProjectionStorage(context.Background(), valid[:n], 5000, 5000, 2500); err == nil {
			t.Fatal("truncated projection accepted", n)
		}
	}
	corrupt := slices.Clone(valid)
	corrupt[rowProjectionHeaderBytes] ^= 1
	if err := validateRowProjectionStorage(context.Background(), corrupt, 5000, 5000, 2500); err == nil {
		t.Fatal("payload CRC ignored")
	}
	file := filepath.Join(t.TempDir(), "mask.bin")
	if err := os.WriteFile(file, valid, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := mapRowProjectionFile(context.Background(), file, 5000, 5000, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if err := unmapIndexFile(data); err != nil {
		t.Fatal(err)
	}
	if _, err := mapRowProjectionFile(context.Background(), file, 5001, 5000, 2500); err == nil {
		t.Fatal("different source accepted")
	}
}

func TestRowProjectionBillionPositionMaskWithTwoVisibleRecordsStaysCompressed(t *testing.T) {
	const n = formats.ExternalRows
	dir := filepath.Join(t.TempDir(), "never-created")
	b := newRowProjectionBuilder(context.Background(), dir, n, n, formats.RowProjectionMemoryBytes, &queryQuota{max: 1})
	defer b.abort()
	var words [rowProjectionBlockRows / 64]uint64
	for start := 0; start < n; start += rowProjectionBlockRows {
		clear(words[:])
		count := min(rowProjectionBlockRows, n-start)
		if start == 0 {
			words[0] = 1
		}
		if start+count == n {
			words[(count-1)/64] |= uint64(1) << uint((count-1)%64)
		}
		if err := b.addBlock(count, words[:]); err != nil {
			t.Fatal(err)
		}
	}
	data, mapped, file, err := b.finish()
	if err != nil || mapped || file != nil || len(data) > 2048 || len(b.blocks) != 3 {
		t.Fatal("sparse billion-position mask expanded", len(data), len(b.blocks), err)
	}
	base := ownRowOrder(&rowOrderData{count: n, sourceRows: n, start: 0, increasing: true})
	p := ownRowProjection(&rowProjectionData{base: base, sourceRows: n, parentRows: n, count: 2, storage: data, payloadBytes: int(b.payloadBytes), blocks: len(b.blocks)})
	defer p.Close()
	if p.At(0) != 0 || p.At(1) != n-1 || !slices.Equal(slices.Collect(p.Iter()), []int{0, n - 1}) {
		t.Fatal("compressed source positions changed")
	}
	for _, source := range []int{1, n / 2, n - 2} {
		if at, ok := p.Position(source); ok || at != 1 {
			t.Fatal(source, at, ok)
		}
	}
	if at, ok := p.Position(n - 1); !ok || at != 1 {
		t.Fatal(at, ok)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("sparse model used disk", err)
	}
}
