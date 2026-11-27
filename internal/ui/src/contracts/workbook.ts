/**
 * Core Workbook, Styles, Conditional Presentation, Compact Wire Protocol,
 * Layout, Viewport, Filter/Sort, Row-View, and Search API contracts.
 */
import type {
  AxisRange,
  AxisSizeRange,
  MergeRange,
} from './common.ts';
import type {
  AxisOutlineOverride,
  AxisViewSettings,
  SourceFilterRule,
  SourceFilterSpec,
  SourceSortKey,
} from './analysis.ts';

export interface SheetMeta {
  readonly name: string;
  readonly state?: string;
  readonly tabColor?: string;
  readonly sourceName?: string;
}

export interface ArchiveStep {
  readonly member: number;
  readonly name: string;
}

/** @httpResponse TextTableHeader */
export interface TextTableHeader {
  readonly row: number;
  readonly columns: readonly string[];
  readonly detected: boolean;
}

export interface DatabaseSnapshotInfo {
  readonly retained: boolean;
  readonly legacy: boolean;
}

export interface SourceInfo {
  readonly tableHeader?: TextTableHeader;
  readonly archiveName?: string;
  readonly archiveMember?: string;
  readonly archiveNotice?: string;
  readonly incomplete?: boolean;
  readonly format: string;
  readonly encoding?: string;
  readonly delimiter?: string;
  readonly notice?: string;
  readonly presentationNotice?: string;
  readonly databaseSnapshot?: DatabaseSnapshotInfo;
  readonly readOnly?: boolean;
}

export interface ArchiveSummary {
  readonly id?: string;
  readonly format?: string;
  readonly count?: number;
  readonly depth?: number;
  readonly name?: string;
  readonly path?: string;
  readonly entries?: number;
  readonly total?: number;
  readonly notice?: string;
  readonly chain?: readonly ArchiveStep[];
}

export interface VisualStyle {
  readonly shortDate?: string;
  readonly numFmtId?: number;
  readonly color?: string;
  readonly background?: string;
  readonly fontName?: string;
  readonly fontSize?: number;
  readonly bold?: boolean;
  readonly italic?: boolean;
  readonly underline?: boolean;
  readonly underlineStyle?: string;
  readonly fontVertical?: string;
  readonly strike?: boolean;
  readonly horizontal?: string;
  readonly vertical?: string;
  readonly wrap?: boolean;
  readonly rotation?: number;
  readonly borderTop?: string;
  readonly borderBottom?: string;
  readonly borderLeft?: string;
  readonly borderRight?: string;
}

export interface RichFont {
  name?: string;
  size?: number;
  color?: string;
  autoColor?: boolean;
  bold?: boolean;
  italic?: boolean;
  strike?: boolean;
  underline?: string;
  vertical?: string;
}

export interface RichTextRun {
  readonly end: number;
  readonly font: RichFont;
}

export interface RichText {
  readonly runs?: readonly RichTextRun[];
  readonly notice?: string;
}

export interface DifferentialBorderSide {
  readonly style?: string;
  readonly color?: string;
  readonly autoColor?: boolean;
}

export interface DifferentialBorders {
  readonly top?: DifferentialBorderSide;
  readonly bottom?: DifferentialBorderSide;
  readonly left?: DifferentialBorderSide;
  readonly right?: DifferentialBorderSide;
}

export interface DifferentialNumberFormat {
  readonly id: number;
  readonly code: string;
}

export interface DifferentialStyle {
  readonly font: RichFont;
  readonly background?: string;
  readonly border?: DifferentialBorders;
  readonly numberFormat?: DifferentialNumberFormat;
  readonly horizontal?: string;
  readonly vertical?: string;
  readonly wrap?: boolean;
  readonly rotation?: number;
  readonly notice?: string;
}

export interface ConditionalPresentation {
  readonly style?: DifferentialStyle;
  readonly matched?: number;
  readonly priorities?: readonly number[];
  readonly notice?: string;
  readonly pending?: boolean;
  readonly visual?: VisualStyle;
  readonly display?: string;
  readonly valueColor?: string;
}

export interface CellHyperlink {
  readonly kind: 'internal' | 'external';
  readonly sheet?: string;
  readonly ref?: string;
  readonly url?: string;
  readonly tooltip?: string;
  readonly display?: string;
}

