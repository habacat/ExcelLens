package workbook

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

const (
	biffFeature11  = 0x0872
	biffFeature12  = 0x0878
	biffContinue11 = 0x0875
)

// The FRT continuation carries raw bytes after its own twelve-byte header,
// including when the split is inside a UTF-16 character. It is not SST Continue.
type biffTableCapture struct {
	meta    SheetMeta
	builder *catalogueBuilder
	data    []byte
	id      uint16
	ordinal int
	bytes   int
	failure string
	stopped bool
}

func newBIFFTableCapture(meta SheetMeta) *biffTableCapture {
	return &biffTableCapture{meta: meta, builder: newCatalogueBuilder()}
}

func (c *biffTableCapture) capture(id uint16, data []byte) {
	if c.stopped {
		return
	}
	if id == biffContinue11 && c.id != 0 {
		if len(data) < 12 || xlsUint16(data, 0) != biffContinue11 || xlsUint16(data, 2)&3 != 0 {
			c.failure = "BIFF8表对象续记录头无效"
		} else {
			c.append(data[12:])
		}
		return
	}
	if c.id != 0 && (id == 0x003C || id == 0x0812 || id == 0x087F) {
		c.failure = "BIFF8表对象使用了不匹配的续记录类型"
	}
	c.flush()
	if id == biffFeature11 || id == biffFeature12 {
		c.ordinal++
		if c.ordinal > formats.CatalogueEntries {
			c.builder.notice("BIFF8表对象数量超过读取容量，目录不完整")
			c.stopped = true
			return
		}
		c.id = id
		c.append(data)
	}
}

func (c *biffTableCapture) append(data []byte) {
	if len(data) > formats.CatalogueBytes-c.bytes || len(data) > formats.CataloguePartBytes-len(c.data) {
		c.failure = "BIFF8表对象定义超过元数据读取容量"
		return
	}
	c.bytes += len(data)
	if c.failure == "" {
		c.data = append(c.data, data...)
	}
}

func (c *biffTableCapture) flush() {
	if c.id == 0 {
		return
	}
	item, err := readBIFFTable(c.id, c.data, c.meta)
	item.ID = fmt.Sprintf("biff-table-%s-%d", strings.TrimPrefix(c.meta.Path, "external/xls/"), c.ordinal)
	if c.failure != "" {
		err = errors.New(c.failure)
	}
	if err != nil {
		item.Status, item.Reason = "unavailable", err.Error()
		c.builder.notice("部分BIFF8表对象未能完整解释，目录不完整")
	}
	encodedSize := 256 + len(item.Name)*6 + len(item.Definition)*6
	if item.Table != nil {
		for _, column := range item.Table.Columns {
			encodedSize += 64 + len(column.Name)*6
		}
	}
	if encodedSize > formats.CataloguePartBytes-c.builder.bytes || !c.builder.add(item) {
		c.builder.notice("BIFF8工作表表对象目录超过读取容量")
		c.stopped = true
	}
	c.data, c.id, c.failure = nil, 0, ""
}

func (c *biffTableCapture) finish() Catalogue {
	c.flush()
	seen := make(map[string]int)
	for i := range c.builder.value.Items {
		item := &c.builder.value.Items[i]
		if item.Table == nil || item.Table.SourceID == "" {
			continue
		}
		if previous, found := seen[item.Table.SourceID]; found {
			for _, index := range []int{previous, i} {
				c.builder.value.Items[index].Status = "invalid"
				c.builder.value.Items[index].Reason = "同一BIFF8工作表中的表对象编号重复"
			}
			c.builder.notice("BIFF8工作表含重复表对象编号，不能确认唯一记录")
		}
		seen[item.Table.SourceID] = i
	}
	for i := range c.builder.value.Items {
		item := &c.builder.value.Items[i]
		if item.Table != nil {
			// With lost framing/capacity, an unread definition might duplicate a
			// retained object. Do not certify just the prefix as the whole sheet.
			item.Table.Verified = c.builder.value.Complete && item.Status == "unresolved"
		}
	}
	return c.builder.value
}

type biffTableCursor struct {
	data []byte
	at   int
	err  error
}

