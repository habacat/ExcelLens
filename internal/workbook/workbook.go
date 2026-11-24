package workbook

import (
	"excellens/internal/fileidentity"

	"archive/zip"
	"crypto/sha1"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxMetadataPart   = 64 << 20
	maxSharedStrings  = 512 << 20
	maxArchiveEntries = 200000
)

func OpenWorkbook(filePath string) (*Workbook, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".xlsx" && ext != ".xlsm" {
		return OpenDataFile(filePath)
	}
	st, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("无法访问文件：%w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("所选路径不是普通文件")
	}
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("不是有效的 XLSX/XLSM 工作簿：%w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxArchiveEntries {
		return nil, fmt.Errorf("工作簿包含过多压缩文件部件：%d", len(zr.File))
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		clean := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			continue
		}
		entries[clean] = f
	}
	wbXML, err := readZipEntry(entries, "xl/workbook.xml", maxMetadataPart)
	if err != nil {
		return nil, fmt.Errorf("读取工作簿结构失败：%w", err)
	}
	relsXML, err := readZipEntry(entries, "xl/_rels/workbook.xml.rels", maxMetadataPart)
	if err != nil {
		return nil, fmt.Errorf("读取工作簿关系失败：%w", err)
	}
	var book workbookXML
	if err := xml.Unmarshal(wbXML, &book); err != nil {
		return nil, fmt.Errorf("解析工作簿结构失败：%w", err)
	}
	var rels relationshipsXML
	if err := xml.Unmarshal(relsXML, &rels); err != nil {
		return nil, fmt.Errorf("解析工作簿关系失败：%w", err)
	}
	relMap := make(map[string]relationship, len(rels.Relationships))
	for _, rel := range rels.Relationships {
		if strings.EqualFold(rel.TargetMode, "External") {
			continue
		}
		relMap[rel.ID] = rel
	}
	wb := &Workbook{
		Path:        filePath,
		Format:      "xlsx",
		SourceInfo:  SourceInfo{Format: "xlsx", ReadOnly: true},
		DisplayName: filepath.Base(filePath),
		ActiveTab:   book.ActiveTab,
		Date1904:    strings.EqualFold(book.Date1904, "1") || strings.EqualFold(book.Date1904, "true"),
		NumFmts:     make(map[int]string),
		indexes:     make(map[string]*SheetIndex),
	}
	wb.NamedRanges, wb.LocalNamedRanges = readNamedRanges(wbXML, book.Sheets)
	for _, s := range book.Sheets {
		rel, ok := relMap[s.RelID]
		if !ok || !strings.Contains(rel.Type, "/worksheet") {
			continue
		}
		target := strings.ReplaceAll(rel.Target, "\\", "/")
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Clean(path.Join("xl", target))
		}
		if strings.HasPrefix(target, "../") || entries[target] == nil {
			continue
		}
		wb.Sheets = append(wb.Sheets, SheetMeta{Name: s.Name, Path: target, State: s.State})
	}
	if len(wb.Sheets) == 0 {
		return nil, errors.New("工作簿中没有可读取的工作表")
	}
	if wb.ActiveTab < 0 || wb.ActiveTab >= len(wb.Sheets) {
		wb.ActiveTab = 0
	}
	if f := entries["xl/sharedStrings.xml"]; f != nil {
		if f.UncompressedSize64 > maxSharedStrings {
			return nil, fmt.Errorf("共享字符串表过大：%d MB", f.UncompressedSize64>>20)
		}
	}
	wb.ThemeColors = loadTheme(entries["xl/theme/theme1.xml"])
	if f := entries["xl/styles.xml"]; f != nil {
		if f.UncompressedSize64 > maxMetadataPart {
			return nil, errors.New("单元格样式定义文件过大")
		}
		if err := wb.readStyles(f); err != nil {
			return nil, fmt.Errorf("读取单元格样式失败：%w", err)
		}
	}
	// Sheet tab colors live in each sheet's <sheetPr>, not workbook.xml.
	// Stream only the small metadata prefix; never decompress gigabyte-sized
	// sheetData merely to render the tab strip.
	for i := range wb.Sheets {
		if f := entries[wb.Sheets[i].Path]; f != nil {
			wb.Sheets[i].TabColor = wb.readSheetTabColor(f)
		}
	}
	wb.Fingerprint = fingerprintZip(zr.File)
	wb.CacheDir = filepath.Join(CacheRoot(), wb.Fingerprint)
	if err := os.MkdirAll(wb.CacheDir, 0700); err != nil {
		return nil, fmt.Errorf("创建本机缓存目录失败：%w", err)
	}
	if f := entries["xl/sharedStrings.xml"]; f != nil {
		wb.Shared, err = wb.readSharedStringsWithRich(f)
		if err != nil {
			return nil, fmt.Errorf("读取共享字符串失败：%w", err)
		}
	}
	return wb, nil
}

func (w *Workbook) FindSheet(name string) (SheetMeta, bool) {
	for _, s := range w.Sheets {
		if s.Name == name {
			return s, true
		}
	}
	for _, s := range w.Sheets {
		if strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(name)) {
			return s, true
		}
	}
	return SheetMeta{}, false
}

func (w *Workbook) GetCachedIndex(sheetPath string) *SheetIndex {
	if w.source != nil && w.checkExternalUnchanged() != nil {
		return nil
	}
	w.indexMu.RLock()
	idx := w.indexes[sheetPath]
	w.indexMu.RUnlock()
	if idx != nil {
		return idx
	}
	xmlPath, idxPath := w.cachePaths(sheetPath)
	var loaded *SheetIndex
	var err error
	if w.Format == "ods" {
		loaded, err = loadODSIndex(idxPath, xmlPath, sheetPath)
	} else {
		loaded, err = loadIndex(idxPath, xmlPath, sheetPath)
	}
	if err != nil {
		return nil
	}
	loaded.SourceFormat = w.Format
	w.indexMu.Lock()
	if existing := w.indexes[sheetPath]; existing != nil {
		idx = existing
	} else {
		w.indexes[sheetPath] = loaded
		idx = loaded
	}
	w.indexMu.Unlock()
	return idx
}

