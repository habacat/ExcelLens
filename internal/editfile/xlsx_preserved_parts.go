package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
)

const (
	lockPivotOutput = "透视表结果所在工作表"
	lockPivotInput  = "透视表数据源所在工作表"
	lockDependency  = "为透视表或固定区域提供公式数据的工作表"
	lockFilter      = "工作表已保存筛选条件"
	lockArray       = "工作表含多格数组公式"
)

func wholeSheetLock(reason string) bool {
	return reason == lockPivotOutput || reason == lockPivotInput || reason == lockDependency || reason == lockFilter || reason == lockArray
}

type ownedRange struct {
	pivot  string
	area   cellRect
	reason string
}
type pivotSource struct {
	sheet       string
	area        cellRect
	refreshable bool
}

func (p *xlsxPackage) inspectStructuredParts(ctx context.Context) error {
	return p.inspectLinkedParts(ctx, false)
}

func (p *xlsxPackage) inspectLinkedParts(ctx context.Context, pivotsOnly bool) error {
	p.locks = map[string]string{}
	p.pivotSheets = map[string]string{}
	p.pivotSources = map[string]pivotSource{}
	p.pivotOutputs = map[string]string{}
	p.pivotRanges = map[string][]ownedRange{}
	var drawings map[string]bool
	for _, s := range p.layout.Sheets {
		if !pivotsOnly {
			if err := p.checkSheetNotes(ctx, p.links[s.Part]); err != nil {
				return fmt.Errorf("工作表 %s：%w", s.Name, err)
			}
		}
		for _, part := range p.links[s.Part] {
			switch classifyPart(part).role {
			case "pivotTable":
				if p.pivotSheets[part] != "" {
					return ErrXLSX
				}
				p.pivotSheets[part] = s.Name
			case "drawing":
				if drawings == nil {
					drawings = map[string]bool{}
				}
				drawings[part] = true
			}
		}
	}
	for _, f := range p.zip.File {
		var check func(context.Context, string, []byte) error
		role := classifyPart(f.Name).role
		if pivotsOnly && role != "pivotCache" && role != "pivotTable" {
			continue
		}
		switch role {
		case "pivotCache":
			check = p.checkPivotDefinition
		case "pivotTable":
			check = p.checkPivotTable
		case "pivotRecords":
			check = checkPivotRecords
		case "chart":
			check = checkChartPart
		case "drawing":
			if !drawings[f.Name] {
				return ErrXLSX
			}
			check = p.checkDrawing
		case "image":
			check = checkEmbeddedImage
		case "printer":
			check = checkPrinterSettings
		default:
			continue
		}
		data, err := p.readPart(ctx, f.Name)
		if err == nil {
			err = check(ctx, f.Name, data)
		}
		if err != nil {
			return fmt.Errorf("%s：%w", f.Name, err)
		}
	}
	return nil
}

func checkPassiveXML(e xmlEvent, space string, elements map[string][]string) (bool, error) {
	switch token := e.token.(type) {
	case xml.StartElement:
		if token.Name.Space != space || !slices.Contains(elements[e.parent()], e.local()) {
			return false, fmt.Errorf("%w：元素 %s", ErrXLSX, e.local())
		}
		return true, nil
	case xml.CharData:
		if len(bytes.TrimSpace(token)) != 0 {
			return false, ErrXLSX
		}
	}
	return false, nil
}

var cacheDefinitionElements = map[string][]string{
	"":                     {"pivotCacheDefinition"},
	"pivotCacheDefinition": {"cacheSource", "cacheFields", "calculatedItems"},
	"calculatedItems":      {"calculatedItem"}, "calculatedItem": {"pivotArea"}, "pivotArea": {"references"}, "references": {"reference"}, "reference": {"x"},
	"cacheSource": {"worksheetSource"},
	"cacheFields": {"cacheField"},
	"cacheField":  {"sharedItems"},
	"sharedItems": {"s", "n", "b", "d", "e", "m"},
}

func pivotIdentity(a xml.Attr) bool {
	return a.Name == (xml.Name{Space: markupNS, Local: "Ignorable"}) || a.Name == (xml.Name{Space: revisionNS, Local: "uid"})
}

