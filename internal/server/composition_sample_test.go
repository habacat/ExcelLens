package server

import (
	"context"
	"testing"

	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestCompositionTypeSamplesUseOriginalEligibilityAndStopAfterEightRows(t *testing.T) {
	book := &workbook.Workbook{Format: "xlsx"}
	info := compositionSourceInfo{Fields: []composition.Field{{Column: 1}, {Column: 2}, {Column: 3}, {Column: 4}}}
	visits := 0
	source := composition.Source{Check: func(ctx context.Context) error { return ctx.Err() }, Scan: func(ctx context.Context, columns []int, visit func(composition.SourceRow) error) error {
		for i := 0; i < 1000; i++ {
			visits++
			if err := visit(composition.SourceRow{Number: i + 1, Cells: []workbook.CellData{{Col: 1, Raw: "123.45", Type: "inlineStr"}, {Col: 2, Raw: "123.45", Type: "n"}, {Col: 3, Raw: "2026-11-16", Type: "inlineStr"}, {Col: 4, HasFormula: true, CacheMissing: true}}}); err != nil {
				return err
			}
		}
		return nil
	}}
	if err := compositionFieldSamples(context.Background(), book, source, &info); err != nil {
		t.Fatal(err)
	}
	if visits != formats.CompositionTypeSampleRows || info.Fields[0].SampleType != "text" || info.Fields[1].SampleType != "number" || info.Fields[2].SampleType != "text" || info.Fields[3].SampleType != "cache" {
		t.Fatal("sampling inferred native amounts/dates or read beyond its bound", visits, info.Fields)
	}
	book.Format = "csv"
	if err := compositionFieldSamples(context.Background(), book, source, &info); err != nil || info.Fields[0].SampleType != "number" {
		t.Fatal("CSV amount eligibility diverged", err, info.Fields)
	}
	book.Format = "sqlite"
	if err := compositionFieldSamples(context.Background(), book, source, &info); err != nil || info.Fields[0].SampleType != "unknown" {
		t.Fatal("SQLite's missing original storage type was guessed", err, info.Fields)
	}
}
