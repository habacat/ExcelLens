package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

const (
	richValuePart     = "xl/richData/rdrichvalue.xml"
	richStructurePart = "xl/richData/rdrichvaluestructure.xml"
	richTypesPart     = "xl/richData/rdRichValueTypes.xml"
	richNS            = "http://schemas.microsoft.com/office/spreadsheetml/2017/richdata"
	richTypeNS        = "http://schemas.microsoft.com/office/spreadsheetml/2017/richdata2"
)

func richPartType(name string) partType {
	return partType{"richValue", "application/vnd.ms-excel." + strings.ToLower(name) + "+xml", "http://schemas.microsoft.com/office/2017/06/relationships/" + name, preservedPart}
}

const dynamicMetadataType = `<metadataType name="XLDAPR" minSupportedVersion="120000" copy="1" pasteAll="1" pasteValues="1" merge="1" splitFirst="1" rowColShift="1" clearFormats="1" clearComments="1" assign="1" coerce="1" cellMeta="1"/>`
const dynamicFuture = `<futureMetadata name="XLDAPR" count="1"><bk><extLst><ext uri="{bdbb8cdc-fa1e-496e-a857-3c3f30c029c3}"><dynamicArrayProperties xmlns="` + dynamicArrayNS + `" fDynamic="1" fCollapsed="0"/></ext></extLst></bk></futureMetadata>`
const richMetadataType = `<metadataType name="XLRICHVALUE" minSupportedVersion="120000" copy="1" pasteAll="1" pasteValues="1" merge="1" splitFirst="1" rowColShift="1" clearFormats="1" clearComments="1" assign="1" coerce="1"/>`
const basicArrayMetadata = `<metadata xmlns="` + mainNS + `"><metadataTypes count="1">` + dynamicMetadataType + `</metadataTypes>` + dynamicFuture + `<cellMetadata count="1"><bk><rc t="1" v="0"/></bk></cellMetadata></metadata>`

type richError struct {
	kind, subtype, row, col int
	propagated              bool
}

func (r richError) fields() ([]string, []int) {
	if r.propagated {
		return []string{"errorType", "propagated"}, []int{r.kind, 1}
	}
	if r.kind == 8 {
		return []string{"colOffset", "errorType", "rwOffset", "subType"}, []int{r.col, r.kind, r.row, r.subtype}
	}
	return []string{"errorType", "subType"}, []int{r.kind, r.subtype}
}

func (r richError) code() string {
	switch r.kind {
	case 8:
		return "#SPILL!"
	case 13:
		return "#CALC!"
	}
	return ""
}

type richErrorStore struct {
	layouts                            map[string]int
	values                             []richError
	structures                         int
	recordCount, futureCount, richType int
}

func (r *richErrorStore) code(index string) string {
	n, ok := positiveInt(index, len(r.values))
	if !ok {
		return ""
	}
	return r.values[n-1].code()
}

type metadataReference struct {
	Type  int `xml:"t,attr"`
	Index int `xml:"v,attr"`
}

