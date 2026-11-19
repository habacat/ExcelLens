// Package editfile checks original file coordinates and generates verified
// copies from a fixed edit revision. It never writes through a source handle.
package editfile

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/separated"
	"excellens/internal/workbook"
	"golang.org/x/text/transform"
)

var (
	ErrBinding = errors.New("编辑源内容已变更，请打开创建副本时的原文件")
	ErrTarget  = errors.New("所选地址没有可编辑的单元格")
)

type TextLayout struct {
	Sheet     string  `json:"sheet"`
	Delimiter byte    `json:"delimiter"`
	Encoding  string  `json:"encoding"`
	MarkBytes int     `json:"markBytes"`
	Rows      int     `json:"rows"`
	Columns   int     `json:"columns"`
	Size      int64   `json:"size"`
	Offsets   []int64 `json:"offsets"`
}

func (l TextLayout) Valid() bool {
	_, _, mark, encodingOK := textEncoding(l.Encoding)
	if !encodingOK || l.MarkBytes != 0 && l.MarkBytes != len(mark) || l.Encoding == "UTF-8 BOM" && l.MarkBytes != len(mark) ||
		l.Sheet == "" || !strings.ContainsRune(",\t;|@", rune(l.Delimiter)) ||
		l.Rows < 1 || l.Rows > formats.EditTextRows || l.Columns < 1 || l.Columns > formats.ExcelColumns ||
		l.Size <= int64(l.MarkBytes) || l.Size > formats.EditSourceBytes || len(l.Offsets) != (l.Rows-1)/formats.EditTextStride+1 {
		return false
	}
	previous := int64(l.MarkBytes) - 1
	for _, offset := range l.Offsets {
		if offset <= previous || offset >= l.Size {
			return false
		}
		previous = offset
	}
	return true
}

// InspectText reuses the selected source dialect but records the exact field
// counts and original stream offsets, which the normal row cache omits.
func InspectText(ctx context.Context, f *workbook.EditSourceFile, wb *workbook.Workbook) (TextLayout, string, error) {
	delim, name := wb.EditDialect()
	if len(wb.Sheets) != 1 || delim == 0 || wb.SourceInfo.Incomplete {
		return TextLayout{}, "", ErrBinding
	}
	layout := TextLayout{Sheet: wb.Sheets[0].Name, Delimiter: delim, Encoding: name, Size: f.Size()}
	_, _, marker, ok := textEncoding(name)
	if !ok {
		return layout, "", errors.New("源编码暂不支持编辑")
	}
	h := sha256.New()
	in := io.TeeReader(io.NewSectionReader(f, 0, f.Size()), h)
	if len(marker) > 0 {
		var prefix [3]byte
		if n, err := f.ReadAt(prefix[:len(marker)], 0); err == nil && bytes.Equal(prefix[:n], marker) {
			layout.MarkBytes = n
			if _, err := io.CopyN(io.Discard, in, int64(n)); err != nil {
				return layout, "", err
			}
		}
	}
	reader := newTextReader(in, int64(layout.MarkBytes), layout)
	if err := scanText(ctx, reader, func(row int, rec textRecord) error {
		if row > formats.EditTextRows {
			return fmt.Errorf("文本记录超过 %d 行编辑上限", formats.EditTextRows)
		}
		if rec.fields != nil {
			if (row-1)%formats.EditTextStride == 0 {
				layout.Offsets = append(layout.Offsets, rec.start)
			}
			layout.Rows, layout.Columns = row, max(layout.Columns, len(rec.fields))
		}
		return nil
	}); err != nil {
		return layout, "", err
	}
	if !layout.Valid() || reader.position != f.Size() {
		return layout, "", errors.New("文本中没有可编辑的完整记录")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	verified, err := f.SHA256(ctx)
	if err == nil && verified != digest {
		err = ErrBinding
	}
	return layout, digest, err
}

func scanText(ctx context.Context, reader *textReader, visit func(int, textRecord) error) error {
	row := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, err := reader.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("第 %d 条记录或其附近的文本无效：%w", row+1, err)
		}
		if rec.fields != nil {
			row++
		}
		if err := visit(row, rec); err != nil {
			return err
		}
	}
}

func VerifySource(ctx context.Context, f *workbook.EditSourceFile, expected string) error {
	proof, err := f.SHA256(ctx)
	if err != nil {
		return err
	}
	if proof != expected {
		return ErrBinding
	}
	return nil
}

func textValue(raw string) editdoc.Value {
	if raw == "" {
		return editdoc.Value{Kind: "blank"}
	}
	return editdoc.Value{Kind: "text", Raw: raw}
}

// ReadTextCells walks at most one sparse-index stride for a single source row.
// A batch shares nearby record walks and never uses a sorted-view row index.
func ReadTextCells(ctx context.Context, f *workbook.EditSourceFile, l TextLayout, targets []editdoc.Address) (map[editdoc.Address]editdoc.Value, error) {
	if !l.Valid() || f.Size() != l.Size {
		return nil, ErrBinding
	}
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, func(a, b editdoc.Address) int { return cmp.Compare(a.Row, b.Row) })
	values := make(map[editdoc.Address]editdoc.Value, len(ordered))
	var reader *textReader
	var record textRecord
	row := 0
	for _, target := range ordered {
		if target.Sheet != l.Sheet || target.Row < 1 || target.Row > l.Rows || target.Col < 1 || target.Col > l.Columns {
			return nil, ErrTarget
		}
		block := (target.Row - 1) / formats.EditTextStride
		first := block * formats.EditTextStride
		if row <= first {
			start := l.Offsets[block]
			reader = newTextReader(io.NewSectionReader(f, start, l.Size-start), start, l)
			row = first
		}
		for row < target.Row {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var err error
			record, err = reader.next()
			if err != nil {
				return nil, fmt.Errorf("编辑源记录偏移已失效：%w", err)
			}
			if record.fields != nil {
				row++
			}
		}
		if target.Col > len(record.fields) {
			values[target] = editdoc.Value{Kind: "missing"}
			continue
		}
		value := textValue(record.fields[target.Col-1])
		if len(value.Raw) > formats.EditCellBytes {
			return nil, fmt.Errorf("%s%d 的原文超过单格编辑上限", workbook.ColumnName(target.Col), target.Row)
		}
		values[target] = value
	}
	return values, f.Check()
}

