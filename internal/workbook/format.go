package workbook

import (
	"math"
	"strconv"
	"strings"
	"time"
)

func parseDimension(ref string) (minRow, maxRow, maxCol int) {
	parts := strings.Split(ref, ":")
	if len(parts) == 0 {
		return
	}
	_, r1, c1, err := parseCellRef(parts[0])
	if err == nil {
		minRow, maxRow, maxCol = r1, r1, c1
	}
	if len(parts) > 1 {
		_, r2, c2, err := parseCellRef(parts[1])
		if err == nil {
			if r2 > maxRow {
				maxRow = r2
			}
			if c2 > maxCol {
				maxCol = c2
			}
		}
	}
	return
}

func ParseCellRef(ref string) (col, row int, err error) {
	_, row, col, err = parseCellRef(ref)
	return
}

func parseCellRef(ref string) (string, int, int, error) {
	ref = strings.TrimSpace(strings.ReplaceAll(ref, "$", ""))
	if ref == "" {
		return "", 0, 0, strconv.ErrSyntax
	}
	i := 0
	for i < len(ref) {
		c := ref[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c < 'A' || c > 'Z' {
			break
		}
		i++
	}
	if i == 0 || i == len(ref) || i > 3 {
		return "", 0, 0, strconv.ErrSyntax
	}
	colName := strings.ToUpper(ref[:i])
	col := 0
	for j := 0; j < len(colName); j++ {
		col = col*26 + int(colName[j]-'A'+1)
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row <= 0 || row > maxExternalRows || col <= 0 || col > 16384 {
		return "", 0, 0, strconv.ErrSyntax
	}
	return colName, row, col, nil
}

func ColumnName(n int) string {
	if n <= 0 || n > 16384 {
		return ""
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		n--
		i--
		b[i] = byte('A' + n%26)
		n /= 26
	}
	return string(b[i:])
}

// formatNumeric is the single entry point for ALL numeric cell display values,
// including cached results of SUMIF, INDEX, XLOOKUP, shared formulas, etc.
// Normalization happens before style lookup: valid XLSX files may omit styles
// entirely or reference a missing XF. A missing style must not leak "-0".
// Raw cache bytes are retained in CellData.Raw for lossless audit inspection.
func (w *Workbook) formatNumeric(raw string, style int) string {
	if raw == "" {
		return raw
	}
	// General (or a missing XF) is by far the most common format in large
	// reporting sheets. ExcelLens preserves its exact numeric source spelling,
	// including long identifiers; the only normalization needed is an exact
	// signed/decimal zero. Avoid converting every ordinary number to float64.
	if style < 0 || style >= len(w.Styles) || w.Styles[style].NumFmtID == 0 {
		if isDecimalZero(raw) {
			if v, err := strconv.ParseFloat(raw, 64); err == nil && v == 0 {
				return "0"
			}
		}
		return raw
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return raw
	}
	// Only normalize an *exact decimal zero*, never a nonzero value which
	// underflowed float64 or merely rounds to zero at a format's precision.
	if v == 0 {
		if !isDecimalZero(raw) {
			// A decimal smaller than float64's minimum magnitude must not be
			// turned into a fabricated zero during number formatting.
			return raw
		}
		v = 0
		raw = "0"
	}
	if style < 0 || style >= len(w.Styles) {
		return raw
	}
	fmtID := w.Styles[style].NumFmtID
	format := w.NumFmts[fmtID]
	if display, ok := formatDecimalDisplay(raw, format, fmtID); ok {
		return display
	}
	// An explicit or source-localized format overrides the built-in fallback.
	if format != "" {
		return formatCustomNumber(v, format, w.Date1904)
	}
	switch fmtID {
	case 0:
		return raw
	case 1:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case 2:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case 3:
		return commaNumber(v, 0)
	case 4:
		return commaNumber(v, 2)
	case 5, 6:
		return formatCustomNumber(v, `$#,##0;($#,##0)`, w.Date1904)
	case 7, 8:
		return formatCustomNumber(v, `$#,##0.00;($#,##0.00)`, w.Date1904)
	case 9:
		return strconv.FormatFloat(v*100, 'f', 0, 64) + "%"
	case 10:
		return strconv.FormatFloat(v*100, 'f', 2, 64) + "%"
	case 11, 48:
		return strconv.FormatFloat(v, 'E', 2, 64)
	case 14, 15, 16, 17, 18, 19, 20, 21, 22, 45, 46, 47:
		return formatExcelDate(v, w.Date1904, fmtID)
	case 37, 38:
		return formatAccounting(v, 0, true)
	case 39, 40:
		return formatAccounting(v, 2, true)
	case 41, 42:
		return formatAccounting(v, 0, true)
	case 43, 44:
		return formatAccounting(v, 2, true)
	}
	if format == "" {
		return raw
	}
	return formatCustomNumber(v, format, w.Date1904)
}

// PresentNumeric applies the workbook's number format and color to a draft
// value without changing the source model or its row cache.
func (w *Workbook) PresentNumeric(cell *CellData) {
	cell.Display = w.formatNumeric(cell.Raw, cell.Style)
	cell.ValueColor = w.numericTextColor(cell.Raw, cell.Style)
}

// isDecimalZero tests the literal OOXML mantissa rather than the float64
// result. For example, -1e-999 is mathematically nonzero even when parsing
// it to float64 would underflow; it must never be silently changed to zero.
func isDecimalZero(raw string) bool {
	mantissa := strings.TrimSpace(raw)
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		mantissa = mantissa[:i]
	}
	mantissa = strings.TrimPrefix(strings.TrimPrefix(mantissa, "+"), "-")
	if mantissa == "" {
		return false
	}
	digit := false
	for i := 0; i < len(mantissa); i++ {
		if mantissa[i] == '.' {
			continue
		}
		if mantissa[i] != '0' {
			return false
		}
		digit = true
	}
	return digit
}

func formatCustomNumber(v float64, format string, date1904 bool) string {
	if strings.EqualFold(strings.TrimSpace(format), "General") {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	sections := splitFormatSections(format)
	section := numericFormatSection(sections, v < 0, v == 0)
	if section == "" {
		return ""
	}
	if isDateFormat(section) {
		return formatCustomDate(v, section, date1904)
	}
	clean, currency, parens := cleanNumberFormat(section)
	if clean == "" {
		return ""
	}
	if v == 0 && len(sections) >= 3 {
		// Excel's accounting zero sections commonly contain a literal dash
		// followed by '?' alignment placeholders, e.g. _(* "-"??_).
		// Those '?' placeholders reserve digit space; they are NOT zeros.
		// Treating them as mandatory digits produces the infamous "-0".
		if dash, ok := formatZeroSectionDash(section); ok {
			return dash
		}
		if !strings.ContainsAny(clean, "0#?") {
			return clean
		}
	}
	first, last := -1, -1
	for i := 0; i < len(clean); i++ {
		if clean[i] == '0' || clean[i] == '#' || clean[i] == '?' {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return clean
	}
	prefix, suffix := clean[:first], clean[last+1:]
	spec := clean[first : last+1]
	percent := strings.Contains(spec, "%") || strings.Contains(suffix, "%")
	spec = strings.ReplaceAll(spec, "%", "")
	suffix = strings.ReplaceAll(suffix, "%", "")
	negative := v < 0
	abs := math.Abs(v)
	if percent {
		abs *= 100
	}
	if strings.Contains(strings.ToUpper(spec), "E+") || strings.Contains(strings.ToUpper(spec), "E-") {
		dec := decimalPlaces(spec)
		return strconv.FormatFloat(v, 'E', dec, 64)
	}
	dec := decimalPlaces(spec)
	if dec > 10 {
		dec = 10
	}
	var number string
	if hasGrouping(spec) {
		number = commaNumber(abs, dec)
	} else {
		number = strconv.FormatFloat(abs, 'f', dec, 64)
	}
	if percent {
		number += "%"
	}
	if currency != "" {
		prefix = currency + prefix
	}
	number = prefix + number + suffix
	if negative {
		if parens || strings.Contains(section, "(") && strings.Contains(section, ")") {
			if !strings.HasPrefix(number, "(") {
				number = "(" + strings.TrimPrefix(number, "-") + ")"
			}
		} else if !strings.HasPrefix(number, "-") {
			number = "-" + number
		}
	}
	return number
}

// formatZeroSectionDash recognizes Excel's dash-for-zero accounting section.
// A quoted or escaped dash is meaningful text; optional #/? digit positions
// and spacer directives must not be converted into literal zero digits.
// This is deliberately narrow: formats with mandatory digit placeholders are
// left to the numeric formatter, and raw/cached numeric values stay intact.
func formatZeroSectionDash(section string) (string, bool) {
	// Detect only an alignment-padded dash, not a numeric or textual zero
	// section. Other visible literals (units, currency, parentheses, etc.)
	// invalidate this fast path: deleting them would falsify the display.
	dashCount := 0
	mandatory := false
	otherLiteral := false
	quoted, escaped := false, false
	for i := 0; i < len(section); i++ {
		c := section[i]
		if escaped {
			if c == '-' {
				dashCount++
			} else if c != ' ' && c != '\t' {
				otherLiteral = true
			}
			escaped = false
			continue
		}
		if quoted {
			if c == '"' {
				quoted = false
			} else if c == '-' {
				dashCount++
			} else if c != ' ' && c != '\t' {
				otherLiteral = true
			}
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(section[i+1:], ']'); end >= 0 {
				i += end + 1
				continue
			}
			otherLiteral = true
			continue
		}
		if c == '_' || c == '*' {
			// Alignment consumes the next rune/character; it does not print.
			if i+1 < len(section) {
				i++
			}
			continue
		}
		switch c {
		case '-':
			dashCount++
		case '0':
			mandatory = true
		case '?', '#', ' ', '\t', '.', ',':
			// Optional digit spacing, thousands/decimal separators.
		default:
			otherLiteral = true
		}
	}
	return "-", !mandatory && !otherLiteral && !quoted && !escaped && dashCount == 1
}

func splitFormatSections(format string) []string {
	parts := make([]string, 0, 4)
	start := 0
	quoted, escaped := false, false
	bracket := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == '[' {
			bracket++
		} else if c == ']' && bracket > 0 {
			bracket--
		} else if c == ';' && bracket == 0 {
			parts = append(parts, format[start:i])
			start = i + 1
		}
	}
	parts = append(parts, format[start:])
	return parts
}

func cleanNumberFormat(section string) (clean, currency string, parens bool) {
	var b strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(section); i++ {
		c := section[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			b.WriteByte(c)
			continue
		}
		if c == '[' {
			end := strings.IndexByte(section[i+1:], ']')
			if end >= 0 {
				content := section[i+1 : i+1+end]
				if strings.HasPrefix(content, "$") {
					code := strings.TrimPrefix(content, "$")
					if dash := strings.IndexByte(code, '-'); dash >= 0 {
						code = code[:dash]
					}
					currency = code
				}
				i += end + 1
				continue
			}
		}
		if c == '_' || c == '*' {
			i++
			continue
		}
		if c == '(' {
			parens = true
		}
		b.WriteByte(c)
	}
	clean = b.String()
	return strings.TrimSpace(clean), currency, parens
}

func decimalPlaces(format string) int {
	quoted, escaped := false, false
	inFraction := false
	count := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				i += end + 1
				continue
			}
		}
		if c == '.' {
			inFraction = true
			continue
		}
		if inFraction {
			if c == '0' || c == '#' || c == '?' {
				count++
			} else if c != ',' {
				break
			}
		}
	}
	return count
}