// Value metadata (vm) and cell metadata (cm) use independent one-based
// indexes. The rc, future block, rich-value record and structure indexes
// identify the stored error, including its obstruction offset.
func (p *xlsxPackage) loadRichMetadata(ctx context.Context) error {
	var meta struct {
		XMLName xml.Name `xml:"metadata"`
		Types   []struct {
			Name string `xml:"name,attr"`
		} `xml:"metadataTypes>metadataType"`
		Future []struct {
			Name   string `xml:"name,attr"`
			Blocks []struct {
				Rich *struct {
					Index int `xml:"i,attr"`
				} `xml:"extLst>ext>rvb"`
				Dynamic *struct {
					Flag string `xml:"fDynamic,attr"`
				} `xml:"extLst>ext>dynamicArrayProperties"`
			} `xml:"bk"`
		} `xml:"futureMetadata"`
		Cells  []metadataReference `xml:"cellMetadata>bk>rc"`
		Values []metadataReference `xml:"valueMetadata>bk>rc"`
	}
	var types struct {
		XMLName xml.Name `xml:"rvStructures"`
		Defs    []struct {
			Type string `xml:"t,attr"`
			Keys []struct {
				Name string `xml:"n,attr"`
			} `xml:"k"`
		} `xml:"s"`
	}
	var data struct {
		XMLName xml.Name `xml:"rvData"`
		Records []struct {
			Structure int      `xml:"s,attr"`
			Values    []string `xml:"v"`
		} `xml:"rv"`
	}
	for _, part := range []struct {
		name, ns string
		target   any
		root     *xml.Name
	}{
		{sheetMetadataPart, mainNS, &meta, &meta.XMLName}, {richStructurePart, richNS, &types, &types.XMLName}, {richValuePart, richNS, &data, &data.XMLName},
	} {
		raw, err := p.readPart(ctx, part.name)
		if err != nil {
			return err
		}
		if err = xml.Unmarshal(raw, part.target); err != nil || part.root.Space != part.ns {
			return fmt.Errorf("%s：%w", part.name, ErrXLSX)
		}
	}
	info := &richErrorStore{structures: len(types.Defs), recordCount: len(data.Records)}
	info.layouts = map[string]int{}
	for i, def := range types.Defs {
		if def.Type != "_error" {
			continue
		}
		var keys []string
		for _, k := range def.Keys {
			keys = append(keys, k.Name)
		}
		info.layouts[strings.Join(keys, "/")] = i
	}
	dynamicType := 0
	for i, t := range meta.Types {
		switch t.Name {
		case "XLDAPR":
			dynamicType = i + 1
		case "XLRICHVALUE":
			info.richType = i + 1
		}
	}
	var indices []int
	for _, f := range meta.Future {
		if f.Name == "XLDAPR" && len(f.Blocks) == 1 && f.Blocks[0].Dynamic != nil && isTrue(f.Blocks[0].Dynamic.Flag) {
			p.dynamic = dynamicType > 0 && len(meta.Cells) > 0 && meta.Cells[0] == (metadataReference{dynamicType, 0})
		}
		if f.Name == "XLRICHVALUE" {
			for _, b := range f.Blocks {
				if b.Rich == nil {
					return ErrXLSX
				}
				indices = append(indices, b.Rich.Index)
			}
		}
	}
	info.futureCount = len(indices)
	for _, r := range meta.Values {
		var v richError
		if r.Type == info.richType && info.richType > 0 {
			if r.Index < 0 || r.Index >= len(indices) || indices[r.Index] < 0 || indices[r.Index] >= len(data.Records) {
				return ErrXLSX
			}
			rec := data.Records[indices[r.Index]]
			if rec.Structure < 0 || rec.Structure >= len(types.Defs) {
				return ErrXLSX
			}
			def := types.Defs[rec.Structure]
			if def.Type == "_error" {
				if len(rec.Values) != len(def.Keys) {
					return ErrXLSX
				}
				for i, k := range def.Keys {
					n, err := strconv.Atoi(rec.Values[i])
					if err != nil {
						return ErrXLSX
					}
					switch k.Name {
					case "errorType":
						v.kind = n
					case "propagated":
						v.propagated = n != 0
					case "subType":
						v.subtype = n
					case "rwOffset":
						v.row = n
					case "colOffset":
						v.col = n
					}
				}
			}
		}
		info.values = append(info.values, v)
	}
	p.rich = info
	return nil
}

type arrayMetadata struct {
	p           *xlsxPackage
	needDynamic bool
	added       []richError
}

func newArrayMetadata(p *xlsxPackage) *arrayMetadata { return &arrayMetadata{p: p} }

