/**
 * Feature domain API contracts: Export, Bookmarks, Named Views, Catalogue,
 * Data Rules, Printing, Record Card, Cell Explanation, Profile, Selection Stats/Quick,
 * Inspect, Autofit, Reconcile, Relations, Archives, Online Database, Database Snapshots,
 * Storage, Task Checkpoints, Background Tasks, and Calculator.
 */
import type { MergeRange } from './common.ts';
import type {
  AxisViewSettings,
  NumericDistribution,
  QuickSegment,
  QuickSummary,
  SourceFilterSpec,
} from './analysis.ts';
import type {
  ArchiveStep,
  ArchiveSummary,
  CellData,
  RowData,
  SearchHit,
  SheetMeta,
  VisualStyle,
} from './workbook.ts';

export interface RecentEntry {
  readonly archivePath?: string;
  readonly archiveName?: string;
  readonly archiveEntry?: number;
  readonly archiveChain?: readonly ArchiveStep[];
  readonly archiveMember?: string;
  readonly archivePartial?: boolean;
  readonly database?: boolean;
  readonly id: string;
  readonly name: string;
  readonly path: string;
  readonly fingerprint: string;
  readonly fileSize: number;
  readonly sheetCount: number;
  readonly openedAt: string;
  readonly pinned: boolean;
  readonly available: boolean;
}

export interface ExportStatusResponse {
  readonly id: string;
  readonly status: string;
  readonly stage: string;
  readonly error: string;
  readonly progress: number;
  readonly doneRows: number;
  readonly totalRows: number;
  readonly size: number;
  readonly fileName: string;
  readonly elapsedMs: number;
  readonly taskId?: string;
  readonly recoverable?: boolean;
  readonly format?: string;
  readonly auditReport?: boolean;
  readonly downloadUrl?: string;
}

export interface Bookmark {
  readonly id: string;
  readonly sheet: string;
  readonly ref: string;
  readonly tag: string;
  readonly note: string;
  readonly value?: string;
  readonly formula?: string;
  readonly created: string;
  readonly updated: string;
}

export interface BookmarkStore {
  readonly version: number;
  readonly items: readonly Bookmark[];
}

export interface NamedViewLayout {
  readonly rows: Readonly<Record<string, number>>;
  readonly cols: Readonly<Record<string, number>>;
  readonly font: string;
  readonly columnMode: string;
  readonly columnVisibility: Readonly<Record<string, boolean>>;
}

export interface NamedViewTable {
  readonly row: number;
  readonly startCol: number;
  readonly endCol: number;
  readonly sticky: boolean;
}

export interface NamedViewConfig {
  readonly freeze: { readonly rows: number; readonly cols: number };
  readonly layout: NamedViewLayout;
  readonly table: NamedViewTable | null;
  readonly zoom: number;
  readonly showStyles: boolean;
  readonly showFormulas: boolean;
  readonly rowVisibility?: AxisViewSettings;
}

export interface ReconcileField {
  readonly column: number;
  readonly name: string;
  readonly rawName?: string;
  readonly headerHash?: string;
  readonly headerLoaded: boolean;
  readonly truncated?: boolean;
  readonly uniqueHeader?: boolean;
}

export interface ViewSourceBinding {
  readonly fingerprint: string;
  readonly format: string;
  readonly name: string;
  readonly sheet: string;
  readonly sheetPath: string;
  readonly headerRow: number;
  readonly columns: number;
  readonly fields: readonly ReconcileField[];
}

export interface NamedView {
  readonly id: string;
  readonly name: string;
  readonly ruleVersion: number;
  readonly source: ViewSourceBinding;
  readonly query: SourceFilterSpec;
  readonly config: NamedViewConfig;
  readonly created: string;
  readonly updated: string;
}

export interface NamedViewStore {
  readonly version: number;
  readonly revision: number;
  readonly fingerprint: string;
  readonly items: readonly NamedView[];
}

export interface CatalogueArea {
  readonly sheet: string;
  readonly start: string;
  readonly end: string;
  readonly row: number;
  readonly col: number;
  readonly endRow: number;
  readonly endCol: number;
}

export interface CatalogueTable {
  readonly ref: string;
  readonly headerRows: number;
  readonly totalRows: number;
  readonly columns?: readonly { readonly id: string; readonly name: string }[];
  readonly part?: string;
  readonly relationId?: string;
  readonly sourceId?: string;
  readonly verified: boolean;
}

