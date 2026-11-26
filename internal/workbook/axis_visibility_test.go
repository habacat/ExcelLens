package workbook

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"testing"

	"excellens/internal/formats"
)

func outlineVisibilityFixture() SourceLayout {
	return SourceLayout{RowStates: []SourceAxisRange{
		{Min: 1, Max: 1, HiddenSet: true},
		{Min: 2, Max: 3, Hidden: true, HiddenSet: true, Level: 1},
		{Min: 4, Max: 5, Hidden: true, HiddenSet: true, Level: 2},
		{Min: 6, Max: 6, Hidden: true, HiddenSet: true, Level: 1, Collapsed: true},
		{Min: 7, Max: 8, Hidden: true, HiddenSet: true, Level: 1},
		{Min: 9, Max: 9, Collapsed: true},
		{Min: 12, Max: 13, Level: 1},
		{Min: 20, Max: 20, Hidden: true, HiddenSet: true},
	}}
}

func TestAxisVisibilityNestedOutlinesKeepSourceFlagsAndAncestorCollapse(t *testing.T) {
	source := outlineVisibilityFixture()
	before := slices.Clone(source.RowStates)
	parent, child := AxisOutlineKey{Min: 2, Max: 8, Level: 1}, AxisOutlineKey{Min: 4, Max: 5, Level: 2}
	depth0, depth1, depth7 := uint8(0), uint8(1), uint8(7)
	for _, tc := range []struct {
		name     string
		settings AxisViewSettings
		hidden   []AxisInterval
	}{
		{"original", AxisViewSettings{}, []AxisInterval{{Min: 2, Max: 8}, {Min: 20, Max: 20}}},
		{"parent expands, child stays closed", AxisViewSettings{Groups: []AxisOutlineOverride{{AxisOutlineKey: parent, Collapsed: false}}}, []AxisInterval{{Min: 4, Max: 5}, {Min: 20, Max: 20}}},
		{"child does not open closed ancestor", AxisViewSettings{Groups: []AxisOutlineOverride{{AxisOutlineKey: child, Collapsed: false}}}, []AxisInterval{{Min: 2, Max: 8}, {Min: 20, Max: 20}}},
		{"both expand", AxisViewSettings{Groups: []AxisOutlineOverride{{AxisOutlineKey: child, Collapsed: false}, {AxisOutlineKey: parent, Collapsed: false}}}, []AxisInterval{{Min: 20, Max: 20}}},
		{"ancestor collapse wins", AxisViewSettings{Groups: []AxisOutlineOverride{{AxisOutlineKey: parent, Collapsed: true}, {AxisOutlineKey: child, Collapsed: false}}}, []AxisInterval{{Min: 2, Max: 8}, {Min: 20, Max: 20}}},
		{"temporary revealed row", AxisViewSettings{Ranges: []AxisVisibilityOverride{{Min: 5, Max: 5, Hidden: false}}}, []AxisInterval{{Min: 2, Max: 4}, {Min: 6, Max: 8}, {Min: 20, Max: 20}}},
		{"summary level", AxisViewSettings{Depth: &depth0}, []AxisInterval{{Min: 2, Max: 8}, {Min: 12, Max: 13}, {Min: 20, Max: 20}}},
		{"first detail level", AxisViewSettings{Depth: &depth1}, []AxisInterval{{Min: 4, Max: 5}, {Min: 20, Max: 20}}},
		{"all outline levels", AxisViewSettings{Depth: &depth7}, []AxisInterval{{Min: 20, Max: 20}}},
		{"show source hidden rows", AxisViewSettings{Mode: "show-all"}, nil},
		{"hide all except explicit range", AxisViewSettings{Mode: "hide-all", Ranges: []AxisVisibilityOverride{{Min: 9, Max: 10, Hidden: false}}}, []AxisInterval{{Min: 1, Max: 8}, {Min: 11, Max: 24}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := source.BuildAxisVisibility(context.Background(), "row", 24, tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v.HiddenRanges(), tc.hidden) {
				t.Fatal(v.HiddenRanges(), tc.hidden)
			}
			groups := v.Groups()
			if len(groups) != 3 || groups[0].AxisOutlineKey != parent || groups[0].Summary != 9 || groups[1].AxisOutlineKey != child || groups[1].Summary != 6 || groups[1].Parent != 1 || !groups[0].SourceCollapsed || !groups[1].SourceCollapsed {
				t.Fatal("outline tree drifted", groups)
			}
			var visible []int
			for n := 1; n <= 24; n++ {
				if !slices.ContainsFunc(tc.hidden, func(r AxisInterval) bool { return r.Min <= n && r.Max >= n }) {
					visible = append(visible, n)
				}
			}
			if v.Len() != len(visible) {
				t.Fatal(v.Len(), visible)
			}
			for i, n := range visible {
				if v.At(i) != n {
					t.Fatal(i, n, v.At(i))
				}
				if at, ok := v.Position(n); !ok || at != i {
					t.Fatal(n, at, ok)
				}
			}
			for n := 0; n <= 25; n++ {
				want := sort.SearchInts(visible, n)
				if v.Before(n) != want {
					t.Fatal("prefix", n, v.Before(n), want)
				}
			}
			for _, g := range groups {
				want := 0
				for _, n := range visible {
					if n >= g.Min && n <= g.Max {
						want++
					}
				}
				if g.Visible != want {
					t.Fatal("group count", g, want)
				}
			}
			copy := v.HiddenRanges()
			if len(copy) > 0 {
				copy[0].Min = 999
			}
			groups[0].Min = 999
			if v.Groups()[0].Min == 999 || slices.ContainsFunc(v.HiddenRanges(), func(r AxisInterval) bool { return r.Min == 999 }) {
				t.Fatal("caller mutated shared view")
			}
		})
	}
	if !reflect.DeepEqual(source.RowStates, before) {
		t.Fatal("view settings rewrote source metadata")
	}
}

