package analysis

import (
	"context"
	"errors"
	"math/big"
)

// ComparisonLabel is shared by exact exports and describes units without
// implying that a rational ratio is already multiplied by one hundred.
func ComparisonLabel(metric Metric) string {
	switch metric.Comparison {
	case "percent_grand":
		return "占范围总计（比值，1=100%）"
	case "percent_row":
		return "占行合计（比值，1=100%）"
	case "percent_column":
		return "占列合计（比值，1=100%）"
	case "running_total", "running_percent":
		axis := "沿行累计"
		if metric.ComparisonAxis == "columns" {
			axis = "沿列累计"
		}
		if metric.Comparison == "running_percent" {
			axis += "占比（比值，1=100%）"
		}
		return axis
	}
	return ""
}

func outputCell(cell Cell) (text string, numeric, percent bool) {
	if value := cell.Comparison; value != nil {
		if value.Status != "" {
			return value.Display, false, false
		}
		return value.Exact, value.Exact != "", value.Percent
	}
	return cell.Exact, cell.Exact != "", false
}

// ComparedValue is a presentation result, never a replacement for the original
// aggregate in Cell. Percent values are exact ratios (one means 100 percent).
type ComparedValue struct {
	Mode        string `json:"mode"`
	Axis        string `json:"axis,omitempty"`
	Display     string `json:"display"`
	Exact       string `json:"exact"`
	Status      string `json:"status,omitempty"`
	Percent     bool   `json:"percent,omitempty"`
	Numerator   string `json:"numerator,omitempty"`
	Denominator string `json:"denominator,omitempty"`
}

func HasComparisons(q Query) bool {
	for _, metric := range q.Metrics {
		if metric.Comparison != "" || metric.ComparisonAxis != "" {
			return true
		}
	}
	return false
}

func ValidateComparisons(q Query) error {
	for _, metric := range q.Metrics {
		switch metric.Comparison {
		case "", "percent_grand", "percent_row", "percent_column":
			if metric.ComparisonAxis != "" {
				return errors.New("只有累计指标可以指定行列方向")
			}
		case "running_total", "running_percent":
			if metric.ComparisonAxis != "" && metric.ComparisonAxis != "rows" && metric.ComparisonAxis != "columns" {
				return errors.New("累计方向无效，请选择沿行或沿列")
			}
		default:
			return errors.New("指标比较方式不支持")
		}
	}
	return nil
}

func running(metric Metric) bool {
	return metric.Comparison == "running_total" || metric.Comparison == "running_percent"
}

func comparisonNeeds(metrics []Metric, plan metricPlan, columns bool) []metricNeeds {
	needs := make([]metricNeeds, len(plan.fields))
	for i, metric := range metrics {
		if !running(metric) || (metric.ComparisonAxis == "columns") != columns {
			continue
		}
		n := &needs[plan.slots[i]]
		switch metric.Aggregate {
		case "count":
			n.count = true
		case "distinct":
			n.count, n.distinct = true, true
		case "sum", "average":
			n.number, n.sum = true, true
		case "min":
			n.number, n.min = true, true
		case "max":
			n.number, n.max = true, true
		}
	}
	return needs
}

// Merge original accumulator states, not displayed subtotals. Average keeps
// its original weighting and distinct unions the original text sets.
func mergeMetrics(ctx context.Context, target, source []accumulator, needs []metricNeeds, used *int, memory *queryMemory) error {
	if len(source) == 0 {
		return nil
	}
	for i, need := range needs {
		if need == (metricNeeds{}) {
			continue
		}
		a, b := &target[i], &source[i]
		if need.count {
			a.count += b.count
		}
		if need.number {
			a.numericCount += b.numericCount
		}
		if need.sum && b.sum != nil {
			if a.sum == nil {
				a.sum = new(big.Rat).Set(b.sum)
			} else {
				a.sum.Add(a.sum, b.sum)
			}
		}
		if need.min && b.min != nil && (a.min == nil || b.min.Cmp(a.min) < 0) {
			a.min = new(big.Rat).Set(b.min)
		}
		if need.max && b.max != nil && (a.max == nil || b.max.Cmp(a.max) > 0) {
			a.max = new(big.Rat).Set(b.max)
		}
		if need.distinct {
			at := 0
			for key := range b.distinct {
				if at%256 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				at++
				if err := a.addDistinct(key, used, memory); err != nil {
					return err
				}
			}
		}
	}
	return ctx.Err()
}

