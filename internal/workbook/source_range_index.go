package workbook

import "sort"

// A source range stays a rectangle, independently of the number of covered
// cells. Conditional formatting and data rules share this immutable row tree.
type sourceRangeEntry struct {
	area  CellMerge
	group int
}

type sourceRangeNode struct {
	entry       sourceRangeEntry
	left, right int
	maxRow      int
	onlyGroup   int
}

func buildSourceRangeTree(entries []sourceRangeEntry) ([]sourceRangeNode, int) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].area.Row != entries[j].area.Row {
			return entries[i].area.Row < entries[j].area.Row
		}
		return entries[i].area.Col < entries[j].area.Col
	})
	nodes := make([]sourceRangeNode, 0, len(entries))
	var build func(int, int) int
	build = func(lo, hi int) int {
		if lo >= hi {
			return -1
		}
		mid := lo + (hi-lo)/2
		at := len(nodes)
		entry := entries[mid]
		nodes = append(nodes, sourceRangeNode{entry: entry, maxRow: entry.area.EndRow, onlyGroup: entry.group})
		left, right := build(lo, mid), build(mid+1, hi)
		node := &nodes[at]
		node.left, node.right = left, right
		for _, child := range []int{left, right} {
			if child >= 0 {
				node.maxRow = max(node.maxRow, nodes[child].maxRow)
				if nodes[child].onlyGroup != node.onlyGroup {
					node.onlyGroup = -1
				}
			}
		}
		return at
	}
	root := build(0, len(entries))
	return nodes, root
}
