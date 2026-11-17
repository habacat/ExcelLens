package workbook

import (
	"context"
	"errors"
	"iter"
	"os"

	"excellens/internal/formats"
)

// ReadRowsByNumbersWithOptionsContext reads an explicit, bounded caller-owned
// set of source row numbers, including proven sparse blanks. It never adds
// these blanks to the source index or changes its used extent.
func (w *Workbook) ReadRowsByNumbersWithOptionsContext(ctx context.Context, idx *SheetIndex, numbers []int, first, last int, options RowReadOptions) ([]RowData, error) {
	rows := make([]RowData, 0, len(numbers))
	err := w.visitSourceNumbers(ctx, idx, func(yield func(int) bool) {
		for _, n := range numbers {
			if !yield(n) {
				return
			}
		}
	}, first, last, options, false, func(row RowData) error { rows = append(rows, row); return nil })
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// VisitRowsByNumbersWithOptionsContext streams source coordinates with one
// descriptor. Presentation budgets reset per row, like rich-text budgets;
// the dependency memo stays bounded for the whole visit.
func (w *Workbook) VisitRowsByNumbersWithOptionsContext(ctx context.Context, idx *SheetIndex, numbers iter.Seq[int], first, last int, options RowReadOptions, visit func(RowData) error) error {
	return w.visitSourceNumbers(ctx, idx, numbers, first, last, options, true, visit)
}

func (w *Workbook) visitSourceNumbers(ctx context.Context, idx *SheetIndex, numbers iter.Seq[int], first, last int, options RowReadOptions, byRow bool, visit func(RowData) error) error {
	return w.visitSourceReferences(ctx, idx, sourceRowReferences(idx, numbers), first, last, options, byRow, true, visit)
}

func sourceRowReferences(idx *SheetIndex, numbers iter.Seq[int]) iter.Seq[SourceRowReference] {
	return func(yield func(SourceRowReference) bool) {
		previous, position, seen := 0, 0, false
		for number := range numbers {
			if seen && number == previous+1 {
				if position < idx.RowCount() && idx.RowAt(position).Number < number {
					position++
				}
			} else {
				position = idx.FindRowOffset(number)
			}
			previous, seen = number, true
			ref := SourceRowReference{Number: number, Position: -1}
			if position < idx.RowCount() && idx.RowAt(position).Number == number {
				ref.Position = position
			}
			if !yield(ref) {
				return
			}
		}
	}
}

// VisitRowsByReferencesWithOptionsContext shares the source-number reader's
// descriptor and presentation budgets, while ordered callers reuse known index
// positions. Invalid coordinates, mismatched positions and false blanks fail.
func (w *Workbook) VisitRowsByReferencesWithOptionsContext(ctx context.Context, idx *SheetIndex, refs iter.Seq[SourceRowReference], first, last int, options RowReadOptions, visit func(RowData) error) error {
	return w.visitSourceReferences(ctx, idx, refs, first, last, options, true, false, visit)
}

func (w *Workbook) visitSourceReferences(ctx context.Context, idx *SheetIndex, refs iter.Seq[SourceRowReference], first, last int, options RowReadOptions, byRow, knownBlanks bool, visit func(RowData) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if idx == nil || refs == nil || visit == nil {
		return errors.New("源行引用或读取目标无效")
	}
	if first < 1 || last < first || last > formats.ExcelColumns {
		return errors.New("源列范围无效")
	}
	var selected map[int]struct{}
	if len(options.Columns) > 0 {
		selected = make(map[int]struct{}, len(options.Columns))
		previous := 0
		for _, c := range options.Columns {
			if c <= previous || c < first || c > last {
				return errors.New("稀疏源列必须按升序且不重复")
			}
			selected[c] = struct{}{}
			previous = c
		}
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return err
	}
	defer f.Close()
	presentation, err := w.newConditionalRead(ctx, idx, f, options)
	if err != nil {
		return err
	}
	if !byRow {
		options = prepareRichRead(options)
	}
	stream := indexedStream{f: f, idx: idx}
	for ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		number, position := ref.Number, ref.Position
		if number < 1 || number > MaxRowsForIndex(idx) {
			return errors.New("源行号超出允许范围")
		}
		if position < -1 || position >= idx.RowCount() || position >= 0 && idx.RowAt(position).Number != number {
			return errors.New("源行号与索引位置不匹配")
		}
		if position == -1 && !knownBlanks {
			i := idx.FindRowOffset(number)
			if i < idx.RowCount() && idx.RowAt(i).Number == number {
				return errors.New("已有源记录不能伪装成空白行")
			}
		}
		row := RowData{Number: number}
		if position >= 0 {
			row, err = stream.readWithOptions(w, position, first, last, selected, options)
			if err != nil {
				return err
			}
		}
		if presentation != nil {
			if byRow {
				presentation.resetBudget()
			}
			if err := presentation.apply(&row, first, last, selected); err != nil {
				return err
			}
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return ctx.Err()
}
