package editfile

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Part recognition, content type, allowed inbound relation and the minimum
// inspection strategy are specified together for each supported OOXML part.
type partMode uint8

const (
	plainPart partMode = iota
	preservedPart
	coordinatePart
)

type partType struct {
	role, mime, relation string
	mode                 partMode
}

const (
	vbaMIME            = "application/vnd.ms-office.vbaProject"
	externalBookTarget = "#external-workbook"
)

var (
	tablePart        = regexp.MustCompile(`^xl/tables/[^/\\:]+\.xml$`)
	sheetPart        = regexp.MustCompile(`^xl/worksheets/sheet[1-9][0-9]*\.xml$`)
	themePart        = regexp.MustCompile(`^xl/theme/theme[1-9][0-9]*\.xml$`)
	externalBookPart = regexp.MustCompile(`^xl/externalLinks/externalLink[1-9][0-9]*\.xml$`)
	controlPart      = regexp.MustCompile(`^xl/ctrlProps/ctrlProp[1-9][0-9]*\.xml$`)
	customXMLPart    = regexp.MustCompile(`^customXml/item[1-9][0-9]*\.xml$`)
	customPropsPart  = regexp.MustCompile(`^customXml/itemProps[1-9][0-9]*\.xml$`)
)

var (
	chartPart        = regexp.MustCompile(`^xl/charts/chart[1-9][0-9]*\.xml$`)
	pivotPart        = regexp.MustCompile(`^xl/pivotTables/pivotTable[1-9][0-9]*\.xml$`)
	pivotCachePart   = regexp.MustCompile(`^xl/pivotCache/pivotCacheDefinition[1-9][0-9]*\.xml$`)
	pivotRecordsPart = regexp.MustCompile(`^xl/pivotCache/pivotCacheRecords[1-9][0-9]*\.xml$`)
	imageDrawingPart = regexp.MustCompile(`^xl/drawings/drawing[1-9][0-9]*\.xml$`)
	imagePart        = regexp.MustCompile(`^xl/media/image[1-9][0-9]*\.(png|jpeg|jpg)$`)
	noteDrawingPart  = regexp.MustCompile(`^xl/drawings/[A-Za-z0-9_-]+\.vml$`)
	commentsPart     = regexp.MustCompile(`^xl/(?:comments[1-9][0-9]*|comments/[A-Za-z0-9_-]+)\.xml$`)
	printerPart      = regexp.MustCompile(`^xl/printerSettings/printerSettings[1-9][0-9]*\.bin$`)
)

