package workbook

import (
	"context"
	"iter"
	"reflect"
	"testing"
)

func TestSourceReferenceReaderPreservesPresentationAndRejectsFalseBlanksOrPositions(t *testing.T) {
	w, idx := presentationFixture(t)
	options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true}
	want, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2, 3, 1}, 1, 5, options)
	if err != nil {
		t.Fatal(err)
	}
	refs := []SourceRowReference{{Number: 2, Position: 1}, {Number: 3, Position: -1}, {Number: 1, Position: 0}}
	sequence := func(yield func(SourceRowReference) bool) {
		for _, ref := range refs {
			if !yield(ref) {
				return
			}
		}
	}
	var got []RowData
	if err := w.VisitRowsByReferencesWithOptionsContext(context.Background(), idx, sequence, 1, 5, options, func(row RowData) error { got = append(got, row); return nil }); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("reference reader changed source types/values/presentation/order", got, want, err)
	}
	for _, ref := range []SourceRowReference{{Number: 1, Position: -1}, {Number: 1, Position: 1}, {Number: 1, Position: 2}, {Number: 1, Position: -2}, {Number: 0, Position: 0}} {
		bad := func(yield func(SourceRowReference) bool) { yield(ref) }
		if err := w.VisitRowsByReferencesWithOptionsContext(context.Background(), idx, bad, 1, 5, options, func(RowData) error { return nil }); err == nil {
			t.Fatal("invalid source reference accepted", ref)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.VisitRowsByReferencesWithOptionsContext(ctx, idx, iter.Seq[SourceRowReference](sequence), 1, 5, options, func(RowData) error { t.Fatal("cancelled reader emitted a row"); return nil }); err != context.Canceled {
		t.Fatal(err)
	}
}
