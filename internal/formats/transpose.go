package formats

// Transpose reads the shared bounded source selection; display pages never
// turn the workbook's source row capacity into a browser column count.
type TransposeLimits struct {
	Rows        int `json:"rows"`
	Columns     int `json:"columns"`
	Cells       int `json:"cells"`
	Bytes       int `json:"bytes"`
	PageColumns int `json:"pageColumns"`
}

func Transpose() TransposeLimits {
	return TransposeLimits{Rows: ClipboardRows, Columns: ClipboardColumns, Cells: ClipboardCells,
		Bytes: ClipboardTextBytes*6 + RichReadBytes*6 + ClipboardCells*512, PageColumns: 16}
}