export interface CellData {
  currency?: string;
  sourceType?: string;
  shortDate?: string;
  ref: string;
  col: number;
  raw: string;
  display: string;
  formula?: string;
  hasFormula: boolean;
  type?: string;
  style?: number;
  displayNumFmt?: number;
  valueColor?: string;
  cacheMissing?: boolean;
  sharedFormula?: boolean;
  arrayFormula?: MergeRange;
  arrayFormulaMember?: boolean;
  hyperlink?: CellHyperlink;
  richText?: RichText;
  conditional?: ConditionalPresentation;
  presentationOnly?: boolean;
  present?: boolean;
}

export interface RowData {
  readonly number: number;
  style?: number;
  cells: CellData[];
  presentationNotice?: string;
}

export interface WireCell {
  readonly cu?: string;
  readonly st?: string;
  readonly c: number;
  readonly v: string;
  readonly d?: string;
  readonly r?: string;
  readonly t?: string;
  readonly s?: number;
  readonly f?: string;
  readonly k?: number;
  readonly o?: string;
  readonly h?: CellHyperlink;
  readonly x?: RichText;
  readonly q?: ConditionalPresentation;
}

export interface WireRow {
  readonly n: number;
  readonly s?: number;
  readonly c: readonly WireCell[];
  readonly p?: string;
}

export interface SourceAxisRange {
  readonly min: number;
  readonly max: number;
  readonly hidden?: boolean;
  readonly hiddenSet?: boolean;
  readonly level?: number;
  readonly collapsed?: boolean;
  readonly zeroSize?: boolean;
  readonly positiveSize?: boolean;
}

export interface AxisOutlineGroup {
  readonly min: number;
  readonly max: number;
  readonly level: number;
  readonly summary?: number;
  readonly parent?: number;
  readonly sourceCollapsed?: boolean;
  readonly collapsed?: boolean;
  readonly visible: number;
}

export interface ImageMarker {
  readonly col: number;
  readonly row: number;
  readonly colOff: number;
  readonly rowOff: number;
}

export interface ImageCrop {
  readonly left?: number;
  readonly top?: number;
  readonly right?: number;
  readonly bottom?: number;
}

export interface ImageDTO {
  readonly id: number;
  readonly name?: string;
  readonly description?: string;
  readonly kind: string;
  readonly editAs?: string;
  readonly hasTransform?: boolean;
  readonly from: ImageMarker;
  readonly to?: ImageMarker;
  readonly width?: number;
  readonly height?: number;
  readonly x?: number;
  readonly y?: number;
  readonly crop?: ImageCrop;
  readonly rotation?: number;
  readonly flipH?: boolean;
  readonly flipV?: boolean;
  readonly src: string;
}

export interface SourceNote {
  readonly ref: string;
  readonly author: string;
  readonly text: string;
}

export interface WorkbookStatusResponse {
  readonly version?: string;
  readonly opened: boolean;
  readonly error?: string;
  readonly workbookKey?: string;
  readonly book?: string;
  readonly fileName?: string;
  readonly fileSize?: number;
  readonly sheetCount?: number;
  readonly activeTab?: number;
  readonly sheets?: readonly SheetMeta[];
  readonly source?: SourceInfo;
  readonly archive?: ArchiveSummary | null;
  readonly archiveOnly?: boolean;
}

export type { StylesWire as StylesResponse } from './http-wire.generated.ts';

export interface SheetViewportResponse {
  readonly hyperlinkError?: string;
  readonly sheet: string;
  readonly dimension?: string;
  readonly offset: number;
  readonly limit: number;
  readonly totalRows: number;
  readonly totalRowsOriginal?: number;
  readonly filtered: boolean;
  readonly filterId?: string;
  readonly maxRow: number;
  readonly maxCol: number;
  readonly startCol: number;
  readonly colCount: number;
  readonly columns?: readonly number[] | null;
  rows: RowData[];
  anchors?: Map<number, number[]>;
  readonly conditionalStatsPending?: boolean;
  readonly viewId?: string;
  readonly pinnedThroughRow?: number;
  readonly compact?: number;
}

