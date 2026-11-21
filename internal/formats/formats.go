// Package formats owns supported file extensions and resolved source semantics.
// It has no dependency on readers, servers, UI, or platform APIs.
package formats

import "strings"

const (
	ExcelRows          = 1_048_576
	ExcelColumns       = 16_384
	BIFFRows           = 65_536
	BIFFColumns        = 256
	BIFFSubstreamDepth = 64
	BIFFFormulaBytes   = 64 << 10
	BIFFFormulaWork    = 1_000_000
	ExternalRows       = 1_000_000_000
	// Large mode is based on indexed records, not a sparse sheet's last row.
	LargeDataRows        = 5_000_000
	MaxUploadBytes int64 = 1 << 40
	// Shared process budget for temporary query runs and retained row orders.
	MaxQueryTempBytes int64 = 8 << 30
	MaxQueryTempFiles       = 8192
	// Per-sort comparison keys and merge heads; excludes source/cache mappings.
	QuerySortMemoryBytes int64 = 64 << 20
	// Presentation spans are separate from the full source text and values.
	MaxCellRichRuns  = 4096
	MaxCellRichBytes = 4 << 20
	// Encoded presentation metadata in one resident read batch, excluding text.
	RichReadBytes           = 4 << 20
	MaxRichCacheBytes int64 = 2 << 30
	// Span nodes across the scrolling grid, and separately all frozen panes.
	// Text is retained when presentation must fall back to plain text.
	RichViewRuns = 8192
	// Bound expanded decimal display independently of the unchanged source text.
	NumericDisplayBytes = 64 << 10
	// Resident clipboard content; large ranges use streaming exports instead.
	ClipboardTextBytes = 4 << 20
	ClipboardHTMLBytes = 16 << 20
	ClipboardRows      = 250
	ClipboardColumns   = 50
	ClipboardCells     = 5000
	ODSContentBytes    = 512 << 20
	ODSLayoutBytes     = 16 << 20
	ODSRowTemplates    = 1_000_000
	ODSSheets          = 4096
	ODSMerges          = 100_000
	ClipboardTSVRows   = 2000
	ClipboardTSVCols   = 200
	ClipboardTSVCells  = 50000
	// Read-only name/table directory: metadata and references, never cell arrays.
	CatalogueEntries           = 8192
	CatalogueBytes             = 16 << 20
	CataloguePartBytes         = 4 << 20
	CatalogueDefinitionMax     = 64 << 10
	CatalogueAreas             = 64
	CatalogueDepth             = 16
	CatalogueResolveWork       = 1_000_000
	CatalogueXMLDepth          = 64
	CatalogueRootBytes         = 64 << 10
	CatalogueSheetCacheBytes   = 4 << 20
	CatalogueSheetCacheEntries = 32
	CatalogueRequestBytes      = 8 << 10
	CatalogueActiveTasks       = 2
	CatalogueTaskHistory       = 4
	CatalogueSnapshots         = 2
	CatalogueSnapshotBytes     = 32 << 20
	CataloguePageRows          = 50
	CatalogueSearchBytes       = 1024
	CatalogueLabelRunes        = 256
	// Read-only data-validation metadata; never expand covered source cells.
	DataRuleCount          = 8192
	DataRuleRanges         = 65536
	DataRuleTextBytes      = 64 << 10
	DataRuleMetadataBytes  = 16 << 20
	DataRuleBlockBytes     = 4 << 20
	DataRuleListItems      = 4096
	DataRuleXMLDepth       = 64
	DataRulePageRows       = 50
	DataRuleCacheBytes     = 32 << 20
	DataRuleCacheSheets    = 16
	DataRuleAttributeCount = 32
	DataRuleRootAttributes = 128
	DataRuleNoticeCount    = 32
	DataRuleQNameBytes     = 1024
	DataRuleBIFFRanges     = 432
	DataRuleBIFFBytes      = 64 << 10
	DataRuleBIFFSegments   = 256
	DataRuleContainers     = 8
	DataRuleRequestBytes   = 8 << 10
	DataRuleActiveTasks    = 2
	DataRuleTaskHistory    = 4
	DataRuleSnapshots      = 2
	DataRuleSnapshotBytes  = 64 << 20
	DataRuleLabelRunes     = 256
	// Saved print settings are opt-in metadata, separate from display geometry.
	PrintMetadataBytes               = 16 << 20
	PrintBlockBytes                  = 4 << 20
	PrintTextBytes                   = 64 << 10
	PrintDefinitions                 = 256
	PrintBreaks                      = 65536
	PrintBIFFBreaks                  = 1026
	PrintBIFFSegments                = 256
	PrintBIFFTextUnits               = 255
	PrintXMLDepth                    = 64
	PrintAttributes                  = 64
	PrintCacheBytes                  = 32 << 20
	PrintCacheSheets                 = 16
	PrintNoticeCount                 = 32
	PrintGeometryBytes               = 96 << 20
	PrintGeometryCacheBytes          = 128 << 20
	PrintGeometryCacheSheets         = 4
	PrintGeometryAxisRanges          = ExcelRows + ExcelColumns
	PrintGeometryColumnWork          = 8 << 20
	PrintGeometryPageRows            = 100
	PrintGeometryMeasureBytes        = 128
	PrintGeometryMeasureCache        = 4096
	PrintPaginationPages             = 10000
	PrintPaginationParts             = 100000
	PrintPaginationWork              = 8 << 20
	PrintPaginationAreas             = 256
	PrintPaginationFitPages          = 10000
	PrintPaginationBytes             = 32 << 20
	PrintPreviewRequestBytes         = 64 << 10
	PrintPreviewActiveTasks          = 2
	PrintPreviewTaskHistory          = 4
	PrintPreviewSnapshots            = 2
	PrintPreviewSnapshotBytes        = 256 << 20
	PrintPreviewPageRows             = 8192
	PrintPreviewPageColumns          = 2048
	PrintPreviewPageCells            = 20000
	PrintPreviewMergeLimit           = 100000
	PrintPreviewPageBytes            = 16 << 20
	PrintPreviewPageWork             = 2_000_000
	PrintPreviewDOMPixels            = 8_000_000
	PrintFirstPageMin                = -32768
	PrintFirstPageMax                = 4294967295
	PrintGeometryUnitsPerPoint int64 = 1_000_000_000
	// Source presentation metadata is indexed without expanding worksheet cells.
	ConditionalBlocks               = 8192
	ConditionalRules                = 8192
	ConditionalRanges               = 65536
	ConditionalFormulaBytes         = 4096
	ConditionalFormulaCount         = 8
	ConditionalParameterCount       = 32
	ConditionalNamespaces           = 32
	ConditionalNamespaceBytes       = 4096
	ConditionalNamespacePrefixBytes = 256
	ConditionalOperandBytes         = 64 << 10
	ConditionalEvalWork             = 2_000_000
	ConditionalEvalTextBytes        = 8 << 20
	ConditionalExplainRules         = 16
	ConditionalMetadataBytes        = 16 << 20
	ConditionalCacheBytes           = 32 << 20
	ConditionalReadBytes            = 4 << 20
	ConditionalReadCells            = 20000
	ConditionalDependencyReads      = 4096
	ConditionalDependencyXMLBytes   = 16 << 20
	ConditionalDependencyCacheBytes = 2 << 20
	ConditionalNoticeBytes          = 1024
	DifferentialStyles              = 8192
	SourceAxisRanges                = 100000
	// View-only visibility stays compressed across the source address domain.
	AxisOutlineGroups          = 100000
	AxisVisibilityOverrides    = 4096
	AxisVisibilityIntervals    = 262144
	AxisVisibilityWorkBytes    = 32 << 20
	RowProjectionMemoryBytes   = 1 << 20
	RowProjectionBuildBytes    = 32 << 20
	RowViewSnapshots           = 4
	RowViewTaskHistory         = 8
	RowViewActiveTasks         = 2
	RowViewSavedSheets         = 256
	RowViewGroupPage           = 200
	RowViewRequestBytes        = 1 << 20
	RowViewConfigBytes         = 8 << 20
	AnalysisSourceSnapshots    = 8
	AnalysisTreeTasks          = 8
	AnalysisTreeActiveTasks    = 2
	AnalysisTreePathBytes      = 1 << 20
	AnalysisTreeRequestBytes   = AnalysisTreePathBytes + (8 << 10)
	AnalysisTreeResultBytes    = 16 << 20
	AnalysisTreeRetainedBytes  = 32 << 20
	AnalysisTreeBrowserNodes   = 24
	AnalysisTreeBrowserBytes   = 24 << 20
	AnalysisTreePageRows       = 150
	SourceScopeBytes           = 1 << 20
	AnalysisSourceRequestBytes = SourceScopeBytes + (8 << 10)
	FilterRuleCount            = 32
	FilterRequestBytes         = 8 << 20
	SortKeyCount               = 5
	FilterSetValues            = 2500
	// XLSX snapshots keep source style IDs and bound additional palette work.
	XLSXStylePartBytes    = 64 << 20
	XLSXStyleWorkBytes    = 64 << 20
	XLSXStyleRecords      = 65536
	XLSXStyleXMLDepth     = 64
	XLSXStyleHeaderBytes  = 64 << 10
	XLSXStyleChildRecords = 65536
)