func (m *arrayMetadata) bind(result arrayResult, c arrayCell, propagated bool) int {
	m.needDynamic = m.needDynamic || c.dynamic
	if c.value.Kind != "error" {
		return 0
	}
	var e richError
	switch c.value.Raw {
	case "#SPILL!":
		e = result.failure
		e.kind = 8
	case "#CALC!":
		e.kind, e.subtype = 13, 3
	default:
		return 0
	}
	if propagated {
		e = richError{kind: e.kind, propagated: true}
	}
	base := 0
	if r := m.p.rich; r != nil {
		if i := slices.Index(r.values, e); i >= 0 {
			return i + 1
		}
		base = len(r.values)
	}
	if i := slices.Index(m.added, e); i >= 0 {
		return base + i + 1
	}
	m.added = append(m.added, e)
	return base + len(m.added)
}

// Append to the source element while retaining the namespace, attributes and
// existing children byte for byte. A counted collection is updated together.
func appendPartChildren(ctx context.Context, data []byte, local, name, children string, additional int) ([]byte, error) {
	var ps []xmlPatch
	depth := 0
	found := false
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() != local || name != "" && attribute(t, "", "name") != name {
				return nil
			}
			if found {
				return ErrXLSX
			}
			found = true
			depth = len(e.stack)
			if additional > 0 {
				old, ok := nonnegativeInt(attribute(t, "", "count"))
				if !ok {
					return ErrXLSX
				}
				p, err := setXMLAttribute(data, e.from, e.to, "count", strconv.Itoa(old+additional))
				if err != nil {
					return err
				}
				ps = append(ps, p...)
			}
		case xml.EndElement:
			if depth > 0 && len(e.stack) == depth && e.local() == local {
				if e.from == e.to {
					return ErrXLSX
				}
				ps = append(ps, xmlPatch{e.from, e.from, children})
				depth = 0
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrXLSX
	}
	return applyXML(ctx, data, ps)
}