type TextOptions struct {
	Encoding string `json:"encoding"`
	Protect  bool   `json:"protect"`
}

func (l TextLayout) output(options TextOptions) (TextLayout, error) {
	switch options.Encoding {
	case "source":
	case "utf8":
		l.Encoding, l.MarkBytes = "UTF-8", 0
	case "utf8-bom":
		l.Encoding, l.MarkBytes = "UTF-8 BOM", 3
	default:
		return l, errors.New("请选择副本编码")
	}
	return l, nil
}

type TextResult struct {
	Layout    TextLayout
	Protected int64
	Signature [32]byte
}

type recordDigest struct {
	h hash.Hash
	b []byte
}

func (d *recordDigest) add(record textRecord) {
	d.b = binary.AppendUvarint(d.b[:0], uint64(len(record.fields)))
	d.b = binary.AppendUvarint(d.b, uint64(len(record.ending)))
	d.b = append(d.b, record.ending...)
	for _, field := range record.fields {
		d.b = binary.AppendUvarint(d.b, uint64(len(field)))
		d.b = append(d.b, field...)
	}
	_, _ = d.h.Write(d.b)
}

func (d *recordDigest) sum() (value [32]byte) {
	d.h.Sum(value[:0])
	return value
}

// WriteText emits exact decoded field text (or explicit protection), retains
// each record's field count and line ending, and verifies each consumed byte
// against the fixed original digest before the caller can publish the copy.
func WriteText(ctx context.Context, dst io.Writer, source *workbook.EditSourceFile, l TextLayout, proof string, edits map[editdoc.Address]editdoc.Value, options TextOptions) (TextResult, error) {
	var result TextResult
	if !l.Valid() || source.Size() != l.Size {
		return result, ErrBinding
	}
	out, err := l.output(options)
	if err != nil {
		return result, err
	}
	result.Layout = out
	codec, _, marker, _ := textEncoding(out.Encoding)
	if out.MarkBytes > 0 {
		if _, err := dst.Write(marker); err != nil {
			return result, err
		}
	}
	var trans *transform.Writer
	if codec != nil {
		trans = transform.NewWriter(dst, codec.NewEncoder())
		dst = trans
	}
	buf := bufio.NewWriterSize(dst, 128<<10)
	raw := sha256.New()
	in := io.TeeReader(io.NewSectionReader(source, 0, source.Size()), raw)
	if _, err := io.CopyN(io.Discard, in, int64(l.MarkBytes)); err != nil {
		return result, err
	}
	reader := newTextReader(in, int64(l.MarkBytes), l)
	digest := recordDigest{h: sha256.New()}
	seen, rows := 0, 0
	err = scanText(ctx, reader, func(row int, rec textRecord) error {
		for i, field := range rec.fields {
			if i > 0 {
				if err := buf.WriteByte(l.Delimiter); err != nil {
					return err
				}
			}
			if v, ok := edits[editdoc.Address{Sheet: l.Sheet, Row: row, Col: i + 1}]; ok {
				if !v.Valid() || v.Kind != "text" && v.Kind != "blank" {
					return editdoc.ErrInvalid
				}
				field = v.Raw
				seen++
			}
			if options.Protect {
				changed := separated.ProtectText(field)
				if changed != field {
					result.Protected++
				}
				field = changed
			}
			rec.fields[i] = field
			// A single empty field needs quotes. An unquoted empty line
			// would lose that record and shift every following coordinate.
			force := len(rec.fields) == 1 && field == "" || i == 0 && strings.HasPrefix(field, "\ufeff")
			if err := separated.WriteField(buf, field, l.Delimiter, force); err != nil {
				return err
			}
		}
		digest.add(rec)
		rows = row
		_, err := buf.WriteString(rec.ending)
		return err
	})
	if err != nil {
		return result, err
	}
	if seen != len(edits) || rows != l.Rows || reader.position != l.Size || hex.EncodeToString(raw.Sum(nil)) != proof {
		return result, ErrBinding
	}
	if err := source.Check(); err != nil {
		return result, err
	}
	if err := buf.Flush(); err != nil {
		return result, err
	}
	if trans != nil {
		if err := trans.Close(); err != nil {
			return result, err
		}
	}
	result.Signature = digest.sum()
	return result, nil
}

func VerifyText(ctx context.Context, file *os.File, expected TextResult) error {
	layout := expected.Layout
	_, _, mark, _ := textEncoding(layout.Encoding)
	if layout.MarkBytes > 0 {
		actual := make([]byte, layout.MarkBytes)
		if _, err := io.ReadFull(file, actual); err != nil || !bytes.Equal(actual, mark) {
			return errors.New("副本编码标记核对失败")
		}
	}
	reader := newTextReader(file, int64(layout.MarkBytes), layout)
	digest := recordDigest{h: sha256.New()}
	rows := 0
	if err := scanText(ctx, reader, func(row int, rec textRecord) error {
		rows = row
		digest.add(rec)
		return nil
	}); err != nil {
		return err
	}
	if rows != layout.Rows || digest.sum() != expected.Signature {
		return errors.New("副本记录、列数或格内原文核对失败")
	}
	return nil
}
