package workbook

import (
	"context"
	"iter"
	"os"
)

// VisitRowsContext keeps a single file handle and a bounded read-ahead buffer
// for explicit large selections. Positions may be streamed without allocating
// a million-element row array. Values are raw; no display float conversion.
func (w *Workbook) VisitRowsContext(ctx context.Context, idx *SheetIndex, positions iter.Seq[int], first, last int, visit func(RowData) error) error {
	return w.VisitRowsWithOptionsContext(ctx, idx, positions, first, last, RowReadOptions{}, visit)
}

type RowReadOptions struct {
	FormatValue              bool
	IncludeFormula           bool
	RichText                 bool
	ConditionalFormats       bool
	ConditionalStatsReadOnly bool
	ConditionalStatsStatus   string
	Columns                  []int
	richBudget               *richReadBudget
}

// VisitRowsWithOptionsContext streams source rows with one descriptor and a
// bounded read-ahead buffer. Export may request saved display/formula metadata;
// calculation continues to use the raw-value wrapper above.
func (w *Workbook) VisitRowsWithOptionsContext(ctx context.Context, idx *SheetIndex, positions iter.Seq[int], first, last int, options RowReadOptions, visit func(RowData) error) error {
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return err
	}
	defer f.Close()
	presentation, err := w.newConditionalRead(ctx, idx, f, options)
	if err != nil {
		return err
	}
	defer presentation.closeStatistics()
	var selected map[int]struct{}
	if len(options.Columns) > 0 {
		selected = make(map[int]struct{}, len(options.Columns))
		for _, col := range options.Columns {
			selected[col] = struct{}{}
		}
	}
	stream := indexedStream{f: f, idx: idx}
	for position := range positions {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := stream.readWithOptions(w, position, first, last, selected, options)
		if err != nil {
			return err
		}
		if presentation != nil {
			presentation.resetBudget()
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
