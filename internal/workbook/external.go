package workbook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
)

// SupportedExtensions is shared by local file launches, the upload API and
// recent-file reopening. All source formats are opened read-only.
func SupportedExtensions(ext string) bool {
	return formats.Supported(ext)
}

func SupportedDataExtension(ext string) bool {
	return formats.DataExtension(ext)
}

// SourceInfo is presentation metadata. No settings inferred from CSV/TXT may
// change the original content; every imported value remains literal text.
type SourceInfo struct {
	TableHeader   *TextTableHeader `json:"tableHeader,omitempty"`
	ArchiveName   string           `json:"archiveName,omitempty"`
	ArchiveMember string           `json:"archiveMember,omitempty"`
	ArchiveNotice string           `json:"archiveNotice,omitempty"`
	Incomplete    bool             `json:"incomplete,omitempty"`
	Format        string           `json:"format"`
	Encoding      string           `json:"encoding,omitempty"`
	Delimiter     string           `json:"delimiter,omitempty"`
	Notice        string           `json:"notice,omitempty"`
	ReadOnly      bool             `json:"readOnly"`
}

type externalSource struct {
	ods               *odsBook
	html              *htmlStyleMetadata
	htmlDefaultName   bool
	format            string
	fingerprintDomain string
	encoding          string
	delimiter         rune
	fixed             *fixedColumns
	oracle            *oracleTextLayout
	sqliteNames       map[string]string // synthetic sheet path -> original table/view name
	xlsSheets         []xlsSheet
	xlsCatalogue      *Catalogue
	size              int64
	textReadLimit     int64
	modified          time.Time
	walExists         bool
	walSize           int64
	walModified       time.Time
}

func OpenDataFile(path string) (*Workbook, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ods":
		return openODS(path)
	case ".csv", ".tsv", ".txt":
		return openTextWorkbook(path)
	case ".db", ".sqlite", ".sqlite3":
		return openSQLiteWorkbook(path)
	case ".xls":
		return openXLSWorkbook(path)
	default:
		return nil, fmt.Errorf("不支持的文件类型 %q", filepath.Ext(path))
	}
}

func newExternalWorkbook(filePath, format, fingerprint string, st os.FileInfo) (*Workbook, error) {
	wb := &Workbook{
		Path: filePath, DisplayName: filepath.Base(filePath), Fingerprint: fingerprint,
		Format: format, NumFmts: make(map[int]string), indexes: make(map[string]*SheetIndex),
		source: &externalSource{format: format, fingerprintDomain: format, size: st.Size(), modified: st.ModTime()},
	}
	if format == "sqlite" {
		if wal, err := os.Stat(filePath + "-wal"); err == nil {
			if !wal.Mode().IsRegular() {
				return nil, errors.New("SQLite WAL 路径不是普通文件")
			}
			wb.source.walExists = true
			wb.source.walSize = wal.Size()
			wb.source.walModified = wal.ModTime()
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("无法获取 SQLite WAL 状态：%w", err)
		}
	}
	wb.CacheDir = filepath.Join(CacheRoot(), wb.Fingerprint)
	if err := os.MkdirAll(wb.CacheDir, 0700); err != nil {
		return nil, fmt.Errorf("创建本地只读索引目录失败：%w", err)
	}
	return wb, nil
}

