package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const (
	sheetMetadataPart = "xl/metadata.xml"
	drawingNS         = "http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing"
	x14NS             = "http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"
	absPathNS         = "http://schemas.microsoft.com/office/spreadsheetml/2010/11/ac"
	calcFeaturesNS    = "http://schemas.microsoft.com/office/spreadsheetml/2018/calcfeatures"
	dynamicArrayNS    = "http://schemas.microsoft.com/office/spreadsheetml/2017/dynamicarray"
	x15NS             = "http://schemas.microsoft.com/office/spreadsheetml/2010/11/main"
	revisionNS        = "http://schemas.microsoft.com/office/spreadsheetml/2014/revision"
)

func packageTarget(owner, target string) (string, bool) {
	if strings.ContainsAny(target, "\\?#%:\x00") || path.Clean(target) != target {
		return "", false
	}
	if absolute, ok := strings.CutPrefix(target, "/"); ok {
		target = absolute
	} else {
		if strings.HasPrefix(target, "../") && (owner == "" || owner == "xl/workbook.xml" && !customXMLPart.MatchString(strings.TrimPrefix(target, "../")) || strings.HasPrefix(target, "../../")) {
			return "", false
		}
		target = path.Join(path.Dir(owner), target)
	}
	return target, fs.ValidPath(target) && target != "."
}

func (p *xlsxPackage) readRelationships(ctx context.Context) error {
	p.links = map[string]map[string]string{}
	incoming := map[string]int{}
	for _, file := range p.zip.File {
		var owner string
		switch file.Name {
		case "_rels/.rels":
		case "xl/_rels/workbook.xml.rels":
			owner = "xl/workbook.xml"
		default:
			owner = relationshipOwner(file.Name)
			if owner == "" {
				continue
			}
			if p.files[owner] == nil {
				return fmt.Errorf("%w：关系部件缺少来源 %s", ErrXLSX, file.Name)
			}
		}
		links, err := p.relationships(ctx, file.Name, owner)
		if err != nil {
			return fmt.Errorf("%s：%w", file.Name, err)
		}
		p.links[owner] = links
		for _, target := range links {
			incoming[target]++
		}
	}
	for _, file := range p.zip.File {
		kind := classifyPart(file.Name)
		n := incoming[file.Name]
		if kind.relation != "" && (n == 0 || n > 1 && kind.role != "pivotCache" && kind.role != "image") {
			return fmt.Errorf("%w：部件关系归属不明确 %s", ErrXLSX, file.Name)
		}
	}
	return nil
}

type cellRect struct{ firstRow, firstCol, lastRow, lastCol int }

func parseRect(ref string) (cellRect, bool) {
	first, last, pair := strings.Cut(ref, ":")
	if !pair {
		last = first
	}
	fc, fr, err := workbook.ParseCellRef(first)
	lc, lr, endErr := workbook.ParseCellRef(last)
	return cellRect{fr, fc, lr, lc}, err == nil && endErr == nil && fr <= lr && fc <= lc && lr <= formats.ExcelRows &&
		first == workbook.ColumnName(fc)+strconv.Itoa(fr) && last == workbook.ColumnName(lc)+strconv.Itoa(lr)
}

func (r cellRect) contains(row, col int) bool {
	return row >= r.firstRow && row <= r.lastRow && col >= r.firstCol && col <= r.lastCol
}

func (r cellRect) covers(other cellRect) bool {
	return r.contains(other.firstRow, other.firstCol) && r.contains(other.lastRow, other.lastCol)
}

func (r cellRect) single() bool { return r.firstRow == r.lastRow && r.firstCol == r.lastCol }

func nonnegativeInt(raw string) (int, bool) {
	n, err := strconv.ParseUint(raw, 10, 31)
	return int(n), err == nil && strconv.FormatUint(n, 10) == raw
}

type workbookExtensions struct {
	p         *xlsxPackage
	links     map[string]string
	seen      map[string]bool
	names     []workbook.CatalogueItem
	nameText  strings.Builder
	extension xlsxExtension
}

