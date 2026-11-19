package editfile

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"excellens/internal/calculator"
	"excellens/internal/formats"
)

// Item references belong to a cache field, not to worksheet coordinates.
// Give each operand a local name in the shared worksheet calculator while
// retaining the field-qualified Excel expression for persistence.
type itemExpression struct {
	form   calculator.EditableFormula
	stored string
	refs   []int
}

func pivotToken(s string) (text string, n int, ok bool) {
	if len(s) == 0 {
		return "", 0, false
	}
	if s[0] == '\'' {
		var out strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] != '\'' {
				out.WriteByte(s[i])
				continue
			}
			if i+1 < len(s) && s[i+1] == '\'' {
				out.WriteByte('\'')
				i++
				continue
			}
			return out.String(), i + 1, true
		}
		return "", 0, false
	}
	for n < len(s) {
		r, size := utf8.DecodeRuneInString(s[n:])
		if !(unicode.IsLetter(r) || r == '_' || n > 0 && (unicode.IsDigit(r) || r == '.')) {
			break
		}
		n += size
	}
	return s[:n], n, n > 0
}

func itemIdentifier(value string) string {
	if word, n, ok := pivotToken(value); ok && n == len(value) && word == value {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func itemOperand(i int) string { return "_PIVOTITEM" + strconv.Itoa(i) }

func compileItemFormula(ctx context.Context, body, field string, dict pivotDictionary) (itemExpression, error) {
	var out itemExpression
	body = strings.TrimSpace(strings.TrimPrefix(body, "="))
	if len(body) == 0 || len(body) >= formats.EditFormulaBytes {
		return out, fmt.Errorf("计算项目公式长度无效")
	}
	lookup := map[string]int{}
	for i, v := range dict.values {
		if v.kind == "m" {
			continue
		}
		k := strings.ToLower(v.raw)
		if _, exists := lookup[k]; exists {
			lookup[k] = -1
		} else {
			lookup[k] = i
		}
	}
	var normalized, saved strings.Builder
	for pos := 0; pos < len(body); {
		r, size := utf8.DecodeRuneInString(body[pos:])
		if r == '"' {
			end := pos + size
			for end < len(body) {
				if body[end] == '"' {
					end++
					if end == len(body) || body[end] != '"' {
						break
					}
				}
				end++
			}
			normalized.WriteString(body[pos:end])
			saved.WriteString(body[pos:end])
			pos = end
			continue
		}
		if r >= '0' && r <= '9' || r == '.' {
			end := pos
			for end < len(body) && (body[end] >= '0' && body[end] <= '9' || body[end] == '.') {
				end++
			}
			if end < len(body) && (body[end] == 'e' || body[end] == 'E') {
				k := end + 1
				if k < len(body) && (body[k] == '+' || body[k] == '-') {
					k++
				}
				start := k
				for k < len(body) && body[k] >= '0' && body[k] <= '9' {
					k++
				}
				if k > start {
					end = k
				}
			}
			normalized.WriteString(body[pos:end])
			saved.WriteString(body[pos:end])
			pos = end
			continue
		}
		token, n, ok := pivotToken(body[pos:])
		qualified := false
		if pos+n < len(body) && body[pos+n] == '[' {
			if n != 0 && !strings.EqualFold(token, field) {
				return out, fmt.Errorf("计算项目引用了其他字段 %s", token)
			}
			qualified = true
			start := pos + n + 1
			if start >= len(body) {
				return out, ErrXLSX
			}
			if body[start] == '\'' {
				s, k, ok := pivotToken(body[start:])
				end := start + k
				if !ok || end >= len(body) || body[end] != ']' {
					return out, fmt.Errorf("计算项目的引号或方括号未闭合")
				}
				token, n = s, end+1-pos
			} else {
				end := strings.IndexByte(body[start:], ']')
				if end < 0 {
					return out, fmt.Errorf("计算项目缺少右方括号")
				}
				token, n = body[start:start+end], start+end+1-pos
			}
			ok = true
		}
		if !ok {
			normalized.WriteRune(r)
			saved.WriteRune(r)
			pos += size
			continue
		}
		rest := strings.TrimLeftFunc(body[pos+n:], unicode.IsSpace)
		x, known := lookup[strings.ToLower(token)]
		if !qualified && (strings.HasPrefix(rest, "(") || !known && (strings.EqualFold(token, "FALSE") || strings.EqualFold(token, "TRUE"))) {
			normalized.WriteString(token)
			saved.WriteString(token)
			pos += n
			continue
		}
		if !known || x < 0 {
			return out, fmt.Errorf("字段 %s 中没有唯一的项目 %q", field, token)
		}
		normalized.WriteString(itemOperand(x))
		saved.WriteString(itemIdentifier(field) + "[" + itemIdentifier(dict.values[x].raw) + "]")
		out.refs = append(out.refs, x)
		pos += n
	}
	f, err := calculator.ParseLocalFormula(ctx, normalized.String(), nil, func(name string) (bool, bool) { return true, strings.HasPrefix(name, "_PIVOTITEM") })
	if err != nil || !f.Scalar {
		return out, fmt.Errorf("计算项目需要使用同一字段中的项目及标量公式")
	}
	out.form, out.stored = f, saved.String()
	return out, nil
}

func (expr itemExpression) evaluate(ctx context.Context, values map[int]calculator.FormulaValue) (pivotValue, error) {
	form := expr.form.BindWorksheet(1, 1, false, func(name string) calculator.WorksheetName {
		i, _ := strconv.Atoi(strings.TrimPrefix(name, "_PIVOTITEM"))
		v, ok := values[i]
		if !ok {
			v = calculator.NumberValue(new(big.Rat))
		}
		return calculator.WorksheetName{Constant: v}
	})
	v, err := form.Evaluate(ctx, nil)
	if err != nil {
		return pivotValue{}, err
	}
	out, err := calcOutput(v)
	if err != nil {
		return pivotValue{}, err
	}
	switch out.Kind {
	case "number":
		return pivotValue{"n", out.Raw}, nil
	case "error":
		return pivotValue{"e", out.Raw}, nil
	case "boolean":
		if v.Bool {
			return pivotValue{"n", "1"}, nil
		}
		return pivotValue{"n", "0"}, nil
	}
	return pivotValue{"e", "#VALUE!"}, nil
}
