package editdoc

// At and Before use one-based coordinates of the current revision. Before
// identifies the original row or column before which a moved block is placed.
type Structure struct {
	Kind           string               `json:"kind"`
	Sheet          string               `json:"sheet"`
	Name           string               `json:"name,omitempty"`
	At             int                  `json:"at,omitempty"`
	Count          int                  `json:"count,omitempty"`
	Before         int                  `json:"before,omitempty"`
	Pivot          string               `json:"pivot,omitempty"`
	Rows           string               `json:"rows,omitempty"`
	Columns        string               `json:"columns,omitempty"`
	Data           string               `json:"data,omitempty"`
	Aggregate      string               `json:"aggregate,omitempty"`
	Filter         string               `json:"filter,omitempty"`
	FilterMode     string               `json:"filterMode,omitempty"`
	Item           string               `json:"item,omitempty"`
	CalculatedItem *PivotCalculatedItem `json:"calculatedItem,omitempty"`
	ValueFilter    *PivotValueFilter    `json:"valueFilter,omitempty"`
	Axes           *PivotAxes           `json:"axes,omitempty"`

	Measures    []PivotMeasure    `json:"measures,omitempty"`
	ValuesAxis  *PivotValuesAxis  `json:"valuesAxis,omitempty"`
	LabelFilter *PivotLabelFilter `json:"labelFilter,omitempty"`
	DateFilter  *PivotDateFilter  `json:"dateFilter,omitempty"`
	DateGroup   *PivotDateGroup   `json:"dateGroup,omitempty"`

	Paste            *PortableRange `json:"paste,omitempty"`
	TargetRows       []int          `json:"targetRows,omitempty"`
	PasteVisibleOnly bool           `json:"pasteVisibleOnly,omitempty"`

	DestinationSheet string `json:"destinationSheet,omitempty"`
	Destination      string `json:"destination,omitempty"`
	SourceRevision   uint64 `json:"sourceRevision,string,omitempty"`

	Range           string `json:"range,omitempty"`
	Formula         string `json:"formula,omitempty"`
	ReplaceContents bool   `json:"replaceContents,omitempty"`
}

type PivotCalculatedItem struct {
	Field   string `json:"field"`
	Name    string `json:"name"`
	Formula string `json:"formula"`
}

// PivotValueFilter applies the selected data measure to the members of one
// axis field. A second bound is used by between and not-between comparisons.
type PivotValueFilter struct {
	Measure int     `json:"measure,omitempty"`
	Field   string  `json:"field"`
	Kind    string  `json:"kind"`
	Value   float64 `json:"value"`
	Second  float64 `json:"second,omitempty"`
}

type PivotMeasure struct {
	Field     string `json:"field"`
	Aggregate string `json:"aggregate"`
	Caption   string `json:"caption,omitempty"`
	// Index in the bound revision's dataFields. -1 denotes an added measure.
	SourceIndex *int `json:"sourceIndex,omitempty"`
}
type PivotValuesAxis struct {
	Axis     string `json:"axis"`
	Position int    `json:"position"`
}
type PivotLabelFilter struct {
	Field  string `json:"field"`
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Second string `json:"second,omitempty"`
}
type PivotDateGroup struct {
	Start    string   `json:"start,omitempty"`
	End      string   `json:"end,omitempty"`
	Interval int      `json:"interval,omitempty"`
	Field    string   `json:"field"`
	Units    []string `json:"units"`
}

// Start and End are local calendar dates or times, independent of the
// workbook's 1900/1904 serial system. Relative conditions persist by name.
type PivotDateFilter struct {
	Field    string `json:"field"`
	Kind     string `json:"kind"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	WholeDay bool   `json:"wholeDay,omitempty"`
}

// Axis order is significant. A page with no selected items displays all items.
// Keep the value type so a numeric identifier and the same text can be selected separately.
type PivotAxes struct {
	Rows    []string    `json:"rows"`
	Columns []string    `json:"columns"`
	Pages   []PivotPage `json:"pages"`
}

type PivotPage struct {
	Field string      `json:"field"`
	Items []PivotItem `json:"items,omitempty"`
}

type PivotItem struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// A structural snapshot incorporates the preceding sparse edits. Switching
// back restores their original addresses and their earlier undo history.
type workspace struct {
	base      Base
	values    map[Address]Value
	templates map[Address]string
	rules     map[Area]Rule
}

func (d *Document) switchWorkspace(next Base, forward bool) {
	if forward {
		d.workspaces = append(d.workspaces, workspace{d.Base, d.value, d.templates, d.rules})
		d.Base = next
		d.value = make(map[Address]Value)
		d.templates = make(map[Address]string)
		d.rules = make(map[Area]Rule)
		return
	}
	i := len(d.workspaces) - 1
	old := d.workspaces[i]
	d.workspaces[i] = workspace{}
	d.workspaces = d.workspaces[:i]
	d.Base, d.value, d.templates, d.rules = old.base, old.values, old.templates, old.rules
}

// TransferSource exposes the immutable input to a structural revision still on
// the current undo branch. Repeated pastes can read the original rectangle
// even after an overlapping paste replaced its source cells.
func (d *Document) TransferSource(revision uint64) (*Document, bool) {
	if revision == d.Revision {
		return d, true
	}
	step := 0
	for _, tx := range d.undo {
		if tx.NextBase == nil {
			continue
		}
		if d.receipts[tx.Request].revision-1 == revision {
			w := d.workspaces[step]
			source := &Document{Entry: d.Entry, value: w.values, templates: w.templates, rules: w.rules}
			source.Base, source.Revision, source.Rules = w.base, revision, len(w.rules)
			return source, true
		}
		step++
	}
	return nil, false
}

func (d *Document) OriginalBase() Base {
	if d.origin.Source.Path != "" {
		return d.origin
	}
	if len(d.workspaces) == 0 {
		return d.Base
	}
	return d.workspaces[0].base
}

// HistoryBase identifies the immutable file which an undo/redo would expose.
func (d *Document) HistoryBase(action string) *Base {
	if action == "undo" && len(d.undo) > 0 && d.undo[len(d.undo)-1].NextBase != nil {
		return &d.workspaces[len(d.workspaces)-1].base
	}
	if action == "redo" && len(d.redo) > 0 {
		return d.redo[len(d.redo)-1].NextBase
	}
	return nil
}