export interface CatalogueItem {
  readonly id: string;
  readonly kind: string;
  readonly name: string;
  readonly scope?: string;
  readonly sheet?: string;
  readonly scopeId?: string;
  readonly hidden?: boolean;
  readonly comment?: string;
  readonly definition: string;
  readonly tokenHex?: string;
  readonly status: string;
  readonly reason?: string;
  readonly areas?: readonly CatalogueArea[];
  readonly table?: CatalogueTable;
}

export interface CatalogueListItem {
  readonly id: string;
  readonly kind: string;
  readonly name: string;
  readonly scope: string;
  readonly sheet: string;
  readonly status: string;
  readonly hidden: boolean;
  readonly shortened: boolean;
  readonly unknownScope: boolean;
  readonly areas: number;
}

export interface CatalogueItemsResponse {
  readonly items: readonly CatalogueListItem[];
  readonly offset: number;
  readonly previous: number;
  readonly next: number;
  readonly matched: number;
  readonly total: number;
  readonly complete: boolean;
  readonly notices: readonly string[];
  readonly workbookKey: string;
  readonly book: string;
}

export interface SourceDataRule {
  readonly id: number;
  readonly namespace?: string;
  readonly sqref: string;
  readonly ranges: readonly MergeRange[];
  readonly type: string;
  readonly operator: string;
  readonly errorStyle: string;
  readonly imeMode: string;
  readonly allowBlank: boolean;
  readonly suppressDropDown: boolean;
  readonly showInputMessage: boolean;
  readonly showErrorMessage: boolean;
  readonly promptTitle?: string;
  readonly prompt?: string;
  readonly errorTitle?: string;
  readonly error?: string;
  readonly formula1?: string;
  readonly formula2?: string;
  readonly formula1Tokens?: string;
  readonly formula2Tokens?: string;
  readonly listDefinition?: string;
  readonly rawXml?: string;
  readonly recordHex?: string;
  readonly continuationHex?: readonly string[];
  readonly inlineList?: readonly string[];
  readonly listSource?: string;
  readonly status: string;
  readonly reason?: string;
  readonly attributes?: Readonly<Record<string, string>>;
}

export interface DataRuleContainer {
  readonly namespace?: string;
  readonly disablePrompts: boolean;
  readonly attributes?: Readonly<Record<string, string>>;
}

export interface DataRuleSummary {
  readonly rules: number;
  readonly ranges: number;
  readonly unresolved: number;
  readonly unknownScopes: number;
  readonly complete: boolean;
  readonly disablePrompts: boolean;
  readonly inputWindowClosed?: boolean;
  readonly notices?: readonly string[];
  readonly containers?: readonly DataRuleContainer[];
  readonly promptSettingsKnown: boolean;
}

export interface DataRuleListItem {
  readonly id: number;
  readonly type: string;
  readonly status: string;
  readonly sqref: string;
  readonly rangeCount: number;
  readonly listItems: number;
  readonly listSource?: string;
  readonly shortened: boolean;
}

export interface DataRuleListPage {
  readonly items: readonly DataRuleListItem[];
  readonly offset: number;
  readonly previous: number;
  readonly next: number;
  readonly matched: number;
  readonly total: number;
  readonly scopeComplete: boolean;
}

export interface DataRuleDetailPage {
  readonly item: SourceDataRule;
  readonly rangeCount: number;
  readonly rangeOffset: number;
  readonly rangePrevious: number;
  readonly rangeNext: number;
  readonly listCount: number;
  readonly listOffset: number;
  readonly listPrevious: number;
  readonly listNext: number;
}

export interface PrintInterval {
  readonly first: number;
  readonly last: number;
}

export interface PrintOptions {
  readonly usePreviewDefaults: boolean;
  readonly paper?: { readonly width: string; readonly height: string };
  readonly orientation?: string;
  readonly margins?: {
    readonly left: string;
    readonly right: string;
    readonly top: string;
    readonly bottom: string;
    readonly header: string;
    readonly footer: string;
  };
  readonly scalePercent?: string;
  readonly fit?: { readonly width: number; readonly height: number };
  readonly areas: readonly MergeRange[];
  readonly repeatRows: readonly PrintInterval[];
  readonly repeatColumns: readonly PrintInterval[];
  readonly missingRowHeight?: string;
  readonly missingColumnWidth?: string;
  readonly pageOrder?: string;
  readonly firstPageNumber?: number;
  readonly ignoreManualBreaks: boolean;
}

