package workbook

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestSourceRowSnapshotKeepsSparseBlanksTypesAndIndependentRangeLease(t *testing.T) {
	w, idx := presentationFixture(t)
	numbers := []int{3, 2, 1, 7}
	snapshot, err := BuildSourceRowSnapshot(context.Background(), t.TempDir(), idx, len(numbers), func(ctx context.Context, add func(int) error) error {
		for _, number := range numbers {
			if err := add(number); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := snapshot.Retain()
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Retain(); err == nil {
		t.Fatal("closed owner retained a snapshot")
	}
	options := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true}
	want, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, numbers, 1, 5, options)
	if err != nil {
		t.Fatal(err)
	}
	var got []RowData
	if err := w.VisitRowsByReferencesWithOptionsContext(context.Background(), idx, borrowed.Rows(), 1, 5, options, func(row RowData) error { got = append(got, row); return nil }); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot changed sparse rows or source values/types", got, want, err)
	}
	var refs []SourceRowReference
	for ref := range borrowed.RowsRange(1, 2) {
		refs = append(refs, ref)
	}
	if !reflect.DeepEqual(refs, []SourceRowReference{{Number: 2, Position: 1}, {Number: 1, Position: 0}}) || borrowed.Index() != idx || borrowed.Len() != 4 {
		t.Fatal("ordinal range changed the source coordinates", refs)
	}
}

func TestSourceRowSnapshotRejectsCountDuplicateBoundsAndIgnoredErrors(t *testing.T) {
	idx := &SheetIndex{SourceFormat: "csv"}
	for _, test := range []struct {
		name    string
		count   int
		numbers []int
	}{
		{"short", 2, []int{1}}, {"long", 1, []int{1, 2}},
		{"duplicate", 3, []int{3, 1, 3}}, {"zero", 1, []int{0}},
		{"overflow", 1, []int{MaxRowsForIndex(idx) + 1}}, {"negative-count", -1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			got, err := BuildSourceRowSnapshot(context.Background(), dir, idx, test.count, func(ctx context.Context, add func(int) error) error {
				for _, number := range test.numbers {
					_ = add(number) // admission failure must stay sticky
				}
				return nil
			})
			if got != nil || err == nil || QueryFilesInUse(dir) {
				t.Fatal("invalid sequence published a candidate or leaked files", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := BuildSourceRowSnapshot(ctx, t.TempDir(), idx, 0, func(context.Context, func(int) error) error { return nil }); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled empty sequence published a snapshot", got, err)
	}
}

func TestSourceRowSnapshotMappedReverseRowsReleaseOnlyLastBorrower(t *testing.T) {
	dir := t.TempDir()
	idx := &SheetIndex{SourceFormat: "csv"}
	const count = residentOrderRows + 1
	snapshot, err := BuildSourceRowSnapshot(context.Background(), dir, idx, count, func(ctx context.Context, add func(int) error) error {
		for row := count; row > 0; row-- {
			if err := add(row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := snapshot.Retain()
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	if err := snapshot.Close(); err != nil || !QueryFilesInUse(dir) {
		t.Fatal("source coordinates lost their mapped reservation", err)
	}
	var tail []SourceRowReference
	for ref := range borrowed.RowsRange(count-3, 3) {
		tail = append(tail, ref)
	}
	if !reflect.DeepEqual(tail, []SourceRowReference{{Number: 3, Position: -1}, {Number: 2, Position: -1}, {Number: 1, Position: -1}}) {
		t.Fatal(tail)
	}
	if err := borrowed.Close(); err != nil || QueryFilesInUse(dir) {
		t.Fatal("last coordinate snapshot leaked its mapping", err)
	}
}
