/**
 * Editable Copy subsystem API contracts mirroring `internal/editdoc`,
 * `internal/editfile`, and `internal/server/edit*.go`.
 */
import type { MergeRange } from './common.ts';
import type { CellData, SheetMeta, SourceInfo } from './workbook.ts';

export interface EditAddress {
  readonly sheet: string;
  readonly row: number;
  readonly col: number;
}

export interface EditArea {
  readonly start: EditAddress;
  readonly endRow: number;
  readonly endCol: number;
}

export type EditValueKind =
  | ''
  | 'blank'
  | 'text'
  | 'number'
  | 'boolean'
  | 'error'
  | 'formula'
  | 'date'
  | 'missing';

export interface EditValue {
  readonly kind: EditValueKind;
  readonly raw: string;
  readonly style?: string;
  readonly numberStyle?: string;
  readonly builtinFormat?: number;
}

export interface EditChange extends EditAddress {
  readonly value: EditValue;
}

export interface EditKey {
  readonly id: string;
  readonly binding: string;
}

export interface EditInfo extends EditKey {
  readonly name: string;
  readonly revision: string;
  readonly updated: string;
  readonly changed: number;
  readonly rulesChanged?: number;
  readonly structuresChanged?: number;
  readonly undo: boolean;
  readonly redo: boolean;
}

export interface EditRule extends EditArea {
  readonly type: string;
  readonly operator?: string;
  readonly first: string;
  readonly second?: string;
  readonly allowBlank: boolean;
  readonly reference?: boolean;
}

export interface ReviewNote {
  readonly sheet: string;
  readonly ref: string;
  readonly text: string;
}

export interface PivotItem {
  readonly type: string;
  readonly value: string;
}

export interface PivotPage {
  readonly field: string;
  readonly items?: readonly PivotItem[];
}

export interface PivotAxes {
  readonly rows: readonly string[];
  readonly columns: readonly string[];
  readonly pages: readonly PivotPage[];
}

export interface PivotCalculatedItem {
  readonly field: string;
  readonly name: string;
  readonly formula: string;
}

export interface PivotValueFilter {
  readonly measure?: number;
  readonly field: string;
  readonly kind: string;
  readonly value: number;
  readonly second?: number;
}

export interface PivotMeasure {
  readonly field: string;
  readonly aggregate: string;
  readonly caption?: string;
  readonly sourceIndex?: number;
}

export interface PivotValuesAxis {
  readonly axis: string;
  readonly position: number;
}

export interface PivotLabelFilter {
  readonly field: string;
  readonly kind: string;
  readonly value?: string;
  readonly second?: string;
}

export interface PivotDateGroup {
  readonly start?: string;
  readonly end?: string;
  readonly interval?: number;
  readonly field: string;
  readonly units: readonly string[];
}

export interface PivotDateFilter {
  readonly field: string;
  readonly kind: string;
  readonly start?: string;
  readonly end?: string;
  readonly wholeDay?: boolean;
}

export interface PortableCell {
  readonly value: EditValue;
  readonly style?: number;
}

export interface PortableStyle {
  readonly builtin?: number;
  readonly format?: string;
  readonly font?: string;
  readonly fill?: string;
  readonly border?: string;
  readonly alignment?: string;
  readonly protection?: string;
}

export interface PortableRange {
  readonly visibleSourceRows?: readonly number[];
  readonly visibleSourceCols?: readonly number[];
  readonly origin?: string;
  readonly start: EditAddress;
  readonly rows: number;
  readonly cols: number;
  readonly cells: readonly PortableCell[];
  readonly styles: readonly PortableStyle[];
  readonly merges?: readonly EditArea[];
  readonly sourceRows?: readonly number[];
  readonly sourceCols?: readonly number[];
}

export interface PortableClipboard {
  readonly range: PortableRange | null;
  readonly proof: string;
}

export interface EditClipboardCell {
  readonly value?: EditValue;
  readonly formula?: string;
  readonly style?: string;
  readonly numberStyle?: string;
  readonly builtinFormat?: number;
}

