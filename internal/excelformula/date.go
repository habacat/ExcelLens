// Package excelformula implements value algorithms shared by both Excel formula evaluators.
package excelformula

import "time"

func MaxSerial(date1904 bool) int64 {
	if date1904 {
		return 2957003
	}
	return 2958465
}
func Serial(t time.Time, date1904 bool) int64 {
	y, m, d := t.Date()
	t = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := (t.Unix() - base.Unix()) / 86400
	if !date1904 && !t.Before(time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)) {
		days++
	}
	return days
}

// Date normalizes the month, then counts days on Excel's serial calendar,
// which includes the compatibility day 60 in the 1900 system.
func Date(year, month, day int64, date1904 bool) (int64, bool) {
	if year < 0 || year >= 10000 || month < -120000 || month > 120000 || day < -4000000 || day > 4000000 {
		return 0, false
	}
	if year < 1900 {
		year += 1900
	}
	first := time.Date(int(year), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	minYear := 1900
	if date1904 {
		minYear = 1904
	}
	if first.Year() < minYear || first.Year() > 9999 {
		return 0, false
	}
	n := Serial(first, date1904) + day - 1
	return n, n >= 0 && n <= MaxSerial(date1904)
}
func DateParts(serial int64, date1904 bool) (year, month, day int, weekday time.Weekday) {
	if date1904 {
		t := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(serial))
		return t.Year(), int(t.Month()), t.Day(), t.Weekday()
	}
	if serial == 0 {
		return 1900, 1, 0, time.Saturday
	}
	if serial == 60 {
		return 1900, 2, 29, time.Wednesday
	}
	days := serial
	if days > 60 {
		days--
	}
	t := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days))
	return t.Year(), int(t.Month()), t.Day(), time.Weekday((serial + 6) % 7)
}

func Weekday(wd time.Weekday, returnType int64) (int, bool) {
	// wd: Sunday=0, Monday=1, ..., Saturday=6
	sun0 := int(wd)
	switch returnType {
	case 1, 17:
		return sun0 + 1, true
	case 2, 11:
		return (sun0+6)%7 + 1, true
	case 3:
		return (sun0 + 6) % 7, true
	case 12:
		return (sun0+5)%7 + 1, true
	case 13:
		return (sun0+4)%7 + 1, true
	case 14:
		return (sun0+3)%7 + 1, true
	case 15:
		return (sun0+2)%7 + 1, true
	case 16:
		return (sun0+1)%7 + 1, true
	}
	return 0, false
}
