package analysis

import (
	"errors"
	"math/big"
	"strconv"
)

var errDistinctLimit = errors.New("去重值数量超出内存保护限制，请缩小分析范围")

// Each referenced field is interpreted once per included record. Several
// measures may share state, while their count semantics remain independent.
type metricNeeds struct{ count, number, sum, min, max, distinct bool }
type metricField struct {
	id    string
	needs metricNeeds
}
type metricPlan struct {
	fields []metricField
	slots  []int
}
type metricInput struct {
	value  Value
	number *big.Rat // immutable until every aggregate for this row has consumed it
}

func compileMetrics(metrics []Metric) metricPlan {
	p := metricPlan{slots: make([]int, len(metrics))}
	byField := make(map[string]int, len(metrics))
	for i, metric := range metrics {
		slot, exists := byField[metric.Field]
		if !exists {
			slot = len(p.fields)
			byField[metric.Field] = slot
			p.fields = append(p.fields, metricField{id: metric.Field})
		}
		p.slots[i] = slot
		n := &p.fields[slot].needs
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
	return p
}

func (p metricPlan) prepare(values map[string]Value, inputs []metricInput) {
	for i, field := range p.fields {
		v := values[field.id]
		if field.id == "" { // COUNT records, including empty and missing-cache rows.
			v = Value{Text: "1"}
		}
		inputs[i] = metricInput{value: v}
		if field.needs.number {
			inputs[i].number, _ = numericOrEmpty(v)
		}
	}
}

type accumulator struct {
	count, numericCount int64
	sum, min, max       *big.Rat
	distinct            map[string]struct{}
}

func (a *accumulator) addDistinct(value string, used *int, memory *queryMemory) error {
	if _, exists := a.distinct[value]; exists {
		return nil
	}
	if err := memory.reserve(int64(len(value)) + 64); err != nil {
		return err
	}
	*used++
	if *used > MaxDistinct {
		return errDistinctLimit
	}
	if a.distinct == nil {
		a.distinct = make(map[string]struct{})
	}
	a.distinct[value] = struct{}{}
	return nil
}

func (a *accumulator) add(input metricInput, needs metricNeeds, distinctUsed *int, memory *queryMemory) error {
	v := input.value
	if v.Text == "" || v.MissingCache {
		return nil
	}
	if needs.distinct {
		if err := a.addDistinct(v.Text, distinctUsed, memory); err != nil {
			return err
		}
	}
	if needs.count {
		a.count++
	}
	d := input.number
	if d == nil {
		return nil
	}
	a.numericCount++
	if needs.sum {
		if a.sum == nil {
			a.sum = new(big.Rat).Set(d)
		} else {
			a.sum.Add(a.sum, d)
		}
	}
	if needs.min && (a.min == nil || d.Cmp(a.min) < 0) {
		a.min = new(big.Rat).Set(d)
	}
	if needs.max && (a.max == nil || d.Cmp(a.max) > 0) {
		a.max = new(big.Rat).Set(d)
	}
	return nil
}

func (a *accumulator) result(agg string, precision int) Cell {
	var v *big.Rat
	count := a.numericCount
	switch agg {
	case "count":
		count = a.count
		v = new(big.Rat).SetInt64(count)
	case "distinct":
		count = a.count
		v = new(big.Rat).SetInt64(int64(len(a.distinct)))
	case "sum":
		v = a.sum
		if v == nil {
			v = new(big.Rat)
		}
	case "average":
		if count > 0 && a.sum != nil {
			v = new(big.Rat).Quo(a.sum, new(big.Rat).SetInt64(count))
		}
	case "min":
		v = a.min
	case "max":
		v = a.max
	}
	if v == nil {
		return Cell{Count: count}
	}
	display := decimalDisplay(v, precision)
	if agg == "count" || agg == "distinct" {
		display = strconv.FormatInt(v.Num().Int64(), 10)
	}
	return Cell{Display: display, Exact: decimalExact(v), Count: count}
}
