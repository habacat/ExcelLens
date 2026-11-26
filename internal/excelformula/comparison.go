package excelformula

import "strings"

// BooleanText accepts Excel logical literals, without trimming significant spaces.
func BooleanText(text string) (bool, bool) {
	if strings.EqualFold(text, "TRUE") {
		return true, true
	}
	if strings.EqualFold(text, "FALSE") {
		return false, true
	}
	return false, false
}
func MatchesComparison(cmp int, operator string) bool {
	switch operator {
	case "=", "equal":
		return cmp == 0
	case "<>", "notEqual":
		return cmp != 0
	case ">", "greaterThan":
		return cmp > 0
	case "<", "lessThan":
		return cmp < 0
	case ">=", "greaterThanOrEqual":
		return cmp >= 0
	case "<=", "lessThanOrEqual":
		return cmp <= 0
	}
	return false
}
