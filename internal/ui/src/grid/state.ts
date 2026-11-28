/**
 * Strongly-typed workbook & grid runtime state, replacing the untyped `S` god-object.
 * Shared across `grid/`, `features/`, `editing/`, and `app/`.
 */
import type {
  ActivityHandle,
  AxisRange,
  AxisSizeRange,
  CellPoint,
  CellRef,
  SelectionArea,
} from '../contracts/common.ts';
import type {
  AutoFilterState,
  CellData,
  IndexStartResponse,
  IndexStatusResponse,
  PreviewResponse,
  RowData,
  RowViewState,
  SearchHit,
  SearchHistoryItem,
  SelectedCell,
  SheetLayoutResponse,
  SheetMeta,
  SheetViewportResponse,
  SourceFilterRule,
  SourceFilterSpec,
  SourceInfo,
  SourceSortKey,
  VisualStyle,
} from '../contracts/workbook.ts';
import type { RowVisibility } from './row-visibility.ts';
import type { RichTextCellMeta } from './richtext.ts';
import type { PresentationNoticeMeta } from './conditional.ts';
import type { MergedCellMeta } from './merges.ts';

export interface BorderSegment {
  readonly start: number;
  end: number;
  readonly style: string;
}

export interface BorderLayerElement extends HTMLSpanElement {
  _xlRuns?: readonly BorderSegment[];
}

export interface GridCellElement
  extends HTMLDivElement,
    RichTextCellMeta,
    PresentationNoticeMeta,
    MergedCellMeta {
  _xlModel?: CellData | null;
  _xlRowStyle?: number;
  _xlPaintKey?: string;
  _xlRichAllowed?: boolean;
  _xlRowPresentationNotice?: string;
  _xlNumericOriginal?: ChildNode | null;
  _xlNumericOriginalTitle?: string;
  _xlNumericSpan?: HTMLSpanElement | null;
  _xlOverflowSpan?: HTMLSpanElement | null;
  _xlborderRight?: string;
  _xlborderBottom?: string;
  _xlborderRightParts?: BorderLayerElement | null;
  _xlborderBottomParts?: BorderLayerElement | null;
  _xlCornerCap?: HTMLSpanElement | null;
  _xlCornerKey?: string;
  _xlInnerCorners?: HTMLSpanElement | null;
  _xlInnerCornerKey?: string;
  _xlOuterLeft?: string;
  _xlOuterTop?: string;
  _xlColumnLeft?: number;
  _xlColumnWidth?: number;
  _xlNextCol?: number;
  _xlRangeColor?: string;
  _xlInRange?: boolean;
  _xlHyperlinkVisited?: boolean;
}

export interface GridRowElement extends HTMLDivElement {
  _xlCells?: Map<number, GridCellElement>;
  _xlTop?: number;
  _xlHeight?: number;
  _xlRowNumber?: number;
  _xlVisualIndex?: number;
  _xlSheetEpoch?: number;
  _xlDecorVersion?: number;
  _xlBelow?: number;
  _xlLayoutVersion?: string;
  _xlGridKey?: string;
  _xlHeavy?: boolean;
}

export interface RenderMetrics {
  commits: number;
  reusedRows: number;
  createdRows: number;
  createdCells: number;
  lastMs: number;
}

export interface LastHighlightState {
  key: string;
  col: number;
  hadMultiCol: boolean;
  anchorCol: number;
  ref: string;
  hadRange: boolean;
}

export interface ViewportBounds {
  readonly context: string;
  readonly filtered: boolean;
  readonly filterId: string;
  readonly viewId: string;
  readonly excludeThroughRow: number;
  readonly displayStart: number;
  readonly displayEnd: number;
  readonly startCol: number;
  readonly endCol: number;
  readonly columns: readonly number[] | null;
  columnSet?: Set<number>;
}

export interface ViewportRequestPosition {
  readonly x: number;
  readonly y: number;
  readonly physical?: number;
  readonly w?: number;
  readonly h?: number;
  readonly pinned?: { readonly rows: number; readonly cols: number };
  readonly pw: number;
  readonly ph: number;
}