func hasGrouping(format string) bool {
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == ',' {
			return true
		}
	}
	return false
}

func formatAccounting(v float64, decimals int, grouping bool) string {
	if v == 0 {
		return "-"
	}
	// Excel accounting formats round exact half values away from zero.
	// strconv.FormatFloat uses round-to-even for ties, so 1234.5 must
	// display as 1,235 rather than 1,234 in the zero-decimal formats.
	if decimals == 0 {
		v = math.Round(v)
	}
	if v < 0 {
		v = -v
		if grouping {
			return "(" + commaNumber(v, decimals) + ")"
		}
		return "(" + strconv.FormatFloat(v, 'f', decimals, 64) + ")"
	}
	if grouping {
		return commaNumber(v, decimals)
	}
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

func commaNumber(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.SplitN(s, ".", 2)
	whole := parts[0]
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if len(parts) == 2 {
		whole += "." + parts[1]
	}
	if neg {
		whole = "-" + whole
	}
	return whole
}

func isDateFormat(format string) bool {
	var b strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				content := strings.ToLower(format[i+1 : i+1+end])
				if content == "h" || content == "hh" || content == "m" || content == "mm" || content == "s" || content == "ss" {
					return true
				}
				i += end + 1
				continue
			}
		}
		b.WriteByte(byte(strings.ToLower(string(c))[0]))
	}
	v := b.String()
	return strings.Contains(v, "y") || strings.Contains(v, "d") || strings.Contains(v, "h") || strings.Contains(v, "s") || strings.Contains(v, "m")
}

