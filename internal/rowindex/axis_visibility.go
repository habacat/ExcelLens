package rowindex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
)

type AxisViewSettings = sourceview.AxisViewSettings
type AxisInterval = sourceview.AxisInterval
type AxisVisibilityOverride = sourceview.AxisVisibilityOverride
type AxisOutlineKey = sourceview.AxisOutlineKey
type AxisOutlineOverride = sourceview.AxisOutlineOverride

// SourceAxisRange retains visibility separately from size and source values.
// HiddenSet distinguishes an explicit visible row from the sheet's zeroHeight.
type SourceAxisRange struct {
	Min          int   `json:"min"`
	Max          int   `json:"max"`
	Hidden       bool  `json:"hidden,omitempty"`
	HiddenSet    bool  `json:"hiddenSet,omitempty"`
	Level        uint8 `json:"level,omitempty"`
	Collapsed    bool  `json:"collapsed,omitempty"`
	ZeroSize     bool  `json:"zeroSize,omitempty"`
	PositiveSize bool  `json:"positiveSize,omitempty"`
}

type SourceSizeRange struct {
	Min  int     `json:"min"`
	Max  int     `json:"max"`
	Size float64 `json:"size"`
}

func ValidSourceSize(size float64) bool { return size >= 10 && size <= 1200 }

func ValidSourceAxisRanges(ranges []SourceAxisRange, maximum int) bool {
	if len(ranges) > formats.SourceAxisRanges {
		return false
	}
	previous := 0
	for _, r := range ranges {
		if r.Min <= previous || r.Max < r.Min || r.Max > maximum || r.Level > 7 || r.ZeroSize && r.PositiveSize {
			return false
		}
		previous = r.Max
	}
	return true
}

// AxisLayoutInput borrows the immutable slices/flags from SourceLayout without
// copying any interval arrays or importing workbook types.
type AxisLayoutInput struct {
	Rows               []SourceSizeRange
	Cols               map[int]float64
	RowStates          []SourceAxisRange
	ColStates          []SourceAxisRange
	ZeroHeight         bool
	DefaultRowZeroSize bool
	DefaultColZeroSize bool
	SummaryBelow       *bool
	SummaryRight       *bool
	StateNotice        string
}

type AxisOutlineGroup struct {
	AxisOutlineKey
	Summary         int  `json:"summary,omitempty"`
	Parent          int  `json:"parent,omitempty"` // 1-based group; 0 means root
	SourceCollapsed bool `json:"sourceCollapsed,omitempty"`
	Collapsed       bool `json:"collapsed,omitempty"`
	Visible         int  `json:"visible"`
}

// AxisVisibility is immutable. Intervals, not per-row entries, represent even
// a billion-row address space. Prefix counts support rank/select and skipping
// an arbitrarily large hidden gap without walking through that gap.
type AxisVisibility struct {
	maximum int
	hidden  []AxisInterval
	prefix  []int
	groups  []AxisOutlineGroup
	notice  string
	digest  string
}

