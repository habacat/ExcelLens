/**
 * Analysis Workspace, Recipes, Text Clustering, Comparison Snapshots, and
 * Multi-Table Composition API contracts. Mirrors `internal/sourceview`,
 * `internal/analysis`, `internal/recipe`, `internal/clustering`,
 * `internal/composition`, and `internal/server/analysis_*.go`.
 */

import type { AsyncJobStatusKind } from './common.ts';

export interface SourceSelection {
  readonly startRow: number;
  readonly endRow: number;
  readonly startColumn: number;
  readonly endColumn: number;
  readonly visualStart?: number;
  readonly visualEnd?: number;
}

export interface SourceFilterRule {
  readonly column: number;
  readonly operator: string;
  readonly value: string;
  readonly values?: readonly string[];
}

export interface SourceSortKey {
  readonly column: number;
  readonly descending: boolean;
  readonly mode?: string;
}

export interface SourceFilterSpec {
  readonly rules: readonly SourceFilterRule[] | null;
  readonly headerRow: number;
  readonly sortColumn?: number;
  readonly descending?: boolean;
  readonly sortKeys: readonly SourceSortKey[] | null;
}

export interface AxisVisibilityOverride {
  readonly min: number;
  readonly max: number;
  readonly hidden: boolean;
}

export interface AxisOutlineOverride {
  readonly min: number;
  readonly max: number;
  readonly level: number;
  readonly collapsed: boolean;
}

export interface AxisViewSettings {
  readonly mode?: 'original' | 'show-all' | 'hide-all' | '';
  readonly ranges?: readonly AxisVisibilityOverride[];
  readonly depth?: number;
  readonly groups?: readonly AxisOutlineOverride[];
}

export interface SourceScope {
  readonly version: number;
  readonly fingerprint: string;
  readonly mode: 'filtered' | 'visible' | 'selection';
  readonly filter?: SourceFilterSpec;
  readonly visibility?: AxisViewSettings;
  readonly selection?: SourceSelection;
  readonly frozenThrough?: number;
}

export interface TableRef {
  book: string;
  fingerprint: string;
  source: string;
  stamp?: string;
  headerRow: number;
  scope?: SourceScope;
}

export interface AppendInput {
  readonly ref: TableRef;
  readonly columns: readonly number[];
}

export interface CompositionDefinition {
  readonly version: number;
  readonly name: string;
  readonly fields: readonly string[];
  readonly inputs: readonly AppendInput[];
}

export interface HeaderMatch {
  readonly field: string;
  readonly target: string;
  readonly status:
    | 'match'
    | 'missing'
    | 'manual'
    | 'ambiguous'
    | 'uncertain'
    | 'untyped'
    | 'type_mismatch'
    | 'mixed';
  readonly candidates: readonly string[];
}

export interface SourceOrigin {
  readonly input: number;
  readonly book: string;
  readonly fingerprint: string;
  readonly file: string;
  readonly source: string;
  readonly sheet: string;
  readonly stamp: string;
  readonly columns: readonly number[];
}

export interface AnalysisValue {
  readonly text: string;
  readonly display?: string;
  readonly numeric?: boolean;
  readonly missingCache?: boolean;
  readonly conversionFailed?: boolean;
  readonly textual?: boolean;
}

export interface AnalysisField {
  readonly id: string;
  readonly name: string;
  readonly column: number;
  readonly numeric?: boolean;
}

export type AnalysisFilterOp =
  | 'eq'
  | 'neq'
  | 'contains'
  | 'notcontains'
  | 'startswith'
  | 'endswith'
  | 'blank'
  | 'notblank'
  | 'gt'
  | 'gte'
  | 'lt'
  | 'lte'
  | 'between'
  | 'in';

export interface AnalysisFilter {
  field: string;
  op: AnalysisFilterOp;
  value?: string;
}

export interface TextMapping {
  readonly from: string;
  readonly to: string;
}

export type AnalysisTransformOp =
  | 'trim'
  | 'upper'
  | 'lower'
  | 'year'
  | 'month'
  | 'yearmonth'
  | 'number'
  | 'text'
  | 'abs'
  | 'multiply'
  | 'divide'
  | 'prefix'
  | 'suffix'
  | 'replace'
  | 'length'
  | 'map_text'
  | 'date'
  | 'time'
  | 'datetime';

