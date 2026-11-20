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
	DateFilters     []editdoc.PivotDateFilter     `json:"dateFilters,omitempty"`
	GroupFields     map[string]PivotGroupField    `json:"groupFields,omitempty"`
	Measures        []editdoc.PivotMeasure        `json:"measures,omitempty"`
	ValuesAxis      *editdoc.PivotValuesAxis      `json:"valuesAxis,omitempty"`
	LabelFilters    []editdoc.PivotLabelFilter    `json:"labelFilters,omitempty"`
	DateGroups      []editdoc.PivotDateGroup      `json:"dateGroups,omitempty"`
	DateFields      []string                      `json:"dateFields,omitempty"`
	Part            string                        `json:"part"`
	Name            string                        `json:"name"`
	Sheet           string                        `json:"sheet"`
	Fields          []string                      `json:"fields,omitempty"`
	Row             string                        `json:"row,omitempty"`
	Data            string                        `json:"data,omitempty"`
	Aggregate       string                        `json:"aggregate,omitempty"`
	Axes            *editdoc.PivotAxes            `json:"axes,omitempty"`
	SourceSheet     string                        `json:"sourceSheet,omitempty"`
	SourceRef       string                        `json:"sourceRef,omitempty"`
	Areas           []editdoc.Area                `json:"areas,omitempty"`
	CalculatedItems []editdoc.PivotCalculatedItem `json:"calculatedItems,omitempty"`
	ValueFilters    []editdoc.PivotValueFilter    `json:"valueFilters,omitempty"`
	RefreshReason   string                        `json:"refreshReason,omitempty"`
}

type pivotMember struct {
	XMLName    xml.Name
	V          string `xml:"v,attr"`
	Calculated string `xml:"f,attr"`
}

type cacheFieldXML struct {
	Database string          `xml:"databaseField,attr"`
	Group    *cacheDateGroup `xml:"fieldGroup"`
	Name     string          `xml:"name,attr"`
	Formula  string          `xml:"formula,attr"`
	Shared   *struct {
		Date  string        `xml:"containsDate,attr"`
		Items []pivotMember `xml:",any"`
	} `xml:"sharedItems"`
}

