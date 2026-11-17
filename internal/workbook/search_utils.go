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
