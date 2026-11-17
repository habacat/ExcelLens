// Package valueconv parses explicitly requested date/time conversions. Nothing
// in this package scans a source or changes its stored type.
package valueconv

import (
	"errors"
	"strings"
	"time"
)

type Temporal struct {
	Time    time.Time
	HasDate bool
	HasTime bool
}

func ValidInput(input string) bool {
	switch input {
	case "", "auto", "ymd", "dmy", "mdy", "compact":
		return true
	}
	return false
}

// Parse never guesses the order of a day/month-first date, treats a bare
// number as a date, or silently shifts a timezone. Ambiguous input needs an
// explicit format. UTC is a neutral calendar container, not a timezone shift.
func Parse(text, input string) (Temporal, error) {
	if len(text) > 256 || !ValidInput(input) {
		return Temporal{}, errors.New("日期或时间格式无效")
	}
	s := strings.TrimSpace(text)
	var dates []string
	switch input {
	case "dmy":
		dates = []string{"2/1/2006", "2-1-2006", "2.1.2006"}
	case "mdy":
		dates = []string{"1/2/2006", "1-2-2006", "1.2.2006"}
	case "compact":
		dates = []string{"20060102"}
	default:
		if len(s) >= 5 {
			switch s[4] {
			case '-':
				dates = []string{"2006-1-2"}
			case '/':
				dates = []string{"2006/1/2"}
			case '.':
				dates = []string{"2006.1.2"}
			default:
				if strings.HasPrefix(s[4:], "年") {
					dates = []string{"2006年1月2日"}
				}
			}
		}
	}
	for _, date := range dates {
		for _, clock := range []string{"", " 15:04:05.999999999", " 15:04", "T15:04:05.999999999", "T15:04"} {
			if t, err := time.Parse(date+clock, s); err == nil {
				if t.Year() < 1 {
					return Temporal{}, errors.New("日期年份无效")
				}
				return Temporal{Time: t, HasDate: true, HasTime: clock != ""}, nil
			}
		}
	}
	for _, layout := range []string{"15:04:05.999999999", "15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return Temporal{Time: t, HasTime: true}, nil
		}
	}
	return Temporal{}, errors.New("日期或时间无效；日/月顺序不明确时请选择输入格式")
}

func (t Temporal) Format(kind string) (string, error) {
	switch kind {
	case "date":
		if t.HasDate {
			return t.Time.Format("2006-01-02"), nil
		}
	case "time":
		if t.HasTime {
			return t.Time.Format("15:04:05.999999999"), nil
		}
	case "datetime":
		if t.HasDate && t.HasTime {
			return t.Time.Format("2006-01-02 15:04:05.999999999"), nil
		}
	}
	return "", errors.New("源值不包含所需的日期或时间部分")
}

func Convert(text, kind, input string) (string, error) {
	t, err := Parse(text, input)
	if err != nil {
		return "", err
	}
	return t.Format(kind)
}

// Suggest is only for a bounded text result of a user-requested calculation.
// Pure numeric identifiers and ambiguous dates deliberately receive no hint.
func Suggest(text string) (kind, preview string) {
	if len(text) > 256 {
		return "", ""
	}
	s := strings.TrimSpace(text)
	yearFirst := len(s) > 4
	for i := 0; yearFirst && i < 4; i++ {
		yearFirst = s[i] >= '0' && s[i] <= '9'
	}
	yearFirst = yearFirst && (strings.ContainsRune("-/.", rune(s[4])) || strings.HasPrefix(s[4:], "年"))
	colon := strings.IndexByte(s, ':')
	clock := colon == 1 || colon == 2
	for i := 0; clock && i < colon; i++ {
		clock = s[i] >= '0' && s[i] <= '9'
	}
	if !yearFirst && !clock {
		return "", ""
	}
	t, err := Parse(text, "auto")
	if err != nil || (t.HasDate && t.Time.Year() < 1900) {
		return "", ""
	}
	kind = "date"
	if t.HasTime {
		kind = "time"
		if t.HasDate {
			kind = "datetime"
		}
	}
	preview, _ = t.Format(kind)
	return kind, preview
}