export interface AnalysisTransform {
  id: string;
  name: string;
  source: string;
  op: AnalysisTransformOp;
  arg?: string;
  mapping?: readonly TextMapping[];
}

export type AnalysisAggregateKind = 'count' | 'sum' | 'average' | 'min' | 'max' | 'distinct';

export type ComparisonMode =
  | ''
  | 'percent_grand'
  | 'percent_row'
  | 'percent_column'
  | 'running_total'
  | 'running_percent';

export type ComparisonAxis = '' | 'rows' | 'columns';

export interface AnalysisMetric {
  field?: string;
  aggregate: AnalysisAggregateKind;
  name?: string;
  comparison?: ComparisonMode;
  comparisonAxis?: ComparisonAxis;
}

export type AnalysisSortOrder = '' | 'value_desc' | 'value_asc' | 'label_asc' | 'label_desc';

export interface AnalysisQuery {
  source: string;
  sourceScope?: SourceScope;
  composition?: CompositionDefinition;
  headerRow?: number;
  rows: string[] | null;
  columns?: string[];
  value?: string;
  aggregate: AnalysisAggregateKind;
  metrics?: AnalysisMetric[];
  filters?: AnalysisFilter[];
  transforms?: AnalysisTransform[];
  sort?: AnalysisSortOrder;
  top?: number;
  precision?: number;
  chart?: string;
  chartMetric?: number;
  layout?: '' | 'tree';
}

export interface ComparedValue {
  readonly mode: string;
  readonly axis?: string;
  readonly display: string;
  readonly exact: string;
  readonly status?:
    | ''
    | 'empty'
    | 'empty_denominator'
    | 'zero_denominator'
    | 'missing'
    | 'only_right'
    | 'only_left'
    | 'unavailable'
    | 'zero_base';
  readonly percent?: boolean;
  readonly numerator?: string;
  readonly denominator?: string;
}

export interface AnalysisCell {
  readonly display: string;
  readonly exact: string;
  readonly count: number;
  readonly comparison?: ComparedValue;
}

export interface AnalysisResultRow {
  readonly keys: readonly string[];
  readonly values: readonly AnalysisCell[];
  readonly total: AnalysisCell;
  readonly metricValues?: readonly (readonly AnalysisCell[])[];
  readonly metricTotals?: readonly AnalysisCell[];
  readonly count: number;
}

export interface TreeLevel {
  readonly path: readonly string[];
  readonly levels: number;
  readonly hasChildren: boolean;
}

export interface AnalysisIssue {
  readonly origin?: SourceOrigin;
  readonly sequence?: number;
  readonly kind: 'conversion' | 'missing_cache' | 'number';
  readonly row: number;
  readonly field: string;
  readonly fieldName: string;
  readonly sourceField: string;
  readonly sourceColumn: number;
  readonly original: string;
  readonly input: string;
  readonly value: string;
  readonly reason: string;
  readonly textTruncated?: boolean;
}

export interface AnalysisResult {
  readonly rowFields: readonly string[] | null;
  readonly columnField?: string;
  readonly columnKeys: readonly string[];
  readonly rows: readonly AnalysisResultRow[];
  readonly totals: readonly AnalysisCell[];
  readonly metrics?: readonly AnalysisMetric[];
  readonly metricGrand?: readonly AnalysisCell[];
  readonly metricColumnTotals?: readonly (readonly AnalysisCell[])[];
  readonly grand: AnalysisCell;
  readonly scanned: number;
  readonly included: number;
  readonly invalidNumbers: number;
  readonly invalidConversions?: number;
  readonly missingFormulaCaches: number;
  readonly issues?: readonly AnalysisIssue[];
  readonly issuesTruncated?: boolean;
  readonly aggregation: string;
  readonly truncated?: boolean;
  readonly tree?: TreeLevel;
}

export interface DrillRow {
  readonly row: number;
  readonly cells: readonly string[];
  readonly sequence?: number;
  readonly origin?: SourceOrigin;
}

