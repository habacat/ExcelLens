package workbook

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/styles"
)

const xlsbSSTMagic = "ELNSST01"
const xlsbSSTHeaderBytes = 24

type xlsbString struct {
	text string
	rich []byte
}

type xlsbSSTSlot struct {
	xlsbString
	id    uint32
	valid bool
}

type xlsbSST struct {
	file       *os.File
	offsets    int64
	count      uint64
	recent     *[4096]xlsbSSTSlot
	previous   uint32
	indexPage  xlsbSSTPage
	stringPage xlsbSSTPage
}

// Index and text pages have separate bounds, so reading the last text entry
// never consumes index bytes. Random misses leave the read-ahead pages intact.
type xlsbSSTPage struct {
	start int64
	data  []byte
}

func (p *xlsbSSTPage) lookup(file *os.File, pos, end int64, size, readAhead int) ([]byte, error) {
	if offset := pos - p.start; offset >= 0 && offset <= int64(len(p.data)-size) {
		at := int(offset)
		return p.data[at : at+size], nil
	}
	if size > readAhead {
		return nil, nil
	}
	if cap(p.data) < readAhead {
		p.data = make([]byte, readAhead)
	}
	p.data = p.data[:min(int64(readAhead), end-pos)]
	if _, err := file.ReadAt(p.data, pos); err != nil {
		p.data = p.data[:0]
		return nil, err
	}
	p.start = pos
	return p.data[:size], nil
}

func (p *xlsbSSTPage) readHeader(file *os.File, pos, end int64, readAhead int) (uint64, error) {
	const size = 8
	data, err := p.lookup(file, pos, end, size, readAhead)
	if err != nil {
		return 0, err
	}
	if data == nil {
		var word [size]byte
		if _, err := file.ReadAt(word[:], pos); err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint64(word[:]), nil
	}
	return binary.LittleEndian.Uint64(data), nil
}

func xlsbPreviewStrings(part *zip.File, wrap func(io.Reader) io.Reader) (func(uint32) (xlsbString, error), func() error, error) {
	input, err := part.Open()
	if err != nil {
		return nil, nil, err
	}
	var source io.Reader = io.LimitReader(input, 8<<20)
	if wrap != nil {
		source = wrap(source)
	}
	record := newXLSBRecords(source)
	var prefix []xlsbString
	used := 0
	get := func(id uint32) (xlsbString, error) {
		if id >= 16384 {
			return xlsbString{}, ErrPreviewUnavailable
		}
		for uint32(len(prefix)) <= id {
			kind, data, err := record.next()
			if err != nil || kind == brtEndSst {
				return xlsbString{}, ErrPreviewUnavailable
			}
			if kind == brtSSTItem {
				f := xlsbFields{data: data}
				value := f.richString()
				if f.err != nil {
					return xlsbString{}, f.err
				}
				used += len(value.text) + len(value.rich)
				if used > 4<<20 {
					return xlsbString{}, ErrPreviewUnavailable
				}
				prefix = append(prefix, value)
			}
		}
		return prefix[id], nil
	}
	return get, input.Close, nil
}

var xlsbSSTGates = func() [16]chan struct{} {
	var gates [16]chan struct{}
	for i := range gates {
		gates[i] = make(chan struct{}, 1)
	}
	return gates
}()

// RichStr keeps the original plain text and bounded presentation start/font
// pairs. A phonetic layer is identified separately from the base text.
func (f *xlsbFields) richString() xlsbString {
	flags := f.u8()
	result := xlsbString{text: f.wide(false, 32767)}
	var runs []byte
	if flags&1 != 0 {
		count := f.u32()
		if count > formats.MaxCellRichRuns {
			f.err = errors.New("XLSB 富文本片段超过容量")
		} else {
			runs = f.take(4 * int(count))
		}
	}
	if len(runs) > 0 || flags&2 != 0 {
		result.rich = append([]byte{flags & 2}, runs...)
	}
	return result
}