// ClipboardLimits is shared by the bounded source reader and the browser.
type ClipboardLimits struct {
	Rows      int `json:"rows"`
	Columns   int `json:"columns"`
	Cells     int `json:"cells"`
	TextBytes int `json:"textBytes"`
	HTMLBytes int `json:"htmlBytes"`
	TSVRows   int `json:"tsvRows"`
	TSVCols   int `json:"tsvColumns"`
	TSVCells  int `json:"tsvCells"`
}

func Clipboard() ClipboardLimits {
	return ClipboardLimits{
		Rows: ClipboardRows, Columns: ClipboardColumns, Cells: ClipboardCells,
		TextBytes: ClipboardTextBytes, HTMLBytes: ClipboardHTMLBytes,
		TSVRows: ClipboardTSVRows, TSVCols: ClipboardTSVCols, TSVCells: ClipboardTSVCells,
	}
}

// Source describes the read-only subset supplied by an adapter. HTML XLS has
// its own semantics even though content detection shares the .xls extension.
type Source struct {
	ID                  string `json:"id"`
	MaxRows             int    `json:"maxRows"`
	MaxColumns          int    `json:"maxColumns"`
	Styled              bool   `json:"styled"`
	TextAmounts         bool   `json:"textAmounts"`
	ConditionalMetadata bool   `json:"conditionalMetadata"`
	CatalogueMetadata   bool   `json:"catalogueMetadata"`
	DataRuleMetadata    bool   `json:"dataRuleMetadata"`
	NoteMetadata        bool   `json:"noteMetadata"`
	PrintMetadata       bool   `json:"printMetadata"`
	TextClustering      bool   `json:"textClustering"`
}

