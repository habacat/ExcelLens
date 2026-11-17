package workbook

import (
	"context"
	"slices"
	"sort"
	"testing"
)

// Independent, literal small-sheet reference: enumerate each level and cell.
// The product implementation sweeps compressed boundaries instead.
func literalAxisGroups(states []SourceAxisRange, maximum int, after bool) []AxisOutlineGroup {
	levels := make([]uint8, maximum+1)
	closed := make([]bool, maximum+1)
	for _, r := range states {
		for n := r.Min; n <= r.Max; n++ {
			levels[n], closed[n] = r.Level, r.Collapsed
		}
	}
	var groups []AxisOutlineGroup
	for level := uint8(1); level <= 7; level++ {
		for n := 1; n <= maximum; {
			if levels[n] < level {
				n++
				continue
			}
			first := n
			for n <= maximum && levels[n] >= level {
				n++
			}
			summary := first - 1
			if after {
				summary = n
			}
			if summary < 1 || summary > maximum {
				summary = 0
			}
			groups = append(groups, AxisOutlineGroup{AxisOutlineKey: AxisOutlineKey{Min: first, Max: n - 1, Level: level}, Summary: summary, SourceCollapsed: summary > 0 && closed[summary]})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Min != b.Min {
			return a.Min < b.Min
		}
		if a.Max != b.Max {
			return a.Max > b.Max
		}
		return a.Level < b.Level
	})
	return groups
}

func FuzzAxisVisibilityMatchesLiteralSheet(f *testing.F) {
	f.Add([]byte{0, 1, 20, 7, 31, 65, 13, 126, 5, 255, 0, 0, 5})
	f.Add([]byte{61, 7, 39, 20, 27, 63, 5, 96, 31, 3, 16, 0, 129})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 3 || len(data) > 512 {
			return
		}
		get := func(i int) byte { return data[i%len(data)] }
		maximum := int(get(2)%40) + 1
		after := get(0)&4 != 0
		defaultHidden := get(0)&32 != 0
		layout := SourceLayout{ZeroHeight: defaultHidden, SummaryBelow: &after}
		layout.DefaultRowZeroSize = get(0)&64 != 0
		for n := 1; n <= maximum; n++ {
			if get(n+5)&16 != 0 {
				appendSourceRowSize(&layout.Rows, n, 20)
			}
		}
		for n := 1; n <= maximum; n++ {
			b := get(n + 2)
			if b&128 != 0 {
				continue
			}
			layout.RowStates = append(layout.RowStates, SourceAxisRange{Min: n, Max: n, Level: uint8((b >> 2) % 4), HiddenSet: b&2 != 0, Hidden: b&1 != 0, Collapsed: b&32 != 0, ZeroSize: b&64 != 0, PositiveSize: b&16 != 0 && b&64 == 0})
		}
		groups := literalAxisGroups(layout.RowStates, maximum, after)
		settings := AxisViewSettings{Mode: []string{"original", "show-all", "hide-all"}[get(0)%3]}
		if get(0)&8 != 0 {
			depth := uint8(get(1) % 8)
			settings.Depth = &depth
		}
		if get(0)&16 != 0 {
			for n, g := range groups {
				if n%3 == 0 {
					settings.Groups = append(settings.Groups, AxisOutlineOverride{AxisOutlineKey: g.AxisOutlineKey, Collapsed: get(n+7)&1 != 0})
				}
			}
		}
		for n := 1; n <= maximum; n += 7 {
			settings.Ranges = append(settings.Ranges, AxisVisibilityOverride{Min: n, Max: n, Hidden: get(n+9)&1 != 0})
		}
		view, err := layout.BuildAxisVisibility(context.Background(), "row", maximum, settings)
		if err != nil {
			t.Fatal(err)
		}
		var want []int
		for n := 1; n <= maximum; n++ {
			original := defaultHidden
			for _, r := range layout.RowStates {
				if r.Min <= n && r.Max >= n {
					if r.HiddenSet {
						original = r.Hidden
					}
					original = original || r.ZeroSize
				}
			}
			if layout.DefaultRowZeroSize && !slices.ContainsFunc(layout.Rows, func(r SourceSizeRange) bool { return n >= r.Min && n <= r.Max }) && !slices.ContainsFunc(layout.RowStates, func(r SourceAxisRange) bool { return n >= r.Min && n <= r.Max && r.PositiveSize }) {
				original = true
			}
			hidden := settings.Mode == "hide-all" || settings.Mode == "original" && original
			expanded, collapse, sourceGuard := false, false, false
			for _, g := range groups {
				if n < g.Min || n > g.Max {
					continue
				}
				explicit := settings.Depth != nil
				closed := settings.Mode == "original" && g.SourceCollapsed
				if settings.Depth != nil {
					closed = g.Level > *settings.Depth
				}
				for _, change := range settings.Groups {
					if change.AxisOutlineKey == g.AxisOutlineKey {
						closed, explicit = change.Collapsed, true
					}
				}
				if explicit {
					if closed {
						collapse = true
					} else {
						expanded = true
					}
				} else if closed {
					sourceGuard = true
				}
			}
			if expanded && !sourceGuard {
				hidden = false
			}
			if collapse {
				hidden = true
			}
			for _, r := range settings.Ranges {
				if n >= r.Min && n <= r.Max {
					hidden = r.Hidden
				}
			}
			if view.Visible(n) == hidden {
				t.Fatal("literal visibility differs", n, hidden, layout, settings)
			}
			if !hidden {
				want = append(want, n)
			}
		}
		if view.Len() != len(want) {
			t.Fatal(view.Len(), want)
		}
		start := int(get(7)) % (len(want) + 1)
		count := int(get(8)) % (len(want) - start + 1)
		if got := slices.Collect(view.IterRange(start, count)); !slices.Equal(got, want[start:start+count]) {
			t.Fatal("axis range iteration differs", start, count, got, want)
		}
		got, err := view.Page(0, maximum)
		if err != nil || !slices.Equal(got, want) {
			t.Fatal(got, want, err)
		}
		for n := 0; n <= maximum+1; n++ {
			before := sort.SearchInts(want, n)
			if view.Before(n) != before {
				t.Fatal("rank differs", n, view.Before(n), before)
			}
			if n > 0 && n <= maximum {
				at, visible := view.Position(n)
				if at != before || visible != slices.Contains(want, n) {
					t.Fatal("position differs", n, at, visible)
				}
			}
		}
		for i, g := range view.Groups() {
			if g.AxisOutlineKey != groups[i].AxisOutlineKey || g.Summary != groups[i].Summary || g.SourceCollapsed != groups[i].SourceCollapsed {
				t.Fatal("literal outline differs", g, groups[i])
			}
		}
	})
}
