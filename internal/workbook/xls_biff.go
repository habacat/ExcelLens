package workbook

// Read-only BIFF8 (Excel 97-2003) record stream, binary helpers, and shared
// string table (SST) decoding. XLS is never executed or converted through
// Office. Formula cells use their stored results; formula tokens are decoded
// only where unambiguous. Unsupported tokens remain explicitly marked.
import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
)

type xlsSheet struct {
	name   string
	offset int64
	state  string
}

type xlsGlobals struct {
	sheets    []xlsSheet
	sst       []string
	styles    []StyleInfo
	visual    []VisualStyle
	formats   map[int]string
	date1904  bool
	catalogue Catalogue
}

type biffReader struct {
	r        *bufio.Reader
	pos, end int64
	scratch  []byte
}

func newBIFFReader(stream io.ReaderAt, offset, end int64) *biffReader {
	return &biffReader{r: bufio.NewReaderSize(io.NewSectionReader(stream, offset, end-offset), 256<<10), pos: offset, end: end}
}

func (r *biffReader) next() (uint16, []byte, error) {
	if r.pos >= r.end {
		return 0, nil, io.EOF
	}
	var hdr [4]byte
	if _, e := io.ReadFull(r.r, hdr[:]); e != nil {
		return 0, nil, e
	}
	size := int(binary.LittleEndian.Uint16(hdr[2:4]))
	r.pos += 4
	if size > 8224 || r.pos+int64(size) > r.end {
		return 0, nil, errors.New("BIFF 记录长度无效或已截断")
	}
	if cap(r.scratch) < size {
		r.scratch = make([]byte, size)
	}
	data := r.scratch[:size]
	if _, e := io.ReadFull(r.r, data); e != nil {
		return 0, nil, e
	}
	r.pos += int64(size)
	return binary.LittleEndian.Uint16(hdr[0:2]), data, nil
}

func xlsUint16(b []byte, i int) uint16 {
	if i < 0 || i+2 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint16(b[i : i+2])
}

func xlsUint32(b []byte, i int) uint32 {
	if i < 0 || i+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[i : i+4])
}

func xlsFloat64(b []byte, i int) float64 {
	if i < 0 || i+8 > len(b) {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(b[i : i+8]))
}

func xlsInlineUnicode(b []byte, short bool) (string, int, error) {
	prefix := 3
	if short {
		prefix = 2
	}
	if len(b) < prefix {
		return "", 0, errors.New("BIFF Unicode 字符串头截断")
	}
	chars := int(xlsUint16(b, 0))
	flags := b[2]
	if short {
		chars = int(b[0])
		flags = b[1]
	}
	ptr := prefix
	rich, ext := 0, 0
	if flags&8 != 0 {
		if ptr+2 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		rich = int(xlsUint16(b, ptr))
		ptr += 2
	}
	if flags&4 != 0 {
		if ptr+4 > len(b) {
			return "", 0, io.ErrUnexpectedEOF
		}
		ext = int(xlsUint32(b, ptr))
		ptr += 4
	}
	bytesPer := 1
	if flags&1 != 0 {
		bytesPer = 2
	}
	if chars < 0 || chars > 65535 || chars*bytesPer > len(b)-ptr {
		return "", 0, errors.New("BIFF Unicode 字符串长度无效")
	}
	result := decodeBIFFChars(b[ptr:ptr+chars*bytesPer], bytesPer == 2)
	ptr += chars * bytesPer
	if rich > 4096 || ext > 16<<20 || rich*4+ext > len(b)-ptr {
		return "", 0, errors.New("BIFF 字符串富文本数据越界")
	}
	ptr += rich*4 + ext
	return result, ptr, nil
}

func decodeBIFFChars(b []byte, u16 bool) string {
	if !u16 {
		var out strings.Builder
		out.Grow(len(b))
		for _, v := range b {
			out.WriteRune(rune(v))
		}
		return out.String()
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(units))
}

type xlsSegments struct {
	seg  [][]byte
	s, i int
}

