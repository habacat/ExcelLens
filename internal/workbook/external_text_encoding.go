package workbook

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type fixedColumns struct {
	start  []int
	end    []int
	header []string
	rule   string
}

func detectTextEncoding(sample []byte) string {
	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		return "UTF-8 BOM"
	}
	if bytes.HasPrefix(sample, []byte{0xFF, 0xFE}) {
		return "UTF-16 LE"
	}
	if bytes.HasPrefix(sample, []byte{0xFE, 0xFF}) {
		return "UTF-16 BE"
	}
	// BOM-less UTF16 is common in Oracle exports; reject false positives by
	// checking the ASCII range of the opposite byte lane.
	if len(sample) > 24 {
		var evenZero, oddZero int
		for i := 0; i+1 < len(sample) && i < 1024; i += 2 {
			if sample[i] == 0 {
				evenZero++
			}
			if sample[i+1] == 0 {
				oddZero++
			}
		}
		if oddZero > len(sample[:min(len(sample), 1024)])/5 && evenZero < oddZero/5 {
			return "UTF-16 LE"
		}
		if evenZero > len(sample[:min(len(sample), 1024)])/5 && oddZero < evenZero/5 {
			return "UTF-16 BE"
		}
	}
	if utf8.Valid(sample) {
		return "UTF-8"
	}
	for trim := 1; trim < utf8.UTFMax && trim < len(sample); trim++ {
		if utf8.Valid(sample[:len(sample)-trim]) && !utf8.FullRune(sample[len(sample)-trim:]) {
			return "UTF-8"
		}
	}
	return "GB18030"
}

func decodeTextSample(raw []byte, encoding string) (string, error) {
	switch encoding {
	case "UTF-8", "UTF-8 BOM":
		raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
		// The sample may terminate inside the final character.
		return strings.ToValidUTF8(string(raw), "\uFFFD"), nil
	case "UTF-16 LE", "UTF-16 BE":
		if encoding == "UTF-16 LE" {
			raw = bytes.TrimPrefix(raw, []byte{0xFF, 0xFE})
		} else {
			raw = bytes.TrimPrefix(raw, []byte{0xFE, 0xFF})
		}
		raw = raw[:len(raw)/2*2]
		return decodeUTF16(raw, encoding), nil
	default:
		// A sample can end in the middle of a multi-byte character; keep a
		// short prefix for delimiter detection and let full import validate.
		for trim := 0; trim <= 4 && len(raw)-trim > 0; trim++ {
			if s, err := decodeGB18030(raw[:len(raw)-trim]); err == nil {
				return s, nil
			}
		}
		return "", errors.New("数据似乎采用 GB18030/GBK 编码，但无法解码。请确认文件编码")
	}
}

func decodeUTF16(data []byte, encoding string) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if encoding == "UTF-16 BE" {
			units = append(units, binary.BigEndian.Uint16(data[i:]))
		} else {
			units = append(units, binary.LittleEndian.Uint16(data[i:]))
		}
	}
	return string(utf16.Decode(units))
}

// Each line is individually decoded into UTF-8 while retaining the original
// newlines. csv.Reader can then handle quoted multi-line fields correctly.
type decodedTextReader struct {
	stream   io.Reader
	raw      *bufio.Reader
	encoding string
	pending  []byte
	started  bool
	done     bool
}

func newDecodedTextReader(file io.Reader, encoding string) *decodedTextReader {
	if encoding == "GB18030" {
		// Decode buffered chunks instead of allocating a UTF-16 buffer and
		// making two Windows API calls for every physical journal line.
		return &decodedTextReader{stream: transform.NewReader(&boundedTextLines{r: file}, simplifiedchinese.GB18030.NewDecoder())}
	}
	return &decodedTextReader{raw: bufio.NewReaderSize(file, 256<<10), encoding: encoding}
}

