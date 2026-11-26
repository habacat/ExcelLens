package workbook

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"

	"excellens/internal/formats"
)

// MS-XLSB 2.1.4. A record type uses at most two 7-bit groups and its
// payload length uses at most four. Payloads are borrowed until the next record.
type xlsbRecords struct {
	input  *bufio.Reader
	data   []byte
	offset int64
}

func newXLSBRecords(input io.Reader) *xlsbRecords {
	return &xlsbRecords{input: bufio.NewReaderSize(input, 128<<10)}
}

func (r *xlsbRecords) next() (uint16, []byte, error) {
	b, err := r.input.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	r.offset++
	kind := uint16(b & 127)
	if b&128 != 0 {
		b, err = r.input.ReadByte()
		if err != nil {
			return 0, nil, xlsbTruncated(err)
		}
		r.offset++
		if b&128 != 0 {
			return 0, nil, errors.New("BIFF12 记录类型编码无效")
		}
		kind |= uint16(b) << 7
	}
	size := 0
	for shift := 0; shift < 28; shift += 7 {
		b, err = r.input.ReadByte()
		if err != nil {
			return 0, nil, xlsbTruncated(err)
		}
		r.offset++
		size |= int(b&127) << shift
		if b&128 == 0 {
			break
		}
		if shift == 21 {
			return 0, nil, errors.New("BIFF12 记录长度编码无效")
		}
	}
	if size > formats.XLSBRecordBytes {
		return 0, nil, fmt.Errorf("BIFF12 记录 %d 超过 %d MiB", kind, formats.XLSBRecordBytes>>20)
	}
	if cap(r.data) < size {
		r.data = make([]byte, size)
	}
	n, err := io.ReadFull(r.input, r.data[:size])
	r.offset += int64(n)
	if err != nil {
		return 0, nil, xlsbTruncated(err)
	}
	return kind, r.data[:size], nil
}

func xlsbTruncated(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func readXLSBPart(file *zip.File, limit uint64, handle func(uint16, []byte) error) error {
	return readXLSBPartWithReader(file, limit, nil, handle)
}

func readXLSBPartWithReader(file *zip.File, limit uint64, wrap func(io.Reader) io.Reader, handle func(uint16, []byte) error) error {
	if file == nil {
		return errors.New("缺少 BIFF12 部件")
	}
	if limit != 0 && file.UncompressedSize64 > limit {
		return fmt.Errorf("BIFF12 部件 %q 超过读取容量", file.Name)
	}
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	var source io.Reader = input
	if wrap != nil {
		source = wrap(source)
	}
	records := newXLSBRecords(source)
	for {
		kind, data, err := records.next()
		if err == io.EOF {
			return nil
		}
		if err == nil {
			err = handle(kind, data)
		}
		if err != nil {
			return fmt.Errorf("BIFF12 部件 %q 字节 %d 记录 %d：%w", file.Name, records.offset, kind, err)
		}
	}
}

type xlsbFields struct {
	data []byte
	err  error
}

func (f *xlsbFields) take(n int) []byte {
	if f.err != nil {
		return nil
	}
	if n < 0 || n > len(f.data) {
		f.err = io.ErrUnexpectedEOF
		return nil
	}
	data := f.data[:n]
	f.data = f.data[n:]
	return data
}

func (f *xlsbFields) u8() byte {
	b := f.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (f *xlsbFields) u16() uint16 {
	b := f.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (f *xlsbFields) u32() uint32 {
	b := f.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (f *xlsbFields) f64() float64 {
	b := f.take(8)
	if b == nil {
		return 0
	}
	v := math.Float64frombits(binary.LittleEndian.Uint64(b))
	if math.IsNaN(v) || math.IsInf(v, 0) {
		f.err = errors.New("BIFF12 浮点值无效")
	}
	return v
}

func (f *xlsbFields) wide(nullable bool, limit uint32) string {
	n := f.u32()
	if nullable && n == math.MaxUint32 {
		return ""
	}
	return f.utf16(n, limit)
}

func (f *xlsbFields) utf16(n, limit uint32) string {
	if n > limit && f.err == nil {
		f.err = errors.New("BIFF12 字符串长度超限")
	}
	data := f.take(int(n) * 2)
	if len(data) == 0 {
		return ""
	}
	var text strings.Builder
	text.Grow(len(data) / 2)
	for len(data) != 0 {
		r := rune(binary.LittleEndian.Uint16(data))
		data = data[2:]
		if utf16.IsSurrogate(r) {
			if len(data) < 2 {
				f.err = errors.New("BIFF12 UTF-16 字符串不完整")
				return ""
			}
			r = utf16.DecodeRune(r, rune(binary.LittleEndian.Uint16(data)))
			data = data[2:]
			if r == '\uFFFD' {
				f.err = errors.New("BIFF12 UTF-16 字符串无效")
				return ""
			}
		}
		if r < 128 {
			text.WriteByte(byte(r))
		} else {
			text.WriteRune(r)
		}
	}
	return text.String()
}

// RfX records store consecutive first/last rows and first/last columns,
// each in the native zero-based coordinate system.
func (f *xlsbFields) area() CellMerge {
	r1, r2, c1, c2 := f.u32(), f.u32(), f.u32(), f.u32()
	if f.err == nil && (r1 > r2 || r2 >= formats.ExcelRows || c1 > c2 || c2 >= formats.ExcelColumns) {
		f.err = errors.New("BIFF12 单元格区域坐标无效")
	}
	return CellMerge{Row: int(r1) + 1, EndRow: int(r2) + 1, Col: int(c1) + 1, EndCol: int(c2) + 1}
}