func BuildAxisVisibility(ctx context.Context, s AxisLayoutInput, axis string, maximum int, settings AxisViewSettings) (*AxisVisibility, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var states []SourceAxisRange
	defaultHidden, summaryAfter := false, true
	defaultZero := false
	switch axis {
	case "row":
		if maximum < 1 || maximum > formats.ExternalRows {
			return nil, errors.New("源行范围无效")
		}
		states, defaultHidden = s.RowStates, s.ZeroHeight
		defaultZero = s.DefaultRowZeroSize
		if s.SummaryBelow != nil {
			summaryAfter = *s.SummaryBelow
		}
	case "col":
		if maximum < 1 || maximum > formats.ExcelColumns {
			return nil, errors.New("源列范围无效")
		}
		states = s.ColStates
		defaultZero = s.DefaultColZeroSize
		if s.SummaryRight != nil {
			summaryAfter = *s.SummaryRight
		}
	default:
		return nil, errors.New("行列轴类型无效")
	}
	// An explicit show-all operation does not need possibly incomplete source
	// flags. Keep the cause; never present a partial source layout as complete.
	if s.StateNotice != "" {
		if settings.Mode != "show-all" || settings.Depth != nil || len(settings.Groups) != 0 {
			return nil, fmt.Errorf("源显隐元数据未完整还原：%s；可暂时显示全部", s.StateNotice)
		}
		states, defaultHidden = nil, false
	}
	if !ValidSourceAxisRanges(states, maximum) {
		if settings.Mode == "show-all" && settings.Depth == nil && len(settings.Groups) == 0 {
			fallback, err := buildAxisVisibility(ctx, maximum, nil, false, summaryAfter, settings, nil)
			if err != nil {
				return nil, err
			}
			fallback.notice = "源行列显隐区间无效，已按要求显示全部"
			fallback.digest = fallback.checksum()
			return fallback, nil
		}
		return nil, errors.New("源行列显隐区间无效，未生成部分视图")
	}
	var zero []AxisInterval
	if defaultZero && (settings.Mode == "" || settings.Mode == "original") {
		var positive []AxisInterval
		if axis == "row" {
			if len(s.Rows) > formats.SourceAxisRanges {
				return nil, errors.New("源行高区间超过容量")
			}
			last := 0
			for _, r := range s.Rows {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if r.Min <= last || r.Max < r.Min || r.Max > maximum || !ValidSourceSize(r.Size) {
					return nil, errors.New("源行高区间无效")
				}
				positive = append(positive, AxisInterval{Min: r.Min, Max: r.Max})
				last = r.Max
			}
		} else {
			if len(s.Cols) > formats.ExcelColumns {
				return nil, errors.New("源列宽数量超过容量")
			}
			var columns []int
			for col, size := range s.Cols {
				if col < 1 || col > maximum || !ValidSourceSize(size) {
					return nil, errors.New("源列宽区间无效")
				}
				columns = append(columns, col)
			}
			sort.Ints(columns)
			for _, col := range columns {
				if n := len(positive); n > 0 && positive[n-1].Max+1 == col {
					positive[n-1].Max = col
				} else {
					positive = append(positive, AxisInterval{Min: col, Max: col})
				}
			}
		}
		for _, r := range states {
			if r.PositiveSize {
				positive = append(positive, AxisInterval{Min: r.Min, Max: r.Max})
			}
		}
		sort.Slice(positive, func(i, j int) bool { return positive[i].Min < positive[j].Min })
		n := 0
		for _, r := range positive {
			if n > 0 && positive[n-1].Max+1 >= r.Min {
				positive[n-1].Max = max(positive[n-1].Max, r.Max)
			} else {
				positive[n] = r
				n++
			}
		}
		positive = positive[:n]
		first := 1
		for _, r := range positive {
			if first < r.Min {
				zero = append(zero, AxisInterval{Min: first, Max: r.Min - 1})
			}
			first = r.Max + 1
		}
		if first <= maximum {
			zero = append(zero, AxisInterval{Min: first, Max: maximum})
		}
	}
	v, err := buildAxisVisibility(ctx, maximum, states, defaultHidden, summaryAfter, settings, zero)
	if err != nil {
		if ctx.Err() != nil || settings.Mode != "show-all" || settings.Depth != nil || len(settings.Groups) != 0 {
			return nil, err
		}
		// A complete-value fallback must remain available even when the
		// source's outline catalogue itself exceeds its metadata budget.
		fallback, fallbackErr := buildAxisVisibility(ctx, maximum, nil, false, summaryAfter, settings, nil)
		if fallbackErr != nil {
			return nil, err
		}
		fallback.notice = err.Error()
		fallback.digest = fallback.checksum()
		return fallback, nil
	}
	v.notice = s.StateNotice
	v.digest = v.checksum()
	return v, nil
}

type axisVisibilityBudget struct{ used int }

func (b *axisVisibilityBudget) add(n int) error {
	if n < 0 || n > formats.AxisVisibilityWorkBytes-b.used {
		return errors.New("行列显隐投影超过工作容量，未生成部分视图")
	}
	b.used += n
	return nil
}

func compareOutlineKeys(a, b AxisOutlineKey) int {
	return sourceview.CompareOutlineKeys(a, b)
}

func normalizeAxisView(maximum int, input AxisViewSettings) (AxisViewSettings, error) {
	return input.Normalized(maximum)
}

