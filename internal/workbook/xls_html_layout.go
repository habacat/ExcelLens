package workbook

import (
	"math"
	"strconv"
	"strings"
)

// Read only fixed dimensions declared by the document. A percentage is useful
// only when its table has a known fixed width; browser auto-layout is not guessed.
func (m *htmlStyleMetadata) dimension(token htmlTableToken, name string, reference float64) float64 {
	value, _ := token.attr(name)
	class, _ := token.attr("class")
	inline, _ := token.attr("style")
	var css strings.Builder
	for _, name := range strings.Fields(class) {
		css.WriteString(m.rules[name])
		css.WriteByte(';')
	}
	css.WriteString(inline)
	for _, decl := range strings.Split(css.String(), ";") {
		key, v, ok := strings.Cut(decl, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			value = strings.TrimSpace(v)
		}
	}
	value = strings.ToLower(strings.TrimSpace(value))
	factor := 1.0
	switch {
	case strings.HasSuffix(value, "pt"):
		factor, value = 4.0/3, strings.TrimSuffix(value, "pt")
	case strings.HasSuffix(value, "px"):
		value = strings.TrimSuffix(value, "px")
	case strings.HasSuffix(value, "%"):
		factor, value = reference/100, strings.TrimSuffix(value, "%")
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n*factor > 1_000_000 {
		return 0
	}
	return n * factor
}

func (s *htmlDocumentSheet) addColumns(first, count int, size float64, explicit ...bool) {
	if size < 10 || size > 1200 || first < 1 || first+count-1 > 16384 {
		return
	}
	if s.Cols == nil {
		s.Cols = make(map[int]float64)
	}
	if s.widthVotes == nil {
		s.widthVotes = make(map[int]map[float64]int)
		s.explicitCols = make(map[int]bool)
	}
	size = math.Round(size*1000) / 1000
	for c := first; c < first+count; c++ {
		if len(explicit) > 0 && explicit[0] {
			s.Cols[c] = size
			s.explicitCols[c] = true
			continue
		}
		if s.explicitCols[c] {
			continue
		}
		votes := s.widthVotes[c]
		if votes == nil {
			votes = make(map[float64]int)
			s.widthVotes[c] = votes
		}
		if _, ok := votes[size]; !ok && len(votes) >= 16 {
			continue
		}
		votes[size]++
		// Title tables may use a different grid. Prefer widths repeated by
		// actual report rows; explicit <col> declarations take priority above.
		if s.Cols[c] == 0 || votes[size] > votes[s.Cols[c]] {
			s.Cols[c] = size
		}
	}
}

func (s *htmlDocumentSheet) addRowSize(row int, size float64) {
	appendSourceRowSize(&s.Rows, row, size)
}
