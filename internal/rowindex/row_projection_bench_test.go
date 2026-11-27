package rowindex

import (
	"context"
	"testing"
)

// Exercise actual block decoding, popcount and selection with a mixed bitmap.
func BenchmarkProjectionBitmap(b *testing.B) {
	const n = 65536
	offsets := make([]RowOffset, n)
	positions := make([]int, n)
	var states []SourceAxisRange
	for i := range offsets {
		offsets[i].Number, positions[i] = i+1, i
		if i%4 == 3 {
			states = append(states, SourceAxisRange{Min: i + 1, Max: i + 1, HiddenSet: true, Hidden: true})
		}
	}
	dir := b.TempDir()
	ctx := context.Background()
	idx := NewRowIndex(offsets)
	visible, err := BuildAxisVisibility(ctx, AxisLayoutInput{RowStates: states}, "row", n, AxisViewSettings{})
	if err != nil {
		b.Fatal(err)
	}
	base, err := NewRowOrder(ctx, dir, n, positions)
	if err != nil {
		b.Fatal(err)
	}
	defer base.Close()
	view, err := NewRowProjection(ctx, dir, &idx, n, base, visible, 0, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer view.Close()
	b.ReportAllocs()
	for b.Loop() {
		for i := 0; i < view.Len(); i += 47 {
			src := view.At(i)
			if pos, ok := view.Position(src); !ok || pos != i || src != i+i/3 {
				b.Fatal(i, src, pos, ok)
			}
		}
	}
}
