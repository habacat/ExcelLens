package searchdsl

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type token struct {
	kind, value string
	pos         int
}

func lex(source string) ([]token, error) {
	out := make([]token, 0, 32)
	for i := 0; i < len(source); {
		r, w := utf8Rune(source[i:])
		if unicode.IsSpace(r) {
			i += w
			continue
		}
		start := i
		switch source[i] {
		case '(', ')', ',', ':', '~', '=':
			out = append(out, token{kind: string(source[i]), value: string(source[i]), pos: i})
			i++
		case '!', '>', '<':
			i++
			if i < len(source) && source[i] == '=' {
				i++
			}
			out = append(out, token{kind: source[start:i], value: source[start:i], pos: start})
		default:
			if source[i] == '[' {
				i++
				for i < len(source) && source[i] != ']' {
					i++
				}
				if i >= len(source) {
					return nil, fmt.Errorf("语法位置 %d: 列名缺少 ]", start+1)
				}
				val := strings.TrimSpace(source[start+1 : i])
				i++
				if val == "" {
					return nil, fmt.Errorf("语法位置 %d: 列名不能为空", start+1)
				}
				out = append(out, token{"field", val, start})
				continue
			}
			if source[i] == '"' || source[i] == '\'' {
				quote := source[i]
				i++
				var b strings.Builder
				closed := false
				for i < len(source) {
					c := source[i]
					i++
					if c == quote {
						closed = true
						break
					}
					if c == '\\' && i < len(source) {
						next := source[i]
						if next == quote || next == '\\' {
							b.WriteByte(next)
							i++
							continue
						}
					}
					b.WriteByte(c)
				}
				if !closed {
					return nil, fmt.Errorf("语法位置 %d: 引号未闭合", start+1)
				}
				out = append(out, token{"word", b.String(), start})
				continue
			}
			// '-' preceding a token is NOT; negative numbers stay together
			// as operator operands, e.g. C2>=-100.
			if source[i] == '-' && (i+1 < len(source) && !isNumberStart(source[i+1])) {
				out = append(out, token{"NOT", "NOT", i})
				i++
				continue
			}
			for i < len(source) {
				c := source[i]
				if c == '(' || c == ')' || c == ',' || c == ':' || c == '~' || c == '=' || c == '!' || c == '>' || c == '<' || c == '[' || c == ']' || c == '"' || c == '\'' {
					break
				}
				rr, ww := utf8Rune(source[i:])
				if unicode.IsSpace(rr) {
					break
				}
				i += ww
			}
			if i == start {
				return nil, fmt.Errorf("语法位置 %d: 意外字符", start+1)
			}
			val := source[start:i]
			upper := strings.ToUpper(val)
			switch upper {
			case "AND", "OR", "NOT", "IN", "BETWEEN", "IS", "EMPTY":
				out = append(out, token{upper, upper, start})
			default:
				out = append(out, token{"word", val, start})
			}
		}
		if len(out) > 256 {
			return nil, fmt.Errorf("查询条件过多（最多256个词元）")
		}
	}
	out = append(out, token{"EOF", "", len(source)})
	return out, nil
}

func utf8Rune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }
func isNumberStart(c byte) bool     { return c >= '0' && c <= '9' || c == '.' }
