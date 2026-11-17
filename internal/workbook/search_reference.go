package workbook

import (
	"regexp"
	"strings"
)

// sharedReferenceCandidates is a conservative fast precheck for reference-mode
// search. A formula can only reference one target address if some A1 token in
// its anchor translates to that address. Uncertain cases fall through to the
// original regex matcher; the precheck never decides a positive result.
var coarseA1Pattern = regexp.MustCompile(`(?i)\$?[A-Z]{1,3}\$?[1-9][0-9]*`)

type relativeRef struct {
	col, row       int
	absCol, absRow bool
}

type sharedRefCandidates struct {
	refs                 []relativeRef
	anchorCol, anchorRow int
	uncertain            bool
}

func newSharedRefCandidates(anchor SharedFormulaAnchor) sharedRefCandidates {
	ac, ar, err := ParseCellRef(anchor.Ref)
	if err != nil {
		return sharedRefCandidates{uncertain: true}
	}
	result := sharedRefCandidates{anchorCol: ac, anchorRow: ar}
	formula := stripFormulaStringLiterals(anchor.Formula)
	matches := coarseA1Pattern.FindAllStringIndex(formula, -1)
	result.refs = make([]relativeRef, 0, len(matches))
	for _, match := range matches {
		word := formula[match[0]:match[1]]
		colon := strings.IndexByte(word, '$')
		absCol := strings.HasPrefix(word, "$")
		absRow := colon > 0 || (absCol && strings.LastIndexByte(word, '$') > 0)
		col, row, e := ParseCellRef(strings.ReplaceAll(word, "$", ""))
		if e == nil {
			result.refs = append(result.refs, relativeRef{col: col, row: row, absCol: absCol, absRow: absRow})
		}
	}
	return result
}
func (s sharedRefCandidates) potentiallyReferences(cellRef string, targetCol, targetRow int) bool {
	if s.uncertain {
		return true
	}
	col, row, err := ParseCellRef(cellRef)
	if err != nil {
		return true
	}
	dCol, dRow := col-s.anchorCol, row-s.anchorRow
	for _, candidate := range s.refs {
		c, r := candidate.col, candidate.row
		if !candidate.absCol {
			c += dCol
		}
		if !candidate.absRow {
			r += dRow
		}
		if c == targetCol && r == targetRow {
			return true
		}
	}
	return false
}
