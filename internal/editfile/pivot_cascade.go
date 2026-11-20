package editfile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"excellens/internal/editdoc"
	"excellens/internal/opc"
)

// Edges point from the producer's output to the consumer's source. The
// structural transaction rebuilds only the connected downstream caches.
type pivotGraph map[string]map[string]bool

func (g pivotGraph) order(start string) ([]string, error) {
	seen := map[string]byte{}
	var reverse []string
	var visit func(string) error
	visit = func(n string) error {
		if seen[n] == 1 {
			return fmt.Errorf("透视缓存存在循环依赖（%s）", n)
		}
		if seen[n] == 2 {
			return nil
		}
		seen[n] = 1
		for _, next := range slices.Sorted(maps.Keys(g[n])) {
			if err := visit(next); err != nil {
				return err
			}
		}
		seen[n] = 2
		reverse = append(reverse, n)
		return nil
	}
	if err := visit(start); err != nil {
		return nil, err
	}
	slices.Reverse(reverse)
	return reverse, nil
}

func (g pivotGraph) link(from, to string) {
	if g[from] == nil {
		g[from] = map[string]bool{}
	}
	g[from][to] = true
}

func (p *xlsxPackage) refreshPivotCaches(ctx context.Context, dst io.Writer, target string, op *editdoc.Structure, values objectValues) (opc.Receipt, error) {
	return p.refreshCacheRoots(ctx, dst, []string{target}, op, values, nil)
}

func (p *xlsxPackage) refreshCacheRoots(ctx context.Context, dst io.Writer, targets []string, op *editdoc.Structure, values objectValues, chartParts []string) (opc.Receipt, error) {
	p.pivotAsOf = calendarDay(time.Now())
	chartKeys, err := p.linkedPivotCharts(ctx)
	if err != nil {
		return opc.Receipt{}, err
	}
	graph, err := p.cacheGraph(ctx, values.book)
	if err != nil {
		return opc.Receipt{}, err
	}
	target := targets[0]
	root := ""
	for _, part := range targets {
		graph.link(root, p.pivotOutputs[part])
	}
	for {
		receipt, reorder, err := p.renderCacheGraph(ctx, dst, root, target, op, values, chartParts, chartKeys, graph)
		if err != nil || !reorder {
			return receipt, err
		}
		// A newly expanded result revealed an edge to an already visited
		// sibling. Replan from the same source before emitting any bytes.
	}
}

func (p *xlsxPackage) renderCacheGraph(ctx context.Context, dst io.Writer, root, target string, op *editdoc.Structure, values objectValues, chartParts []string, chartKeys map[string]bool, graph pivotGraph) (opc.Receipt, bool, error) {
	done := map[string]bool{}
	updates := map[string][]byte{}
	changes := map[string]map[editdoc.Address]editdoc.Value{}
	overlay := map[editdoc.Address]editdoc.Value{}
	var plans []pivotPlan
	charts := map[string]*pivotChartData{}
	for {
		order, err := graph.order(root)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		i := slices.IndexFunc(order, func(part string) bool { return part != root && !done[part] })
		if i < 0 {
			break
		}
		cache := order[i]
		request := op
		if cache != p.pivotOutputs[target] {
			request = nil
		}
		next, err := p.renderPivotCache(ctx, cache, target, request, values, updates, chartKeys)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		for _, chart := range next.charts {
			charts[chartKey(chart.sheet, chart.name)] = chart
		}
		plans = append(plans, next.plans...)
		for a, v := range next.cells {
			if changes[a.Sheet] == nil {
				changes[a.Sheet] = map[editdoc.Address]editdoc.Value{}
			}
			if _, exists := overlay[a]; exists {
				return opc.Receipt{}, false, errors.New("不同缓存的透视结果区域重叠")
			}
			overlay[a] = v
			changes[a.Sheet][a] = v
		}
		// Explicitly refreshed pivot outputs are authoritative inputs for
		// formula recalculation, including formulas that feed another cache.
		valueBook := *values.book
		valueBook.owners = slices.DeleteFunc(slices.Clone(valueBook.owners), func(o calcOwner) bool { return o.output.Pivot })
		links, err := p.linkFormulaSources(ctx, graph, cache, &valueBook, slices.Collect(maps.Keys(next.cells)))
		if err != nil {
			return opc.Receipt{}, false, err
		}
		for _, dep := range links {
			if done[dep] {
				return opc.Receipt{}, true, nil
			}
		}
		done[cache] = true
		values.overlay = overlay
		values.calculation, err = valueBook.Calculate(ctx, overlay, nil)
		if err != nil {
			return opc.Receipt{}, false, err
		}
	}
	for _, s := range p.layout.Sheets {
		if len(changes[s.Name]) == 0 {
			continue
		}
		raw, err := p.pivotSheet(ctx, s, changes[s.Name], plans, updates)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		updates[s.Part] = raw
	}
	if p.files["xl/sharedStrings.xml"] != nil {
		raw, err := p.readPart(ctx, "xl/sharedStrings.xml")
		if err != nil {
			return opc.Receipt{}, false, err
		}
		ps, err := sharedCountPatches(ctx, raw)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		if len(ps) > 0 {
			updates["xl/sharedStrings.xml"], err = applyXML(ctx, raw, ps)
			if err != nil {
				return opc.Receipt{}, false, err
			}
		}
	}
	var dependentErr error
	if len(values.calculation.Arrays) > 0 {
		dependentErr = p.writeArrayResults(ctx, values.book, values.calculation, overlay, updates)
	} else {
		dependentErr = p.refreshDependents(ctx, values, updates)
	}
	if dependentErr != nil {
		return opc.Receipt{}, false, dependentErr
	}
	if err := p.writePivotCharts(ctx, charts, updates); err != nil {
		return opc.Receipt{}, false, err
	}
	for _, part := range chartParts {
		raw, err := p.readPart(ctx, part)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		if _, pivot, err := chartPivotSource(ctx, raw); err != nil {
			return opc.Receipt{}, false, err
		} else if pivot {
			continue
		}
		next, err := p.chartCaches(ctx, raw, values)
		if err != nil {
			return opc.Receipt{}, false, err
		}
		updates[part] = next
	}
	receipt, err := p.writeValueStructure(ctx, dst, updates)
	return receipt, false, err
}