func openXLSBSSTFile(path string) (*xlsbSST, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	bad := func(err error) (*xlsbSST, error) {
		f.Close()
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		return bad(err)
	}
	var head [xlsbSSTHeaderBytes]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return bad(err)
	}
	count, offsets := binary.LittleEndian.Uint64(head[8:]), binary.LittleEndian.Uint64(head[16:])
	if string(head[:8]) != xlsbSSTMagic || offsets < xlsbSSTHeaderBytes ||
		offsets > uint64(stat.Size()) || count > (uint64(stat.Size())-offsets)/8 || offsets+count*8 != uint64(stat.Size()) {
		return bad(errors.New("XLSB 共享字符串缓存结构损坏"))
	}
	return &xlsbSST{file: f, offsets: int64(offsets), count: count, recent: new([4096]xlsbSSTSlot), previous: ^uint32(0)}, nil
}

func (s *xlsbSST) close() {
	if s != nil {
		s.file.Close()
	}
}

func (s *xlsbSST) get(id uint32) (xlsbString, error) {
	if s == nil || uint64(id) >= s.count {
		return xlsbString{}, errors.New("XLSB 共享字符串索引越界")
	}
	slot := &s.recent[id%uint32(len(s.recent))]
	if slot.valid && slot.id == id {
		return slot.xlsbString, nil
	}
	var indexAhead, textAhead int
	if id == s.previous+1 {
		indexAhead, textAhead = 16<<10, 64<<10
	}
	s.previous = id
	index, err := s.indexPage.readHeader(s.file, s.offsets+8*int64(id), s.offsets+8*int64(s.count), indexAhead)
	if err != nil {
		return xlsbString{}, err
	}
	pos := int64(index)
	if pos < xlsbSSTHeaderBytes || pos > s.offsets-8 {
		return xlsbString{}, errors.New("XLSB 共享字符串缓存偏移无效")
	}
	header, err := s.stringPage.readHeader(s.file, pos, s.offsets, textAhead)
	if err != nil {
		return xlsbString{}, err
	}
	n, r := uint64(uint32(header)), header>>32
	total := n + r
	if n > maxExternalCell || r > 4*formats.MaxCellRichRuns+1 || total > uint64(s.offsets-pos-8) {
		return xlsbString{}, errors.New("XLSB 共享字符串缓存长度无效")
	}
	var value xlsbString
	if total != 0 {
		payload, err := s.stringPage.lookup(s.file, pos+8, s.offsets, int(total), textAhead)
		if err != nil {
			return xlsbString{}, err
		}
		if payload == nil {
			payload = make([]byte, int(total))
			if _, err := s.file.ReadAt(payload, pos+8); err != nil {
				return xlsbString{}, err
			}
			if r != 0 {
				value.rich = payload[n:]
			}
		} else if r != 0 {
			value.rich = bytes.Clone(payload[n:])
		}
		value.text = string(payload[:n])
	}
	if total <= 2048 {
		*slot = xlsbSSTSlot{xlsbString: value, id: id, valid: true}
	}
	return value, nil
}

func (w *Workbook) xlsbStrings(part *zip.File, cancel <-chan struct{}) (*xlsbSST, error) {
	if part == nil {
		return nil, nil
	}
	path := filepath.Join(w.CacheDir, "strings.xlsb-sst")
	if s, err := openXLSBSSTFile(path); err == nil {
		return s, nil
	}
	gate := xlsbSSTGates[w.Fingerprint[0]%byte(len(xlsbSSTGates))]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-cancel:
		return nil, errors.New("XLSB 共享字符串读取已取消")
	}
	if s, err := openXLSBSSTFile(path); err == nil {
		return s, nil
	}
	if err := w.buildXLSBSST(part, path, cancel); err != nil {
		return nil, err
	}
	return openXLSBSSTFile(path)
}