func sourceOutlineGroups(ctx context.Context, states []SourceAxisRange, maximum int, summaryAfter bool, budget *axisVisibilityBudget) ([]AxisOutlineGroup, error) {
	var groups []AxisOutlineGroup
	var opened [8]int
	closeAt := func(level uint8, end int) error {
		if opened[level] == 0 {
			return nil
		}
		if len(groups) >= formats.AxisOutlineGroups {
			return errors.New("源大纲组数量超过读取容量，未生成部分大纲")
		}
		if err := budget.add(96); err != nil {
			return err
		}
		g := AxisOutlineGroup{AxisOutlineKey: AxisOutlineKey{Min: opened[level], Max: end, Level: level}}
		g.Summary = g.Min - 1
		if summaryAfter {
			g.Summary = g.Max + 1
		}
		if g.Summary < 1 || g.Summary > maximum {
			g.Summary = 0
		} else {
			i := sort.Search(len(states), func(i int) bool { return states[i].Max >= g.Summary })
			if i < len(states) && states[i].Min <= g.Summary {
				g.SourceCollapsed = states[i].Collapsed
			}
		}
		groups = append(groups, g)
		opened[level] = 0
		return nil
	}
	previous := 0
	for _, r := range states {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.Min > previous+1 {
			for level := uint8(1); level <= 7; level++ {
				if err := closeAt(level, previous); err != nil {
					return nil, err
				}
			}
		}
		for level := uint8(1); level <= 7; level++ {
			if r.Level >= level {
				if opened[level] == 0 {
					opened[level] = r.Min
				}
			} else if err := closeAt(level, r.Min-1); err != nil {
				return nil, err
			}
		}
		previous = r.Max
	}
	for level := uint8(1); level <= 7; level++ {
		if err := closeAt(level, previous); err != nil {
			return nil, err
		}
	}
	sort.Slice(groups, func(i, j int) bool { return compareOutlineKeys(groups[i].AxisOutlineKey, groups[j].AxisOutlineKey) < 0 })
	var parents []int
	for i := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g := &groups[i]
		for len(parents) > 0 {
			p := groups[parents[len(parents)-1]]
			if p.Min <= g.Min && p.Max >= g.Max && p.Level < g.Level {
				g.Parent = parents[len(parents)-1] + 1
				break
			}
			parents = parents[:len(parents)-1]
		}
		parents = append(parents, i)
	}
	return groups, ctx.Err()
}

type axisVisibilityEvent struct {
	at    int
	kind  uint8
	delta int8
}

const (
	axisSourceHidden = iota
	axisExpanded
	axisCollapsed
	axisSourceGuard
	axisManualHidden
	axisManualShown
	axisEventKinds
)

