package workbook

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"

	"excellens/internal/formats"
)

const (
	maxExcelRows    = formats.ExcelRows
	maxSheetXMLSize = 8 << 30
)

func (w *Workbook) BuildIndex(meta SheetMeta, progress func(done, total int64, stage string), cancel <-chan struct{}) (*SheetIndex, error) {
	if idx := w.GetCachedIndex(meta.Path); idx != nil {
		if progress != nil {
			progress(1, 1, "已读取本机索引缓存")
		}
		return idx, nil
	}
	key := sha256.Sum256([]byte(w.CacheDir + "\x00" + meta.Path))
	gate := indexBuildGates[key[0]%64]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-cancel:
		return nil, errors.New("index cancelled")
	}
	if w.Format == "ods" {
		return w.buildODSIndex(meta, progress, cancel)
	}
	if w.source != nil {
		return w.buildExternalIndex(meta, progress, cancel)
	}
	if idx := w.GetCachedIndex(meta.Path); idx != nil {
		if progress != nil {
			progress(1, 1, "已读取本机索引缓存")
		}
		return idx, nil
	}
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return nil, fmt.Errorf("重新打开工作簿失败：%w", err)
	}
	defer zr.Close()
	if fingerprintZip(zr.File) != w.Fingerprint {
		return nil, errors.New("工作簿文件在打开后发生了变化，请重新选择文件")
	}
	var entry *zip.File
	for _, f := range zr.File {
		if path.Clean(f.Name) == meta.Path {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("找不到工作表数据：%s", meta.Path)
	}
	if entry.UncompressedSize64 > uint64(maxSheetXMLSize) {
		return nil, fmt.Errorf("工作表解压后超过 %d GB 安全上限", maxSheetXMLSize>>30)
	}

	xmlPath, idxPath := w.cachePaths(meta.Path)
	// On an index-schema upgrade, reuse the previously decompressed XML if
	// its size and CRC32 still match the current OOXML part. This avoids
	// decompressing a 500+ MB worksheet just to recompute its used extent.
	if cached, err := os.Stat(xmlPath); err == nil && cached.Mode().IsRegular() && uint64(cached.Size()) == entry.UncompressedSize64 {
		f, openErr := os.Open(xmlPath)
		if openErr == nil {
			hash := crc32.NewIEEE()
			if progress != nil {
				progress(0, cached.Size(), "正在更新有效内容边界索引")
			}
			linkStart := int64(-1)
			layout := SourceLayout{DefaultRow: 20, DefaultCol: defaultColumnPixels(8, w.normalDigitWidth()), digitWidth: w.normalDigitWidth(), catalogue: new(worksheetCatalogueSeed)}
			rows, anchors, dim, maxRow, maxCol, scanErr := scanWorksheetWithSharedHint(io.TeeReader(f, hash), cached.Size(), cancel, progress, w.Shared, &linkStart, &layout)
			_ = f.Close()
			if scanErr == nil && hash.Sum32() == entry.CRC32 {
				idx := &SheetIndex{
					SheetPath: meta.Path, XMLPath: xmlPath, IndexPath: idxPath,
					Dimension: dim, MaxRow: maxRow, MaxCol: maxCol,
					XMLSize: cached.Size(), RowIndex: NewRowIndex(rows), SharedAnchors: anchors, HyperlinkStart: linkStart,
				}
				if err := saveIndex(idx); err != nil {
					return nil, err
				}
				w.rememberWorksheetCatalogue(meta, idx, layout.catalogue, entry.CRC32)
				layout.catalogue = nil
				w.rememberSourceLayout(meta, layout)
				w.indexMu.Lock()
				w.indexes[meta.Path] = idx
				w.indexMu.Unlock()
				if progress != nil {
					progress(cached.Size(), cached.Size(), "索引更新完成")
				}
				return idx, nil
			}
			if scanErr != nil && cancel != nil {
				select {
				case <-cancel:
					return nil, scanErr
				default:
				}
			}
		}
	}
	tmp, err := os.CreateTemp(w.CacheDir, ".sheet-*.xml")
	if err != nil {
		return nil, fmt.Errorf("创建工作表缓存失败：%w", err)
	}
	tmpPath := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	rc, err := entry.Open()
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}

	// For large worksheets, decompress/spool and scan concurrently. The pipe
	// bounds buffering while the scanner still processes the exact same byte
	// stream, keeping row offsets and ZIP CRC verification deterministic.
	// Small sheets retain the lower-overhead synchronous path.
	linkStart := int64(-1)
	layout := SourceLayout{DefaultRow: 20, DefaultCol: defaultColumnPixels(8, w.normalDigitWidth()), digitWidth: w.normalDigitWidth(), catalogue: new(worksheetCatalogueSeed)}
	rows, anchors, dimension, maxRow, maxCol, scanErr := scanAndSpoolWorksheetHint(rc, tmp, int64(entry.UncompressedSize64), cancel, progress, w.Shared, &linkStart, &layout)
	if scanErr != nil {
		_ = rc.Close()
		_ = tmp.Close()
		return nil, scanErr
	}
	if err := rc.Close(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("读取工作表时校验压缩数据失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		return nil, err
	}
	if err := replaceFile(tmpPath, xmlPath); err != nil {
		return nil, fmt.Errorf("保存工作表缓存失败：%w", err)
	}
	keepTemp = true

	idx := &SheetIndex{
		SheetPath: meta.Path, XMLPath: xmlPath, IndexPath: idxPath,
		Dimension: dimension, MaxRow: maxRow, MaxCol: maxCol,
		XMLSize: st.Size(), RowIndex: NewRowIndex(rows), SharedAnchors: anchors, HyperlinkStart: linkStart,
	}
	if err := saveIndex(idx); err != nil {
		return nil, fmt.Errorf("保存行索引失败：%w", err)
	}
	w.rememberWorksheetCatalogue(meta, idx, layout.catalogue, entry.CRC32)
	layout.catalogue = nil
	w.rememberSourceLayout(meta, layout)
	w.indexMu.Lock()
	w.indexes[meta.Path] = idx
	w.indexMu.Unlock()
	if progress != nil {
		progress(int64(entry.UncompressedSize64), int64(entry.UncompressedSize64), "索引建立完成")
	}
	return idx, nil
}

