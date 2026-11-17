package workbook

import (
	"strings"
)

func translateSharedFormula(formula, anchorRef, targetRef string) string {
	anchorCol, anchorRow, err := ParseCellRef(anchorRef)
	if err != nil {
		return formula
	}
	targetCol, targetRow, err := ParseCellRef(targetRef)
	if err != nil {
		return formula
	}
	deltaCol, deltaRow := targetCol-anchorCol, targetRow-anchorRow
	if deltaCol == 0 && deltaRow == 0 {
		return formula
	}
	var out strings.Builder
	out.Grow(len(formula) + 16)
	for i := 0; i < len(formula); {
		if formula[i] == '[' {
			start := i
			depth := 0
			for i < len(formula) {
				if formula[i] == '[' {
					depth++
				}
				if formula[i] == ']' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
				i++
			}
			out.WriteString(formula[start:i])
			continue
		}
		if formula[i] == '"' {
			start := i
			i++
			for i < len(formula) {
				if formula[i] == '"' {
					i++
					if i < len(formula) && formula[i] == '"' {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(formula[start:i])
			continue
		}
		if formula[i] == '\'' {
			start := i
			i++
			for i < len(formula) {
				if formula[i] == '\'' {
					i++
					if i < len(formula) && formula[i] == '\'' {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(formula[start:i])
			continue
		}
		if ref, end, ok := parseFormulaCellRef(formula, i, deltaCol, deltaRow); ok {
			out.WriteString(ref)
			i = end
			continue
		}
		out.WriteByte(formula[i])
		i++
	}
	return out.String()
}

func parseFormulaCellRef(s string, start, deltaCol, deltaRow int) (string, int, bool) {
	if start > 0 && isNameChar(s[start-1]) {
		return "", start, false
	}
	i := start
	absCol := false
	absRow := false
	if i < len(s) && s[i] == '$' {
		absCol = true
		i++
	}
	colStart := i
	for i < len(s) && ((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) && i-colStart < 4 {
		i++
	}
	colLen := i - colStart
	if colLen < 1 || colLen > 3 {
		return "", start, false
	}
	if i < len(s) && s[i] == '$' {
		absRow = true
		i++
	}
	rowStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if rowStart == i {
		return "", start, false
	}
	if i < len(s) && (isNameChar(s[i]) || s[i] == '!') {
		return "", start, false
	}
	if i < len(s) && s[i] == '(' {
		return "", start, false
	}
	col := 0
	for j := colStart; j < colStart+colLen; j++ {
		c := s[j]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		col = col*26 + int(c-'A'+1)
	}
	row := 0
	for j := rowStart; j < i; j++ {
		row = row*10 + int(s[j]-'0')
	}
	if col < 1 || col > 16384 || row < 1 || row > 1048576 {
		return "", start, false
	}
	newCol, newRow := col, row
	if !absCol {
		newCol += deltaCol
	}
	if !absRow {
		newRow += deltaRow
	}
	if newCol < 1 || newCol > 16384 || newRow < 1 || newRow > 1048576 {
		return "#REF!", i, true
	}
	colText := s[colStart : colStart+colLen]
	if !absCol {
		colText = ColumnName(newCol)
	}
	rowText := s[rowStart:i]
	if !absRow {
		rowText = itoa(newRow)
	}
	prefix := ""
	if absCol {
		prefix = "$"
	}
	if absRow {
		rowText = "$" + rowText
	}
	return prefix + colText + rowText, i, true
}

func isNameChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.'
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