func buildAxisVisibility(ctx context.Context, maximum int, states []SourceAxisRange, defaultHidden, summaryAfter bool, input AxisViewSettings, zero []AxisInterval) (*AxisVisibility, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidSourceAxisRanges(states, maximum) {
		return nil, errors.New("源行列显隐区间无效，未生成部分视图")
	}
	settings, err := normalizeAxisView(maximum, input)
	if err != nil {
		return nil, err
	}
	budget := &axisVisibilityBudget{}
	if err := budget.add(512 + len(settings.Ranges)*32 + len(settings.Groups)*64 + len(zero)*24); err != nil {
		return nil, err
	}
	groups, err := sourceOutlineGroups(ctx, states, maximum, summaryAfter, budget)
	if err != nil {
		return nil, err
	}
	v := &AxisVisibility{maximum: maximum, groups: groups}
	var events []axisVisibilityEvent
	add := func(first, last int, kind uint8) error {
		if last < first {
			return nil
		}
		if err := budget.add(64); err != nil {
			return err
		}
		events = append(events, axisVisibilityEvent{first, kind, 1}, axisVisibilityEvent{last + 1, kind, -1})
		return nil
	}
	if settings.Mode == "hide-all" {
		if err := add(1, maximum, axisSourceHidden); err != nil {
			return nil, err
		}
	} else if settings.Mode == "original" {
		for _, r := range zero {
			if err := add(r.Min, r.Max, axisSourceHidden); err != nil {
				return nil, err
			}
		}
		first := 1
		for _, r := range states {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if defaultHidden {
				if err := add(first, r.Min-1, axisSourceHidden); err != nil {
					return nil, err
				}
			}
			hidden := defaultHidden
			if r.HiddenSet {
				hidden = r.Hidden
			}
			if hidden || r.ZeroSize {
				if err := add(r.Min, r.Max, axisSourceHidden); err != nil {
					return nil, err
				}
			}
			first = r.Max + 1
		}
		if defaultHidden {
			if err := add(first, maximum, axisSourceHidden); err != nil {
				return nil, err
			}
		}
	}
	change := 0
	for i := range v.groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		g := &v.groups[i]
		if change < len(settings.Groups) && compareOutlineKeys(settings.Groups[change].AxisOutlineKey, g.AxisOutlineKey) < 0 {
			return nil, errors.New("大纲组与源工作表不匹配")
		}
		explicit := settings.Depth != nil
		g.Collapsed = settings.Mode == "original" && g.SourceCollapsed
		if settings.Depth != nil {
			g.Collapsed = g.Level > *settings.Depth
		}
		if change < len(settings.Groups) && compareOutlineKeys(settings.Groups[change].AxisOutlineKey, g.AxisOutlineKey) == 0 {
			g.Collapsed, explicit = settings.Groups[change].Collapsed, true
			change++
		}
		kind := uint8(axisSourceGuard)
		if explicit {
			kind = axisExpanded
			if g.Collapsed {
				kind = axisCollapsed
			}
		} else if !g.Collapsed {
			continue
		}
		if err := add(g.Min, g.Max, kind); err != nil {
			return nil, err
		}
	}
	if change != len(settings.Groups) {
		return nil, errors.New("大纲组与源工作表不匹配")
	}
	for _, r := range settings.Ranges {
		kind := uint8(axisManualShown)
		if r.Hidden {
			kind = axisManualHidden
		}
		if err := add(r.Min, r.Max, kind); err != nil {
			return nil, err
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	var active [axisEventKinds]int
	appendHidden := func(first, last int) error {
		hidden := active[axisSourceHidden] > 0
		if active[axisExpanded] > 0 && active[axisSourceGuard] == 0 {
			hidden = false
		}
		if active[axisCollapsed] > 0 || active[axisManualHidden] > 0 {
			hidden = true
		}
		if active[axisManualShown] > 0 {
			hidden = false
		}
		if !hidden || last < first {
			return nil
		}
		if n := len(v.hidden); n > 0 && v.hidden[n-1].Max+1 == first {
			v.hidden[n-1].Max = last
			return nil
		}
		if len(v.hidden) >= formats.AxisVisibilityIntervals {
			return errors.New("显隐投影区间超过容量，未生成部分视图")
		}
		if err := budget.add(24); err != nil {
			return err
		}
		v.hidden = append(v.hidden, AxisInterval{Min: first, Max: last})
		return nil
	}
	previous := 1
	for at := 0; at < len(events); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		position := events[at].at
		if err := appendHidden(previous, position-1); err != nil {
			return nil, err
		}
		for at < len(events) && events[at].at == position {
			e := events[at]
			active[e.kind] += int(e.delta)
			at++
		}
		previous = position
	}
	if err := appendHidden(previous, maximum); err != nil {
		return nil, err
	}
	v.prefix = make([]int, len(v.hidden))
	total := 0
	for i, r := range v.hidden {
		total += r.Max - r.Min + 1
		v.prefix[i] = total
	}
	for i := range v.groups {
		g := &v.groups[i]
		g.Visible = v.Before(g.Max+1) - v.Before(g.Min)
	}
	return v, ctx.Err()
}

func (v *AxisVisibility) Maximum() int { return v.maximum }
func (v *AxisVisibility) Len() int {
	if len(v.prefix) == 0 {
		return v.maximum
	}
	return v.maximum - v.prefix[len(v.prefix)-1]
}
func (v *AxisVisibility) Notice() string               { return v.notice }
func (v *AxisVisibility) Digest() string               { return v.digest }
func (v *AxisVisibility) HiddenRanges() []AxisInterval { return slices.Clone(v.hidden) }
func (v *AxisVisibility) Groups() []AxisOutlineGroup   { return slices.Clone(v.groups) }

func (v *AxisVisibility) GroupCount() int { return len(v.groups) }

// GroupPage bounds metadata copies for outline panels; a page does not clone
// the entire source outline catalogue. Returned groups remain independent.
func (v *AxisVisibility) GroupPage(offset, limit int) ([]AxisOutlineGroup, error) {
	if offset < 0 || offset > len(v.groups) || limit < 0 || limit > 10000 {
		return nil, errors.New("大纲读取范围无效")
	}
	return slices.Clone(v.groups[offset : offset+min(limit, len(v.groups)-offset)]), nil
}

