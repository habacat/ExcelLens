package workbook

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
)

func (w *Workbook) readBIFFPrintGeometry(ctx context.Context, meta SheetMeta, b *printGeometryBuilder, progress func(int64, int64, string)) error {
	r, err := w.openBIFFWorksheet(meta)
	if err != nil {
		return err
	}
	defer r.cf.Close()
	r.ctx = ctx
	custom, standardSeen := false, false
	last, total := int64(0), r.reader.end-r.start
	seen := make(map[uint16]bool)
	if progress != nil {
		progress(0, total, "正在读取源打印尺寸")
	}
	for {
		id, data, err := r.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if id == 0x01AA {
			if custom {
				return errors.New("BIFF8自定义视图起始记录重复")
			}
			custom = true
		} else if id == 0x01AB {
			if !custom {
				return errors.New("BIFF8自定义视图缺少起始记录")
			}
			custom = false
		} else if !custom && (id == 0x0225 || id == 0x0055 || id == 0x0099 || id == 0x0208 || id == 0x007D) {
			if id == 0x0225 || id == 0x0055 || id == 0x0099 {
				if seen[id] {
					return errors.New("BIFF8默认打印尺寸定义重复")
				}
				seen[id] = true
			}
			attrs := map[string]string{"recordId": strconv.Itoa(int(id))}
			switch id {
			case 0x0225:
				if len(data) != 4 {
					return errors.New("BIFF8默认行高记录长度无效")
				}
				attrs["recordHex"] = hex.EncodeToString(data)
				attrs["height"] = strconv.Itoa(int(int16(xlsUint16(data, 2))))
				attrs["hidden"] = strconv.FormatBool(xlsUint16(data, 0)&2 != 0)
				attrs["customHeight"] = strconv.FormatBool(xlsUint16(data, 0)&1 != 0)
				spec := b.specification(attrs, "height", "twip", "hidden")
				if height := int16(xlsUint16(data, 2)); height < 0 || height > 8179 || height == 0 && xlsUint16(data, 0)&2 == 0 {
					spec.status, spec.reason = "invalid", "BIFF8默认行高超出源格式范围，未采用占位高度"
				}
				at, err := b.intern(spec)
				if err != nil {
					return err
				}
				b.index.rows.defaultSpec = at
			case 0x0055, 0x0099:
				if len(data) != 2 {
					return errors.New("BIFF8默认列宽记录长度无效")
				}
				if id == 0x0055 && standardSeen {
					break
				}
				unit := "base-char"
				if id == 0x0099 {
					unit, standardSeen = "biff-col256", true
				}
				attrs["recordHex"] = hex.EncodeToString(data)
				attrs["width"] = strconv.Itoa(int(xlsUint16(data, 0)))
				at, err := b.intern(b.specification(attrs, "width", unit, "hidden"))
				if err != nil {
					return err
				}
				b.index.columns.defaultSpec = at
			case 0x0208:
				if len(data) != 16 {
					return errors.New("BIFF8打印行高记录长度无效")
				}
				attrs["height"] = strconv.Itoa(int(xlsUint16(data, 6)))
				attrs["heightHex"], attrs["flagsHex"] = hex.EncodeToString(data[6:8]), hex.EncodeToString(data[12:16])
				attrs["hidden"] = strconv.FormatBool(xlsUint16(data, 12)&0x20 != 0)
				attrs["customHeight"] = strconv.FormatBool(xlsUint16(data, 12)&0x40 != 0)
				spec := b.specification(attrs, "height", "twip", "hidden")
				if height := xlsUint16(data, 6); height < 2 || height > 8192 {
					spec.status, spec.reason = "invalid", "BIFF8行高超出源格式范围，未把高位当作可靠默认高度"
				}
				if err := b.addRow(int(xlsUint16(data, 0))+1, spec); err != nil {
					return err
				}
			case 0x007D:
				if len(data) != 12 {
					return errors.New("BIFF8打印列宽记录长度无效")
				}
				attrs["width"] = strconv.Itoa(int(xlsUint16(data, 4)))
				attrs["widthHex"], attrs["flagsHex"] = hex.EncodeToString(data[4:6]), hex.EncodeToString(data[8:12])
				attrs["hidden"] = strconv.FormatBool(xlsUint16(data, 8)&1 != 0)
				attrs["customWidth"] = strconv.FormatBool(xlsUint16(data, 8)&2 != 0)
				attrs["bestFit"] = strconv.FormatBool(xlsUint16(data, 8)&4 != 0)
				if err := b.addColumns(int(xlsUint16(data, 0))+1, int(xlsUint16(data, 2))+1, b.specification(attrs, "width", "biff-col256", "hidden")); err != nil {
					return err
				}
			}
		}
		current := r.reader.pos - r.start
		if progress != nil && current-last >= 8<<20 {
			progress(current, total, "正在读取源打印尺寸")
			last = current
		}
	}
	if custom {
		return errors.New("BIFF8自定义视图未完整结束")
	}
	return ctx.Err()
}