export interface ViewportMergeParts {
  readonly anchors: Map<number, number[]>;
  readonly parts: readonly ViewportRequest[];
}

export interface ViewportRequest {
  readonly context: string;
  readonly book?: string;
  readonly workbookKey?: string;
  readonly sheet?: string | null;
  readonly filtered: boolean;
  readonly filterId: string;
  readonly viewId: string;
  readonly excludeThroughRow: number;
  readonly displayStart: number;
  readonly displayEnd: number;
  readonly visibleStart: number;
  readonly visibleEnd: number;
  readonly visibleColStart: number;
  readonly visibleColEnd: number;
  readonly row: number;
  readonly offset: number;
  readonly limit: number;
  readonly startCol: number;
  readonly cols: number;
  readonly selectedCols: readonly number[] | null;
  readonly leadRow: number;
  readonly leadCol: number;
  readonly r1?: number;
  readonly r2?: number;
  readonly c1?: number;
  readonly c2?: number;
  readonly rowCount?: number;
  readonly colCount?: number;
  readonly rowOrder?: RowVisibility | null;
  readonly colOrder?: RowVisibility | null;
  readonly position?: ViewportRequestPosition;
  mountColStart?: number;
  visibleRanks?: readonly [number, number, number, number];
  prediction?: readonly [number, number, number, number];
  tileRows?: number;
  tileCols?: number;
  mergeParts?: ViewportMergeParts;
}

export interface ViewportEntry {
  readonly data: SheetViewportResponse;
  readonly request: ViewportRequest;
  readonly bounds: ViewportBounds;
  readonly bytes: number;
  rowMap?: Map<number, RowData>;
}

export interface HistoryEntry {
  readonly sheet: string;
  readonly ref: string;
}

export interface SheetPositionSnapshot {
  readonly top: number;
  readonly left: number;
  readonly ref: string;
  readonly visual: number;
  readonly frozenThrough: number;
}

export interface CachedSheetView {
  readonly bytes: number;
  readonly index: IndexStartResponse | IndexStatusResponse;
  readonly layout: SheetLayoutResponse;
  readonly freeze: { readonly rows: number; readonly cols: number };
  readonly entry: ViewportEntry;
  readonly frozen: Map<string, SheetViewportResponse>;
  readonly selected: SelectedCell | null;
  readonly filterId: string;
  readonly maxRow: number;
  readonly maxCol: number;
  readonly rows: Map<number, GridRowElement>;
  readonly presentation: string;
  readonly hasConditionalData: boolean;
  readonly conditionalStatsReady: boolean;
}

export interface TraceRefEntry {
  readonly sheet: string;
  readonly label?: string;
  readonly key?: string;
  readonly external?: boolean;
  readonly range?: boolean;
  readonly formula?: string;
  readonly start: CellRef;
  readonly end: CellRef;
}

export interface ColumnSelectionSpan {
  readonly start: number;
  readonly end: number;
}

export interface SearchCacheEntry {
  readonly matches: readonly SearchHit[];
  readonly truncated?: boolean;
}

export interface SearchSessionState {
  readonly key: string;
  readonly sheet: string;
  readonly input: string;
  readonly query: string;
  readonly mode: string;
  readonly scope: string;
  readonly full: boolean;
  matches: SearchHit[];
  status: string;
  truncated: boolean;
  summary: string;
  selected: number;
}

export interface PendingFilterSpec {
  readonly sheet: string;
  readonly rules: readonly SourceFilterRule[];
  readonly options: Readonly<Record<string, unknown>>;
  readonly restore?: SourceFilterSpec;
}

