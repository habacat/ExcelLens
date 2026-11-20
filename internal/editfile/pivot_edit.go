package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"

	"excellens/internal/editdoc"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

type pivotPlan struct {
	measures            []pivotDataField
	measureOrigins      []int
	formats             pivotNumberFormats
	valuesAxis          *editdoc.PivotValuesAxis
	labelFilters        []pivotLabel
	dates               []pivotDateFilter
	date1904            bool
	chart               *pivotChartData
	part, sheet         string
	table               pivotTableXML
	xml                 []byte
	data                int
	rows, cols, pages   []int
	selections          map[int][]editdoc.PivotItem
	aggregation, filter string
	valueFilters        []pivotFilter
	keepFilter          bool
	old                 cellRect
	oldPages            []cellRect
}

func (p *xlsxPackage) planPivot(ctx context.Context, part string, fields []cacheFieldXML, schema pivotSchema, dates pivotCalendarLayout, change *editdoc.Structure) (pivotPlan, error) {
	t, raw, err := p.readPivot(ctx, part)
	plan := pivotPlan{part: part, sheet: p.pivotSheets[part], table: t, xml: raw, keepFilter: true, selections: map[int][]editdoc.PivotItem{}}
	if err != nil {
		return plan, err
	}
	original := t
	plan.old, err = t.refreshArea(len(schema.newForOld))
	if err != nil {
		return plan, err
	}
	plan.oldPages, err = t.oldPageAreas(plan.old)
	if err != nil {
		return plan, err
	}
	if schema.changed {
		plan.xml, err = schema.table(ctx, raw)
		if err != nil {
			return plan, err
		}
		t = pivotTableXML{}
		if err = xml.Unmarshal(plan.xml, &t); err != nil {
			return plan, err
		}
		if _, err = t.refreshArea(len(fields)); err != nil {
			return plan, err
		}
		plan.table = t
	}
	axes, err := t.savedAxes(fields)
	if err != nil {
		return plan, err
	}
	plan.valuesAxis = t.valuesAxis()
	if d := dates.replacement; d != nil {
		onTarget := change != nil && change.Kind == "pivotDateGroup"
		var row, col bool
		var rowPosition, colPosition int
		axes.Rows, row, rowPosition = d.axis(original.RowFields, schema, onTarget)
		axes.Columns, col, colPosition = d.axis(original.ColFields, schema, onTarget)
		if plan.valuesAxis != nil {
			plan.valuesAxis.Position = max(rowPosition, colPosition)
		}
		if onTarget && !row && !col {
			return plan, errors.New("请先把日期字段放入行或列区域")
		}
	}
	v := t.DataFields[0]
	plan.data, plan.aggregation = v.Fld, v.Subtotal
	if plan.aggregation == "" {
		plan.aggregation = "sum"
	}
	if change != nil && change.Kind == "configurePivot" {
		if change.Axes != nil {
			axes = change.Axes
		} else {
			// A 1.8.29 request describes a single row axis.
			axes = &editdoc.PivotAxes{Rows: []string{change.Rows}}
			if change.Columns != "" {
				axes.Columns = []string{change.Columns}
			}
		}
		plan.data, plan.aggregation = findField(fields, change.Data), change.Aggregate
		plan.filter = change.Item
		switch change.FilterMode {
		case "":
			plan.keepFilter = change.Item == ""
		case "keep", "all":
			if change.Item != "" {
				return plan, errors.New("筛选方式与指定项目不一致")
			}
			plan.keepFilter = change.FilterMode == "keep"
		case "item":
			if change.Item == "" {
				return plan, errors.New("请填写要显示的行项目")
			}
			plan.keepFilter = false
		default:
			return plan, errors.New("透视筛选方式无效")
		}
		if (plan.filter != "" || change.Filter != "") && (len(axes.Rows) == 0 || change.Filter != "" && change.Filter != axes.Rows[0]) {
			return plan, errors.New("请选择用于行项目筛选的首个行字段")
		}
	}
	if err = plan.configureMeasures(fields, change, axes); err != nil {
		return plan, err
	}
	seen := map[int]bool{}
	add := func(name string, dest *[]int) error {
		i := findField(fields, name)
		if i < 0 || seen[i] {
			return fmt.Errorf("透视布局包含未知或重复字段 %q", name)
		}
		if t.Fields[i].AutoSort != nil {
			return errors.New("此字段包含基于值的排序")
		}
		seen[i] = true
		*dest = append(*dest, i)
		return nil
	}
	for _, name := range axes.Rows {
		if err := add(name, &plan.rows); err != nil {
			return plan, err
		}
	}
	for _, name := range axes.Columns {
		if err := add(name, &plan.cols); err != nil {
			return plan, err
		}
	}
	for _, page := range axes.Pages {
		if err := add(page.Field, &plan.pages); err != nil {
			return plan, err
		}
		plan.selections[plan.pages[len(plan.pages)-1]] = page.Items
	}
	plan.valueFilters, err = t.valueFilters(len(fields))
	if err != nil {
		return plan, err
	}
	if err = plan.configureValueFilter(fields, change); err != nil {
		return plan, err
	}
	if err = plan.configureLabels(fields, change); err != nil {
		return plan, err
	}
	plan.date1904 = p.date1904
	err = plan.configureDates(fields, dates.levels, change, p.pivotAsOf)
	return plan, err
}