export interface PreviewResponse {
  readonly available: boolean;
  readonly partial?: boolean;
  readonly sheet?: string;
  readonly startCol?: number;
  readonly colCount?: number;
  readonly rows?: RowData[];
  readonly maxRow?: number;
  readonly maxCol?: number;
  readonly totalRows?: number;
  readonly compact?: number;
}

/** @httpResponse CellResponse */
export interface CellResponse {
  readonly sheet: string;
  readonly ref: string;
  readonly cell: CellData;
  readonly presentationNotice?: string;
}

export interface ClipboardRow {
  readonly row: number;
  readonly style?: number;
  cells: CellData[];
  readonly presentationNotice?: string;
}

/** @httpResponse ClipboardPayload */
export interface ClipboardPayload {
  readonly portable?: unknown;
  readonly portableNotice?: string;
  readonly workbookKey?: string;
  readonly book?: string;
  readonly sheet?: string;
  readonly startCol: number;
  readonly endCol: number;
  readonly filtered?: boolean;
  readonly rows: ClipboardRow[];
  readonly header?: ClipboardRow;
}

/** @httpResponse SheetLayoutResponse */
export interface SheetLayoutResponse {
  readonly rows?: Readonly<Record<string, number>>;
  readonly cols?: Readonly<Record<string, number>>;
  readonly font?: string;
  readonly columnMode?: string;
  readonly columnVisibility?: Readonly<Record<string, boolean>>;
  readonly sourceHidden?: readonly AxisRange[];
  readonly sourceMerges?: readonly MergeRange[];
  readonly sourceRows?: readonly AxisSizeRange[];
  readonly sourceCols?: Readonly<Record<string, number>>;
  readonly sourceGridLines?: boolean;
  readonly sourceDefaultRow?: number;
  readonly sourceDefaultCol?: number;
  readonly sourceRowStates?: readonly SourceAxisRange[];
  readonly sourceColStates?: readonly SourceAxisRange[];
  readonly sourceZeroHeight?: boolean;
  readonly sourceDefaultRowZeroSize?: boolean;
  readonly sourceDefaultColZeroSize?: boolean;
  readonly sourceSummaryBelow?: boolean;
  readonly sourceSummaryRight?: boolean;
  readonly sourceStateNotice?: string;
}

/** @httpResponse FreezeViewResponse */
export interface FreezeViewResponse {
  readonly sheet: string;
  readonly rows: number;
  readonly cols: number;
  readonly source: 'saved' | 'original';
}

/** @httpResponse IndexStartResponse */
export interface IndexStartResponse {
  readonly ready: boolean;
  readonly jobId: string;
  readonly sheet?: string;
  readonly dimension?: string;
  readonly usedDimension?: string;
  readonly totalRows?: number;
  readonly maxRow?: number;
  readonly maxCol?: number;
}

/** @httpResponse IndexStatusResponse */
export interface IndexStatusResponse {
  readonly id: string;
  readonly sheet: string;
  readonly status: string;
  readonly stage?: string;
  readonly progress?: number;
  readonly doneBytes: number;
  readonly totalBytes: number;
  readonly error?: string;
  readonly statisticsProgress?: { readonly done: number; readonly total: number };
  readonly dimension?: string;
  readonly usedDimension?: string;
  readonly totalRows?: number;
  readonly maxRow?: number;
  readonly maxCol?: number;
}

/** @httpResponse ConditionalStatsStartResponse */
export interface ConditionalStatsStartResponse {
  readonly sheet: string;
  readonly needed: boolean;
  readonly ready: boolean;
  readonly status: string;
  readonly jobId?: string;
  readonly damaged?: boolean;
  readonly error?: string;
}

/** @httpResponse ConditionalStatsCancelResponse */
export interface ConditionalStatsCancelResponse {
  readonly cancelled: boolean;
}

/** @httpResponse ConditionalStatsStatusResponse */
export interface ConditionalStatsStatusResponse {
  readonly id: string;
  readonly sheet: string;
  readonly status: string;
  readonly stage: string;
  readonly progress: number;
  readonly doneRows: number;
  readonly totalRows: number;
  readonly damaged: boolean;
  readonly error: string;
}

export interface DistinctValue {
  readonly value: string;
  readonly count: number;
}

/** @httpResponse FilterDistinctResponse */
export interface FilterDistinctResponse {
  readonly values?: readonly DistinctValue[] | null;
  readonly truncated: boolean;
  readonly cached: boolean;
}

