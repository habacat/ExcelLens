package server

import (
	"context"
	"errors"
	"slices"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var errCompositionSampleDone = errors.New("composition field sample complete")

// Only an explicit field-preparation task samples values. Reuse the same
// source-type/numeric classification as guarded analysis plans; no text dates,
// full-column types or source conversions are inferred from eight records.
func compositionFieldSamples(ctx context.Context, book *workbook.Workbook, source composition.Source, info *compositionSourceInfo) error {
	fields := info.Fields[:min(len(info.Fields), formats.CompositionColumns)]
	columns := make([]int, len(fields))
	slots := make(map[int]int, len(fields))
	for i, field := range fields {
		columns[i] = field.Column
		slots[field.Column] = i
	}
	slices.Sort(columns)
	masks := make([]recipeTypeMask, len(fields))
	rows := 0
	err := source.Scan(ctx, columns, func(row composition.SourceRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows++
		for _, cell := range row.Cells {
			slot, exists := slots[cell.Col]
			if !exists {
				continue
			}
			numeric := sourceCellNumeric(book, cell)
			format := book.Format
			if strings.HasPrefix(info.Ref.Source, "scratch:") {
				numeric = analysis.IsNumericLiteral(cell.Raw)
				format = "scratch"
			}
			masks[slot] |= recipeCellMask(format, cell, numeric)
		}
		if rows >= formats.CompositionTypeSampleRows {
			return errCompositionSampleDone
		}
		return nil
	})
	if err != nil && !errors.Is(err, errCompositionSampleDone) {
		return err
	}
	if err := source.Check(ctx); err != nil {
		return err
	}
	for i, mask := range masks {
		fields[i].Sampled = true
		fields[i].SampleType = compositionSampleType(mask)
	}
	return ctx.Err()
}

func compositionSampleType(mask recipeTypeMask) string {
	if mask&(recipeUnknown|recipeTypeMissing) != 0 {
		return "unknown"
	}
	if mask&(recipeNumber|recipeAmountText) != 0 {
		mask &^= recipeAmountText
		mask |= recipeNumber
	}
	switch mask {
	case 0:
		return "empty"
	case recipeNumber:
		return "number"
	case recipeText:
		return "text"
	case recipeBoolean:
		return "boolean"
	case recipeDate:
		return "date"
	case recipeError:
		return "error"
	case recipeMissingCache:
		return "cache"
	case recipeInvalidNumber:
		return "invalid"
	default:
		return "mixed"
	}
}