export interface PrintDimensions {
  readonly width: string;
  readonly height: string;
}

export interface PrintMarginUnits {
  readonly left: string;
  readonly right: string;
  readonly top: string;
  readonly bottom: string;
  readonly header: string;
  readonly footer: string;
}

export interface PrintScale {
  readonly numerator: string;
  readonly denominator: string;
  readonly percent: string;
}

export interface PrintPart {
  readonly first: number;
  readonly last: number;
  readonly offset: string;
  readonly length: string;
  readonly repeated: boolean;
}

export interface PrintPage {
  readonly number: number;
  readonly area: number;
  readonly rows: readonly PrintPart[];
  readonly columns: readonly PrintPart[];
  readonly body: PrintDimensions;
  readonly content: PrintDimensions;
  readonly offsetX: string;
  readonly offsetY: string;
  readonly oversize: boolean;
}

export interface PrintGeometryDefault {
  readonly value: string;
  readonly unit: string;
}

export interface PrintGeometryDefaults {
  readonly row?: PrintGeometryDefault;
  readonly column?: PrintGeometryDefault;
}

export interface PrintSummary {
  readonly sourceId: string;
  readonly sheet: string;
  readonly pages: number;
  readonly paper: PrintDimensions;
  readonly margins: PrintMarginUnits;
  readonly scale: PrintScale;
  readonly areas: readonly MergeRange[] | null;
  readonly repeatRows: readonly PrintInterval[];
  readonly repeatColumns: readonly PrintInterval[];
  readonly pageOrder: string;
  readonly firstPageNumber: number;
  readonly headings: PrintDimensions;
  readonly defaults: PrintGeometryDefaults;
  readonly approximate: boolean;
  readonly origins: Readonly<Record<string, string>>;
  readonly notices: readonly string[];
}

export interface SourcePrintProperty {
  readonly type: string;
  readonly value: string;
  readonly status: string;
  readonly reason?: string;
}

export interface PrintPageRect {
  readonly x: string;
  readonly y: string;
  readonly width: string;
  readonly height: string;
}

export interface PrintPageAxis {
  readonly number: number;
  readonly part: number;
  readonly start: string;
  readonly size: string;
  readonly clipStart: string;
  readonly clipSize: string;
  readonly repeated: boolean;
}

export interface PrintCellBorders {
  readonly borderTop?: string;
  readonly borderRight?: string;
  readonly borderBottom?: string;
  readonly borderLeft?: string;
}

export interface PrintPageCell {
  readonly row: number;
  readonly col: number;
  readonly ref: string;
  readonly cell?: CellData;
  readonly style: number;
  readonly box: PrintPageRect;
  readonly clip: PrintPageRect;
  readonly merge?: MergeRange;
  readonly hiddenAnchor?: boolean;
  readonly borders?: PrintCellBorders;
}

export interface PrintPageContent {
  readonly page: PrintPage;
  readonly rows: readonly PrintPageAxis[];
  readonly columns: readonly PrintPageAxis[];
  readonly cells: readonly PrintPageCell[];
  readonly styles: Readonly<Record<string, VisualStyle>>;
  readonly notices: readonly string[];
}

export interface CellExplanationResponse {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId: string;
  readonly filterId?: string;
  readonly ref: string;
  readonly pinnedThroughRow: number;
  readonly cell: CellData;
  readonly numeric: { readonly eligible: boolean; readonly reason: string; readonly exact?: string };
  readonly formulaState: string;
  readonly formulaAvailable: boolean;
  readonly filter?: SourceFilterSpec;
  readonly rowState: string;
  readonly presentationNotice?: string;
}

export interface RecordCardResponse {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId: string;
  readonly filterId?: string;
  readonly row: number;
  readonly headerRow: number;
  readonly firstColumn: number;
  readonly columns: number;
  readonly pinnedThroughRow: number;
  readonly position: number;
  readonly totalRecords: number;
  readonly previousRow: number;
  readonly nextRow: number;
  readonly totalColumns: number;
  readonly rowStyle: number;
  readonly headerStyle: number;
  readonly rowIndexed: boolean;
  readonly fields: readonly {
    readonly column: number;
    readonly header?: CellData;
    readonly cell: CellData;
  }[];
  readonly notices: readonly string[];
}