func (p *xlsxPackage) checkPivotDefinition(ctx context.Context, part string, data []byte) error {
	if len(p.pivotSheets) == 0 {
		return ErrXLSX
	}
	bound := false
	for _, related := range p.cacheIDs {
		if related == part {
			bound = true
			break
		}
	}
	if !bound {
		return fmt.Errorf("%w：透视缓存未绑定工作簿", ErrXLSX)
	}
	var source pivotSource
	source.refreshable = true
	seenSource := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if len(e.stack) >= 2 && e.stack[1] == (xml.Name{Space: mainNS, Local: "extLst"}) {
			return checkExtension(e, 2, pivotCacheExtension)
		}
		start, err := checkPassiveXML(e, mainNS, cacheDefinitionElements)
		if err != nil || !start {
			return err
		}
		el := e.token.(xml.StartElement)
		switch e.local() {
		case "pivotCacheDefinition":
			el.Attr = slices.DeleteFunc(slices.Clone(el.Attr), pivotIdentity)
			if flag := attribute(el, "", "enableRefresh"); flag != "" {
				if !ruleBool(flag) {
					return ErrXLSX
				}
				source.refreshable = isTrue(flag)
			}
			id := attribute(el, relationNS, "id")
			links := p.links[part]
			if id != "" || len(links) != 0 {
				if len(links) != 1 || !pivotRecordsPart.MatchString(links[id]) {
					return fmt.Errorf("%w：透视缓存记录关系", ErrXLSX)
				}
			}
			if !relationAttributes(el, "id", "createdVersion", "refreshedVersion", "minRefreshableVersion", "recordCount", "refreshedBy", "refreshedDate", "refreshedDateIso", "saveData", "refreshOnLoad", "enableRefresh", "missingItemsLimit", "invalid") {
				return ErrXLSX
			}
		case "cacheSource":
			if seenSource || attribute(el, "", "type") != "worksheet" || !plainAttributes(el, "type") {
				return fmt.Errorf("%w：透视缓存需使用本簿固定区域", ErrXLSX)
			}
			seenSource = true
		case "worksheetSource":
			if source.sheet != "" || !plainAttributes(el, "sheet", "ref") {
				return ErrXLSX
			}
			area, ok := parseRect(attribute(el, "", "ref"))
			if !ok {
				return fmt.Errorf("%w：透视源范围", ErrXLSX)
			}
			sheet := attribute(el, "", "sheet")
			i := slices.IndexFunc(p.layout.Sheets, func(s XLSXSheet) bool { return strings.EqualFold(s.Name, sheet) })
			if i < 0 {
				return fmt.Errorf("%w：透视源工作表", ErrXLSX)
			}
			source.sheet, source.area = p.layout.Sheets[i].Name, area
		case "cacheField":
			if !plainAttributes(el, "name", "numFmtId", "caption", "uniqueList", "databaseField") {
				return fmt.Errorf("%w：透视计算或扩展字段", ErrXLSX)
			}
		default:
			for _, a := range el.Attr {
				if !nsDeclaration(a) && a.Name.Space != "" {
					return ErrXLSX
				}
			}
		}
		return nil
	})
	if err == nil {
		if source.sheet == "" {
			return fmt.Errorf("%w：透视缓存缺少本簿范围", ErrXLSX)
		}
		p.pivotSources[part] = source
		header := source.area
		header.lastRow = header.firstRow
		p.pivotRanges[source.sheet] = append(p.pivotRanges[source.sheet], ownedRange{area: header, reason: lockPivotHeader})
		if !source.refreshable {
			p.locks[source.sheet] = lockPivotInput
		}
	}
	return err
}

var pivotTableElements = map[string][]string{
	"":                     {"pivotTableDefinition"},
	"pivotTableDefinition": {"location", "pivotFields", "rowFields", "rowItems", "colFields", "colItems", "pageFields", "dataFields", "pivotTableStyleInfo", "filters", "chartFormats"},
	"filters":              {"filter"}, "filter": {"autoFilter"}, "autoFilter": {"filterColumn"}, "filterColumn": {"top10", "customFilters"}, "customFilters": {"customFilter"},
	"chartFormats": {"chartFormat"}, "chartFormat": {"pivotArea"}, "pivotArea": {"references"}, "references": {"reference"}, "reference": {"x"},
	"pivotFields": {"pivotField"},
	"pivotField":  {"items"},
	"items":       {"item"},
	"rowFields":   {"field"},
	"colFields":   {"field"},
	"rowItems":    {"i"},
	"colItems":    {"i"},
	"i":           {"x"},
	"dataFields":  {"dataField"},
	"pageFields":  {"pageField"},
}

