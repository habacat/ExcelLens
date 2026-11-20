package editfile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"excellens/internal/formats"
)

type pivotNode struct {
	parent     *pivotNode
	pos, depth int
	children   map[int]*pivotNode
}

func (n *pivotNode) child(pos int) *pivotNode {
	if n.children == nil {
		n.children = map[int]*pivotNode{}
	}
	if c := n.children[pos]; c != nil {
		return c
	}
	c := &pivotNode{parent: n, pos: pos, depth: n.depth + 1}
	n.children[pos] = c
	return c
}
func (n *pivotNode) path() []int {
	path := make([]int, n.depth)
	for n.parent != nil {
		path[n.depth-1] = n.pos
		n = n.parent
	}
	return path
}

type pivotLine struct {
	node    *pivotNode
	kind    string
	values  bool
	measure int
}
type pivotGroup struct {
	row, col *pivotNode
	measure  int
}
type pivotCube struct {
	rowRoot, colRoot *pivotNode
	groups           map[pivotGroup]*pivotAggregate
	dims             map[int]*pivotDimension
}

func (p pivotPlan) cube(ctx context.Context, r pivotRecords) (pivotCube, error) {
	dims, err := p.dimensions(r)
	c := pivotCube{&pivotNode{}, &pivotNode{}, map[pivotGroup]*pivotAggregate{}, dims}
	if err != nil {
		return c, err
	}
	measures := p.measures
	if len(measures) == 0 {
		measures = []pivotDataField{{Fld: p.data, Subtotal: p.aggregation}}
	}
	rowAxis, colAxis := p.displayAxes()
	work := int64(len(r.rows)) * int64(len(rowAxis)+1) * int64(len(colAxis)+1) * int64(len(measures))
	if work > formats.EditCalcVisits {
		return c, ErrCalcCapacity
	}
	if len(measures) > 1 {
		d := &pivotDimension{name: p.table.DataCaption, field: pivotFieldXML{Subtotal: "0", ShowAll: "0", Compact: p.table.Compact}}
		for i, m := range measures {
			d.items = append(d.items, pivotItemXML{X: i})
			d.values = append(d.values, pivotValue{"s", m.Name})
		}
		c.dims[pivotValuesField] = d
	}
	descend := func(n *pivotNode, axis []int, record []pivotValue, measure int) *pivotNode {
		for _, f := range axis {
			pos := measure
			if f != pivotValuesField {
				pos = dims[f].positions[r.dicts[f].index[record[f]]]
			}
			n = n.child(pos)
		}
		return n
	}
	rows, err := p.filteredRows(ctx, r, dims, func(record []pivotValue) {
		for i := range measures {
			descend(c.rowRoot, rowAxis, record, i)
			descend(c.colRoot, colAxis, record, i)
		}
	})
	if err != nil {
		return c, err
	}
	for _, record := range rows {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		for idx, m := range measures {
			row := descend(c.rowRoot, rowAxis, record, idx)
			col := descend(c.colRoot, colAxis, record, idx)
			value, err := measureValue(record, r, m, idx)
			if err != nil {
				return c, err
			}
			for i := row; i != nil; i = i.parent {
				for j := col; j != nil; j = j.parent {
					key := pivotGroup{i, j, idx}
					g := c.groups[key]
					if g == nil {
						if len(c.groups) >= formats.EditCalcSourceCells {
							return c, ErrCalcCapacity
						}
						g = &pivotAggregate{}
						c.groups[key] = g
					}
					g.addMeasure(value, len(record) > len(r.fields))
				}
			}
		}
	}
	if p.filter != "" && len(c.groups) == 0 {
		return c, errors.New("透视筛选没有可显示的项目")
	}
	return c, nil
}

