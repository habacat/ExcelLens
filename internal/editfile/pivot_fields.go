package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"slices"
	"strconv"

	"excellens/internal/editdoc"
)

type PivotInfo struct {
	Part          string             `json:"part"`
	Name          string             `json:"name"`
	Sheet         string             `json:"sheet"`
	Fields        []string           `json:"fields,omitempty"`
	Row           string             `json:"row,omitempty"`
	Data          string             `json:"data,omitempty"`
	Aggregate     string             `json:"aggregate,omitempty"`
	Axes          *editdoc.PivotAxes `json:"axes,omitempty"`
	SourceSheet   string             `json:"sourceSheet,omitempty"`
	SourceRef     string             `json:"sourceRef,omitempty"`
	Areas         []editdoc.Area     `json:"areas,omitempty"`
	RefreshReason string             `json:"refreshReason,omitempty"`
}

type pivotMember struct {
	XMLName xml.Name
	V       string `xml:"v,attr"`
}

type cacheFieldXML struct {
	Name    string `xml:"name,attr"`
	Formula string `xml:"formula,attr"`
	Shared  *struct {
		Items []pivotMember `xml:",any"`
	} `xml:"sharedItems"`
}

type pivotItemXML struct {
	X      int    `xml:"x,attr"`
	Type   string `xml:"t,attr"`
	Hidden string `xml:"h,attr"`
	Detail string `xml:"sd,attr"`
	Name   string `xml:"n,attr"`
}

type pivotFieldXML struct {
	Name     string         `xml:"name,attr"`
	Items    []pivotItemXML `xml:"items>item"`
	Compact  string         `xml:"compact,attr"`
	Outline  string         `xml:"outline,attr"`
	Top      string         `xml:"subtotalTop,attr"`
	Subtotal string         `xml:"defaultSubtotal,attr"`
	Caption  string         `xml:"subtotalCaption,attr"`
	Blank    string         `xml:"insertBlankRow,attr"`
	Multiple string         `xml:"multipleItemSelectionAllowed,attr"`
	Sort     string         `xml:"sortType,attr"`
	ShowAll  string         `xml:"showAll,attr"`
	AutoSort *struct{}      `xml:"autoSortScope"`
	Repeat   []struct {
		Labels string `xml:"fillDownLabels,attr"`
	} `xml:"extLst>ext>pivotField"`
}

func pivotOn(s string) bool { return s == "" || isTrue(s) }

type pivotAxisField struct {
	X int `xml:"x,attr"`
}
type pivotPageXML struct {
	Field int  `xml:"fld,attr"`
	Item  *int `xml:"item,attr"`
}

type pivotTableXML struct {
	Name           string `xml:"name,attr"`
	DataCaption    string `xml:"dataCaption,attr"`
	GrandCaption   string `xml:"grandTotalCaption,attr"`
	RowCaption     string `xml:"rowHeaderCaption,attr"`
	ColCaption     string `xml:"colHeaderCaption,attr"`
	MissingCaption string `xml:"missingCaption,attr"`
	RowGrand       string `xml:"rowGrandTotals,attr"`
	ColGrand       string `xml:"colGrandTotals,attr"`
	Compact        string `xml:"compact,attr"`
	Over           string `xml:"pageOverThenDown,attr"`
	Wrap           int    `xml:"pageWrap,attr"`
	Headers        string `xml:"showHeaders,attr"`
	MergeLabels    string `xml:"mergeItem,attr"`
	Area           struct {
		Ref      string `xml:"ref,attr"`
		Header   int    `xml:"firstDataRow,attr"`
		Col      int    `xml:"firstDataCol,attr"`
		PageRows int    `xml:"rowPageCount,attr"`
		PageCols int    `xml:"colPageCount,attr"`
	} `xml:"location"`
	RowFields  []pivotAxisField `xml:"rowFields>field"`
	ColFields  []pivotAxisField `xml:"colFields>field"`
	PageFields []pivotPageXML   `xml:"pageFields>pageField"`
	Filters    *struct{}        `xml:"filters"`
	DataFields []struct {
		Fld      int    `xml:"fld,attr"`
		Name     string `xml:"name,attr"`
		Subtotal string `xml:"subtotal,attr"`
		Show     string `xml:"showDataAs,attr"`
	} `xml:"dataFields>dataField"`
	Fields []pivotFieldXML `xml:"pivotFields>pivotField"`
}

