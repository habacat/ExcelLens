package editfile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"excellens/internal/editdoc"
)

type cacheDateRange struct {
	Unit      string `xml:"groupBy,attr"`
	Start     string `xml:"startDate,attr"`
	End       string `xml:"endDate,attr"`
	AutoStart string `xml:"autoStart,attr"`
	AutoEnd   string `xml:"autoEnd,attr"`
	Interval  int    `xml:"groupInterval,attr"`
}
type cacheDateGroup struct {
	Base   *int            `xml:"base,attr"`
	Parent *int            `xml:"par,attr"`
	Range  *cacheDateRange `xml:"rangePr"`
	Items  []pivotMember   `xml:"groupItems>s"`
}

var dateUnits = []string{"seconds", "minutes", "hours", "days", "months", "quarters", "years"}
var dateUnitNames = []string{"秒", "分钟", "小时", "天", "月", "季度", "年"}

type pivotDateLevel struct {
	base, field int
	rule        cacheDateRange
	old         []pivotMember
}
type dateReplacement struct {
	old   map[int]bool
	base  int
	names []string
}
type pivotCalendarLayout struct {
	physical    int
	levels      []pivotDateLevel
	replacement *dateReplacement
	english     bool
}

func isSourceField(f cacheFieldXML) bool { return f.Database == "" || isTrue(f.Database) }
func (s *pivotSchema) appendField(name string, old int) int {
	i := len(s.names)
	s.names = append(s.names, name)
	s.oldForNew = append(s.oldForNew, old)
	if old >= 0 {
		s.newForOld[old] = i
	}
	return i
}
func planDateSchema(values objectValues, source pivotSource, old []cacheFieldXML, change *editdoc.PivotDateGroup, english bool) (pivotSchema, pivotCalendarLayout, error) {
	var physical []string
	var indices []int
	for i, f := range old {
		if isSourceField(f) {
			physical = append(physical, f.Name)
			indices = append(indices, i)
		}
	}
	s, err := readPivotSchema(values, source, physical)
	d := pivotCalendarLayout{physical: len(s.names), english: english}
	if err != nil {
		return s, d, err
	}
	s.newForOld = make([]int, len(old))
	for i := range old {
		s.newForOld[i] = -1
	}
	for i, j := range s.oldForNew {
		if j >= 0 {
			s.oldForNew[i] = indices[j]
			s.newForOld[indices[j]] = i
		}
	}
	target := -1
	used := map[string]bool{}
	for _, n := range s.names {
		used[strings.ToLower(n)] = true
	}
	if change != nil {
		target = findField(old, change.Field)
		if target < 0 || !isSourceField(old[target]) || s.newForOld[target] < 0 {
			return s, d, errors.New("请选择日期来源字段")
		}
		d.replacement = &dateReplacement{base: target, old: map[int]bool{target: true}}
	}
	oldLevels := map[string]int{}
	for i, f := range old {
		g := f.Group
		if g == nil || g.Range == nil {
			if !isSourceField(f) {
				return s, d, errors.New("透视缓存含未识别的派生字段")
			}
			continue
		}
		if g.Base == nil || *g.Base < 0 || *g.Base >= len(old) || s.newForOld[*g.Base] < 0 || !slices.Contains(dateUnits, g.Range.Unit) {
			return s, d, errors.New("此缓存使用非日期分组或缺少来源字段")
		}
		base := *g.Base
		if base == target {
			d.replacement.old[i] = true
			oldLevels[g.Range.Unit] = i
			continue
		}
		num := s.newForOld[i]
		if !isSourceField(f) {
			num = s.appendField(f.Name, i)
			used[strings.ToLower(f.Name)] = true
		}
		d.levels = append(d.levels, pivotDateLevel{s.newForOld[base], num, *g.Range, g.Items})
	}
	if change != nil {
		units := slices.Clone(change.Units)
		if len(units) == 0 && len(oldLevels) == 0 {
			return s, d, errors.New("此日期字段尚未分组")
		}
		slices.SortFunc(units, func(a, b string) int { return slices.Index(dateUnits, a) - slices.Index(dateUnits, b) })
		if len(units) != len(slices.Compact(slices.Clone(units))) || len(units) > len(dateUnits) {
			return s, d, errors.New("请选择不同的日期分组单位")
		}
		if change.Interval < 0 || change.Interval > 32767 || change.Interval > 1 && !slices.Equal(units, []string{"days"}) {
			return s, d, errors.New("多日间隔只适用于单独按天分组")
		}
		for _, u := range units {
			n := slices.Index(dateUnits, u)
			if n < 0 {
				return s, d, errors.New("未知的日期分组单位")
			}
			rule := cacheDateRange{Unit: u, Start: change.Start, End: change.End, Interval: change.Interval}
			if rule.Start != "" {
				rule.AutoStart = "0"
			}
			if rule.End != "" {
				rule.AutoEnd = "0"
			}
			name := dateUnitNames[n] + "(" + change.Field + ")"
			if english {
				name = strings.ToUpper(u[:1]) + u[1:] + " (" + change.Field + ")"
			}
			oldIndex := -1
			var items []pivotMember
			if j, found := oldLevels[u]; found && j != target {
				name = old[j].Name
				oldIndex = j
				items = old[j].Group.Items
			}
			base := name
			for k := 2; used[strings.ToLower(name)]; k++ {
				name = fmt.Sprintf("%s%d", base, k)
			}
			used[strings.ToLower(name)] = true
			idx := s.appendField(name, oldIndex)
			d.levels = append(d.levels, pivotDateLevel{s.newForOld[target], idx, rule, items})
			d.replacement.names = append(d.replacement.names, name)
		}
		slices.Reverse(d.replacement.names)
		if len(units) == 0 {
			d.replacement.names = []string{s.names[s.newForOld[target]]}
		}
	}
	oldNames := make([]string, len(old))
	for i, f := range old {
		oldNames[i] = f.Name
	}
	s.changed = !slices.Equal(oldNames, s.names)
	return s, d, nil
}

func (d *dateReplacement) axis(original []pivotAxisField, s pivotSchema, includeBase bool) ([]string, bool, int) {
	var out []string
	applied := false
	valuesPosition := -1
	for _, a := range original {
		if a.X == pivotValuesField {
			valuesPosition = len(out)
			continue
		}
		if d.old[a.X] && (a.X != d.base || includeBase) {
			if !applied {
				out = append(out, d.names...)
				applied = true
			}
		} else if i := s.newForOld[a.X]; i >= 0 {
			out = append(out, s.names[i])
		}
	}
	return out, applied, valuesPosition
}
