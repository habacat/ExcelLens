package workbook

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"excellens/internal/fileidentity"
)

const maxTextLine = 64 << 20

func openTextWorkbook(filePath string) (*Workbook, error) {
	return openTextWorkbookMode(filePath, false)
}

// OpenTextFragment is an explicit recovery view, never a normal import. Only
// layouts with unambiguous physical record boundaries can be recovered.
func OpenTextFragment(filePath string) (*Workbook, error) {
	if !strings.EqualFold(filepath.Ext(filePath), ".txt") {
		return nil, errors.New("片段查看仅支持 TXT 文件")
	}
	return openTextWorkbookMode(filePath, true)
}

func openTextWorkbookMode(filePath string, fragment bool) (*Workbook, error) {
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
	// Layout changes must not reuse indexes built with an older delimiter or
	// record-boundary interpretation of the same source bytes.
	cacheFormat := format + ":text-layout-v3"
	if fragment {
		cacheFormat += ":complete-physical-records-v1"
	}
	fingerprint, st, err := fileidentity.Fingerprint(filePath, cacheFormat)
	if err != nil {
		return nil, err
	}
	wb, err := newExternalWorkbook(filePath, format, fingerprint, st)
	if err != nil {
		return nil, err
	}
	wb.source.sqliteNames = nil
	wb.source.fingerprintDomain = cacheFormat
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sample := make([]byte, 256<<10)
	n, err := f.Read(sample)
	if err != nil && err != io.EOF {
		return nil, err
	}
	sample = sample[:n]
	encoding := detectTextEncoding(sample)
	wb.source.encoding = encoding
	sampleText, err := decodeTextSample(sample, encoding)
	if err != nil {
		return nil, err
	}
	delimiter, fixed := detectTextLayout(sampleText, format)
	wb.source.delimiter = delimiter
	wb.source.fixed = fixed
	if format == "txt" {
		wb.source.oracle = detectOracleTextLayout(sampleText, delimiter)
	}
	detail := ""
	switch {
	case fixed != nil:
		detail = "Oracle SQL*Plus 定宽列"
	case delimiter == '\t':
		detail = "Tab"
	case delimiter == '|':
		detail = "|"
	case delimiter == ',':
		detail = ","
	case delimiter == ';':
		detail = ";"
	case delimiter == '@':
		detail = "@"
	default:
		detail = "未识别结构，按单列原文读取"
	}
	wb.SourceInfo = SourceInfo{Format: format, Encoding: encoding, Delimiter: detail, ReadOnly: true,
		Notice: "所有字段按文本原值保留（含前导零、长编号与高精度金额），不执行公式。"}
	if format == "txt" {
		wb.SourceInfo.TableHeader = wb.detectTextTableHeader(sampleText)
	}
	if wb.source.oracle != nil && wb.source.oracle.joinedHeader {
		wb.SourceInfo.Notice += "检测到 Oracle 表头与首条数据缺少换行，已按完整字段结构拆分为两行；源文件未改写。"
	}
	if fragment {
		if wb.source.oracle == nil && fixed == nil && delimiter != 0 {
			return nil, errors.New("此 TXT 的记录边界无法安全确认，暂不支持片段查看；请重新获取完整压缩包（支持已识别的 Oracle 凭证、SQL*Plus 定宽或单列原文 TXT）")
		}
		limit, err := completeTextPrefix(f, st.Size(), encoding)
		if err != nil {
			return nil, err
		}
		wb.source.textReadLimit = limit
		wb.SourceInfo.Incomplete = true
		wb.SourceInfo.Notice += fmt.Sprintf("当前仅为不完整 TXT 的现存片段；末尾未结束的物理行（%d 字节）未纳入表格，原始片段字节仍保留。统计和导出仅涵盖当前片段，不能代表全量账目。", st.Size()-limit)
	}
	wb.Sheets = []SheetMeta{{Name: "数据", Path: "external/data"}}
	return wb, nil
}

// At most one bounded physical line is inspected. UTF-16 newlines are matched
// on code-unit boundaries so an odd trailing byte cannot become a character.
func completeTextPrefix(f *os.File, size int64, encoding string) (int64, error) {
	floor := max(int64(0), size-maxTextLine-4)
	utf16Text := encoding == "UTF-16 LE" || encoding == "UTF-16 BE"
	if utf16Text {
		floor -= floor % 2
	}
	end := size
	if utf16Text {
		end -= end % 2
	}
	buf := make([]byte, 64<<10)
	for end > floor {
		start := max(floor, end-int64(len(buf)))
		block := buf[:int(end-start)]
		if _, err := f.ReadAt(block, start); err != nil {
			return 0, err
		}
		if !utf16Text {
			if at := bytes.LastIndexByte(block, '\n'); at >= 0 {
				return start + int64(at) + 1, nil
			}
		} else {
			for at := len(block) - 2; at >= 0; at -= 2 {
				if encoding == "UTF-16 LE" && block[at] == '\n' && block[at+1] == 0 || encoding == "UTF-16 BE" && block[at] == 0 && block[at+1] == '\n' {
					return start + int64(at) + 2, nil
				}
			}
		}
		end = start
	}
	return 0, errors.New("TXT 片段末尾 64MB 内没有完整换行，无法安全确认记录边界；请重新获取完整文件")
}

