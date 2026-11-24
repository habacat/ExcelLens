package workbook

import "excellens/internal/searchresults"

import "sync"

type SheetMeta struct {
	Name     string `json:"name"`
	Path     string `json:"-"`
	State    string `json:"state,omitempty"`
	TabColor string `json:"tabColor,omitempty"`
}

type Workbook struct {
	ArchiveOrigin        *ArchiveOrigin
	Format               string
	SourceInfo           SourceInfo
	source               *externalSource
	Path                 string
	DisplayName          string
	Fingerprint          string
	CacheDir             string
	Sheets               []SheetMeta
	ActiveTab            int
	Date1904             bool
	Shared               []string
	richShared           *richTextStore
	richNotice           string
	NamedRanges          map[string]string
	LocalNamedRanges     map[string]map[string]string
	linkMu               sync.Mutex
	linkCache            map[string]*SheetHyperlinkIndex
	Styles               []StyleInfo
	VisualStyles         []VisualStyle
	ThemeColors          []string
	IndexedColors        []string
	differentialStyles   []DifferentialStyle
	differentialNotice   string
	conditionalMu        sync.Mutex
	conditionalCache     map[string]conditionalCacheEntry
	conditionalLoading   map[string]*conditionalLoad
	conditionalClock     uint64
	conditionalBytes     int64
	catalogueMu          sync.Mutex
	catalogueSheets      map[string]worksheetCatalogueEntry
	catalogueLoading     map[string]*worksheetCatalogueLoad
	catalogueClock       uint64
	catalogueBytes       int
	dataRuleMu           sync.Mutex
	dataRuleCache        map[string]dataRuleCacheEntry
	dataRuleLoading      map[string]*dataRuleLoad
	dataRuleClock        uint64
	dataRuleBytes        int64
	printMu              sync.Mutex
	printCache           map[string]sheetMetadataEntry[SourcePrintIndex]
	printLoading         map[string]*sheetMetadataLoad[SourcePrintIndex]
	printClock           uint64
	printBytes           int64
	printGeometryMu      sync.Mutex
	printGeometryCache   map[string]sheetMetadataEntry[SourcePrintGeometryIndex]
	printGeometryLoading map[string]*sheetMetadataLoad[SourcePrintGeometryIndex]
	printGeometryClock   uint64
	printGeometryBytes   int64
	NumFmts              map[int]string
	columnCache          columnCacheState
	imageMu              sync.Mutex
	imageCache           map[string][]EmbeddedImage
	indexMu              sync.RWMutex
	indexes              map[string]*SheetIndex
	sourceLayoutMu       sync.Mutex
	sourceLayouts        map[string]SourceLayout
	sourceLayoutLoading  map[string]*sourceLayoutLoad
	sourceDigitOnce      sync.Once
	sourceDigitWidth     float64
	normalFont           VisualStyle
}

type ArchiveStep struct {
	Member int    `json:"member"`
	Name   string `json:"name"`
}
type ArchiveOrigin struct {
	Path, Name, Member string
	MemberID           int
	Chain              []ArchiveStep
	Partial            bool
}

type StyleInfo struct {
	NumFmtID      int
	SectionColors [3]string
}

type SheetIndex struct {
	odsLayout *SourceLayout
	RowIndex
	SourceFormat   string // supplied by its owning adapter, not encoded in the index
	SheetPath      string
	XMLPath        string
	IndexPath      string
	Dimension      string
	MaxRow         int
	MaxCol         int
	XMLSize        int64
	SharedAnchors  map[string]SharedFormulaAnchor
	HyperlinkStart int64 // -2: legacy index (unknown), -1: absent, >=0: XML byte offset
}

type SharedFormulaAnchor struct {
	Ref     string
	Formula string
}

type RowOffset struct {
	Number int
	Start  int64
	End    int64
}

type RowData struct {
	Number             int        `json:"number"`
	Style              int        `json:"style,omitempty"`
	Cells              []CellData `json:"cells"`
	PresentationNotice string     `json:"presentationNotice,omitempty"`
}

// SourceRowReference identifies one source coordinate and its immutable index
// position. Position -1 represents an absent source row, retained as a blank.
// A present indexed row must never be represented as an absent row.
type SourceRowReference struct {
	Number   int
	Position int
}

type CellData struct {
	Currency   string `json:"currency,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	ShortDate  string `json:"shortDate,omitempty"`
	Ref        string `json:"ref"`
	Col        int    `json:"col"`
	Raw        string `json:"raw"`
	Display    string `json:"display"`
	Formula    string `json:"formula,omitempty"`
	HasFormula bool   `json:"hasFormula"`
	Type       string `json:"type,omitempty"`
	Style      int    `json:"style"`
	// A draft can combine the target's visual XF with another number format.
	DisplayNumFmt    *int                     `json:"displayNumFmt,omitempty"`
	ValueColor       string                   `json:"valueColor,omitempty"`
	CacheMissing     bool                     `json:"cacheMissing,omitempty"`
	SharedFormula    bool                     `json:"sharedFormula,omitempty"`
	Hyperlink        *CellHyperlink           `json:"hyperlink,omitempty"`
	RichText         *RichText                `json:"richText,omitempty"`
	Conditional      *ConditionalPresentation `json:"conditional,omitempty"`
	PresentationOnly bool                     `json:"presentationOnly,omitempty"`
}

type SearchResult = searchresults.Hit

type SearchProgress struct {
	Scanned int
	Total   int
	Matches []SearchResult
}