export interface WorkbookState {
  opened: boolean;
  opening: boolean;
  sheets: SheetMeta[];
  styles: VisualStyle[];
  showStyles: boolean;
  showFormulas: boolean;
  fontOverride: string;
  activeTab: number;
  sheet: string | null;
  fileName: string;
  workbookKey: string;
  book: string;
  source: SourceInfo | null;
  rowView: RowViewState | null;
  rowVisibility: RowVisibility | null;
  rowViewFailure: boolean;
  fileSize: number;
  sheetEpoch: number;
  viewportRequest: number;
  navigationTicket: number;
  navigationAbort: AbortController | null;
  viewportFrame: number;
  fallbackFrame: number;
  viewportActivity: ActivityHandle | null;
  viewportForce: boolean;
  viewportRetryTimer: number;
  viewportRetryAttempt: number;
  viewportData: ViewportEntry | null;
  renderedRows: Map<number, GridRowElement>;
  renderedScrollTop: number;
  renderedHeadersKey: string;
  decorVersion: number;
  renderMetrics: RenderMetrics;
  lastHighlight: LastHighlightState;
  index: IndexStartResponse | IndexStatusResponse | null;
  indexJob: string | null;
  indexReady: boolean;
  conditionalStatsJob: string | null;
  conditionalStatsTimer: number;
  conditionalStatsSeq: number;
  conditionalStatsState: string;
  tilesSkippedConditional: boolean;
  previewOnly: boolean;
  previewAbort: AbortController | null;
  previewData: PreviewResponse | null;
  previewFrame: number;
  maxRow: number;
  virtualScrollScale: number;
  maxCol: number;
  usedMaxRow: number;
  usedMaxCol: number;
  largeData: boolean;
  dimension: string;
  filtered: boolean;
  filterId: string;
  filterTotal: number;
  filtersBySheet: Map<string, string>;
  positionsBySheet: Map<string, SheetPositionSnapshot>;
  sheetViews: Map<string, CachedSheetView>;
  sheetViewsBytes: number;
  sheetLoadAbort: AbortController | null;
  sheetLoadingEpoch: number;
  sheetLayout: SheetLayoutResponse | null;
  reusableSheetRows: Map<number, GridRowElement> | null;
  reusableSheetData: SheetViewportResponse | null;
  filterTotalsBySheet: Map<string, number>;
  autoFiltersBySheet: Map<string, AutoFilterState | null>;
  sortKeysBySheet: Map<string, SourceSortKey[]>;
  sortHeaderBySheet: Map<string, number>;
  lastRulesBySheet: Map<string, SourceFilterRule[]>;
  autoFilter: AutoFilterState | null;
  textHeaderRevision: number;
  wholeRowSelected: boolean;
  filterJob: string | null;
  filterRequest: number;
  filterCommitAbort: AbortController | null;
  filterActivity: ActivityHandle | null;
  pendingFilterSpec: PendingFilterSpec | null;
  searchJob: string | null;
  searchActivity: ActivityHandle | null;
  searchHitMap: Map<string, Set<string>>;
  selected: SelectedCell | null;
  selectedVisualIndex: number;
  selectedFrozenThrough: number;
  cellMap: Map<string, CellData>;
  rowNumberByVisual: Map<number, number>;
  window: ViewportBounds | null;
  traceRefs: TraceRefEntry[];
  zoom: number;
  searchMatches: SearchHit[];
  searchMode: string;
  freeze: { rows: number; cols: number };
  frozenCellMap: Map<string, CellData>;
  searchCache: Map<string, SearchCacheEntry>;
  searchSession: SearchSessionState | null;
  searchSelectedIndex: number;
  searchRequestID: number;
  visibleSearchIndexes: number[];
  serverSearchHistory: SearchHistoryItem[];
  history: HistoryEntry[];
  historyIndex: number;
  historyNavigating: boolean;
  range: SelectionArea | null;
  multiRanges: SelectionArea[];
  rangeAnchor: CellPoint | null;
  rangeFrameRaf: number;
  visitedHyperlinks: Set<string>;
  linkErrorSheet: string;
  gridNavigation: object | null;
  columnWidths: Record<string, number>;
  rowSizes: Record<string, number>;
  sourceRowSizes: AxisSizeRange[];
  sourceColumnWidths: Record<string, number>;
  sourceDefaultRow: number;
  sourceDefaultCol: number;
  sourceGridLines: boolean;
  columnSourceRanges: AxisRange[];
  columnMode: string;
  columnOverrides: Record<string, boolean>;
  hiddenColumns: Set<number>;
  columnSelection: ColumnSelectionSpan | null;
  columnAnchor: number;
}

