package editfile

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"excellens/internal/formats"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	xunicode "golang.org/x/text/encoding/unicode"
)

func textEncoding(name string) (codec encoding.Encoding, order binary.ByteOrder, marker []byte, ok bool) {
	switch name {
	case "UTF-8":
		return nil, nil, nil, true
	case "UTF-8 BOM":
		return nil, nil, []byte{0xef, 0xbb, 0xbf}, true
	case "UTF-16 LE":
		return xunicode.UTF16(xunicode.LittleEndian, xunicode.IgnoreBOM), binary.LittleEndian, []byte{0xff, 0xfe}, true
	case "UTF-16 BE":
		return xunicode.UTF16(xunicode.BigEndian, xunicode.IgnoreBOM), binary.BigEndian, []byte{0xfe, 0xff}, true
	case "GB18030":
		return simplifiedchinese.GB18030, nil, nil, true
	default:
		return nil, nil, nil, false
	}
}

type textRecord struct {
	fields []string // nil for a physical blank line
	ending string
	start  int64
}

type textReader struct {
	in       *bufio.Reader
	sep      byte
	codec    encoding.Encoding
	order    binary.ByteOrder
	position int64
	raw      []byte
	fields   []string
}

func newTextReader(source io.Reader, offset int64, layout TextLayout) *textReader {
	codec, order, _, _ := textEncoding(layout.Encoding)
	return &textReader{in: bufio.NewReaderSize(source, 64<<10), sep: layout.Delimiter, codec: codec, order: order, position: offset}
}

func (r *textReader) physicalBytes(limit int) ([]byte, error) {
	r.raw = r.raw[:0]
	for {
		if r.order != nil {
			// Read whole UTF-16 code units. An individual 0x0a byte inside
			// another code point must not split the physical line.
			unit, err := r.in.Peek(2)
			if err != nil {
				if len(unit) != 0 || err != io.EOF {
					return nil, errors.New("UTF-16 文件含不完整的编码单元")
				}
				return r.raw, io.EOF
			}
			if len(r.raw)+2 > limit {
				return nil, errRecordSize
			}
			r.raw = append(r.raw, unit...)
			end := r.order.Uint16(unit) == '\n'
			_, _ = r.in.Discard(2)
			if end {
				return r.raw, nil
			}
			continue
		}
		line, err := r.in.ReadSlice('\n')
		if len(line)+len(r.raw) > limit {
			return nil, errRecordSize
		}
		if len(r.raw) == 0 && err != bufio.ErrBufferFull {
			return line, err
		}
		r.raw = append(r.raw, line...)
		if err != bufio.ErrBufferFull {
			return r.raw, err
		}
	}
}

var errRecordSize = fmt.Errorf("单条记录超过 %d MB 编辑上限", formats.EditRecordBytes>>20)

func (r *textReader) physical(limit int) (line, ending string, err error) {
	raw, readErr := r.physicalBytes(limit)
	if readErr != nil && (readErr != io.EOF || len(raw) == 0) {
		return "", "", readErr
	}
	r.position += int64(len(raw))
	if r.order != nil {
		for i := 0; i < len(raw); i += 2 {
			u := rune(r.order.Uint16(raw[i:]))
			if utf16.IsSurrogate(u) {
				if i+3 >= len(raw) || utf16.DecodeRune(u, rune(r.order.Uint16(raw[i+2:]))) == utf8.RuneError {
					return "", "", errors.New("源文件含无效 UTF-16 字符")
				}
				i += 2
			}
		}
	}
	if r.codec != nil {
		decoded, err := r.codec.NewDecoder().Bytes(raw)
		if err != nil {
			return "", "", err
		}
		if r.order == nil {
			original, err := r.codec.NewEncoder().Bytes(decoded)
			if err != nil || !bytes.Equal(original, raw) {
				return "", "", errors.New("源文件含不可逆的 GB18030 编码，请核对原文后转换为 UTF-8")
			}
		}
		raw = decoded
	} else if !utf8.Valid(raw) {
		return "", "", errors.New("源文件含无效的 UTF-8 编码")
	}
	line = string(raw)
	if strings.HasSuffix(line, "\r\n") {
		return line[:len(line)-2], "\r\n", nil
	}
	if strings.HasSuffix(line, "\n") || readErr == io.EOF && strings.HasSuffix(line, "\r") {
		return line[:len(line)-1], line[len(line)-1:], nil
	}
	return line, "", nil
}

// next follows strict separated-field quoting and preserves the original
// newline inside quotes. Blank physical lines are returned separately so they
// can survive saving without acquiring a logical source row number.
func (r *textReader) next() (textRecord, error) {
	start := r.position
	line, ending, err := r.physical(formats.EditRecordBytes)
	record := textRecord{start: start, ending: ending}
	if err != nil || line == "" {
		return record, err
	}
	clear(r.fields)
	r.fields = r.fields[:0]
	decoded := len(line) + len(ending)
	for {
		if len(r.fields) == formats.ExcelColumns || decoded > formats.EditRecordBytes {
			return record, errRecordSize
		}
		var field string
		if strings.HasPrefix(line, `"`) {
			line = line[1:]
			var quoted strings.Builder
			for {
				at := strings.IndexByte(line, '"')
				if at < 0 {
					if ending == "" || ending == "\r" {
						return record, errors.New("带引号的字段未结束")
					}
					quoted.WriteString(line)
					quoted.WriteString(ending)
					line, ending, err = r.physical(formats.EditRecordBytes - int(r.position-start))
					if err != nil {
						return record, fmt.Errorf("带引号的字段不完整：%w", err)
					}
					decoded += len(line) + len(ending)
					if decoded > formats.EditRecordBytes {
						return record, errRecordSize
					}
					continue
				}
				quoted.WriteString(line[:at])
				line = line[at+1:]
				if !strings.HasPrefix(line, `"`) {
					break
				}
				quoted.WriteByte('"')
				line = line[1:]
			}
			field = quoted.String()
			if len(line) > 0 && line[0] != r.sep {
				return record, errors.New("字段闭合引号后应为分隔符或记录末尾")
			}
		} else {
			at := strings.IndexByte(line, r.sep)
			if at < 0 {
				at = len(line)
			}
			field, line = line[:at], line[at:]
			if strings.ContainsRune(field, '"') {
				return record, errors.New("未加引号的字段中含引号")
			}
		}
		r.fields = append(r.fields, field)
		if len(line) == 0 {
			record.fields, record.ending = r.fields, ending
			return record, nil
		}
		line = line[1:]
	}
}