// CheckSourceUnchanged checks the live file before an explicit operation binds
// to this workbook. Cached snapshot readers intentionally do not call it: they
// may finish exporting an already captured result after the live file changes.
func (w *Workbook) CheckSourceUnchanged() error {
	if w.source != nil {
		if err := w.checkExternalUnchanged(); err != nil {
			return err
		}
		// Explicit source binding must also detect same-size rewrites with a
		// restored timestamp. Reliable Windows revision stamps use the existing
		// bounded fingerprint memo; other cases verify the full content hash.
		fingerprint, _, err := fileidentity.Fingerprint(w.Path, w.source.fingerprintDomain)
		if err != nil {
			return err
		}
		if fingerprint != w.Fingerprint {
			return errors.New("源数据内容在打开后发生了变化，请重新打开文件")
		}
		return nil
	}
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return fmt.Errorf("源工作簿不可访问：%w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxArchiveEntries || fingerprintZip(zr.File) != w.Fingerprint {
		return errors.New("源工作簿在打开后发生了变化，请重新打开以避免使用过期索引")
	}
	return nil
}

func (w *Workbook) cachePaths(sheetPath string) (string, string) {
	sum := sha1.Sum([]byte(sheetPath))
	base := fmt.Sprintf("sheet-%x", sum[:8])
	if w.Format == "ods" {
		base += "-ods2"
	}
	if w.Format == "xls" {
		base += fmt.Sprintf("-biff%d", biffRowReaderVersion)
	}
	if w.source != nil {
		return filepath.Join(w.CacheDir, base+".rows"), filepath.Join(w.CacheDir, base+".idx")
	}
	return filepath.Join(w.CacheDir, base+".xml"), filepath.Join(w.CacheDir, base+".idx")
}

func readZipEntry(entries map[string]*zip.File, name string, max int64) ([]byte, error) {
	f := entries[name]
	if f == nil {
		return nil, fmt.Errorf("压缩包缺少 %q", name)
	}
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("文件部件 %q 超过安全读取上限", name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("文件部件 %q 超过安全读取上限", name)
	}
	return b, nil
}

func readSharedStrings(f *zip.File) ([]string, error) {
	return readSharedStringsWithPresentation(f, nil)
}

func readSharedStringsWithPresentation(f *zip.File, presentation *sharedStringPresentation) ([]string, error) {
	// Typical investment workbooks contain many simple strings in a small part.
	// Fast parse only bounded parts; larger/complex OOXML keeps the proven
	// streaming XML decoder with its constant-size input buffer.
	const fastLimit = 16 << 20
	if f.UncompressedSize64 <= fastLimit {
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(r, fastLimit+1))
		closeErr := r.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) <= fastLimit {
			if values, ok := decodeSharedStringsFastWithPresentation(data, presentation); ok {
				return values, nil
			}
			if presentation != nil {
				presentation.reset()
			}
		}
	}
	return readSharedStringsStrictWithPresentation(f, presentation)
}

func readSharedStringsStrict(f *zip.File) ([]string, error) {
	return readSharedStringsStrictWithPresentation(f, nil)
}

func readSharedStringsStrictWithPresentation(f *zip.File, presentation *sharedStringPresentation) ([]string, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	dec := xml.NewDecoder(r)
	out := make([]string, 0, 4096)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}
		if presentation != nil {
			value, rich, err := presentation.read(dec, start)
			if err != nil {
				return nil, err
			}
			if rich != nil {
				presentation.observe(len(out), value, rich)
			}
			out = append(out, value)
		} else {
			var item sharedStringItem
			if err := dec.DecodeElement(&item, &start); err != nil {
				return nil, err
			}
			out = append(out, item.Text)
		}
	}
	return out, nil
}

// fingerprintZip identifies the *uncompressed workbook content*, rather than
// the external filename, modification time, or ZIP compression settings.
// Every OOXML part contributes its normalized name, uncompressed size, and ZIP
// CRC32. SHA-1 is only used to compact that inexpensive manifest; this does
// not hash the (potentially multi-GB) workbook bytes a second time.
//
// Files with identical OOXML parts, but different on-disk names or compression,
// therefore share one cache. A changed part gets a different fingerprint.
func fingerprintZip(files []*zip.File) string {
	type part struct {
		name string
		size uint64
		crc  uint32
	}
	entries := make([]part, 0, len(files))
	for _, f := range files {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			continue
		}
		entries = append(entries, part{name, f.UncompressedSize64, f.CRC32})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := sha1.New()
	_, _ = io.WriteString(h, "ExcelLens-OOXML-CRC32-v2\x00")
	for _, entry := range entries {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%08x\n", entry.name, entry.size, entry.crc)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (w *Workbook) ClearIndexes() {
	w.clearColumnCache()
	w.indexMu.Lock()
	w.indexes = make(map[string]*SheetIndex)
	w.indexMu.Unlock()
}

// Windows cannot remove an index while its read-only mapping is in use.
// Queries retain their own immutable snapshots until they finish.
func (w *Workbook) HasMappedIndexes() bool {
	if w.richShared != nil {
		return true
	}
	w.indexMu.RLock()
	defer w.indexMu.RUnlock()
	for _, idx := range w.indexes {
		if idx.RowsMapped() {
			return true
		}
	}
	return false
}