export function createInitialWorkbookState(maxRow: number, maxCol: number): WorkbookState {
  return {
    opened: false,
    opening: false,
    sheets: [],
    styles: [],
    showStyles: true,
    showFormulas: false,
    fontOverride: '',
    activeTab: 0,
    sheet: null,
    fileName: '',
    workbookKey: '',
    book: '',
    source: null,
    rowView: null,
    rowVisibility: null,
    rowViewFailure: false,
    fileSize: 0,
    sheetEpoch: 0,
    viewportRequest: 0,
    navigationTicket: 0,
    navigationAbort: null,
    viewportFrame: 0,
    fallbackFrame: 0,
    viewportActivity: null,
    viewportForce: false,
    viewportRetryTimer: 0,
    viewportRetryAttempt: 0,
    viewportData: null,
    renderedRows: new Map(),
    renderedScrollTop: 0,
    renderedHeadersKey: '',
    decorVersion: 0,
    renderMetrics: { commits: 0, reusedRows: 0, createdRows: 0, createdCells: 0, lastMs: 0 },
    lastHighlight: { key: '', col: 0, hadMultiCol: false, anchorCol: 0, ref: '', hadRange: false },
    index: null,
    indexJob: null,
    indexReady: false,
    conditionalStatsJob: null,
    conditionalStatsTimer: 0,
    conditionalStatsSeq: 0,
    conditionalStatsState: '',
    tilesSkippedConditional: false,
    previewOnly: false,
    previewAbort: null,
    previewData: null,
    previewFrame: 0,
    maxRow,
    virtualScrollScale: 1,
    maxCol,
    usedMaxRow: 1,
    usedMaxCol: 1,
    largeData: false,
    dimension: 'A1',
    filtered: false,
    filterId: '',
    filterTotal: 0,
    filtersBySheet: new Map(),
    positionsBySheet: new Map(),
    sheetViews: new Map(),
    sheetViewsBytes: 0,
    sheetLoadAbort: null,
    sheetLoadingEpoch: 0,
    sheetLayout: null,
    reusableSheetRows: null,
    reusableSheetData: null,
    filterTotalsBySheet: new Map(),
    autoFiltersBySheet: new Map(),
    sortKeysBySheet: new Map(),
    sortHeaderBySheet: new Map(),
    lastRulesBySheet: new Map(),
    autoFilter: null,
    textHeaderRevision: 0,
    wholeRowSelected: false,
    filterJob: null,
    filterRequest: 0,
    filterCommitAbort: null,
    filterActivity: null,
    pendingFilterSpec: null,
    searchJob: null,
    searchActivity: null,
    searchHitMap: new Map(),
    selected: null,
    selectedVisualIndex: 1,
    selectedFrozenThrough: 0,
    cellMap: new Map(),
    rowNumberByVisual: new Map(),
    window: null,
    traceRefs: [],
    zoom: 100,
    searchMatches: [],
    searchMode: 'value',
    freeze: { rows: 0, cols: 0 },
    frozenCellMap: new Map(),
    searchCache: new Map(),
    searchSession: null,
    searchSelectedIndex: -1,
    searchRequestID: 0,
    visibleSearchIndexes: [],
    serverSearchHistory: [],
    history: [],
    historyIndex: -1,
    historyNavigating: false,
    range: null,
    multiRanges: [],
    rangeAnchor: null,
    rangeFrameRaf: 0,
    visitedHyperlinks: new Set(),
    linkErrorSheet: '',
    gridNavigation: null,
    columnWidths: {},
    rowSizes: {},
    sourceRowSizes: [],
    sourceColumnWidths: {},
    sourceDefaultRow: 0,
    sourceDefaultCol: 0,
    sourceGridLines: true,
    columnSourceRanges: [],
    columnMode: 'original',
    columnOverrides: {},
    hiddenColumns: new Set(),
    columnSelection: null,
    columnAnchor: 0,
  };
}
