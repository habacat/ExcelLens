package server

import (
	"context"
	"fmt"
	"testing"

	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

func TestCompositionPreviewDoesNotDecodeTheRowAfterItsVisibleLimit(t *testing.T) {
	definition := sourceview.Append{Version: sourceview.AppendVersion, Name: "预览", Fields: []string{"值"}, Inputs: []sourceview.AppendInput{
		{Ref: sourceview.TableRef{Book: "a", Fingerprint: "a", Source: "wb:one", Stamp: "a"}, Columns: []int{1}},
		{Ref: sourceview.TableRef{Book: "b", Fingerprint: "b", Source: "wb:two", Stamp: "b"}, Columns: []int{1}},
	}}
	reads := 0
	sources := make([]composition.Source, 2)
	for i := range sources {
		sources[i] = composition.Source{Ref: definition.Inputs[i].Ref, Format: "csv", Fields: []composition.Field{{Column: 1}}, Rows: 200, MaxRow: 200, MaxCol: 1, Check: func(context.Context) error { return nil }, Scan: func(ctx context.Context, columns []int, visit func(composition.SourceRow) error) error {
			for row := 1; row <= 200; row++ {
				reads++
				if reads > formats.CompositionPreviewRows {
					return fmt.Errorf("preview decoded a row outside its limit")
				}
				if err := visit(composition.SourceRow{Number: row, Position: row - 1, Cells: []workbook.CellData{{Col: 1, Raw: "0.10", Display: "0.10"}}}); err != nil {
					return err
				}
			}
			return ctx.Err()
		}}
	}
	plan, err := composition.BindAppend(definition, sources)
	if err != nil {
		t.Fatal(err)
	}
	source := ownCompositionSnapshot(&compositionSnapshotData{plan: plan})
	defer source.close()
	preview, truncated, err := compositionPreview(context.Background(), source)
	if err != nil || !truncated || len(preview) != formats.CompositionPreviewRows || reads != formats.CompositionPreviewRows {
		t.Fatal("bounded preview read one extra source record", len(preview), reads, truncated, err)
	}
}
