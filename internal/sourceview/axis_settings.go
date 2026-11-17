// Package sourceview owns declarative source ranges, filtering and visibility.
// It contains no workbook readers, runtime row orders or user-file writes.
package sourceview

import (
	"errors"
	"excellens/internal/formats"
	"slices"
	"sort"
)

// AxisViewSettings affects presentation only. Source hidden/zero-size flags,
// values, types and coordinates are never overwritten by local view choices.
type AxisViewSettings struct {
	Mode   string                   `json:"mode,omitempty"`
	Ranges []AxisVisibilityOverride `json:"ranges,omitempty"`
	Depth  *uint8                   `json:"depth,omitempty"`
	Groups []AxisOutlineOverride    `json:"groups,omitempty"`
}

type AxisInterval struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type AxisVisibilityOverride struct {
	Min    int  `json:"min"`
	Max    int  `json:"max"`
	Hidden bool `json:"hidden"`
}

type AxisOutlineKey struct {
	Min   int   `json:"min"`
	Max   int   `json:"max"`
	Level uint8 `json:"level"`
}

type AxisOutlineOverride struct {
	AxisOutlineKey
	Collapsed bool `json:"collapsed"`
}

func CompareOutlineKeys(a, b AxisOutlineKey) int {
	if a.Min != b.Min {
		return a.Min - b.Min
	}
	if a.Max != b.Max {
		return b.Max - a.Max
	}
	return int(a.Level) - int(b.Level)
}

// Normalized validates and copies settings without reading source metadata.
// BuildAxisVisibility additionally verifies that outline keys exist in the source.
func (s AxisViewSettings) Normalized(maximum int) (AxisViewSettings, error) {
	return normalizeAxisView(maximum, s)
}

func normalizeAxisView(maximum int, input AxisViewSettings) (AxisViewSettings, error) {
	out := input
	if out.Mode == "" {
		out.Mode = "original"
	}
	if maximum < 1 || maximum > formats.ExternalRows || (out.Mode != "original" && out.Mode != "show-all" && out.Mode != "hide-all") || len(input.Ranges) > formats.AxisVisibilityOverrides || len(input.Groups) > formats.AxisVisibilityOverrides {
		return AxisViewSettings{}, errors.New("行列显隐设置或覆盖数量无效")
	}
	if input.Depth != nil {
		depth := *input.Depth
		if depth > 7 {
			return AxisViewSettings{}, errors.New("大纲展开层级必须为0至7")
		}
		out.Depth = &depth
	}
	out.Ranges = slices.Clone(input.Ranges)
	sort.Slice(out.Ranges, func(i, j int) bool { return out.Ranges[i].Min < out.Ranges[j].Min })
	n := 0
	for _, r := range out.Ranges {
		if r.Min < 1 || r.Max < r.Min || r.Max > maximum || (n > 0 && r.Min <= out.Ranges[n-1].Max) {
			return AxisViewSettings{}, errors.New("行列显隐覆盖区间无效或重叠")
		}
		if n > 0 && out.Ranges[n-1].Max+1 == r.Min && out.Ranges[n-1].Hidden == r.Hidden {
			out.Ranges[n-1].Max = r.Max
		} else {
			out.Ranges[n] = r
			n++
		}
	}
	out.Ranges = out.Ranges[:n]
	out.Groups = slices.Clone(input.Groups)
	sort.Slice(out.Groups, func(i, j int) bool {
		return CompareOutlineKeys(out.Groups[i].AxisOutlineKey, out.Groups[j].AxisOutlineKey) < 0
	})
	for i, g := range out.Groups {
		if g.Min < 1 || g.Max < g.Min || g.Max > maximum || g.Level < 1 || g.Level > 7 || (i > 0 && CompareOutlineKeys(out.Groups[i-1].AxisOutlineKey, g.AxisOutlineKey) == 0) {
			return AxisViewSettings{}, errors.New("大纲组设置无效或重复")
		}
	}
	return out, nil
}

// WithRange atomically splits/replaces overlapping local intervals. One
// hide/show operation covering millions of rows remains a small range edit.
func (s AxisViewSettings) WithRange(maximum, first, last int, hidden bool) (AxisViewSettings, error) {
	out, err := normalizeAxisView(maximum, s)
	if err != nil {
		return AxisViewSettings{}, err
	}
	if first < 1 || last < first || last > maximum {
		return AxisViewSettings{}, errors.New("行列显隐覆盖范围无效")
	}
	var ranges []AxisVisibilityOverride
	for _, r := range out.Ranges {
		if r.Max < first || r.Min > last {
			ranges = append(ranges, r)
			continue
		}
		if r.Min < first {
			ranges = append(ranges, AxisVisibilityOverride{r.Min, first - 1, r.Hidden})
		}
		if r.Max > last {
			ranges = append(ranges, AxisVisibilityOverride{last + 1, r.Max, r.Hidden})
		}
	}
	ranges = append(ranges, AxisVisibilityOverride{first, last, hidden})
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Min < ranges[j].Min })
	n := 0
	for _, r := range ranges {
		if n > 0 && ranges[n-1].Max+1 == r.Min && ranges[n-1].Hidden == r.Hidden {
			ranges[n-1].Max = r.Max
		} else {
			ranges[n] = r
			n++
		}
	}
	if n > formats.AxisVisibilityOverrides {
		return AxisViewSettings{}, errors.New("行列显隐覆盖数量超过容量")
	}
	out.Ranges = ranges[:n]
	return out, nil
}
