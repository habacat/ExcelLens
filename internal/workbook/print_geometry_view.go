package workbook

import (
	"context"
	"errors"
	"math"
	"math/bits"

	"excellens/internal/formats"
)

// These are explicit derived defaults, never saved source properties. A view
// shares the immutable source prefix, including million-segment geometries.
// It replaces only genuinely absent defaults, never invalid saved dimensions.
type PrintGeometryDefault struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}
type PrintGeometryDefaults struct {
	Row    *PrintGeometryDefault `json:"row,omitempty"`
	Column *PrintGeometryDefault `json:"column,omitempty"`
}
type PrintGeometryView struct {
	base        *SourcePrintGeometryIndex
	row, column *printGeometryMeasurement
}

func (index *SourcePrintGeometryIndex) ResolveDefaults(ctx context.Context, defaults PrintGeometryDefaults) (*PrintGeometryView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := &PrintGeometryView{base: index}
	for _, item := range []struct {
		value  *PrintGeometryDefault
		target **printGeometryMeasurement
	}{{defaults.Row, &view.row}, {defaults.Column, &view.column}} {
		if item.value == nil {
			continue
		}
		units, status, reason := printGeometryMeasure(item.value.Value, item.value.Unit, index.digit)
		if status != "stored" && status != "approximate" {
			return nil, errors.New("预览缺省尺寸无效：" + reason)
		}
		*item.target = &printGeometryMeasurement{units, status, reason}
	}
	return view, nil
}

func (view *PrintGeometryView) Extent(ctx context.Context, axis string, first, last int) (PrintGeometryExtent, error) {
	if err := ctx.Err(); err != nil {
		return PrintGeometryExtent{}, err
	}
	a, err := view.base.axis(axis)
	if err != nil {
		return PrintGeometryExtent{}, err
	}
	if first < 1 || last < first || last > a.maximum {
		return PrintGeometryExtent{}, errors.New("打印几何源范围越界")
	}
	before, after := a.prefixAt(first-1), a.prefixAt(last)
	unknown, fallback := after.unknown-before.unknown, after.fallback-before.fallback
	value := view.row
	if axis == "col" {
		value = view.column
	}
	if value != nil {
		unknown -= fallback
	}
	if unknown != 0 {
		return PrintGeometryExtent{}, errors.New("打印范围含缺少或无法解释的尺寸，请明确预览缺省设置")
	}
	lo, borrow := bits.Sub64(after.lo, before.lo, 0)
	hi, _ := bits.Sub64(after.hi, before.hi, borrow)
	approximate := after.approximate != before.approximate
	if value != nil && fallback != 0 {
		addHi, addLo := bits.Mul64(uint64(value.units), uint64(fallback))
		var carry uint64
		lo, carry = bits.Add64(lo, addLo, 0)
		hi, _ = bits.Add64(hi, addHi, carry)
		approximate = approximate || value.status == "approximate"
	}
	if hi != 0 || lo > math.MaxInt64 {
		return PrintGeometryExtent{}, errors.New("打印范围物理长度超过计算容量")
	}
	return PrintGeometryExtent{Units: int64(lo), Points: float64(lo) / float64(formats.PrintGeometryUnitsPerPoint), Approximate: approximate, Defaulted: value != nil && fallback != 0}, nil
}
