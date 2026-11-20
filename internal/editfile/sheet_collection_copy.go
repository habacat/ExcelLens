package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

func (a *sheetCollectionEdit) cloneParts(ctx context.Context) error {
	p := a.p
	a.parts[a.oldPart] = a.newPart
	var discover func(string)
	discover = func(part string) {
		for _, old := range slices.Sorted(maps.Values(p.links[part])) {
			role := classifyPart(old).role
			if old == externalBookTarget || role == "image" || role == "pivotCache" || a.parts[old] != "" {
				continue
			}
			a.parts[old] = a.allocate(old)
			discover(old)
		}
	}
	discover(a.oldPart)
	occupied := map[string]bool{}
	for _, t := range p.layout.Tables {
		occupied[strings.ToLower(t.Name)] = true
	}
	for _, t := range p.layout.Pivots {
		occupied[strings.ToLower(t.Name)] = true
	}
	unique := func(old string) string {
		for i := 2; ; i++ {
			n := old + "_" + strconv.Itoa(i)
			if !occupied[strings.ToLower(n)] {
				occupied[strings.ToLower(n)] = true
				return n
			}
		}
	}
	for _, t := range p.layout.Tables {
		if a.parts[t.Part] != "" {
			a.tableNames[t.Name] = unique(t.Name)
		}
	}
	for _, t := range p.layout.Pivots {
		if a.parts[t.Part] != "" {
			a.pivotNames[t.Name] = unique(t.Name)
		}
	}
	// Table IDs are workbook-wide and independent of their OPC filename.
	var maxTable int
	for _, t := range p.layout.Tables {
		raw, err := p.readPart(ctx, t.Part)
		if err != nil {
			return err
		}
		var v struct {
			ID int `xml:"id,attr"`
		}
		if err = xml.Unmarshal(raw, &v); err != nil {
			return err
		}
		maxTable = max(maxTable, v.ID)
	}
	for _, old := range slices.Sorted(maps.Keys(a.parts)) {
		next := a.parts[old]
		raw, err := p.readPart(ctx, old)
		if err != nil {
			return err
		}
		role := classifyPart(old).role
		if role == "printer" {
			a.updates[next] = raw
			continue
		}
		if role == "sheet" || role == "chart" || role == "table" {
			raw, err = a.rewriteSheetReferences(ctx, raw)
			if err != nil {
				return err
			}
		}
		var ps []xmlPatch
		err = walkXML(ctx, raw, func(e xmlEvent) error {
			el, ok := e.token.(xml.StartElement)
			if !ok {
				return nil
			}
			set := func(key, value string) error {
				changes, err := setXMLAttribute(raw, e.from, e.to, key, escapedXML(value))
				ps = append(ps, changes...)
				return err
			}
			if role == "table" && len(e.stack) == 1 && el.Name == (xml.Name{Space: mainNS, Local: "table"}) {
				maxTable++
				if err := set("id", strconv.Itoa(maxTable)); err != nil {
					return err
				}
				v := a.tableNames[attribute(el, "", "displayName")]
				if v == "" {
					return ErrXLSX
				}
				if attribute(el, "", "name") != "" {
					if err := set("name", v); err != nil {
						return err
					}
				}
				return set("displayName", v)
			}
			if role == "pivotTable" && len(e.stack) == 1 && el.Name == (xml.Name{Space: mainNS, Local: "pivotTableDefinition"}) {
				v := a.pivotNames[attribute(el, "", "name")]
				if v == "" {
					return ErrXLSX
				}
				return set("name", v)
			}
			if role == "sheet" && e.local() == "sheetPr" && attribute(el, "", "codeName") != "" {
				if p.layout.MacroEnabled {
					return fmt.Errorf("工作表 %s 带有 VBA 工作表模块", a.op.Sheet)
				}
				changes, err := removeXMLAttributes(raw, e.from, e.to, "codeName")
				ps = append(ps, changes...)
				return err
			}
			if role == "sheet" && e.local() == "sheetView" && attribute(el, "", "tabSelected") != "" {
				return set("tabSelected", "0")
			}
			return nil
		})
		if err != nil {
			return err
		}
		a.updates[next], err = applyXML(ctx, raw, ps)
		if err != nil {
			return err
		}
		oldRels := collectionRels(old)
		if p.files[oldRels] == nil {
			continue
		}
		raw, err = p.readPart(ctx, oldRels)
		if err != nil {
			return err
		}
		ps = nil
		err = walkXML(ctx, raw, func(e xmlEvent) error {
			el, ok := e.token.(xml.StartElement)
			if !ok || e.local() != "Relationship" {
				return nil
			}
			target := p.links[old][attribute(el, "", "Id")]
			if copy := a.parts[target]; copy != "" {
				changes, err := setXMLAttribute(raw, e.from, e.to, "Target", "/"+escapedXML(copy))
				ps = append(ps, changes...)
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		a.updates[collectionRels(next)], err = applyXML(ctx, raw, ps)
		if err != nil {
			return err
		}
	}
	return nil
}
