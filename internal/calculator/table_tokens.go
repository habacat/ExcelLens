package calculator

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// RewriteTableReferences visits bracketed table operands without requiring
// the containing function to be supported by the calculation engine.
func RewriteTableReferences(body string, replace func(string) string) string {
	if !strings.Contains(body, "[") {
		return body
	}
	var out strings.Builder
	for i := 0; i < len(body); {
		start := i
		r, n := utf8.DecodeRuneInString(body[i:])
		switch {
		case r == '"' || r == '\'':
			quote := body[i]
			i++
			for i < len(body) {
				b := body[i]
				i++
				if b == quote {
					if i < len(body) && body[i] == quote {
						i++
						continue
					}
					break
				}
			}
		case r == '[' || unicode.IsLetter(r) || r == '_' || r == '\\':
			if size := structuredToken(body[i:]); size > 0 {
				i += size
				// [Book]Sheet! is an external workbook qualifier.
				if i >= len(body) || !strings.HasPrefix(body[start:i], "[") || !isTableNameRune(body[i:]) {
					out.WriteString(replace(body[start:i]))
					continue
				}
			} else {
				i += n
				for i < len(body) && isTableNameRune(body[i:]) {
					_, w := utf8.DecodeRuneInString(body[i:])
					i += w
				}
			}
		default:
			i += n
		}
		out.WriteString(body[start:i])
	}
	return out.String()
}
func isTableNameRune(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '\\'
}