func (w *workbookExtensions) visit(e xmlEvent) (bool, error) {
	if len(e.stack) < 2 {
		return false, nil
	}
	root := e.stack[1]
	if root == (xml.Name{Space: revisionNS, Local: "revisionPtr"}) {
		if len(e.stack) != 2 {
			return true, ErrXLSX
		}
		switch t := e.token.(type) {
		case xml.StartElement:
			if w.seen["revisionPtr"] {
				return true, ErrXLSX
			}
			w.seen["revisionPtr"] = true
			for _, a := range t.Attr {
				if nsDeclaration(a) {
					continue
				}
				switch a.Name.Space {
				case "":
					if a.Name.Local == "revIDLastSave" || a.Name.Local == "documentId" {
						continue
					}
				case "http://schemas.microsoft.com/office/spreadsheetml/2016/revision6":
					if a.Name.Local == "coauthVersionLast" || a.Name.Local == "coauthVersionMax" {
						continue
					}
				case "http://schemas.microsoft.com/office/spreadsheetml/2016/revision10":
					if a.Name.Local == "uidLastSave" {
						continue
					}
				}
				return true, ErrXLSX
			}
		case xml.CharData:
			if len(bytes.TrimSpace(t)) != 0 {
				return true, ErrXLSX
			}
		}
		return true, nil
	}
	if root == (xml.Name{Space: markupNS, Local: "AlternateContent"}) {
		w.p.layout.Structured = true
		return true, w.checkAbsPath(e)
	}
	if root.Space != mainNS {
		return false, nil
	}
	switch root.Local {
	case "externalReferences":
		if !w.p.layout.Native {
			return true, ErrXLSX
		}
		if el, ok := e.token.(xml.StartElement); ok {
			if len(e.stack) == 2 && plainAttributes(el) && !w.seen["externalReferences"] {
				w.seen["externalReferences"] = true
				return true, nil
			}
			if len(e.stack) == 3 && el.Name == (xml.Name{Space: mainNS, Local: "externalReference"}) && relationAttributes(el, "id") && externalBookPart.MatchString(w.links[attribute(el, relationNS, "id")]) {
				return true, nil
			}
			return true, ErrXLSX
		}
		return true, nil
	case "extLst":
		w.p.layout.Structured = true
		return true, checkKnownExtension(e, &w.extension, calcFeatureExtension, chartTrackingExtension, compatibilityVersionExtension)
	case "definedNames":
		return true, w.readDefinedName(e)
	case "pivotCaches":
		return true, w.checkPivotCache(e)
	}
	return false, nil
}

func (w *workbookExtensions) checkAbsPath(e xmlEvent) error {
	switch token := e.token.(type) {
	case xml.StartElement:
		switch len(e.stack) {
		case 2:
			if plainAttributes(token) && !w.seen["AlternateContent"] {
				w.seen["AlternateContent"] = true
				return nil
			}
		case 3:
			if token.Name == (xml.Name{Space: markupNS, Local: "Choice"}) && plainAttributes(token, "Requires") && attribute(token, "", "Requires") != "" && !w.seen["absPathChoice"] {
				w.seen["absPathChoice"] = true
				return nil
			}
		case 4:
			if token.Name == (xml.Name{Space: absPathNS, Local: "absPath"}) && plainAttributes(token, "url") && attribute(token, "", "url") != "" && !w.seen["absPath"] {
				w.seen["absPath"] = true
				return nil
			}
		}
		return ErrXLSX
	case xml.CharData:
		if len(bytes.TrimSpace(token)) > 0 {
			return ErrXLSX
		}
	case xml.EndElement:
		if len(e.stack) == 2 && !w.seen["absPath"] {
			return ErrXLSX
		}
	}
	return nil
}

func (w *workbookExtensions) checkPivotCache(e xmlEvent) error {
	switch token := e.token.(type) {
	case xml.StartElement:
		if len(e.stack) == 2 && plainAttributes(token) && w.p.cacheIDs == nil {
			w.p.cacheIDs = map[string]string{}
			return nil
		}
		if len(e.stack) == 3 && token.Name == (xml.Name{Space: mainNS, Local: "pivotCache"}) && relationAttributes(token, "id", "cacheId") {
			id := attribute(token, "", "cacheId")
			target := w.links[attribute(token, relationNS, "id")]
			_, ok := nonnegativeInt(id)
			if ok && pivotCachePart.MatchString(target) && w.p.cacheIDs[id] == "" {
				for _, existing := range w.p.cacheIDs {
					if existing == target {
						return ErrXLSX
					}
				}
				w.p.cacheIDs[id] = target
				return nil
			}
		}
		return ErrXLSX
	case xml.CharData:
		if len(bytes.TrimSpace(token)) > 0 {
			return ErrXLSX
		}
	case xml.EndElement:
		if len(e.stack) == 2 && len(w.p.cacheIDs) == 0 {
			return ErrXLSX
		}
	}
	return nil
}

