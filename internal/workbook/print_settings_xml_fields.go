package workbook

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

var printPropertyTypes = map[string]map[string]string{
	"pageSetup":    {"paperSize": "uint", "paperHeight": "measure", "paperWidth": "measure", "paperUnits": "enum:mm|cm|in|pt|pc|pi", "scale": "uint", "firstPageNumber": "uint", "fitToWidth": "uint", "fitToHeight": "uint", "pageOrder": "enum:downThenOver|overThenDown", "orientation": "enum:default|portrait|landscape", "usePrinterDefaults": "bool", "blackAndWhite": "bool", "draft": "bool", "cellComments": "enum:none|asDisplayed|atEnd", "useFirstPageNumber": "bool", "errors": "enum:displayed|blank|dash|NA", "horizontalDpi": "uint", "verticalDpi": "uint", "copies": "uint"},
	"pageSetUpPr":  {"autoPageBreaks": "bool", "fitToPage": "bool"},
	"printOptions": {"horizontalCentered": "bool", "verticalCentered": "bool", "headings": "bool", "gridLines": "bool", "gridLinesSet": "bool"},
	"pageMargins":  {"left": "decimal", "right": "decimal", "top": "decimal", "bottom": "decimal", "header": "decimal", "footer": "decimal"},
	"headerFooter": {"differentOddEven": "bool", "differentFirst": "bool", "scaleWithDoc": "bool", "alignWithMargins": "bool"},
	"rowBreaks":    {"count": "uint", "manualBreakCount": "uint"},
	"colBreaks":    {"count": "uint", "manualBreakCount": "uint"},
}

func printAttributeKey(attr xml.Attr) string {
	if attr.Name.Space == "" {
		return attr.Name.Local
	}
	return "{" + attr.Name.Space + "}" + attr.Name.Local
}

func printRawAttributes(node xml.StartElement) map[string]string {
	result := make(map[string]string)
	for _, attr := range node.Attr {
		if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
			result[printAttributeKey(attr)] = attr.Value
		}
	}
	return result
}

func printRelationshipAttribute(attr xml.Attr) bool {
	return attr.Name.Local == "id" && (attr.Name.Space == "http://schemas.openxmlformats.org/officeDocument/2006/relationships" || attr.Name.Space == "http://purl.oclc.org/ooxml/officeDocument/relationships")
}

func readPrintBlockAttributes(node xml.StartElement, b *printMetadataBuilder) {
	group := node.Name.Local
	fields := printPropertyTypes[group]
	units := ""
	for _, attr := range node.Attr {
		if attr.Name.Space == "" && attr.Name.Local == "paperUnits" {
			units = attr.Value
		}
	}
	for _, attr := range node.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		if printRelationshipAttribute(attr) && (group == "pageSetup" || group == "legacyDrawingHF") {
			kind := "printerSettings"
			if group == "legacyDrawingHF" {
				kind = "headerFooterDrawing"
			}
			b.value.References = append(b.value.References, SourcePrintReference{Kind: kind, ID: attr.Value, Status: "unresolved"})
			continue
		}
		name, kind, value := printAttributeKey(attr), "unknown", attr.Value
		if attr.Name.Space == "" && fields != nil {
			kind = fields[attr.Name.Local]
		}
		// Older producer schemas store a numeric dimension plus paperUnits.
		// Keep the original attribute in raw evidence, normalize only the measure.
		if kind == "measure" && units != "" {
			if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				value = strings.TrimSpace(value) + units
			}
		}
		b.property(group, name, kind, value)
	}
	if group == "pageMargins" {
		for _, required := range []string{"left", "right", "top", "bottom", "header", "footer"} {
			if _, found := b.value.Properties[group][required]; !found {
				b.unknown(group, required, "decimal", "源页边距未保存完整字段，不能补成应用默认值")
			}
		}
	}
}

