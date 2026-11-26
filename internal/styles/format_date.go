package styles

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func IsDateFormat(format string) bool {
	var b strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' || c == '_' || c == '*' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				content := strings.ToLower(format[i+1 : i+1+end])
				if content == "h" || content == "hh" || content == "m" || content == "mm" || content == "s" || content == "ss" {
					return true
				}
				i += end + 1
				continue
			}
		}
		b.WriteByte(byte(strings.ToLower(string(c))[0]))
	}
	v := b.String()
	return strings.Contains(v, "y") || strings.Contains(v, "d") || strings.Contains(v, "h") || strings.Contains(v, "s") || strings.Contains(v, "m")
}

func FormatCustomDate(serial float64, format string, date1904 bool) string {
	if serial < 0 || serial > 2958465 {
		return strconv.FormatFloat(serial, 'f', -1, 64)
	}
	base := ExcelSerialTime(serial, date1904)
	fakeLeapDay := !date1904 && serial >= 60 && serial < 61 && base.Day() == 28
	hasDatePart := FormatHasToken(format, 'y') || FormatHasToken(format, 'd')
	timeStart := firstFormatToken(format, 'h')
	has12Hour := strings.Contains(strings.ToUpper(format), "AM/PM") || strings.Contains(strings.ToUpper(format), "A/P")
	var out strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(format); {
		c := format[i]
		if escaped {
			out.WriteByte(c)
			escaped = false
			i++
			continue
		}
		if c == '\\' || c == '_' || c == '*' {
			escaped = true
			i++
			continue
		}
		if c == '"' {
			quoted = !quoted
			i++
			continue
		}
		if quoted {
			out.WriteByte(c)
			i++
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				content := strings.ToLower(format[i+1 : i+1+end])
				switch content {
				case "h", "hh":
					hours := int64(math.Floor(serial * 24))
					if content == "hh" && hours < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(hours, 10))
				case "m", "mm":
					minutes := int64(math.Floor(serial * 1440))
					if content == "mm" && minutes < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(minutes, 10))
				case "s", "ss":
					seconds := int64(math.Floor(serial * 86400))
					if content == "ss" && seconds < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(seconds, 10))
				}
				i += end + 2
				continue
			}
		}
		if strings.EqualFold(format[i:], "AM/PM") {
			out.WriteString(base.Format("PM"))
			i += 5
			continue
		}
		if strings.EqualFold(format[i:], "A/P") {
			out.WriteString(base.Format("P"))
			i += 3
			continue
		}
		if c == 'y' || c == 'Y' || c == 'm' || c == 'M' || c == 'd' || c == 'D' || c == 'h' || c == 'H' || c == 's' || c == 'S' {
			j := i + 1
			for j < len(format) && (format[j] == c || format[j] == c+32 || format[j] == c-32) {
				j++
			}
			token := strings.ToLower(format[i:j])
			switch token[0] {
			case 'y':
				if len(token) >= 4 {
					out.WriteString(base.Format("2006"))
				} else {
					out.WriteString(base.Format("06"))
				}
			case 'd':
				if len(token) >= 4 {
					out.WriteString(base.Format("Monday"))
				} else if len(token) == 3 {
					out.WriteString(base.Format("Mon"))
				} else if fakeLeapDay {
					out.WriteString("29")
				} else if len(token) == 2 {
					out.WriteString(base.Format("02"))
				} else {
					out.WriteString(base.Format("2"))
				}
			case 'h':
				if has12Hour {
					if len(token) >= 2 {
						out.WriteString(base.Format("03"))
					} else {
						out.WriteString(base.Format("3"))
					}
				} else {
					out.WriteString(base.Format("15"))
				}
			case 's':
				if len(token) >= 2 {
					out.WriteString(base.Format("05"))
				} else {
					out.WriteString(base.Format("5"))
				}
			case 'm':
				isMinute := (timeStart >= 0 && i > timeStart) || (!hasDatePart && (FormatHasToken(format, 'h') || FormatHasToken(format, 's')))
				if isMinute {
					if len(token) >= 2 {
						out.WriteString(base.Format("04"))
					} else {
						out.WriteString(base.Format("4"))
					}
				} else if len(token) >= 4 {
					out.WriteString(base.Format("January"))
				} else if len(token) == 3 {
					out.WriteString(base.Format("Jan"))
				} else if len(token) == 2 {
					out.WriteString(base.Format("01"))
				} else {
					out.WriteString(base.Format("1"))
				}
			}
			i = j
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func FormatHasToken(format string, wanted byte) bool {
	return firstFormatToken(format, wanted) >= 0
}

func firstFormatToken(format string, wanted byte) int {
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' || c == '_' || c == '*' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				i += end + 1
				continue
			}
		}
		if c == wanted || c == wanted-32 || c == wanted+32 {
			return i
		}
	}
	return -1
}

