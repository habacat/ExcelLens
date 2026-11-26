package conditional

import (
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type Reference struct {
	ForeignSheet             string
	Row, Col                 int
	AbsoluteRow, AbsoluteCol bool
}

func ParseCellRef(raw string) (Reference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || len(s) > 12 {
		return Reference{}, false
	}
	i := 0
	absoluteCol := s[0] == '$'
	if absoluteCol {
		i++
	}
	letters := i
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i == letters || i-letters > 3 {
		return Reference{}, false
	}
	endLetters := i
	absoluteRow := i < len(s) && s[i] == '$'
	if absoluteRow {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i != len(s) || digits == i {
		return Reference{}, false
	}
	col := 0
	for _, r := range strings.ToUpper(s[letters:endLetters]) {
		col = col*26 + int(r-'A') + 1
	}
	row, err := strconv.Atoi(s[digits:])
	if err != nil || row < 1 || row > formats.ExcelRows || col < 1 || col > formats.ExcelColumns {
		return Reference{}, false
	}
	return Reference{Row: row, Col: col, AbsoluteCol: absoluteCol, AbsoluteRow: absoluteRow}, true
}

func ParseColumnRef(raw string) (Reference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > 4 {
		return Reference{}, false
	}
	i := 0
	absoluteCol := s[0] == '$'
	if absoluteCol {
		i++
	}
	letters := i
	col := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		ch := s[i]
		if ch >= 'a' {
			ch -= 32
		}
		col = col*26 + int(ch-'A'+1)
		i++
	}
	if i != len(s) || i == letters || i-letters > 3 || col < 1 || col > formats.ExcelColumns {
		return Reference{}, false
	}
	return Reference{Col: col, AbsoluteCol: absoluteCol, AbsoluteRow: true}, true
}

func ParseRowRef(raw string) (Reference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > 8 {
		return Reference{}, false
	}
	i := 0
	absoluteRow := s[0] == '$'
	if absoluteRow {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i != len(s) || i == digits {
		return Reference{}, false
	}
	row, err := strconv.Atoi(s[digits:])
	if err != nil || row < 1 || row > formats.ExcelRows {
		return Reference{}, false
	}
	return Reference{Row: row, AbsoluteRow: absoluteRow, AbsoluteCol: true}, true
}