func (r *biffTableCursor) take(size uint32) []byte {
	if r.err != nil {
		return nil
	}
	if uint64(size) > uint64(len(r.data)-r.at) {
		r.err = errors.New("BIFF8表对象定义截断或字段长度越界")
		return nil
	}
	value := r.data[r.at : r.at+int(size)]
	r.at += int(size)
	return value
}

func (r *biffTableCursor) u16() uint16 { return xlsUint16(r.take(2), 0) }
func (r *biffTableCursor) u32() uint32 { return xlsUint32(r.take(4), 0) }

// XLUnicodeString here has no rich-text/extension header. Its seven reserved
// flag bits are ignored, as required by MS-XLS, rather than reinterpreted.
func (r *biffTableCursor) text(maxChars int) string {
	chars := int(r.u16())
	flags := r.take(1)
	if r.err != nil {
		return ""
	}
	if chars > maxChars {
		r.err = errors.New("BIFF8表对象文字超过字段容量")
		return ""
	}
	size := chars * (1 + int(flags[0]&1))
	data := r.take(uint32(size))
	if r.err != nil {
		return ""
	}
	encoded := append([]byte{flags[0] & 1}, data...)
	text, _, err := biffCatalogueString(encoded, chars)
	if err != nil {
		r.err = err
	}
	return text
}

func (r *biffTableCursor) formula() {
	size := r.u16()
	if size == 0 && r.err == nil {
		r.err = errors.New("BIFF8表对象公式标记为空")
	}
	r.take(uint32(size)) // Preserve source formula semantics: never execute.
}