func (c *xlsSegments) nextBytes(n int) ([]byte, error) {
	if n < 0 || n > 64<<20 {
		return nil, errors.New("BIFF SST 字段过大")
	}
	data := make([]byte, n)
	at := 0
	for at < n {
		for c.s < len(c.seg) && c.i >= len(c.seg[c.s]) {
			c.s++
			c.i = 0
		}
		if c.s >= len(c.seg) {
			return nil, io.ErrUnexpectedEOF
		}
		avail := len(c.seg[c.s]) - c.i
		m := min(n-at, avail)
		copy(data[at:at+m], c.seg[c.s][c.i:c.i+m])
		c.i += m
		at += m
	}
	return data, nil
}

func (c *xlsSegments) stringChars(chars int, u16 bool) (string, error) {
	if chars < 0 || chars > 65535 {
		return "", errors.New("SST 字符串长度无效")
	}
	// Decode UTF-16 only *after* collecting all character units: a valid
	// surrogate pair can straddle a BIFF CONTINUE boundary. Decoding each
	// individual record turns such characters into two replacement runes.
	units := make([]uint16, 0, chars)
	for len(units) < chars {
		for c.s < len(c.seg) && c.i >= len(c.seg[c.s]) {
			c.s++
			c.i = 0
			if c.s < len(c.seg) && len(c.seg[c.s]) > 0 {
				// This byte is present only when CONTINUE splits the
				// *characters* of an XLUnicodeRichExtendedString.
				u16 = c.seg[c.s][0]&1 != 0
				c.i = 1
			}
		}
		if c.s >= len(c.seg) {
			return "", io.ErrUnexpectedEOF
		}
		avail := len(c.seg[c.s]) - c.i
		if u16 {
			n := min(chars-len(units), avail/2)
			if n == 0 {
				return "", errors.New("SST UTF-16 字符被截断")
			}
			for j := 0; j < n; j++ {
				units = append(units, xlsUint16(c.seg[c.s], c.i+j*2))
			}
			c.i += n * 2
		} else {
			n := min(chars-len(units), avail)
			if n == 0 {
				continue
			}
			for _, ch := range c.seg[c.s][c.i : c.i+n] {
				units = append(units, uint16(ch))
			}
			c.i += n
		}
	}
	result := string(utf16.Decode(units))
	if len(result) > maxExternalCell {
		return "", errors.New("SST 单字符串超过 8MB")
	}
	return result, nil
}

func parseXLSSST(segments [][]byte) ([]string, error) {
	c := &xlsSegments{seg: segments}
	head, e := c.nextBytes(8)
	if e != nil {
		return nil, e
	}
	unique := xlsUint32(head, 4)
	if unique > 8_000_000 {
		return nil, errors.New("BIFF 共享字符串数量超过安全上限")
	}
	result := make([]string, 0, min(int(unique), 8192))
	for i := uint32(0); i < unique; i++ {
		b, e := c.nextBytes(3)
		if e != nil {
			return nil, fmt.Errorf("SST 字符串 %d: %w", i, e)
		}
		chars := int(xlsUint16(b, 0))
		flags := b[2]
		rich, ext := 0, 0
		if flags&8 != 0 {
			v, e := c.nextBytes(2)
			if e != nil {
				return nil, e
			}
			rich = int(xlsUint16(v, 0))
		}
		if flags&4 != 0 {
			v, e := c.nextBytes(4)
			if e != nil {
				return nil, e
			}
			ext = int(xlsUint32(v, 0))
		}
		text, e := c.stringChars(chars, flags&1 != 0)
		if e != nil {
			return nil, fmt.Errorf("SST 字符串 %d: %w", i, e)
		}
		if rich > 4096 || ext > 16<<20 {
			return nil, errors.New("SST 富文本字段过长")
		}
		if _, e = c.nextBytes(rich*4 + ext); e != nil {
			return nil, e
		}
		result = append(result, text)
	}
	return result, nil
}
