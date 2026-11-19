package editfile

import (
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
)

// Read one column or item specifier, keeping Excel's escape quote until
// resolution so a column literally named #Data is distinct from the selector.
func tableSpecifier(s string) (string, string, bool) {
	if len(s) < 2 || s[0] != '[' {
		return "", "", false
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if i+1 < len(s) && strings.ContainsRune("[]#@'", rune(s[i+1])) {
				i++
			}
		case '[':
			return "", "", false
		case ']':
			return s[1:i], s[i+1:], i > 1
		}
	}
	return "", "", false
}

var tableCaptionUnescape = strings.NewReplacer("'[", "[", "']", "]", "'#", "#", "'@", "@", "''", "'")
var tableCaptionEscape = strings.NewReplacer("'", "''", "[", "'[", "]", "']", "#", "'#", "@", "'@")

func (l XLSXLayout) tableReference(raw string, a editdoc.Address) (calculator.WorksheetReference, string, bool) {
	var ref calculator.WorksheetReference
	name, tail, bracket := strings.Cut(raw, "[")
	var t *xlsxTable
	if name == "" {
		t, _ = l.tableAt(a)
	} else {
		for i := range l.Tables {
			if strings.EqualFold(l.Tables[i].Name, name) {
				t = &l.Tables[i]
				break
			}
		}
	}
	if t == nil {
		return ref, "", false
	}
	bounds := t.bounds()
	first, last := t.dataRows()
	ref = calculator.WorksheetReference{Sheet: t.Sheet, Top: first, Bottom: last, Left: bounds.firstCol, Right: bounds.lastCol}
	if !bracket {
		return ref, t.Name, first <= last
	}
	inner, ok := strings.CutSuffix(tail, "]")
	if !ok || inner == "" {
		return ref, "", false
	}
	item := ""
	if strings.HasPrefix(inner, "@") {
		item = "#This Row"
		inner = inner[1:]
	}
	var fields []string
	if strings.HasPrefix(inner, "[") {
		for {
			word, rest, ok := tableSpecifier(inner)
			if !ok {
				return ref, "", false
			}
			if strings.HasPrefix(word, "#") {
				if item != "" || len(fields) != 0 {
					return ref, "", false
				}
				item = word
				if rest != "" && !strings.HasPrefix(rest, ",") {
					return ref, "", false
				}
			} else {
				fields = append(fields, word)
				if rest != "" && (!strings.HasPrefix(rest, ":") || len(fields) != 1) {
					return ref, "", false
				}
			}
			if rest == "" {
				break
			}
			inner = rest[1:]
		}
	} else if strings.HasPrefix(inner, "#") {
		if item != "" {
			return ref, "", false
		}
		item = inner
	} else if inner != "" {
		fields = append(fields, inner)
	}
	switch strings.ToUpper(item) {
	case "":
	case "#DATA":
		item = "#Data"
	case "#ALL":
		item = "#All"
		ref.Top, ref.Bottom = bounds.firstRow, bounds.lastRow
	case "#HEADERS":
		if t.Headers == 0 {
			return ref, "", false
		}
		item = "#Headers"
		ref.Top, ref.Bottom = bounds.firstRow, bounds.firstRow
	case "#TOTALS":
		if t.Totals == 0 {
			return ref, "", false
		}
		item = "#Totals"
		ref.Top, ref.Bottom = bounds.lastRow, bounds.lastRow
	case "#THIS ROW":
		if a.Row < first || a.Row > last {
			return ref, "", false
		}
		item = "#This Row"
		ref.Top, ref.Bottom = a.Row, a.Row
	default:
		return ref, "", false
	}
	var captions []string
	for j, word := range fields {
		field := tableCaptionUnescape.Replace(word)
		index := -1
		for i, c := range t.Columns {
			if strings.EqualFold(c.Name, field) {
				index = i
				captions = append(captions, "["+tableCaptionEscape.Replace(c.Name)+"]")
				break
			}
		}
		if index < 0 {
			return ref, "", false
		}
		if j == 0 {
			ref.Left = bounds.firstCol + index
		}
		ref.Right = bounds.firstCol + index
	}
	if ref.Left > ref.Right || ref.Top > ref.Bottom {
		return ref, "", false
	}
	sel := strings.Join(captions, ":")
	if item != "" {
		if sel == "" {
			sel = "[" + item + "]"
		} else {
			sel = "[[" + item + "]," + sel + "]"
		}
	} else if len(captions) == 2 {
		sel = "[" + sel + "]"
	}
	return ref, t.Name + sel, true
}