func classifyPart(name string) partType {
	switch name {
	case "[Content_Types].xml":
		return partType{role: "types"}
	case "_rels/.rels", "xl/_rels/workbook.xml.rels":
		return partType{role: "rels", mime: "application/vnd.openxmlformats-package.relationships+xml"}
	case "xl/workbook.xml":
		return partType{"book", spreadMIME + "sheet.main+xml", relationNS + "/officeDocument", plainPart}
	case "xl/styles.xml":
		return partType{"styles", spreadMIME + "styles+xml", relationNS + "/styles", plainPart}
	case "xl/sharedStrings.xml":
		return partType{"shared", spreadMIME + "sharedStrings+xml", relationNS + "/sharedStrings", plainPart}
	case calcChainPart:
		return partType{"chain", spreadMIME + "calcChain+xml", relationNS + "/calcChain", plainPart}
	case "docProps/core.xml":
		return partType{"core", "application/vnd.openxmlformats-package.core-properties+xml", packageNS + "/metadata/core-properties", plainPart}
	case "docProps/app.xml":
		return partType{"app", "application/vnd.openxmlformats-officedocument.extended-properties+xml", relationNS + "/extended-properties", plainPart}
	case "docProps/custom.xml":
		return partType{"custom", "application/vnd.openxmlformats-officedocument.custom-properties+xml", relationNS + "/custom-properties", plainPart}
	}
	if sheetPart.MatchString(name) {
		return partType{"sheet", spreadMIME + "worksheet+xml", relationNS + "/worksheet", plainPart}
	}
	if themePart.MatchString(name) {
		return partType{"theme", "application/vnd.openxmlformats-officedocument.theme+xml", relationNS + "/theme", plainPart}
	}
	switch {
	case name == sheetMetadataPart:
		return partType{"sheetMetadata", spreadMIME + "sheetMetadata+xml", relationNS + "/sheetMetadata", preservedPart}
	case tablePart.MatchString(name):
		return partType{"table", spreadMIME + "table+xml", relationNS + "/table", preservedPart}
	case chartPart.MatchString(name):
		return partType{"chart", "application/vnd.openxmlformats-officedocument.drawingml.chart+xml", relationNS + "/chart", preservedPart}
	case pivotPart.MatchString(name):
		return partType{"pivotTable", spreadMIME + "pivotTable+xml", relationNS + "/pivotTable", preservedPart}
	case pivotCachePart.MatchString(name):
		return partType{"pivotCache", spreadMIME + "pivotCacheDefinition+xml", relationNS + "/pivotCacheDefinition", preservedPart}
	case pivotRecordsPart.MatchString(name):
		return partType{"pivotRecords", spreadMIME + "pivotCacheRecords+xml", relationNS + "/pivotCacheRecords", preservedPart}
	case imageDrawingPart.MatchString(name):
		return partType{"drawing", "application/vnd.openxmlformats-officedocument.drawing+xml", relationNS + "/drawing", preservedPart}
	case noteDrawingPart.MatchString(name):
		return partType{"notesDrawing", vmlMIME, relationNS + "/vmlDrawing", preservedPart}
	case commentsPart.MatchString(name):
		return partType{"comments", spreadMIME + "comments+xml", relationNS + "/comments", preservedPart}
	case printerPart.MatchString(name):
		return partType{"printer", printerMIME, relationNS + "/printerSettings", preservedPart}
	case imagePart.MatchString(name):
		mime := "image/jpeg"
		if path.Ext(name) == ".png" {
			mime = "image/png"
		}
		return partType{"image", mime, relationNS + "/image", preservedPart}
	case relationshipOwner(name) != "":
		return partType{role: "rels", mime: "application/vnd.openxmlformats-package.relationships+xml", mode: preservedPart}
	case name == "xl/vbaProject.bin":
		return partType{"vba", vbaMIME, "http://schemas.microsoft.com/office/2006/relationships/vbaProject", coordinatePart}
	case controlPart.MatchString(name):
		return partType{"control", "application/vnd.ms-excel.controlproperties+xml", relationNS + "/ctrlProp", coordinatePart}
	case externalBookPart.MatchString(name):
		return partType{"externalBook", spreadMIME + "externalLink+xml", relationNS + "/externalLink", coordinatePart}
	case customXMLPart.MatchString(name):
		return partType{"customXml", "application/xml", relationNS + "/customXml", coordinatePart}
	case customPropsPart.MatchString(name):
		return partType{"customXmlProps", "application/vnd.openxmlformats-officedocument.customXmlProperties+xml", relationNS + "/customXmlProps", coordinatePart}
	}
	return partType{}
}

func relationshipOwner(name string) string {
	file, ok := strings.CutSuffix(path.Base(name), ".xml.rels")
	if !ok || path.Base(path.Dir(name)) != "_rels" {
		return ""
	}
	part := path.Join(path.Dir(path.Dir(name)), file+".xml")
	if (sheetPart.MatchString(part) || pivotPart.MatchString(part) || pivotCachePart.MatchString(part) || imageDrawingPart.MatchString(part) || externalBookPart.MatchString(part) || customXMLPart.MatchString(part)) &&
		name == path.Join(path.Dir(part), "_rels", path.Base(part)+".rels") {
		return part
	}
	return ""
}

func allowsRelationship(owner, role string) bool {
	if owner == "" {
		return role == "book" || role == "core" || role == "app" || role == "custom"
	}
	switch classifyPart(owner).role {
	case "book":
		return slices.Contains([]string{"sheet", "styles", "shared", "theme", "chain", "pivotCache", "sheetMetadata", "vba", "externalBook", "customXml"}, role)
	case "sheet":
		return role == "table" || role == "pivotTable" || role == "drawing" || role == "notesDrawing" || role == "comments" || role == "printer" || role == "control"
	case "customXml":
		return role == "customXmlProps"
	case "pivotTable":
		return role == "pivotCache"
	case "pivotCache":
		return role == "pivotRecords"
	case "drawing":
		return role == "image" || role == "chart"
	}
	return false
}

func (p *xlsxPackage) partMIME(name string) string {
	if name == "xl/workbook.xml" && p.layout.MacroEnabled {
		return macroBookMIME
	}
	return classifyPart(name).mime
}
