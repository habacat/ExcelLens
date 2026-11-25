package workbook

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// SourcePrintGeometry is opt-in. It never changes display SourceLayout, source
// cells or cache encodings. Only the caller's already prepared index is read.
func (w *Workbook) SourcePrintGeometry(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int64, int64, string)) (*SourcePrintGeometryIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := formats.Lookup(w.Format)
	actual, found := w.FindSheet(meta.Name)
	if !ok || !source.PrintMetadata || !found || actual.Path != meta.Path {
		return nil, errors.New("此来源不支持源打印几何，或工作表身份不匹配")
	}
	w.indexMu.RLock()
	prepared := w.indexes[meta.Path]
	w.indexMu.RUnlock()
	if idx == nil || idx != prepared || idx.SheetPath != meta.Path {
		return nil, errors.New("请先准备同一来源工作表索引，再读取打印几何")
	}
	state := sheetMetadataState[SourcePrintGeometryIndex]{&w.printGeometryMu, &w.printGeometryCache, &w.printGeometryLoading, &w.printGeometryClock, &w.printGeometryBytes}
	return readSheetMetadata(ctx, meta.Path, state, sheetMetadataLimits{formats.PrintGeometryCacheBytes, formats.PrintGeometryCacheSheets}, (*SourcePrintGeometryIndex).BudgetBytes, w.CheckSourceUnchanged, func() (*SourcePrintGeometryIndex, error) {
		b := newPrintGeometryBuilder(meta.Name, w.Format, w.normalFont.FontName, w.normalDigitWidth())
		if w.Format == "xlsb" {
			if err := w.readXLSBPrintGeometry(ctx, meta, b); err != nil {
				return nil, err
			}
		} else if w.source != nil {
			if err := w.readBIFFPrintGeometry(ctx, meta, b, progress); err != nil {
				return nil, err
			}
		} else {
			if err := w.readVerifiedSheetXML(ctx, meta, idx, func(input io.Reader, total int64) error {
				return scanPrintGeometryXML(ctx, input, total, b, progress)
			}); err != nil {
				return nil, err
			}
		}
		return b.finish(ctx)
	})
}

func geometryAttributes(node xml.StartElement, wanted ...string) map[string]string {
	result := make(map[string]string)
	for _, attr := range node.Attr {
		for _, name := range wanted {
			if attr.Name.Local == name {
				result[printAttributeKey(attr)] = attr.Value
				break
			}
		}
	}
	return result
}

func (b *printGeometryBuilder) specification(attrs map[string]string, sizeKey, unit, hiddenKey string) printAxisSpec {
	raw, _ := json.Marshal(attrs)
	spec := printAxisSpec{raw: string(raw), size: attrs[sizeKey], unit: unit, status: "inherit"}
	if size, present := attrs[sizeKey]; present {
		spec.units, spec.status, spec.reason = b.measure(size, unit)
	}
	for _, name := range []string{hiddenKey, "customHeight", "customWidth", "bestFit", "thickTop", "thickBot", "thickBottom"} {
		if value, present := attrs[name]; present {
			flag, valid := sourceBool([]byte(value))
			if name == hiddenKey {
				spec.hidden, spec.hiddenSet = flag, true
			}
			if !valid {
				spec.status, spec.reason = "invalid", "源尺寸相关标志无效，原始属性保留"
			}
		}
	}
	for name := range attrs {
		if strings.HasPrefix(name, "{") {
			spec.status, spec.reason = "unresolved", "源尺寸含扩展命名空间属性，原始定义保留"
		}
	}
	return spec
}

func scanPrintGeometryXML(ctx context.Context, input io.Reader, total int64, b *printGeometryBuilder, progress func(int64, int64, string)) error {
	var root, container []byte
	kind := ""
	settings := newPrintMetadataBuilder(b.index.sheet, b.index.format)
	observe := func(tag []byte, info tagInfo, depth int) error {
		name := string(info.name)
		if depth == 1 {
			root = bytes.Clone(tag)
			return nil
		}
		if depth == 2 {
			kind = ""
			if name == "sheetData" || name == "cols" {
				node, err := printSettingsHeader([][]byte{root}, tag)
				if err != nil {
					return err
				}
				if node.Name.Local != "" && !info.selfClosing {
					container, kind = bytes.Clone(tag), name
				}
				return nil
			}
			if name != "sheetFormatPr" {
				return nil
			}
			node, err := printSettingsHeader([][]byte{root}, tag)
			if err != nil {
				return err
			}
			if node.Name.Local == "" {
				b.notice("源默认尺寸命名空间未解释")
				return nil
			}
			if b.formatSeen {
				return errors.New("源默认行列尺寸定义重复")
			}
			b.formatSeen = true
			rowAttrs := geometryAttributes(node, "defaultRowHeight", "zeroHeight", "customHeight", "thickTop", "thickBottom")
			row := b.specification(rowAttrs, "defaultRowHeight", "point", "zeroHeight")
			id, err := b.intern(row)
			if err != nil {
				return err
			}
			b.index.rows.defaultSpec = id
			colAttrs := geometryAttributes(node, "defaultColWidth", "baseColWidth")
			sizeKey, unit := "defaultColWidth", "base-char"
			if _, found := colAttrs[sizeKey]; !found {
				sizeKey, unit = "baseColWidth", "base-char"
			}
			id, err = b.intern(b.specification(colAttrs, sizeKey, unit, "hidden"))
			if err != nil {
				return err
			}
			b.index.columns.defaultSpec = id
			return nil
		}
		if depth != 3 || !(kind == "sheetData" && name == "row" || kind == "cols" && name == "col") {
			return nil
		}
		node, err := printSettingsHeader([][]byte{root, container}, tag)
		if err != nil {
			return err
		}
		if node.Name.Local == "" {
			return errors.New("源行列尺寸命名空间不可靠，未发布部分几何")
		}
		attrs := geometryAttributes(node, "ht", "width", "hidden", "customHeight", "customWidth", "bestFit", "thickTop", "thickBot")
		if name == "row" {
			row := b.rowSeen + 1
			for _, attr := range node.Attr {
				if attr.Name.Space == "" && attr.Name.Local == "r" {
					row, err = strconv.Atoi(attr.Value)
					if err != nil || row < 1 {
						return errors.New("源打印行尺寸坐标无效")
					}
				}
			}
			return b.addRow(row, b.specification(attrs, "ht", "point", "hidden"))
		}
		first, last := 0, 0
		for _, attr := range node.Attr {
			if attr.Name.Space == "" && (attr.Name.Local == "min" || attr.Name.Local == "max") {
				value, err := strconv.Atoi(attr.Value)
				if err != nil {
					return err
				}
				if attr.Name.Local == "min" {
					first = value
				} else {
					last = value
				}
			}
		}
		return b.addColumns(first, last, b.specification(attrs, "width", "stored-char", "hidden"))
	}
	if err := scanPrintSourceXML(ctx, input, total, settings, progress, observe); err != nil {
		return err
	}
	return ctx.Err()
}