func (c pivotCube) lines(ctx context.Context, root *pivotNode, axis []int, rows, grand bool) ([]pivotLine, error) {
	if len(axis) == 0 {
		return []pivotLine{{node: root, values: true}}, nil
	}
	var lines []pivotLine
	valuesDepth := slices.Index(axis, pivotValuesField)
	appendLine := func(n *pivotNode, kind string, values bool) {
		m := 0
		if valuesDepth >= 0 {
			if n.depth > valuesDepth {
				m = n.path()[valuesDepth]
			} else if values {
				for index := range c.dims[pivotValuesField].items {
					lines = append(lines, pivotLine{n, kind, true, index})
				}
				return
			}
		}
		lines = append(lines, pivotLine{n, kind, values, m})
	}
	visited := 0
	var visit func(*pivotNode) error
	visit = func(parent *pivotNode) error {
		depth := parent.depth
		d := c.dims[axis[depth]]
		order := make([]int, 0, len(parent.children))
		if pivotOn(d.field.ShowAll) {
			for pos, it := range d.items {
				if !isTrue(it.Hidden) {
					order = append(order, pos)
				}
			}
		} else {
			for pos := range parent.children {
				order = append(order, pos)
			}
			slices.Sort(order)
		}
		var allowed map[int]bool
		if d.allowed != nil {
			allowed = d.allowed[filterPath(parent.path())]
		}
		for _, pos := range order {
			if d.labelPass != nil && !d.labelPass[pos] || d.allowed != nil && !allowed[pos] {
				continue
			}
			visited++
			if visited%512 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if visited > formats.EditCalcSourceCells {
				return errors.New("透视项目组合超过单次刷新容量")
			}
			n := parent.children[pos]
			if n == nil {
				n = &pivotNode{parent: parent, pos: pos, depth: depth + 1}
			}
			if depth == len(axis)-1 || !pivotOn(d.items[pos].Detail) {
				appendLine(n, "", true)
				continue
			}
			subs := d.subtotalTypes()
			if depth+1 == len(axis)-1 && valuesDepth == depth+1 {
				subs = nil
			}
			outline := rows && pivotOn(d.field.Outline)
			top := (valuesDepth < 0 || depth >= valuesDepth) && outline && pivotOn(d.field.Top) && len(subs) == 1 && subs[0] == "default"
			if outline {
				appendLine(n, "", top)
			}
			if err := visit(n); err != nil {
				return err
			}
			if !top {
				for _, subtotal := range subs {
					appendLine(n, subtotal, true)
				}
			}
			if rows && isTrue(d.field.Blank) {
				appendLine(n, "blank", false)
			}
			if len(lines) > formats.EditCalcSourceCells {
				return errors.New("透视输出超过单次刷新容量")
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	if grand && !(len(axis) == 1 && valuesDepth == 0) {
		appendLine(root, "grand", true)
	}
	return lines, nil
}

func pivotCommonPrefix(a, b []int) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
func axisItemsXML(tag string, lines []pivotLine) string {
	var out strings.Builder
	fmt.Fprintf(&out, `<%s xmlns="%s" count="%d">`, tag, mainNS, len(lines))
	var prev []int
	for _, line := range lines {
		path := line.node.path()
		repeat := pivotCommonPrefix(prev, path)
		if len(path) > 0 {
			repeat = min(repeat, len(path)-1)
		}
		out.WriteString("<i")
		if line.kind != "" {
			fmt.Fprintf(&out, ` t="%s"`, line.kind)
		}
		if line.measure > 0 {
			fmt.Fprintf(&out, ` i="%d"`, line.measure)
		}
		if repeat > 0 {
			fmt.Fprintf(&out, ` r="%d"`, repeat)
		}
		out.WriteString(">")
		for _, pos := range path[repeat:] {
			fmt.Fprintf(&out, `<x v="%d"/>`, pos)
		}
		if line.kind == "grand" {
			out.WriteString("<x/>")
		}
		out.WriteString("</i>")
		prev = path
	}
	fmt.Fprintf(&out, "</%s>", tag)
	return out.String()
}
