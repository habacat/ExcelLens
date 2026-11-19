package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

type pivotCalculated struct {
	field, index int
	formula      string
	expr         itemExpression
}

func readCalculatedDefinitions(raw []byte) ([]pivotCalculated, error) {
	var cache struct {
		Items []struct {
			Formula string `xml:"formula,attr"`
			Area    struct {
				Cache      string `xml:"cacheIndex,attr"`
				References []struct {
					Field *int `xml:"field,attr"`
					Items []struct {
						Index int `xml:"v,attr"`
					} `xml:"x"`
				} `xml:"references>reference"`
			} `xml:"pivotArea"`
		} `xml:"calculatedItems>calculatedItem"`
	}
	if err := xml.Unmarshal(raw, &cache); err != nil {
		return nil, err
	}
	var items []pivotCalculated
	for _, it := range cache.Items {
		refs := it.Area.References
		if len(refs) != 1 || refs[0].Field == nil || len(refs[0].Items) != 1 || !isTrue(it.Area.Cache) || it.Formula == "" {
			return nil, errors.New("此计算项目涉及多个区域或未绑定缓存项目")
		}
		items = append(items, pivotCalculated{field: *refs[0].Field, index: refs[0].Items[0].Index, formula: it.Formula})
	}
	return items, nil
}

func (r *pivotRecords) configureCalculatedItems(ctx context.Context, raw []byte, fields []cacheFieldXML, schema pivotSchema, op *editdoc.Structure) ([]byte, error) {
	items, err := readCalculatedDefinitions(raw)
	if err != nil {
		return nil, err
	}
	changed := false
	for i := range items {
		x := &items[i]
		old := x.field
		if schema.changed {
			x.field, err = schema.index(old)
			if err != nil {
				return nil, err
			}
			changed = changed || old != x.field
		}
		if x.field < 0 || x.field >= len(fields) || x.index < 0 || x.index >= len(r.dicts[x.field].values) || !r.dicts[x.field].calculated[x.index] {
			return nil, errors.New("计算项目的字段或缓存成员已失效")
		}
	}
	if op != nil && op.Kind == "pivotCalculatedItem" {
		input := op.CalculatedItem
		if input == nil {
			return nil, ErrTarget
		}
		f := findField(fields, input.Field)
		if f < 0 || r.dicts[f].index == nil || strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 255 || validateXLSXText(input.Name) != nil {
			return nil, errors.New("请选择轴字段并填写有效的计算项目名称")
		}
		d := &r.dicts[f]
		member := pivotValue{"s", input.Name}
		index, found := d.index[member]
		if !found {
			for _, old := range d.values {
				if strings.EqualFold(old.raw, input.Name) {
					return nil, errors.New("计算项目名称与已有项目重复")
				}
			}
			if input.Formula == "" {
				return nil, errors.New("找不到要移除的计算项目")
			}
			index = d.add(member)
			if d.calculated == nil {
				d.calculated = map[int]bool{}
			}
			d.calculated[index] = true
			items = append(items, pivotCalculated{field: f, index: index, formula: input.Formula})
		} else {
			id := slices.IndexFunc(items, func(it pivotCalculated) bool { return it.field == f && it.index == index })
			if id < 0 {
				return nil, errors.New("项目名称已由源数据使用")
			}
			if input.Formula == "" {
				items = slices.Delete(items, id, id+1)
				for _, it := range items {
					if it.field != f {
						continue
					}
					program, err := compileItemFormula(ctx, it.formula, fields[f].Name, *d)
					if err != nil {
						return nil, err
					}
					if slices.Contains(program.refs, index) {
						return nil, errors.New("其他计算项目仍引用此项目")
					}
				}
				d.values = slices.Delete(d.values, index, index+1)
				d.index = map[pivotValue]int{}
				flags := map[int]bool{}
				for x, v := range d.values {
					d.index[v] = x
					old := x
					if old >= index {
						old++
					}
					if d.calculated[old] {
						flags[x] = true
					}
				}
				d.calculated = flags
				for k := range items {
					if items[k].field == f && items[k].index > index {
						items[k].index--
					}
				}
				r.removedItems = map[int]int{f: index}
			} else {
				items[id].formula = input.Formula
			}
		}
		changed = true
	}
	for i := range items {
		it := &items[i]
		it.expr, err = compileItemFormula(ctx, it.formula, fields[it.field].Name, r.dicts[it.field])
		if err != nil {
			return nil, err
		}
	}
	r.items, err = orderCalculated(items)
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	var body strings.Builder
	if len(items) > 0 {
		fmt.Fprintf(&body, `<calculatedItems xmlns="%s" count="%d">`, mainNS, len(items))
		for _, it := range items {
			fmt.Fprintf(&body, `<calculatedItem formula="%s"><pivotArea cacheIndex="1" outline="0" fieldPosition="0"><references count="1"><reference field="%d" count="1"><x v="%d"/></reference></references></pivotArea></calculatedItem>`, escapedXML(it.expr.stored), it.field, it.index)
		}
		body.WriteString("</calculatedItems>")
	}
	return replacePivotChild(ctx, raw, "calculatedItems", []string{"calculatedMembers", "dimensions", "measureGroups", "maps", "extLst"}, body.String())
}