export interface ProfileResponse {
  readonly workbookKey?: string;
  readonly book?: string;
  readonly sheet: string;
  readonly range: string;
  readonly rows: number;
  readonly firstColumn: number;
  readonly lastColumn: number;
  readonly total: number;
  readonly nonEmpty: number;
  readonly blank: number;
  readonly number: number;
  readonly text: number;
  readonly formulas: number;
  readonly cacheMissing: number;
  readonly errors: number;
  readonly sum: string;
  readonly average: string;
  readonly minimum: string;
  readonly maximum: string;
  readonly sumExact: string;
  readonly averageExact: string;
  readonly minimumExact: string;
  readonly maximumExact: string;
  readonly unique: number;
  readonly uniqueTruncated: boolean;
  readonly topValues: readonly { readonly value: string; readonly count: number }[];
  readonly errorRefs: readonly string[];
  readonly missingRefs: readonly string[];
  readonly filtered: boolean;
  readonly warning?: string;
  readonly distribution?: NumericDistribution;
}

export interface SelectionStatsResponse {
  readonly total: number;
  readonly nonEmpty: number;
  readonly number: number;
  readonly sum: string;
  readonly average: string;
  readonly minimum?: string;
  readonly maximum?: string;
  readonly ranges: number;
  readonly filtered: boolean;
}

export interface QuickCounts {
  readonly number: number;
  readonly negative: number;
  readonly zero: number;
  readonly positive: number;
  readonly blank: number;
  readonly other: number;
  readonly errors: number;
  readonly missing: number;
  readonly invalid: number;
}

export interface SelectionQuickResponse {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly viewId?: string;
  readonly filterId?: string;
  readonly start: string;
  readonly end: string;
  readonly skipFirstRow: boolean;
  readonly minuend: number;
  readonly subtrahend: number;
  readonly selectedRows: number;
  readonly rows: number;
  readonly firstColumn: number;
  readonly lastColumn: number;
  readonly columns: readonly {
    readonly column: number;
    readonly counts: QuickCounts;
    readonly summary: QuickSummary;
    readonly segments: readonly QuickSegment[];
  }[];
  readonly difference?: {
    readonly paired: number;
    readonly unpaired: number;
    readonly summary: QuickSummary;
    readonly segments: readonly QuickSegment[];
  };
}

export interface InspectResponse {
  readonly sheet: string;
  readonly start: string;
  readonly end: string;
  readonly mode: string;
  readonly matches: readonly SearchHit[];
  readonly matched: number;
  readonly scanned: number;
  readonly duplicateGroups: number;
  readonly truncated: boolean;
  readonly durationMs: number;
}

export interface AutofitResponse {
  readonly kind: 'col' | 'row';
  readonly index: number;
  readonly sampledRows: number;
  readonly totalRows: number;
  readonly rows: readonly RowData[] | null;
}

export interface ReconcileSourceRef {
  book: string;
  source: string;
  headerRow: number;
  stamp?: string;
}

export interface ReconcileBookChoice {
  readonly id: string;
  readonly name: string;
  readonly attached: boolean;
  readonly incomplete: boolean;
  readonly sheets: readonly SheetMeta[];
  readonly scratch: readonly { readonly id: string; readonly name: string }[] | null;
  readonly scratchLoaded: boolean;
}

export interface ReconcileValue {
  readonly raw: string;
  readonly display?: string;
  readonly type?: string;
  readonly exists: boolean;
  readonly missingCache?: boolean;
  readonly uncertain?: string;
  readonly typeMissing?: boolean;
  readonly truncated?: boolean;
}

export interface ReconcileRecord {
  readonly position: number;
  readonly number: number;
  readonly values: Readonly<Record<string, ReconcileValue>>;
}

export interface ReconcileSourceInfo {
  readonly id: string;
  readonly name: string;
  readonly sheet: string;
  readonly kind: string;
  readonly format?: string;
  readonly notice?: string;
  readonly fingerprint: string;
  readonly stamp: string;
  readonly headerRow: number;
  readonly fields: readonly ReconcileField[];
  readonly total: number;
}

export interface ReconcilePair {
  tolerance?: string;
  left: number;
  right: number;
  mode?: string;
  trim?: boolean;
  ignoreCase?: boolean;
}

export interface ReconcileQuery {
  readonly ruleVersion?: number;
  readonly keys: readonly ReconcilePair[];
  readonly fields: readonly ReconcilePair[];
  readonly includeEqual?: boolean;
}

export interface ReconcileComparison {
  readonly withinTolerance?: boolean;
  readonly pair: number;
  readonly equal: boolean;
  readonly delta?: string;
  readonly reason?: string;
}

