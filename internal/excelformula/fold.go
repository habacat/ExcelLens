package excelformula

import "strings"

// FoldString applies Excel's case-insensitive simple Unicode folding.
func FoldString(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToUpper(s)
	}
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		out.WriteRune(FoldRune(r))
	}
	return out.String()
}
