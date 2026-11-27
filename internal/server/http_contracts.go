package server

import "excellens/internal/workbook"

// These wire types back the status, open, recent, edit-result and recovery
// routes. tools/httpcontract derives the browser types from their JSON tags.
type OpenedWorkbookResponse struct {
	Version     string               `json:"version,omitempty"`
	Opened      bool                 `json:"opened"`
	WorkbookKey string               `json:"workbookKey"`
	Book        string               `json:"book"`
	FileName    string               `json:"fileName"`
	FileSize    int64                `json:"fileSize"`
	SheetCount  int                  `json:"sheetCount"`
	ActiveTab   int                  `json:"activeTab"`
	Sheets      []workbook.SheetMeta `json:"sheets"`
	Source      workbook.SourceInfo  `json:"source"`
	Archive     *ArchiveSummary      `json:"archive,omitempty"`
}

type ClosedWorkbookResponse struct {
	Version     string          `json:"version,omitempty"`
	Opened      bool            `json:"opened"`
	Error       string          `json:"error,omitempty"`
	ArchiveOnly bool            `json:"archiveOnly,omitempty"`
	Archive     *ArchiveSummary `json:"archive"`
}

type ArchiveSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
	Count  int    `json:"count"`
	Total  int    `json:"total"`
	Depth  int    `json:"depth"`
	Notice string `json:"notice"`
}

type StylesResponse struct {
	Styles         []workbook.VisualStyle `json:"styles"`
	RichTextNotice string                 `json:"richTextNotice"`
}

func openedWorkbook(wb *workbook.Workbook, name string, size int64) *OpenedWorkbookResponse {
	return &OpenedWorkbookResponse{Opened: true, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb),
		FileName: name, FileSize: size, SheetCount: len(wb.Sheets), ActiveTab: wb.ActiveTab, Sheets: wb.Sheets, Source: wb.SourceInfo}
}

// ViewportHeader carries the same source coordinates for compact and plain rows.
type ViewportHeader struct {
	Sheet                   string `json:"sheet"`
	Dimension               string `json:"dimension"`
	Offset                  int    `json:"offset"`
	Limit                   int    `json:"limit"`
	TotalRows               int    `json:"totalRows"`
	TotalRowsOriginal       int    `json:"totalRowsOriginal"`
	Filtered                bool   `json:"filtered"`
	FilterID                string `json:"filterId"`
	MaxRow                  int    `json:"maxRow"`
	MaxCol                  int    `json:"maxCol"`
	StartCol                int    `json:"startCol"`
	ColCount                int    `json:"colCount"`
	Columns                 []int  `json:"columns"`
	HyperlinkError          string `json:"hyperlinkError"`
	ConditionalStatsPending bool   `json:"conditionalStatsPending,omitempty"`
	ViewID                  string `json:"viewId,omitempty"`
	PinnedThroughRow        int    `json:"pinnedThroughRow,omitempty"`
	Compact                 int    `json:"compact,omitempty"`
}

type viewportResponse[R any] struct {
	ViewportHeader
	Rows []R `json:"rows"`
}

type PreviewHeader struct {
	Available bool   `json:"available"`
	Partial   bool   `json:"partial"`
	Sheet     string `json:"sheet"`
	StartCol  int    `json:"startCol"`
	ColCount  int    `json:"colCount"`
	MaxRow    int    `json:"maxRow"`
	MaxCol    int    `json:"maxCol"`
	TotalRows int    `json:"totalRows"`
	Compact   int    `json:"compact,omitempty"`
}

type previewResponse[R any] struct {
	PreviewHeader
	Rows []R `json:"rows"`
}
