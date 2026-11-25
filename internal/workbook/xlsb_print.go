package workbook

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func (w *Workbook) readXLSBPrintSettings(ctx context.Context, meta SheetMeta, b *printMetadataBuilder) error {
	if err := w.readPrintNames(ctx, meta, nil, b); err != nil {
		return err
	}
	data, err := w.xlsbSheetMeta(ctx, meta)
	if err != nil {
		return err
	}
	if data.PrintFit != nil {
		b.property("pageSetUpPr", "fitToPage", "bool", strconv.FormatBool(*data.PrintFit))
	}
	breakAxis := ""
	for _, record := range data.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := xlsbFields{data: record.Data}
		group := ""
		switch record.Kind {
		case brtMargins:
			group = "pageMargins"
			for _, key := range []string{"left", "right", "top", "bottom", "header", "footer"} {
				b.property(group, key, "inches", xlsbNumber(f.f64()))
			}
		case brtPrintOptions:
			group = "printOptions"
			flags := f.u16()
			for bit, key := range []string{"horizontalCentered", "verticalCentered", "headings", "gridLines"} {
				b.property(group, key, "bool", strconv.FormatBool(flags&(1<<bit) != 0))
			}
		case brtPageSetup:
			group = "pageSetup"
			numeric := [8]uint32{f.u32(), f.u32(), f.u32(), f.u32(), f.u32(), f.u32(), f.u32(), f.u32()}
			flags := f.u16()
			id := f.wide(true, 255)
			for i, key := range []string{"paperSize", "scale", "horizontalDpi", "verticalDpi", "copies", "firstPageNumber", "fitToWidth", "fitToHeight"} {
				if i == 5 {
					if flags&128 != 0 {
						b.property(group, key, "int", strconv.FormatInt(int64(int32(numeric[i])), 10))
					}
					continue
				}
				b.property(group, key, "uint", fmt.Sprint(numeric[i]))
			}
			order := "downThenOver"
			if flags&1 != 0 {
				order = "overThenDown"
			}
			b.property(group, "pageOrder", "enum", order)
			orientation := "default"
			if flags&64 == 0 {
				orientation = "portrait"
				if flags&2 != 0 {
					orientation = "landscape"
				}
			}
			b.property(group, "orientation", "enum", orientation)
			b.property(group, "blackAndWhite", "bool", strconv.FormatBool(flags&8 != 0))
			b.property(group, "draft", "bool", strconv.FormatBool(flags&16 != 0))
			b.property(group, "useFirstPageNumber", "bool", strconv.FormatBool(flags&128 != 0))
			b.property(group, "cellComments", "enum", [...]string{"none", "asDisplayed", "atEnd"}[xlsbPrintedNotes(flags)])
			b.property(group, "errors", "enum", [...]string{"displayed", "blank", "dash", "NA"}[flags>>9&3])
			if id != "" {
				link := w.source.xlsb.sheets[meta.Path].relations[id]
				ref := SourcePrintReference{Kind: "printerSettings", ID: id, Target: link.Target, TargetMode: link.TargetMode,
					Status: "unresolved", Reason: "XLSB 打印设备配置关系不可用"}
				if _, ok := xlsbPartTarget(meta.Path, link); ok && strings.HasSuffix(link.Type, "/printerSettings") {
					ref.Status, ref.Reason = "referenced", "确认打印设备配置关系，未执行设备指令"
				}
				if err := b.reserve(ref); err != nil {
					return err
				}
				b.value.References = append(b.value.References, ref)
			}
		case brtBeginHeaderFooter:
			group = "headerFooter"
			flags := f.u16()
			for bit, key := range []string{"differentOddEven", "differentFirst", "scaleWithDoc", "alignWithMargins"} {
				b.property(group, key, "bool", strconv.FormatBool(flags&(1<<bit) != 0))
			}
			for _, key := range []string{"oddHeader", "oddFooter", "evenHeader", "evenFooter", "firstHeader", "firstFooter"} {
				if value := f.wide(true, 32767); value != "" {
					b.property(group, key, "text", value)
				}
			}
		case brtBeginRwBrk, brtBeginColBrk:
			breakAxis = "rowBreaks"
			if record.Kind == brtBeginColBrk {
				breakAxis = "colBreaks"
			}
			count, manual := f.u32(), f.u32()
			b.property(breakAxis, "count", "uint", fmt.Sprint(count))
			b.property(breakAxis, "manualBreakCount", "uint", fmt.Sprint(manual))
			group = breakAxis
		case brtEndRwBrk, brtEndColBrk:
			breakAxis = ""
		case brtBrk:
			pos, first, last := f.u32(), f.u32(), f.u32()
			manual, pivot := f.u32() != 0, f.u32() != 0
			if breakAxis == "" || first > last {
				return errors.New("BIFF12 打印分页范围不完整")
			}
			item := SourcePrintBreak{Index: int(pos), Min: int(first), Max: int(last), Manual: &manual, Pivot: &pivot,
				Status: "stored", Attributes: map[string]string{"id": fmt.Sprint(pos), "min": fmt.Sprint(first), "max": fmt.Sprint(last)}}
			if err := b.reserve(item); err != nil {
				return err
			}
			if breakAxis == "rowBreaks" {
				b.value.RowBreaks = append(b.value.RowBreaks, item)
			} else {
				b.value.ColBreaks = append(b.value.ColBreaks, item)
			}
		}
		if f.err != nil {
			return f.err
		}
		if group != "" {
			if err := b.unique(group); err != nil {
				return err
			}
			d := SourcePrintDefinition{Kind: group, RecordID: record.Kind}
			if len(record.Data) <= 4096 {
				d.RecordHex = hex.EncodeToString(record.Data)
			}
			if err := b.addDefinition(d); err != nil {
				return err
			}
		}
	}
	return nil
}