func (m *arrayMetadata) write(ctx context.Context, updates map[string][]byte) error {
	p := m.p
	if !m.needDynamic && len(m.added) == 0 {
		return nil
	}
	if p.files[sheetMetadataPart] != nil && !p.dynamic && m.needDynamic {
		return fmt.Errorf("%w：动态数组单元格元数据索引", ErrXLSX)
	}
	newParts := map[string]string{}
	read := func(part, initial string) ([]byte, error) {
		if p.files[part] != nil {
			return p.readPart(ctx, part)
		}
		newParts[part] = classifyPart(part).relation
		return []byte(initial), nil
	}
	meta, err := read(sheetMetadataPart, basicArrayMetadata)
	if err != nil {
		return err
	}
	if len(m.added) > 0 {
		info := p.rich
		if info == nil {
			info = &richErrorStore{richType: 2}
		}
		value, err := read(richValuePart, `<rvData xmlns="`+richNS+`" count="0"></rvData>`)
		if err != nil {
			return err
		}
		defs, err := read(richStructurePart, `<rvStructures xmlns="`+richNS+`" count="0"></rvStructures>`)
		if err != nil {
			return err
		}
		if p.files[richTypesPart] == nil {
			newParts[richTypesPart] = classifyPart(richTypesPart).relation
			updates[richTypesPart] = []byte(`<rvTypesInfo xmlns="` + richTypeNS + `"><global><keyFlags><key name="_Self"><flag name="ExcludeFromFile" value="1"/><flag name="ExcludeFromCalcComparison" value="1"/></key></keyFlags></global></rvTypesInfo>`)
		}
		var records, futures, values, structures strings.Builder
		indices := map[string]int{}
		for k, v := range info.layouts {
			indices[k] = v
		}
		addedDefs := 0
		for i, e := range m.added {
			keys, fields := e.fields()
			key := strings.Join(keys, "/")
			index, ok := indices[key]
			if !ok {
				index = info.structures + addedDefs
				addedDefs++
				indices[key] = index
				structures.WriteString(`<s xmlns="` + richNS + `" t="_error">`)
				for _, k := range keys {
					kind := "i"
					if k == "propagated" {
						kind = "b"
					}
					fmt.Fprintf(&structures, `<k n="%s" t="%s"/>`, k, kind)
				}
				structures.WriteString("</s>")
			}
			fmt.Fprintf(&records, `<rv xmlns="%s" s="%d">`, richNS, index)
			for _, v := range fields {
				fmt.Fprintf(&records, "<v>%d</v>", v)
			}
			records.WriteString("</rv>")
			fmt.Fprintf(&futures, `<bk xmlns="%s"><extLst><ext uri="{3e2802c4-a4d2-4d8b-9148-e3be6c30e623}"><rvb xmlns="%s" i="%d"/></ext></extLst></bk>`, mainNS, richNS, info.recordCount+i)
			fmt.Fprintf(&values, `<bk xmlns="%s"><rc t="%d" v="%d"/></bk>`, mainNS, info.richType, info.futureCount+i)
		}
		updates[richStructurePart], err = appendPartChildren(ctx, defs, "rvStructures", "", structures.String(), addedDefs)
		if err != nil {
			return err
		}
		updates[richValuePart], err = appendPartChildren(ctx, value, "rvData", "", records.String(), len(m.added))
		if err != nil {
			return err
		}
		if p.rich == nil {
			meta, err = appendPartChildren(ctx, meta, "metadataTypes", "", richMetadataType, 1)
			if err != nil {
				return err
			}
			var insert []xmlPatch
			err = walkXML(ctx, meta, func(e xmlEvent) error {
				if _, start := e.token.(xml.StartElement); start && e.local() == "cellMetadata" {
					insert = append(insert, xmlPatch{e.from, e.from, fmt.Sprintf(`<futureMetadata xmlns="%s" name="XLRICHVALUE" count="%d">%s</futureMetadata>`, mainNS, len(m.added), futures.String())})
				}
				return nil
			})
			if err != nil {
				return err
			}
			meta, err = applyXML(ctx, meta, insert)
			if err != nil {
				return err
			}
			meta, err = appendPartChildren(ctx, meta, "metadata", "", fmt.Sprintf(`<valueMetadata xmlns="%s" count="%d">%s</valueMetadata>`, mainNS, len(m.added), values.String()), 0)
		} else {
			meta, err = appendPartChildren(ctx, meta, "futureMetadata", "XLRICHVALUE", futures.String(), len(m.added))
			if err == nil {
				meta, err = appendPartChildren(ctx, meta, "valueMetadata", "", values.String(), len(m.added))
			}
		}
		if err != nil {
			return err
		}
	}
	if p.files[sheetMetadataPart] == nil || len(m.added) > 0 {
		updates[sheetMetadataPart] = meta
	}
	if len(newParts) == 0 {
		return nil
	}
	var overrides, links strings.Builder
	base := p.links["xl/workbook.xml"]
	id := 1
	for _, name := range []string{sheetMetadataPart, richValuePart, richStructurePart, richTypesPart} {
		rel, ok := newParts[name]
		if !ok {
			continue
		}
		for base["rId"+strconv.Itoa(id)] != "" {
			id++
		}
		fmt.Fprintf(&links, `<Relationship xmlns="%s" Id="rId%d" Type="%s" Target="%s"/>`, packageNS, id, rel, strings.TrimPrefix(path.Clean(name), "xl/"))
		id++
		fmt.Fprintf(&overrides, `<Override xmlns="%s" PartName="/%s" ContentType="%s"/>`, contentNS, name, classifyPart(name).mime)
	}
	for _, part := range []struct{ file, root, children string }{{"[Content_Types].xml", "Types", overrides.String()}, {"xl/_rels/workbook.xml.rels", "Relationships", links.String()}} {
		raw, ok := updates[part.file]
		if !ok {
			raw, err = p.readPart(ctx, part.file)
			if err != nil {
				return err
			}
		}
		updates[part.file], err = appendPartChildren(ctx, raw, part.root, "", part.children, 0)
		if err != nil {
			return err
		}
	}
	return nil
}