func (p *xlsxPackage) checkPivotTable(ctx context.Context, part string, data []byte) error {
	seenLocation := false
	sheet := p.pivotSheets[part]
	if sheet == "" {
		return ErrXLSX
	}
	var location cellRect
	var pageFields bool
	var extension xlsxExtension
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if len(e.stack) >= 2 && e.stack[1] == (xml.Name{Space: mainNS, Local: "extLst"}) {
			return checkKnownExtension(e, &extension, pivotExtension, pivotDefaultLayoutExtension)
		}
		if len(e.stack) >= 4 && e.stack[2].Local == "pivotField" && e.stack[3] == (xml.Name{Space: mainNS, Local: "extLst"}) {
			return checkExtension(e, 4, pivotFieldExtension)
		}
		start, err := checkPassiveXML(e, mainNS, pivotTableElements)
		if err != nil || !start {
			return err
		}
		el := e.token.(xml.StartElement)
		for _, a := range el.Attr {
			if len(e.stack) == 1 && pivotIdentity(a) {
				continue
			}
			if !nsDeclaration(a) && (a.Name.Space != "" || a.Name.Local == "formula") {
				return ErrXLSX
			}
		}
		switch e.local() {
		case "pivotTableDefinition":
			cache := p.cacheIDs[attribute(el, "", "cacheId")]
			links := p.links[part]
			p.pivotOutputs[part] = cache
			if cache == "" || len(links) != 1 {
				return fmt.Errorf("%w：透视表缺少缓存", ErrXLSX)
			}
			for _, related := range links {
				if related != cache {
					return fmt.Errorf("%w：透视表缓存 ID 与关系不一致", ErrXLSX)
				}
			}
		case "pageFields":
			pageFields = true
		case "location":
			var ok bool
			location, ok = parseRect(attribute(el, "", "ref"))
			if seenLocation || !ok {
				return ErrXLSX
			}
			seenLocation = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !seenLocation {
		return ErrXLSX
	}
	p.registerPivotAreas(sheet, part, location, data, pageFields)
	return nil
}

var pivotRecordElements = map[string][]string{
	"":                  {"pivotCacheRecords"},
	"pivotCacheRecords": {"r"},
	"r":                 {"s", "n", "b", "d", "e", "m", "x"},
}

func checkPivotRecords(ctx context.Context, _ string, data []byte) error {
	return walkXML(ctx, data, func(e xmlEvent) error {
		start, err := checkPassiveXML(e, mainNS, pivotRecordElements)
		if err != nil || !start {
			return err
		}
		el := e.token.(xml.StartElement)
		for _, a := range el.Attr {
			if len(e.stack) == 1 && pivotIdentity(a) {
				continue
			}
			if !nsDeclaration(a) && a.Name.Space != "" {
				return ErrXLSX
			}
		}
		return nil
	})
}

var imageDrawingElements = map[string][]string{
	"":               {"wsDr"},
	"wsDr":           {"twoCellAnchor", "oneCellAnchor", "absoluteAnchor"},
	"twoCellAnchor":  {"from", "to", "pic", "graphicFrame", "clientData"},
	"oneCellAnchor":  {"from", "ext", "pic", "graphicFrame", "clientData"},
	"absoluteAnchor": {"pos", "ext", "pic", "graphicFrame", "clientData"},
	"from":           {"col", "colOff", "row", "rowOff"},
	"to":             {"col", "colOff", "row", "rowOff"},
	"pic":            {"nvPicPr", "blipFill", "spPr"},
	"nvPicPr":        {"cNvPr", "cNvPicPr"},
	"cNvPicPr":       {"picLocks"},
	"blipFill":       {"blip", "stretch", "srcRect"},
	"stretch":        {"fillRect"},
	"spPr":           {"xfrm", "prstGeom", "noFill", "ln"},
	"xfrm":           {"off", "ext"},
	"prstGeom":       {"avLst"},
	"ln":             {"noFill"},
}

func (p *xlsxPackage) checkDrawing(ctx context.Context, part string, data []byte) error {
	used := map[string]bool{}
	links := p.links[part]
	hasImage := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		// Excel attaches an identity to a picture. It has no workbook reference.
		for i := len(e.stack) - 1; i > 0 && i >= len(e.stack)-3; i-- {
			if e.stack[i] != (xml.Name{Space: themeNS, Local: "extLst"}) || e.stack[i-1] != (xml.Name{Space: drawingNS, Local: "cNvPr"}) {
				continue
			}
			switch t := e.token.(type) {
			case xml.StartElement:
				switch len(e.stack) - 1 - i {
				case 0:
					if plainAttributes(t) {
						return nil
					}
				case 1:
					if t.Name == (xml.Name{Space: themeNS, Local: "ext"}) && plainAttributes(t, "uri") && strings.EqualFold(attribute(t, "", "uri"), "{FF2B5EF4-FFF2-40B4-BE49-F238E27FC236}") {
						return nil
					}
				case 2:
					if t.Name == (xml.Name{Space: "http://schemas.microsoft.com/office/drawing/2014/main", Local: "creationId"}) && plainAttributes(t, "id") {
						return nil
					}
				}
				return ErrXLSX
			case xml.CharData:
				if len(bytes.TrimSpace(t)) != 0 {
					return ErrXLSX
				}
			}
			return nil
		}
		if len(e.stack) >= 3 && e.stack[2] == (xml.Name{Space: drawingNS, Local: "graphicFrame"}) {
			if t, ok := e.token.(xml.StartElement); ok {
				switch t.Name {
				case xml.Name{Space: chartNS, Local: "chart"}:
					id := attribute(t, relationNS, "id")
					if e.parent() != "graphicData" || hasImage || classifyPart(links[id]).role != "chart" {
						return ErrXLSX
					}
					used[id], hasImage = true, true
				default:
					if t.Name.Space != drawingNS && t.Name.Space != themeNS {
						return ErrXLSX
					}
					for _, attr := range t.Attr {
						if attr.Name.Space == relationNS {
							return ErrXLSX
						}
					}
				}
			}
			return nil
		}
		switch token := e.token.(type) {
		case xml.StartElement:
			space := drawingNS
			if (len(e.stack) > 1 && e.stack[len(e.stack)-2].Space == themeNS) ||
				slices.Contains([]string{"picLocks", "blip", "stretch", "srcRect", "xfrm", "prstGeom", "noFill", "ln"}, e.local()) {
				space = themeNS
			}
			if token.Name.Space != space || !slices.Contains(imageDrawingElements[e.parent()], e.local()) {
				return fmt.Errorf("%w：绘图元素 %s", ErrXLSX, e.local())
			}
			if len(e.stack) == 2 {
				hasImage = false
			}
			if e.local() == "blip" {
				id := attribute(token, relationNS, "embed")
				if classifyPart(links[id]).role != "image" || !relationAttributes(token, "embed", "cstate") || hasImage {
					return fmt.Errorf("%w：绘图仅支持嵌入图片", ErrXLSX)
				}
				used[id], hasImage = true, true
			} else if !mainAttributes(token) {
				return ErrXLSX
			}
		case xml.CharData:
			if !slices.Contains([]string{"col", "colOff", "row", "rowOff"}, e.local()) && len(bytes.TrimSpace(token)) != 0 {
				return ErrXLSX
			}
		case xml.EndElement:
			if len(e.stack) == 2 && !hasImage {
				return ErrXLSX
			}
		}
		return nil
	})
	if err == nil && (len(used) == 0 || len(used) != len(links)) {
		return fmt.Errorf("%w：绘图关系未绑定", ErrXLSX)
	}
	return err
}