// Before is a clamped prefix count in the source coordinate domain. Position
// additionally reports whether the requested source coordinate is visible.
func (v *AxisVisibility) Before(index int) int {
	index = min(v.maximum+1, max(1, index))
	i := sort.Search(len(v.hidden), func(i int) bool { return v.hidden[i].Max >= index })
	hidden := 0
	if i > 0 {
		hidden = v.prefix[i-1]
	}
	if i < len(v.hidden) && v.hidden[i].Min < index {
		hidden += index - v.hidden[i].Min
	}
	return index - 1 - hidden
}
func (v *AxisVisibility) Visible(index int) bool {
	if index < 1 || index > v.maximum {
		return false
	}
	i := sort.Search(len(v.hidden), func(i int) bool { return v.hidden[i].Max >= index })
	return i == len(v.hidden) || v.hidden[i].Min > index
}
func (v *AxisVisibility) Position(index int) (int, bool) { return v.Before(index), v.Visible(index) }
func (v *AxisVisibility) At(position int) int {
	if position < 0 || position >= v.Len() {
		panic("visible axis position out of range")
	}
	i := sort.Search(len(v.hidden), func(i int) bool {
		removed := 0
		if i > 0 {
			removed = v.prefix[i-1]
		}
		return v.hidden[i].Min-1-removed > position
	})
	removed := 0
	if i > 0 {
		removed = v.prefix[i-1]
	}
	return position + 1 + removed
}

// IterRange streams visible source coordinates, skipping compressed hidden
// gaps without walking them or doing another rank/select lookup per row.
func (v *AxisVisibility) IterRange(offset, count int) iter.Seq[int] {
	return func(yield func(int) bool) {
		if offset < 0 || count < 0 || offset > v.Len() || count > v.Len()-offset {
			panic("invalid visible axis iteration range")
		}
		if count == 0 {
			return
		}
		next := v.At(offset)
		hidden := sort.Search(len(v.hidden), func(i int) bool { return v.hidden[i].Max >= next })
		for count > 0 {
			last := next + count - 1
			if hidden < len(v.hidden) {
				last = min(last, v.hidden[hidden].Min-1)
			}
			for next <= last {
				if !yield(next) {
					return
				}
				next++
				count--
			}
			if count > 0 && hidden < len(v.hidden) {
				next = v.hidden[hidden].Max + 1
				hidden++
			}
		}
	}
}
func (v *AxisVisibility) Next(index int) int {
	position := v.Before(index)
	if position >= v.Len() {
		return 0
	}
	return v.At(position)
}
func (v *AxisVisibility) Previous(index int) int {
	if index < 1 {
		return 0
	}
	position := v.Before(min(v.maximum, index)+1) - 1
	if position < 0 {
		return 0
	}
	return v.At(position)
}
func (v *AxisVisibility) Page(offset, limit int) ([]int, error) {
	if offset < 0 || offset > v.Len() || limit < 0 || limit > 10000 {
		return nil, errors.New("可见行列读取范围无效")
	}
	out := make([]int, min(limit, v.Len()-offset))
	for i := range out {
		out[i] = v.At(offset + i)
	}
	return out, nil
}

func (v *AxisVisibility) checksum() string {
	h := sha256.New()
	var raw [64]byte
	binary.LittleEndian.PutUint64(raw[:], uint64(v.maximum))
	binary.LittleEndian.PutUint64(raw[8:], uint64(len(v.hidden)))
	binary.LittleEndian.PutUint64(raw[16:], uint64(len(v.groups)))
	binary.LittleEndian.PutUint64(raw[24:], uint64(len(v.notice)))
	_, _ = h.Write(raw[:32])
	for _, r := range v.hidden {
		binary.LittleEndian.PutUint64(raw[:], uint64(r.Min))
		binary.LittleEndian.PutUint64(raw[8:], uint64(r.Max))
		_, _ = h.Write(raw[:16])
	}
	for _, g := range v.groups {
		clear(raw[:])
		binary.LittleEndian.PutUint64(raw[:], uint64(g.Min))
		binary.LittleEndian.PutUint64(raw[8:], uint64(g.Max))
		binary.LittleEndian.PutUint64(raw[16:], uint64(g.Summary))
		binary.LittleEndian.PutUint64(raw[24:], uint64(g.Parent))
		raw[32] = g.Level
		if g.SourceCollapsed {
			raw[33] = 1
		}
		if g.Collapsed {
			raw[34] = 1
		}
		_, _ = h.Write(raw[:35])
	}
	_, _ = h.Write([]byte(v.notice))
	return hex.EncodeToString(h.Sum(nil))
}
