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
	node   *pivotNode
	kind   string
	values bool
}
type pivotCube struct {
	rowRoot, colRoot *pivotNode
	groups           map[[2]*pivotNode]*pivotAggregate
	dims             map[int]*pivotDimension
}

func (p pivotPlan) cube(ctx context.Context, r pivotRecords) (pivotCube, error) {
	dims, err := p.dimensions(r)
	c := pivotCube{&pivotNode{}, &pivotNode{}, map[[2]*pivotNode]*pivotAggregate{}, dims}
	if err != nil {
		return c, err
	}
	work := int64(len(r.rows)) * int64(len(p.rows)+1) * int64(len(p.cols)+1)
	if work > formats.EditCalcVisits {
		return c, errors.New("透视层级组合超过单次计算上限")
	}
	descend := func(n *pivotNode, axis []int, record []pivotValue) *pivotNode {
		for _, f := range axis {
			n = n.child(dims[f].positions[r.dicts[f].index[record[f]]])
		}
		return n
	}
	rows, err := p.filteredRows(ctx, r, dims, func(record []pivotValue) {
		descend(c.rowRoot, p.rows, record)
		descend(c.colRoot, p.cols, record)
	})
	if err != nil {
		return c, err
	}
	for _, record := range rows {
		if err := ctx.Err(); err != nil {
			return c, err
		}
		row, col := descend(c.rowRoot, p.rows, record), descend(c.colRoot, p.cols, record)
		value, err := pivotMeasure(record[p.data], r)
		if err != nil {
			return c, err
		}
		for i := row; i != nil; i = i.parent {
			for j := col; j != nil; j = j.parent {
				key := [2]*pivotNode{i, j}
				a := c.groups[key]
				if a == nil {
					if len(c.groups) >= formats.EditCalcSourceCells {
						return c, errors.New("透视小计交叉项目超过单次刷新容量")
					}
					a = &pivotAggregate{}
					c.groups[key] = a
				}
				a.addMeasure(value, len(record) > len(r.fields))
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
		return []pivotLine{{root, "", true}}, nil
	}
	var lines []pivotLine
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
			if d.allowed != nil && !allowed[pos] {
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
			appendLine := func(kind string, values bool) { lines = append(lines, pivotLine{n, kind, values}) }
			if depth == len(axis)-1 || !pivotOn(d.items[pos].Detail) {
				appendLine("", true)
				continue
			}
			subs := d.subtotalTypes()
			outline := rows && pivotOn(d.field.Outline)
			top := outline && pivotOn(d.field.Top) && len(subs) == 1 && subs[0] == "default"
			if outline {
				appendLine("", top)
			}
			if err := visit(n); err != nil {
				return err
			}
			if !top {
				for _, subtotal := range subs {
					appendLine(subtotal, true)
				}
			}
			if rows && isTrue(d.field.Blank) {
				appendLine("blank", false)
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
	if grand {
		lines = append(lines, pivotLine{root, "grand", true})
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
