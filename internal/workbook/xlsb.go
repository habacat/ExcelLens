package workbook

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"strings"
	"sync"

	"excellens/internal/fileidentity"
	"excellens/internal/formats"
	"excellens/internal/styles"
)

const xlsbCacheDomain = "xlsb-biff12-v3"

type xlsbBook struct {
	workbook, sharedStrings, styles, theme string
	sheetNames                             []string
	sheets                                 map[string]*xlsbSheet
	supports                               []bool // MS-XLSB supporting links. True means the same workbook.
	externals                              []xlsbXTI
	names                                  []xlsbName
	tables                                 map[uint32]*xlsbTable
	fonts                                  []styles.FontXML
	baseDXFCount                           int
	metaMu                                 sync.Mutex
	metadata                               map[string]*xlsbSheetData
}

type xlsbSheet struct {
	relations map[string]relationship
	tables    []uint32
}

type xlsbXTI struct {
	source      uint32
	first, last int32
}

type xlsbName struct {
	name, definition string
	scope            uint32
	flags            uint32
	formula          xlsbFormula
}

type xlsbTable struct {
	id                     uint32
	sheet, name, display   string
	part, relation         string
	area                   CellMerge
	headerRows, totalsRows int
	names                  []string
	columnIDs              []uint32
}

func openXLSBParts(file string) (*zip.ReadCloser, map[string]*zip.File, error) {
	book, err := zip.OpenReader(file)
	if err != nil {
		return nil, nil, fmt.Errorf("不是有效的 XLSB 容器：%w", err)
	}
	if len(book.File) > maxArchiveEntries {
		book.Close()
		return nil, nil, errors.New("XLSB 部件数量超过上限")
	}
	parts := make(map[string]*zip.File, len(book.File))
	for _, f := range book.File {
		name := path.Clean(f.Name)
		if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.Contains(f.Name, "\\") || f.FileInfo().IsDir() {
			continue
		}
		if parts[name] != nil {
			book.Close()
			return nil, nil, fmt.Errorf("XLSB 部件路径重复：%q", name)
		}
		parts[name] = f
	}
	return book, parts, nil
}

func xlsbRelationships(parts map[string]*zip.File, owner string) (map[string]relationship, error) {
	name := "_rels/.rels"
	if owner != "" {
		name = path.Join(path.Dir(owner), "_rels", path.Base(owner)+".rels")
		if parts[name] == nil {
			return nil, nil
		}
	}
	data, err := readZipEntry(parts, name, maxMetadataPart)
	if err != nil {
		return nil, err
	}
	var rels relationshipsXML
	if err := xml.Unmarshal(data, &rels); err != nil {
		return nil, err
	}
	result := make(map[string]relationship, len(rels.Relationships))
	for _, rel := range rels.Relationships {
		if _, exists := result[rel.ID]; exists || rel.ID == "" {
			return nil, errors.New("XLSB 部件关系标识无效")
		}
		result[rel.ID] = rel
	}
	return result, nil
}

func xlsbPartTarget(owner string, rel relationship) (string, bool) {
	if !strings.HasPrefix(rel.Type, "http") || strings.EqualFold(rel.TargetMode, "External") {
		return "", false
	}
	return resolvePackagePart(owner, rel.Target)
}

func xlsbRelatedPart(owner string, rels map[string]relationship, typ string) (string, error) {
	var result string
	for _, rel := range rels {
		if !strings.HasSuffix(rel.Type, "/"+typ) {
			continue
		}
		target, ok := xlsbPartTarget(owner, rel)
		if !ok || result != "" {
			return "", fmt.Errorf("XLSB %s 部件关系无效", typ)
		}
		result = target
	}
	return result, nil
}

