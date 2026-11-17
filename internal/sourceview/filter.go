package sourceview

// FilterRule describes a read-only column predicate. Column is one-based.
type FilterRule struct {
	Column   int      `json:"column"`
	Operator string   `json:"operator"`
	Value    string   `json:"value"`
	Values   []string `json:"values,omitempty"`
}

// SortKey is one level of Excel-style stable, multi-column sorting.
// Keys are applied in order and require only a single row scan.
type SortKey struct {
	Column     int    `json:"column"`
	Descending bool   `json:"descending"`
	Mode       string `json:"mode,omitempty"` // source type, number, text, or natural text
}

// FilterSpec records only the rules that produced a source row order.
// It deliberately has no task IDs, row arrays or executable callbacks.
type FilterSpec struct {
	Rules      []FilterRule `json:"rules"`
	HeaderRow  int          `json:"headerRow"`
	SortColumn int          `json:"sortColumn,omitempty"`
	Descending bool         `json:"descending,omitempty"`
	SortKeys   []SortKey    `json:"sortKeys"`
}