export interface ReconcileGroup {
  readonly keyId: string;
  readonly key: readonly string[];
  readonly status: string;
  readonly leftCount: number;
  readonly rightCount: number;
  readonly left?: ReconcileRecord;
  readonly right?: ReconcileRecord;
  readonly leftRows?: readonly number[];
  readonly rightRows?: readonly number[];
  readonly comparisons?: readonly ReconcileComparison[];
  readonly reason?: string;
  readonly textTruncated?: boolean;
}

export interface ReconcileCounts {
  readonly withinTolerance: number;
  readonly equal: number;
  readonly different: number;
  readonly leftOnly: number;
  readonly rightOnly: number;
  readonly duplicate: number;
  readonly review: number;
  readonly invalidKeys: number;
  readonly leftRecords: number;
  readonly rightRecords: number;
}

export interface ReconcileResult {
  readonly left: ReconcileSourceInfo;
  readonly right: ReconcileSourceInfo;
  readonly query: ReconcileQuery;
  readonly counts: ReconcileCounts;
  readonly samples: readonly ReconcileGroup[];
  readonly samplesTruncated: boolean;
}

export interface RelationQuery {
  readonly keys: readonly ReconcilePair[];
  readonly leftColumns: readonly number[];
  readonly rightColumns: readonly number[];
}

export interface RelationCounts {
  readonly leftRecords: string;
  readonly rightRecords: string;
  readonly leftInvalid: string;
  readonly rightInvalid: string;
  readonly leftDuplicateGroups: string;
  readonly rightDuplicateGroups: string;
  readonly manyToManyGroups: string;
  readonly matchingGroups: string;
  readonly leftMatched: string;
  readonly rightMatched: string;
  readonly leftOnly: string;
  readonly rightOnly: string;
  readonly innerRows: string;
  readonly leftJoinRows: string;
}

export interface RelationRow {
  readonly sequence: string;
  readonly left?: ReconcileRecord;
  readonly right?: ReconcileRecord;
  readonly status: string;
  readonly reason?: string;
  readonly rightMatches: string;
}

export interface RelationPreview {
  readonly rows: readonly RelationRow[];
  readonly total: string;
  readonly truncated: boolean;
}

export interface RelationLimits {
  readonly keys: number;
  readonly columns: number;
  readonly previewRows: number;
  readonly defaultRows: number;
  readonly maximumRows: number;
  readonly requestBytes: number;
  readonly resultBytes: number;
  readonly attachments: number;
  readonly viewRows: number;
  readonly viewColumns: number;
  readonly initialFields: number;
  readonly snippetUnits: number;
  readonly zipRows: number;
}

export interface RelationResultResponse {
  readonly id: string;
  readonly mode: string;
  readonly outputMode: string;
  readonly parentId: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly left: ReconcileSourceInfo;
  readonly right: ReconcileSourceInfo;
  readonly query: RelationQuery;
  readonly counts: RelationCounts;
  readonly preview: RelationPreview;
}

export interface ArchiveMember {
  readonly id: number;
  readonly name: string;
  readonly size: number;
  readonly packed?: number;
  readonly archive?: boolean;
  readonly incomplete?: boolean;
  readonly declaredSize?: number;
}

export interface ArchiveListResponse {
  readonly archive: ArchiveSummary & {
    readonly id: string;
    readonly name: string;
    readonly format: string;
    readonly count: number;
    readonly total: number;
    readonly depth: number;
    readonly notice: string;
  };
  readonly items: readonly ArchiveMember[];
  readonly total: number;
  readonly offset: number;
  readonly limit: number;
}

export interface DBEngineProtocol {
  readonly id: string;
  readonly label: string;
  readonly plainPort: number;
  readonly tlsPort: number;
}

export interface DBEngine {
  readonly id: string;
  readonly label: string;
  readonly protocols: readonly DBEngineProtocol[];
}

export interface DBConfig {
  readonly engine: string;
  readonly protocol: string;
  readonly host: string;
  readonly port: number;
  readonly database: string;
  readonly schema: string;
  readonly user: string;
  readonly password: string;
  readonly tls: string;
  readonly ca: string;
}

export interface DBImportProgress {
  readonly id: string;
  readonly status: string;
  readonly name?: string;
  readonly table: number;
  readonly tables: number;
  readonly rows: number;
  readonly error?: string;
  readonly workbookKey?: string;
}

