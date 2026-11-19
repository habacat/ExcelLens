package editdoc

// At and Before use one-based coordinates of the current revision. Before
// identifies the original row or column before which a moved block is placed.
type Structure struct {
	Kind            string               `json:"kind"`
	Sheet           string               `json:"sheet"`
	Name            string               `json:"name,omitempty"`
	At              int                  `json:"at,omitempty"`
	Count           int                  `json:"count,omitempty"`
	Before          int                  `json:"before,omitempty"`
	Pivot           string               `json:"pivot,omitempty"`
	Rows            string               `json:"rows,omitempty"`
	Columns         string               `json:"columns,omitempty"`
	Data            string               `json:"data,omitempty"`
	Aggregate       string               `json:"aggregate,omitempty"`
	Filter          string               `json:"filter,omitempty"`
	FilterMode      string               `json:"filterMode,omitempty"`
	Item            string               `json:"item,omitempty"`
	CalculatedItem  *PivotCalculatedItem `json:"calculatedItem,omitempty"`
	ValueFilter     *PivotValueFilter    `json:"valueFilter,omitempty"`
	Axes            *PivotAxes           `json:"axes,omitempty"`
	Range           string               `json:"range,omitempty"`
	Formula         string               `json:"formula,omitempty"`
	ReplaceContents bool                 `json:"replaceContents,omitempty"`
}

type PivotCalculatedItem struct {
	Field   string `json:"field"`
	Name    string `json:"name"`
	Formula string `json:"formula"`
}

// PivotValueFilter applies the selected data measure to the members of one
// axis field. A second bound is used by between and not-between comparisons.
type PivotValueFilter struct {
	Field  string  `json:"field"`
	Kind   string  `json:"kind"`
	Value  float64 `json:"value"`
	Second float64 `json:"second,omitempty"`
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

func (d *Document) OriginalBase() Base {
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