/** @httpResponse EditClipboard */
export interface EditClipboard {
  readonly portable?: PortableClipboard;
  readonly format: 'ExcelLens.EditSelection';
  readonly version: 2;
  readonly source?: string;
  readonly start: EditAddress;
  readonly rows: number;
  readonly cols: number;
  readonly cells: readonly EditClipboardCell[];
  readonly proof?: string;
}

export interface EditStructure {
  readonly notes?: readonly ReviewNote[];
  readonly kind: string;
  readonly sheet: string;
  readonly name?: string;
  readonly at?: number;
  readonly count?: number;
  readonly before?: number;
  readonly pivot?: string;
  readonly rows?: string;
  readonly columns?: string;
  readonly data?: string;
  readonly aggregate?: string;
  readonly filter?: string;
  readonly filterMode?: string;
  readonly item?: string;
  readonly calculatedItem?: PivotCalculatedItem;
  readonly valueFilter?: PivotValueFilter;
  readonly axes?: PivotAxes;
  readonly measures?: readonly PivotMeasure[];
  readonly valuesAxis?: PivotValuesAxis;
  readonly labelFilter?: PivotLabelFilter;
  readonly dateFilter?: PivotDateFilter;
  readonly dateGroup?: PivotDateGroup;
  readonly paste?: PortableRange;
  readonly targetRows?: readonly number[];
  readonly pasteVisibleOnly?: boolean;
  readonly destinationSheet?: string;
  readonly destination?: string;
  readonly sourceRevision?: string;
  readonly range?: string;
  readonly formula?: string;
  readonly replaceContents?: boolean;
}

export type EditOperationAction = 'change' | 'rule' | 'structure' | 'undo' | 'redo';

export interface EditOperation {
  readonly request: string;
  readonly revision: string;
  readonly action: EditOperationAction;
  readonly expandTable?: boolean;
  readonly inferNumberFormat?: boolean;
  readonly entryAnchor?: EditAddress;
  readonly changes?: readonly EditChange[];
  readonly rule?: EditRule;
  readonly structure?: EditStructure;
}

export interface TextSaveOptions {
  readonly encoding: string;
  readonly protect: boolean;
}

/** @httpResponse EditArtifact */
export interface EditArtifact {
  readonly version: number;
  readonly key: string;
  readonly revision: string;
  readonly format: string;
  readonly name: string;
  readonly bytes: number;
  readonly sha256: string;
  readonly encoding?: string;
  readonly delimiter?: number;
  readonly protected: number;
  readonly changedParts?: number;
  readonly formulasPending?: number;
  readonly nativeRecalc?: boolean;
  readonly pivotCachesPending?: number;
  readonly pivotCachesAfterCalc?: number;
  readonly downloadUrl: string;
}

export interface FormulaSymbol {
  readonly name: string;
  readonly kind: 'name' | 'table';
  readonly scope?: string;
  readonly columns?: readonly string[];
  readonly ref?: string;
}

export interface ArrayOwner {
  readonly formula?: string;
  readonly range: string;
  readonly dynamic: boolean;
}

export interface TableRange {
  readonly name: string;
  readonly sheet: string;
  readonly ref: string;
  readonly totals?: number;
}

export interface GeneratedArea extends EditArea {
  readonly input: string;
  readonly pivot?: boolean;
}

/** @httpResponse PivotChoicesResponse */
export interface PivotChoicesResponse {
  readonly items: readonly PivotItem[];
  readonly more: boolean;
}

export interface PivotGroupField {
  readonly source: string;
  readonly unit: string;
  readonly start: string;
  readonly end: string;
  readonly interval?: number;
  readonly english?: boolean;
}