func checkEmbeddedImage(_ context.Context, part string, data []byte) error {
	switch classifyPart(part).mime {
	case "image/png":
		if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			return nil
		}
	case "image/jpeg":
		if bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
			return nil
		}
	}
	return fmt.Errorf("%w：嵌入图片类型不符", ErrXLSX)
}

var dynamicMetadataElements = map[string][]string{
	"":               {"metadata"},
	"metadata":       {"metadataTypes", "futureMetadata", "cellMetadata"},
	"metadataTypes":  {"metadataType"},
	"futureMetadata": {"bk"},
	"cellMetadata":   {"bk"},
	"bk":             {"rc"},
}

// This profile binds one XLDAPR cell-metadata record, using the standard
// one-based cm index and zero-based futureMetadata block reference.
func (p *xlsxPackage) checkDynamicMetadata(ctx context.Context) error {
	data, err := p.readPart(ctx, sheetMetadataPart)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	err = walkXML(ctx, data, func(e xmlEvent) error {
		if len(e.stack) >= 4 && e.stack[1].Local == "futureMetadata" {
			if err := checkExtension(e, 4, dynamicArrayExtension); err != nil {
				return err
			}
			if el, ok := e.token.(xml.StartElement); ok && el.Name.Space == dynamicArrayNS {
				if seen["dynamicArrayProperties"] || !isTrue(attribute(el, "", "fDynamic")) ||
					!slices.Contains([]string{"", "0", "false"}, attribute(el, "", "fCollapsed")) {
					return ErrXLSX
				}
				seen["dynamicArrayProperties"] = true
			}
			return nil
		}
		start, err := checkPassiveXML(e, mainNS, dynamicMetadataElements)
		if err != nil || !start {
			return err
		}
		el := e.token.(xml.StartElement)
		key := e.parent() + "/" + e.local()
		if seen[key] || !mainAttributes(el) {
			return ErrXLSX
		}
		seen[key] = true
		switch e.local() {
		case "metadataTypes", "cellMetadata", "futureMetadata":
			if attribute(el, "", "count") != "1" {
				return ErrXLSX
			}
			if e.local() == "futureMetadata" && attribute(el, "", "name") != "XLDAPR" {
				return ErrXLSX
			}
		case "metadataType":
			if attribute(el, "", "name") != "XLDAPR" || !isTrue(attribute(el, "", "cellMeta")) {
				return ErrXLSX
			}
		case "rc":
			if len(e.stack) != 4 || e.stack[1].Local != "cellMetadata" || !plainAttributes(el, "t", "v") ||
				attribute(el, "", "t") != "1" || attribute(el, "", "v") != "0" {
				return ErrXLSX
			}
		}
		return nil
	})
	if err == nil {
		p.dynamic = seen["metadataTypes/metadataType"] && seen["dynamicArrayProperties"] && seen["bk/rc"]
		if !p.dynamic {
			return fmt.Errorf("%w：无法绑定动态数组元数据", ErrXLSX)
		}
	}
	if err != nil {
		return fmt.Errorf("%s：%w", sheetMetadataPart, err)
	}
	return nil
}