/** @httpResponse DrillResult */
export interface DrillResult {
  readonly fields: readonly AnalysisField[];
  readonly rows: readonly DrillRow[] | null;
  readonly hasMore: boolean;
  readonly scanned: number;
}

export interface AnalysisCellChange {
  readonly row: number;
  readonly col: number;
  readonly value: string;
}

export interface AnalysisSheetSummary {
  readonly id: string;
  readonly name: string;
  readonly columns: readonly string[];
  readonly rows: number;
  readonly updated: string;
  readonly revision: number;
}

/** @httpResponse AnalysisSheetPage */
export interface AnalysisSheetPage {
  readonly id: string;
  readonly name: string;
  readonly columns: readonly string[];
  readonly rows: readonly (readonly string[])[];
  readonly offset: number;
  readonly total: number;
  readonly revision: number;
}

export interface AnalysisReport {
  readonly id: string;
  readonly name: string;
  readonly query: AnalysisQuery;
  readonly updated: string;
}

export interface AnalysisSheetMeta {
  readonly name: string;
  readonly state?: string;
  readonly tabColor?: string;
  readonly sourceName?: string;
}

/** @httpResponse AnalysisWorkspaceResponse */
export interface AnalysisWorkspaceResponse {
  readonly sheets: readonly AnalysisSheetSummary[];
  readonly reports: readonly AnalysisReport[];
  readonly dashboardOrder: readonly string[] | null;
  readonly sourceSheets: readonly AnalysisSheetMeta[];
  readonly workbook: string;
  readonly fingerprint: string;
}

/** @httpResponse AnalysisFieldsResponse */
export interface AnalysisFieldsResponse {
  readonly fields: readonly AnalysisField[];
  readonly preview: readonly DrillRow[];
  readonly totalRows: number;
  readonly headerRow: number;
  readonly truncatedFields: boolean;
  readonly composition?: CompositionDefinition;
  readonly sourceScope?: SourceScope | null;
  readonly sourceStamp?: string;
}

/** @httpResponse AnalysisProfileResponse */
export interface AnalysisProfileResponse {
  readonly field: string;
  readonly name: string;
  readonly rows: number;
  readonly nonEmpty: number;
  readonly empty: number;
  readonly missingFormulaCaches: number;
  readonly unique: number;
  readonly uniqueTruncated: boolean;
  readonly numeric: number;
  readonly minText: string;
  readonly maxText: string;
  readonly minNumber: string;
  readonly maxNumber: string;
  readonly frequent: readonly { readonly value: string; readonly count: number }[];
}

/** @httpResponse AnalysisSourceBindingResponse */
export interface AnalysisSourceBindingResponse {
  readonly id: string;
  readonly source: string;
  readonly sourceScope: SourceScope;
  readonly rows: number;
  readonly workbookKey: string;
  readonly book: string;
}

/** @httpResponse AnalysisQueryStatusResponse */
export interface AnalysisQueryStatusResponse {
  readonly id?: string;
  readonly status: AsyncJobStatusKind;
  readonly stage: string;
  readonly progress: number;
  readonly total: number;
  readonly error: string;
  readonly result?: AnalysisResult;
  readonly query?: AnalysisQuery;
  readonly sourceStamp?: string;
  readonly sourceFields?: readonly AnalysisField[];
  readonly recipeId?: string;
  readonly exportFormat?: 'csv' | 'xlsx';
}

/** @httpResponse AnalysisTreeStatusResponse */
export interface AnalysisTreeStatusResponse {
  readonly id?: string;
  readonly status: AsyncJobStatusKind;
  readonly error: string;
  readonly stage: string;
  readonly progress: number;
  readonly total: number;
  readonly rootJobId: string;
  readonly path: readonly string[];
  readonly result: AnalysisResult | null;
}

export interface AnalysisDisplayOptions {
  groupThousands: boolean;
  scientific: boolean;
  scientificExponent: 9 | 12 | 15;
}

/** @httpResponse AnalysisDisplayConfig */
export interface AnalysisDisplayConfig {
  readonly version: number;
  readonly revision: number;
  readonly options: AnalysisDisplayOptions;
}