func xlsbPrintedNotes(flags uint16) int {
	if flags&32 == 0 {
		return 0
	}
	if flags&256 != 0 {
		return 2
	}
	return 1
}

func (w *Workbook) readXLSBPrintGeometry(ctx context.Context, meta SheetMeta, b *printGeometryBuilder) error {
	data, err := w.xlsbSheetMeta(ctx, meta)
	if err != nil {
		return err
	}
	if data.DefaultRowTwips > 0 || data.Layout.ZeroHeight {
		spec := b.specification(map[string]string{"height": fmt.Sprint(data.DefaultRowTwips), "hidden": strconv.FormatBool(data.Layout.ZeroHeight)},
			"height", "twip", "hidden")
		b.index.rows.defaultSpec, err = b.intern(spec)
		if err != nil {
			return err
		}
	}
	if data.DefaultColRaw != 0 || data.DefaultColBase != 0 {
		unit, size := "biff-col256", data.DefaultColRaw
		if size == math.MaxUint32 {
			unit, size = "base-char", uint32(data.DefaultColBase)
		}
		b.index.columns.defaultSpec, err = b.intern(b.specification(map[string]string{"width": fmt.Sprint(size)}, "width", unit, "hidden"))
		if err != nil {
			return err
		}
	}
	for i, col := range data.Columns {
		hidden := i < len(data.Layout.ColStates) && data.Layout.ColStates[i].Hidden
		attr := map[string]string{"width": fmt.Sprint(int(math.Round(col.Width * 256))), "hidden": strconv.FormatBool(hidden)}
		if err := b.addColumns(col.Min, col.Max, b.specification(attr, "width", "biff-col256", "hidden")); err != nil {
			return err
		}
	}
	var height, state int
	end := 0
	if list := data.Layout.Rows; len(list) > 0 {
		end = list[len(list)-1].Max
	}
	if list := data.Layout.RowStates; len(list) > 0 {
		end = max(end, list[len(list)-1].Max)
	}
	for row := 1; row <= end; row++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for height < len(data.Layout.Rows) && data.Layout.Rows[height].Max < row {
			height++
		}
		for state < len(data.Layout.RowStates) && data.Layout.RowStates[state].Max < row {
			state++
		}
		var attributes map[string]string
		if height < len(data.Layout.Rows) && data.Layout.Rows[height].Min <= row {
			attributes = map[string]string{"height": fmt.Sprint(int(math.Round(data.Layout.Rows[height].Size * 15)))}
		}
		if state < len(data.Layout.RowStates) && data.Layout.RowStates[state].Min <= row {
			item := data.Layout.RowStates[state]
			if item.HiddenSet || item.ZeroSize {
				if attributes == nil {
					attributes = make(map[string]string, 2)
				}
				attributes["hidden"] = strconv.FormatBool(item.Hidden)
				if item.ZeroSize {
					attributes["height"] = "0"
				}
			}
		}
		if attributes != nil {
			if err := b.addRow(row, b.specification(attributes, "height", "twip", "hidden")); err != nil {
				return err
			}
		}
	}
	return nil
}
