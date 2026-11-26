package workbook

import (
	"strconv"
	"strings"

	"excellens/internal/formats"
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
	if err != nil || row <= 0 || row > maxExternalRows || col <= 0 || col > formats.ExcelColumns {
		return "", 0, 0, strconv.ErrSyntax
	}
	return colName, row, col, nil
}

func ColumnName(n int) string {
	if n <= 0 || n > formats.ExcelColumns {
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