// The original records and pivot definition remain available as a saved
// snapshot until the spreadsheet host refreshes the cache.
func pivotRefreshPatches(ctx context.Context, data []byte, automatic bool) ([]xmlPatch, error) {
	var patches []xmlPatch
	err := walkXML(ctx, data, func(e xmlEvent) error {
		el, ok := e.token.(xml.StartElement)
		if !ok || len(e.stack) != 1 {
			return nil
		}
		for _, name := range []string{"invalid", "refreshOnLoad"} {
			want := "1"
			if name == "refreshOnLoad" && !automatic {
				want = "0"
			}
			if isTrue(attribute(el, "", name)) == (want == "1") {
				continue
			}
			ps, err := setXMLAttribute(data, e.from, e.to, name, want)
			if err != nil {
				return err
			}
			patches = append(patches, ps...)
		}
		return nil
	})
	return patches, err
}

func checkChartPart(ctx context.Context, _ string, data []byte) error {
	return walkXML(ctx, data, func(e xmlEvent) error {
		if e.stack[0] != (xml.Name{Space: chartNS, Local: "chartSpace"}) {
			return ErrXLSX
		}
		if elem, ok := e.token.(xml.StartElement); ok {
			for _, a := range elem.Attr {
				if a.Name.Space == relationNS {
					return fmt.Errorf("%w：图表含外部或嵌入数据关系", ErrXLSX)
				}
			}
		}
		return nil
	})
}
