package server

import (
	"math/big"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// sourceCellNumeric keeps original Excel typed values unchanged, while giving
// imported CSV/TXT/SQLite accounting amounts precise numeric semantics in
// statistics, field detection and pivot aggregation. Source text itself is
// always retained, including thousands grouping and leading-zero identifiers.
func sourceCellNumeric(wb *workbook.Workbook, c workbook.CellData) bool {
	return sourceCellNumber(wb, c) != nil
}

// Eligibility and value share one exact parse. The returned rational belongs
// to the caller; source text and source types are never modified.
func sourceCellNumber(wb *workbook.Workbook, c workbook.CellData) *big.Rat {
	if c.CacheMissing || c.Raw == "" {
		return nil
	}
	if c.Type == "n" || c.Type == "" {
		n, _ := analysis.Decimal(c.Raw)
		return n
	}
	if wb == nil || (c.Type != "s" && c.Type != "str" && c.Type != "inlineStr") {
		return nil
	}
	source, ok := formats.Lookup(wb.Format)
	if ok && source.TextAmounts {
		n, _ := analysis.AccountingNumericValue(c.Raw)
		return n
	}
	return nil
}