func (w *Workbook) streamTextRows(emit func([]string) error, progress func(int64, int64, string)) error {
	f, err := os.Open(w.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	var raw io.Reader = f
	if w.source.textReadLimit > 0 {
		raw = io.NewSectionReader(f, 0, w.source.textReadLimit)
	}
	input := &countingReader{r: raw}
	// UTF-8 needs no transcoding. Pass buffered file bytes directly to
	// encoding/csv: the previous line-decoder allocated and copied each
	// physical line twice, including for quoted multi-line fields.
	var source io.Reader
	directUTF8 := w.source.encoding == "UTF-8" || w.source.encoding == "UTF-8 BOM"
	if directUTF8 {
		buffered := bufio.NewReaderSize(input, 1<<20)
		if w.source.encoding == "UTF-8 BOM" {
			if hdr, err := buffered.Peek(3); err == nil && bytes.Equal(hdr, []byte{0xEF, 0xBB, 0xBF}) {
				_, _ = buffered.Discard(3)
			}
		}
		source = buffered
	} else {
		source = newDecodedTextReader(input, w.source.encoding)
	}
	seen := int64(0)
	track := func(fields []string) error {
		if directUTF8 {
			for col, field := range fields {
				if !utf8.ValidString(field) {
					return fmt.Errorf("第 %d 列含非法 UTF-8 字节；请检查原文件编码", col+1)
				}
			}
		}
		if err := emit(fields); err != nil {
			return err
		}
		if progress != nil && input.n-seen >= 1<<20 {
			seen = input.n
			progress(min(input.n, w.source.size), w.source.size, "正在解析文本并建立只读索引")
		}
		return nil
	}
	return w.streamTextRecords(source, track)
}

// The same record parser is used for the bounded header sample and the full
// import, including repaired Oracle headers and quoted multiline records.
func (w *Workbook) streamTextRecords(source io.Reader, track func([]string) error) error {
	if w.source.fixed != nil {
		return streamOracleFixed(source, w.source.fixed, track)
	}
	if w.source.oracle != nil {
		return streamOracleDelimited(source, w.source.oracle, track)
	}
	if w.source.delimiter == 0 {
		scanner := bufio.NewReaderSize(source, 256<<10)
		for {
			line, err := scanner.ReadString('\n')
			if len(line) > 0 {
				if e := track([]string{strings.TrimRight(line, "\r\n")}); e != nil {
					return e
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	reader := csv.NewReader(source)
	reader.Comma = w.source.delimiter
	reader.FieldsPerRecord = -1
	// Oracle TXT occasionally contains unescaped quotes, whereas .csv/.tsv
	// must obey RFC4180 to preserve exact quoted values.
	reader.LazyQuotes = w.Format == "txt"
	reader.ReuseRecord = true
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("CSV/TXT 第 %d 条记录解析失败：%w", reader.InputOffset(), err)
		}
		if w.Format == "txt" && w.source.delimiter == '|' {
			copyFields := make([]string, len(fields))
			for i, v := range fields {
				copyFields[i] = strings.TrimSpace(v)
			}
			fields = copyFields
		}
		if err := track(fields); err != nil {
			return err
		}
	}
}

func streamOracleFixed(src io.Reader, rule *fixedColumns, emit func([]string) error) error {
	if err := emit(rule.header); err != nil {
		return err
	}
	br := bufio.NewReaderSize(src, 256<<10)
	inData := false
	for {
		line, err := br.ReadString('\n')
		if len(line) > maxTextLine {
			return errors.New("定宽 TXT 单行超过 64MB")
		}
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			trimmed := strings.TrimSpace(strings.ReplaceAll(line, "\f", ""))
			switch {
			case trimmed == "":
			case parseOracleRule(line) != nil:
				inData = true
			case sameFixedHeader(splitFixedLine(line, rule), rule.header):
				// A repeated page header.
			case strings.HasPrefix(trimmed, "SQL>"):
			case strings.HasSuffix(strings.ToLower(trimmed), "rows selected."):
			case strings.Contains(trimmed, "已选择") && strings.Contains(trimmed, "行"):
			case inData:
				fields := splitFixedLine(line, rule)
				if err := emit(fields); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func sameFixedHeader(row, header []string) bool {
	if len(row) != len(header) {
		return false
	}
	for i := range row {
		if strings.TrimSpace(row[i]) != strings.TrimSpace(header[i]) {
			return false
		}
	}
	return true
}
