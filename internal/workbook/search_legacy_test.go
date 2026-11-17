package workbook

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

// Legacy standard-XML implementation retained only in tests to catch semantic changes.
func (w *Workbook) searchSheetLegacy(idx *SheetIndex, sheetName, query, mode string, limit int, cancel <-chan struct{}, progress func(scanned, total int, matches []SearchResult)) ([]SearchResult, bool, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, false, errors.New("搜索内容不能为空")
	}
	if mode != "value" && mode != "reference" {
		mode = "formula"
	}
	var unqualifiedReferencePattern, qualifiedReferencePattern *regexp.Regexp
	referenceTargetSheet := sheetName
	if mode == "reference" {
		reference := query
		if bang := strings.LastIndex(reference, "!"); bang >= 0 {
			referenceTargetSheet = strings.Trim(reference[:bang], "'")
			referenceTargetSheet = strings.ReplaceAll(referenceTargetSheet, "''", "'")
			reference = reference[bang+1:]
		}
		col, row, parseErr := ParseCellRef(reference)
		if parseErr != nil {
			return nil, false, errors.New("引用追踪地址无效")
		}
		canonicalCol := regexp.QuoteMeta(ColumnName(col))
		canonicalRow := regexp.QuoteMeta(itoa(row))
		unqualifiedReferencePattern = regexp.MustCompile(`(?i)(^|[^A-Z0-9_.!])\$?` + canonicalCol + `\$?` + canonicalRow + `([^A-Z0-9_.]|$)`)
		quotedSheet := regexp.QuoteMeta(strings.ReplaceAll(referenceTargetSheet, "'", "''"))
		plainSheet := regexp.QuoteMeta(referenceTargetSheet)
		qualifiedReferencePattern = regexp.MustCompile(`(?i)(?:'` + quotedSheet + `'|` + plainSheet + `)!\$?` + canonicalCol + `\$?` + canonicalRow + `([^A-Z0-9_.]|$)`)
		query = ColumnName(col) + itoa(row)
	}
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	matches := make([]SearchResult, 0, minInt(limit, 64))
	total := len(idx.Rows)
	for i := range idx.Rows {
		if i&15 == 0 {
			select {
			case <-cancel:
				return matches, false, errors.New("search cancelled")
			default:
			}
		}
		row, err := w.readRowFromFileMode(f, idx, i, 1, 16384, mode == "value", mode != "value")
		if err != nil {
			return matches, false, err
		}
		for _, cell := range row.Cells {
			matched := false
			if mode == "value" {
				matched = containsFold(cell.Raw, query) || containsFold(cell.Display, query)
			} else if cell.HasFormula {
				if mode == "reference" {
					formula := stripFormulaStringLiterals(cell.Formula)
					matched = qualifiedReferencePattern.MatchString(formula)
					if !matched && strings.EqualFold(sheetName, referenceTargetSheet) {
						matched = unqualifiedReferencePattern.MatchString(formula)
					}
				} else {
					matched = containsFold(cell.Formula, query)
				}
			}
			if matched {
				matches = append(matches, SearchResult{Sheet: sheetName, Ref: cell.Ref, Row: row.Number, Col: cell.Col, Formula: cell.Formula, Value: cell.Display})
				if len(matches) >= limit {
					if progress != nil {
						progress(i+1, total, matches)
					}
					return matches, true, nil
				}
			}
		}
		if progress != nil && (i&31 == 0 || i == total-1) {
			progress(i+1, total, matches)
		}
	}
	return matches, false, nil
}