export interface ComparisonSnapshot {
  readonly version: number;
  readonly id: string;
  readonly name: string;
  readonly created: string;
  readonly analysisAt: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly file: string;
  readonly sourceStamp: string;
  readonly query: AnalysisQuery;
  readonly fields: readonly AnalysisField[];
  readonly metricIndex: number;
  readonly metric: AnalysisMetric;
  readonly result: AnalysisResult;
}

export interface SnapshotDelta {
  readonly left: AnalysisCell | null;
  readonly right: AnalysisCell | null;
  readonly status?: 'missing' | 'only_right' | 'only_left' | 'empty' | 'unavailable';
  readonly difference: AnalysisCell;
  readonly change: ComparedValue;
}

export interface SnapshotComparisonRow {
  readonly keys: readonly string[];
  readonly values: readonly SnapshotDelta[];
  readonly total: SnapshotDelta;
}

export interface SnapshotComparison {
  readonly leftId: string;
  readonly rightId: string;
  readonly columnKeys: readonly string[];
  readonly totals: readonly SnapshotDelta[] | null;
  readonly rows: readonly SnapshotComparisonRow[];
  readonly grand: SnapshotDelta;
}

export interface ComparisonSnapshotSummary {
  readonly id: string;
  readonly name: string;
  readonly created: string;
  readonly file: string;
}

export interface ComparisonSnapshotCatalog {
  readonly version: number;
  readonly revision: number;
  readonly items: readonly ComparisonSnapshotSummary[];
}

export interface RecipeField {
  readonly id: string;
  readonly column: number;
  readonly name: string;
  readonly headerHash: string;
  readonly headerLoaded: boolean;
  readonly headerUnique: boolean;
  readonly truncated: boolean;
  readonly mask: number;
  readonly uncertain: boolean;
}

export interface RecipeOrigin {
  readonly name: string;
  readonly source: string;
  readonly format: string;
  readonly fingerprint: string;
  readonly headerRow: number;
  readonly sourceScope?: SourceScope;
}

export interface RecipeGroup {
  readonly rows: readonly string[];
  readonly columns: readonly string[];
  readonly value: string;
  readonly aggregate: string;
  readonly metrics: readonly AnalysisMetric[];
  readonly sort: string;
  readonly top: number;
  readonly precision: number;
  readonly chart: string;
  readonly chartMetric: number;
  readonly layout?: '' | 'tree';
}

export interface RecipeStep {
  readonly kind: 'transform' | 'filter' | 'group' | 'export';
  readonly transform?: AnalysisTransform;
  readonly filter?: AnalysisFilter;
  readonly group?: RecipeGroup;
  readonly export?: { readonly format: 'csv' | 'xlsx' };
}

export interface Recipe {
  readonly version: number;
  readonly id: string;
  readonly name: string;
  readonly origin: RecipeOrigin;
  readonly fields: readonly RecipeField[];
  readonly steps: readonly RecipeStep[];
  readonly created: string;
  readonly updated: string;
}

export interface RecipeStore {
  readonly version: number;
  readonly revision: number;
  readonly items: readonly Recipe[];
}

export interface ClusterOptions {
  foldCase: boolean;
  foldWidth: boolean;
  space: '' | 'trim' | 'collapse' | 'remove';
  ignorePunctuation: boolean;
}

export interface ClusterVariant {
  readonly raw: string;
  readonly type: string;
  readonly count: number;
  readonly formulaCount: number;
  readonly rows: readonly number[];
}

export interface ClusterCounts {
  readonly scanned: number;
  readonly eligible: number;
  readonly blank: number;
  readonly missingCache: number;
  readonly numeric: number;
  readonly otherTypes: number;
  readonly emptyKey: number;
  readonly unique: number;
  readonly exactGroups: number;
  readonly similarGroups: number;
}

export interface ClusterEnvelope {
  readonly id: string;
  readonly workbookKey: string;
  readonly book: string;
  readonly source: string;
  readonly field: string;
  readonly fieldName: string;
  readonly headerRow: number;
  readonly sourceScope: SourceScope | null;
  readonly stamp: string;
}

export interface ClusterGroupSummary {
  readonly id: number;
  readonly key: string;
  readonly shortened: boolean;
  readonly kind: 'exact' | 'similar';
  readonly count: number;
  readonly variants: number;
}

