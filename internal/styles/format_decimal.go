package styles

// Fixed decimal display uses the original decimal spelling. Converting a
// large amount to float64 first can change even its integer digits. General
// source display keeps its existing fast path; dates retain serial semantics.
import (
	"strings"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

func FormatDecimalDisplay(raw, format string, id int) (string, bool) {
	if format == "" {
		switch id {
		case 1:
			format = "0"
		case 2:
			format = "0.00"
		case 3:
			format = "#,##0"
		case 4:
			format = "#,##0.00"
		case 5, 6:
			format = `$#,##0;($#,##0)`
		case 7, 8:
			format = `$#,##0.00;($#,##0.00)`
		case 9:
			format = "0%"
		case 10:
			format = "0.00%"
		case 37, 38, 41, 42:
			format = `#,##0;(#,##0);"-"`
		case 39, 40, 43, 44:
			format = `#,##0.00;(#,##0.00);"-"`
		default:
			return "", false
		}
	}
	n, ok := excelformula.ParseDecimal(raw)
	if !ok {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(format), "General") {
		return decimalGeneralDisplay(n)
	}
	sections := SplitFormatSections(format)
	section := numericFormatSection(sections, n.Negative, n.Digits == "0")
	if IsDateFormat(section) || ambiguousFixedLiterals(section) {
		return "", false
	}
	clean, currency, parens := cleanNumberFormat(section)
	if strings.EqualFold(strings.TrimSpace(clean), "General") && !strings.ContainsAny(section, `"\_*`) {
		// A color directive does not turn the General keyword into literal
		// text. Quoted/escaped "General" must remain a literal instead.
		return decimalGeneralDisplay(n)
	}
	if n.Digits == "0" && len(sections) >= 3 {
		if dash, ok := formatZeroSectionDash(section); ok {
			return dash, true
		}
	}
	first, last := -1, -1
	for i := range clean {
		if strings.ContainsRune("0#?", rune(clean[i])) {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return clean, true
	}
	spec := clean[first : last+1]
	parts := strings.Split(spec, ".")
	if len(parts) > 2 {
		return "", false
	}
	grouping := parts[0] == "#,##0"
	minimumInteger := 1
	if !grouping {
		if strings.Trim(parts[0], "0") != "" || parts[0] == "" {
			return "", false
		}
		minimumInteger = len(parts[0])
	}
	decimals := 0
	if len(parts) == 2 {
		if strings.Trim(parts[1], "0") != "" || len(parts[1]) > 10 {
			return "", false
		}
		decimals = len(parts[1])
	}
	prefix, suffix := clean[:first], clean[last+1:]
	percent := strings.Count(prefix+suffix, "%")
	if percent > 1 {
		return "", false
	}
	text, ok := decimalFixedAbs(n, decimals, percent*2, minimumInteger, grouping)
	if !ok {
		return "", false
	}
	// Keep literal placement, including a percent sign before the mask.
	text = currency + prefix + text + suffix
	if n.Negative {
		if parens {
			if !strings.HasPrefix(text, "(") {
				text = "(" + strings.TrimPrefix(text, "-") + ")"
			}
		} else if !strings.HasPrefix(text, "-") {
			text = "-" + text
		}
	}
	if len(text) > formats.NumericDisplayBytes {
		return "", false
	}
	return text, true
}

func decimalGeneralDisplay(n excelformula.Decimal) (string, bool) {
	decimals := max(0, -n.Exponent)
	if decimals > formats.NumericDisplayBytes {
		return "", false
	}
	text, ok := decimalFixedAbs(n, int(decimals), 0, 1, false)
	if n.Negative {
		text = "-" + text
	}
	return text, ok
}

func numericFormatSection(sections []string, negative, zero bool) string {
	if len(sections) == 1 {
		return sections[0]
	}
	if negative {
		return sections[1]
	}
	if !zero || len(sections) == 2 {
		return sections[0]
	}
	return sections[2]
}

// Quoted/escaped digit placeholders cannot be inferred from the cleaned mask.
// Leave those and conditional/scaled/scientific masks to their existing path.
func ambiguousFixedLiterals(section string) bool {
	quoted, escaped := false, false
	for i := 0; i < len(section); i++ {
		c := section[i]
		if escaped {
			if strings.ContainsRune("0#?.,%", rune(c)) {
				return true
			}
			escaped = false
			continue
		}
		if c == '\\' || c == '_' || c == '*' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted && strings.ContainsRune("0#?.,%", rune(c)) {
			return true
		}
		if !quoted && c == '[' {
			at := strings.IndexByte(section[i+1:], ']')
			if at < 0 || strings.ContainsAny(section[i+1:i+1+at], "<>=") {
				return true
			}
			i += at + 1
		}
	}
	return quoted || escaped
}

func decimalFixedAbs(n excelformula.Decimal, decimals, shift, minimumInteger int, grouping bool) (string, bool) {
	point := int64(len(n.Digits)) + n.Exponent + int64(shift)
	cut := point + int64(decimals)
	if decimals < 0 || minimumInteger < 1 || max(point, int64(minimumInteger))+int64(decimals)+2 > formats.NumericDisplayBytes {
		return "", false
	}
	digits := "0"
	if n.Digits != "0" && cut >= 0 {
		if cut > formats.NumericDisplayBytes {
			return "", false
		}
		end := int(cut)
		if end < len(n.Digits) {
			digits = n.Digits[:end]
			if n.Digits[end] >= '5' {
				b := []byte(digits)
				i := len(b) - 1
				for i >= 0 && b[i] == '9' {
					b[i] = '0'
					i--
				}
				if i >= 0 {
					b[i]++
					digits = string(b)
				} else {
					digits = "1" + string(b)
				}
			}
		} else {
			digits = n.Digits + strings.Repeat("0", end-len(n.Digits))
		}
	}
	if len(digits) < decimals+minimumInteger {
		digits = strings.Repeat("0", decimals+minimumInteger-len(digits)) + digits
	}
	whole, fraction := digits, ""
	if decimals > 0 {
		whole, fraction = digits[:len(digits)-decimals], "."+digits[len(digits)-decimals:]
	}
	if grouping {
		var out strings.Builder
		out.Grow(len(whole) + len(whole)/3 + len(fraction))
		for i := range whole {
			if i > 0 && (len(whole)-i)%3 == 0 {
				out.WriteByte(',')
			}
			out.WriteByte(whole[i])
		}
		out.WriteString(fraction)
		return out.String(), out.Len() <= formats.NumericDisplayBytes
	}
	return whole + fraction, true
}