/** @httpResponse FilterStartResponse */
export interface FilterStartResponse {
  readonly jobId: string;
  readonly taskId?: string;
  readonly recoverable?: boolean;
  readonly operation?: string;
  readonly filter?: SourceFilterSpec;
}

/** @httpResponse FilterStatusResponse */
export interface FilterStatusResponse {
  readonly id: string;
  readonly sheet: string;
  readonly status: string;
  readonly stage?: string;
  readonly progress?: number;
  readonly scanned?: number;
  readonly durationMs?: number;
  readonly total?: number;
  readonly error?: string;
  readonly filterId?: string;
  readonly taskId?: string;
  readonly matchedRows?: number;
  readonly filter?: SourceFilterSpec;
}

/** @httpResponse FilterLocateResponse */
export interface FilterLocateResponse {
  readonly found: boolean;
  readonly visualIndex: number;
  readonly row: number;
  readonly viewId?: string;
  readonly hidden?: boolean;
  readonly pinned?: boolean;
}

/** @httpResponse NavigateResponse */
export interface NavigateResponse {
  readonly sheet: string;
  readonly ref: string;
  readonly row: number;
  readonly col: number;
}

export interface RowViewChange {
  readonly action: string;
  readonly first?: number;
  readonly last?: number;
  readonly hidden?: boolean;
  readonly group?: AxisOutlineOverride;
  readonly depth?: number;
}

/** @httpResponse RowViewState */
export interface RowViewState {
  readonly id: string;
  readonly sheet: string;
  readonly filterId: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly revision: number;
  readonly settings: AxisViewSettings;
  readonly totalRows: number;
  readonly maxRow: number;
  readonly digest: string;
  readonly notice: string;
  readonly settingsWarning: string;
  readonly matchedPinnedRows?: readonly number[] | null;
  readonly hiddenRanges?: readonly AxisRange[] | null;
  readonly groupCount: number;
}

/** @httpResponse RowViewStatusResponse */
export interface RowViewStatusResponse {
  readonly id: string;
  readonly status: string;
  readonly scanned: number;
  readonly total: number;
  readonly error: string;
  readonly viewId: string;
}

/** @httpResponse RowViewGroupsResponse */
export interface RowViewGroupsResponse {
  readonly id: string;
  readonly groups?: readonly AxisOutlineGroup[] | null;
  readonly total: number;
}

export interface SearchHit {
  readonly sheet?: string;
  readonly ref: string;
  readonly row: number;
  readonly col: number;
  readonly formula?: string;
  readonly value?: string;
}

export interface SearchPageResponse {
  readonly matches: readonly SearchHit[];
  readonly next: number;
  readonly more: boolean;
  readonly previewClipped: boolean;
}

export interface SearchHistoryItem {
  readonly sheet: string;
  readonly query: string;
  readonly mode: string;
  readonly scope: string;
  readonly hits: number;
  readonly truncated: boolean;
  readonly updated: string;
}

export interface SearchStatusResponse {
  readonly id: string;
  readonly sheet: string;
  readonly query: string;
  readonly mode: string;
  readonly scope: string;
  readonly status: string;
  readonly stage: string;
  readonly progress: number;
  readonly scanned: number;
  readonly durationMs: number;
  readonly total: number;
  readonly sheetsDone: number;
  readonly sheetsTotal: number;
  readonly matches: readonly SearchHit[];
  readonly truncated: boolean;
  readonly error: string;
  readonly taskId: string;
  readonly full?: boolean;
  readonly hitCount?: number;
  readonly downloadUrl?: string;
}

export interface AutoFilterRule {
  readonly column: number;
  readonly operator: string;
  readonly value?: string;
  readonly values?: readonly string[];
}

export interface AutoFilterState {
  row: number;
  startCol: number;
  endCol: number;
  names?: readonly string[];
  table?: boolean;
  detected?: boolean;
  rules: Map<number, AutoFilterRule>;
  sortColumn: number;
  descending: boolean;
}

export interface SelectedCell extends CellData {
  readonly row: number;
  readonly sheet: string;
  readonly crosshair?: { readonly row: number; readonly col: number } | null;
}

export type { SourceFilterRule, SourceFilterSpec, SourceSortKey };