// MS-XLS Feature11/Feature12, TableFeatureType and Feat11FieldDataItem.
// The ignored FRT header ref8 is never used as the actual table range.
func readBIFFTable(id uint16, data []byte, meta SheetMeta) (CatalogueItem, error) {
	item := CatalogueItem{Kind: "table", Sheet: meta.Name, Status: "unresolved", Table: &CatalogueTable{}}
	if len(data) < 27 || xlsUint16(data, 0) != id || xlsUint16(data, 2)&2 != 0 || xlsUint16(data, 12) != 5 {
		return item, errors.New("BIFF8表对象记录头或共享类型无效")
	}
	count, length := int(xlsUint16(data, 19)), uint64(xlsUint32(data, 21))
	start := 27 + count*8
	if count != 1 || start > len(data) {
		return item, errors.New("BIFF8表对象必须包含一个明确的矩形范围")
	}
	if length != 0 && length != uint64(len(data)-start) {
		return item, errors.New("BIFF8表对象声明长度与完整记录不一致")
	}
	ref := data[27:35]
	r1, r2, c1, c2 := int(xlsUint16(ref, 0))+1, int(xlsUint16(ref, 2))+1, int(xlsUint16(ref, 4))+1, int(xlsUint16(ref, 6))+1
	if r1 > r2 || c1 > c2 || c2 > formats.BIFFColumns {
		return item, errors.New("BIFF8表对象范围无效或超出源格式容量")
	}
	item.Table.Ref = ColumnName(c1) + strconv.Itoa(r1) + ":" + ColumnName(c2) + strconv.Itoa(r2)
	item.Definition = "'" + strings.ReplaceAll(meta.Name, "'", "''") + "'!" + absoluteCatalogueRef(item.Table.Ref)
	r := biffTableCursor{data: data[start:]}
	header := r.take(64)
	if r.err != nil {
		return item, r.err
	}
	lt, sourceID := xlsUint32(header, 0), xlsUint32(header, 4)
	item.Table.SourceID = strconv.FormatUint(uint64(sourceID), 10)
	h, totals, flags := xlsUint32(header, 8), xlsUint32(header, 12), xlsUint32(header, 28)
	item.Table.HeaderRows, item.Table.TotalRows = int(h), int(totals)
	single, filter := flags&(1<<9) != 0, flags&2 != 0
	version := (flags >> 16) & 15
	if lt > 3 || xlsUint32(header, 20) != 64 || (version != 11 && version != 12) || h > 1 || totals > 1 || int(h+totals) > r2-r1+1 || (filter && h != 1) || (single && (lt != 2 || h != 0 || totals != 0 || r1 != r2 || c1 != c2)) || (id == biffFeature11 && (lt == 3 || (h == 0 && !single))) {
		return item, errors.New("BIFF8表对象固定字段、表头或合计范围无效")
	}
	item.Name = r.text(255)
	columns := int(r.u16())
	if item.Name == "" || columns < 1 || columns > formats.BIFFColumns || columns != c2-c1+1 {
		return item, errors.New("BIFF8表对象名称或列数量与范围不匹配")
	}
	if flags&(1<<14) != 0 {
		if lt != 1 {
			return item, errors.New("BIFF8表对象提供程序名称与数据源类型不匹配")
		}
		r.text(65535)
	}
	if flags&(1<<20) != 0 {
		entryID := r.text(65535)
		if lt == 2 && entryID != item.Table.SourceID {
			return item, errors.New("BIFF8表对象条目标识与表编号不一致")
		}
	}
	if lt == 1 {
		// Web-provider schemas/default values are not interpreted as local
		// column definitions. Retain the table and explicit limitation.
		return item, errors.New("BIFF8 Web数据提供程序表的列定义尚未支持确认；不会连接外部来源")
	}
	if flags&((1<<5)|(1<<15)|(1<<21)) != 0 {
		return item, errors.New("BIFF8表对象含不适用于本地数据源的同步字段")
	}
	ids, captions, sourceNames, queryIDs := map[uint32]bool{}, map[string]bool{}, map[string]bool{}, map[uint32]bool{}
	for column := 0; column < columns && r.err == nil; column++ {
		fixed := r.take(36)
		if r.err != nil {
			break
		}
		fieldID, kind, xmlKind, aggregate, aggBytes, colFlags, insertBytes := xlsUint32(fixed, 0), xlsUint32(fixed, 4), xlsUint32(fixed, 8), xlsUint32(fixed, 12), xlsUint32(fixed, 16), xlsUint32(fixed, 24), xlsUint32(fixed, 28)
		if fieldID == 0 || ids[fieldID] || kind != 0 || (lt != 2 && xmlKind != 0) || aggregate > 9 || colFlags&8 != 0 || (colFlags&4 != 0 && lt != 2) || (colFlags&(1<<8) != 0 && colFlags&(1<<7) == 0) || (colFlags&(1<<7) != 0 && aggregate != 9) || (colFlags&(1<<10) != 0 && aggregate != 0) || (id == biffFeature11 && colFlags&((1<<7)|(1<<10)) != 0) {
			return item, errors.New("BIFF8表对象列编号、类型或公式标志无效")
		}
		ids[fieldID] = true
		name := r.text(255)
		caption := name
		if !single {
			caption = r.text(255)
		}
		if r.err != nil {
			break
		}
		if name == "" || caption == "" || captions[caption] || (lt == 3 && sourceNames[name]) {
			return item, errors.New("BIFF8表对象含空列名或重复列标题")
		}
		captions[caption] = true
		sourceNames[name] = true
		item.Table.Columns = append(item.Table.Columns, CatalogueColumn{ID: strconv.FormatUint(uint64(fieldID), 10), Name: caption})
		r.take(aggBytes)
		r.take(insertBytes)
		if filter {
			size := r.u32()
			r.take(2)
			if size > 2080 {
				return item, errors.New("BIFF8表对象筛选字段超过规范容量")
			}
			r.take(size)
		}
		if colFlags&4 != 0 {
			count := r.u16()
			if count > 1 {
				return item, errors.New("BIFF8表对象XML映射数量无效")
			}
			if count == 1 {
				r.take(8) // XMapEntry flags and source schema ID; no external IO.
				r.text(31999)
			}
		}
		if colFlags&(1<<7) != 0 {
			if colFlags&(1<<8) != 0 {
				return item, errors.New("BIFF8表对象含尚未支持解释的合计数组公式附加数据")
			}
			r.formula()
		}
		if colFlags&(1<<10) != 0 {
			r.text(32767)
		}
		if lt == 3 {
			queryID := r.u32()
			if queryID == 0 || queryIDs[queryID] {
				return item, errors.New("BIFF8查询表列编号无效或重复")
			}
			queryIDs[queryID] = true
		}
		if h == 0 && !single {
			r.take(r.u32())
			if colFlags&(1<<9) != 0 {
				r.text(65535)
			}
		}
	}
	if r.err != nil {
		return item, r.err
	}
	if r.at != len(r.data) {
		return item, errors.New("BIFF8表对象含未解释的尾部或长度不一致")
	}
	return item, nil
}
