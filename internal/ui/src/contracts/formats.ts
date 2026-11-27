/**
 * Format registry & capacity limits injected by `internal/ui/assets.go`
 * directly from `internal/formats`. Single source of truth for browser limits.
 */

export interface SourceCapabilities {
  readonly id: string;
  readonly maxRows: number;
  readonly maxColumns: number;
  readonly styled: boolean;
  readonly textAmounts: boolean;
  readonly conditionalMetadata: boolean;
  readonly catalogueMetadata: boolean;
  readonly dataRuleMetadata: boolean;
  readonly noteMetadata: boolean;
  readonly pictureMetadata: boolean;
  readonly printMetadata: boolean;
  readonly textClustering: boolean;
}

export interface RichTextLimits {
  readonly cellRuns: number;
  readonly viewRuns: number;
}

export interface ClipboardLimits {
  readonly rows: number;
  readonly columns: number;
  readonly cells: number;
  readonly textBytes: number;
  readonly htmlBytes: number;
  readonly tsvRows: number;
  readonly tsvColumns: number;
  readonly tsvCells: number;
}

export interface PrintingLimits {
  readonly outputPages: number;
  readonly outputBytes: number;
  readonly pages: number;
  readonly rows: number;
  readonly columns: number;
  readonly cells: number;
  readonly areas: number;
  readonly parts: number;
  readonly bytes: number;
  readonly requestBytes: number;
  readonly notices: number;
  readonly domPixels: number;
  readonly unitsPerPoint: number;
  readonly firstPageMin: number;
  readonly firstPageMax: number;
}

export interface RecordCardLimits {
  readonly columns: number;
  readonly bytes: number;
  readonly requestBytes: number;
  readonly textUnits: number;
}

export interface CellExplanationLimits {
  readonly bytes: number;
  readonly requestBytes: number;
}

export interface ProfileLimits {
  readonly cells: number;
  readonly requestBytes: number;
  readonly bytes: number;
  readonly bins: number;
}

export interface ClusteringLimits {
  readonly pageGroups: number;
  readonly pageVariants: number;
}

export interface CompositionLimits {
  readonly sources: number;
  readonly columns: number;
  readonly previewRows: number;
  readonly requestBytes: number;
  readonly resultBytes: number;
  readonly attachments: number;
  readonly fieldPageRows: number;
  readonly pickerRows: number;
}

export interface SelectionQuickLimits {
  readonly cells: number;
  readonly columns: number;
  readonly segments: number;
  readonly requestBytes: number;
  readonly bytes: number;
}

export interface ChartExportLimits {
  readonly types: readonly string[];
  readonly scales: readonly number[];
  readonly minWidth: number;
  readonly maxWidth: number;
  readonly minHeight: number;
  readonly maxHeight: number;
  readonly defaultWidth: number;
  readonly defaultHeight: number;
  readonly pixels: number;
  readonly dataBytes: number;
  readonly imageBytes: number;
  readonly svgBytes: number;
  readonly svgNodes: number;
  readonly packageBytes: number;
}

export interface TransposeLimits {
  readonly rows: number;
  readonly columns: number;
  readonly cells: number;
  readonly bytes: number;
  readonly pageColumns: number;
}

export interface SnapshotLimits {
  readonly entries: number;
  readonly rows: number;
  readonly series: number;
  readonly bytes: number;
  readonly storeBytes: number;
  readonly pageRows: number;
}

export interface EditLimits {
  readonly extensions: readonly string[];
  readonly cellBytes: number;
  readonly formulaBytes: number;
  readonly batchCells: number;
  readonly requestBytes: number;
}

export interface RelationExportOption {
  readonly format: string;
  readonly name: string;
  readonly compressed: boolean;
}

export interface TaskRecoveryLimits {
  readonly tasks: number;
}

export interface CatalogueLimits {
  readonly pageRows: number;
  readonly searchBytes: number;
}

export interface DataRulesLimits {
  readonly pageRows: number;
}

export interface AnalysisTreeLimits {
  readonly nodes: number;
  readonly bytes: number;
  readonly pageRows: number;
  readonly active: number;
}

export interface FileFormatsConfig {
  readonly extensions: readonly string[];
  readonly sources: Readonly<Record<string, SourceCapabilities>>;
  readonly largeDataRows: number;
  readonly queryTempBytes: number;
  readonly richText: RichTextLimits;
  readonly clipboard: ClipboardLimits;
  readonly reconcileRecordBytes: number;
  readonly printing: PrintingLimits;
  readonly record: RecordCardLimits;
  readonly cellExplanation: CellExplanationLimits;
  readonly profile: ProfileLimits;
  readonly clustering: ClusteringLimits;
  readonly composition: CompositionLimits;
  readonly selectionQuick: SelectionQuickLimits;
  readonly chartExport: ChartExportLimits;
  readonly transpose: TransposeLimits;
  readonly snapshots: SnapshotLimits;
  readonly editing: EditLimits;
  readonly relationExports: readonly RelationExportOption[];
  readonly taskRecovery: TaskRecoveryLimits;
  readonly catalogue: CatalogueLimits;
  readonly dataRules: DataRulesLimits;
  readonly analysisTree: AnalysisTreeLimits;
}

export interface FileFormatsRegistry extends FileFormatsConfig {
  readonly raw: FileFormatsConfig;
  readonly maxRows: number;
  readonly maxColumns: number;
  supports(name: string): boolean;
  source(id?: string): SourceCapabilities;
}
