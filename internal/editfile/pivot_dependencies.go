package editfile

import (
	"context"
	"slices"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
)

func (p *xlsxPackage) cacheGraph(ctx context.Context, book *CalculationBook) (pivotGraph, error) {
	g := pivotGraph{}
	if len(p.pivotSources) == 0 {
		return g, nil
	}
	d := *book
	d.unbound = nil
	d.owners = slices.DeleteFunc(slices.Clone(d.owners), func(o calcOwner) bool { return o.output.Pivot })
	for sheet, owners := range p.pivotRanges {
		for _, output := range owners {
			if output.pivot == "" {
				continue
			}
			from := p.pivotOutputs[output.pivot]
			r := output.area
			for to, source := range p.pivotSources {
				if sheet == source.sheet && r.overlaps(source.area) {
					g.link(from, to)
				}
			}
			var roots []editdoc.Address
			for a, formulas := range d.deps.exact {
				if sheet == a.Sheet && r.contains(a.Row, a.Col) {
					roots = append(roots, formulas...)
				}
			}
			for rangeRef, formulas := range d.deps.ranges {
				if overlapsSheet(sheet, r, rangeRef) {
					roots = append(roots, formulas...)
				}
			}
			if _, err := p.linkFormulaSources(ctx, g, from, &d, roots); err != nil {
				return nil, err
			}
		}
	}
	return g, ctx.Err()
}

func overlapsSheet(sheet string, area cellRect, r calculator.WorksheetReference) bool {
	return sheet == r.Sheet && area.overlaps(cellRect{r.Top, r.Left, r.Bottom, r.Right})
}

func (p *xlsxPackage) linkFormulaSources(ctx context.Context, g pivotGraph, from string, b *CalculationBook, roots []editdoc.Address) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	index := *b
	index.unbound = nil // Unbound expressions cannot establish a local reference edge.
	queue, _, _, err := index.dependents(ctx, roots, calcDependencies{})
	if err != nil {
		return nil, err
	}
	var added []string
	for to, source := range p.pivotSources {
		if g[from][to] {
			continue
		}
		for _, a := range queue {
			if a.Sheet == source.sheet && source.area.contains(a.Row, a.Col) {
				g.link(from, to)
				added = append(added, to)
				break
			}
		}
	}
	return added, nil
}
