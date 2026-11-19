package calculator

import "strings"

type formulaReference struct {
	sheet, start, end string
	length            int
}

func letter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func digit(c byte) bool  { return c >= '0' && c <= '9' }
func cellAddress(s string) (string, int) {
	i := 0
	if i < len(s) && s[i] == '$' {
		i++
	}
	first := i
	for i < len(s) && letter(s[i]) {
		i++
	}
	if i == first || i-first > 3 {
		return "", 0
	}
	if i < len(s) && s[i] == '$' {
		i++
	}
	first = i
	for i < len(s) && digit(s[i]) {
		i++
	}
	if i == first || s[first] == '0' {
		return "", 0
	}
	return strings.ToUpper(strings.ReplaceAll(s[:i], "$", "")), i
}
func reference(s string) (formulaReference, bool) {
	r := formulaReference{}
	i := 0
	if strings.HasPrefix(s, "'") {
		var name strings.Builder
		i = 1
		closed := false
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					name.WriteByte('\'')
					i += 2
					continue
				}
				i++
				closed = true
				break
			}
			name.WriteByte(s[i])
			i++
		}
		if !closed || i >= len(s) || s[i] != '!' {
			return r, false
		}
		r.sheet = name.String()
		i++
	} else {
		j := 0
		for j < len(s) && !strings.ContainsRune("!+-*/^%&(),:[]=<> \t\r\n", rune(s[j])) {
			j++
		}
		if j < len(s) && s[j] == '!' {
			r.sheet = s[:j]
			i = j + 1
		}
	}
	if r.sheet != "" && strings.HasPrefix(s[i:], "#REF!") {
		r.start, r.length = "#REF!", i+len("#REF!")
		return r, true
	}
	address, n := cellAddress(s[i:])
	if n == 0 {
		start := i
		axis := i
		if axis < len(s) && s[axis] == '$' {
			axis++
		}
		letters := axis < len(s) && letter(s[axis])
		i = axisEnd(s, axis, letters)
		if i == axis || i >= len(s) || s[i] != ':' {
			return r, false
		}
		r.start = strings.ToUpper(strings.ReplaceAll(s[start:i], "$", ""))
		i++
		start = i
		if i < len(s) && s[i] == '$' {
			i++
		}
		axis = i
		i = axisEnd(s, axis, letters)
		if i == axis {
			return r, false
		}
		r.end = strings.ToUpper(strings.ReplaceAll(s[start:i], "$", ""))
		if i < len(s) && (letter(s[i]) || digit(s[i]) || s[i] == '_' || s[i] == '!') {
			return r, false
		}
		r.length = i
		return r, true
	}
	r.start = address
	i += n
	if i < len(s) && s[i] == ':' {
		end, n := cellAddress(s[i+1:])
		if n == 0 {
			return r, false
		}
		r.end = end
		i += n + 1
	}
	if i < len(s) && (letter(s[i]) || digit(s[i]) || s[i] == '_' || s[i] == '$' || s[i] == '!') {
		return r, false
	}
	r.length = i
	j := i
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	if j < len(s) && s[j] == '(' {
		return r, false
	}
	return r, true
}

// DeletedReference identifies the complete error operand Excel stores in a
// defined name after the last referenced row or column has been deleted.
func DeletedReference(raw string, hasSheet func(string) bool) bool {
	if raw == "#REF!" {
		return true
	}
	ref, ok := reference(raw)
	return ok && ref.start == "#REF!" && ref.length == len(raw) && hasSheet(ref.sheet)
}

func axisEnd(s string, i int, letters bool) int {
	for i < len(s) && (letters && letter(s[i]) || !letters && digit(s[i])) {
		i++
	}
	return i
}