/** @httpResponse PivotInfo */
export interface PivotInfo {
  readonly dateFilters?: readonly PivotDateFilter[];
  readonly groupFields?: Readonly<Record<string, PivotGroupField>>;
  readonly measures?: readonly PivotMeasure[];
  readonly valuesAxis?: PivotValuesAxis;
  readonly labelFilters?: readonly PivotLabelFilter[];
  readonly dateGroups?: readonly PivotDateGroup[];
  readonly dateFields?: readonly string[];
  readonly part: string;
  readonly name: string;
  readonly sheet: string;
  readonly fields?: readonly string[];
  readonly row?: string;
  readonly data?: string;
  readonly aggregate?: string;
  readonly axes?: PivotAxes;
  readonly sourceSheet?: string;
  readonly sourceRef?: string;
  readonly areas?: readonly EditArea[];
  readonly calculatedItems?: readonly PivotCalculatedItem[];
  readonly valueFilters?: readonly PivotValueFilter[];
  readonly refreshReason?: string;
}

export interface EditSheetMeta {
  readonly name: string;
  readonly firstRow: number;
  readonly firstCol: number;
  readonly rows: number;
  readonly cols: number;
  readonly readOnly?: string;
  readonly sourceRules?: boolean;
}

/** @httpResponse EditStateResponse */
export interface EditStateResponse {
  readonly tables?: readonly TableRange[];
  readonly chartSheets?: readonly string[];
  readonly pivots?: readonly PivotInfo[];
  readonly book: string;
  readonly originalBook: string;
  readonly document: EditInfo;
  readonly format: string;
  readonly sheets: readonly EditSheetMeta[];
  readonly encoding?: string;
  readonly structured?: boolean;
  readonly artifacts: readonly EditArtifact[];
}

/** @httpResponse EditReadResponse */
export interface EditReadResponse {
  readonly address: EditAddress;
  readonly ref: string;
  readonly base: EditValue;
  readonly value: EditValue;
  readonly formulaState?: string;
  readonly savedResult?: EditValue;
  readonly calculatedResult?: EditValue;
  readonly rule?: EditRule;
  readonly choices?: readonly string[];
  readonly readOnly?: string;
  readonly array?: ArrayOwner;
  readonly generatedStatus?: string;
}

export interface EditGridRuleArea extends EditArea {
  readonly list?: boolean;
}

/** @httpResponse EditGridProjectionResponse */
export interface EditGridProjectionResponse {
  readonly cells: Readonly<Record<string, Readonly<Record<string, CellData | null>>>>;
  readonly calculated?: Readonly<Record<string, Readonly<Record<string, CellData | null>>>>;
  readonly affected?: Readonly<Record<string, boolean>>;
  readonly generated?: readonly GeneratedArea[];
  readonly rules?: readonly EditGridRuleArea[];
}

/** @httpResponse EditBatchPreviewResponse */
export interface EditBatchPreviewResponse {
  readonly structure?: boolean;
  readonly changes?: readonly EditChange[];
  readonly last: EditAddress;
  readonly lastValue?: string;
  readonly rule?: EditRule;
  readonly proof?: string;
}

/** @httpResponse EditCommitResponse */
export interface EditCommitResponse {
  readonly document: EditInfo;
  readonly committed: string;
  readonly reloadGrid?: boolean;
  readonly book: string;
}

export interface ListControlArea extends MergeRange {
  readonly hidden?: boolean;
}

export interface WhatIfSpec {
  readonly mode: 'seek' | 'table';
  readonly Input: EditAddress;
  readonly Second?: EditAddress;
  readonly Target: EditAddress;
  readonly Goal?: string;
  readonly Lower?: string;
  readonly Upper?: string;
  readonly Tolerance?: string;
  readonly Values?: readonly string[];
  readonly SecondValues?: readonly string[];
}

export interface TrialPoint {
  readonly input: string;
  readonly second?: string;
  readonly result?: string;
  readonly error?: string;
}

/** @httpResponse WhatIfResult */
export interface WhatIfResult {
  readonly status: string;
  readonly iterations: number;
  readonly residual?: string;
  readonly points: readonly TrialPoint[];
}

export interface EditOpenWorkbookResponse {
  readonly opened: true;
  readonly workbookKey: string;
  readonly book: string;
  readonly fileName: string;
  readonly fileSize: number;
  readonly sheetCount?: number;
  readonly activeTab: number;
  readonly sheets: readonly SheetMeta[];
  readonly source: SourceInfo;
}
