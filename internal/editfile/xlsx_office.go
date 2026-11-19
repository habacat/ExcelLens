package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strings"
)

// Shape placement, printer settings, external caches and VBA remain in their
// original parts. The application never instantiates a control or follows an
// external relationship. Only known local pivot sources can own grid cells.
func (p *xlsxPackage) inspectOfficeParts(ctx context.Context) error {
	p.pivotRanges = map[string][]ownedRange{}
	p.pivotSources = map[string]pivotSource{}
	p.pivotSheets, p.pivotOutputs = map[string]string{}, map[string]string{}
	for _, sheet := range p.layout.Sheets {
		links := p.links[sheet.Part]
		for _, part := range links {
			switch classifyPart(part).role {
			case "control":
				data, err := p.readPart(ctx, part)
				if err != nil {
					return err
				}
				var control struct {
					XMLName xml.Name
					Type    string `xml:"objectType,attr"`
					Link    string `xml:"fmlaLink,attr"`
				}
				if xml.Unmarshal(data, &control) != nil || control.XMLName != (xml.Name{Space: x14NS, Local: "formControlPr"}) || control.Type != "Button" || control.Link != "" {
					return fmt.Errorf("%w：控件属性 %s", ErrXLSX, part)
				}
			case "pivotTable":
				data, err := p.readPart(ctx, part)
				if err != nil {
					return err
				}
				var pivot struct {
					XMLName   xml.Name
					Cache     string `xml:"cacheId,attr"`
					Locations []struct {
						Ref string `xml:"ref,attr"`
					} `xml:"location"`
					Pages *struct{} `xml:"pageFields"`
				}
				if err = xml.Unmarshal(data, &pivot); err != nil {
					return err
				}
				owner := p.cacheIDs[pivot.Cache]
				if pivot.XMLName != (xml.Name{Space: mainNS, Local: "pivotTableDefinition"}) || owner == "" || len(p.links[part]) != 1 || len(pivot.Locations) != 1 {
					return fmt.Errorf("%w：透视表关系 %s", ErrXLSX, part)
				}
				for _, ref := range p.links[part] {
					if ref != owner {
						return ErrXLSX
					}
				}
				p.pivotSheets[part], p.pivotOutputs[part] = sheet.Name, owner
				loc, ok := parseRect(pivot.Locations[0].Ref)
				if !ok {
					return ErrXLSX
				}
				p.registerPivotAreas(sheet.Name, part, loc, data, pivot.Pages != nil)
			}
		}
		if err := p.checkSheetNotes(ctx, links); err != nil {
			return fmt.Errorf("工作表 %s：%w", sheet.Name, err)
		}
	}
	for _, part := range p.cacheIDs {
		data, err := p.readPart(ctx, part)
		if err != nil {
			return err
		}
		var cache struct {
			XMLName xml.Name
			Refresh string `xml:"enableRefresh,attr"`
			Source  struct {
				Type  string `xml:"type,attr"`
				Sheet struct {
					Sheet string `xml:"sheet,attr"`
					Ref   string `xml:"ref,attr"`
				} `xml:"worksheetSource"`
			} `xml:"cacheSource"`
		}
		if err = xml.Unmarshal(data, &cache); err != nil {
			return err
		}
		area, ok := parseRect(cache.Source.Sheet.Ref)
		name := cache.Source.Sheet.Sheet
		if cache.XMLName != (xml.Name{Space: mainNS, Local: "pivotCacheDefinition"}) || cache.Source.Type != "worksheet" || !ok || !p.layout.HasSheet(name) {
			return fmt.Errorf("%w：透视缓存来源 %s", ErrXLSX, part)
		}
		for _, s := range p.layout.Sheets {
			if strings.EqualFold(s.Name, name) {
				name = s.Name
				break
			}
		}
		refresh := cache.Refresh == "" || isTrue(cache.Refresh)
		p.pivotSources[part] = pivotSource{sheet: name, area: area, refreshable: refresh}
		reason := lockPivotHeader
		if refresh {
			area.lastRow = area.firstRow
		} else {
			reason = lockPivotInput
		}
		p.pivotRanges[name] = append(p.pivotRanges[name], ownedRange{area: area, reason: reason})
	}
	for _, f := range p.zip.File {
		switch classifyPart(f.Name).role {
		case "printer":
			b, err := p.readPart(ctx, f.Name)
			if err != nil {
				return err
			}
			if err = checkPrinterSettings(ctx, f.Name, b); err != nil {
				return err
			}
		case "vba":
			b, err := p.readPart(ctx, f.Name)
			if err != nil {
				return err
			}
			if len(b) < 512 || !bytes.Equal(b[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
				return fmt.Errorf("%w：VBA 项目容器", ErrXLSX)
			}
		}
	}
	return nil
}