func comparedCell(base Cell, numerator Cell, denominator *Cell, metric Metric, precision int, memory *queryMemory) (Cell, error) {
	value := &ComparedValue{Mode: metric.Comparison, Numerator: numerator.Exact, Percent: metric.Comparison != "running_total"}
	if running(metric) {
		value.Axis = metric.ComparisonAxis
		if value.Axis == "" {
			value.Axis = "rows"
		}
	}
	valid := func(cell Cell) bool {
		return cell.Exact != "" && (cell.Count > 0 || metric.Aggregate == "count" || metric.Aggregate == "distinct")
	}
	if denominator != nil {
		value.Denominator = denominator.Exact
	}
	if !valid(numerator) {
		value.Status, value.Display = "empty", "无有效值"
	} else if denominator != nil && !valid(*denominator) {
		value.Status, value.Display = "empty_denominator", "分母无有效值"
	} else {
		n, ok := new(big.Rat).SetString(numerator.Exact)
		if !ok {
			return Cell{}, errors.New("指标比较的精确聚合值无效")
		}
		if denominator != nil {
			d, ok := new(big.Rat).SetString(denominator.Exact)
			if !ok {
				return Cell{}, errors.New("指标比较的精确分母无效")
			}
			if d.Sign() == 0 {
				value.Status, value.Display = "zero_denominator", "分母为0"
			} else {
				n.Quo(n, d)
			}
		}
		if value.Status == "" {
			value.Exact = decimalExact(n)
			if value.Percent {
				value.Display = decimalDisplay(new(big.Rat).Mul(n, big.NewRat(100, 1)), precision) + "%"
			} else {
				value.Display = decimalDisplay(n, precision)
			}
		}
	}
	// Account strings and their transient UTF-8/JSON representations as well as
	// the attached object; this is a work budget, not the whole process limit.
	if err := memory.reserve(256 + 4*int64(len(value.Exact)+len(value.Display)+len(value.Numerator)+len(value.Denominator))); err != nil {
		return Cell{}, err
	}
	base.Comparison = value
	return base, nil
}