func (r *decodedTextReader) Read(p []byte) (int, error) {
	if r.stream != nil {
		return r.stream.Read(p)
	}
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		var raw []byte
		var err error
		if r.encoding == "UTF-16 LE" || r.encoding == "UTF-16 BE" {
			raw, err = r.readUTF16RawLine()
		} else {
			raw, err = r.raw.ReadBytes('\n')
		}
		if len(raw) > maxTextLine {
			return 0, fmt.Errorf("文本单行超过 %d MB 安全上限", maxTextLine>>20)
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		if err == io.EOF {
			r.done = true
		}
		if len(raw) == 0 {
			return 0, io.EOF
		}
		if !r.started {
			r.started = true
			switch r.encoding {
			case "UTF-8 BOM":
				raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
			case "UTF-16 LE":
				raw = bytes.TrimPrefix(raw, []byte{0xFF, 0xFE})
			case "UTF-16 BE":
				raw = bytes.TrimPrefix(raw, []byte{0xFE, 0xFF})
			}
		}
		switch r.encoding {
		case "UTF-8", "UTF-8 BOM":
			if !utf8.Valid(raw) {
				return 0, errors.New("UTF-8 数据含无效编码，请核对文件编码")
			}
			r.pending = raw
		case "UTF-16 LE", "UTF-16 BE":
			if len(raw)%2 != 0 {
				return 0, errors.New("UTF-16 编码长度不是偶数")
			}
			r.pending = []byte(decodeUTF16(raw, r.encoding))
		default:
			text, e := decodeGB18030(raw)
			if e != nil {
				return 0, fmt.Errorf("GB18030/GBK 解码失败：%w", e)
			}
			r.pending = []byte(text)
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// UTF-16 code units must be read atomically: a Chinese character such as
// U+300A (《) contains byte 0x0A, which is NOT a linefeed by itself.
func (r *decodedTextReader) readUTF16RawLine() ([]byte, error) {
	var data []byte
	for {
		a, e := r.raw.ReadByte()
		if e != nil {
			if e == io.EOF && len(data) > 0 {
				return data, io.EOF
			}
			return data, e
		}
		b, e := r.raw.ReadByte()
		if e != nil {
			return nil, errors.New("UTF-16 数据存在不完整的双字节字符")
		}
		data = append(data, a, b)
		if len(data) > maxTextLine {
			return nil, errors.New("UTF-16 单行超过 64MB 安全上限")
		}
		var unit uint16
		if r.encoding == "UTF-16 BE" {
			unit = uint16(a)<<8 | uint16(b)
		} else {
			unit = uint16(b)<<8 | uint16(a)
		}
		if unit == 10 {
			return data, nil
		}
	}
}

type boundedTextLines struct {
	r    io.Reader
	line int
}

func (r *boundedTextLines) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	remaining := p[:n]
	for len(remaining) > 0 {
		end := bytes.IndexByte(remaining, '\n')
		if end < 0 {
			r.line += len(remaining)
			remaining = nil
		} else {
			r.line += end + 1
			remaining = remaining[end+1:]
		}
		if r.line > maxTextLine {
			return n, errors.New("文本单行超过 64MB 安全上限")
		}
		if end >= 0 {
			r.line = 0
		}
	}
	return n, err
}

func detectTextLayout(sample, format string) (rune, *fixedColumns) {
	if format == "tsv" {
		return '\t', nil
	}
	lines := strings.Split(strings.ReplaceAll(sample, "\r\n", "\n"), "\n")
	if format == "txt" {
		for i := 1; i < len(lines) && i < 15; i++ {
			rule := parseOracleRule(strings.TrimRight(lines[i], "\r"))
			if rule == nil {
				continue
			}
			rule.header = splitFixedLine(lines[i-1], rule)
			return 0, rule
		}
		// Oracle journal spools have an explicit field schema and literal
		// quotes. Let that schema take priority over CSV quote heuristics.
		if detectOracleTextLayout(sample, '@') != nil {
			return '@', nil
		}
	}
	best := rune(0)
	bestScore := -1e9
	for _, d := range []rune{'\t', '|', ',', ';', '@'} {
		reader := csv.NewReader(strings.NewReader(sample))
		reader.Comma = d
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		counts := make(map[int]int)
		valid, more := 0, 0
		for valid < 24 {
			row, err := reader.Read()
			if err != nil {
				break
			}
			if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
				continue
			}
			valid++
			if len(row) > 1 {
				more++
			}
			counts[len(row)]++
		}
		if more == 0 {
			continue
		}
		modal, freq := 0, 0
		for n, f := range counts {
			if f > freq || f == freq && n > modal {
				modal, freq = n, f
			}
		}
		score := float64(freq*100 + more*10 + modal*4 - valid*8)
		if d == '\t' || d == '|' {
			score += 5
		}
		if score > bestScore {
			bestScore = score
			best = d
		}
	}
	if best == 0 && format == "csv" {
		return ',', nil
	}
	return best, nil
}

func parseOracleRule(s string) *fixedColumns {
	r := []rune(strings.TrimRight(s, "\r"))
	if len(r) < 5 {
		return nil
	}
	var starts, ends []int
	for i := 0; i < len(r); {
		if r[i] != '-' && r[i] != '=' {
			i++
			continue
		}
		j := i
		for j < len(r) && (r[j] == '-' || r[j] == '=') {
			j++
		}
		if j-i >= 2 {
			starts = append(starts, i)
			ends = append(ends, j)
		}
		i = j
	}
	if len(starts) < 2 {
		return nil
	}
	for _, c := range r {
		if c != '-' && c != '=' && c != ' ' && c != '\t' {
			return nil
		}
	}
	return &fixedColumns{start: starts, end: ends, rule: strings.TrimSpace(s)}
}

// Oracle SQL*Plus pads fields by displayed (not UTF-8 byte) width. Most CJK
// characters occupy two console columns; preserving these positions avoids
// shifting the debit/credit columns in Chinese financial ledgers.
func runeCellWidth(r rune) int {
	if r >= 0x1100 && (r <= 0x11FF || r >= 0x2E80 && r <= 0xA4CF || r >= 0xAC00 && r <= 0xD7A3 || r >= 0xF900 && r <= 0xFAFF || r >= 0xFE10 && r <= 0xFE6F || r >= 0xFF01 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6) {
		return 2
	}
	return 1
}

func splitFixedLine(s string, rule *fixedColumns) []string {
	// Split at the midpoint of the inter-column gaps, not within a numeric
	// field. Right-aligned values and Chinese headers remain whole.
	cuts := make([]int, 0, len(rule.start)+1)
	cuts = append(cuts, 0)
	for i := 1; i < len(rule.start); i++ {
		cuts = append(cuts, (rule.end[i-1]+rule.start[i])/2)
	}
	cuts = append(cuts, 1<<30)
	var fields []string
	var text strings.Builder
	col := 0
	idx := 0
	for _, c := range s {
		for idx+1 < len(cuts)-1 && col >= cuts[idx+1] {
			fields = append(fields, strings.TrimSpace(text.String()))
			text.Reset()
			idx++
		}
		text.WriteRune(c)
		col += runeCellWidth(c)
	}
	fields = append(fields, strings.TrimSpace(text.String()))
	for len(fields) < len(rule.start) {
		fields = append(fields, "")
	}
	if len(fields) > len(rule.start) {
		fields = fields[:len(rule.start)]
	}
	return fields
}