/** @httpResponse ClusterGroupsResponse */
export interface ClusterGroupsResponse extends ClusterEnvelope {
  readonly items: readonly ClusterGroupSummary[];
  readonly offset: number;
  readonly next: number;
  readonly total: number;
  readonly counts: ClusterCounts;
  readonly options: ClusterOptions;
}

export interface ClusterDetailResponse extends ClusterEnvelope {
  readonly group: number;
  readonly key: string;
  readonly kind: 'exact' | 'similar';
  readonly count: number;
  readonly variants: readonly ClusterVariant[];
  readonly offset: number;
  readonly next: number;
  readonly total: number;
}

export type CompositionSampleType =
  | 'unknown'
  | 'empty'
  | 'number'
  | 'text'
  | 'boolean'
  | 'date'
  | 'error'
  | 'cache'
  | 'invalid'
  | 'mixed';

export interface CompositionField {
  readonly column: number;
  readonly name: string;
  readonly headerHash?: string;
  readonly headerLoaded: boolean;
  readonly headerUnique: boolean;
  readonly truncated?: boolean;
  readonly sampled?: boolean;
  readonly sampleType?: CompositionSampleType;
}

export interface CompositionSourceInfo {
  readonly ref: TableRef;
  readonly name: string;
  readonly sheet: string;
  readonly kind: string;
  readonly format?: string;
  readonly rows: number;
  readonly fields: readonly CompositionField[] | null;
}

export interface CompositionPreviewCell {
  readonly column: number;
  readonly sourceColumn: number;
  readonly exists: boolean;
  readonly ref: string;
  readonly raw: string;
  readonly display: string;
  readonly type?: string;
  readonly formula?: string;
  readonly hasFormula: boolean;
  readonly cacheMissing?: boolean;
  readonly sharedFormula?: boolean;
  readonly style: number;
  readonly valueColor?: string;
}

export interface CompositionPreviewRecord {
  readonly sequence: number;
  readonly input: number;
  readonly row: number;
  readonly position: number;
  readonly cells: readonly CompositionPreviewCell[];
}

/** @httpResponse CompositionTaskResult */
export interface CompositionTaskResult {
  readonly id: string;
  readonly mode: 'fields' | 'append';
  readonly workbookKey: string;
  readonly book: string;
  readonly source: string;
  readonly definition?: CompositionDefinition;
  readonly info?: CompositionSourceInfo;
  readonly sources?: readonly CompositionSourceInfo[];
  readonly rows: number;
  readonly preview?: readonly CompositionPreviewRecord[];
  readonly previewTruncated: boolean;
}

export interface SavedComposition {
  readonly id: string;
  readonly captureId: string;
  readonly definition: CompositionDefinition;
  readonly created: string;
  readonly updated: string;
}

/** @httpResponse CompositionDefinitionStore */
export interface CompositionDefinitionStore {
  readonly version: number;
  readonly revision: number;
  readonly items: readonly SavedComposition[];
}

export interface DistributionQuantile {
  readonly percent: number;
  readonly value: string;
}

export interface DistributionBin {
  readonly lower: string;
  readonly upper: string;
  readonly upperInclusive: boolean;
  readonly count: number;
}

export interface NumericDistribution {
  readonly method: string;
  readonly definition: string;
  readonly count: number;
  readonly quantiles: readonly DistributionQuantile[];
  readonly bins: readonly DistributionBin[];
}

export interface QuickSummary {
  readonly count: number;
  readonly sum: string;
  readonly average: string;
  readonly minimum: string;
  readonly maximum: string;
  readonly minimumRow: number;
  readonly maximumRow: number;
}

export interface QuickSegment {
  readonly first: number;
  readonly last: number;
  readonly firstRow: number;
  readonly lastRow: number;
  readonly summary: QuickSummary;
}

/** @httpResponse CompositionNavigationResponse */
export type CompositionNavigationResponse =
  | {
      readonly sameBook: true;
      readonly kind: string;
      readonly sheet: string;
      readonly analysisSheet: string;
      readonly row: number;
      readonly col: number;
    }
  | {
      readonly sameBook: false;
      readonly url: string;
    };