func (t pivotTableXML) refreshArea(fields int) (cellRect, error) {
	r, ok := parseRect(t.Area.Ref)
	if !ok || len(t.Fields) != fields {
		return r, ErrXLSX
	}
	if len(t.DataFields) != 1 {
		return r, errors.New("此布局需要一个值字段")
	}
	if t.Filters != nil {
		return r, errors.New("此透视表使用标签或值筛选，可在 Excel 中刷新")
	}
	v := t.DataFields[0]
	if v.Fld < 0 || v.Fld >= fields {
		return r, ErrXLSX
	}
	if v.Show != "" && v.Show != "normal" {
		return r, errors.New("此透视表使用特殊值显示方式，可在 Excel 中刷新")
	}
	if isTrue(t.MergeLabels) {
		return r, errors.New("此透视表使用合并标签，可在 Excel 中刷新")
	}
	seen := map[int]bool{}
	check := func(i int) error {
		if i < 0 || i >= fields || seen[i] {
			return ErrXLSX
		}
		seen[i] = true
		if t.Fields[i].AutoSort != nil {
			return errors.New("此字段按汇总值排序，可在 Excel 中刷新")
		}
		return nil
	}
	for _, axis := range [][]pivotAxisField{t.RowFields, t.ColFields} {
		for _, f := range axis {
			if err := check(f.X); err != nil {
				return r, err
			}
		}
	}
	for _, p := range t.PageFields {
		if err := check(p.Field); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (t pivotTableXML) savedAxes(fields []cacheFieldXML) (*editdoc.PivotAxes, error) {
	axes := &editdoc.PivotAxes{}
	for _, f := range t.RowFields {
		axes.Rows = append(axes.Rows, fields[f.X].Name)
	}
	for _, f := range t.ColFields {
		axes.Columns = append(axes.Columns, fields[f.X].Name)
	}
	for _, p := range t.PageFields {
		field := t.Fields[p.Field]
		page := editdoc.PivotPage{Field: fields[p.Field].Name}
		chosen := func(pos int) error {
			if pos < 0 || pos >= len(field.Items) {
				return ErrXLSX
			}
			x := field.Items[pos].X
			shared := fields[p.Field].Shared
			if shared == nil || x < 0 || x >= len(shared.Items) {
				return ErrXLSX
			}
			m := shared.Items[x]
			v, err := cacheMember(m)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, v.choice())
			return nil
		}
		if p.Item != nil {
			if err := chosen(*p.Item); err != nil {
				return nil, err
			}
		} else if isTrue(field.Multiple) && slices.ContainsFunc(field.Items, func(i pivotItemXML) bool { return isTrue(i.Hidden) }) {
			for pos, it := range field.Items {
				if (it.Type == "" || it.Type == "data") && !isTrue(it.Hidden) {
					if err := chosen(pos); err != nil {
						return nil, err
					}
				}
			}
		}
		axes.Pages = append(axes.Pages, page)
	}
	return axes, nil
}

func cacheMember(m pivotMember) (pivotValue, error) {
	v := pivotValue{m.XMLName.Local, m.V}
	if v.kind == "d" {
		t, err := editdoc.ISODateTime(v.raw)
		if err != nil {
			return v, err
		}
		v.raw = t.Format(pivotDateLayout)
	}
	if v.kind == "b" {
		if !ruleBool(v.raw) {
			return v, ErrXLSX
		}
		if isTrue(v.raw) {
			v.raw = "1"
		} else {
			v.raw = "0"
		}
	}
	if v.kind == "n" {
		n, err := strconv.ParseFloat(v.raw, 64)
		if err != nil {
			return v, err
		}
		v.raw = strconv.FormatFloat(n, 'g', -1, 64)
	}
	return v, nil
}
func (v pivotValue) choice() editdoc.PivotItem { return editdoc.PivotItem{Type: v.kind, Value: v.raw} }

func (p *xlsxPackage) readPivot(ctx context.Context, part string) (pivotTableXML, []byte, error) {
	var table pivotTableXML
	data, err := p.readPart(ctx, part)
	if err == nil {
		err = xml.Unmarshal(data, &table)
	}
	return table, data, err
}
func (p *xlsxPackage) cacheFields(ctx context.Context, part string) ([]cacheFieldXML, []byte, error) {
	var cache struct {
		Fields []cacheFieldXML `xml:"cacheFields>cacheField"`
	}
	data, err := p.readPart(ctx, part)
	if err == nil {
		err = xml.Unmarshal(data, &cache)
	}
	return cache.Fields, data, err
}

func (p *xlsxPackage) inspectPivotFields(ctx context.Context) error {
	caches := map[string][]cacheFieldXML{}
	for _, part := range p.zip.File {
		cache := p.pivotOutputs[part.Name]
		if cache == "" {
			continue
		}
		fields, ok := caches[cache]
		if !ok {
			var err error
			fields, _, err = p.cacheFields(ctx, cache)
			if err != nil {
				return err
			}
			caches[cache] = fields
		}
		t, _, err := p.readPivot(ctx, part.Name)
		if err != nil {
			return err
		}
		info := PivotInfo{Part: part.Name, Name: t.Name, Sheet: p.pivotSheets[part.Name]}
		if body, ok := parseRect(t.Area.Ref); ok {
			pages, _ := t.oldPageAreas(body)
			for _, area := range append(pages, body) {
				info.Areas = append(info.Areas, editdoc.Area{Start: editdoc.Address{Sheet: info.Sheet, Row: area.firstRow, Col: area.firstCol}, EndRow: area.lastRow, EndCol: area.lastCol})
			}
		}
		src := p.pivotSources[cache]
		info.SourceSheet = src.sheet
		info.SourceRef = pivotRectRef(src.area)
		for _, f := range fields {
			info.Fields = append(info.Fields, f.Name)
		}
		if _, err = t.refreshArea(len(fields)); err == nil {
			info.Axes, err = t.savedAxes(fields)
		}
		if err != nil {
			info.RefreshReason = err.Error()
		} else if !p.pivotSources[cache].refreshable {
			info.RefreshReason = "此工作簿已禁用该缓存的刷新"
		}
		if info.Axes != nil {
			if len(info.Axes.Rows) > 0 {
				info.Row = info.Axes.Rows[0]
			}
			d := t.DataFields[0]
			info.Data = fields[d.Fld].Name
			info.Aggregate = d.Subtotal
			if info.Aggregate == "" {
				info.Aggregate = "sum"
			}
		}
		p.layout.Pivots = append(p.layout.Pivots, info)
	}
	return nil
}
func (p *xlsxPackage) pivotPartFor(sheet, name string) (string, error) {
	for _, i := range p.layout.Pivots {
		if i.Sheet == sheet && i.Name == name {
			return i.Part, nil
		}
	}
	return "", errors.New("当前工作表没有选定的数据透视表")
}
func findField(fields []cacheFieldXML, name string) int {
	return slices.IndexFunc(fields, func(f cacheFieldXML) bool { return f.Name == name })
}
