package workbook

import "strings"

// stripFormulaStringLiterals removes double-quoted string constants before
// reference matching so text such as "A1" is not treated as a cell reference.
func stripFormulaStringLiterals(formula string) string {
	var out strings.Builder
	out.Grow(len(formula))
	inString := false
	for i := 0; i < len(formula); i++ {
		c := formula[i]
		if c == '"' {
			if inString && i+1 < len(formula) && formula[i+1] == '"' {
				out.WriteString("  ")
				i++
				continue
			}
			inString = !inString
			out.WriteByte(' ')
			continue
		}
		if inString {
			out.WriteByte(' ')
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type searchTextQuery struct {
	needle  string
	literal bool
}

// Lowercasing cannot introduce or remove ASCII nonletters or basic Han
// characters. For these common amount, ID and Chinese queries, strings.Contains
// can use the standard library's architecture-dispatched substring search.
func newSearchTextQuery(lower string) searchTextQuery {
	for _, r := range lower {
		if !literalSearchRune(r) {
			return searchTextQuery{needle: lower}
		}
	}
	return searchTextQuery{needle: lower, literal: true}
}

func literalSearchRune(r rune) bool {
	if r < 128 {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}
	return r >= 0x4e00 && r <= 0x9fff
}

func (q searchTextQuery) contains(value string) bool {
	if q.literal {
		return strings.Contains(value, q.needle)
	}
	return containsFold(value, q.needle)
}

func (q searchTextQuery) matchesCell(c CellData) bool {
	return q.contains(c.Raw) || (c.Display != c.Raw && q.contains(c.Display))
}

func containsFold(value, lowerNeedle string) bool {
	if lowerNeedle == "" {
		return true
	}
	ascii := true
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			ascii = false
			break
		}
	}
	for i := 0; i < len(lowerNeedle); i++ {
		if lowerNeedle[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if !ascii {
		return strings.Contains(strings.ToLower(value), lowerNeedle)
	}
	if len(lowerNeedle) > len(value) {
		return false
	}
	for i := 0; i <= len(value)-len(lowerNeedle); i++ {
		matched := true
		for j := 0; j < len(lowerNeedle); j++ {
			c := value[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerNeedle[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
