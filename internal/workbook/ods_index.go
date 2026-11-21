package workbook

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strconv"

	"excellens/internal/formats"
)

// One span describes any number of identical physical rows; logical positions
// keep the existing sparse index contract used by browsing, sort and search.
type odsRowSpan struct {
	First, Position, Count int
	Start, End             int64
}

func decodeODSColumns(raw []byte) ([]odsColumn, error) {
	var cols []odsColumn
	if err := json.Unmarshal(raw, &cols); err != nil {
		return nil, err
	}
	last := 0
	for _, col := range cols {
		if col.First <= last || col.First > formats.ExcelColumns || col.Count < 1 || col.Count > formats.ExcelColumns-col.First+1 {
			return nil, errors.New("ODS 行缓存重复范围无效")
		}
		last = col.First + col.Count - 1
	}
	return cols, nil
}

func (w *Workbook) buildODSIndex(meta SheetMeta, progress func(int64, int64, string), cancel <-chan struct{}) (*SheetIndex, error) {
	if cached := w.GetCachedIndex(meta.Path); cached != nil {
		return cached, nil
	}
	source, d, err := w.odsTableDecoder(meta.Path)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	rowsPath, idxPath := w.cachePaths(meta.Path)
	f, err := os.CreateTemp(w.CacheDir, ".ods-rows-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	output := bufio.NewWriterSize(f, 256<<10)
	checksum := crc32.NewIEEE()
	dst := io.MultiWriter(output, checksum)
	const marker = "ODSROW01"
	if _, err = io.WriteString(dst, marker); err != nil {
		return nil, err
	}
	idx := &SheetIndex{SheetPath: meta.Path, SourceFormat: "ods", IndexPath: idxPath, XMLPath: rowsPath, XMLSize: int64(len(marker)), HyperlinkStart: -1, odsLayout: &SourceLayout{}}
	idx.odsRuns = []odsRowSpan{}
	number, position, colPos := 1, 0, 1
	var hiddenScope []bool
	for {
		select {
		case <-cancel:
			return nil, errors.New("ODS 索引已取消")
		default:
		}
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if len(hiddenScope) < 2 || el.Name.Space == odsTable && odsSheetWrapper(el.Name.Local) {
				if len(hiddenScope) >= 64 {
					return nil, errors.New("ODS 行列分组层级超限")
				}
				hidden := len(hiddenScope) > 0 && hiddenScope[len(hiddenScope)-1]
				if el.Name.Local == "table-row-group" || el.Name.Local == "table-column-group" {
					display, _ := odsAttr(el, odsTable, "display")
					hidden = hidden || display == "false" || display == "0"
				}
				hiddenScope = append(hiddenScope, hidden)
				continue
			}
			if el.Name.Space != odsTable {
				if err = d.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			visibility, _ := odsAttr(el, odsTable, "visibility")
			hidden := hiddenScope[len(hiddenScope)-1] || visibility == "collapse" || visibility == "filter"
			switch el.Name.Local {
			case "table-column":
				n, e := odsNumber(el, odsTable, "number-columns-repeated", formats.ExcelColumns)
				if e != nil || colPos > formats.ExcelColumns-n+1 {
					return nil, errors.New("ODS 重复列超限")
				}
				if hidden {
					idx.odsLayout.ColStates = append(idx.odsLayout.ColStates, SourceAxisRange{Min: colPos, Max: colPos + n - 1, Hidden: true, HiddenSet: true})
				}
				colPos += n
				if err = d.Skip(); err != nil {
					return nil, err
				}
			case "table-row":
				n, e := odsNumber(el, odsTable, "number-rows-repeated", formats.ExternalRows)
				if e != nil || number > formats.ExternalRows-n+1 {
					return nil, errors.New("ODS 重复行超限")
				}
				if hidden {
					idx.odsLayout.appendRowAxisState(SourceAxisRange{Min: number, Max: number + n - 1, Hidden: true, HiddenSet: true})
					if idx.odsLayout.StateNotice != "" {
						return nil, errors.New(idx.odsLayout.StateNotice)
					}
				}
				cols, used, e := odsRow(d, el, number, n, idx.odsLayout)
				if e != nil {
					return nil, e
				}
				if len(cols) > 0 || used > 0 {
					if len(idx.odsRuns) >= formats.ODSRowTemplates {
						return nil, errors.New("ODS 物理行模板超限")
					}
					raw, e := json.Marshal(cols)
					if e != nil {
						return nil, e
					}
					if len(raw) > maxExternalRowBytes {
						return nil, errors.New("ODS 单行缓存超限")
					}
					if _, e = dst.Write(raw); e != nil {
						return nil, e
					}
					end := idx.XMLSize + int64(len(raw))
					if end > formats.MaxQueryTempBytes {
						return nil, errors.New("ODS 行缓存超过可用容量")
					}
					idx.odsRuns = append(idx.odsRuns, odsRowSpan{number, position, n, idx.XMLSize, end})
					idx.XMLSize = end
					position += n
					idx.MaxRow = number + n - 1
					idx.MaxCol = max(idx.MaxCol, used)
				}
				number += n
				if progress != nil && len(idx.odsRuns)&1023 == 0 {
					progress(d.InputOffset(), 0, "正在索引 ODS 源行")
				}
			default:
				if err = d.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			hiddenScope = hiddenScope[:len(hiddenScope)-1]
		}
	}
	for _, m := range idx.odsLayout.Merges {
		idx.MaxRow = max(idx.MaxRow, m.EndRow)
		idx.MaxCol = max(idx.MaxCol, m.EndCol)
	}
	if !idx.odsLayout.valid() {
		return nil, errors.New("ODS 源布局超过容量或无效")
	}
	idx.odsCount = position
	idx.Dimension = "A1"
	if idx.MaxCol > 0 {
		idx.Dimension = "A1:" + ColumnName(idx.MaxCol) + strconv.Itoa(idx.MaxRow)
	}
	if err = output.Flush(); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = w.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	if err = replaceFile(f.Name(), rowsPath); err != nil {
		return nil, err
	}
	if err = writeODSIndex(idx, checksum.Sum32()); err != nil {
		return nil, err
	}
	w.indexMu.Lock()
	w.indexes[meta.Path] = idx
	w.indexMu.Unlock()
	return idx, nil
}

func writeODSIndex(idx *SheetIndex, bodyCRC uint32) error {
	raw, err := json.Marshal(idx.odsLayout)
	if err != nil {
		return err
	}
	if len(raw) > formats.ODSLayoutBytes {
		return errors.New("ODS 布局缓存超过容量")
	}
	tmp, err := os.CreateTemp(filepathDir(idx.IndexPath), ".ods-index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	buf := bufio.NewWriter(tmp)
	hash := crc32.NewIEEE()
	w := io.MultiWriter(buf, hash)
	if _, err = io.WriteString(w, "ODSIX002"); err != nil {
		return err
	}
	header := [7]uint64{uint64(idx.MaxRow), uint64(idx.MaxCol), uint64(idx.XMLSize), uint64(len(idx.odsRuns)), uint64(len(raw)), uint64(bodyCRC), uint64(idx.odsCount)}
	if err = binary.Write(w, binary.LittleEndian, header); err != nil {
		return err
	}
	for _, r := range idx.odsRuns {
		if err = binary.Write(w, binary.LittleEndian, [4]uint64{uint64(r.First), uint64(r.Count), uint64(r.Start), uint64(r.End)}); err != nil {
			return err
		}
	}
	if _, err = w.Write(raw); err != nil {
		return err
	}
	if err = binary.Write(buf, binary.LittleEndian, hash.Sum32()); err != nil {
		return err
	}
	if err = buf.Flush(); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmp.Name(), idx.IndexPath)
}
func loadODSIndex(indexPath, rowsPath, sheet string) (*SheetIndex, error) {
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	hash := crc32.NewIEEE()
	r := io.TeeReader(br, hash)
	var magic [8]byte
	var h [7]uint64
	if _, err = io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "ODSIX002" {
		return nil, errors.New("ODS 索引版本无效")
	}
	if err = binary.Read(r, binary.LittleEndian, &h); err != nil {
		return nil, err
	}
	if h[0] > formats.ExternalRows || h[1] > formats.ExcelColumns || h[2] < 8 || h[2] > uint64(formats.MaxQueryTempBytes) || h[3] > formats.ODSRowTemplates || h[4] > formats.ODSLayoutBytes || h[6] > formats.ExternalRows || uint64(st.Size()) != 8+56+h[3]*32+h[4]+4 {
		return nil, errors.New("ODS 索引容量或长度无效")
	}
	idx := &SheetIndex{XMLPath: rowsPath, IndexPath: indexPath, SheetPath: sheet, SourceFormat: "ods", MaxRow: int(h[0]), MaxCol: int(h[1]), XMLSize: int64(h[2]), HyperlinkStart: -1, odsLayout: &SourceLayout{}}
	idx.odsRuns = make([]odsRowSpan, int(h[3]))
	prev, last, position := uint64(0), uint64(8), 0
	for i := range idx.odsRuns {
		var v [4]uint64
		if err = binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		if v[0] <= prev || v[1] == 0 || v[0] > h[0] || v[1] > h[0]-v[0]+1 || v[2] != last || v[3] <= last || v[3] > h[2] || v[3]-last > maxExternalRowBytes {
			return nil, errors.New("ODS 索引跨度无效")
		}
		idx.odsRuns[i] = odsRowSpan{int(v[0]), position, int(v[1]), int64(last), int64(v[3])}
		position += int(v[1])
		prev = v[0] + v[1] - 1
		last = v[3]
	}
	if last != h[2] || uint64(position) != h[6] {
		return nil, errors.New("ODS 索引条数不符")
	}
	idx.odsCount = position
	layout := make([]byte, int(h[4]))
	if _, err = io.ReadFull(r, layout); err != nil {
		return nil, err
	}
	var wanted uint32
	if err = binary.Read(br, binary.LittleEndian, &wanted); err != nil || wanted != hash.Sum32() || json.Unmarshal(layout, idx.odsLayout) != nil || !idx.odsLayout.valid() {
		return nil, errors.New("ODS 索引或布局校验失败")
	}
	body, err := os.Open(rowsPath)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	crc := crc32.NewIEEE()
	n, err := io.Copy(crc, io.LimitReader(body, idx.XMLSize+1))
	if err != nil || n != idx.XMLSize || uint64(crc.Sum32()) != h[5] {
		return nil, fmt.Errorf("ODS 行缓存校验失败")
	}
	idx.Dimension = "A1"
	if idx.MaxCol > 0 {
		idx.Dimension = "A1:" + ColumnName(idx.MaxCol) + strconv.Itoa(idx.MaxRow)
	}
	return idx, nil
}
