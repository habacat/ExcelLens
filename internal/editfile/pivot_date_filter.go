package editfile

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"excellens/internal/editdoc"
)

type pivotDateFilter struct {
	spec         editdoc.PivotDateFilter
	field, order int
	id           uint32
	low, high    float64
}

var dateComparisons = map[string]struct{ first, second string }{
	"dateEqual": {"equal", ""}, "dateNotEqual": {"notEqual", ""},
	"dateOlderThan": {"lessThan", ""}, "dateOlderThanOrEqual": {"lessThanOrEqual", ""},
	"dateNewerThan": {"greaterThan", ""}, "dateNewerThanOrEqual": {"greaterThanOrEqual", ""},
	"dateBetween": {"greaterThanOrEqual", "lessThanOrEqual"}, "dateNotBetween": {"lessThan", "greaterThan"},
}

// Use local civil dates at UTC midnight so calendar boundaries remain stable
// through daylight saving changes. The upper bound is exclusive.
func relativeDateBounds(kind string, today time.Time) (time.Time, time.Time, bool) {
	day := calendarDay(today)
	y, m, d := day.Date()
	start := day
	months, days := 0, 0
	switch kind {
	case "yesterday", "today", "tomorrow":
		if kind == "yesterday" {
			start = day.AddDate(0, 0, -1)
		} else if kind == "tomorrow" {
			start = day.AddDate(0, 0, 1)
		}
		days = 1
	case "lastWeek", "thisWeek", "nextWeek":
		start = day.AddDate(0, 0, -int(day.Weekday()))
		if kind == "lastWeek" {
			start = start.AddDate(0, 0, -7)
		} else if kind == "nextWeek" {
			start = start.AddDate(0, 0, 7)
		}
		days = 7
	case "lastMonth", "thisMonth", "nextMonth":
		months = 1
	case "lastQuarter", "thisQuarter", "nextQuarter":
		m = (m-1)/3*3 + 1
		months = 3
	case "lastYear", "thisYear", "nextYear":
		m = time.January
		months = 12
	case "yearToDate":
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(y, day.Month(), d+1, 0, 0, 0, 0, time.UTC), true
	default:
		return time.Time{}, time.Time{}, false
	}
	if months != 0 {
		start = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		if strings.HasPrefix(kind, "last") {
			start = start.AddDate(0, -months, 0)
		} else if strings.HasPrefix(kind, "next") {
			start = start.AddDate(0, months, 0)
		}
	}
	return start, start.AddDate(0, months, days), true
}

func datePeriod(kind string) (month, quarter int) {
	if len(kind) < 2 || len(kind) > 3 {
		return
	}
	v, err := strconv.Atoi(kind[1:])
	if err != nil || strconv.Itoa(v) != kind[1:] || v < 1 {
		return
	}
	if kind[0] == 'Q' && v <= 4 {
		quarter = v
	}
	if kind[0] == 'M' && v <= 12 {
		month = v
	}
	return
}

func isPivotDateKind(kind string) bool {
	if _, ok := dateComparisons[kind]; ok {
		return true
	}
	m, q := datePeriod(kind)
	if m != 0 || q != 0 {
		return true
	}
	_, _, ok := relativeDateBounds(kind, time.Time{})
	return ok
}

func filterSerial(t time.Time, date1904 bool) float64 {
	return float64(t.Unix()-pivotEpoch(date1904).Unix()) / 86400
}

func filterDateText(serial float64, date1904, whole bool) (string, error) {
	if math.IsNaN(serial) || math.IsInf(serial, 0) || serial < -700000 || serial > 3000000 {
		return "", ErrXLSX
	}
	t := time.Unix(pivotEpoch(date1904).Unix()+int64(math.Round(serial*86400)), 0).UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return "", ErrXLSX
	}
	if whole && serial == math.Floor(serial) {
		return t.Format(time.DateOnly), nil
	}
	return t.Format(pivotDateLayout), nil
}

func requestDateNumber(text string, whole, date1904 bool) (float64, error) {
	layout := pivotDateLayout
	if whole && len(text) == len(time.DateOnly) {
		layout = time.DateOnly
	}
	t, err := time.Parse(layout, text)
	if err != nil || t.Year() < 1 || t.Format(layout) != text {
		return 0, errors.New("请输入有效日期和时间")
	}
	return filterSerial(t, date1904), nil
}

func (f *pivotDateFilter) setPeriod(today time.Time, date1904 bool) {
	if a, b, ok := relativeDateBounds(f.spec.Kind, today); ok {
		f.low, f.high = filterSerial(a, date1904), filterSerial(b, date1904)
	}
}

func (f pivotDateFilter) matches(t time.Time, date1904 bool) bool {
	serial := filterSerial(t, date1904)
	// Excel preserves a fractional threshold even with useWholeDay set.
	// Strictly-before and at-or-after compare the original timestamp.
	if f.spec.WholeDay && f.spec.Kind != "dateOlderThan" && f.spec.Kind != "dateNewerThanOrEqual" {
		serial = math.Floor(serial)
	}
	switch f.spec.Kind {
	case "dateEqual":
		return serial == f.low
	case "dateNotEqual":
		return serial != f.low
	case "dateOlderThan":
		return serial < f.low
	case "dateOlderThanOrEqual":
		return serial <= f.low
	case "dateNewerThan":
		return serial > f.low
	case "dateNewerThanOrEqual":
		return serial >= f.low
	case "dateBetween":
		return serial >= f.low && serial <= f.high
	case "dateNotBetween":
		return serial < f.low || serial > f.high
	}
	m, q := datePeriod(f.spec.Kind)
	if m != 0 {
		return int(t.Month()) == m
	}
	if q != 0 {
		return (int(t.Month())-1)/3+1 == q
	}
	return serial >= f.low && serial < f.high
}
