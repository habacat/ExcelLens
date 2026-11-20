package editfile

import (
	"slices"

	"excellens/internal/editdoc"
)

type PivotGroupField struct {
	Source   string `json:"source"`
	Unit     string `json:"unit"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Interval int    `json:"interval,omitempty"`
	English  bool   `json:"english,omitempty"`
}

func (g PivotGroupField) rule() cacheDateRange {
	return cacheDateRange{Unit: g.Unit, Interval: g.Interval, Start: g.Start, End: g.End}
}
func (p *PivotInfo) dateInfo(fields []cacheFieldXML, english bool) {
	for _, f := range fields {
		if eligibleDateField(f) {
			p.DateFields = append(p.DateFields, f.Name)
		}
		g := f.Group
		if g == nil || g.Range == nil || g.Base == nil || *g.Base < 0 || *g.Base >= len(fields) {
			continue
		}
		base := fields[*g.Base].Name
		if p.GroupFields == nil {
			p.GroupFields = map[string]PivotGroupField{}
		}
		r := g.Range
		p.GroupFields[f.Name] = PivotGroupField{base, r.Unit, r.Start, r.End, r.Interval, english}
		i := slices.IndexFunc(p.DateGroups, func(x editdoc.PivotDateGroup) bool { return x.Field == base })
		if i < 0 {
			i = len(p.DateGroups)
			entry := editdoc.PivotDateGroup{Field: base, Interval: r.Interval}
			if !pivotOn(r.AutoStart) {
				entry.Start = r.Start
			}
			if !pivotOn(r.AutoEnd) {
				entry.End = r.End
			}
			p.DateGroups = append(p.DateGroups, entry)
		}
		p.DateGroups[i].Units = append(p.DateGroups[i].Units, r.Unit)
	}
}
