package workbook

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"excellens/internal/formats"
)

const odsOffice = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
const odsTable = "urn:oasis:names:tc:opendocument:xmlns:table:1.0"
const odsText = "urn:oasis:names:tc:opendocument:xmlns:text:1.0"
const odsMIME = "application/vnd.oasis.opendocument.spreadsheet"

type odsTableSpan struct {
	start, end int64
	prefix     string
}
type odsBook struct {
	content string
	tables  map[string]odsTableSpan
	mu      sync.Mutex
	last    []byte
	columns []odsColumn
}
type odsColumn struct {
	First, Count                      int
	Kind, Type, Raw, Display, Formula string
	Currency                          string `json:",omitempty"`
	Missing                           bool   `json:",omitempty"`
}

func odsAttr(el xml.StartElement, ns, name string) (string, bool) {
	for _, a := range el.Attr {
		if a.Name.Space == ns && a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}
func odsNumber(el xml.StartElement, ns, key string, max int) (int, error) {
	raw, ok := odsAttr(el, ns, key)
	if !ok {
		return 1, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1 || n > max {
		return 0, fmt.Errorf("ODS 属性 %s 超出容量", key)
	}
	return n, nil
}

func openODS(path string) (*Workbook, error) {
	digest, st, err := fingerprintFile(path, "ods")
	if err != nil {
		return nil, err
	}
	w, err := newExternalWorkbook(path, "ods", digest, st)
	if err != nil {
		return nil, err
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	if len(z.File) > maxArchiveEntries {
		return nil, errors.New("ODS 部件数量超限")
	}
	parts := map[string]*zip.File{}
	for _, f := range z.File {
		if _, ok := parts[f.Name]; ok {
			return nil, errors.New("ODS 部件路径重复")
		}
		parts[f.Name] = f
	}
	mime, err := readZipEntry(parts, "mimetype", 256)
	if err != nil || string(mime) != odsMIME {
		return nil, errors.New("不是 OpenDocument 电子表格")
	}
	content := parts["content.xml"]
	if content == nil || content.UncompressedSize64 == 0 || content.UncompressedSize64 > formats.ODSContentBytes {
		return nil, errors.New("ODS 正文缺失或超过容量")
	}
	cache := filepath.Join(w.CacheDir, "ods-content-v1.xml")
	read := func() error {
		file, e := os.Open(cache)
		if e != nil {
			return e
		}
		defer file.Close()
		stat, e := file.Stat()
		if e != nil || uint64(stat.Size()) != content.UncompressedSize64 {
			return errors.New("ODS 正文缓存长度无效")
		}
		hash := crc32.NewIEEE()
		book := &odsBook{content: cache, tables: map[string]odsTableSpan{}}
		sheets, e := book.directory(xml.NewDecoder(io.TeeReader(file, hash)))
		if e != nil {
			return e
		}
		if hash.Sum32() != content.CRC32 {
			return errors.New("ODS 正文缓存校验失败")
		}
		w.source.ods = book
		w.Sheets = sheets
		return nil
	}
	if err = read(); err != nil {
		tmp, e := os.CreateTemp(w.CacheDir, ".ods-content-*")
		if e != nil {
			return nil, e
		}
		defer os.Remove(tmp.Name())
		rc, e := content.Open()
		if e != nil {
			tmp.Close()
			return nil, e
		}
		n, e := io.Copy(tmp, io.LimitReader(rc, formats.ODSContentBytes+1))
		rc.Close()
		e = errors.Join(e, tmp.Close())
		if e != nil || uint64(n) != content.UncompressedSize64 {
			return nil, errors.New("ODS 正文无法完整解压，可能已加密或损坏")
		}
		// Another tab can publish the same immutable content first. Its open
		// reader can prevent replacement on Windows. Validate the winning file.
		publishErr := os.Rename(tmp.Name(), cache)
		if e = read(); e != nil {
			return nil, errors.Join(publishErr, e)
		}
	}
	if err = w.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	w.SourceInfo = SourceInfo{Format: "ods", ReadOnly: true, Notice: "ODS 按源坐标、类型及保存结果读取。公式保留 OpenFormula 原文，样式与图形采用基础显示。"}
	return w, nil
}

func (b *odsBook) directory(d *xml.Decoder) ([]SheetMeta, error) {
	var result []SheetMeta
	var stack []xml.Name
	var scopes []map[string]string
	seen := map[string]bool{}
	for {
		start := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch el := token.(type) {
		case xml.StartElement:
			if len(stack) > 64 {
				return nil, errors.New("ODS 层级超限")
			}
			if len(stack) == 0 && el.Name != (xml.Name{Space: odsOffice, Local: "document-content"}) {
				return nil, errors.New("ODS 正文根元素无效")
			}
			if len(stack) == 3 && stack[1] == (xml.Name{Space: odsOffice, Local: "body"}) && stack[2] == (xml.Name{Space: odsOffice, Local: "spreadsheet"}) && el.Name == (xml.Name{Space: odsTable, Local: "table"}) {
				name, _ := odsAttr(el, odsTable, "name")
				if name == "" || seen[name] || len(result) >= formats.ODSSheets {
					return nil, errors.New("ODS 表名缺失、重复或工作表数超限")
				}
				seen[name] = true
				var prefix bytes.Buffer
				prefix.WriteString("<wrapper")
				ns := map[string]string{}
				for _, scope := range scopes {
					for k, v := range scope {
						ns[k] = v
					}
				}
				for key, value := range ns {
					prefix.WriteString(" " + key + "=\"")
					_ = xml.EscapeText(&prefix, []byte(value))
					prefix.WriteByte('"')
				}
				prefix.WriteByte('>')
				if err = d.Skip(); err != nil {
					return nil, err
				}
				id := "ods/" + strconv.Itoa(len(result)+1)
				b.tables[id] = odsTableSpan{start, d.InputOffset(), prefix.String()}
				state := "visible"
				if v, _ := odsAttr(el, odsTable, "display"); v == "false" {
					state = "hidden"
				}
				result = append(result, SheetMeta{Name: name, Path: id, State: state})
				continue
			}
			scope := map[string]string{}
			for _, a := range el.Attr {
				if a.Name.Space == "xmlns" {
					scope["xmlns:"+a.Name.Local] = a.Value
				} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
					scope["xmlns"] = a.Value
				}
			}
			stack = append(stack, el.Name)
			scopes = append(scopes, scope)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			scopes = scopes[:len(scopes)-1]
		}
	}
	if len(result) == 0 {
		return nil, errors.New("ODS 没有工作表")
	}
	return result, nil
}

// Preserve declared native values independently of formatted paragraph text.
func odsReadCell(d *xml.Decoder, el xml.StartElement, first, repeat int) (odsColumn, error) {
	cell := odsColumn{First: first, Count: repeat}
	cell.Kind, _ = odsAttr(el, odsOffice, "value-type")
	if kind, ok := odsAttr(el, "urn:org:documentfoundation:names:experimental:calc:xmlns:calcext:1.0", "value-type"); ok && kind == "error" {
		cell.Kind = "error"
	}
	cell.Formula, _ = odsAttr(el, odsTable, "formula")
	var attr string
	switch cell.Kind {
	case "float", "currency", "percentage":
		cell.Type = "n"
		attr = "value"
	case "date":
		cell.Type = "d"
		attr = "date-value"
	case "time":
		cell.Type = "inlineStr"
		attr = "time-value"
	case "error":
		cell.Type = "e"
		attr = "string-value"
	case "boolean":
		cell.Type = "b"
		attr = "boolean-value"
	case "string", "":
		cell.Type = "inlineStr"
		attr = "string-value"
	case "void":
		cell.Type = "inlineStr"
	default:
		return cell, fmt.Errorf("ODS 单元格类型 %q 尚无法可靠读取", cell.Kind)
	}
	raw, has := odsAttr(el, odsOffice, attr)
	cell.Raw = raw
	if cell.Type == "b" && has && raw != "true" && raw != "false" && raw != "1" && raw != "0" {
		return cell, errors.New("ODS 布尔值无效")
	}
	if cell.Kind == "currency" {
		cell.Currency, _ = odsAttr(el, odsOffice, "currency")
	}
	var text strings.Builder
	paragraphs := 0
	inParagraph := false
	depth := 0
	for {
		token, err := d.Token()
		if err != nil {
			return cell, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name == (xml.Name{Space: odsText, Local: "p"}) && depth == 0 {
				if paragraphs > 0 {
					text.WriteByte('\n')
				}
				paragraphs++
				inParagraph = true
			} else if !inParagraph {
				if err := d.Skip(); err != nil {
					return cell, err
				}
				continue
			} else if t.Name.Space == odsText {
				switch t.Name.Local {
				case "line-break":
					text.WriteByte('\n')
				case "tab":
					text.WriteByte('\t')
				case "s":
					n, e := odsNumber(t, odsText, "c", maxExternalCell)
					if e != nil || n > maxExternalCell-text.Len() {
						return cell, errors.New("ODS 单元格文字超限")
					}
					text.WriteString(strings.Repeat(" ", n))
				}
			}
			depth++
			if depth > 64 {
				return cell, errors.New("ODS 单元格层级超限")
			}
		case xml.CharData:
			if inParagraph {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == 0 {
				cell.Display = text.String()
				if !has && (cell.Type == "inlineStr" || cell.Type == "e") && cell.Kind != "time" {
					cell.Raw = cell.Display
					has = paragraphs > 0 || cell.Kind == "string"
				}
				if paragraphs == 0 {
					cell.Display = cell.Raw
				}
				if cell.Formula != "" {
					cell.Missing = !has
				} else if !has && cell.Kind != "" && cell.Kind != "void" {
					return cell, errors.New("ODS 类型字段缺少源值")
				}
				if len(cell.Raw) > maxExternalCell || len(cell.Formula) > maxExternalCell {
					return cell, errors.New("ODS 单元格超过容量")
				}
				return cell, nil
			}
			depth--
			if depth == 0 {
				inParagraph = false
			}
		}
		if text.Len() > maxExternalCell {
			return cell, errors.New("ODS 单元格文字超限")
		}
	}
}

func (w *Workbook) decodeODS(data []byte, number, first, last int, selected map[int]struct{}, formatted, formulas bool) (RowData, error) {
	book := w.source.ods
	book.mu.Lock()
	cols := book.columns
	if !bytes.Equal(book.last, data) {
		var err error
		cols, err = decodeODSColumns(data)
		if err != nil {
			book.mu.Unlock()
			return RowData{}, err
		}
		if len(data) <= 1<<20 {
			book.last = append(book.last[:0], data...)
			book.columns = cols
		}
	}
	book.mu.Unlock()
	row := RowData{Number: number}
	suffix := strconv.Itoa(number)
	start := sort.Search(len(cols), func(i int) bool { return cols[i].First+cols[i].Count > first })
	for _, run := range cols[start:] {
		if run.First > last {
			break
		}
		for c := max(first, run.First); c <= min(last, run.First+run.Count-1); c++ {
			if selected != nil {
				if _, ok := selected[c]; !ok {
					continue
				}
			}
			display := run.Raw
			if formatted {
				display = run.Display
			}
			cell := CellData{Col: c, Ref: nativeColumnNames[c] + suffix, Raw: run.Raw, Display: display, Type: run.Type, SourceType: run.Kind, Currency: run.Currency, HasFormula: run.Formula != "", CacheMissing: run.Missing}
			if formulas {
				cell.Formula = run.Formula
			}
			row.Cells = append(row.Cells, cell)
		}
	}
	return row, nil
}

func (w *Workbook) odsTableDecoder(path string) (*os.File, *xml.Decoder, error) {
	b := w.source.ods
	span, ok := b.tables[path]
	if !ok {
		return nil, nil, errors.New("ODS 工作表不存在")
	}
	f, err := os.Open(b.content)
	if errors.Is(err, os.ErrNotExist) {
		// Clearing the active cache removes content.xml as well as the row
		// indexes. Rebuild its directory from the bound source before reusing
		// any offsets. Other readers may still hold the original directory.
		if err := w.CheckSourceUnchanged(); err != nil {
			return nil, nil, err
		}
		fresh, err := openODS(w.Path)
		if err != nil {
			return nil, nil, err
		}
		if fresh.Fingerprint != w.Fingerprint {
			return nil, nil, errors.New("ODS 来源已变更，请重新打开")
		}
		f, err = os.Open(b.content)
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	r := io.MultiReader(strings.NewReader(span.prefix), io.NewSectionReader(f, span.start, span.end-span.start), strings.NewReader("</wrapper>"))
	return f, xml.NewDecoder(r), nil
}

func odsRow(d *xml.Decoder, el xml.StartElement, number, repeat int, layout *SourceLayout) ([]odsColumn, int, error) {
	var cells []odsColumn
	rowBytes := 0
	col, used := 1, 0
	for {
		token, err := d.Token()
		if err != nil {
			return nil, 0, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Space != odsTable || (t.Name.Local != "table-cell" && t.Name.Local != "covered-table-cell") {
				if err = d.Skip(); err != nil {
					return nil, 0, err
				}
				continue
			}
			count, err := odsNumber(t, odsTable, "number-columns-repeated", formats.ExcelColumns)
			if err != nil || col > formats.ExcelColumns-count+1 {
				return nil, 0, errors.New("ODS 行的重复列超出支持范围")
			}
			if t.Name.Local == "covered-table-cell" {
				if err = d.Skip(); err != nil {
					return nil, 0, err
				}
				col += count
				continue
			}
			v, err := odsReadCell(d, t, col, count)
			if err != nil {
				return nil, 0, err
			}
			wide, e1 := odsNumber(t, odsTable, "number-columns-spanned", formats.ExcelColumns)
			high, e2 := odsNumber(t, odsTable, "number-rows-spanned", formats.ExternalRows)
			if e1 != nil || e2 != nil || col+count+wide-2 > formats.ExcelColumns || number+repeat+high-2 > formats.ExternalRows {
				return nil, 0, errors.New("ODS 合并范围无效")
			}
			if wide > 1 || high > 1 {
				if int64(repeat)*int64(count) > int64(formats.ODSMerges-len(layout.Merges)) || wide > 1 && count > 1 || high > 1 && repeat > 1 {
					return nil, 0, errors.New("ODS 重复合并范围交叠或超限")
				}
				for r := range repeat {
					for c := range count {
						layout.Merges = append(layout.Merges, CellMerge{Row: number + r, Col: col + c, EndRow: number + r + high - 1, EndCol: col + c + wide - 1})
					}
				}
				used = max(used, col+count+wide-2)
			}
			if (v.Kind != "" && v.Kind != "void") || v.Formula != "" || v.Raw != "" {
				rowBytes += len(v.Raw) + len(v.Display) + len(v.Formula) + 128
				if rowBytes > maxExternalRowBytes {
					return nil, 0, errors.New("ODS 单行模板超过容量")
				}
				cells = append(cells, v)
				used = max(used, col+count-1)
			}
			col += count
		case xml.EndElement:
			if t.Name == el.Name {
				return cells, used, nil
			}
		}
	}
}

func odsSheetWrapper(name string) bool {
	return slices.Contains([]string{"table-header-rows", "table-rows", "table-row-group", "table-header-columns", "table-columns", "table-column-group"}, name)
}
