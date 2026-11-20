package editdoc

// PortableRange contains only the copied rectangle. Source style indices have
// meaning only inside their own workbook, so cells refer to this style table.
type PortableRange struct {
	Origin     string          `json:"origin,omitempty"`
	Start      Address         `json:"start"`
	Rows       int             `json:"rows"`
	Cols       int             `json:"cols"`
	Cells      []PortableCell  `json:"cells"`
	Styles     []PortableStyle `json:"styles"`
	Merges     []Area          `json:"merges,omitempty"`
	SourceRows []int           `json:"sourceRows,omitempty"`
}

type PortableCell struct {
	Value Value `json:"value"`
	Style int   `json:"style,omitempty"`
}

type PortableStyle struct {
	Builtin    int    `json:"builtin,omitempty"`
	Format     string `json:"format,omitempty"`
	Font       string `json:"font,omitempty"`
	Fill       string `json:"fill,omitempty"`
	Border     string `json:"border,omitempty"`
	Alignment  string `json:"alignment,omitempty"`
	Protection string `json:"protection,omitempty"`
}