func orderCalculated(items []pivotCalculated) ([]pivotCalculated, error) {
	if len(items) > formats.EditBatchCells {
		return nil, ErrCalcCapacity
	}
	key := func(it pivotCalculated) [2]int { return [2]int{it.field, it.index} }
	byKey := make(map[[2]int]int, len(items))
	for i, it := range items {
		if _, ok := byKey[key(it)]; ok {
			return nil, ErrXLSX
		}
		byKey[key(it)] = i
	}
	state := make([]byte, len(items))
	var ordered []pivotCalculated
	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return errors.New("计算项目之间存在循环引用")
		case 2:
			return nil
		}
		state[i] = 1
		it := items[i]
		for _, ref := range it.expr.refs {
			if dep, ok := byKey[[2]int{it.field, ref}]; ok {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		state[i] = 2
		ordered = append(ordered, it)
		return nil
	}
	for i := range items {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

type calculatedGroup struct {
	row     []pivotValue
	members map[int]*pivotAggregate
}

// A calculated item is evaluated for each combination of the other visible
// axis fields. For example, a product's grand total sums the products of the
// individual months. Source records and the serialized cache stay untouched.
func (p pivotPlan) calculatedRows(ctx context.Context, r pivotRecords, dims map[int]*pivotDimension) ([][]pivotValue, error) {
	if len(r.items) == 0 {
		return r.rows, nil
	}
	if p.aggregation == "average" {
		return nil, errors.New("Excel 不支持含计算项目的平均值汇总")
	}
	records := slices.Clone(r.rows)
	for _, field := range append(slices.Clone(p.rows), p.cols...) {
		var items []pivotCalculated
		for _, it := range r.items {
			if it.field == field {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			continue
		}
		axes := append(slices.Clone(p.rows), p.cols...)
		groups := map[string]*calculatedGroup{}
		var order []string
		path := make([]int, 0, len(axes))
		for i, row := range records {
			if i%512 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if !pivotPageVisible(row, r, dims) {
				continue
			}
			path = path[:0]
			for _, axis := range axes {
				if axis != field {
					path = append(path, r.dicts[axis].index[row[axis]])
				}
			}
			key := filterPath(path)
			g := groups[key]
			if g == nil {
				g = &calculatedGroup{row: row, members: map[int]*pivotAggregate{}}
				groups[key] = g
				order = append(order, key)
			}
			x := r.dicts[field].index[row[field]]
			if g.members[x] == nil {
				g.members[x] = &pivotAggregate{}
			}
			val, err := pivotMeasure(row[p.data], r)
			if err != nil {
				return nil, err
			}
			g.members[x].addMeasure(val, len(row) > len(r.fields))
		}
		if int64(len(records))+int64(len(items))*int64(len(groups)) > int64(formats.EditCalcSourceCells)/int64(len(r.fields)) {
			return nil, ErrCalcCapacity
		}
		for _, key := range order {
			g := groups[key]
			inputs := map[int]calculator.FormulaValue{}
			for index, agg := range g.members {
				v, err := calcOperand(agg.value(p.aggregation), false)
				if err != nil {
					return nil, err
				}
				inputs[index] = v
			}
			for _, it := range items {
				v, err := it.expr.evaluate(ctx, inputs)
				if err != nil {
					return nil, err
				}
				out := editdoc.Value{Kind: "number", Raw: v.raw}
				if v.kind == "e" {
					out.Kind = "error"
				}
				inputs[it.index], err = calcOperand(out, false)
				if err != nil {
					return nil, err
				}
				line := slices.Clone(g.row)
				line[field] = r.dicts[field].values[it.index]
				if p.data == field {
					return nil, errors.New("计算项目字段同时用作值字段")
				}
				line[p.data] = v
				// The extra slot marks an already aggregated calculated measure;
				// count must sum its numeric value instead of counting it as 1.
				line = append(line[:len(r.fields)], pivotValue{kind: "calculated", raw: strconv.Itoa(field)})
				records = append(records, line)
			}
		}
	}
	return records, nil
}

func pivotPageVisible(row []pivotValue, r pivotRecords, dims map[int]*pivotDimension) bool {
	for f, d := range dims {
		if d.page {
			pos := d.positions[r.dicts[f].index[row[f]]]
			if isTrue(d.items[pos].Hidden) || len(d.pagePositions) == 1 && d.pagePositions[0] != pos {
				return false
			}
		}
	}
	return true
}
