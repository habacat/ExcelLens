package workbook

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"excellens/internal/formats"
)

func projectionFixture(t *testing.T, n int) (*SheetIndex, *AxisVisibility, []int) {
	t.Helper()
	idx := &SheetIndex{MaxRow: n * 3, MaxCol: 2}
	var states []SourceAxisRange
	for i := 0; i < n; i++ {
		number := i*3 + 1
		idx.Rows = append(idx.Rows, RowOffset{Number: number})
		if i%4 == 1 {
			states = append(states, SourceAxisRange{Min: number, Max: number, HiddenSet: true, Hidden: true})
		}
	}
	view, err := (SourceLayout{RowStates: states}).BuildAxisVisibility(context.Background(), "row", n*3, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	positions := rand.New(rand.NewSource(20261115)).Perm(n)
	return idx, view, positions
}

func TestRowProjectionPreservesSortedSourceCoordinatesWithoutDuplicatingReverseIndex(t *testing.T) {
	idx, visibility, positions := projectionFixture(t, 1000)
	positions = positions[:983]
	for _, resident := range []int{formats.RowProjectionMemoryBytes, rowProjectionHeaderBytes} {
		base, baseQuota := testRowOrder(t, positions, idx.RowCount(), 8)
		quota := &queryQuota{max: 1 << 20}
		view, err := newRowProjection(context.Background(), t.TempDir(), idx, base, visibility, 10, nil, resident, quota)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = view.Close() })
		var want []int
		for _, source := range positions {
			number := idx.RowAt(source).Number
			if number > 10 && source%4 != 1 {
				want = append(want, source)
			}
		}
		if view.Len() != len(want) || view.SourceRows() != idx.RowCount() || !slices.Equal(slices.Collect(view.Iter()), want) {
			t.Fatal("view changed sort/source order", view.Len(), len(want))
		}
		for visual, source := range want {
			if view.At(visual) != source {
				t.Fatal(visual, source)
			}
			if at, ok := view.Position(source); !ok || at != visual {
				t.Fatal("reverse mapping drifted", source, at, ok)
			}
		}
		for source := 0; source < idx.RowCount(); source++ {
			_, found := view.Position(source)
			if found != slices.Contains(want, source) {
				t.Fatal("hidden/unmatched membership changed", source, found)
			}
		}
		page, err := view.Page(view.Len()-3, 100)
		if err != nil || !slices.Equal(page, want[len(want)-3:]) {
			t.Fatal(page, err)
		}
		if view.data.base.data != base.data {
			t.Fatal("projection duplicated/replaced the source reverse index")
		}
		if resident == rowProjectionHeaderBytes && (!view.data.mapped || view.data.file == nil || quota.used.Load() == 0) {
			t.Fatal("forced mapped path was not exercised")
		}
		if resident == formats.RowProjectionMemoryBytes && (view.data.mapped || quota.used.Load() != 0) {
			t.Fatal("small mask unnecessarily used disk")
		}
		snapshot, err := view.Retain()
		if err != nil {
			t.Fatal(err)
		}
		if err := base.Close(); err != nil {
			t.Fatal(err)
		}
		if err := view.Close(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		if !slices.Equal(slices.Collect(snapshot.Iter()), want) || baseQuota.used.Load() == 0 {
			t.Fatal("clearing an owner released another owner's source rows")
		}
		if err := snapshot.Close(); err != nil {
			t.Fatal(err)
		}
		if quota.used.Load() != 0 || baseQuota.used.Load() != 0 {
			t.Fatal("last snapshot leaked mask/source query storage", quota.used.Load(), baseQuota.used.Load())
		}
	}
}

func TestRowProjectionIdentityEmptyAndActiveCancellationDoNotLeakFiles(t *testing.T) {
	idx, visibility, positions := projectionFixture(t, 6000)
	base, _ := testRowOrder(t, positions, idx.RowCount(), residentOrderRows)
	shown, err := (SourceLayout{}).BuildAxisVisibility(context.Background(), "row", idx.MaxRow, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := (SourceLayout{}).BuildAxisVisibility(context.Background(), "row", idx.MaxRow, AxisViewSettings{Mode: "hide-all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []*AxisVisibility{shown, hidden} {
		quota := &queryQuota{max: 1}
		dir := filepath.Join(t.TempDir(), "never-created")
		p, err := newRowProjection(context.Background(), dir, idx, base, v, 0, nil, rowProjectionHeaderBytes, quota)
		if err != nil {
			t.Fatal(err)
		}
		if p.Len() != 0 && !p.data.identity {
			t.Fatal("all-visible view was not implicit")
		}
		if p.Len() == 0 && p.data.base != nil {
			t.Fatal("empty view unnecessarily retained source storage")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("uniform projection created query files", err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"cancel", "quota"} {
		dir := t.TempDir()
		quota := &queryQuota{max: 1 << 20}
		ctx, cancel := context.WithCancel(context.Background())
		done := 0
		progress := func(n, total int) {
			if total != len(positions) {
				t.Fatal(total)
			}
			done = n
			if phase == "cancel" && n > 0 {
				cancel()
			}
		}
		if phase == "quota" {
			quota.max = rowProjectionHeaderBytes + 1
		}
		p, err := newRowProjection(ctx, dir, idx, base, visibility, 0, progress, rowProjectionHeaderBytes, quota)
		cancel()
		if p != nil || err == nil || phase == "cancel" && (!errors.Is(err, context.Canceled) || done != rowProjectionBlockRows) {
			t.Fatal("in-flight build did not fail atomically", phase, p, done, err)
		}
		if quota.used.Load() != 0 {
			t.Fatal("failed projection retained charged bytes", phase, quota.used.Load())
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 0 {
			t.Fatal("failed projection left query files", files, err)
		}
		if base.At(0) != positions[0] {
			t.Fatal("failed projection changed its input")
		}
	}
	wrong := *idx
	wrong.Rows = wrong.Rows[:len(wrong.Rows)-1]
	if _, err := NewRowProjection(context.Background(), t.TempDir(), &wrong, base, visibility, 0, nil); err == nil {
		t.Fatal("different source index accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRowProjection(ctx, t.TempDir(), idx, base, visibility, 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRowProjectionParallelRetainedConsumersSurviveOwnerRemoval(t *testing.T) {
	idx, visibility, positions := projectionFixture(t, 9000)
	base, _ := testRowOrder(t, positions, idx.RowCount(), 8)
	quota := &queryQuota{max: 1 << 20}
	p, err := newRowProjection(context.Background(), t.TempDir(), idx, base, visibility, 0, nil, rowProjectionHeaderBytes, quota)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Collect(p.Iter())
	var copies []*RowProjection
	for i := 0; i < 4; i++ {
		copy, err := p.Retain()
		if err != nil {
			t.Fatal(err)
		}
		copies = append(copies, copy)
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, copy := range copies {
		wg.Go(func() {
			defer copy.Close()
			for pass := 0; pass < 3; pass++ {
				for i := 0; i < copy.Len(); i += 17 {
					if copy.At(i) != want[i] {
						t.Error("concurrent view drifted")
						return
					}
				}
				if !slices.Equal(slices.Collect(copy.Iter()), want) {
					t.Error("concurrent stream drifted")
					return
				}
			}
		})
	}
	wg.Wait()
	if quota.used.Load() != 0 {
		t.Fatal("concurrent snapshots leaked mask storage", quota.used.Load())
	}
}
