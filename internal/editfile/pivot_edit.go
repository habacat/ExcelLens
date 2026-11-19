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
	part, sheet         string
	table               pivotTableXML
	xml                 []byte
	data                int
	rows, cols, pages   []int
	selections          map[int][]editdoc.PivotItem
	aggregation, filter string
	keepFilter          bool
	old                 cellRect
	oldPages            []cellRect
}

func (p *xlsxPackage) planPivot(ctx context.Context, part string, fields []cacheFieldXML, schema pivotSchema, change *editdoc.Structure) (pivotPlan, error) {
	t, raw, err := p.readPivot(ctx, part)
	plan := pivotPlan{part: part, sheet: p.pivotSheets[part], table: t, xml: raw, keepFilter: true, selections: map[int][]editdoc.PivotItem{}}
	if err != nil {
		return plan, err
	}
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
	if plan.data < 0 || plan.data >= len(fields) || !slices.Contains([]string{"sum", "count", "average", "min", "max"}, plan.aggregation) {
		return plan, errors.New("透视字段或汇总方式无效")
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
	return plan, nil
}

func (p *xlsxPackage) rewritePivot(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
	target, err := p.pivotPartFor(op.Sheet, op.Pivot)
	if err != nil {
		return opc.Receipt{}, err
	}
	cache := p.pivotOutputs[target]
	fields, definition, err := p.cacheFields(ctx, cache)
	if err != nil {
		return opc.Receipt{}, err
	}
	if err := walkXML(ctx, definition, func(e xmlEvent) error {
		if tag, ok := e.token.(xml.StartElement); ok && tag.Name.Space == mainNS {
			switch tag.Name.Local {
			case "calculatedItems", "calculatedMembers", "fieldGroup", "cacheHierarchies", "dimensions", "measureGroups":
				return errors.New("此缓存含计算项目、分组或数据模型，请在 Excel 中刷新")
			}
		}
		return nil
	}); err != nil {
		return opc.Receipt{}, err
	}
	var recordPart string
	for _, part := range p.links[cache] {
		if classifyPart(part).role == "pivotRecords" {
			recordPart = part
		}
	}
	if recordPart == "" {
		return opc.Receipt{}, errors.New("透视缓存没有已保存的记录部件")
	}
	values, err := newObjectValues(ctx, src, p.layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		if f.Formula != "" {
			return opc.Receipt{}, errors.New("此缓存含计算字段")
		}
		names[i] = f.Name
	}
	schema, err := readPivotSchema(values, p.pivotSources[cache], names)
	if err != nil {
		return opc.Receipt{}, err
	}
	fields, definition, err = schema.cache(ctx, definition, fields)
	if err != nil {
		return opc.Receipt{}, err
	}
	var plans []pivotPlan
	dimensions := map[int]bool{}
	for _, f := range p.zip.File {
		if p.pivotOutputs[f.Name] != cache {
			continue
		}
		var change *editdoc.Structure
		if f.Name == target {
			change = &op
		}
		plan, err := p.planPivot(ctx, f.Name, fields, schema, change)
		if err != nil {
			return opc.Receipt{}, fmt.Errorf("%s：%w", f.Name, err)
		}
		for _, axis := range [][]int{plan.rows, plan.cols, plan.pages} {
			for _, i := range axis {
				dimensions[i] = true
			}
		}
		plans = append(plans, plan)
	}
	records, err := loadPivotRecords(ctx, values, p.pivotSources[cache], fields, dimensions)
	if err != nil {
		return opc.Receipt{}, err
	}
	newDef, err := records.definition(ctx, definition)
	if err != nil {
		return opc.Receipt{}, err
	}
	oldRecords, err := p.readPart(ctx, recordPart)
	if err != nil {
		return opc.Receipt{}, err
	}
	newRecords, err := records.xmlRecords(ctx, oldRecords)
	if err != nil {
		return opc.Receipt{}, err
	}
	updates := map[string][]byte{cache: newDef, recordPart: newRecords}
	edits := map[string]map[editdoc.Address]editdoc.Value{}
	for _, plan := range plans {
		xmlTable, changes, err := plan.render(ctx, records, values)
		if err != nil {
			return opc.Receipt{}, err
		}
		updates[plan.part] = xmlTable
		if edits[plan.sheet] == nil {
			edits[plan.sheet] = map[editdoc.Address]editdoc.Value{}
		}
		for a, v := range changes {
			if _, found := edits[plan.sheet][a]; found {
				return opc.Receipt{}, errors.New("更新后的透视结果区域重叠")
			}
			edits[plan.sheet][a] = v
		}
	}
	for _, sheet := range p.layout.Sheets {
		if len(edits[sheet.Name]) == 0 {
			continue
		}
		data, err := p.pivotSheet(ctx, sheet, edits[sheet.Name], plans)
		if err != nil {
			return opc.Receipt{}, err
		}
		updates[sheet.Part] = data
	}
	if p.files["xl/sharedStrings.xml"] != nil {
		data, err := p.readPart(ctx, "xl/sharedStrings.xml")
		if err != nil {
			return opc.Receipt{}, err
		}
		ps, err := sharedCountPatches(ctx, data)
		if err != nil {
			return opc.Receipt{}, err
		}
		if len(ps) > 0 {
			updates["xl/sharedStrings.xml"], err = applyXML(ctx, data, ps)
			if err != nil {
				return opc.Receipt{}, err
			}
		}
	}
	changes := map[editdoc.Address]editdoc.Value{}
	for _, cells := range edits {
		for a, v := range cells {
			changes[a] = v
		}
	}
	recalculated, err := values.book.Calculate(ctx, changes, nil)
	if err != nil {
		return opc.Receipt{}, err
	}
	current := objectValues{book: values.book, calculation: recalculated, overlay: changes}
	if err := p.refreshDependents(ctx, current, updates); err != nil {
		return opc.Receipt{}, err
	}
	return replaceParts(ctx, dst, p.zip, updates)
}