func (w *Workbook) buildXLSBSST(part *zip.File, target string, cancel <-chan struct{}) error {
	temp, err := os.CreateTemp(w.CacheDir, ".xlsb-sst-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	offsets, err := os.CreateTemp(w.CacheDir, ".xlsb-sst-idx-*")
	if err != nil {
		return err
	}
	defer os.Remove(offsets.Name())
	defer offsets.Close()
	if _, err := temp.Seek(xlsbSSTHeaderBytes, io.SeekStart); err != nil {
		return err
	}
	textWriter := bufio.NewWriterSize(temp, 256<<10)
	indexWriter := bufio.NewWriterSize(offsets, 64<<10)
	var seen, ended bool
	var count, expected uint64
	offset := uint64(xlsbSSTHeaderBytes)
	err = readXLSBPart(part, formats.XLSBSharedStringBytes, func(kind uint16, data []byte) error {
		select {
		case <-cancel:
			return errors.New("XLSB 共享字符串读取已取消")
		default:
		}
		f := xlsbFields{data: data}
		switch kind {
		case brtBeginSst:
			if seen {
				return errors.New("XLSB 共享字符串表重复")
			}
			seen = true
			f.u32()
			expected = uint64(f.u32())
		case brtEndSst:
			ended = true
		case brtSSTItem:
			if !seen || ended || count >= expected {
				return errors.New("XLSB 共享字符串数量不一致")
			}
			value := f.richString()
			if f.err != nil {
				return f.err
			}
			var data [8]byte
			binary.LittleEndian.PutUint64(data[:], offset)
			if _, err := indexWriter.Write(data[:]); err != nil {
				return err
			}
			binary.LittleEndian.PutUint32(data[:4], uint32(len(value.text)))
			binary.LittleEndian.PutUint32(data[4:], uint32(len(value.rich)))
			if _, err := textWriter.Write(data[:]); err != nil {
				return err
			}
			if _, err := textWriter.WriteString(value.text); err != nil {
				return err
			}
			if _, err := textWriter.Write(value.rich); err != nil {
				return err
			}
			count++
			offset += uint64(8 + len(value.text) + len(value.rich))
		}
		return f.err
	})
	if err != nil {
		return err
	}
	if !seen || !ended || count != expected {
		return errors.New("XLSB 共享字符串表不完整")
	}
	if err := errors.Join(textWriter.Flush(), indexWriter.Flush()); err != nil {
		return err
	}
	if _, err := offsets.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(temp, offsets); err != nil {
		return err
	}
	var header [xlsbSSTHeaderBytes]byte
	copy(header[:], xlsbSSTMagic)
	binary.LittleEndian.PutUint64(header[8:], count)
	binary.LittleEndian.PutUint64(header[16:], offset)
	if _, err := temp.WriteAt(header[:], 0); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := w.checkExternalUnchanged(); err != nil {
		return err
	}
	return replaceFile(temp.Name(), target)
}

func (w *Workbook) xlsbRichPresentation(text string, data []byte) (*RichText, error) {
	if len(data) < 1 || (len(data)-1)%4 != 0 {
		return nil, errors.New("XLSB 富文本缓存无效")
	}
	ends := make(map[int]bool, len(text)+1)
	ends[0] = true
	units := 0
	for _, r := range text {
		units++
		if r > 0xFFFF {
			units++
		}
		ends[units] = true
	}
	rich := &RichText{}
	if data[0]&2 != 0 {
		rich.Notice = "XLSB 文本含有未显示的注音层"
	}
	start := 0
	var font RichFont
	for at := 1; at < len(data); at += 4 {
		next := int(binary.LittleEndian.Uint16(data[at:]))
		id := int(binary.LittleEndian.Uint16(data[at+2:]))
		if next < start || !ends[next] || id >= len(w.source.xlsb.fonts) {
			return &RichText{Notice: "XLSB 富文本字体或字符范围无法呈现"}, nil
		}
		if next > start {
			rich.Runs = append(rich.Runs, RichTextRun{End: next, Font: font})
		}
		start, font = next, w.xlsbRichFont(id)
	}
	if start < units && len(data) > 1 {
		rich.Runs = append(rich.Runs, RichTextRun{End: units, Font: font})
	}
	return rich, nil
}

func (w *Workbook) xlsbRichFont(id int) RichFont {
	f := w.source.xlsb.fonts[id]
	size, _ := strconv.ParseFloat(f.Size.Val, 64)
	bold, italic, strike := styles.FlagEnabled(f.Bold), styles.FlagEnabled(f.Italic), styles.FlagEnabled(f.Strike)
	out := RichFont{
		Name: f.Name.Val, Size: size, Color: w.resolveColor(f.Color, w.IndexedColors),
		Bold: &bold, Italic: &italic, Strike: &strike, Vertical: f.Vertical.Val,
		Underline: "none", AutoColor: f.Color != nil && f.Color.Auto == "1",
	}
	if f.Underline != nil {
		out.Underline = f.Underline.Val
	}
	return out
}
