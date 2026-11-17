package workbook

// Office/WPS also export HTML tables under the .xls extension. Read their
// stored cell text into the native row cache without Office, a browser, script
// execution, external resources, or an in-memory HTML document tree.
import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func openXLSWorkbook(filePath string) (*Workbook, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	sample := make([]byte, 256<<10)
	n, err := io.ReadFull(f, sample)
	_ = f.Close()
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	sample = sample[:n]
	if !bytes.HasPrefix(sample, []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")) {
		encoding := detectTextEncoding(sample)
		if text, err := decodeTextSample(sample, encoding); err == nil {
			prefix := strings.ToLower(strings.TrimSpace(text))
			if strings.HasPrefix(prefix, "<html") || strings.HasPrefix(prefix, "<!doctype html") ||
				strings.HasPrefix(prefix, "<table") || strings.HasPrefix(prefix, "<!--") ||
				strings.HasPrefix(prefix, "<?xml") && strings.Contains(prefix, "<html") {
				return openHTMLXLSWorkbook(filePath, encoding)
			}
		}
	}
	return openBIFFWorkbook(filePath)
}

func openHTMLXLSWorkbook(filePath, encoding string) (*Workbook, error) {
	// Display metadata has its own version. Keep the workbook identity so local
	// annotations, analysis sheets and user layout settings remain associated.
	fingerprint, st, err := fingerprintFile(filePath, "xls-html-v3-document")
	if err != nil {
		return nil, err
	}
	wb, err := newExternalWorkbook(filePath, "xls-html", fingerprint, st)
	if err != nil {
		return nil, err
	}
	wb.source.fingerprintDomain = "xls-html-v3-document"
	cachePath := filepath.Join(wb.CacheDir, "html-metadata-v4.json")
	if cache, e := os.Open(cachePath); e == nil {
		raw, readErr := io.ReadAll(io.LimitReader(cache, (8<<20)+1))
		_ = cache.Close()
		var cached htmlWorkbookMetadata
		if readErr == nil && len(raw) < 8<<20 && json.Unmarshal(raw, &cached) == nil &&
			cached.Version == 4 && cached.Fingerprint == wb.Fingerprint && cached.Checksum != "" &&
			len(cached.Names) > 0 && len(cached.Names) <= 4096 && cached.Styles.valid() && cached.Checksum == cached.checksum() {
			return finishHTMLWorkbook(wb, encoding, cached.Names, &cached.Styles), nil
		}
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	styles := newHTMLStyleMetadata()
	names, err := readStyledHTMLTables(newDecodedTextReader(f, encoding), -1, nil, nil, styles)
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 格式 XLS 失败：%w", err)
	}
	if len(names) == 0 {
		return nil, errors.New("HTML 格式 XLS 中没有可读取的表格")
	}
	styles.seal()
	metadata := htmlWorkbookMetadata{Version: 4, Fingerprint: wb.Fingerprint, Names: names, Styles: *styles}
	metadata.Checksum = metadata.checksum()
	if raw, e := json.Marshal(metadata); e == nil && len(raw) < 8<<20 {
		tmp, e := os.CreateTemp(wb.CacheDir, ".html-meta-*")
		if e == nil {
			name := tmp.Name()
			if _, e = tmp.Write(raw); e == nil {
				e = tmp.Close()
				if e == nil {
					_ = replaceFile(name, cachePath)
				}
			}
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}
	return finishHTMLWorkbook(wb, encoding, names, styles), nil
}

func finishHTMLWorkbook(wb *Workbook, encoding string, names []string, styles *htmlStyleMetadata) *Workbook {
	wb.source.encoding = encoding
	wb.source.html = styles
	wb.VisualStyles = styles.Palette
	wb.Styles = make([]StyleInfo, len(styles.Palette))
	wb.SourceInfo = SourceInfo{Format: "xls-html", Encoding: encoding, ReadOnly: true,
		Notice: "此 .xls 实际为 HTML 工作表。按文档顺序读取多段表格和段落；保留常见字体、填色、对齐、边框及合并位置。仅静态识别 Oracle 的 =\"字面量\"，不执行其他公式、脚本或外部资源；不保证完整 HTML/CSS 排版。"}
	used := make(map[string]bool)
	for i, name := range names {
		if name == "" {
			wb.source.htmlDefaultName = true
			name = htmlDefaultSheetName(wb.DisplayName)
		}
		base := name
		for suffix := 2; used[name]; suffix++ {
			name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		used[name] = true
		wb.Sheets = append(wb.Sheets, SheetMeta{Name: name, Path: fmt.Sprintf("external/html/%d", i)})
	}
	return wb
}

func htmlDefaultSheetName(name string) string {
	name = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune("[]:*?/\\", r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " '")
	if name == "" {
		name = "数据"
	}
	return string([]rune(name)[:min(31, len([]rune(name)))])
}

// SetDisplayName preserves an original archive/upload filename as the default
// HTML sheet name, rather than exposing the hashed local cache filename.
func (w *Workbook) SetDisplayName(name string) {
	w.DisplayName = name
	if w.source != nil && w.source.htmlDefaultName && len(w.Sheets) == 1 {
		w.Sheets[0].Name = htmlDefaultSheetName(name)
	}
}

func (w *Workbook) streamHTMLXLSRows(meta SheetMeta, emit func([]string) error, progress func(int64, int64, string)) error {
	return w.streamHTMLXLSStyled(meta, func(fields []string, _ []int) error { return emit(fields) }, progress)
}

func (w *Workbook) streamHTMLXLSStyled(meta SheetMeta, emit func([]string, []int) error, progress func(int64, int64, string)) error {
	target, err := strconv.Atoi(strings.TrimPrefix(meta.Path, "external/html/"))
	if err != nil || target < 0 || target >= len(w.Sheets) {
		return errors.New("HTML 数据表不存在")
	}
	f, err := os.Open(w.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	input := &countingReader{r: f}
	seen := int64(0)
	_, err = readStyledHTMLTables(newDecodedTextReader(input, w.source.encoding), target, nil, func(fields []string, styles []int) error {
		if err := emit(fields, styles); err != nil {
			return err
		}
		if progress != nil && input.n-seen >= 1<<20 {
			seen = input.n
			progress(min(input.n, w.source.size), w.source.size, "正在解析 HTML 财务表格并建立只读索引")
		}
		return nil
	}, w.source.html)
	return err
}

type htmlTableToken struct {
	kind byte // t text, s start tag, e end tag, c comment, d declaration
	name string
	data string
}

func htmlSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' }

// Only inspect attributes actually needed for table coordinates. Boolean
// x:num / x:str attributes and > characters inside quoted values are legal.
func (t htmlTableToken) attr(want string) (string, bool) {
	s := t.data
	for i := 0; i < len(s); {
		for i < len(s) && (htmlSpace(s[i]) || s[i] == '/') {
			i++
		}
		start := i
		for i < len(s) && !htmlSpace(s[i]) && s[i] != '=' && s[i] != '/' {
			i++
		}
		if start == i {
			i++
			continue
		}
		name := s[start:i]
		for i < len(s) && htmlSpace(s[i]) {
			i++
		}
		value := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && htmlSpace(s[i]) {
				i++
			}
			if i < len(s) && (s[i] == '\'' || s[i] == '"') {
				quote := s[i]
				i++
				start = i
				for i < len(s) && s[i] != quote {
					i++
				}
				value = s[start:i]
				if i < len(s) {
					i++
				}
			} else {
				start = i
				for i < len(s) && !htmlSpace(s[i]) {
					i++
				}
				value = s[start:i]
			}
		}
		if strings.EqualFold(name, want) {
			return html.UnescapeString(value), true
		}
	}
	return "", false
}

type htmlTableReader struct{ r *bufio.Reader }

func (h *htmlTableReader) until(end string, limit int) (string, error) {
	var b strings.Builder
	for {
		c, err := h.r.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
		b.WriteByte(c)
		if b.Len() > limit {
			return "", errors.New("HTML 标记超过解析安全上限")
		}
		if c == end[len(end)-1] && (strings.HasSuffix(b.String(), end) || end[0] == '<' && b.Len() >= len(end) && strings.EqualFold(b.String()[b.Len()-len(end):], end)) {
			return b.String()[:b.Len()-len(end)], nil
		}
	}
}

func (h *htmlTableReader) next() (htmlTableToken, error) {
	c, err := h.r.ReadByte()
	if err != nil {
		return htmlTableToken{}, err
	}
	if c != '<' {
		_ = h.r.UnreadByte()
		var text strings.Builder
		for {
			part, err := h.r.ReadSlice('<')
			if len(part) > 0 && part[len(part)-1] == '<' {
				_ = h.r.UnreadByte()
				part = part[:len(part)-1]
			}
			text.Write(part)
			if text.Len() > maxExternalCell {
				return htmlTableToken{}, errors.New("HTML 文本片段超过 8MB 安全上限")
			}
			if err != bufio.ErrBufferFull {
				if err != nil && err != io.EOF {
					return htmlTableToken{}, err
				}
				break
			}
		}
		return htmlTableToken{kind: 't', data: text.String()}, nil
	}
	peek, _ := h.r.Peek(3)
	if len(peek) > 0 && !(peek[0] >= 'A' && peek[0] <= 'Z' || peek[0] >= 'a' && peek[0] <= 'z' || peek[0] == '/' || peek[0] == '!' || peek[0] == '?') {
		return htmlTableToken{kind: 't', data: "<"}, nil
	}
	if string(peek) == "!--" {
		_, _ = h.r.Discard(3)
		comment, err := h.until("-->", maxMetadataPart)
		return htmlTableToken{kind: 'c', data: comment}, err
	}
	var raw strings.Builder
	quote := byte(0)
	for {
		c, err := h.r.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return htmlTableToken{}, err
		}
		if c == '>' && quote == 0 {
			break
		}
		if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		} else if c == quote {
			quote = 0
		}
		raw.WriteByte(c)
		if raw.Len() > 1<<20 {
			return htmlTableToken{}, errors.New("HTML 标签超过 1MB 安全上限")
		}
	}
	s := strings.TrimSpace(raw.String())
	if s == "" || s[0] == '!' || s[0] == '?' {
		return htmlTableToken{kind: 'd', data: s}, nil
	}
	kind := byte('s')
	if s[0] == '/' {
		kind = 'e'
		s = strings.TrimSpace(s[1:])
	}
	i := 0
	for i < len(s) && !htmlSpace(s[i]) && s[i] != '/' {
		i++
	}
	return htmlTableToken{kind: kind, name: strings.ToLower(s[:i]), data: s[i:]}, nil
}

// Raw HTML script/style content can contain unescaped < and >. Skip it as
// bytes rather than interpreting it as tags; nothing is executed or loaded.
func (h *htmlTableReader) skipRaw(name string) error {
	end := "/" + name
	for {
		part, err := h.r.ReadSlice('<')
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		if len(part) == 0 {
			continue
		}
		peek, _ := h.r.Peek(len(end) + 1)
		if len(peek) > len(end) && strings.EqualFold(string(peek[:len(end)]), end) && (peek[len(end)] == '>' || htmlSpace(peek[len(end)])) {
			_, _ = h.r.Discard(len(end))
			_, err = h.until(">", 1<<20)
			return err
		}
	}
}

var htmlWorksheetNames = regexp.MustCompile(`(?is)<x:ExcelWorksheet\b[^>]*>.*?<x:Name\b[^>]*>([^<]*)</x:Name\s*>`)

// target=-1 discovers top-level tables. Indexing subsequently streams only the
// selected table. Nested tables are rejected explicitly to avoid shifting data.
func readHTMLTables(src io.Reader, target int, emit func([]string) error) ([]string, error) {
	return readStyledHTMLTables(src, target, emit, nil, nil)
}