type entry struct {
	Source
	extensions []string
	archive    bool
}

var registry = []entry{
	{Source{ID: "xlsx", MaxRows: ExcelRows, MaxColumns: ExcelColumns, Styled: true, ConditionalMetadata: true, CatalogueMetadata: true, DataRuleMetadata: true, NoteMetadata: true, PrintMetadata: true, TextClustering: true}, []string{".xlsx", ".xlsm"}, false},
	{Source{ID: "xls", MaxRows: BIFFRows, MaxColumns: BIFFColumns, Styled: true, CatalogueMetadata: true, DataRuleMetadata: true, PrintMetadata: true, TextClustering: true}, []string{".xls"}, false},
	{Source{ID: "xls-html", MaxRows: ExternalRows, MaxColumns: ExcelColumns, Styled: true, TextAmounts: true, TextClustering: true}, nil, false},
	{Source{ID: "ods", MaxRows: ExternalRows, MaxColumns: ExcelColumns, TextClustering: true}, []string{".ods"}, false},
	{Source{ID: "csv", MaxRows: ExternalRows, MaxColumns: ExcelColumns, TextAmounts: true, TextClustering: true}, []string{".csv"}, false},
	{Source{ID: "tsv", MaxRows: ExternalRows, MaxColumns: ExcelColumns, TextAmounts: true, TextClustering: true}, []string{".tsv"}, false},
	{Source{ID: "txt", MaxRows: ExternalRows, MaxColumns: ExcelColumns, TextAmounts: true, TextClustering: true}, []string{".txt"}, false},
	{Source{ID: "sqlite", MaxRows: ExternalRows, MaxColumns: ExcelColumns, TextAmounts: true}, []string{".db", ".sqlite", ".sqlite3"}, false},
	{Source{ID: "archive"}, []string{".zip", ".tar", ".gz", ".tgz", ".7z", ".bz2", ".tbz", ".tbz2", ".xz", ".txz"}, true},
}

// Lookup accepts adapter IDs, plus the legacy xlsm ID used by clients.
func Lookup(id string) (Source, bool) {
	if id == "xlsm" {
		id = "xlsx"
	}
	for _, e := range registry {
		if e.ID == id && !e.archive {
			return e.Source, true
		}
	}
	return Source{}, false
}

func supported(ext string, archivesOnly, dataOnly bool) bool {
	ext = strings.ToLower(ext)
	for _, e := range registry {
		if (archivesOnly && !e.archive) || (dataOnly && e.archive) {
			continue
		}
		for _, candidate := range e.extensions {
			if ext == candidate {
				return true
			}
		}
	}
	return false
}

func Supported(ext string) bool        { return supported(ext, false, false) }
func DataExtension(ext string) bool    { return supported(ext, false, true) }
func ArchiveExtension(ext string) bool { return supported(ext, true, false) }

// Extensions returns a fresh, ordered list for upload, shell and dialog use.
func Extensions() []string {
	var out []string
	for _, e := range registry {
		out = append(out, e.extensions...)
	}
	return out
}

func Accept() string { return strings.Join(Extensions(), ",") }

// DialogFilter returns the paired, double-NUL-terminated Windows filter.
func DialogFilter() string {
	patterns := "*" + strings.Join(Extensions(), ";*")
	return "支持的数据文件 (" + patterns + ")\x00" + patterns + "\x00所有文件 (*.*)\x00*.*\x00\x00"
}

// Sources returns independent presentation metadata for the browser bootstrap.
func Sources() map[string]Source {
	out := make(map[string]Source)
	for _, e := range registry {
		if !e.archive {
			out[e.ID] = e.Source
		}
	}
	out["xlsm"] = out["xlsx"]
	return out
}