func formatCustomDate(serial float64, format string, date1904 bool) string {
	if serial < 0 || serial > 2958465 {
		return strconv.FormatFloat(serial, 'f', -1, 64)
	}
	base := excelSerialTime(serial, date1904)
	fakeLeapDay := !date1904 && serial >= 60 && serial < 61 && base.Day() == 28
	hasDatePart := formatHasToken(format, 'y') || formatHasToken(format, 'd')
	timeStart := firstFormatToken(format, 'h')
	has12Hour := strings.Contains(strings.ToUpper(format), "AM/PM") || strings.Contains(strings.ToUpper(format), "A/P")
	var out strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(format); {
		c := format[i]
		if escaped {
			out.WriteByte(c)
			escaped = false
			i++
			continue
		}
		if c == '\\' || c == '_' || c == '*' {
			escaped = true
			i++
			continue
		}
		if c == '"' {
			quoted = !quoted
			i++
			continue
		}
		if quoted {
			out.WriteByte(c)
			i++
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				content := strings.ToLower(format[i+1 : i+1+end])
				switch content {
				case "h", "hh":
					hours := int64(math.Floor(serial * 24))
					if content == "hh" && hours < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(hours, 10))
				case "m", "mm":
					minutes := int64(math.Floor(serial * 1440))
					if content == "mm" && minutes < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(minutes, 10))
				case "s", "ss":
					seconds := int64(math.Floor(serial * 86400))
					if content == "ss" && seconds < 10 {
						out.WriteByte('0')
					}
					out.WriteString(strconv.FormatInt(seconds, 10))
				}
				i += end + 2
				continue
			}
		}
		if strings.EqualFold(format[i:], "AM/PM") {
			out.WriteString(base.Format("PM"))
			i += 5
			continue
		}
		if strings.EqualFold(format[i:], "A/P") {
			out.WriteString(base.Format("P"))
			i += 3
			continue
		}
		if c == 'y' || c == 'Y' || c == 'm' || c == 'M' || c == 'd' || c == 'D' || c == 'h' || c == 'H' || c == 's' || c == 'S' {
			j := i + 1
			for j < len(format) && (format[j] == c || format[j] == c+32 || format[j] == c-32) {
				j++
			}
			token := strings.ToLower(format[i:j])
			switch token[0] {
			case 'y':
				if len(token) >= 4 {
					out.WriteString(base.Format("2006"))
				} else {
					out.WriteString(base.Format("06"))
				}
			case 'd':
				if len(token) >= 4 {
					out.WriteString(base.Format("Monday"))
				} else if len(token) == 3 {
					out.WriteString(base.Format("Mon"))
				} else if fakeLeapDay {
					out.WriteString("29")
				} else if len(token) == 2 {
					out.WriteString(base.Format("02"))
				} else {
					out.WriteString(base.Format("2"))
				}
			case 'h':
				if has12Hour {
					if len(token) >= 2 {
						out.WriteString(base.Format("03"))
					} else {
						out.WriteString(base.Format("3"))
					}
				} else {
					out.WriteString(base.Format("15"))
				}
			case 's':
				if len(token) >= 2 {
					out.WriteString(base.Format("05"))
				} else {
					out.WriteString(base.Format("5"))
				}
			case 'm':
				isMinute := (timeStart >= 0 && i > timeStart) || (!hasDatePart && (formatHasToken(format, 'h') || formatHasToken(format, 's')))
				if isMinute {
					if len(token) >= 2 {
						out.WriteString(base.Format("04"))
					} else {
						out.WriteString(base.Format("4"))
					}
				} else if len(token) >= 4 {
					out.WriteString(base.Format("January"))
				} else if len(token) == 3 {
					out.WriteString(base.Format("Jan"))
				} else if len(token) == 2 {
					out.WriteString(base.Format("01"))
				} else {
					out.WriteString(base.Format("1"))
				}
			}
			i = j
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func formatHasToken(format string, wanted byte) bool {
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				i += end + 1
				continue
			}
		}
		if c == wanted || c == wanted-32 || c == wanted+32 {
			return true
		}
	}
	return false
}