func TestAxisVisibilityBillionRowDefaultHiddenAndBoundaryRankSelectStayCompressed(t *testing.T) {
	source := SourceLayout{ZeroHeight: true, RowStates: []SourceAxisRange{{Min: 1, Max: 1, HiddenSet: true}, {Min: 500_000_000, Max: 500_000_000, HiddenSet: true}, {Min: formats.ExternalRows, Max: formats.ExternalRows, HiddenSet: true}}}
	v, err := source.BuildAxisVisibility(context.Background(), "row", formats.ExternalRows, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Len() != 3 || len(v.HiddenRanges()) != 2 || len(v.Groups()) != 0 {
		t.Fatal("expanded the billion-row domain", v.Len(), v.HiddenRanges())
	}
	page, err := v.Page(0, 5)
	if err != nil || !slices.Equal(page, []int{1, 500_000_000, formats.ExternalRows}) {
		t.Fatal(page, err)
	}
	if v.Next(2) != 500_000_000 || v.Previous(formats.ExternalRows-1) != 500_000_000 || v.Next(formats.ExternalRows+1) != 0 || v.Previous(0) != 0 || v.Visible(0) || v.Visible(formats.ExternalRows+1) {
		t.Fatal("boundary/gap lookup changed")
	}
	for _, n := range []int{2, 400_000_000, 999_999_999} {
		if at, ok := v.Position(n); ok || at < 1 || at > 2 {
			t.Fatal(n, at, ok)
		}
	}
	zero := SourceLayout{DefaultRowZeroSize: true, RowStates: []SourceAxisRange{{Min: 1, Max: 1, HiddenSet: true, ZeroSize: true}}}
	all, err := zero.BuildAxisVisibility(context.Background(), "row", formats.ExternalRows, AxisViewSettings{})
	if err != nil || all.Len() != 0 || len(all.HiddenRanges()) != 1 || all.Next(1) != 0 || all.Previous(formats.ExternalRows) != 0 {
		t.Fatal("all-hidden domain fabricated a visible row", err)
	}
}

func TestAxisVisibilitySummaryAboveColumnDefaultsAndIncompleteMetadata(t *testing.T) {
	no := false
	source := SourceLayout{SummaryRight: &no, DefaultColZeroSize: true, Cols: map[int]float64{1: 70, 5: 80}, ColStates: []SourceAxisRange{{Min: 1, Max: 1, Collapsed: true, HiddenSet: true}, {Min: 2, Max: 4, Level: 1}, {Min: 5, Max: 5, HiddenSet: true}}}
	v, err := source.BuildAxisVisibility(context.Background(), "col", formats.ExcelColumns, AxisViewSettings{})
	if err != nil || v.Len() != 2 || !v.Visible(1) || !v.Visible(5) || v.Groups()[0].Summary != 1 || !v.Groups()[0].SourceCollapsed {
		t.Fatal(v, err)
	}
	source.StateNotice = "源行列状态超过读取容量"
	if _, err := source.BuildAxisVisibility(context.Background(), "col", formats.ExcelColumns, AxisViewSettings{}); err == nil {
		t.Fatal("incomplete source flags became complete")
	}
	shown, err := source.BuildAxisVisibility(context.Background(), "col", formats.ExcelColumns, AxisViewSettings{Mode: "show-all"})
	if err != nil || shown.Len() != formats.ExcelColumns || shown.Notice() != source.StateNotice || len(shown.Groups()) != 0 {
		t.Fatal("explicit complete-value fallback lost cause", err)
	}
}

func TestAxisVisibilityDefaultZeroSizeRequiresPositiveSourceSizeOrExplicitViewReveal(t *testing.T) {
	source := SourceLayout{DefaultRowZeroSize: true, RowStates: []SourceAxisRange{{Min: 1, Max: 1, HiddenSet: true}, {Min: 3, Max: 3, HiddenSet: true}, {Min: 4, Max: 4, Hidden: true, HiddenSet: true}}, Rows: []SourceSizeRange{{Min: 3, Max: 4, Size: 20}}}
	v, err := source.BuildAxisVisibility(context.Background(), "row", 10, AxisViewSettings{})
	if err != nil || v.Len() != 1 || v.At(0) != 3 || v.Visible(1) || v.Visible(4) {
		t.Fatal("visible flag invented a nonzero source height", v, err)
	}
	shown, err := source.BuildAxisVisibility(context.Background(), "row", 10, AxisViewSettings{Ranges: []AxisVisibilityOverride{{Min: 1, Max: 1, Hidden: false}}})
	if err != nil || shown.Len() != 2 || !shown.Visible(1) {
		t.Fatal("explicit view reveal failed", err)
	}
}

func TestAxisVisibilitySettingsRejectStaleGroupsOverlapsLimitsAndCancelWithoutMutation(t *testing.T) {
	depth := uint8(8)
	for _, settings := range []AxisViewSettings{
		{Mode: "bad"}, {Depth: &depth}, {Ranges: []AxisVisibilityOverride{{Min: 1, Max: 3, Hidden: true}, {Min: 3, Max: 5, Hidden: false}}}, {Ranges: []AxisVisibilityOverride{{Min: 0, Max: 2, Hidden: true}}},
		{Groups: []AxisOutlineOverride{{AxisOutlineKey: AxisOutlineKey{Min: 2, Max: 8, Level: 2}, Collapsed: false}}}, {Groups: []AxisOutlineOverride{{AxisOutlineKey: AxisOutlineKey{Min: 2, Max: 8, Level: 1}, Collapsed: false}, {AxisOutlineKey: AxisOutlineKey{Min: 2, Max: 8, Level: 1}, Collapsed: true}}},
		{Ranges: make([]AxisVisibilityOverride, formats.AxisVisibilityOverrides+1)},
	} {
		if _, err := outlineVisibilityFixture().BuildAxisVisibility(context.Background(), "row", 24, settings); err == nil {
			t.Fatal("invalid/stale settings accepted", settings)
		}
	}
	bad := SourceLayout{RowStates: []SourceAxisRange{{Min: 1, Max: 4}, {Min: 3, Max: 7}}}
	if _, err := bad.BuildAxisVisibility(context.Background(), "row", 10, AxisViewSettings{}); err == nil {
		t.Fatal("overlapping source metadata accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := outlineVisibilityFixture().BuildAxisVisibility(ctx, "row", 24, AxisViewSettings{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	settings := AxisViewSettings{Ranges: []AxisVisibilityOverride{{Min: 10, Max: 20, Hidden: true}, {Min: 30, Max: 40, Hidden: true}}}
	changed, err := settings.WithRange(formats.ExternalRows, 15, 35, false)
	if err != nil || !reflect.DeepEqual(changed.Ranges, []AxisVisibilityOverride{{Min: 10, Max: 14, Hidden: true}, {Min: 15, Max: 35, Hidden: false}, {Min: 36, Max: 40, Hidden: true}}) {
		t.Fatal(changed, err)
	}
	if !reflect.DeepEqual(settings.Ranges, []AxisVisibilityOverride{{Min: 10, Max: 20, Hidden: true}, {Min: 30, Max: 40, Hidden: true}}) {
		t.Fatal("range update changed its input")
	}
	changed, err = changed.WithRange(formats.ExternalRows, 15, 35, true)
	if err != nil || !reflect.DeepEqual(changed.Ranges, []AxisVisibilityOverride{{Min: 10, Max: 40, Hidden: true}}) {
		t.Fatal("range merging failed", changed, err)
	}
	maximal := AxisViewSettings{}
	for n := 0; n < formats.AxisVisibilityOverrides; n++ {
		maximal.Ranges = append(maximal.Ranges, AxisVisibilityOverride{Min: n*3 + 1, Max: n*3 + 2, Hidden: true})
	}
	if _, err := maximal.WithRange(formats.ExternalRows, 1, 1, true); err != nil {
		t.Fatal("idempotent edit at the canonical limit failed", err)
	}
}

func TestAxisVisibilityExcessiveOutlineKeepsAnExplicitShowAllFallbackAndMarkersDoNotInventHiddenRows(t *testing.T) {
	source := SourceLayout{RowStates: []SourceAxisRange{{Min: 2, Max: 4, Level: 1}, {Min: 5, Max: 5, Collapsed: true}}}
	v, err := source.BuildAxisVisibility(context.Background(), "row", 8, AxisViewSettings{})
	if err != nil || v.Len() != 8 || !v.Groups()[0].SourceCollapsed || v.Groups()[0].Visible != 3 {
		t.Fatal("collapsed marker replaced actual hidden flags", err)
	}
	source.RowStates = nil
	for n := 0; n < 20000; n++ {
		source.RowStates = append(source.RowStates, SourceAxisRange{Min: n*2 + 1, Max: n*2 + 1, Level: 7})
	}
	if _, err := source.BuildAxisVisibility(context.Background(), "row", 50000, AxisViewSettings{}); err == nil {
		t.Fatal("excessive inferred groups became partial success")
	}
	shown, err := source.BuildAxisVisibility(context.Background(), "row", 50000, AxisViewSettings{Mode: "show-all"})
	if err != nil || shown.Len() != 50000 || shown.Notice() == "" || len(shown.Groups()) != 0 {
		t.Fatal("source group budget blocked complete-value fallback", err)
	}
}