// GridShortDate supplies the regional spelling of Excel's built-ins to the
// grid. Exported display text and source inspection retain their stable form.
func GridShortDate(id int, numFmts map[int]string) string {
	if (id == 14 || id == 22) && numFmts[id] == "" && localShortDate != "yyyy-mm-dd" {
		return localShortDate
	}
	return ""
}

func FormatExcelDate(serial float64, date1904 bool, fmtID int) string {
	if serial < 0 || serial > 2958465 {
		return strconv.FormatFloat(serial, 'f', -1, 64)
	}
	t := ExcelSerialTime(serial, date1904)
	fakeLeapDay := !date1904 && serial >= 60 && serial < 61 && t.Day() == 28
	switch fmtID {
	case 15:
		if fakeLeapDay {
			return "29-" + t.Format("Jan-06")
		}
		return t.Format("2-Jan-06")
	case 16:
		if fakeLeapDay {
			return "29-" + t.Format("Jan")
		}
		return t.Format("2-Jan")
	case 17:
		return t.Format("Jan-06")
	case 18:
		return t.Format("3:04 PM")
	case 19:
		return t.Format("3:04:05 PM")
	case 20:
		return t.Format("15:04")
	case 21:
		return t.Format("15:04:05")
	case 22:
		if fakeLeapDay {
			return "1900-02-29 " + t.Format("15:04")
		}
		return t.Format("2006-01-02 15:04")
	case 45:
		return t.Format("04:05")
	case 46:
		seconds := int64(math.Round(serial * 86400))
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	case 47:
		tenths := int64(math.Round(serial * 864000))
		return fmt.Sprintf("%02d:%02d.%d", tenths/600%60, tenths/10%60, tenths%10)
	default:
		if fakeLeapDay {
			return "1900-02-29"
		}
		return t.Format("2006-01-02")
	}
}

func ExcelSerialTime(serial float64, date1904 bool) time.Time {
	whole := int64(math.Floor(serial))
	fraction := serial - float64(whole)
	var base time.Time
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if whole < 60 {
		base = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	} else {
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	if !date1904 && whole == 60 {
		return time.Date(1900, 2, 28, 0, 0, 0, 0, time.UTC).Add(time.Duration(math.Round(fraction*86400)) * time.Second)
	}
	t := base.AddDate(0, 0, int(whole))
	seconds := int64(math.Round(fraction * 86400))
	if seconds >= 86400 {
		t = t.AddDate(0, 0, 1)
		seconds = 0
	}
	return t.Add(time.Duration(seconds) * time.Second)
}

// CalendarDateNumberFormat distinguishes calendar dates (including date and
// time) from clocks and elapsed durations for Excel's formula-entry rules.
func CalendarDateNumberFormat(id int, code string) bool {
	if code == "" {
		switch id {
		case 14, 15, 16, 17, 22, 27, 28, 29, 30, 31, 36, 50, 51, 52, 53, 54, 57, 58, 71, 72, 73, 74, 75, 78:
			return true
		}
		return false
	}
	section := strings.ToLower(SplitFormatSections(code)[0])
	var tokens []byte
	quoted := false
	for i := 0; i < len(section); i++ {
		c := section[i]
		switch {
		case c == '"':
			quoted = !quoted
		case quoted:
			continue
		case c == '\\' || c == '_' || c == '*':
			i++
		case c == '[':
			end := strings.IndexByte(section[i+1:], ']')
			if end < 0 {
				return false
			}
			switch section[i+1 : i+1+end] {
			case "h", "hh":
				tokens = append(tokens, 'h')
			case "m", "mm", "s", "ss":
				tokens = append(tokens, 's')
			}
			i += end + 1
		case strings.HasPrefix(section[i:], "am/pm"):
			i += 4
		case strings.HasPrefix(section[i:], "a/p"):
			i += 2
		case strings.HasPrefix(section[i:], "general"):
			i += 6
		case c == 'd' || c == 'y' || strings.HasPrefix(section[i:], "aaa") || strings.HasPrefix(section[i:], "gg"):
			return true
		case c == 'e' && (i+1 == len(section) || section[i+1] != '+' && section[i+1] != '-'):
			return true
		case c == 'h' || c == 's' || c == 'm':
			n := i
			for i+1 < len(section) && section[i+1] == c {
				i++
			}
			if c == 'm' && i-n >= 2 {
				return true
			}
			tokens = append(tokens, c)
		}
	}
	for i, c := range tokens {
		if c == 'm' && (i == 0 || tokens[i-1] != 'h') && (i+1 == len(tokens) || tokens[i+1] != 's') {
			return true
		}
	}
	return false
}
