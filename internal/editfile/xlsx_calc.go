package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func validSheetID(raw string) bool {
	id, err := strconv.ParseUint(raw, 10, 32)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == raw
}

func (p *xlsxPackage) checkCalcChain(ctx context.Context) error {
	if p.files[calcChainPart] == nil {
		return nil
	}
	data, err := p.readPart(ctx, calcChainPart)
	if err != nil {
		return err
	}
	var sheet string
	err = walkXML(ctx, data, func(e xmlEvent) error {
		switch token := e.token.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(token)) != 0 {
				return ErrXLSX
			}
		case xml.StartElement:
			if token.Name.Space != mainNS {
				return ErrXLSX
			}
			if e.parent() == "" && e.local() == "calcChain" {
				for _, a := range token.Attr {
					if !nsDeclaration(a) {
						return ErrXLSX
					}
				}
				return nil
			}
			if e.parent() != "calcChain" || e.local() != "c" {
				return ErrXLSX
			}
			for _, a := range token.Attr {
				if nsDeclaration(a) {
					continue
				}
				if a.Name.Space != "" {
					return ErrXLSX
				}
				switch a.Name.Local {
				case "r":
				case "i":
					sheet = a.Value
				case "l", "s", "a", "t":
					if a.Value != "0" && a.Value != "1" && a.Value != "true" && a.Value != "false" {
						return ErrXLSX
					}
				default:
					return ErrXLSX
				}
			}
			ref := attribute(token, "", "r")
			col, row, err := workbook.ParseCellRef(ref)
			if err != nil || row > formats.ExcelRows || col > formats.ExcelColumns || !p.sheetIDs[sheet] || ref != workbook.ColumnName(col)+strconv.Itoa(row) {
				return ErrXLSX
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("计算链无效：%w", err)
	}
	return nil
}

type calculationAttribute struct {
	name, value string
}

var calculationAttributes = []calculationAttribute{
	{"fullCalcOnLoad", "1"}, {"forceFullCalc", "1"}, {"calcCompleted", "0"}, {"calcOnSave", "1"},
}

func requestsFullRecalc(el xml.StartElement) bool {
	for _, setting := range calculationAttributes {
		if attribute(el, "", setting.name) != setting.value {
			return false
		}
	}
	return true
}

func calcPropertiesPatches(ctx context.Context, data []byte) ([]xmlPatch, error) {
	var settings strings.Builder
	var names []string
	for _, setting := range calculationAttributes {
		fmt.Fprintf(&settings, ` %s="%s"`, setting.name, setting.value)
		names = append(names, setting.name)
	}
	var patches []xmlPatch
	var found bool
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch token := e.token.(type) {
		case xml.StartElement:
			if !found && len(e.stack) == 2 && token.Name.Space == mainNS && (token.Name.Local == "pivotCaches" || token.Name.Local == "extLst") {
				found = true
				patches = append(patches, xmlPatch{from: e.from, to: e.from, data: `<calcPr xmlns="` + mainNS + `"` + settings.String() + `/>`})
				return nil
			}
			if e.parent() != "workbook" || token.Name.Local != "calcPr" {
				return nil
			}
			found = true
			changed, err := removeXMLAttributes(data, e.from, e.to, names...)
			if err != nil {
				return err
			}
			pos := e.to - 1
			if data[pos-1] == '/' {
				pos--
			}
			patches = append(changed, xmlPatch{from: pos, to: pos, data: settings.String()})
		case xml.EndElement:
			if e.parent() == "" && token.Name.Local == "workbook" && !found {
				patches = append(patches, xmlPatch{from: e.from, to: e.from, data: `<calcPr xmlns="` + mainNS + `"` + settings.String() + `/>`})
			}
		}
		return nil
	})
	return patches, err
}

func calcChainReferencePatch(ctx context.Context, data []byte, part string) ([]xmlPatch, error) {
	var result []xmlPatch
	start := -1
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if len(e.stack) != 2 {
			return nil
		}
		switch token := e.token.(type) {
		case xml.StartElement:
			if part == "[Content_Types].xml" && token.Name.Local == "Override" && attribute(token, "", "PartName") == "/"+calcChainPart ||
				part == "xl/_rels/workbook.xml.rels" && token.Name.Local == "Relationship" && attribute(token, "", "Type") == relationNS+"/calcChain" {
				start = e.from
			}
		case xml.EndElement:
			if start >= 0 {
				result = append(result, xmlPatch{from: start, to: e.to})
				start = -1
			}
		}
		return nil
	})
	if err == nil && len(result) != 1 {
		err = fmt.Errorf("%w：找不到唯一的计算链引用", ErrXLSX)
	}
	return result, err
}