func readPrintSettingsBlock(ctx context.Context, parents [][]byte, block []byte, b *printMetadataBuilder) error {
	var prefix, suffix strings.Builder
	for _, parent := range parents {
		prefix.Write(parent)
	}
	for at := len(parents) - 1; at >= 0; at-- {
		suffix.WriteString("</" + catalogueTagQName(parents[at]) + ">")
	}
	d := xml.NewDecoder(io.MultiReader(strings.NewReader(prefix.String()), bytes.NewReader(block), strings.NewReader(suffix.String())))
	depth, containerDepth, manual := 0, len(parents)+1, 0
	var node xml.StartElement
	var count int
	seen := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if err == io.EOF {
			if depth != 0 || node.Name.Local == "" {
				return errors.New("打印定义XML未完整结束")
			}
			for _, attr := range node.Attr {
				if attr.Name.Space == "" && (attr.Name.Local == "count" || attr.Name.Local == "manualBreakCount") && (node.Name.Local == "rowBreaks" || node.Name.Local == "colBreaks") {
					number, err := strconv.ParseUint(strings.TrimSpace(attr.Value), 10, 32)
					actual := count
					if attr.Name.Local == "manualBreakCount" {
						actual = manual
					}
					if err != nil || number != uint64(actual) {
						return errors.New("源分页符数量与声明不一致，未发布部分设置")
					}
				}
			}
			return b.addDefinition(SourcePrintDefinition{Kind: node.Name.Local, Namespace: node.Name.Space, Attributes: printRawAttributes(node), RawXML: string(block)})
		}
		if err != nil {
			return err
		}
		if err := validatePrintXMLToken(token, depth); err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth <= len(parents) {
				continue
			}
			if depth == containerDepth {
				node = value
				readPrintBlockAttributes(node, b)
				continue
			}
			if value.Name.Space == node.Name.Space && depth == containerDepth+1 && (node.Name.Local == "rowBreaks" || node.Name.Local == "colBreaks") && value.Name.Local == "brk" {
				item := readPrintXMLBreak(value, node.Name.Local == "rowBreaks", b)
				content, err := skipPrintElement(ctx, d, depth)
				if err != nil {
					return err
				}
				depth--
				if content {
					item.Status, item.Reason = "unresolved", "源分页符含未知子结构或文字，不能作为可靠分页"
					b.notice(item.Reason)
				}
				if err := b.reserve(item); err != nil {
					return err
				}
				if len(b.value.RowBreaks)+len(b.value.ColBreaks) >= formats.PrintBreaks {
					return errors.New("源分页符数量超过读取容量")
				}
				if item.Manual != nil && *item.Manual {
					manual++
				}
				if node.Name.Local == "rowBreaks" {
					b.value.RowBreaks = append(b.value.RowBreaks, item)
				} else {
					b.value.ColBreaks = append(b.value.ColBreaks, item)
				}
				count++
				continue
			}
			if node.Name.Local == "headerFooter" && value.Name.Space == node.Name.Space && depth == containerDepth+1 && isPrintHeaderText(value.Name.Local) {
				if seen[value.Name.Local] {
					return errors.New("源页眉页脚定义重复，未拼接为有效文字")
				}
				seen[value.Name.Local] = true
				text, valid, err := readPrintText(ctx, d, value, depth)
				if err != nil {
					return err
				}
				if valid {
					b.property("headerFooter", value.Name.Local, "text", text)
				} else {
					b.unknown("headerFooter", value.Name.Local, "text", "源页眉页脚含未知子结构，原始定义保留，未拼接成有效文字")
				}
				depth--
				continue
			}
			b.notice("源打印定义含未解释的子结构，原始XML保留")
			if _, err := skipPrintElement(ctx, d, depth); err != nil {
				return err
			}
			depth--
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth >= containerDepth && len(bytes.TrimSpace(value)) > 0 {
				b.notice("源打印容器含未知文字，原始XML保留")
			}
		case xml.Directive:
			return errors.New("源打印设置含不支持的XML指令")
		}
	}
}

func isPrintHeaderText(name string) bool {
	switch name {
	case "oddHeader", "oddFooter", "evenHeader", "evenFooter", "firstHeader", "firstFooter":
		return true
	}
	return false
}

func readPrintText(ctx context.Context, d *xml.Decoder, start xml.StartElement, depth int) (string, bool, error) {
	valid := true
	for _, attr := range start.Attr {
		if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" && !(attr.Name.Space == "http://www.w3.org/XML/1998/namespace" && attr.Name.Local == "space" && (attr.Value == "preserve" || attr.Value == "default")) {
			valid = false
		}
	}
	var text strings.Builder
	for inner := 1; inner > 0; {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		token, err := d.Token()
		if err != nil {
			return "", false, err
		}
		if err := validatePrintXMLToken(token, depth+inner-1); err != nil {
			return "", false, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			valid = false
			inner++
		case xml.EndElement:
			inner--
		case xml.CharData:
			if inner == 1 {
				if len(value) > formats.PrintTextBytes-text.Len() {
					return "", false, errors.New("源页眉页脚文字超过读取容量")
				}
				text.Write(value)
			}
		case xml.Directive:
			return "", false, errors.New("源页眉页脚含不支持的XML指令")
		}
	}
	return text.String(), valid, nil
}

func skipPrintElement(ctx context.Context, d *xml.Decoder, depth int) (bool, error) {
	inner := 1
	content := false
	for inner > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		token, err := d.Token()
		if err != nil {
			return false, err
		}
		if err := validatePrintXMLToken(token, depth+inner-1); err != nil {
			return false, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			content = true
			inner++
		case xml.EndElement:
			inner--
		case xml.Directive:
			return false, errors.New("源打印定义含不支持的XML指令")
		case xml.CharData:
			content = content || len(bytes.TrimSpace(value)) > 0
		}
	}
	return content, nil
}

func readPrintXMLBreak(node xml.StartElement, horizontal bool, b *printMetadataBuilder) SourcePrintBreak {
	item := SourcePrintBreak{Index: -1, Min: 0, Max: 0, Status: "stored", Attributes: printRawAttributes(node)}
	source, _ := formats.Lookup(b.value.Format)
	axisMax, crossMax := source.MaxColumns, formats.ExcelRows
	if horizontal {
		axisMax, crossMax = source.MaxRows, formats.ExcelColumns
	}
	issue := func(reason string) { item.Status, item.Reason = "invalid", reason; b.notice(reason) }
	for _, attr := range node.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space != "" {
			issue("分页符含未知属性，原始定义保留")
			continue
		}
		switch attr.Name.Local {
		case "id", "min", "max":
			number, err := strconv.ParseUint(strings.TrimSpace(attr.Value), 10, 32)
			limit := crossMax - 1
			if attr.Name.Local == "id" {
				limit = axisMax
			}
			if err != nil || number > uint64(limit) {
				issue("分页符源坐标无效或越界，未采用默认坐标")
				continue
			}
			switch attr.Name.Local {
			case "id":
				item.Index = int(number)
			case "min":
				item.Min = int(number)
			case "max":
				item.Max = int(number)
			}
		case "man", "pt":
			value, valid := sourceBool([]byte(attr.Value))
			if !valid {
				issue("分页符标志无效，未采用默认标志")
			} else if attr.Name.Local == "man" {
				item.Manual = &value
			} else {
				item.Pivot = &value
			}
		default:
			issue("分页符含未解释的属性，原始定义保留")
		}
	}
	if item.Index < 0 || item.Max < item.Min {
		issue("分页符缺少可靠的源边界或跨轴范围")
	}
	return item
}
