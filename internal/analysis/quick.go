package analysis

import (
	"errors"
	"math/big"

	"excellens/internal/formats"
)

// QuickSummary preserves the exact arithmetic of the shared accumulators.
// An empty series has sum 0, but no average or extrema. Row numbers are source
// coordinates, even when input arrives in a filtered or descending view.
type QuickSummary struct {
	Count      int64  `json:"count"`
	Sum        string `json:"sum"`
	Average    string `json:"average"`
	Minimum    string `json:"minimum"`
	Maximum    string `json:"maximum"`
	MinimumRow int    `json:"minimumRow"`
	MaximumRow int    `json:"maximumRow"`
}

type QuickSegment struct {
	First    int          `json:"first"`
	Last     int          `json:"last"`
	FirstRow int          `json:"firstRow"`
	LastRow  int          `json:"lastRow"`
	Summary  QuickSummary `json:"summary"`
}

type quickAggregate struct {
	acc                accumulator
	minimumRow, maxRow int
}

func (a *quickAggregate) add(value *big.Rat, row int) {
	if value == nil {
		return
	}
	if a.acc.min == nil || value.Cmp(a.acc.min) < 0 {
		a.minimumRow = row
	}
	if a.acc.max == nil || value.Cmp(a.acc.max) > 0 {
		a.maxRow = row
	}
	// Distinct/count are disabled, so this path requires no query allocation.
	_ = a.acc.add(metricInput{value: Value{Text: "1"}, number: value}, metricNeeds{number: true, sum: true, min: true, max: true}, nil, nil)
}

func (a *quickAggregate) summary() QuickSummary {
	sum, average := "0", ""
	if a.acc.sum != nil {
		sum = decimalExact(a.acc.sum)
		average = decimalExact(new(big.Rat).Quo(a.acc.sum, new(big.Rat).SetInt64(a.acc.numericCount)))
	}
	return QuickSummary{Count: a.acc.numericCount, Sum: sum,
		Average: average, Minimum: decimalExact(a.acc.min), Maximum: decimalExact(a.acc.max),
		MinimumRow: a.minimumRow, MaximumRow: a.maxRow}
}

// QuickSeries consumes every selected row exactly once, including nil values.
// Consecutive visual positions form balanced, non-overlapping segments; nil
// is unavailable, never zero. Memory depends on the fixed segment budget.
type QuickSeries struct {
	rows, seen int
	total      quickAggregate
	parts      []quickAggregate
	segments   []QuickSegment
}

func NewQuickSeries(rows, segments int) (*QuickSeries, error) {
	if rows < 0 || rows > formats.SelectionQuickCells || segments < 1 || segments > formats.SelectionQuickSegments {
		return nil, errors.New("快速分析行数或分段数无效")
	}
	n := min(rows, segments)
	return &QuickSeries{rows: rows, parts: make([]quickAggregate, n), segments: make([]QuickSegment, n)}, nil
}

func (s *QuickSeries) Add(row int, value *big.Rat) error {
	if row < 1 || s.seen >= s.rows {
		return errors.New("快速分析实际行数与选区不符")
	}
	at := int(int64(s.seen) * int64(len(s.parts)) / int64(s.rows))
	p := &s.segments[at]
	if p.First == 0 {
		p.First, p.FirstRow = s.seen+1, row
	}
	p.Last, p.LastRow = s.seen+1, row
	s.total.add(value, row)
	s.parts[at].add(value, row)
	s.seen++
	return nil
}

func (s *QuickSeries) Result() (QuickSummary, []QuickSegment, error) {
	if s.seen != s.rows {
		return QuickSummary{}, nil, errors.New("快速分析读取不完整，未发布部分结果")
	}
	parts := append([]QuickSegment{}, s.segments...)
	for i := range parts {
		parts[i].Summary = s.parts[i].summary()
	}
	return s.total.summary(), parts, nil
}