func isTrue(s string) bool { return s == "1" || s == "true" }

func relationAttributes(el xml.StartElement, rel string, names ...string) bool {
	for _, a := range el.Attr {
		if nsDeclaration(a) || a.Name == (xml.Name{Space: relationNS, Local: rel}) {
			continue
		}
		if a.Name.Space != "" || !slices.Contains(names, a.Name.Local) {
			return false
		}
	}
	return true
}

type xlsxExtension struct {
	uri, space, element string
	attrs               []string
}

var (
	compatibilityVersionExtension = xlsxExtension{"{D14903EA-33C4-47F7-8F05-3474C54BE107}", "http://schemas.microsoft.com/office/spreadsheetml/2024/workbookCompatibilityVersion", "version", []string{"setVersion"}}
	calcFeatureExtension          = xlsxExtension{"{B58B0392-4F1F-4190-BB64-5DF3571DCE5F}", calcFeaturesNS, "calcFeatures", nil}
	chartTrackingExtension        = xlsxExtension{"{140A7094-0E35-4892-8432-C4D2E57EDEB5}", x15NS, "workbookPr", []string{"chartTrackingRefBase"}}
	pivotExtension                = xlsxExtension{"{962EF5D1-5CA2-4c93-8EF4-DBF5C05439D2}", x14NS, "pivotTableDefinition", []string{"hideValuesRow", "enableEdit", "autoApply", "fillDownLabelsDefault"}}
	pivotDefaultLayoutExtension   = xlsxExtension{"{747A6164-185A-40DC-8AA5-F01512510D54}", "http://schemas.microsoft.com/office/spreadsheetml/2016/pivotdefaultlayout", "pivotTableDefinition16", nil}
	pivotFieldExtension           = xlsxExtension{"{2946ED86-A175-432a-8AC1-64E0C546D7DE}", x14NS, "pivotField", []string{"fillDownLabels"}}
	pivotCacheExtension           = xlsxExtension{"{725AE2AE-9491-48be-B2B4-4EB974FC3084}", x14NS, "pivotCacheDefinition", nil}
	dynamicArrayExtension         = xlsxExtension{"{bdbb8cdc-fa1e-496e-a857-3c3f30c029c3}", dynamicArrayNS, "dynamicArrayProperties", []string{"fDynamic", "fCollapsed"}}
)

func checkKnownExtension(e xmlEvent, current *xlsxExtension, allowed ...xlsxExtension) error {
	if t, ok := e.token.(xml.StartElement); ok && len(e.stack) == 3 {
		uri := attribute(t, "", "uri")
		for _, profile := range allowed {
			if strings.EqualFold(uri, profile.uri) {
				*current = profile
				return checkExtension(e, 2, profile)
			}
		}
		return fmt.Errorf("%w：扩展 %s", ErrXLSX, uri)
	}
	return checkExtension(e, 2, *current)
}

func checkExtension(e xmlEvent, depth int, kind xlsxExtension) error {
	switch token := e.token.(type) {
	case xml.StartElement:
		switch len(e.stack) - depth {
		case 0:
			if token.Name == (xml.Name{Space: mainNS, Local: "extLst"}) && plainAttributes(token) {
				return nil
			}
		case 1:
			if token.Name == (xml.Name{Space: mainNS, Local: "ext"}) && plainAttributes(token, "uri") && strings.EqualFold(attribute(token, "", "uri"), kind.uri) {
				return nil
			}
		case 2:
			if token.Name == (xml.Name{Space: kind.space, Local: kind.element}) && plainAttributes(token, kind.attrs...) {
				return nil
			}
		case 3:
			if kind.element == "calcFeatures" && token.Name == (xml.Name{Space: calcFeaturesNS, Local: "feature"}) &&
				plainAttributes(token, "name") && strings.HasPrefix(attribute(token, "", "name"), "microsoft.com:") {
				return nil
			}
		}
		return fmt.Errorf("%w：扩展元素 %s", ErrXLSX, e.local())
	case xml.CharData:
		if len(bytes.TrimSpace(token)) != 0 {
			return ErrXLSX
		}
	}
	return nil
}
