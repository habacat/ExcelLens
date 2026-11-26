package rowindex

import (
	"context"
	"slices"
	"testing"

	"excellens/internal/formats"
)

func TestProjectionRangeIterationAcrossMappedMixedUniformAndEmptyBlocks(t *testing.T) {
	const n = 20_000
	offsets := make([]RowOffset, 0, n)
	positions := make([]int, n)
	var states []SourceAxisRange
	for i := 0; i < n; i++ {
		offsets = append(offsets, RowOffset{Number: i + 1})
		positions[i] = i
		if i >= 4096 && i < 8192 && i%3 == 0 || i >= 8192 && i < 12288 {
			states = append(states, SourceAxisRange{Min: i + 1, Max: i + 1, Hidden: true, HiddenSet: true})
		}
	}
	idx := NewRowIndex(offsets)
	axis, err := BuildAxisVisibility(context.Background(), AxisLayoutInput{RowStates: states}, "row", n, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := testRowOrder(t, positions, n, 8)
	p, err := newRowProjection(context.Background(), t.TempDir(), &idx, n, base, axis, 0, nil, rowProjectionHeaderBytes, &QueryQuota{max: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.data.mapped {
		t.Fatal("mapped range path not exercised")
	}
	want := []int{}
	for i := 0; i < n; i++ {
		if axis.Visible(i + 1) {
			want = append(want, i)
		}
	}
	for _, span := range [][2]int{{0, 0}, {0, 1}, {4090, 80}, {5000, 4000}, {len(want) - 3, 3}, {len(want), 0}, {0, len(want)}} {
		if got := slices.Collect(p.IterRange(span[0], span[1])); !slices.Equal(got, want[span[0]:span[0]+span[1]]) {
			t.Fatal("range differs across projection block kinds", span)
		}
	}
	for range p.IterRange(4090, 2000) {
		break
	}
	// A billion-coordinate interval and a tail seek stay compressed; this is
	// an address-domain check, not a billion-row workbook benchmark.
	sparse, err := BuildAxisVisibility(context.Background(), AxisLayoutInput{RowStates: []SourceAxisRange{{Min: 2, Max: formats.ExternalRows - 1, Hidden: true, HiddenSet: true}}}, "row", formats.ExternalRows, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(sparse.IterRange(1, 1)); !slices.Equal(got, []int{formats.ExternalRows}) {
		t.Fatal(got)
	}
}