func (w *Workbook) checkExternalUnchanged() error {
	st, err := os.Stat(w.Path)
	if err != nil {
		return fmt.Errorf("源文件不可访问：%w", err)
	}
	if st.Size() != w.source.size || !st.ModTime().Equal(w.source.modified) {
		return errors.New("源数据文件在打开后发生了变化，请重新打开以避免使用过期索引")
	}
	if w.Format == "sqlite" {
		wal, err := os.Stat(w.Path + "-wal")
		if errors.Is(err, os.ErrNotExist) {
			if w.source.walExists {
				return errors.New("SQLite WAL 日志已改变，请重新打开数据库")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !w.source.walExists || wal.Size() != w.source.walSize || !wal.ModTime().Equal(w.source.walModified) {
			return errors.New("SQLite WAL 日志已改变，请重新打开数据库")
		}
	}
	return nil
}

func (w *Workbook) buildExternalIndex(meta SheetMeta, progress func(int64, int64, string), cancel <-chan struct{}) (*SheetIndex, error) {
	if idx := w.GetCachedIndex(meta.Path); idx != nil {
		if progress != nil {
			progress(1, 1, "已读取只读索引缓存")
		}
		return idx, nil
	}
	if err := w.checkExternalUnchanged(); err != nil {
		return nil, err
	}
	if w.source == nil {
		return nil, errors.New("没有外部数据源")
	}
	xmlPath, idxPath := w.cachePaths(meta.Path)
	tmp, err := os.CreateTemp(w.CacheDir, ".import-*.rows")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	writer, err := newExternalRowWriter(tmp)
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	defer writer.offsets.close()
	emit := func(fields []string) error {
		if cancel != nil {
			select {
			case <-cancel:
				return errors.New("读取已取消")
			default:
			}
		}
		if err := writer.row(fields); err != nil {
			return err
		}
		if progress != nil && writer.rows%1024 == 0 && w.Format == "sqlite" {
			progress(int64(writer.rows), 0, fmt.Sprintf("已读取 %d 行 SQLite 数据", writer.rows))
		}
		return nil
	}
	var catalogue *biffTableCapture
	switch w.Format {
	case "csv", "tsv", "txt":
		err = w.streamTextRows(emit, progress)
	case "xls":
		catalogue = newBIFFTableCapture(meta)
		err = w.streamXLSRows(meta, writer, cancel, progress, catalogue)
	case "xls-html":
		err = w.streamHTMLXLSStyled(meta, func(fields []string, styles []int) error {
			if cancel != nil {
				select {
				case <-cancel:
					return errors.New("读取已取消")
				default:
				}
			}
			cells := make([]externalCell, 0, len(fields))
			for i, raw := range fields {
				style := 0
				if i < len(styles) {
					style = styles[i]
				}
				if raw != "" || style != 0 {
					cells = append(cells, htmlExternalCell(raw, style, i+1))
				}
			}
			return writer.cells(cells, len(fields))
		}, progress)
	case "sqlite":
		table, ok := w.source.sqliteNames[meta.Path]
		if !ok {
			err = errors.New("数据表不存在")
		} else {
			err = sqliteStreamRows(w.Path, table, emit)
		}
	default:
		err = errors.New("未知数据源")
	}
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err = writer.finish(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err = tmp.Close(); err != nil {
		return nil, err
	}
	if err = w.checkExternalUnchanged(); err != nil {
		return nil, err
	}
	if catalogue != nil {
		if err = w.CheckSourceUnchanged(); err != nil {
			return nil, err
		}
	}
	if err = replaceFile(tmpName, xmlPath); err != nil {
		return nil, err
	}
	idx := &SheetIndex{
		SourceFormat: w.Format,
		SheetPath:    meta.Path, XMLPath: xmlPath, IndexPath: idxPath,
		Dimension: writer.dimension(), MaxRow: writer.rows, MaxCol: writer.cols,
		XMLSize: writer.count, RowIndex: NewRowIndex(writer.offsets.rows), HyperlinkStart: -1,
		SharedAnchors: make(map[string]SharedFormulaAnchor),
	}
	if err = writeIndex(idx, writer.offsets.count, writer.offsets.file); err != nil {
		return nil, err
	}
	w.indexMu.Lock()
	w.indexes[meta.Path] = idx
	w.indexMu.Unlock()
	if catalogue != nil {
		w.rememberBIFFCatalogue(meta, catalogue.finish())
	}
	if progress != nil {
		progress(w.source.size, w.source.size, "只读索引建立完成")
	}
	return idx, nil
}

// Previous XML materialization was replaced by native .rows storage in
// external_rows.go. XLSX continues to use the original OOXML path.
func escapeExternalXML(s string) string {
	// Retained solely for legacy compatibility tests and diagnostics.
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\r':
			b.WriteString("&#13;")
		default:
			if r == utf8.RuneError || r == 0 || (r < 0x20 && r != '\n' && r != '\t') {
				b.WriteRune('\uFFFD')
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
