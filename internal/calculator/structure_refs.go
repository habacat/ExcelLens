package calculator

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RewriteReferences visits complete A1 operands. Sheet labels and the address
// are passed separately so callers can preserve spelling and absolute axes.
// Structured references and quoted text are opaque to coordinate operations.
func RewriteReferences(body string, change func(sheet, qualifier, address string) (string, error)) (string, error) {
	var out strings.Builder
	nameRune := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '\\'
	}
	for i := 0; i < len(body); {
		start := i
		r, n := utf8.DecodeRuneInString(body[i:])
		switch {
		case r == '"':
			i++
			for i < len(body) {
				b := body[i]
				i++
				if b == '"' {
					if i < len(body) && body[i] == '"' {
						i++
						continue
					}
					break
				}
			}
		case r == '[':
			end := structuredToken(body[i:])
			if end == 0 {
				return "", errors.New("无法定位结构化引用")
			}
			i += end
			if i < len(body) {
				after, _ := utf8.DecodeRuneInString(body[i:])
				if nameRune(after) || after == '!' {
					return "", errors.New("结构变更涉及外部工作簿引用")
				}
			}
		case r == '#' && strings.HasPrefix(body[i:], "#REF!"):
			i += len("#REF!")
		case r == '\'' || r == '$' || nameRune(r):
			ref, ok := reference(body[i:])
			if ok && ref.length > 0 {
				end := i + ref.length
				if end < len(body) {
					next, _ := utf8.DecodeRuneInString(body[end:])
					ok = !nameRune(next) && next != '['
				}
				if ok && referenceAxis(ref.start) != "" && (ref.end == "" || referenceAxis(ref.end) == referenceAxis(ref.start)) {
					raw := body[i:end]
					q := ""
					if ref.sheet != "" {
						sep := strings.LastIndexByte(raw, '!') + 1
						q, raw = raw[:sep], raw[sep:]
					}
					value, err := change(ref.sheet, q, raw)
					if err != nil {
						return "", err
					}
					out.WriteString(value)
					i = end
					continue
				}
			}
			if r == '\'' {
				// A quoted sheet may qualify a scoped name instead of a cell.
				i++
				for i < len(body) {
					if body[i] != '\'' {
						i++
						continue
					}
					i++
					if i < len(body) && body[i] == '\'' {
						i++
						continue
					}
					break
				}
			} else {
				i += n
				for i < len(body) {
					next, m := utf8.DecodeRuneInString(body[i:])
					if !nameRune(next) {
						break
					}
					i += m
				}
			}
			if i < len(body) && body[i] == '!' {
				label := body[start:i]
				decoded := label
				if strings.HasPrefix(label, "'") {
					decoded = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(label, "'"), "'"), "''", "'")
				}
				if strings.ContainsAny(decoded, "[]:") {
					return "", errors.New("结构变更涉及三维或外部引用")
				}
				value, err := change(decoded, label+"!", "")
				if err != nil {
					return "", err
				}
				out.WriteString(value)
				i++
				continue
			}
			// Sheet1:Sheet3!A1 is not two independent cell references.
			if i < len(body) && body[i] == ':' {
				tail := body[i+1:]
				if next, yes := reference(tail); yes && next.sheet != "" {
					return "", errors.New("结构变更涉及三维工作表引用")
				}
			}
		default:
			i += n
		}
		out.WriteString(body[start:i])
	}
	return out.String(), nil
}