type pivotItemXML struct {
	X          int    `xml:"x,attr"`
	Type       string `xml:"t,attr"`
	Hidden     string `xml:"h,attr"`
	Detail     string `xml:"sd,attr"`
	Calculated string `xml:"f,attr"`
	Name       string `xml:"n,attr"`
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

type pivotDataField struct {
	Fld      int    `xml:"fld,attr"`
	Name     string `xml:"name,attr"`
	Subtotal string `xml:"subtotal,attr"`
	Show     string `xml:"showDataAs,attr"`
	Number   string `xml:"numFmtId,attr"`
}

type pivotTableXML struct {
	Name            string `xml:"name,attr"`
	MultipleFilters string `xml:"multipleFieldFilters,attr"`
	DataCaption     string `xml:"dataCaption,attr"`
	GrandCaption    string `xml:"grandTotalCaption,attr"`
	RowCaption      string `xml:"rowHeaderCaption,attr"`
	ColCaption      string `xml:"colHeaderCaption,attr"`
	MissingCaption  string `xml:"missingCaption,attr"`
	RowGrand        string `xml:"rowGrandTotals,attr"`
	ColGrand        string `xml:"colGrandTotals,attr"`
	CustomListSort  string `xml:"customListSort,attr"`
	Compact         string `xml:"compact,attr"`
	Over            string `xml:"pageOverThenDown,attr"`
	Wrap            int    `xml:"pageWrap,attr"`
	Headers         string `xml:"showHeaders,attr"`
	MergeLabels     string `xml:"mergeItem,attr"`
	Area            struct {
		Ref      string `xml:"ref,attr"`
		Header   int    `xml:"firstDataRow,attr"`
		Col      int    `xml:"firstDataCol,attr"`
		PageRows int    `xml:"rowPageCount,attr"`
		PageCols int    `xml:"colPageCount,attr"`
	} `xml:"location"`
	RowFields  []pivotAxisField `xml:"rowFields>field"`
	ColFields  []pivotAxisField `xml:"colFields>field"`
	PageFields []pivotPageXML   `xml:"pageFields>pageField"`
	Filters    []pivotFilterXML `xml:"filters>filter"`
	DataFields []pivotDataField `xml:"dataFields>dataField"`
	Fields     []pivotFieldXML  `xml:"pivotFields>pivotField"`
}

func (t pivotTableXML) refreshArea(fields int) (cellRect, error) {
	r, ok := parseRect(t.Area.Ref)
	if !ok || len(t.Fields) != fields {
		return r, ErrXLSX
	}
	if len(t.DataFields) > 256 {
		return r, errors.New("值字段数超出 Excel 上限")
	}
	if len(t.DataFields) == 0 {
		return r, errors.New("此布局没有值字段")
	}
	if _, err := t.valueFilters(fields); err != nil {
		return r, err
	}
	if _, err := t.labels(fields); err != nil {
		return r, err
	}
	for _, v := range t.DataFields {
		if !validMeasureMethod(v.method()) {
			return r, errors.New("此值字段的汇总方式不受支持")
		}
		if v.Fld < 0 || v.Fld >= fields {
			return r, ErrXLSX
		}
		if v.Show != "" && v.Show != "normal" {
			return r, errors.New("此透视表使用特殊值显示方式，可在 Excel 中刷新")
		}
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
			if f.X == pivotValuesField {
				if seen[f.X] {
					return r, ErrXLSX
				}
				seen[f.X] = true
				continue
			}
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
		if f.X == pivotValuesField {
			continue
		}
		axes.Rows = append(axes.Rows, fields[f.X].Name)
	}
	for _, f := range t.ColFields {
		if f.X == pivotValuesField {
			continue
		}
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
	itemErrors := map[string]error{}
	itemsByCache := map[string][]editdoc.PivotCalculatedItem{}
	for _, part := range p.zip.File {
		cache := p.pivotOutputs[part.Name]
		if cache == "" {
			continue
		}
		fields, ok := caches[cache]
		if !ok {
			var err error
			var raw []byte
			fields, raw, err = p.cacheFields(ctx, cache)
			if err != nil {
				return err
			}
			items, readErr := readCalculatedDefinitions(raw)
			itemErrors[cache] = readErr
			if readErr == nil {
				for _, it := range items {
					if it.field < 0 || it.field >= len(fields) {
						return ErrXLSX
					}
					f := fields[it.field]
					if f.Shared == nil || it.index < 0 || it.index >= len(f.Shared.Items) {
						return ErrXLSX
					}
					itemsByCache[cache] = append(itemsByCache[cache], editdoc.PivotCalculatedItem{Field: f.Name, Name: f.Shared.Items[it.index].V, Formula: "=" + it.formula})
				}
			}
			caches[cache] = fields
		}
		t, _, err := p.readPivot(ctx, part.Name)
		if err != nil {
			return err
		}
		info := PivotInfo{CalculatedItems: itemsByCache[cache], Part: part.Name, Name: t.Name, Sheet: p.pivotSheets[part.Name]}
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
		info.dateInfo(fields, t.DataCaption == "Values")
		if _, err = t.refreshArea(len(fields)); err == nil {
			info.Axes, err = t.savedAxes(fields)
		}
		if err == nil && itemErrors[cache] != nil {
			err = itemErrors[cache]
		}
		if err != nil {
			info.RefreshReason = err.Error()
		} else if !p.pivotSources[cache].refreshable {
			info.RefreshReason = "此工作簿已禁用该缓存的刷新"
		}
		if info.Axes != nil {
			dates, dateErr := t.dateFilters(len(fields), p.date1904)
			if dateErr != nil {
				info.RefreshReason = dateErr.Error()
			}
			for _, f := range dates {
				s := f.spec
				s.Field = fields[f.field].Name
				info.DateFilters = append(info.DateFilters, s)
			}
			info.Measures = t.measureInfo(fields)
			info.ValuesAxis = t.valuesAxis()
			labels, _ := t.labels(len(fields))
			for _, f := range labels {
				s := f.spec
				s.Field = fields[f.field].Name
				info.LabelFilters = append(info.LabelFilters, s)
			}
			filters, _ := t.valueFilters(len(fields))
			for _, f := range filters {
				cfg := f.spec
				cfg.Field = fields[f.field].Name
				info.ValueFilters = append(info.ValueFilters, cfg)
			}
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