var indexBuildGates = func() [64]chan struct{} {
	var gates [64]chan struct{}
	for i := range gates {
		gates[i] = make(chan struct{}, 1)
	}
	return gates
}()

// scanAndSpoolWorksheet never keeps an entire worksheet in RAM. The producer
// writes the exact uncompressed ZIP bytes to disk and a bounded pipe, while
// the consumer indexes those same bytes. Closing the reader on scan error is
// essential: it unblocks the producer and prevents leaked goroutines on a
// malformed, cancelled or over-limit worksheet.
func scanAndSpoolWorksheet(rc io.Reader, dst io.Writer, size int64, cancel <-chan struct{}, progress func(int64, int64, string), shared []string) ([]RowOffset, map[string]SharedFormulaAnchor, string, int, int, error) {
	return scanAndSpoolWorksheetHint(rc, dst, size, cancel, progress, shared, nil)
}

func scanAndSpoolWorksheetHint(rc io.Reader, dst io.Writer, size int64, cancel <-chan struct{}, progress func(int64, int64, string), shared []string, hyperlinkStart *int64, layouts ...*SourceLayout) ([]RowOffset, map[string]SharedFormulaAnchor, string, int, int, error) {
	if size < 32<<20 {
		return scanWorksheetWithSharedHint(io.TeeReader(rc, dst), size, cancel, progress, shared, hyperlinkStart, layouts...)
	}
	pr, pw := io.Pipe()
	produced := make(chan error, 1)
	go func() {
		// Limit transient buffer growth, and let the scanner independently
		// saturate another CPU core while decompression runs.
		_, err := io.CopyBuffer(io.MultiWriter(dst, pw), rc, make([]byte, 1<<20))
		_ = pw.CloseWithError(err)
		produced <- err
	}()
	rows, anchors, dimension, maxRow, maxCol, scanErr := scanWorksheetWithSharedHint(pr, size, cancel, progress, shared, hyperlinkStart, layouts...)
	if scanErr != nil {
		_ = pr.CloseWithError(scanErr)
	} else {
		_ = pr.Close()
	}
	copyErr := <-produced
	if scanErr != nil {
		return nil, nil, "", 0, 0, scanErr
	}
	if copyErr != nil {
		return nil, nil, "", 0, 0, fmt.Errorf("工作表解压与 CRC 校验失败：%w", copyErr)
	}
	return rows, anchors, dimension, maxRow, maxCol, nil
}

