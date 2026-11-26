package conditional

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"

	"excellens/internal/formats"
)

type aggregateCell struct {
	value              EvalValue
	notice             string
	groupKey, matchKey string
	number             Value
	numberReady        bool
}

func (c *aggregateCell) sumValue() Value {
	if c.numberReady {
		return c.number
	}
	c.numberReady = true
	switch {
	case c.notice != "":
		c.number = Value{Unknown: true, Fault: c.notice}
	case c.value.Kind == "error":
		c.number.Fault = c.value.Text
	case c.value.Kind == "number":
		var ok bool
		c.number.Number, ok = ValueToRat(c.value)
		if !ok {
			c.number.Fault = "#NUM!"
		}
	}
	return c.number
}

type aggregateField struct {
	slot   int
	filter Criterion
}
type aggregatePlan struct {
	from, to, sumSlot int
	kind              string
	fields            []aggregateField
}

// RowAggregator binds a worksheet's plans to compact column slots once. Feed
// strictly increasing physical rows with Next and Set, then call Finish.
type RowAggregator struct {
	writer            *Writer
	engine            *Engine
	columns           []int
	slots             map[int]int
	cells             []aggregateCell
	blank             aggregateCell
	plans             []aggregatePlan
	lower, upper, row int
	key               []byte
}

func NewRowAggregator(ctx context.Context, w *Writer, source []StatPlan, date1904 bool) *RowAggregator {
	a := &RowAggregator{writer: w, engine: NewEngine(ctx, date1904), lower: formats.ExcelRows, upper: 1, blank: aggregateCell{value: BlankValue()}}
	for _, p := range source {
		a.columns = append(a.columns, p.Cols...)
		if p.SumCol != 0 {
			a.columns = append(a.columns, p.SumCol)
		}
		a.lower = min(a.lower, p.From)
		a.upper = max(a.upper, p.To)
	}
	slices.Sort(a.columns)
	a.columns = slices.Compact(a.columns)
	a.slots = make(map[int]int, len(a.columns))
	a.cells = make([]aggregateCell, len(a.columns))
	for slot, col := range a.columns {
		a.slots[col] = slot
	}
	for _, p := range source {
		bound := aggregatePlan{from: p.From, to: p.To, kind: p.Kind, sumSlot: -1}
		if p.SumCol != 0 {
			bound.sumSlot = a.slots[p.SumCol]
		}
		for j, col := range p.Cols {
			field := aggregateField{slot: a.slots[col]}
			if j < len(p.Literals) && p.Literals[j] != "" {
				field.filter = a.engine.CompileCriterion(p.Constants[j])
			}
			bound.fields = append(bound.fields, field)
		}
		a.plans = append(a.plans, bound)
	}
	return a
}

func (a *RowAggregator) Columns() []int     { return a.columns }
func (a *RowAggregator) Bounds() (int, int) { return a.lower, a.upper }

func (a *RowAggregator) Next(row int) error {
	if row <= a.row || row < a.lower || row > a.upper {
		return errors.New("条件统计源行顺序或范围无效")
	}
	if a.row != 0 {
		if err := a.accumulate(a.row, a.row, false); err != nil {
			return err
		}
	}
	if err := a.writer.nextRow(); err != nil {
		return err
	}
	a.engine.ResetBudget()
	first := max(a.lower, a.row+1)
	if first < row {
		if err := a.accumulate(first, row-1, true); err != nil {
			return err
		}
	}
	for i := range a.cells {
		a.cells[i] = aggregateCell{value: BlankValue()}
	}
	a.row = row
	return nil
}
func (a *RowAggregator) Set(col int, cell CellOperand) {
	if slot, ok := a.slots[col]; ok {
		value, notice := a.engine.CellValue(cell)
		a.cells[slot] = aggregateCell{value: value, notice: notice}
	}
}
func (a *RowAggregator) Finish() error {
	if a.row != 0 {
		if err := a.accumulate(a.row, a.row, false); err != nil {
			return err
		}
	}
	first := max(a.lower, a.row+1)
	if first <= a.upper {
		return a.accumulate(first, a.upper, true)
	}
	return nil
}

func (a *RowAggregator) cell(slot int, gap bool) *aggregateCell {
	if gap {
		return &a.blank
	}
	return &a.cells[slot]
}

func appendKeyPart(key []byte, s string) []byte {
	key = binary.AppendUvarint(key, uint64(len(s)))
	return append(key, s...)
}

func (a *RowAggregator) accumulate(from, to int, gap bool) error {
	for id := range a.plans {
		p := &a.plans[id]
		first, last := max(p.from, from), min(p.to, to)
		if first > last {
			continue
		}
		if p.kind == "match" {
			cell := a.cell(p.fields[0].slot, gap)
			if cell.notice != "" {
				if err := a.writer.recordIssue(id, first, Value{Unknown: true, Fault: cell.notice}); err != nil {
					return err
				}
			} else if cell.value.Kind != "blank" {
				if cell.matchKey == "" {
					cell.matchKey = typedKey(cell.value)
				}
				a.key = appendKeyPart(a.key[:0], cell.matchKey)
				if err := a.writer.putFirstMatch(id, a.key, first); err != nil {
					return err
				}
			}
			continue
		}
		a.key = a.key[:0]
		matches := true
		for _, field := range p.fields {
			cell := a.cell(field.slot, gap)
			notice := cell.notice
			if notice == "" {
				if field.filter != nil {
					matches, notice = field.filter(cell.value)
				} else {
					if cell.groupKey == "" {
						cell.groupKey = groupKey(cell.value)
					}
					a.key = appendKeyPart(a.key, cell.groupKey)
				}
			}
			if notice != "" {
				if err := a.writer.recordIssue(id, first, Value{Unknown: true, Fault: notice}); err != nil {
					return err
				}
				matches = false
			}
			if !matches {
				break
			}
		}
		if !matches {
			continue
		}
		var sum Value
		if p.sumSlot >= 0 {
			sum = a.cell(p.sumSlot, gap).sumValue()
		}
		if p.kind == "prefix" {
			if err := a.writer.addPrefix(id, first, sum); err != nil {
				return err
			}
		} else {
			if len(a.key) == 0 {
				a.key = append(a.key, 0)
			}
			if err := a.writer.addGroup(id, a.key, first, last-first+1, sum); err != nil {
				return err
			}
		}
	}
	return nil
}