func openXLSB(file string) (*Workbook, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("所选路径不是普通 XLSB 文件")
	}
	pkg, parts, err := openXLSBParts(file)
	if err != nil {
		return nil, err
	}
	defer pkg.Close()
	root, err := xlsbRelationships(parts, "")
	if err != nil {
		return nil, err
	}
	b := &xlsbBook{sheets: make(map[string]*xlsbSheet), tables: make(map[uint32]*xlsbTable)}
	b.workbook, err = xlsbRelatedPart("", root, "officeDocument")
	if err != nil || b.workbook == "" || !strings.HasSuffix(b.workbook, ".bin") {
		return nil, errors.New("XLSB 缺少有效的二进制工作簿关系")
	}
	rels, err := xlsbRelationships(parts, b.workbook)
	if err != nil {
		return nil, err
	}
	for typ, target := range map[string]*string{"styles": &b.styles, "sharedStrings": &b.sharedStrings, "theme": &b.theme} {
		*target, err = xlsbRelatedPart(b.workbook, rels, typ)
		if err != nil || *target != "" && parts[*target] == nil {
			return nil, fmt.Errorf("XLSB %s 部件缺失或关系无效", typ)
		}
	}
	fingerprint, _, err := fileidentity.Fingerprint(file, xlsbCacheDomain)
	if err != nil {
		return nil, err
	}
	w, err := newExternalWorkbook(file, "xlsb", fingerprint, st)
	if err != nil {
		return nil, err
	}
	w.source.fingerprintDomain, w.source.xlsb = xlsbCacheDomain, b
	w.SourceInfo = SourceInfo{Format: "xlsb", ReadOnly: true}
	w.ThemeColors = styles.LoadTheme(parts[b.theme], maxMetadataPart)
	if err := w.readXLSBBook(parts[b.workbook], rels); err != nil {
		return nil, err
	}
	if b.styles != "" {
		if err := w.readXLSBStyles(parts[b.styles]); err != nil {
			return nil, err
		}
	}
	if b.sharedStrings != "" && parts[b.sharedStrings].UncompressedSize64 > formats.XLSBSharedStringBytes {
		return nil, errors.New("XLSB 共享字符串表超过 8 GiB")
	}
	for i := range w.Sheets {
		sheet := &w.Sheets[i]
		if parts[sheet.Path] == nil {
			return nil, fmt.Errorf("XLSB 工作表 %q 部件缺失", sheet.Name)
		}
		rel, err := xlsbRelationships(parts, sheet.Path)
		if err != nil {
			return nil, err
		}
		s := &xlsbSheet{relations: rel}
		b.sheets[sheet.Path] = s
		sheet.TabColor, err = w.xlsbTabColor(parts[sheet.Path])
		if err != nil {
			return nil, err
		}
		for _, r := range rel {
			if !strings.HasSuffix(r.Type, "/table") {
				continue
			}
			target, ok := xlsbPartTarget(sheet.Path, r)
			if !ok {
				return nil, errors.New("XLSB 工作表的表对象关系无效")
			}
			table, err := readXLSBTable(parts[target], sheet.Name)
			if err != nil {
				return nil, err
			}
			if b.tables[table.id] != nil {
				return nil, errors.New("XLSB 表对象标识重复")
			}
			table.part, table.relation = target, r.ID
			b.tables[table.id] = table
			s.tables = append(s.tables, table.id)
		}
	}
	w.resolveXLSBNames()
	if err := w.checkExternalUnchanged(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workbook) readXLSBBook(file *zip.File, relations map[string]relationship) error {
	b := w.source.xlsb
	var begin, end, view bool
	active := 0
	seenNames := make(map[string]bool)
	seenPaths := make(map[string]bool)
	seenIDs := make(map[uint32]bool)
	err := readXLSBPart(file, maxMetadataPart, func(kind uint16, data []byte) error {
		if !begin && kind != brtBeginBook || end {
			return errors.New("BIFF12 工作簿记录顺序无效")
		}
		f := xlsbFields{data: data}
		switch kind {
		case brtBeginBook:
			if begin {
				return errors.New("BIFF12 工作簿开始记录重复")
			}
			begin = true
		case brtEndBook:
			end = true
		case brtBookView:
			if !view {
				f.take(24)
				active = int(f.u32())
				view = true
			}
		case brtWbProp:
			w.Date1904 = f.u32()&1 != 0
		case brtBundleSh:
			state := f.u32()
			id := f.u32()
			relID := f.wide(true, 255)
			name := f.wide(false, 31)
			lower := strings.ToLower(name)
			if f.err != nil || state > 2 || id == 0 || id > math.MaxUint16 || seenIDs[id] || name == "" || seenNames[lower] || len(b.sheetNames) >= math.MaxUint16 {
				return errors.New("XLSB 工作表名称、标识或可见性无效")
			}
			seenIDs[id] = true
			seenNames[lower] = true
			b.sheetNames = append(b.sheetNames, name)
			rel := relations[relID]
			if !strings.HasSuffix(rel.Type, "/worksheet") {
				if rel.ID == "" && (relID != "" || state != 2) {
					return errors.New("XLSB 工作表关系缺失")
				}
				w.SourceInfo.PresentationNotice = "工作簿含图表页、宏表或其他非工作表页，网格仅显示普通工作表"
				return nil
			}
			target, ok := xlsbPartTarget(b.workbook, rel)
			if !ok || seenPaths[target] || !strings.HasSuffix(target, ".bin") {
				return errors.New("XLSB 工作表关系无效或重复")
			}
			seenPaths[target] = true
			w.Sheets = append(w.Sheets, SheetMeta{Name: name, Path: target, State: [...]string{"", "hidden", "veryHidden"}[state]})
		case brtSupSelf:
			b.supports = append(b.supports, true)
		case brtSupSame, brtSupBookSrc, brtSupAddin:
			b.supports = append(b.supports, false)
		case brtExternSheet:
			count := f.u32()
			if count > uint32(len(f.data)/12) {
				return errors.New("XLSB 外部引用目录长度无效")
			}
			for range count {
				b.externals = append(b.externals, xlsbXTI{f.u32(), int32(f.u32()), int32(f.u32())})
			}
		case brtName:
			if len(b.names) >= formats.CatalogueEntries {
				return errors.New("XLSB 定义名称超过当前读取容量")
			}
			flags := f.u32()
			f.u8()
			scope := f.u32()
			name := f.wide(false, 255)
			formula := f.formula().copy()
			b.names = append(b.names, xlsbName{name: name, scope: scope, flags: flags, formula: formula})
		}
		return f.err
	})
	if err != nil {
		return err
	}
	if !begin || !end || len(w.Sheets) == 0 {
		return errors.New("XLSB 工作簿结构不完整或没有可读取的工作表")
	}
	if active >= 0 && active < len(b.sheetNames) {
		for i, sheet := range w.Sheets {
			if sheet.Name == b.sheetNames[active] {
				w.ActiveTab = i
				break
			}
		}
	}
	return nil
}

func readXLSBTable(file *zip.File, sheet string) (*xlsbTable, error) {
	table := &xlsbTable{sheet: sheet}
	err := readXLSBPart(file, maxMetadataPart, func(kind uint16, data []byte) error {
		f := xlsbFields{data: data}
		switch kind {
		case brtBeginList:
			table.area = f.area()
			f.u32() // table type
			table.id = f.u32()
			table.headerRows, table.totalsRows = int(f.u32()), int(f.u32())
			f.take(32) // flags, six formatting indexes and the connection ID
			table.name = f.wide(true, 255)
			table.display = f.wide(true, 255)
		case brtBeginListCol:
			id := f.u32()
			f.take(20)
			name := f.wide(true, 255)
			caption := f.wide(true, 255)
			table.columnIDs = append(table.columnIDs, id)
			if caption == "" {
				caption = name
			}
			table.names = append(table.names, caption)
		}
		return f.err
	})
	if err != nil {
		return nil, err
	}
	if table.id == 0 || table.display == "" || len(table.names) != table.area.EndCol-table.area.Col+1 || table.headerRows > 1 || table.totalsRows > 1 {
		return nil, errors.New("XLSB 表对象定义不完整")
	}
	return table, nil
}
