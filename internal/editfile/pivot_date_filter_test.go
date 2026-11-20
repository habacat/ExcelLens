package editfile

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"excellens/internal/editdoc"
)

func TestPivotRelativeDatesUseLocalCalendarBoundaries(t *testing.T) {
	// Local Sunday is still Saturday in UTC. Excel starts a week on Sunday.
	now := time.Date(2024, 12, 29, 0, 5, 0, 0, time.FixedZone("local", 8*3600))
	for _, c := range []struct{ kind, lo, hi string }{
		{"thisWeek", "2024-12-29", "2025-01-05"}, {"lastWeek", "2024-12-22", "2024-12-29"},
		{"nextMonth", "2025-01-01", "2025-02-01"}, {"nextQuarter", "2025-01-01", "2025-04-01"},
		{"yearToDate", "2024-01-01", "2024-12-30"}, {"lastYear", "2023-01-01", "2024-01-01"},
	} {
		lo, hi, ok := relativeDateBounds(c.kind, now)
		if !ok || lo.Format(time.DateOnly) != c.lo || hi.Format(time.DateOnly) != c.hi {
			t.Errorf("%s: %v to %v", c.kind, lo, hi)
		}
	}
	for _, epoch := range []bool{false, true} {
		date := time.Date(2024, 2, 29, 16, 33, 17, 0, time.UTC)
		f := pivotDateFilter{spec: editdoc.PivotDateFilter{Kind: "dateEqual", WholeDay: true}, low: filterSerial(calendarDay(date), epoch)}
		if !f.matches(date, epoch) {
			t.Fatal("leap day")
		}
		f.spec.WholeDay = false
		if f.matches(date, epoch) {
			t.Fatal("time precision lost")
		}
		f.low = filterSerial(date, epoch)
		if !f.matches(date, epoch) {
			t.Fatal("exact second")
		}
		for _, kind := range []string{"M2", "Q1", "thisMonth"} {
			f.spec.Kind = kind
			f.setPeriod(date, epoch)
			if !f.matches(date, epoch) || f.matches(date.AddDate(0, 2, 0), epoch) {
				t.Fatal(epoch, kind)
			}
		}
	}
}

func TestPivotDateFilterRebasesStaleBoundsAndKeepsNativeMetadata(t *testing.T) {
	raw := []byte(`<pivotTableDefinition xmlns="` + mainNS + `"><filters count="2"><filter fld="0" id="9" type="today" name="kept" description="date"><autoFilter ref="A1"><filterColumn colId="0"><dynamicFilter type="today" val="100" maxVal="101"/></filterColumn></autoFilter></filter><filter fld="1" id="2" type="captionEqual" stringValue1="A" description="keep this"><autoFilter ref="A1"><filterColumn colId="0"><filters><filter val="A"/></filters></filterColumn></autoFilter></filter></filters></pivotTableDefinition>`)
	var table pivotTableXML
	if err := xml.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	table.Fields = make([]pivotFieldXML, 2)
	dates, err := table.dateFilters(2, false)
	if err != nil {
		t.Fatal(err)
	}
	labels, err := table.labels(2)
	if err != nil {
		t.Fatal(err)
	}
	p := pivotPlan{table: table, dates: dates, labelFilters: labels}
	p.dates[0].setPeriod(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), false)
	newXML, err := p.writeValueFilters(t.Context(), raw)
	want := strings.Replace(string(raw), `val="100" maxVal="101"`, `val="46346" maxVal="46347"`, 1)
	if err != nil || string(newXML) != want {
		t.Fatal(err, string(newXML))
	}
}

func TestNativeWholeDayFilterWithTimeInSavedThreshold(t *testing.T) {
	noon := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	midnight := calendarDay(noon)
	for _, c := range []struct {
		kind           string
		midnight, noon bool
	}{
		{"dateEqual", false, false}, {"dateNotEqual", true, true},
		{"dateOlderThan", true, false}, {"dateNewerThanOrEqual", false, true},
		{"dateOlderThanOrEqual", true, true}, {"dateNewerThan", false, false},
		{"dateBetween", false, false}, {"dateNotBetween", true, true},
	} {
		f := pivotDateFilter{spec: editdoc.PivotDateFilter{Kind: c.kind, WholeDay: true},
			low: filterSerial(noon, false), high: filterSerial(noon.AddDate(0, 0, 3), false)}
		if f.matches(midnight, false) != c.midnight || f.matches(noon, false) != c.noon {
			t.Errorf("saved fractional bounds for %s", c.kind)
		}
	}
}
