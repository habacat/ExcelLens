package formats

// Browser limits use the same source and physical-page contract as the server.
// DOMPixels bounds actual CSS layout before any screen-zoom transform.
type PrintingLimits struct {
	Pages         int   `json:"pages"`
	Rows          int   `json:"rows"`
	Columns       int   `json:"columns"`
	Cells         int   `json:"cells"`
	Areas         int   `json:"areas"`
	Parts         int   `json:"parts"`
	Bytes         int   `json:"bytes"`
	RequestBytes  int   `json:"requestBytes"`
	Notices       int   `json:"notices"`
	DOMPixels     int   `json:"domPixels"`
	UnitsPerPoint int64 `json:"unitsPerPoint"`
	FirstPageMin  int64 `json:"firstPageMin"`
	FirstPageMax  int64 `json:"firstPageMax"`
}

func Printing() PrintingLimits {
	return PrintingLimits{PrintPaginationPages, PrintPreviewPageRows, PrintPreviewPageColumns, PrintPreviewPageCells, PrintPaginationAreas, PrintPaginationParts, PrintPreviewPageBytes, PrintPreviewRequestBytes, PrintNoticeCount, PrintPreviewDOMPixels, PrintGeometryUnitsPerPoint, PrintFirstPageMin, PrintFirstPageMax}
}