export interface DatabaseSnapshotSource {
  readonly engine: string;
  readonly protocol: string;
  readonly host: string;
  readonly port: number;
  readonly database?: string;
  readonly tls: string;
}

export interface DatabaseSnapshotSummary {
  readonly key: string;
  readonly name: string;
  readonly taken: string;
  readonly sheets: number;
  readonly records: number;
  readonly bytes: number;
  readonly retained: boolean;
  readonly source?: DatabaseSnapshotSource;
  readonly tables?: readonly string[];
  readonly legacyKey?: string;
  readonly error?: string;
}

export interface TaskCheckpointInfo {
  readonly full?: boolean;
  readonly id: string;
  readonly revision: string;
  readonly kind: string;
  readonly driver: string;
  readonly title: string;
  readonly file: string;
  readonly path: string;
  readonly book: string;
  readonly workbookKey: string;
  readonly sheet: string;
  readonly query: string;
  readonly mode: string;
  readonly scope: string;
  readonly updated: string;
  readonly complete: boolean;
  readonly busy: boolean;
  readonly backup: boolean;
  readonly error: string;
}

export interface BackgroundItem {
  readonly id: string;
  readonly kind: string;
  readonly file: string;
  readonly state: string;
  readonly stage: string;
  readonly error?: string;
  readonly created: string;
  readonly session: string;
  readonly canAttach: boolean;
  readonly protected?: boolean;
}

export interface BackgroundSession {
  readonly session: string;
  readonly file: string;
}

export interface BackgroundStatusResponse {
  readonly items: readonly BackgroundItem[] | null;
  readonly sessions: readonly BackgroundSession[] | null;
}

export interface StorageRecoveryResponse {
  readonly pending: boolean;
  readonly reason: string;
  readonly running: boolean;
  readonly roots: { readonly config: string; readonly local: string };
  readonly progress: {
    readonly phase?: string;
    readonly bytes?: number;
    readonly total?: number;
    readonly files?: number;
    readonly items?: number;
  };
}

export interface StorageGroupUsage {
  readonly id?: string;
  readonly name: string;
  readonly kind?: string;
  readonly paths?: readonly string[];
  readonly allocated: number;
  readonly files?: number;
  readonly error?: string;
}

export interface StorageUsageResponse {
  readonly groups?: readonly StorageGroupUsage[] | null;
  readonly roots?: { readonly config: string; readonly local: string };
  readonly conditionalStats?: {
    readonly budgetBytes: number;
    readonly minBudgetBytes?: number;
    readonly maxBudgetBytes?: number;
    readonly defaultBudgetBytes?: number;
    readonly usedBytes?: number;
    readonly protectedFiles?: number;
    readonly bytes?: number;
    readonly files?: number;
  };
}

export interface CalculatorStep {
  readonly operation: string;
  readonly left: string;
  readonly right?: string;
  readonly result: string;
}

export interface CalculatorResult {
  readonly kind?: string;
  readonly value: string;
  readonly display: string;
  readonly copy: string;
  readonly exact: string;
  readonly approximate: boolean;
  readonly rounded: boolean;
  readonly zero: boolean;
  readonly steps: readonly CalculatorStep[];
  readonly used: readonly string[];
  readonly suggestions?: readonly {
    readonly kind: string;
    readonly label: string;
    readonly preview: string;
  }[];
}

export interface SelectionStatRect {
  readonly start: string;
  readonly end: string;
  readonly visualStart?: number;
  readonly visualEnd?: number;
}

export interface CalculatorOperand {
  readonly id?: string;
  readonly kind: 'manual' | 'cell' | 'sum' | 'result';
  readonly value: string;
  readonly raw?: string;
  readonly display?: string;
  readonly file?: string;
  readonly workbookKey?: string;
  readonly sheet?: string;
  readonly ref?: string;
  readonly ranges?: readonly SelectionStatRect[];
  readonly filtered?: boolean;
  readonly total?: number;
  readonly numeric?: number;
  readonly text?: number;
  readonly blank?: number;
  readonly textNumbers?: number;
  readonly notice?: string;
  readonly capturedAt: string;
  readonly detail?: string;
}

export interface CalculatorSnapshot {
  readonly name: string;
  readonly note?: string;
  readonly expression: string;
  readonly precision: number;
  readonly percent: boolean;
  readonly operands: readonly CalculatorOperand[];
}

export interface SavedCalculation extends CalculatorSnapshot {
  readonly id: string;
  readonly createdAt: string;
  readonly result: CalculatorResult;
}