// Called once after deterministic raw-value sorting and before Top-N. The
// complete per-level totals already come from this same source scan.
func applyComparisons(ctx context.Context, q Query, result *Result, groups map[string]*metricGroup, colTotals map[string][]accumulator, grand []accumulator, plan metricPlan, distinctUsed *int, memory *queryMemory) error {
	if !HasComparisons(q) {
		return nil
	}
	metrics := queryMetrics(q)
	rowNeeds, columnNeeds := comparisonNeeds(metrics, plan, false), comparisonNeeds(metrics, plan, true)
	states := len(plan.fields)
	if err := memory.reserve(int64(states*(len(result.ColumnKeys)+3))*256 + int64(len(result.Rows))*128); err != nil {
		return err
	}
	prefix := make([]accumulator, states)
	prefixColumns := make([][]accumulator, len(result.ColumnKeys))
	for j := range prefixColumns {
		prefixColumns[j] = make([]accumulator, states)
	}
	// Column-prefix states are reused across rows; releasing their temporary
	// distinct charge avoids charging a succession of dead maps as live memory.
	columnUsed, columnBytes := 0, int64(0)
	columnPrefix := func() []accumulator {
		*distinctUsed -= columnUsed
		memory.used -= columnBytes
		columnUsed, columnBytes = 0, 0
		return make([]accumulator, states)
	}
	mergeColumn := func(target, from []accumulator) error {
		used, bytes := *distinctUsed, memory.used
		err := mergeMetrics(ctx, target, from, columnNeeds, distinctUsed, memory)
		columnUsed += *distinctUsed - used
		columnBytes += memory.used - bytes
		return err
	}
	for r := range result.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := &result.Rows[r]
		group := groups[groupKey(row.Keys)]
		if err := mergeMetrics(ctx, prefix, group.total, rowNeeds, distinctUsed, memory); err != nil {
			return err
		}
		across := columnPrefix()
		for j, key := range result.ColumnKeys {
			if err := mergeMetrics(ctx, prefixColumns[j], group.buckets[key], rowNeeds, distinctUsed, memory); err != nil {
				return err
			}
			if err := mergeColumn(across, group.buckets[key]); err != nil {
				return err
			}
			for m, metric := range metrics {
				if metric.Comparison == "" {
					continue
				}
				numerator := row.MetricValues[j][m]
				var denominator *Cell
				switch metric.Comparison {
				case "percent_grand":
					denominator = &result.MetricGrand[m]
				case "percent_row":
					denominator = &row.MetricTotals[m]
				case "percent_column":
					denominator = &result.MetricColumnTotals[j][m]
				default:
					a := &prefixColumns[j][plan.slots[m]]
					denominator = &result.MetricColumnTotals[j][m]
					if metric.ComparisonAxis == "columns" {
						a, denominator = &across[plan.slots[m]], &row.MetricTotals[m]
					}
					numerator = a.result(metric.Aggregate, q.Precision)
					if metric.Comparison == "running_total" {
						denominator = nil
					}
				}
				cell, err := comparedCell(row.MetricValues[j][m], numerator, denominator, metric, q.Precision, memory)
				if err != nil {
					return err
				}
				row.MetricValues[j][m] = cell
			}
			row.Values[j] = row.MetricValues[j][0]
		}
		for m, metric := range metrics {
			if metric.Comparison == "" {
				continue
			}
			numerator := row.MetricTotals[m]
			denominator := &result.MetricGrand[m]
			if metric.Comparison == "percent_row" || running(metric) && metric.ComparisonAxis == "columns" {
				denominator = &row.MetricTotals[m]
			}
			if running(metric) && metric.ComparisonAxis != "columns" {
				numerator = prefix[plan.slots[m]].result(metric.Aggregate, q.Precision)
			}
			if metric.Comparison == "running_total" {
				denominator = nil
			}
			cell, err := comparedCell(row.MetricTotals[m], numerator, denominator, metric, q.Precision, memory)
			if err != nil {
				return err
			}
			row.MetricTotals[m] = cell
		}
		row.Total = row.MetricTotals[0]
	}
	across := columnPrefix()
	for j, key := range result.ColumnKeys {
		if err := mergeColumn(across, colTotals[key]); err != nil {
			return err
		}
		for m, metric := range metrics {
			if metric.Comparison == "" {
				continue
			}
			numerator := result.MetricColumnTotals[j][m]
			denominator := &result.MetricGrand[m]
			if metric.Comparison == "percent_column" || running(metric) && metric.ComparisonAxis != "columns" {
				denominator = &result.MetricColumnTotals[j][m]
			}
			if running(metric) && metric.ComparisonAxis == "columns" {
				numerator = across[plan.slots[m]].result(metric.Aggregate, q.Precision)
			}
			if metric.Comparison == "running_total" {
				denominator = nil
			}
			cell, err := comparedCell(result.MetricColumnTotals[j][m], numerator, denominator, metric, q.Precision, memory)
			if err != nil {
				return err
			}
			result.MetricColumnTotals[j][m] = cell
		}
		result.Totals[j] = result.MetricColumnTotals[j][0]
	}
	for m, metric := range metrics {
		if metric.Comparison == "" {
			continue
		}
		numerator := grand[plan.slots[m]].result(metric.Aggregate, q.Precision)
		denominator := &result.MetricGrand[m]
		if metric.Comparison == "running_total" {
			denominator = nil
		}
		cell, err := comparedCell(result.MetricGrand[m], numerator, denominator, metric, q.Precision, memory)
		if err != nil {
			return err
		}
		result.MetricGrand[m] = cell
	}
	result.Grand = result.MetricGrand[0]
	return ctx.Err()
}