func firstFormatToken(format string, wanted byte) int {
	quoted, escaped := false, false
	for i := 0; i < len(format); i++ {
		c := format[i]
		if escaped {
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
		if quoted {
			continue
		}
		if c == '[' {
			if end := strings.IndexByte(format[i+1:], ']'); end >= 0 {
				i += end + 1
				continue
			}
		}
		if c == wanted || c == wanted-32 || c == wanted+32 {
			return i
		}
	}
	return -1
}

func formatExcelDate(serial float64, date1904 bool, fmtID int) string {
	if serial < 0 || serial > 2958465 {
		return strconv.FormatFloat(serial, 'f', -1, 64)
	}
	t := excelSerialTime(serial, date1904)
	fakeLeapDay := !date1904 && serial >= 60 && serial < 61 && t.Day() == 28
	switch fmtID {
	case 15:
		if fakeLeapDay {
			return "29-" + t.Format("Jan-06")
		}
		return t.Format("2-Jan-06")
	case 16:
		if fakeLeapDay {
			return "29-" + t.Format("Jan")
		}
		return t.Format("2-Jan")
	case 17:
		return t.Format("Jan-06")
	case 18:
		return t.Format("3:04 PM")
	case 19:
		return t.Format("3:04:05 PM")
	case 20:
		return t.Format("15:04")
	case 21:
		return t.Format("15:04:05")
	case 22:
		if fakeLeapDay {
			return "1900-02-29 " + t.Format("15:04")
		}
		return t.Format("2006-01-02 15:04")
	case 45:
		return t.Format("15:04")
	case 46:
		return t.Format("15:04:05")
	case 47:
		return t.Format("15:04:05.000")
	default:
		if fakeLeapDay {
			return "1900-02-29"
		}
		return t.Format("2006-01-02")
	}
}

func excelSerialTime(serial float64, date1904 bool) time.Time {
	whole := int64(math.Floor(serial))
	fraction := serial - float64(whole)
	var base time.Time
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if whole < 60 {
		base = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	} else {
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	if !date1904 && whole == 60 {
		return time.Date(1900, 2, 28, 0, 0, 0, 0, time.UTC).Add(time.Duration(math.Round(fraction*86400)) * time.Second)
	}
	t := base.AddDate(0, 0, int(whole))
	seconds := int64(math.Round(fraction * 86400))
	if seconds >= 86400 {
		t = t.AddDate(0, 0, 1)
		seconds = 0
	}
	return t.Add(time.Duration(seconds) * time.Second)
}
