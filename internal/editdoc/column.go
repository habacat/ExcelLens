package editdoc

import (
	"maps"

	"excellens/internal/formats"
)

// A calculated-column template is addressed by its first data cell, even
// when that particular cell has a literal exception.
type ColumnFormula struct {
	Address
	Original, Formula string
}
type PreparedChanges struct {
	NextBase  *Base // A verified array spill owns coordinates outside the submitted cells.
	Changes   []Change
	Base      map[Address]Value
	Templates []ColumnFormula
}
type templatePatch struct {
	Address
	// nil inherits the base definition; an explicit empty string removes it.
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
}

func (d *Document) ColumnFormulas() map[Address]string { return maps.Clone(d.templates) }
func (d *Document) ColumnFormula(a Address, base string) string {
	if v, ok := d.templates[a]; ok {
		return v
	}
	return base
}
func (d *Document) validTemplates(patches []templatePatch) bool {
	if len(patches) > formats.EditBatchCells {
		return false
	}
	seen := map[Address]bool{}
	for _, p := range patches {
		previous, exists := d.templates[p.Address]
		if seen[p.Address] || !validAddress(p.Address) || exists != (p.Before != nil) || exists && *p.Before != previous {
			return false
		}
		if p.After == nil && !exists || p.After != nil && (exists && *p.After == previous || *p.After != "" && !(Value{Kind: "formula", Raw: *p.After}).Valid()) {
			return false
		}
		seen[p.Address] = true
	}
	return true
}
