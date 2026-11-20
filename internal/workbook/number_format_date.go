package workbook

import "strings"

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
	section := strings.ToLower(splitFormatSections(code)[0])
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
