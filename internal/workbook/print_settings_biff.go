package workbook

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf16"

	"excellens/internal/formats"
)

func printBIFFKind(id uint16) string {
	switch id {
	case 0x0014:
		return "oddHeader"
	case 0x0015:
		return "oddFooter"
	case 0x001A:
		return "colBreaks"
	case 0x001B:
		return "rowBreaks"
	case 0x0026:
		return "leftMargin"
	case 0x0027:
		return "rightMargin"
	case 0x0028:
		return "topMargin"
	case 0x0029:
		return "bottomMargin"
	case 0x002A:
		return "headings"
	case 0x002B:
		return "gridLines"
	case 0x0033:
		return "printSize"
	case 0x004D:
		return "printerSettings"
	case 0x0081:
		return "pageSetUpPr"
	case 0x0082:
		return "gridLinesSet"
	case 0x0083:
		return "horizontalCentered"
	case 0x0084:
		return "verticalCentered"
	case 0x00A1:
		return "pageSetup"
	case 0x0866:
		return "headerFooterDrawing"
	case 0x089C:
		return "headerFooter"
	}
	return ""
}

// This explicit scan does not read SST or decode cells. The common reader
// excludes embedded charts; custom-view settings never replace sheet settings.
func (w *Workbook) readBIFFPrintSettings(ctx context.Context, meta SheetMeta, b *printMetadataBuilder, progress func(int64, int64, string)) error {
	r, err := w.openBIFFWorksheet(meta)
	if err != nil {
		return err
	}
	defer r.cf.Close()
	r.ctx = ctx
	var id uint16
	var parts [][]byte
	partBytes := 0
	custom := false
	appendPart := func(data []byte) error {
		if len(parts) >= formats.PrintBIFFSegments || len(data) > formats.PrintBlockBytes-partBytes {
			return errors.New("BIFF8打印定义及续记录超过读取容量")
		}
		parts = append(parts, bytes.Clone(data))
		partBytes += len(data)
		return nil
	}
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		err := readBIFFPrintRecord(ctx, id, parts, b)
		parts, partBytes = nil, 0
		return err
	}
	total, last := r.reader.end-r.start, int64(0)
	if progress != nil {
		progress(0, total, "正在读取源打印设置")
	}
	for {
		record, data, err := r.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if record == 0x003C && len(parts) > 0 {
			if err := appendPart(data); err != nil {
				return err
			}
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		switch record {
		case 0x01AA:
			if custom {
				return errors.New("BIFF8自定义视图起始记录重复")
			}
			custom = true
		case 0x01AB:
			if !custom {
				return errors.New("BIFF8自定义视图缺少起始记录")
			}
			custom = false
		default:
			if !custom && printBIFFKind(record) != "" {
				id = record
				if err := appendPart(data); err != nil {
					return err
				}
			}
		}
		current := r.reader.pos - r.start
		if progress != nil && current-last >= 8<<20 {
			progress(current, total, "正在读取源打印设置")
			last = current
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if custom {
		return errors.New("BIFF8自定义视图未完整结束，未发布部分打印设置")
	}
	return ctx.Err()
}

func readBIFFPrintRecord(ctx context.Context, id uint16, parts [][]byte, b *printMetadataBuilder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(parts) == 0 || len(parts) > formats.PrintBIFFSegments {
		return errors.New("BIFF8打印定义缺少记录或续记录数量超限")
	}
	size := 0
	for _, part := range parts {
		if len(part) > formats.PrintBlockBytes-size {
			return errors.New("BIFF8打印定义超过读取容量")
		}
		size += len(part)
	}
	kind := printBIFFKind(id)
	if kind == "" {
		return errors.New("不是已知的BIFF8打印记录")
	}
	definition := SourcePrintDefinition{Kind: kind, RecordID: id, RecordHex: hex.EncodeToString(parts[0])}
	for _, part := range parts[1:] {
		definition.Continuation = append(definition.Continuation, hex.EncodeToString(part))
	}
	if err := b.addDefinition(definition); err != nil {
		return err
	}
	if kind == "printerSettings" || kind == "headerFooterDrawing" || kind == "printSize" {
		b.notice("保留源" + kind + "原始记录，未执行打印机配置或解释图形/特殊打印尺寸")
		return nil
	}
	if kind != "headerFooter" {
		if err := b.unique(kind); err != nil {
			return err
		}
	}
	c := xlsSegments{seg: parts}
	if kind == "oddHeader" || kind == "oddFooter" {
		text := ""
		if size != 0 {
			var err error
			text, err = readBIFFUnicodeText(ctx, &c, formats.PrintBIFFTextUnits, formats.PrintTextBytes)
			if err != nil {
				return err
			}
		}
		if c.remaining() != 0 {
			return errors.New("BIFF8页眉页脚文字后含无法解释的字节")
		}
		b.property("headerFooter", kind, "text", text)
		return nil
	}
	if len(parts) != 1 {
		return errors.New("BIFF8定长打印记录含未支持的续记录，未发布猜测设置")
	}
	data := parts[0]
	switch kind {
	case "headerFooter":
		return readBIFFPrintHeaderFooter(ctx, data, b)
	case "pageSetup":
		return readBIFFPrintSetup(data, b)
	case "rowBreaks", "colBreaks":
		return readBIFFPrintBreaks(data, kind == "rowBreaks", b)
	case "leftMargin", "rightMargin", "topMargin", "bottomMargin":
		if len(data) != 8 {
			return errors.New("BIFF8页边距记录长度无效")
		}
		b.property("pageMargins", kind[:len(kind)-len("Margin")], "decimal", xlsNumber(xlsFloat64(data, 0)))
	case "pageSetUpPr":
		if len(data) != 2 {
			return errors.New("BIFF8工作表打印标志记录长度无效")
		}
		flags := xlsUint16(data, 0)
		b.property("pageSetUpPr", "fitToPage", "bool", strconv.FormatBool(flags&0x0100 != 0))
		b.property("pageSetUpPr", "autoPageBreaks", "bool", strconv.FormatBool(flags&1 != 0))
	default:
		if len(data) != 2 {
			return errors.New("BIFF8打印选项记录长度无效")
		}
		raw := strconv.Itoa(int(xlsUint16(data, 0)))
		b.property("printOptions", kind, "bool", raw)
	}
	return ctx.Err()
}

func readBIFFPrintSetup(data []byte, b *printMetadataBuilder) error {
	if len(data) != 34 {
		return errors.New("BIFF8 Setup打印设置记录长度无效")
	}
	flags := xlsUint16(data, 10)
	putBool := func(name string, mask uint16) {
		b.property("pageSetup", name, "bool", strconv.FormatBool(flags&mask != 0))
	}
	putUint := func(name string, offset int) {
		b.property("pageSetup", name, "uint", strconv.Itoa(int(xlsUint16(data, offset))))
	}
	putBool("usePrinterDefaults", 4)
	putBool("blackAndWhite", 8)
	putBool("draft", 16)
	putBool("useFirstPageNumber", 128)
	for name, offset := range map[string]int{"fitToWidth": 6, "fitToHeight": 8} {
		putUint(name, offset)
		if xlsUint16(data, offset) > 32767 {
			b.invalid("pageSetup", name, "适配页数超过BIFF8允许范围，原值保留")
		}
	}
	if flags&128 != 0 {
		b.property("pageSetup", "firstPageNumber", "int", strconv.Itoa(int(int16(xlsUint16(data, 4)))))
	}
	order := "downThenOver"
	if flags&1 != 0 {
		order = "overThenDown"
	}
	b.property("pageSetup", "pageOrder", "enum:downThenOver|overThenDown", order)
	comments := "none"
	if flags&32 != 0 {
		comments = "asDisplayed"
		if flags&512 != 0 {
			comments = "atEnd"
		}
	}
	b.property("pageSetup", "cellComments", "enum:none|asDisplayed|atEnd", comments)
	b.property("pageSetup", "errors", "enum:displayed|blank|dash|NA", []string{"displayed", "blank", "dash", "NA"}[(flags>>10)&3])
	b.property("pageMargins", "header", "decimal", xlsNumber(xlsFloat64(data, 16)))
	b.property("pageMargins", "footer", "decimal", xlsNumber(xlsFloat64(data, 24)))
	if flags&4 != 0 {
		for _, name := range []string{"paperSize", "scale", "horizontalDpi", "verticalDpi", "copies", "orientation"} {
			b.unknown("pageSetup", name, printPropertyTypes["pageSetup"][name], "BIFF8 Setup的fNoPls为真，此字段未定义，未采用记录中的占位字节")
		}
		return nil
	}
	for name, offset := range map[string]int{"paperSize": 0, "scale": 2, "horizontalDpi": 12, "verticalDpi": 14, "copies": 32} {
		putUint(name, offset)
	}
	orientation := "landscape"
	// fNoOrient overrides fPortrait and specifies portrait (MS-XLS 2.4.257).
	if flags&(2|64) != 0 {
		orientation = "portrait"
	}
	b.property("pageSetup", "orientation", "enum:default|portrait|landscape", orientation)
	return nil
}

func readBIFFPrintBreaks(data []byte, horizontal bool, b *printMetadataBuilder) error {
	if len(data) < 2 {
		return errors.New("BIFF8分页符缺少数量声明")
	}
	count, limit, kind := int(xlsUint16(data, 0)), formats.BIFFColumns-1, "colBreaks"
	axisMax, crossMax := formats.BIFFColumns, formats.BIFFRows
	if horizontal {
		limit, kind, axisMax, crossMax = formats.PrintBIFFBreaks, "rowBreaks", formats.BIFFRows, formats.ExcelColumns
	}
	if count > limit || len(data) != 2+count*6 {
		return errors.New("BIFF8分页符数量、记录长度或容量无效")
	}
	previous := SourcePrintBreak{Index: -1}
	for at := 0; at < count; at++ {
		start := 2 + at*6
		manual := true
		item := SourcePrintBreak{Index: int(xlsUint16(data, start)), Min: int(xlsUint16(data, start+2)), Max: int(xlsUint16(data, start+4)), Manual: &manual, Status: "stored"}
		if item.Index >= axisMax || item.Min >= crossMax || item.Max >= crossMax || item.Min >= item.Max || item.Index < previous.Index || item.Index == previous.Index && item.Min <= previous.Max {
			item.Status, item.Reason = "invalid", "BIFF8分页符坐标、顺序或跨轴范围无效，未改写为整页分页"
			b.notice(item.Reason)
		}
		if err := b.reserve(item); err != nil {
			return err
		}
		if horizontal {
			b.value.RowBreaks = append(b.value.RowBreaks, item)
		} else {
			b.value.ColBreaks = append(b.value.ColBreaks, item)
		}
		previous = item
	}
	b.property(kind, "count", "uint", strconv.Itoa(count))
	b.property(kind, "manualBreakCount", "uint", strconv.Itoa(count))
	return nil
}

func readBIFFPrintHeaderFooter(ctx context.Context, data []byte, b *printMetadataBuilder) error {
	if len(data) < 38 || xlsUint16(data, 0) != 0x089C {
		return errors.New("BIFF8扩展页眉页脚头部无效或截断")
	}
	if !bytes.Equal(data[12:28], make([]byte, 16)) {
		b.notice("BIFF8扩展页眉页脚属于自定义视图，未当作当前工作表设置")
		return nil
	}
	if err := b.unique("headerFooter"); err != nil {
		return err
	}
	flags := xlsUint16(data, 28)
	for at, name := range []string{"differentOddEven", "differentFirst", "scaleWithDoc", "alignWithMargins"} {
		b.property("headerFooter", name, "bool", strconv.FormatBool(flags&(1<<at) != 0))
	}
	c := xlsSegments{seg: [][]byte{data[38:]}}
	for at, name := range []string{"evenHeader", "evenFooter", "firstHeader", "firstFooter"} {
		count := int(xlsUint16(data, 30+at*2))
		if count > formats.PrintBIFFTextUnits || count != 0 && (at < 2 && flags&1 == 0 || at >= 2 && flags&2 == 0) {
			return errors.New("BIFF8扩展页眉页脚长度或启用标志不一致")
		}
		text := ""
		if count > 0 {
			var err error
			text, err = readBIFFUnicodeText(ctx, &c, formats.PrintBIFFTextUnits, formats.PrintTextBytes)
			if err != nil {
				return err
			}
			if len(utf16.Encode([]rune(text))) != count {
				return fmt.Errorf("BIFF8扩展页眉页脚%s文字与声明不一致", name)
			}
		}
		b.property("headerFooter", name, "text", text)
	}
	if c.remaining() != 0 {
		return errors.New("BIFF8扩展页眉页脚存在未解释的尾字节")
	}
	return ctx.Err()
}