func (p *xlsxPackage) rewritePivot(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
	target, err := p.pivotPartFor(op.Sheet, op.Pivot)
	if err != nil {
		return opc.Receipt{}, err
	}
	values, err := newObjectValues(ctx, src, p.layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	return p.refreshPivotCaches(ctx, dst, target, &op, values)
}

type pivotCacheChanges struct {
	cells  map[editdoc.Address]editdoc.Value
	plans  []pivotPlan
	charts []*pivotChartData
}

func (p *xlsxPackage) renderPivotCache(ctx context.Context, cache, target string, op *editdoc.Structure, values objectValues, updates map[string][]byte, chartKeys map[string]bool) (*pivotCacheChanges, error) {
	fields, definition, err := p.cacheFields(ctx, cache)
	if err != nil {
		return nil, err
	}
	if err := walkXML(ctx, definition, func(e xmlEvent) error {
		if tag, ok := e.token.(xml.StartElement); ok && tag.Name.Space == mainNS {
			switch tag.Name.Local {
			case "calculatedMembers", "cacheHierarchies", "dimensions", "measureGroups":
				return errors.New("此缓存含计算项目、分组或数据模型，请在 Excel 中刷新")
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var recordPart string
	for _, part := range p.links[cache] {
		if classifyPart(part).role == "pivotRecords" {
			recordPart = part
		}
	}
	if recordPart == "" {
		return nil, errors.New("透视缓存没有已保存的记录部件")
	}
	for _, f := range fields {
		if f.Formula != "" {
			return nil, errors.New("此缓存含计算字段")
		}
	}
	var requested *editdoc.PivotDateGroup
	english := false
	for _, item := range p.layout.Pivots {
		if p.pivotOutputs[item.Part] == cache {
			table, _, e := p.readPivot(ctx, item.Part)
			if e != nil {
				return nil, e
			}
			english = table.DataCaption == "Values"
			break
		}
	}
	if op != nil && op.Kind == "pivotDateGroup" && p.pivotOutputs[target] == cache {
		requested = op.DateGroup
		if requested == nil {
			return nil, ErrTarget
		}
	}
	schema, dates, err := planDateSchema(values, p.pivotSources[cache], fields, requested, english)
	if err != nil {
		return nil, err
	}
	fields, definition, err = schema.cache(ctx, definition, fields)
	if err != nil {
		return nil, err
	}
	var plans []pivotPlan
	dimensions := map[int]bool{}
	for _, f := range p.zip.File {
		if p.pivotOutputs[f.Name] != cache {
			continue
		}
		var change *editdoc.Structure
		if f.Name == target {
			change = op
		}
		plan, err := p.planPivot(ctx, f.Name, fields, schema, dates, change)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", f.Name, err)
		}
		if change != nil && change.Kind == "pivotCalculatedItem" {
			if change.CalculatedItem == nil {
				return nil, ErrTarget
			}
			index := findField(fields, change.CalculatedItem.Field)
			if !slices.Contains(plan.rows, index) && !slices.Contains(plan.cols, index) {
				return nil, errors.New("请选择行或列字段创建计算项目")
			}
		}
		for _, axis := range [][]int{plan.rows, plan.cols, plan.pages} {
			for _, i := range axis {
				dimensions[i] = true
			}
		}
		plans = append(plans, plan)
	}
	for _, g := range dates.levels {
		dimensions[g.base] = true
	}
	records, err := loadPivotRecords(ctx, values, p.pivotSources[cache], fields[:dates.physical], dimensions)
	if err != nil {
		return nil, err
	}
	definition, err = records.configureCalculatedItems(ctx, definition, fields, schema, op)
	if err != nil {
		return nil, err
	}
	newDef, err := records.definition(ctx, definition)
	if err != nil {
		return nil, err
	}
	oldRecords, err := p.readPart(ctx, recordPart)
	if err != nil {
		return nil, err
	}
	newRecords, err := records.xmlRecords(ctx, oldRecords)
	if err != nil {
		return nil, err
	}
	newDef, err = dates.project(ctx, newDef, fields, &records)
	if err != nil {
		return nil, err
	}
	updates[cache], updates[recordPart] = newDef, newRecords
	final := &pivotCacheChanges{cells: map[editdoc.Address]editdoc.Value{}, plans: plans}
	for i := range plans {
		plan := &plans[i]
		plan.formats, err = measureFormats(plan.table.DataFields, plan.measures)
		if err != nil {
			return nil, err
		}
		if chartKeys[chartKey(plan.sheet, plan.table.Name)] {
			plan.chart = new(pivotChartData)
		}
		xmlTable, changes, err := plan.render(ctx, records, values)
		if err != nil {
			return nil, err
		}
		updates[plan.part] = xmlTable
		if plan.chart != nil {
			final.charts = append(final.charts, plan.chart)
		}
		for a, v := range changes {
			if _, found := final.cells[a]; found {
				return nil, errors.New("更新后的透视结果区域重叠")
			}
			final.cells[a] = v
		}
	}
	return final, nil
}