func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func saveIndex(idx *SheetIndex) error {
	return writeIndex(idx, idx.RowCount(), nil)
}

// A spill file belongs only to the writer. Publish an index for readers after
// replacing the checksummed file and obtaining its final read-only mapping.
func writeIndex(idx *SheetIndex, count int, spill *os.File) error {
	tmp, err := os.CreateTemp(filepathDir(idx.IndexPath), ".rows-*.idx")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	bw := bufio.NewWriterSize(tmp, 1<<20)
	checksum := crc32.NewIEEE()
	payload := io.MultiWriter(bw, checksum)
	fail := func(e error) error { _ = tmp.Close(); return e }
	if _, err := io.WriteString(payload, "XLIDX009"); err != nil {
		return fail(err)
	}
	if err := writeIndexString(payload, idx.Dimension); err != nil {
		return fail(err)
	}
	if err := writeU32(payload, uint32(idx.MaxRow)); err != nil {
		return fail(err)
	}
	if err := writeU32(payload, uint32(idx.MaxCol)); err != nil {
		return fail(err)
	}
	if err := writeU64(payload, uint64(idx.XMLSize)); err != nil {
		return fail(err)
	}
	if err := writeU64(payload, uint64(count)); err != nil {
		return fail(err)
	}
	if spill != nil {
		size := int64(count) * diskRowOffsetBytes
		if _, err := io.CopyN(payload, io.NewSectionReader(spill, 0, size), size); err != nil {
			return fail(err)
		}
	} else {
		var record [diskRowOffsetBytes]byte
		for _, row := range idx.RowOffsets() {
			if err := writeRowOffset(payload, &record, row); err != nil {
				return fail(err)
			}
		}
	}
	keys := make([]string, 0, len(idx.SharedAnchors))
	for key := range idx.SharedAnchors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := writeU32(payload, uint32(len(keys))); err != nil {
		return fail(err)
	}
	for _, key := range keys {
		anchor := idx.SharedAnchors[key]
		if err := writeIndexString(payload, key); err != nil {
			return fail(err)
		}
		if err := writeIndexString(payload, anchor.Ref); err != nil {
			return fail(err)
		}
		if err := writeIndexString(payload, anchor.Formula); err != nil {
			return fail(err)
		}
	}
	if err := writeU64(payload, uint64(idx.HyperlinkStart)); err != nil {
		return fail(err)
	}
	// The footer is excluded from the payload checksum. Any in-place index
	// corruption that leaves sizes/offsets plausible is detected on reload.
	if err := writeU32(bw, checksum.Sum32()); err != nil {
		return fail(err)
	}
	if err := bw.Flush(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(name, idx.IndexPath); err != nil {
		return err
	}
	if count > MaxResidentRowOffsets {
		rows, err := mapIndexRows(idx.IndexPath, indexRowsStart(idx.Dimension), count)
		if err != nil {
			return err
		}
		idx.RowIndex = rows
	}
	return nil
}

func loadIndex(indexPath, xmlPath, sheetPath string) (*SheetIndex, error) {
	st, err := os.Stat(xmlPath)
	if err != nil || st.Size() == 0 {
		return nil, errors.New("本机工作表缓存不存在或不完整")
	}
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	indexStat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	checksum := crc32.NewIEEE()
	payload := io.TeeReader(br, checksum)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(payload, magic); err != nil || (string(magic) != "XLIDX007" && string(magic) != "XLIDX008" && string(magic) != "XLIDX009") {
		return nil, errors.New("row index version mismatch")
	}
	if string(magic) != "XLIDX009" && !isExternalRows(xmlPath) {
		return nil, errors.New("原生工作表已用区域规则更新，需要重建旧索引")
	}
	newest := string(magic) != "XLIDX007"
	dim, err := readIndexString(payload, 4096)
	if err != nil {
		return nil, err
	}
	maxRow, err := readU32(payload)
	if err != nil {
		return nil, err
	}
	maxCol, err := readU32(payload)
	if err != nil {
		return nil, err
	}
	xmlSize, err := readU64(payload)
	if err != nil || int64(xmlSize) != st.Size() {
		return nil, errors.New("缓存 XML 与行索引不匹配")
	}
	count, err := readU64(payload)
	if err != nil || count > uint64(maxIndexRowsForPath(xmlPath)) {
		return nil, errors.New("行索引记录数量无效")
	}
	// Each persisted row uses 20 bytes. Check the actual file before a large
	// allocation: a damaged count must not reserve memory for a billion rows.
	minimumTail := int64(8) // shared-count field and checksum
	if newest {
		minimumTail += 8 // hyperlink offset
	}
	rowBytes := indexStat.Size() - int64(8+4+len(dim)+4+4+8+8) - minimumTail
	if rowBytes < 0 || count > uint64(rowBytes/20) {
		return nil, errors.New("行索引记录数量与缓存长度不匹配")
	}
	if maxRow > uint32(maxIndexRowsForPath(xmlPath)) || maxCol > formats.ExcelColumns {
		return nil, errors.New("行索引源坐标超出格式容量")
	}
	var rows []RowOffset
	if count <= MaxResidentRowOffsets {
		rows = make([]RowOffset, int(count))
	}
	var previousEnd int64
	previousNumber := uint32(0)
	var record [diskRowOffsetBytes]byte
	for i := 0; i < int(count); i++ {
		if _, err := io.ReadFull(payload, record[:]); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(record[:])
		start := binary.LittleEndian.Uint64(record[4:])
		end := binary.LittleEndian.Uint64(record[12:])
		if n == 0 || n > uint32(maxIndexRowsForPath(xmlPath)) || n <= previousNumber || start >= end || end > xmlSize || int64(start) < previousEnd {
			return nil, errors.New("行索引偏移量无效")
		}
		if rows != nil {
			rows[i] = RowOffset{Number: int(n), Start: int64(start), End: int64(end)}
		}
		previousNumber = n
		previousEnd = int64(end)
	}
	sharedCount, err := readU32(payload)
	if err != nil || sharedCount > 1_000_000 {
		return nil, errors.New("共享公式索引数量无效")
	}
	anchors := make(map[string]SharedFormulaAnchor, sharedCount)
	for i := uint32(0); i < sharedCount; i++ {
		key, err := readIndexString(payload, 128)
		if err != nil {
			return nil, err
		}
		ref, err := readIndexString(payload, 64)
		if err != nil {
			return nil, err
		}
		formula, err := readIndexString(payload, 1<<20)
		if err != nil {
			return nil, err
		}
		anchors[key] = SharedFormulaAnchor{Ref: ref, Formula: formula}
	}
	hyperlinkStart := int64(-2)
	if newest {
		value, err := readU64(payload)
		if err != nil {
			return nil, err
		}
		hyperlinkStart = int64(value)
		if hyperlinkStart < -1 || hyperlinkStart >= int64(xmlSize) {
			return nil, errors.New("invalid hyperlink metadata offset")
		}
	}
	// Read the footer from the underlying buffer, not payload; otherwise
	// the checksum would incorrectly include itself. Reject appended bytes.
	wanted, err := readU32(br)
	if err != nil || wanted != checksum.Sum32() {
		return nil, errors.New("本机行索引完整性校验失败，将重新建立索引")
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return nil, errors.New("本机行索引包含多余数据")
	}
	idx := &SheetIndex{SheetPath: sheetPath, XMLPath: xmlPath, IndexPath: indexPath, Dimension: dim, MaxRow: int(maxRow), MaxCol: int(maxCol), XMLSize: int64(xmlSize), RowIndex: NewRowIndex(rows), SharedAnchors: anchors, HyperlinkStart: hyperlinkStart}
	if count > MaxResidentRowOffsets {
		idx.RowIndex, err = mapIndexRows(indexPath, indexRowsStart(dim), int(count))
		if err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func writeIndexString(w io.Writer, s string) error {
	if len(s) > 1<<20 {
		return errors.New("索引字符串过长")
	}
	if err := writeU32(w, uint32(len(s))); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func readIndexString(r io.Reader, max uint32) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("索引字符串超过限制")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func writeU32(w io.Writer, n uint32) error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], n)
	_, err := w.Write(b[:])
	return err
}

func writeU64(w io.Writer, n uint64) error {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	_, err := w.Write(b[:])
	return err
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

func filepathDir(p string) string {
	return filepath.Dir(p)
}

func maxIndexRowsForPath(path string) int {
	if isExternalRows(path) {
		return maxExternalRows
	}
	return maxExcelRows
}
