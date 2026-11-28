package server

import (
	"context"
	"encoding/json"

	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func editFillTargets(layout editfile.Layout, first editdoc.Address, end string) ([]editdoc.Address, error) {
	last, err := editAddress(first.Sheet, end)
	if err != nil || !layout.Contains(first) || !layout.Contains(last) {
		return nil, editfile.ErrTarget
	}
	rows, cols := last.Row-first.Row, last.Col-first.Col
	count := max(rows, -rows) + max(cols, -cols)
	if rows != 0 && cols != 0 || count < 1 || count > formats.EditBatchCells {
		return nil, i18n.Error("service.editFill.chooseFillEndPointWithin")
	}
	targets := make([]editdoc.Address, count)
	for i := range targets {
		targets[i] = editdoc.Address{Sheet: first.Sheet, Row: first.Row + rows/count*(i+1), Col: first.Col + cols/count*(i+1)}
	}
	return targets, nil
}

func planEditFill(ctx context.Context, layout editfile.Layout, f *workbook.EditSourceFile, first editdoc.Address, start editdoc.Value, targets []editdoc.Address, req editRequest) (editBatchPreview, error) {
	count := len(targets)
	var values []editdoc.Value
	var endValue string
	var err error
	if req.Mode == "formula-fill" {
		if layout.XLSX == nil || start.Kind != "formula" {
			return editBatchPreview{}, i18n.Error("service.editFill.startingPointRequiresSupportedRegular")
		}
		formula, err := layout.XLSX.FormulaForFill(ctx, start.Raw)
		if err != nil {
			return editBatchPreview{}, err
		}
		values = make([]editdoc.Value, count)
		bytes := 1024
		for i, target := range targets {
			if err := ctx.Err(); err != nil {
				return editBatchPreview{}, err
			}
			raw, err := formula.Shift(target.Row-first.Row, target.Col-first.Col)
			if err != nil {
				return editBatchPreview{}, err
			}
			values[i] = editdoc.Value{Kind: "formula", Raw: raw}
			encoded, _ := json.Marshal(values[i])
			bytes += len(encoded) + 100 + len(first.Sheet)
			if bytes > formats.EditRequestBytes {
				return editBatchPreview{}, errEditRequestCapacity
			}
		}
		endValue = values[count-1].Raw
	} else {
		values, endValue, err = editfile.CalendarFill(ctx, f, start, req.Mode, req.Step, count)
		if err != nil {
			return editBatchPreview{}, err
		}
	}
	preview := editBatchPreview{Changes: make([]editdoc.Change, count), Last: targets[count-1], LastValue: endValue}
	for i, v := range values {
		preview.Changes[i] = editdoc.Change{Address: targets[i], Value: v}
	}
	return preview, nil
}
