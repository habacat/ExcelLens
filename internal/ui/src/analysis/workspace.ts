/**
 * Main Analysis Workspace orchestrator: coordinates source selection, scope
 * capture, builder/filter/transform configuration, async pivot execution,
 * dashboard, editable scratch sheets, recipes, text clustering, issues,
 * multi-table composition, snapshots, and draft recovery.
 */
import type {
  AnalysisAggregateKind,
  AnalysisField,
  AnalysisFieldsResponse,
  AnalysisFilterOp,
  AnalysisMetric,
  AnalysisProfileResponse,
  AnalysisQueryStatusResponse,
  AnalysisReport,
  AnalysisResult,
  AnalysisSortOrder,
  AnalysisTransformOp,
  AnalysisWorkspaceResponse,
  ComparisonMode,
  CompositionDefinition,
  CompositionNavigationResponse,
  CompositionTaskResult,
  DrillRow,
  SourceOrigin,
} from '../contracts/analysis.ts';
import type {
  ActivityHandle,
  ActivityService,
  ApiClient,
  HttpError,
  PostClient,
  SessionService,
  ToastTone,
} from '../contracts/common.ts';
import type { FileFormatsRegistry } from '../contracts/formats.ts';
import type {
  IndexStartResponse,
  IndexStatusResponse,
  WorkbookStatusResponse,
} from '../contracts/workbook.ts';
import { getActivityService } from '../core/activity.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { labelColumn } from '../core/composition-model.ts';
import { errorMessage, icon } from '../core/display-format.ts';
import {
  byId,
  queryButton,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { requireFileFormats } from '../core/formats.ts';
import { askOverlay } from '../core/overlays.ts';
import { RecoveryJournal, showRecoveryDialog } from '../core/recovery.ts';
import { SourceTasks } from '../core/source-tasks.ts';
import { PasteSpecialController } from '../grid/clipboard-panels.ts';
import { getChartExportController, revalidateChartExport } from './chart-export.ts';
import { resizeCharts } from './charts.ts';
import { AnalysisClusters } from './clusters.ts';
import { COMPARISON_MODES, isRunningComparison } from './comparisons.ts';
import { CompositionPanel } from './composition.ts';
import {
  AnalysisDashboardController,
  type DashboardFieldChoice,
  type ExecutedAnalysisQuery,
  type MutableAnalysisWorkspace,
} from './dashboard.ts';
import { AnalysisDrafts, type AnalysisDraftRecord } from './drafts.ts';
import { AnalysisIssues } from './issues.ts';
import {
  AGGREGATIONS,
  AnalysisPivotView,
  effectiveMetrics,
  formatCountNumber,
  metricLabel,
  restoreReportQuery,
  type ChartCaptureState,
  type EditableAnalysisQuery,
} from './pivot-view.ts';
import { RecipesController, type RecipeRunCompleted } from './recipes.ts';
import {
  AnalysisResultCache,
  type AnalysisResultSnapshot,
  type WorkspaceAnalysisQuery,
} from './results.ts';
import { AnalysisSettings } from './settings.ts';
import {
  AnalysisSheetsController,
  type ActiveSheetCell,
  type MutableAnalysisSheetPage,
  type SheetUndoHistory,
} from './sheets.ts';
import { SnapshotsController } from './snapshots.ts';
import {
  AnalysisSources,
  sourceScopeCaption,
  sourceScopeKey,
  type AnalysisSourceMode,
  type AnalysisSourceRecord,
  type GridSourceContextData,
} from './sources.ts';
import { ANALYSIS_TEMPLATES } from './templates.ts';
import { AnalysisTree, AnalysisTreeRenderer, type AnalysisTreeDiagnostics } from './tree.ts';

export type AnalysisTab = 'pivot' | 'dashboard' | 'sheets' | 'templates';

export interface AnalysisWorkspaceDiagnostics {
  readonly [key: string]: unknown;
  readonly opened: boolean;
  readonly tab: AnalysisTab;
  readonly source: string;
  readonly sourceMode: AnalysisSourceMode;
  readonly layout: '' | 'tree';
  readonly tree: AnalysisTreeDiagnostics;
  readonly fields: number;
  readonly fieldsReady: boolean;
  readonly fieldsLoading: boolean;
  readonly resultJob: string;
  readonly hasResult: boolean;
  readonly pendingWrites: number;
  readonly drafts: number;
  readonly activeJobs: number;
}

export interface AnalysisWorkspaceOptions {
  readonly api: ApiClient;
  readonly post?: PostClient;
  readonly session?: SessionService;
  readonly activity?: ActivityService;
  readonly formats?: FileFormatsRegistry;
  readonly getSourceContext: (mode: Exclude<AnalysisSourceMode, 'full'>) => GridSourceContextData;
  readonly exportIssues?: (jobId: string, protectCSV: boolean) => void;
  readonly exportClusters?: (jobId: string, protectCSV: boolean) => void;
  readonly onNavigate?: (sheet: string, ref: string) => void | Promise<void>;
  readonly workbookReady?: Promise<void>;
  readonly askText?: (title: string, initial?: string, hint?: string) => Promise<string | null>;
  readonly downloadBlob?: (blob: Blob, filename: string) => void;
  readonly onToast?: (message: string, tone: ToastTone) => void;
}

interface CompositionDraft {
  readonly source: string;
  readonly definition: CompositionDefinition;
}

interface SaveReportResponse {
  readonly id: string;
}

interface MaterializeResponse {
  readonly id: string;
  readonly rows?: number;
  readonly truncated?: boolean;
}

interface LoadFieldsOptions {
  readonly mode?: AnalysisSourceMode | '';
  readonly prepared?: CompositionTaskResult | null;
  readonly guard?: () => boolean;
}

interface AnalysisWorkspaceInternalState {
  fingerprint: string;
  book: string;
  currentBook?: string;
  sourceFormat: string;
  opened: boolean;
  openSerial: number;
  openActivity: ActivityHandle | null;
  workspaceRefreshSerial: number;
  tab: AnalysisTab;
  workspace: MutableAnalysisWorkspace | null;
  source: string;
  headerRow: number;
  fields: AnalysisField[];
  preview: DrillRow[];
  fieldsReady: boolean;
  fieldsLoading: boolean;
  loadedFieldsKey: string;
  fieldsController: AbortController | null;
  query: EditableAnalysisQuery | null;
  compositionDraft: CompositionDraft | null;
  jobID: string;
  jobSeq: number;
  activeResult: AnalysisResult | null;
  activeResultQuery: EditableAnalysisQuery | null;
  activeResultFields: AnalysisField[];
  chartBusy: boolean;
  chartCapture: ChartCaptureState | null;
  pivotLimit: number;
  loadedSheet: string | null;
  sheetPage: number;
  sheetCell: ActiveSheetCell | null;
  reportCache: AnalysisResultCache;
  reportOpenSerial: number;
  reportPending: number;
  templateCategory: string;
  templateText: string;
  dashboardObserver: IntersectionObserver | null;
  drillQuery: EditableAnalysisQuery | null;
  drillJob: string;
  drillKeys: string[];
  drillColumn: string;
  drillColumnSelected: boolean | undefined;
  drillOffset: number;
  drillAbort: AbortController | null;
  abortIndex: number;
  activeReportID: string;
  sheetRevisions: Map<string, number>;
  undoHistory: Map<string, SheetUndoHistory>;
  gridData: MutableAnalysisSheetPage | null;
  profileController: AbortController | null;
  sheetLoadSeq: number;
  sheetAbortController: AbortController | null;
}

const VALID_TONES = new Set<string>(['info', 'success', 'warning', 'error']);
const VALID_TABS = new Set<string>(['pivot', 'dashboard', 'sheets', 'templates']);
const VALID_ZONES = new Set<string>(['rows', 'columns', 'values']);
const VALID_SOURCE_MODES = new Set<string>(['full', 'filtered', 'visible', 'selection']);
const VALID_AGGREGATES = new Set<string>(['sum', 'count', 'average', 'min', 'max', 'distinct']);
const VALID_SORTS = new Set<string>(['', 'value_desc', 'value_asc', 'label_asc', 'label_desc']);
const VALID_COMPARISON_MODES = new Set<string>([
  '',
  'percent_grand',
  'percent_row',
  'percent_column',
  'running_total',
  'running_percent',
]);

const FILTER_OPERATIONS: readonly (readonly [AnalysisFilterOp, string])[] = Object.freeze([
  ['eq', '等于'] as const,
  ['neq', '不等于'] as const,
  ['contains', '包含'] as const,
  ['notcontains', '不包含'] as const,
  ['startswith', '开头是'] as const,
  ['endswith', '结尾是'] as const,
  ['gt', '大于'] as const,
  ['gte', '不小于'] as const,
  ['lt', '小于'] as const,
  ['lte', '不大于'] as const,
  ['between', '在区间内'] as const,
  ['in', '多值匹配'] as const,
  ['blank', '为空'] as const,
  ['notblank', '非空'] as const,
]);
const VALID_FILTER_OPS = new Set<string>(FILTER_OPERATIONS.map((x) => x[0]));

const TRANSFORM_OPERATIONS: readonly (readonly [AnalysisTransformOp, string])[] = Object.freeze([
  ['date', '转日期'] as const,
  ['time', '转时间'] as const,
  ['datetime', '转日期时间'] as const,
  ['text', '转文本'] as const,
  ['number', '转数值'] as const,
  ['trim', '去空格'] as const,
  ['upper', '大写'] as const,
  ['lower', '小写'] as const,
  ['year', '提取年份'] as const,
  ['month', '提取月份'] as const,
  ['yearmonth', '提取年月'] as const,
  ['abs', '绝对值'] as const,
  ['multiply', '乘常数'] as const,
  ['divide', '除常数'] as const,
  ['prefix', '添加前缀'] as const,
  ['suffix', '添加后缀'] as const,
  ['replace', '替换文本'] as const,
  ['length', '文本长度'] as const,
]);
const VALID_TRANSFORM_OPS = new Set<string>([
  ...TRANSFORM_OPERATIONS.map((x) => x[0]),
  'map_text',
]);

const NUMERIC_TRANSFORM_OPS = new Set<AnalysisTransformOp>([
  'number',
  'abs',
  'multiply',
  'divide',
  'length',
]);
const TEMPORAL_TRANSFORM_OPS = new Set<AnalysisTransformOp>(['date', 'time', 'datetime']);
const ARGLESS_TRANSFORM_OPS = new Set<AnalysisTransformOp>([
  'text',
  'number',
  'trim',
  'upper',
  'lower',
  'year',
  'month',
  'yearmonth',
  'abs',
  'length',
  'map_text',
]);

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function cloneJson<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function makeAbortError(message: string): Error {
  const err = new Error(message);
  err.name = 'AbortError';
  return err;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function getHttpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isAnalysisFieldArray(value: unknown): value is readonly AnalysisField[] {
  return (
    Array.isArray(value) &&
    value.every(
      (item) =>
        isPlainRecord(item) && typeof item.id === 'string' && typeof item.name === 'string',
    )
  );
}

function isWorkspaceAnalysisQuery(value: unknown): value is WorkspaceAnalysisQuery {
  return isPlainRecord(value) && typeof value.source === 'string';
}

function parseAggregateKind(raw: string): AnalysisAggregateKind {
  return VALID_AGGREGATES.has(raw) ? (raw as AnalysisAggregateKind) : 'count';
}

function parseSortOrder(raw: string): AnalysisSortOrder {
  return VALID_SORTS.has(raw) ? (raw as AnalysisSortOrder) : 'value_desc';
}

function parseComparisonMode(raw: string): ComparisonMode {
  return VALID_COMPARISON_MODES.has(raw) ? (raw as ComparisonMode) : '';
}

function parseFilterOp(raw: string): AnalysisFilterOp {
  return VALID_FILTER_OPS.has(raw) ? (raw as AnalysisFilterOp) : 'eq';
}

function parseTransformOp(raw: string): AnalysisTransformOp {
  return VALID_TRANSFORM_OPS.has(raw) ? (raw as AnalysisTransformOp) : 'trim';
}

function parseSourceMode(raw: string): AnalysisSourceMode {
  return VALID_SOURCE_MODES.has(raw) ? (raw as AnalysisSourceMode) : 'full';
}

function downloadBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

export class AnalysisWorkspace {
  private readonly root: HTMLElement;
  private readonly api: ApiClient;
  private readonly post: PostClient;
  private readonly session?: SessionService;
  private readonly activityOverride?: ActivityService;
  private readonly formats: FileFormatsRegistry;
  private readonly onNavigate?: (sheet: string, ref: string) => void | Promise<void>;
  private readonly askTextOverride?: (
    title: string,
    initial?: string,
    hint?: string,
  ) => Promise<string | null>;
  private readonly downloadBlobFn: (blob: Blob, filename: string) => void;
  private readonly onToastOverride?: (message: string, tone: ToastTone) => void;

  public readonly state: AnalysisWorkspaceInternalState;
  private readonly drafts: AnalysisDrafts;
  private readonly compositionTasks: SourceTasks;
  public readonly sourceRows: AnalysisSources;
  private readonly displaySettings: AnalysisSettings;
  private readonly analysisJobs: AsyncJobs<AnalysisResult, AnalysisQueryStatusResponse>;
  private readonly treeRenderer: AnalysisTreeRenderer;
  public readonly tree: AnalysisTree;
  private readonly issuePanel: AnalysisIssues;
  public readonly clusters: AnalysisClusters;
  private readonly plans: RecipesController;
  private readonly sheetsController: AnalysisSheetsController;
  private readonly pasteSpecial: PasteSpecialController;
  public readonly pivotView: AnalysisPivotView;
  public readonly dashboardController: AnalysisDashboardController;

  public readonly composition: CompositionPanel;
  public readonly snapshots: SnapshotsController;
  public readonly recovery: RecoveryJournal<AnalysisDraftRecord>;

  private toastTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(root: HTMLElement, options: AnalysisWorkspaceOptions) {
    this.root = root;
    this.api = options.api;
    this.post =
      options.post ??
      (<T = unknown>(url: string, payload: unknown): Promise<T> =>
        this.api<T>(url, { method: 'POST', body: JSON.stringify(payload) }));
    this.session = options.session;
    this.activityOverride = options.activity;
    this.formats = options.formats ?? requireFileFormats();
    this.onNavigate = options.onNavigate;
    this.askTextOverride = options.askText;
    this.downloadBlobFn = options.downloadBlob ?? downloadBlob;
    this.onToastOverride = options.onToast;

    // Ensure the shared ChartExportController uses the configured limits & activity service.
    getChartExportController(this.formats.chartExport, this.currentActivity() ?? undefined);

    this.state = {
      fingerprint: '',
      book: '',
      sourceFormat: '',
      opened: false,
      openSerial: 0,
      openActivity: null,
      workspaceRefreshSerial: 0,
      tab: 'pivot',
      workspace: null,
      source: '',
      headerRow: 1,
      fields: [],
      preview: [],
      fieldsReady: false,
      fieldsLoading: false,
      loadedFieldsKey: '',
      fieldsController: null,
      query: null,
      compositionDraft: null,
      jobID: '',
      jobSeq: 0,
      activeResult: null,
      activeResultQuery: null,
      activeResultFields: [],
      chartBusy: false,
      chartCapture: null,
      pivotLimit: 150,
      loadedSheet: null,
      sheetPage: 0,
      sheetCell: null,
      reportCache: new AnalysisResultCache(),
      reportOpenSerial: 0,
      reportPending: 0,
      templateCategory: '全部',
      templateText: '',
      dashboardObserver: null,
      drillQuery: null,
      drillJob: '',
      drillKeys: [],
      drillColumn: '',
      drillColumnSelected: undefined,
      drillOffset: 0,
      drillAbort: null,
      abortIndex: 0,
      activeReportID: '',
      sheetRevisions: new Map(),
      undoHistory: new Map(),
      gridData: null,
      profileController: null,
      sheetLoadSeq: 0,
      sheetAbortController: null,
    };

    const toastFn = (message: string, tone: ToastTone = 'info'): void => this.toast(message, tone);
    const askTextFn = (title: string, initial = '', hint = '请输入名称'): Promise<string | null> =>
      this.askText(title, initial, hint);

    this.drafts = new AnalysisDrafts();
    this.compositionTasks = new SourceTasks({
      api: this.api,
      context: () => ({
        bookKey: this.state.fingerprint,
        book: this.state.book,
        opened: this.state.opened,
      }),
      activity: this.currentActivity() ?? undefined,
    });
    this.sourceRows = new AnalysisSources({
      api: this.api,
      tasks: this.compositionTasks,
      context: () => ({
        bookKey: this.state.fingerprint,
        book: this.state.book,
        opened: this.state.opened,
      }),
      grid: (mode) => options.getSourceContext(mode),
    });
    this.composition = new CompositionPanel({
      api: this.api,
      toast: toastFn,
      onUse: (data, current) => this.adoptComposition(data, current),
      limits: this.formats.composition,
      formats: this.formats,
      activity: this.currentActivity() ?? undefined,
      context: () => ({
        bookKey: this.state.fingerprint,
        book: this.state.book,
        opened: this.state.opened,
        source: this.state.source,
        sourceScope: this.state.query?.sourceScope,
        composition: this.state.query?.composition,
        keepId: this.sourceRows.binding?.composition ? this.sourceRows.binding.id : '',
      }),
    });
    this.displaySettings = new AnalysisSettings({ api: this.api, toast: toastFn });
    this.snapshots = new SnapshotsController({
      api: this.api,
      toast: toastFn,
      limits: this.formats.snapshots,
    });

    this.sheetsController = new AnalysisSheetsController({
      state: this.state,
      drafts: this.drafts,
      api: this.api,
      post: this.post,
      toast: toastFn,
      askText: askTextFn,
      setText: (id, text) => this.setText(id, text),
      fmtNum: formatCountNumber,
      setTab: (tab) => this.setTab(tab),
      refreshWorkspace: () => this.refreshWorkspace(),
      open: () => this.open(),
    });
    this.pasteSpecial = new PasteSpecialController({
      capture: (previous) => this.sheetsController.capturePasteTarget(previous),
      toast: toastFn,
      refresh: (previous) => this.sheetsController.refreshPasteTarget(previous),
      commit: async (context, changes) => {
        await this.sheetsController.commitBatch(context.id, changes, {
          refresh: true,
          guard: () => context.current(),
        });
      },
    });
    this.sheetsController.setPasteSpecial(this.pasteSpecial);

    this.analysisJobs = new AsyncJobs<AnalysisResult, AnalysisQueryStatusResponse>({
      api: this.api,
      maxPolls: 0,
      slot: 'analysisActivity',
      identity: () =>
        JSON.stringify([this.state.fingerprint, this.state.book, this.state.opened]),
      requestIdentity: () => this.state.fingerprint,
      sleep,
      activity: this.currentActivity() ?? undefined,
      onBusy: (busy) => this.setAnalysisBusy(busy),
      onProgress: (status) => this.showAnalysisProgress(status),
    });

    this.treeRenderer = new AnalysisTreeRenderer();
    this.tree = new AnalysisTree({
      api: this.api,
      context: () => ({
        bookKey: this.state.fingerprint,
        book: this.state.book,
        opened: this.state.opened,
      }),
      limits: this.formats.analysisTree,
      onMessage: (message) => this.toast(message, 'warning'),
      onChange: (event) => {
        if (
          this.state.opened &&
          this.state.activeResult?.tree &&
          this.tree.root?.job === this.state.jobID
        ) {
          if (event.type === 'progress') this.treeRenderer.progress(event.node);
          else this.pivotView.renderPivot();
        }
      },
    });

    this.plans = new RecipesController({
      api: this.api,
      toast: toastFn,
      adopt: (completed, preview, current) => this.adoptAnalysisPlan(completed, preview, current),
      context: () => ({
        bookKey: this.state.fingerprint,
        opened: this.state.opened,
        intent: this.state.reportOpenSerial,
        source: this.state.source,
        headerRow: this.state.headerRow,
        sourceId: this.sourceRows.binding?.id || '',
        sourceScope: this.state.query?.sourceScope,
        resultJob:
          this.analysisJobs.main || this.state.activeResultQuery?.composition
            ? ''
            : this.state.jobID,
        sources: [...requireSelect('analysisSourceSelect').options]
          .filter((option) => !option.value.startsWith('append:'))
          .map((option) => ({ value: option.value, label: option.textContent })),
      }),
      capture: () => {
        if (!this.state.activeResult || !this.state.jobID || this.analysisJobs.main) {
          throw new Error('请先完成一次分析，再保存方案');
        }
        return {
          jobId: this.state.jobID,
          format: requireSelect('analysisExportFormat').value,
        };
      },
    });

    this.issuePanel = new AnalysisIssues({
      api: this.api,
      identity: () => this.state.fingerprint,
      toast: toastFn,
      askName: askTextFn,
      startExport: (jobId, protectCSV) => {
        options.exportIssues?.(jobId, protectCSV);
      },
      onMaterialized: async (data) => {
        await this.refreshWorkspace();
        this.state.loadedSheet = data.id;
        this.state.sheetPage = 0;
        void this.setTab('sheets');
      },
      navigate: async (q, issue, ref) => {
        if (q.composition) {
          await this.navigateComposition(issue, issue.sourceColumn, this.issuePanel.snapshot?.job);
        } else if (q.source.startsWith('wb:')) {
          this.close();
          this.navigateWorkbook(q.source.slice(3), ref);
        } else {
          const id = q.source.slice(8);
          this.state.loadedSheet = id;
          this.state.sheetPage = Math.floor(
            (issue.row - 1) / this.sheetsController.sheetPageSize(id),
          );
          void this.setTab('sheets');
          await this.sheetsController.loadSheet(id, this.state.sheetPage);
          requireEl('analysisEditableGrid')
            .querySelector<HTMLTextAreaElement>(
              `textarea[data-row="${issue.row - 1}"][data-col="${Math.max(0, issue.sourceColumn - 1)}"]`,
            )
            ?.focus();
        }
      },
    });

    this.clusters = new AnalysisClusters({
      api: this.api,
      toast: toastFn,
      scopeKey: sourceScopeKey,
      limits: this.formats.clustering,
      context: () => ({
        bookKey: this.state.fingerprint,
        book: this.state.book,
        opened: this.state.opened,
        ready: this.state.fieldsReady && !this.state.fieldsLoading,
        source: this.state.source,
        headerRow: this.state.headerRow,
        sourceScope: this.state.query?.sourceScope,
        sourceId: this.sourceRows.binding?.id || '',
      }),
      onTransform: (step, guard) => {
        if (!guard()) throw new Error('分析来源已变化，请重新核对');
        const q = this.state.query;
        if (!q) return;
        if (q.transforms.length >= 8) {
          throw new Error('最多8个计算列，请先移除部分计算列');
        }
        q.transforms.push(cloneJson(step));
        this.renderFields();
        this.renderTransforms();
      },
      startExport: (jobId, protectCSV) => {
        options.exportClusters?.(jobId, protectCSV);
      },
      navigate: async (data, guard) => {
        if (!guard()) return false;
        if (data.source.startsWith('wb:')) {
          this.close();
          this.navigateWorkbook(data.source.slice(3), data.ref);
          return true;
        }
        const id = data.source.slice(8);
        this.state.loadedSheet = id;
        this.state.sheetPage = Math.floor((data.row - 1) / this.sheetsController.sheetPageSize(id));
        void this.setTab('sheets');
        await this.sheetsController.loadSheet(id, this.state.sheetPage);
        if (!guard()) return false;
        requireEl('analysisEditableGrid')
          .querySelector<HTMLTextAreaElement>(
            `textarea[data-row="${data.row - 1}"][data-col="${data.column - 1}"]`,
          )
          ?.focus();
        return true;
      },
    });

    this.recovery = new RecoveryJournal<AnalysisDraftRecord>({
      kind: 'analysis',
      api: this.api,
      session: this.session,
      onRestore: (records) => {
        if (!this.root.isConnected) return;
        this.drafts.restore(records);
        this.sheetsController.updateSaveStatus();
        if (this.state.opened && this.state.loadedSheet) {
          void this.sheetsController
            .loadSheet(this.state.loadedSheet, this.state.sheetPage)
            .catch((error: unknown) => this.toast(errorMessage(error), 'error'));
        }
      },
      onStatus: (status) => {
        if (!this.root.isConnected) return;
        const button = requireButton('analysisDraftRecovery');
        button.title = status.error || '找回未确认的分析输入；核对后点击重试保存';
        button.classList.toggle('danger', Boolean(status.error));
      },
    });
    this.drafts.onChange = (key, data): void => this.recovery.set(key, data);
    requireButton('analysisDraftRecovery').addEventListener('click', () => {
      showRecoveryDialog(this.recovery, {
        title: '恢复分析草稿',
        explanation:
          '草稿按原文件和编辑表匹配。载入后核对当前已保存值，再点击“重试保存”；本页同格草稿优先保留。',
      });
    });

    this.pivotView = new AnalysisPivotView({
      state: this.state,
      api: this.api,
      toast: toastFn,
      setText: (id, text) => this.setText(id, text),
      tree: this.tree,
      treeRenderer: this.treeRenderer,
      issuePanel: this.issuePanel,
      snapshots: this.snapshots,
      navigateComposition: (record, column, job) => this.navigateComposition(record, column, job),
      navigateSheet: (id, row, col) => this.navigateSheet(id, row, col),
      navigateWorkbook: (sheet, ref) => this.navigateWorkbook(sheet, ref),
      close: () => this.close(),
    });

    this.dashboardController = new AnalysisDashboardController({
      root: this.root,
      state: this.state,
      templates: ANALYSIS_TEMPLATES,
      post: this.post,
      toast: toastFn,
      askText: askTextFn,
      refreshWorkspace: () => this.refreshWorkspace(),
      openReport: (report) => this.openReport(report),
      executeQuery: (q) => this.executeQuery(q),
      effectiveMetrics,
      activateResult: (snapshot) => this.activateResult(snapshot),
      renderResult: (result, q) => this.pivotView.renderResult(result, q),
      drill: (keys, colKey, offset, columnSelected) =>
        this.pivotView.drill(keys, colKey, offset, columnSelected),
      openResultImage: (snapshot, canvas, expectedResult, mode, index, title, current) =>
        this.pivotView.openResultImage(
          snapshot,
          canvas,
          expectedResult,
          mode,
          index,
          title,
          current,
        ),
      allFields: () => this.allFields(),
      setTab: (tab) => this.setTab(tab),
      queryBase: (source) => this.queryBase(source),
      renderFields: () => this.renderFields(),
      renderBuilder: () => this.renderBuilder(),
      run: () => this.run(),
    });

    this.bind();

    const launchParams = new URLSearchParams(window.location.search);
    const launchSheet = launchParams.get('analysisSheet');
    if (launchSheet !== null) {
      void Promise.resolve(options.workbookReady)
        .then(() =>
          this.navigateSheet(
            launchSheet,
            Number(launchParams.get('sourceRow')),
            Number(launchParams.get('sourceCol')),
          ),
        )
        .catch((error: unknown) => this.toast(errorMessage(error), 'error'));
    }
  }

  private isOpened(): boolean {
    return this.state.opened;
  }

  private currentActivity(): ActivityService | null {
    return this.activityOverride ?? getActivityService();
  }

  private navigateWorkbook(sheet: string, ref: string): void {
    if (this.onNavigate) {
      void this.onNavigate(sheet, ref);
      return;
    }
    window.dispatchEvent(
      new CustomEvent('excellens:analysis-navigate', { detail: { sheet, ref } }),
    );
  }

  private toast(message: string, tone: ToastTone = 'info'): void {
    const resolvedTone: ToastTone = VALID_TONES.has(tone) ? tone : 'info';
    if (this.onToastOverride) {
      this.onToastOverride(message, resolvedTone);
      return;
    }
    const el = requireEl('analysisToast');
    el.dataset.tone = resolvedTone;
    el.setAttribute('role', resolvedTone === 'error' ? 'alert' : 'status');
    el.textContent = message;
    el.hidden = false;
    if (this.toastTimer !== undefined) clearTimeout(this.toastTimer);
    this.toastTimer = setTimeout(() => {
      el.hidden = true;
    }, 4800);
  }

  private setText(id: string, text: string): void {
    const el = byId(id);
    if (el) el.textContent = text;
  }

  private askText(title: string, initial = '', hint = '请输入名称'): Promise<string | null> {
    if (this.askTextOverride) return this.askTextOverride(title, initial, hint);
    return askOverlay({ title, value: initial, placeholder: hint, maxLength: 80 }).then((res) =>
      res && typeof res.value === 'string' ? res.value : null,
    );
  }

  private fieldName(id: string | undefined): string {
    if (!id) return '记录数';
    const f =
      this.state.query?.transforms.find((x) => x.id === id) ??
      this.state.fields.find((x) => x.id === id);
    return f?.name || id;
  }

  private metricCaption(m: AnalysisMetric): string {
    return metricLabel(m, (id) => this.fieldName(id));
  }

  private queryBase(source: string): EditableAnalysisQuery {
    return {
      source,
      headerRow: this.state.headerRow,
      rows: [],
      columns: [],
      value: '',
      aggregate: 'count',
      metrics: [],
      filters: [],
      transforms: [],
      sort: 'value_desc',
      top: 30,
      precision: 2,
      chart: 'bar',
      chartMetric: 0,
      layout: '',
    };
  }

  private appendCompositionSources(select: HTMLSelectElement): void {
    for (const group of select.querySelectorAll('optgroup[data-composition]')) {
      group.remove();
    }
    const choices = new Map<string, CompositionDefinition>();
    for (const report of this.state.workspace?.reports ?? []) {
      if (report.query.composition) choices.set(report.query.source, report.query.composition);
    }
    if (this.state.compositionDraft) {
      choices.set(this.state.compositionDraft.source, this.state.compositionDraft.definition);
    }
    if (this.state.query?.composition) {
      choices.set(this.state.query.source, this.state.query.composition);
    }
    if (!choices.size) return;
    const group = document.createElement('optgroup');
    group.label = '联合来源';
    group.setAttribute('data-composition', '');
    for (const [source, definition] of choices) {
      group.append(new Option(definition.name, source));
    }
    select.append(group);
  }

  private async adoptComposition(
    data: CompositionTaskResult,
    guard: () => boolean,
  ): Promise<boolean> {
    if (!guard() || !data.definition) return false;
    const book = this.state.fingerprint;
    const pathBook = this.state.book;
    const previousInfo = requireEl('analysisSourceInfo').textContent;
    const old = {
      source: this.state.source,
      headerRow: this.state.headerRow,
      query: this.state.query,
      fields: this.state.fields,
      preview: this.state.preview,
      loadedFieldsKey: this.state.loadedFieldsKey,
      activeReportID: this.state.activeReportID,
    };
    ++this.state.reportOpenSerial;
    this.state.source = data.source;
    this.state.headerRow = 0;
    this.state.query = {
      ...this.queryBase(data.source),
      composition: cloneJson(data.definition),
      headerRow: 0,
    };
    const loaded = await this.loadFields({ prepared: data, guard });
    if (book !== this.state.fingerprint || pathBook !== this.state.book || !this.state.opened) {
      return false;
    }
    if (!loaded) {
      const reason = requireEl('analysisSourceInfo').textContent;
      this.state.source = old.source;
      this.state.headerRow = old.headerRow;
      this.state.query = old.query;
      this.state.fields = old.fields;
      this.state.preview = old.preview;
      this.state.loadedFieldsKey = old.loadedFieldsKey;
      this.state.activeReportID = old.activeReportID;
      this.state.fieldsReady =
        Boolean(old.loadedFieldsKey) && old.loadedFieldsKey === this.fieldsKey();
      this.renderSourceScope();
      this.renderFields();
      this.renderBuilder();
      requireSelect('analysisSourceSelect').value = this.state.source;
      requireInput('analysisHeaderRow').value = String(this.state.headerRow);
      this.setText('analysisSourceInfo', previousInfo);
      this.setAnalysisBusy(Boolean(this.analysisJobs.main));
      if (guard()) throw new Error(reason || '读取字段失败，请重试');
      return false;
    }
    this.analysisJobs.cancelMain();
    this.state.activeReportID = '';
    if (this.state.query.composition) {
      this.state.compositionDraft = {
        source: this.state.source,
        definition: cloneJson(this.state.query.composition),
      };
    }
    const sourceSelect = requireSelect('analysisSourceSelect');
    this.appendCompositionSources(sourceSelect);
    sourceSelect.value = this.state.source;
    requireInput('analysisHeaderRow').value = '0';
    void this.setTab('pivot');
    this.setAnalysisBusy(false);
    return true;
  }

  private currentQuery(): EditableAnalysisQuery {
    const base = this.state.query ?? this.queryBase(this.state.source);
    const q = cloneJson(base);
    q.source = this.state.source;
    q.headerRow = this.state.headerRow;
    q.chart = requireSelect('analysisChartSelect').value;
    return q;
  }

  private syncFirstMetric(): void {
    const q = this.state.query;
    const first = q?.metrics[0];
    if (q && first) {
      q.value = first.field || '';
      q.aggregate = first.aggregate;
    }
  }

  private addMetric(field: string): void {
    const q = this.state.query;
    if (!q) return;
    if (!q.metrics.length && q.value) {
      q.metrics.push({ field: q.value, aggregate: q.aggregate, name: '' });
    }
    if (q.metrics.length >= 6) {
      this.toast('一次分析最多6个指标', 'warning');
      return;
    }
    const fields = this.allFields();
    const choice = field || fields.find((f) => f.numeric)?.id || '';
    q.metrics.push({
      field: choice,
      aggregate: fields.find((f) => f.id === choice)?.numeric ? 'sum' : 'count',
      name: '',
    });
    this.syncFirstMetric();
    this.renderBuilder();
  }

  public renderMetricEditor(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisMetricEditor');
    list.replaceChildren();
    const metrics: AnalysisMetric[] = q.metrics.length
      ? q.metrics
      : [{ field: q.value || '', aggregate: q.aggregate, name: '' }];
    const adopt = (): void => {
      if (!q.metrics.length) q.metrics = metrics;
    };
    metrics.forEach((metric, i) => {
      const row = document.createElement('div');
      row.className = 'analysis-metric-row';
      const field = this.fieldSelect(metric.field || '', '记录数');
      field.title = '参与统计的字段';
      field.onchange = (): void => {
        adopt();
        metric.field = field.value;
        if (i === 0) this.syncFirstMetric();
        this.renderBuilder();
      };
      const agg = document.createElement('select');
      agg.className = 'analysis-control';
      for (const [value, label] of AGGREGATIONS) agg.append(new Option(label, value));
      agg.value = metric.aggregate;
      agg.onchange = (): void => {
        adopt();
        metric.aggregate = parseAggregateKind(agg.value);
        if (i === 0) this.syncFirstMetric();
        this.renderBuilder();
      };
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.textContent = '×';
      remove.title = '移除指标';
      remove.onclick = (): void => {
        adopt();
        q.metrics.splice(i, 1);
        if (!q.metrics.length) {
          q.value = '';
          q.aggregate = 'count';
        } else {
          this.syncFirstMetric();
        }
        this.renderBuilder();
      };

      const presentation = document.createElement('div');
      presentation.className = 'analysis-metric-comparison';
      const compareLabel = document.createElement('label');
      compareLabel.textContent = '结果显示';
      const compare = document.createElement('select');
      compare.className = 'analysis-control analysis-comparison-mode';
      compare.setAttribute('aria-label', `指标 ${i + 1} 结果显示`);
      for (const [value, label] of COMPARISON_MODES) compare.append(new Option(label, value));
      compare.value = metric.comparison || '';
      compare.onchange = (): void => {
        adopt();
        metric.comparison = parseComparisonMode(compare.value);
        if (!isRunningComparison(metric)) delete metric.comparisonAxis;
        this.syncFirstMetric();
        this.renderBuilder();
      };
      compareLabel.append(compare);
      presentation.append(compareLabel);
      if (isRunningComparison(metric)) {
        const axisLabel = document.createElement('label');
        axisLabel.textContent = '累计方向';
        const axis = document.createElement('select');
        axis.className = 'analysis-control analysis-comparison-axis';
        axis.setAttribute('aria-label', `指标 ${i + 1} 累计方向`);
        axis.append(new Option('沿行（按分类排序）', 'rows'), new Option('沿列（按列标签排序）', 'columns'));
        axis.value = metric.comparisonAxis || 'rows';
        axis.onchange = (): void => {
          adopt();
          metric.comparisonAxis = axis.value === 'columns' ? 'columns' : 'rows';
        };
        axisLabel.append(axis);
        presentation.append(axisLabel);
      }
      row.append(field, agg, remove, presentation);
      list.append(row);
    });
  }

  private setTab(tab: AnalysisTab): Promise<void> | undefined {
    if (tab !== 'pivot') ++this.state.reportOpenSerial;
    this.state.tab = tab;
    revalidateChartExport();
    this.tree.setBlocked(
      tab !== 'pivot' ||
        Boolean(this.analysisJobs.main) ||
        this.state.reportPending === this.state.reportOpenSerial,
    );
    this.root.querySelectorAll<HTMLElement>('[data-analysis-tab]').forEach((btn) => {
      btn.classList.toggle('is-active', btn.dataset.analysisTab === tab);
    });
    this.root.querySelectorAll<HTMLElement>('[data-analysis-panel]').forEach((el) => {
      el.hidden = el.dataset.analysisPanel !== tab;
    });
    requireEl('analysisBuilder').hidden = tab !== 'pivot';
    if (tab === 'pivot') {
      setTimeout(() => this.pivotView.renderChart(), 40);
    }
    if (tab === 'templates') this.dashboardController.renderTemplates();
    if (tab === 'sheets') return this.sheetsController.renderSheets();
    this.state.sheetLoadSeq++;
    this.state.sheetAbortController?.abort();
    this.state.sheetAbortController = null;
    if (tab === 'dashboard') {
      this.dashboardController.renderDashboard();
    } else {
      this.state.dashboardObserver?.disconnect();
      this.state.dashboardObserver = null;
    }
    return undefined;
  }

  async open(): Promise<void> {
    const ticket = ++this.state.openSerial;
    this.state.openActivity?.end();
    const activity = this.currentActivity()?.begin('正在打开分析工作区') ?? null;
    this.state.openActivity = activity;
    this.state.fieldsReady = false;
    this.setAnalysisBusy(Boolean(this.analysisJobs.main));
    try {
      const status = await this.api<WorkbookStatusResponse>('/api/status');
      if (!status.opened) {
        this.toast('请先打开原始 Excel 工作簿', 'warning');
        return;
      }
      if (ticket !== this.state.openSerial) return;
      this.state.opened = true;
      this.root.hidden = false;
      document.body.classList.add('analysis-mode');
      void this.displaySettings.load();
      const workspace = await this.api<AnalysisWorkspaceResponse>('/api/analysis/workspace');
      if (ticket !== this.state.openSerial || !this.isOpened()) return;
      if (workspace.fingerprint !== status.workbookKey) {
        throw new Error('工作簿已切换，请重新打开分析');
      }
      this.state.workspace = { ...workspace };
      if (this.state.fingerprint && this.state.workspace.fingerprint !== this.state.fingerprint) {
        this.state.query = null;
        this.state.source = '';
        this.state.compositionDraft = null;
        this.state.loadedSheet = null;
        this.state.activeResult = null;
        this.state.activeResultQuery = null;
        this.state.activeReportID = '';
        this.state.undoHistory.clear();
        this.state.sheetRevisions.clear();
        this.state.gridData = null;
        this.analysisJobs.reset();
        this.state.reportCache.clear();
        this.state.jobID = '';
        this.state.jobSeq++;
        this.pivotView.clearResultDisplay();
      }
      this.state.fingerprint = this.state.workspace.fingerprint || '';
      this.state.book = status.book || '';
      this.state.sourceFormat = status.source?.format || '';
      const original = this.state.workspace.sourceSheets;
      const select = requireSelect('analysisSourceSelect');
      select.replaceChildren();
      const originalGroup = document.createElement('optgroup');
      originalGroup.label = '原始 Excel · 只读';
      for (const s of original) originalGroup.append(new Option(s.name, `wb:${s.name}`));
      select.append(originalGroup);
      if (this.state.workspace.sheets.length) {
        const editable = document.createElement('optgroup');
        editable.label = '分析工作表 · 可编辑';
        for (const s of this.state.workspace.sheets) {
          editable.append(new Option(s.name, `scratch:${s.id}`));
        }
        select.append(editable);
      }
      this.appendCompositionSources(select);
      const oldSource = this.state.source;
      const fallbackIdx = Math.min(status.activeTab ?? 0, Math.max(0, select.options.length - 1));
      this.state.source =
        oldSource && Array.from(select.options).some((x) => x.value === oldSource)
          ? oldSource
          : (select.options[fallbackIdx]?.value ?? '');
      select.value = this.state.source;
      if (this.state.query?.source !== this.state.source) {
        this.state.query = this.queryBase(this.state.source);
      }
      this.state.headerRow = this.state.query.composition ? 0 : this.state.query.headerRow || 1;
      requireInput('analysisHeaderRow').value = String(this.state.headerRow);
      this.setText('analysisSourceCaption', this.state.workspace.workbook || status.fileName || '');
      void this.setTab(this.state.tab);
      await this.loadFields();
      if (this.state.activeResult && this.isOpened()) {
        this.pivotView.bindTreeResult();
        this.pivotView.renderPivot();
      }
      if (this.state.tab === 'sheets') void this.sheetsController.renderSheets();
    } catch (err: unknown) {
      if (ticket === this.state.openSerial) this.toast(errorMessage(err), 'error');
    } finally {
      activity?.end();
    }
  }

  private invalidateSourceRequests(): void {
    ++this.state.abortIndex;
    this.state.fieldsController?.abort();
    this.state.profileController?.abort();
    this.state.drillAbort?.abort();
    this.clusters.reset();
    this.sourceRows.cancel();
    this.state.fieldsLoading = false;
  }

  close(): void {
    if (this.toastTimer !== undefined) {
      clearTimeout(this.toastTimer);
      this.toastTimer = undefined;
    }
    this.state.dashboardObserver?.disconnect();
    this.state.dashboardObserver = null;
    this.state.openActivity?.end();
    ++this.state.openSerial;
    ++this.state.reportOpenSerial;
    this.invalidateSourceRequests();
    this.composition.reset();
    this.sourceRows.reset();
    this.tree.reset();
    this.plans.close();
    this.issuePanel.close();
    if (this.pasteSpecial.dialog.open) this.pasteSpecial.dialog.close();
    this.analysisJobs.reset();
    this.state.sheetLoadSeq++;
    this.state.sheetAbortController?.abort();
    this.state.sheetAbortController = null;
    this.root.hidden = true;
    document.body.classList.remove('analysis-mode');
    requireEl('analysisDrill').hidden = true;
    this.state.opened = false;
    revalidateChartExport();
  }

  bookChanged(key: string, book = ''): void {
    this.state.currentBook = key || '';
    if (key === this.state.fingerprint && (!book || book === this.state.book)) return;
    this.state.compositionDraft = null;
    this.close();
    this.issuePanel.reset();
    ++this.state.abortIndex;
    this.state.profileController?.abort();
    this.snapshots.changed();
    this.state.activeResult = null;
    this.state.activeResultQuery = null;
    this.state.jobID = '';
    this.state.reportCache.clear();
  }

  hasUnsaved(): boolean {
    return this.sheetsController.hasUnsaved();
  }

  navigateSheet(id: string, row: number, column: number): Promise<void> {
    return this.sheetsController.navigateSheet(id, row, column);
  }

  refresh(): Promise<boolean | undefined> {
    return this.refreshWorkspace();
  }

  public async refreshWorkspace(): Promise<boolean | undefined> {
    if (!this.state.opened) return undefined;
    const serial = ++this.state.workspaceRefreshSerial;
    const opened = this.state.openSerial;
    const book = this.state.fingerprint;
    const pathBook = this.state.book;
    const workspace = await this.api<AnalysisWorkspaceResponse>('/api/analysis/workspace');
    if (
      !this.isOpened() ||
      serial !== this.state.workspaceRefreshSerial ||
      opened !== this.state.openSerial ||
      book !== this.state.fingerprint ||
      pathBook !== this.state.book
    ) {
      return false;
    }
    if (workspace.fingerprint !== book) {
      throw new Error('工作簿已切换，未载入其他来源的分析工作区');
    }
    this.state.workspace = { ...workspace };
    this.state.fingerprint = this.state.workspace.fingerprint || '';
    const old = this.state.source;
    const select = requireSelect('analysisSourceSelect');
    const original = this.state.workspace.sourceSheets;
    select.replaceChildren();
    const originalGroup = document.createElement('optgroup');
    originalGroup.label = '原始 Excel · 只读';
    for (const s of original) originalGroup.append(new Option(s.name, `wb:${s.name}`));
    select.append(originalGroup);
    if (this.state.workspace.sheets.length) {
      const grp = document.createElement('optgroup');
      grp.label = '分析工作表 · 可编辑';
      for (const s of this.state.workspace.sheets) {
        grp.append(new Option(s.name, `scratch:${s.id}`));
      }
      select.append(grp);
    }
    this.appendCompositionSources(select);
    if (Array.from(select.options).some((x) => x.value === old)) {
      select.value = old;
    } else {
      this.state.source = select.options[0]?.value ?? '';
      this.state.query = this.queryBase(this.state.source);
      select.value = this.state.source;
    }
    if (this.state.tab === 'dashboard') this.dashboardController.renderDashboard();
    return true;
  }

  private async ensureIndex(
    sheet: string,
    current: () => boolean = () => this.state.opened,
  ): Promise<void> {
    const activity = this.currentActivity()?.begin('正在建立行索引');
    try {
      const data = await this.post<IndexStartResponse>('/api/index/start', { sheet });
      if (data.ready) return;
      const deadline = Date.now() + 120000;
      while (Date.now() < deadline && current()) {
        await sleep(350);
        const s = await this.api<IndexStatusResponse>(
          `/api/index/status?id=${encodeURIComponent(data.jobId)}`,
        );
        if (!current()) throw makeAbortError('读取字段已取消');
        activity?.job({
          id: s.id,
          stage: s.stage,
          progress: s.progress,
          doneBytes: s.doneBytes,
          totalBytes: s.totalBytes,
          error: s.error,
        });
        this.setText('analysisSourceInfo', s.stage || '正在建立行索引…');
        if (s.status === 'done') return;
        if (s.status === 'error' || s.status === 'cancelled') {
          throw new Error(s.error || '建立索引未完成');
        }
      }
      throw new Error('建立索引超时或已取消');
    } finally {
      activity?.end();
    }
  }

  private renderSourceScope(): void {
    const combined = Boolean(this.state.query?.composition);
    const scopeSelect = requireSelect('analysisSourceScope');
    scopeSelect.disabled = combined;
    requireEl('analysisHeaderLabel').hidden = combined;
    requireButton('analysisPlansOpen').disabled = combined;
    requireEl('analysisScopeLabel').hidden = combined;
    requireEl('analysisScopeControls').hidden = combined;
    const mode = this.state.query?.sourceScope?.mode ?? 'full';
    scopeSelect.value = mode;
    this.setText(
      'analysisScopeInfo',
      this.state.query?.composition
        ? `${this.state.query.composition.inputs.length} 个来源`
        : sourceScopeCaption(this.state.query?.sourceScope),
    );
    requireButton('analysisSourceRefresh').disabled = this.state.fieldsLoading || mode === 'full';
    requireButton('analysisSourceCancel').hidden = !this.state.fieldsLoading;
    for (const option of scopeSelect.options) {
      if (option.value !== 'full') option.disabled = !this.state.source.startsWith('wb:');
    }
  }

  private fieldsKey(): string {
    return JSON.stringify([
      this.state.fingerprint,
      this.state.book,
      this.state.source,
      this.state.headerRow,
      this.state.query?.sourceScope ?? null,
    ]);
  }

  public async loadFields(options: LoadFieldsOptions = {}): Promise<boolean> {
    if (!this.state.source) return false;
    this.invalidateSourceRequests();
    const stamp = this.state.abortIndex;
    const book = this.state.fingerprint;
    const pathBook = this.state.book;
    const source = this.state.source;
    const header = this.state.headerRow;
    const controller = new AbortController();
    this.state.fieldsController = controller;
    const current = (): boolean =>
      stamp === this.state.abortIndex &&
      book === this.state.fingerprint &&
      pathBook === this.state.book &&
      source === this.state.source &&
      header === this.state.headerRow &&
      this.state.opened &&
      !controller.signal.aborted &&
      (!options.guard || options.guard());
    this.state.fieldsReady = false;
    this.state.fieldsLoading = true;
    this.setAnalysisBusy(Boolean(this.analysisJobs.main));
    this.renderSourceScope();
    requireEl('analysisFieldProfile').hidden = true;
    this.setText('analysisSourceInfo', '正在读取表头…');
    let candidate: AnalysisSourceRecord | undefined;
    try {
      if (
        !options.mode &&
        this.state.query?.sourceScope &&
        source.startsWith('wb:') &&
        !this.sourceRows.matches(source, this.state.query.sourceScope)
      ) {
        await this.ensureIndex(source.slice(3), current);
      }
      candidate = await this.sourceRows.prepare(
        {
          source,
          sourceScope: this.state.query?.sourceScope,
          composition: this.state.query?.composition,
          prepared: options.prepared,
          mode: options.mode,
        },
        current,
      );
      if (!current()) return false;
      const url = `/api/analysis/fields?${new URLSearchParams({
        source,
        headerRow: String(header),
        ...(candidate.id ? { sourceId: candidate.id } : {}),
      }).toString()}`;
      let data: AnalysisFieldsResponse;
      try {
        data = await this.api<AnalysisFieldsResponse>(url, { signal: controller.signal });
      } catch (e: unknown) {
        if (getHttpStatus(e) === 409 && source.startsWith('wb:') && !candidate.id && current()) {
          await this.ensureIndex(source.slice(3), current);
          data = await this.api<AnalysisFieldsResponse>(url, { signal: controller.signal });
        } else {
          throw e;
        }
      }
      if (!current()) return false;
      this.sourceRows.commit(candidate);
      const activeQuery = this.state.query ?? this.queryBase(this.state.source);
      this.state.query = activeQuery;
      if (candidate.sourceScope) activeQuery.sourceScope = cloneJson(candidate.sourceScope);
      else delete activeQuery.sourceScope;
      if (candidate.composition) activeQuery.composition = cloneJson(candidate.composition);
      else delete activeQuery.composition;
      this.state.fields = [...data.fields];
      this.state.preview = [...data.preview];
      // The backend owns numeric typing; visible strings cannot override a
      // native Excel text column or turn leading-zero identifiers into amounts.
      this.setText('analysisFieldCount', String(this.state.fields.length));
      this.setText(
        'analysisSourceInfo',
        `${formatCountNumber(data.totalRows)} 条源记录${data.truncatedFields ? ' · 仅显示前256列' : ''}`,
      );
      activeQuery.source = this.state.source;
      activeQuery.headerRow = this.state.headerRow;
      this.state.loadedFieldsKey = this.fieldsKey();
      this.state.fieldsReady = true;
      this.renderFields();
      this.renderBuilder();
      return true;
    } catch (err: unknown) {
      if (current() && !isAbortError(err)) {
        this.state.fieldsReady = this.state.loadedFieldsKey === this.fieldsKey();
        const msg = errorMessage(err);
        this.setText('analysisSourceInfo', msg);
        this.toast(msg, 'error');
      }
      return false;
    } finally {
      this.sourceRows.release(candidate);
      if (current()) {
        this.state.fieldsLoading = false;
        this.renderSourceScope();
        this.setAnalysisBusy(Boolean(this.analysisJobs.main));
      }
    }
  }

  private allFields(): DashboardFieldChoice[] {
    const additional: DashboardFieldChoice[] = (this.state.query?.transforms ?? []).map((t) => ({
      id: t.id,
      name: t.name,
      numeric: NUMERIC_TRANSFORM_OPS.has(t.op),
    }));
    return [...this.state.fields, ...additional];
  }

  private fieldSelect(
    value = '',
    emptyLabel = '选择字段',
    choices: readonly { readonly id: string; readonly name: string }[] = this.allFields(),
  ): HTMLSelectElement {
    const el = document.createElement('select');
    el.className = 'analysis-control';
    el.append(new Option(emptyLabel, ''));
    for (const f of choices) el.append(new Option(f.name, f.id));
    el.value = value;
    return el;
  }

  private renderFields(): void {
    const list = requireEl('analysisFields');
    list.replaceChildren();
    const q = requireInput('analysisFieldSearch').value.trim().toLocaleLowerCase();
    const fragment = document.createDocumentFragment();
    for (const field of this.allFields()) {
      if (q && !`${field.name} ${field.id}`.toLocaleLowerCase().includes(q)) continue;
      const wrapper = document.createElement('div');
      wrapper.className = 'analysis-field-wrapper';
      const chip = document.createElement('button');
      chip.type = 'button';
      chip.className = 'analysis-field-chip';
      chip.draggable = true;
      chip.title = '拖到行、列、汇总区域；单击按类型分配';
      chip.dataset.field = field.id;
      const type = document.createElement('span');
      type.className = 'analysis-type';
      type.textContent = field.numeric ? '#' : 'Aa';
      const name = document.createElement('span');
      name.textContent = field.name;
      chip.append(type, name);
      chip.addEventListener('dragstart', (e: DragEvent) => {
        e.dataTransfer?.setData('text/plain', field.id);
        if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copyMove';
      });
      chip.addEventListener('click', () => this.addField(field.id, field.numeric ? 'values' : 'rows'));
      wrapper.append(chip);
      if (!field.id.startsWith('D_')) {
        const sourceField = this.state.fields.find((f) => f.id === field.id);
        if (sourceField) {
          const info = document.createElement('button');
          info.type = 'button';
          info.className = 'analysis-field-info-btn';
          info.title = '查看字段画像与常见值';
          info.setAttribute('aria-label', `查看字段画像：${field.name}`);
          info.innerHTML = icon('info', 'icon icon-small');
          info.onclick = (): void => {
            void this.loadFieldProfile(sourceField);
          };
          wrapper.append(info);
        }
      }
      fragment.append(wrapper);
    }
    list.append(fragment);
  }

  public async loadFieldProfile(field: AnalysisField): Promise<void> {
    if (!this.state.fieldsReady || this.state.fieldsLoading) return;
    const host = requireEl('analysisFieldProfile');
    host.hidden = false;
    host.textContent = `正在分析 ${field.name} …`;
    this.state.profileController?.abort();
    const controller = new AbortController();
    this.state.profileController = controller;
    const source = this.state.source;
    const header = this.state.headerRow;
    const book = this.state.fingerprint;
    const pathBook = this.state.book;
    const scope = sourceScopeKey(this.state.query?.sourceScope);
    const current = (): boolean =>
      this.state.opened &&
      !controller.signal.aborted &&
      this.state.profileController === controller &&
      book === this.state.fingerprint &&
      pathBook === this.state.book &&
      source === this.state.source &&
      header === this.state.headerRow &&
      scope === sourceScopeKey(this.state.query?.sourceScope);
    try {
      const info = await this.api<AnalysisProfileResponse>(
        `/api/analysis/profile?${new URLSearchParams({
          source,
          headerRow: String(header),
          field: field.id,
          ...(this.sourceRows.binding?.id ? { sourceId: this.sourceRows.binding.id } : {}),
        }).toString()}`,
        { signal: controller.signal },
      );
      if (!current()) return;
      host.replaceChildren();
      const head = document.createElement('div');
      head.className = 'analysis-profile-head';
      const title = document.createElement('strong');
      title.textContent = field.name;
      const closeBtn = document.createElement('button');
      closeBtn.type = 'button';
      closeBtn.textContent = '×';
      closeBtn.title = '收起字段画像';
      closeBtn.setAttribute('aria-label', '收起字段画像');
      closeBtn.onclick = (): void => {
        controller.abort();
        host.hidden = true;
      };
      head.append(title, closeBtn);
      host.append(head);

      const canCluster =
        !this.state.query?.composition &&
        (this.state.source.startsWith('scratch:') ||
          Boolean(this.formats.sources[this.state.sourceFormat]?.textClustering));
      if (canCluster) {
        const candidates = document.createElement('button');
        candidates.type = 'button';
        candidates.className = 'analysis-quiet-button';
        candidates.textContent = '疑似重复';
        candidates.dataset.clusterField = field.id;
        candidates.addEventListener('click', () => {
          if (current()) this.clusters.open(field);
        });
        host.append(candidates);
      }

      const stats = document.createElement('div');
      stats.className = 'analysis-profile-stat';
      const profilePairs: readonly (readonly [string, number])[] = [
        ['总行数', info.rows],
        ['空值', info.empty],
        [`唯一值${info.uniqueTruncated ? '（至少）' : ''}`, info.unique],
        ['数值', info.numeric],
      ];
      for (const [label, value] of profilePairs) {
        const box = document.createElement('div');
        const top = document.createElement('strong');
        const bot = document.createElement('div');
        top.textContent = formatCountNumber(value);
        bot.textContent = label;
        box.append(top, bot);
        stats.append(box);
      }
      host.append(stats);
      if (info.minNumber !== '' && info.maxNumber !== '') {
        const range = document.createElement('p');
        range.className = 'analysis-profile-range';
        range.textContent = `数值范围：${info.minNumber} ～ ${info.maxNumber}`;
        host.append(range);
      }
      const caption = document.createElement('p');
      caption.className = 'analysis-profile-range';
      caption.textContent = '常见值 · 点击即可作为筛选条件';
      host.append(caption);
      for (const item of info.frequent) {
        const row = document.createElement('div');
        row.className = 'analysis-profile-item';
        const select = document.createElement('button');
        select.type = 'button';
        select.textContent = item.value || '（空白）';
        select.title = item.value;
        select.onclick = (): void => {
          if (!current() || !this.state.query) return;
          if (this.state.query.filters.length >= 8) {
            this.toast('最多8个筛选条件', 'warning');
            return;
          }
          this.state.query.filters.push({
            field: field.id,
            op: item.value === '' ? 'blank' : 'eq',
            value: item.value,
          });
          this.renderRules();
          this.toast(`已添加筛选：${field.name}`, 'success');
        };
        const count = document.createElement('span');
        count.textContent = formatCountNumber(item.count);
        row.append(select, count);
        host.append(row);
      }
      if (info.missingFormulaCaches) {
        const warning = document.createElement('p');
        warning.textContent = `${formatCountNumber(info.missingFormulaCaches)} 条公式缺少缓存值`;
        host.append(warning);
      }
    } catch (err: unknown) {
      if (current() && !isAbortError(err)) {
        const msg = errorMessage(err);
        host.textContent = msg;
        this.toast(msg, 'error');
      }
    }
  }

  private async backupExport(): Promise<void> {
    await this.sheetsController.flush();
    if (this.hasUnsaved()) {
      this.toast('仍有未保存的分析内容，请先重试保存', 'warning');
      return;
    }
    const activity = this.currentActivity()?.begin('正在导出分析备份');
    try {
      const blob = await this.api('/api/analysis/backup', { activity: false }, 'blob');
      this.downloadBlobFn(
        blob,
        `ExcelLens-Analysis-Backup-${new Date().toISOString().slice(0, 10)}.json`,
      );
      this.toast('已导出分析工作区备份', 'success');
    } catch (err: unknown) {
      this.toast(errorMessage(err), 'error');
    } finally {
      activity?.end();
    }
  }

  private async backupImport(file: File | undefined): Promise<void> {
    if (!file) return;
    if (this.hasUnsaved()) {
      this.toast('仍有未保存的分析内容，请先重试保存', 'warning');
      return;
    }
    if (file.size > 41 * 1024 * 1024) {
      this.toast('备份文件超过大小限制', 'warning');
      return;
    }
    const answer = await this.askText(
      '恢复分析工作区',
      '',
      '请输入“恢复”确认覆盖当前编辑表和已保存报表',
    );
    if (answer !== '恢复') return;
    const activity = this.currentActivity()?.begin('正在恢复分析备份');
    try {
      const raw = await file.text();
      const parsed: unknown = JSON.parse(raw);
      await this.post('/api/analysis/backup', parsed);
      this.state.reportCache.clear();
      this.state.sheetRevisions.clear();
      this.state.undoHistory.clear();
      this.state.gridData = null;
      this.state.activeResult = null;
      this.state.activeResultQuery = null;
      this.state.activeReportID = '';
      this.state.loadedSheet = null;
      await this.refreshWorkspace();
      await this.loadFields();
      void this.setTab('dashboard');
      this.toast('分析工作区已从备份恢复', 'success');
    } catch (err: unknown) {
      this.toast(`恢复失败：${errorMessage(err)}`, 'error');
    } finally {
      activity?.end();
    }
  }

  private addField(id: string, zone: 'rows' | 'columns' | 'values'): void {
    const q = this.state.query;
    const fields = this.allFields();
    if (!q || !fields.some((f) => f.id === id)) return;
    // Rows and Columns are mutually exclusive; Values may reuse a dimension.
    if (zone === 'rows' || zone === 'columns') {
      q.rows = q.rows.filter((x) => x !== id);
      q.columns = q.columns.filter((x) => x !== id);
    }
    if (zone === 'rows') {
      if (q.rows.length >= 3) {
        this.toast('行维度最多支持3个', 'warning');
        return;
      }
      q.rows.push(id);
    }
    if (zone === 'columns') {
      q.columns = [id];
    }
    if (zone === 'values') {
      if (q.metrics.length) {
        this.addMetric(id);
        return;
      }
      q.value = id;
      if (q.aggregate === 'count' && fields.find((f) => f.id === id)?.numeric) {
        q.aggregate = 'sum';
      }
      q.metrics = [{ field: id, aggregate: q.aggregate, name: '' }];
      this.syncFirstMetric();
    }
    this.renderBuilder();
  }

  private renderBuilder(): void {
    const q = this.state.query;
    if (!q) return;
    requireSelect('analysisLayout').value = q.layout;
    for (const zone of ['rows', 'columns', 'values'] as const) {
      const zoneId =
        zone === 'rows'
          ? 'analysisRowsZone'
          : zone === 'columns'
            ? 'analysisColumnsZone'
            : 'analysisValuesZone';
      const el = requireEl(zoneId);
      el.replaceChildren();
      const metrics = q.metrics;
      const ids: readonly string[] =
        zone === 'values'
          ? metrics.length
            ? metrics.map((m) => m.field || '')
            : q.value
              ? [q.value]
              : []
          : q[zone];
      ids.forEach((id, index) => {
        const chip = document.createElement('div');
        chip.className = 'analysis-selected-chip';
        chip.draggable = zone !== 'values';
        const title = document.createElement('span');
        const metricItem = zone === 'values' ? metrics[index] : undefined;
        title.textContent = metricItem ? this.metricCaption(metricItem) : this.fieldName(id);
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.textContent = '×';
        remove.title = '移除字段';
        remove.addEventListener('click', () => {
          if (zone === 'values') {
            if (metrics.length) {
              q.metrics.splice(index, 1);
              this.syncFirstMetric();
            } else {
              q.value = '';
            }
            if (!q.metrics.length) {
              q.value = '';
              q.aggregate = 'count';
            }
          } else {
            q[zone] = q[zone].filter((v) => v !== id);
          }
          this.renderBuilder();
        });
        if (zone !== 'values') {
          chip.addEventListener('dragstart', (e: DragEvent) => {
            e.dataTransfer?.setData('text/plain', id);
            if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
          });
        }
        chip.append(title, remove);
        el.append(chip);
      });
    }
    const aggSelect = requireSelect('analysisAggregate');
    aggSelect.value = q.aggregate;
    aggSelect.disabled = q.metrics.length > 1;
    requireSelect('analysisSort').value = q.sort || 'value_desc';
    requireInput('analysisTop').value = String(q.top);
    requireInput('analysisPrecision').value = String(q.precision);
    requireSelect('analysisChartSelect').value = q.chart || 'bar';
    this.renderMetricEditor();
    this.renderRules();
    this.renderTransforms();
  }

  private renderRules(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisFilterList');
    list.replaceChildren();
    q.filters.forEach((f, i) => {
      const box = document.createElement('div');
      box.className = 'analysis-rule';
      const h = document.createElement('div');
      h.className = 'analysis-rule-header';
      const select = this.fieldSelect(f.field, '字段');
      select.onchange = (): void => {
        f.field = select.value;
      };
      h.append(select);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'analysis-rule-remove';
      remove.textContent = '×';
      remove.onclick = (): void => {
        q.filters.splice(i, 1);
        this.renderRules();
      };
      h.append(remove);

      const controls = document.createElement('div');
      controls.className = 'analysis-rule-controls';
      const op = document.createElement('select');
      op.className = 'analysis-control';
      for (const [val, label] of FILTER_OPERATIONS) op.append(new Option(label, val));
      op.value = f.op;
      controls.append(op);

      const input = document.createElement('input');
      input.className = 'analysis-control';
      input.value = f.value || '';
      input.onchange = (): void => {
        f.value = input.value;
      };
      input.oninput = (): void => {
        f.value = input.value;
      };
      const syncOp = (): void => {
        f.op = parseFilterOp(op.value);
        input.disabled = f.op === 'blank' || f.op === 'notblank';
        input.placeholder =
          f.op === 'between'
            ? '100..200 或 日期..日期'
            : f.op === 'in'
              ? '多项用 | 分隔'
              : '匹配值';
      };
      op.onchange = syncOp;
      syncOp();
      controls.append(input);
      box.append(h, controls);
      list.append(box);
    });
  }

  public removeTransform(id: string): void {
    const q = this.state.query;
    if (!q) return;
    const removed = new Set([id]);
    for (const step of q.transforms) {
      if (removed.has(step.source)) removed.add(step.id);
    }
    q.transforms = q.transforms.filter((t) => !removed.has(t.id));
    q.filters = q.filters.filter((f) => !removed.has(f.field));
    q.rows = q.rows.filter((rowId) => !removed.has(rowId));
    q.columns = q.columns.filter((colId) => !removed.has(colId));
    q.metrics = q.metrics.filter((m) => !m.field || !removed.has(m.field));
    if (removed.has(q.value)) {
      q.value = '';
      q.aggregate = 'count';
    }
    this.syncFirstMetric();
    this.renderFields();
    this.renderBuilder();
  }

  public renderTransforms(): void {
    const q = this.state.query;
    if (!q) return;
    const list = requireEl('analysisTransformList');
    list.replaceChildren();
    q.transforms.forEach((t, i) => {
      const box = document.createElement('div');
      box.className = 'analysis-rule';
      const row = document.createElement('div');
      row.className = 'analysis-rule-header';
      const name = document.createElement('input');
      name.className = 'analysis-control';
      name.placeholder = '新列名';
      name.value = t.name;
      name.oninput = (): void => {
        t.name = name.value || '新列';
        this.renderFields();
      };
      row.append(name);
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'analysis-rule-remove';
      remove.textContent = '×';
      remove.title = '移除计算列';
      remove.onclick = (): void => this.removeTransform(t.id);
      row.append(remove);

      const temporal = TEMPORAL_TRANSFORM_OPS.has(t.op);
      const operations: (readonly [AnalysisTransformOp, string])[] = [...TRANSFORM_OPERATIONS];
      if (t.op === 'map_text') operations.push(['map_text', '文本映射']);
      const ops = document.createElement('select');
      ops.className = 'analysis-control';
      for (const [val, label] of operations) ops.append(new Option(label, val));
      ops.value = t.op;
      ops.onchange = (): void => {
        t.op = parseTransformOp(ops.value);
        delete t.mapping;
        t.arg = TEMPORAL_TRANSFORM_OPS.has(t.op) ? 'auto' : '';
        this.renderFields();
        this.renderTransforms();
      };

      const sourceChoices = [
        ...this.state.fields.map((f) => ({ id: f.id, name: f.name })),
        ...q.transforms.slice(0, i).map((step) => ({ id: step.id, name: step.name })),
      ];
      const source = this.fieldSelect(t.source, '来源列', sourceChoices);
      source.onchange = (): void => {
        t.source = source.value;
      };

      let argControl: HTMLSelectElement | HTMLInputElement;
      if (temporal) {
        const sel = document.createElement('select');
        sel.className = 'analysis-control';
        sel.setAttribute('aria-label', '日期或时间的输入格式');
        const dateFormats: readonly (readonly [string, string])[] = [
          ['auto', '年在前 / 时间（自动识别）'],
          ['compact', '年月日紧排：20261111'],
          ['dmy', '日/月/年：11/12/2026'],
          ['mdy', '月/日/年：11/12/2026'],
        ];
        for (const [val, label] of dateFormats) sel.append(new Option(label, val));
        sel.value = t.arg || 'auto';
        sel.onchange = (): void => {
          t.arg = sel.value;
        };
        argControl = sel;
      } else {
        const inp = document.createElement('input');
        inp.className = 'analysis-control';
        inp.setAttribute('aria-label', '转换参数');
        inp.placeholder = '常数 / 查找=>替换';
        inp.value = t.arg || '';
        inp.hidden = ARGLESS_TRANSFORM_OPS.has(t.op);
        inp.onchange = (): void => {
          t.arg = inp.value;
        };
        inp.oninput = (): void => {
          t.arg = inp.value;
        };
        argControl = inp;
      }

      const controls = document.createElement('div');
      controls.className = 'analysis-rule-controls';
      controls.append(source, ops);
      argControl.style.marginTop = '7px';
      box.append(row, controls, argControl);
      if (temporal) {
        const note = document.createElement('small');
        note.className = 'analysis-conversion-note';
        note.textContent = '运行分析时转换；无效或含糊的值保留原文并计数。';
        box.append(note);
      }
      if (t.op === 'map_text') {
        const detail = document.createElement('details');
        detail.className = 'analysis-mapping-details';
        const summary = document.createElement('summary');
        summary.textContent = `${t.mapping?.length ?? 0} 条精确文本映射 · 查看原文`;
        const content = document.createElement('pre');
        detail.append(summary, content);
        detail.addEventListener('toggle', () => {
          if (detail.open) {
            content.textContent = (t.mapping ?? [])
              .map((entry) => `${JSON.stringify(entry.from)} → ${JSON.stringify(entry.to)}`)
              .join('\n');
          }
        });
        box.append(detail);
      }
      list.append(box);
    });
  }

  private setAnalysisBusy(busy: boolean): void {
    this.snapshots.changed();
    const snapSave = queryButton('analysisSnapshotSave');
    if (snapSave) snapSave.disabled = busy || !this.state.chartCapture?.current();
    this.state.chartBusy = busy;
    requireButton('analysisChartImage').disabled = busy || !this.state.chartCapture?.current();
    revalidateChartExport();
    this.tree.setBlocked(
      busy ||
        this.state.tab !== 'pivot' ||
        this.state.reportPending === this.state.reportOpenSerial,
    );
    requireButton('analysisIssuesOpen').disabled = busy;
    requireEl('analysisProgress').hidden = !busy;
    requireButton('analysisCancel').hidden = !busy;
    requireButton('analysisRun').disabled =
      busy || this.state.fieldsLoading || !this.state.fieldsReady;
    if (busy) {
      this.setText('analysisProgressText', '正在准备分析…');
      this.setText('analysisProgressPercent', '');
      requireEl('analysisProgressBar').style.width = '0%';
    }
    const ready =
      !busy &&
      this.state.reportPending !== this.state.reportOpenSerial &&
      Boolean(this.state.activeResult);
    for (const id of [
      'analysisSaveReport',
      'analysisMaterialize',
      'analysisMaterializeDetails',
      'analysisExport',
    ] as const) {
      requireButton(id).disabled = !ready;
    }
  }

  private showAnalysisProgress(status: AnalysisQueryStatusResponse): void {
    const percent = status.total
      ? Math.min(99, Math.round((100 * status.progress) / status.total))
      : 0;
    this.setText('analysisProgressText', status.stage || '正在分析…');
    this.setText(
      'analysisProgressPercent',
      status.total ? `${percent}%` : `${formatCountNumber(status.progress)} 行`,
    );
    requireEl('analysisProgressBar').style.width = `${percent}%`;
  }

  private async executeQuery(
    q: WorkspaceAnalysisQuery,
    options: { readonly main?: boolean } = {},
  ): Promise<ExecutedAnalysisQuery> {
    const request = options.main ? this.sourceRows.query(q) : q;
    const completed = await this.analysisJobs.execute(request, options);
    return {
      result: completed.result,
      job: completed.job,
      sourceFields: isAnalysisFieldArray(completed.sourceFields)
        ? completed.sourceFields
        : undefined,
      query: isWorkspaceAnalysisQuery(completed.query) ? completed.query : undefined,
      current: completed.current,
    };
  }

  private async run(): Promise<void> {
    ++this.state.reportOpenSerial;
    if (!this.state.fieldsReady || this.state.fieldsLoading || !this.state.fields.length) {
      this.toast('请先成功读取所选范围的字段', 'warning');
      return;
    }
    const q = this.currentQuery();
    if (q.layout === 'tree' && !q.rows.length) {
      this.toast('层级展开需要至少一个行维度', 'warning');
      return;
    }
    const measures = effectiveMetrics(q);
    if (measures.some((m) => m.aggregate !== 'count' && !m.field)) {
      this.toast('数值指标必须选择字段', 'warning');
      return;
    }
    if (
      !q.rows.length &&
      !q.columns.length &&
      measures.every((m) => m.aggregate === 'count' && !m.field)
    ) {
      this.toast('请选择至少一个分类字段', 'warning');
      return;
    }
    try {
      void this.setTab('pivot');
      const data = await this.executeQuery(q, { main: true });
      if (!data.current()) return;
      const snapshot = this.state.reportCache.set(this.state.fingerprint, data.query ?? q, data);
      if (this.activateResult(snapshot) && this.state.activeResultQuery) {
        this.pivotView.renderResult(data.result, this.state.activeResultQuery);
      }
    } catch (err: unknown) {
      if (!isAbortError(err)) this.toast(errorMessage(err), 'error');
    }
  }

  public activateResult(snapshot: AnalysisResultSnapshot): boolean {
    if (snapshot.book !== this.state.fingerprint) return false;
    this.state.drillAbort?.abort();
    requireEl('analysisDrill').hidden = true;
    this.state.activeResult = snapshot.result;
    this.state.activeResultQuery = restoreReportQuery(snapshot.query);
    this.state.jobID = snapshot.job;
    this.state.activeResultFields = (snapshot.sourceFields ?? this.state.fields).map((field) => ({
      ...field,
    }));
    revalidateChartExport();
    this.pivotView.bindTreeResult();
    return true;
  }

  public async adoptAnalysisPlan(
    completed: RecipeRunCompleted,
    preview: { readonly ref: { readonly source: string }; readonly stamp?: string },
    current: () => boolean,
  ): Promise<boolean> {
    if (!current() || !this.state.opened) return false;
    const book = this.state.fingerprint;
    const serial = this.state.reportOpenSerial;
    const status = completed.result;
    if (!status.query) throw new Error('方案结果与目标来源不一致');
    const q = restoreReportQuery(status.query);
    if (!status.result || !q.source || q.source !== preview.ref.source) {
      throw new Error('方案结果与目标来源不一致');
    }
    // Prepare the complete builder schema before replacing the active result.
    // Errors, cancellation and edits after completion leave the old UI intact.
    const guard = (): boolean =>
      current() &&
      completed.current() &&
      book === this.state.fingerprint &&
      serial === this.state.reportOpenSerial &&
      this.state.opened;
    const candidate = await this.sourceRows.prepare(
      { source: q.source, sourceScope: q.sourceScope },
      guard,
    );
    let schema: AnalysisFieldsResponse;
    try {
      schema = await this.api<AnalysisFieldsResponse>(
        `/api/analysis/fields?${new URLSearchParams({
          source: q.source,
          headerRow: String(q.headerRow),
          stamp: '1',
          ...(candidate.id ? { sourceId: candidate.id } : {}),
        }).toString()}`,
      );
      if (!guard()) return false;
      if (schema.sourceStamp !== preview.stamp) {
        throw new Error('目标表在分析后发生变化，请重新核对字段；当前结果保留');
      }
      this.sourceRows.commit(candidate);
    } finally {
      this.sourceRows.release(candidate);
    }
    ++this.state.reportOpenSerial;
    ++this.state.abortIndex;
    this.analysisJobs.cancelMain();
    this.state.fieldsController?.abort();
    this.state.profileController?.abort();
    this.state.fieldsLoading = false;
    this.state.fieldsReady = true;
    this.state.source = q.source;
    this.state.headerRow = q.headerRow;
    this.state.query = q;
    this.state.fields = [...schema.fields];
    this.state.preview = [...schema.preview];
    this.state.activeReportID = '';
    requireSelect('analysisSourceSelect').value = this.state.source;
    requireInput('analysisHeaderRow').value = String(this.state.headerRow);
    this.setText('analysisFieldCount', String(this.state.fields.length));
    this.setText(
      'analysisSourceInfo',
      `${formatCountNumber(schema.totalRows)} 条源记录${schema.truncatedFields ? ' · 仅显示前256列' : ''}`,
    );
    requireEl('analysisFieldProfile').hidden = true;
    const snapshot = this.state.reportCache.set(book, q, {
      job: completed.job,
      result: status.result,
      sourceFields: status.sourceFields,
    });
    this.activateResult(snapshot);
    this.state.loadedFieldsKey = this.fieldsKey();
    requireSelect('analysisExportFormat').value = status.exportFormat || 'xlsx';
    void this.setTab('pivot');
    this.renderSourceScope();
    this.renderFields();
    this.renderBuilder();
    if (this.state.activeResultQuery) {
      this.pivotView.renderResult(status.result, this.state.activeResultQuery);
    }
    this.setAnalysisBusy(false);
    return true;
  }

  private async navigateComposition(
    record: { readonly row: number; readonly origin?: SourceOrigin },
    column: number | undefined,
    job: string = this.state.jobID,
  ): Promise<void> {
    const origin = record.origin;
    if (!origin || !column) return;
    const identity = JSON.stringify([
      this.state.fingerprint,
      this.state.book,
      this.state.opened,
    ]);
    const foreign = origin.book !== this.state.book;
    const popup = foreign ? window.open('about:blank', '_blank') : null;
    if (popup) popup.opener = null;
    let handled = false;
    try {
      const target = await this.post<CompositionNavigationResponse>(
        '/api/analysis/composition/navigate',
        {
          jobId: job,
          input: origin.input,
          book: origin.book,
          row: record.row,
          col: column,
        },
      );
      if (
        identity !==
        JSON.stringify([this.state.fingerprint, this.state.book, this.state.opened])
      ) {
        return;
      }
      if (target.sameBook) {
        popup?.close();
        handled = true;
        if (target.kind === 'analysis') {
          await this.navigateSheet(target.analysisSheet, target.row, target.col);
        } else {
          this.close();
          this.navigateWorkbook(target.sheet, `${labelColumn(target.col)}${target.row}`);
        }
      } else if (popup) {
        popup.location.assign(target.url);
        handled = true;
      } else {
        this.toast('请允许打开来源窗口后重试', 'warning');
      }
    } catch (error: unknown) {
      if (
        identity ===
        JSON.stringify([this.state.fingerprint, this.state.book, this.state.opened])
      ) {
        this.toast(errorMessage(error), 'error');
      }
    } finally {
      if (popup && !handled) popup.close();
    }
  }

  public async saveReport(): Promise<void> {
    if (!this.state.activeResultQuery || !this.state.activeResult) return;
    const snapshot: AnalysisResultSnapshot = {
      book: this.state.fingerprint,
      query: cloneJson(this.state.activeResultQuery),
      job: this.state.jobID,
      result: this.state.activeResult,
      sourceFields: this.state.activeResultFields,
    };
    const pathBook = this.state.book;
    const opened = this.state.openSerial;
    const reportID = this.state.activeReportID || '';
    const current = (): boolean =>
      this.state.opened &&
      snapshot.book === this.state.fingerprint &&
      pathBook === this.state.book &&
      opened === this.state.openSerial;
    const old = this.state.workspace?.reports.find((x) => x.id === this.state.activeReportID);
    const name = await this.askText(
      old ? '更新分析报表' : '保存分析报表',
      old?.name || this.state.activeResultQuery.title || '我的数据分析',
      '例如：机构资产规模分布',
    );
    if (!name || !current()) return;
    try {
      const data = await this.post<SaveReportResponse>('/api/analysis/report', {
        id: reportID,
        name,
        query: snapshot.query,
        workbookKey: snapshot.book,
        book: pathBook,
      });
      if (!current()) return;
      if (
        this.state.activeResult === snapshot.result &&
        this.state.jobID === snapshot.job &&
        this.state.activeReportID === reportID
      ) {
        this.state.activeReportID = data.id;
        requireButton('analysisSaveReport').textContent = '更新报表';
      }
      // The dashboard can become visible while its catalogue refresh is in
      // flight. Cache the completed result before that refresh adopts/renders.
      this.state.reportCache.set(snapshot.book, snapshot.query, snapshot);
      try {
        await this.refreshWorkspace();
      } catch (e: unknown) {
        if (current()) this.toast(`报表已保存，列表刷新失败：${errorMessage(e)}`, 'warning');
        return;
      }
      if (current()) {
        this.toast(old ? '已更新已保存的报表' : `已保存到仪表盘 · ${name}`, 'success');
      }
    } catch (e: unknown) {
      if (current()) this.toast(errorMessage(e), 'error');
    }
  }

  public async materialize(): Promise<void> {
    if (!this.state.jobID || !this.state.activeResult) return;
    const job = this.state.jobID;
    const book = this.state.fingerprint;
    const isTree = Boolean(this.state.activeResult.tree);
    const name = await this.askText(
      isTree ? '创建首层结果表' : '创建可编辑结果表',
      `${isTree ? '首层结果 ' : '透视结果 '}${new Date().toLocaleDateString('zh-CN')}`,
    );
    if (!name || book !== this.state.fingerprint) return;
    try {
      const data = await this.post<MaterializeResponse>('/api/analysis/materialize', {
        name,
        jobId: job,
      });
      await this.refreshWorkspace();
      this.state.loadedSheet = data.id;
      this.state.sheetPage = 0;
      void this.setTab('sheets');
      this.toast('新工作表已保存，可编辑且不会修改源 Excel', 'success');
    } catch (e: unknown) {
      this.toast(
        getHttpStatus(e) === 404 ? '结果任务已过期，请重新运行分析后保存' : errorMessage(e),
        'error',
      );
    }
  }

  private async materializeDetails(groupOnly = false): Promise<void> {
    const q = groupOnly
      ? this.state.drillQuery
      : (this.state.activeResultQuery ?? (this.state.query ? this.currentQuery() : null));
    if (!q) {
      this.toast('请先配置分析条件', 'warning');
      return;
    }
    const keys = groupOnly ? [...this.state.drillKeys] : null;
    const jobId = groupOnly ? this.state.drillJob : this.state.jobID;
    const columnKey = groupOnly ? this.state.drillColumn : '';
    const columnSelected = groupOnly ? this.state.drillColumnSelected : undefined;
    const book = this.state.fingerprint;
    const pathBook = this.state.book;
    const initial = `${groupOnly ? '下钻明细 ' : '清洗明细 '}${new Date().toLocaleDateString('zh-CN')}`;
    const name = await this.askText('提取可编辑明细', initial, '最多10,000行，保留源行号');
    if (!name || !this.state.opened || book !== this.state.fingerprint || pathBook !== this.state.book) {
      return;
    }
    const btn = requireButton(groupOnly ? 'analysisDrillSave' : 'analysisMaterializeDetails');
    btn.disabled = true;
    this.setText('analysisAutoSave', '正在提取明细…');
    try {
      const response = await this.post<MaterializeResponse>('/api/analysis/detail/materialize', {
        query: q,
        jobId,
        keys,
        columnKey,
        ...(columnSelected !== undefined ? { columnSelected } : {}),
        name,
        limit: 10000,
      });
      if (!this.isOpened() || book !== this.state.fingerprint || pathBook !== this.state.book) {
        return;
      }
      await this.refreshWorkspace();
      this.state.loadedSheet = response.id;
      this.state.sheetPage = 0;
      requireEl('analysisDrill').hidden = true;
      void this.setTab('sheets');
      this.setText('analysisAutoSave', '已保存');
      this.toast(
        `已提取 ${formatCountNumber(response.rows ?? 0)} 行${
          response.truncated ? '；已达到1万行上限，请增加筛选条件' : ''
        }，原工作簿未修改`,
        response.truncated ? 'warning' : 'success',
      );
    } catch (e: unknown) {
      this.setText('analysisAutoSave', '提取失败');
      this.toast(errorMessage(e), 'error');
    } finally {
      btn.disabled = false;
    }
  }

  public async exportCSV(): Promise<void> {
    if (!this.state.jobID) return;
    const job = this.state.jobID;
    const book = this.state.fingerprint;
    const format = requireSelect('analysisExportFormat').value === 'csv' ? 'csv' : 'xlsx';
    const activity = this.currentActivity()?.begin('正在导出分析结果');
    try {
      const url = `/api/analysis/export?id=${encodeURIComponent(job)}&format=${format}`;
      const res = await this.api(url, { activity: false }, 'response');
      if (book !== this.state.fingerprint) return;
      if (res.status === 404) throw new Error('结果任务已过期，请重新运行分析后导出');
      if (!res.ok) {
        const data: unknown = await res.json().catch(() => ({}));
        const msg =
          isPlainRecord(data) && typeof data.error === 'string' && data.error
            ? data.error
            : '导出失败';
        throw new Error(msg);
      }
      const blob = await res.blob();
      if (book !== this.state.fingerprint) return;
      this.downloadBlobFn(blob, `ExcelLens-Analysis.${format}`);
      this.toast('分析结果已导出', 'success');
    } catch (e: unknown) {
      this.toast(errorMessage(e), 'error');
    } finally {
      activity?.end();
    }
  }

  public async openReport(report: AnalysisReport): Promise<void> {
    const serial = ++this.state.reportOpenSerial;
    const book = this.state.fingerprint;
    this.analysisJobs.cancelMain();
    this.state.reportPending = serial;
    this.setAnalysisBusy(false);
    this.state.source = report.query.source;
    this.state.query = restoreReportQuery(report.query);
    this.state.headerRow = this.state.query.headerRow;
    this.state.activeReportID = '';
    requireSelect('analysisSourceSelect').value = this.state.source;
    requireInput('analysisHeaderRow').value = String(this.state.headerRow);
    void this.setTab('pivot');
    const loaded = await this.loadFields();
    if (serial !== this.state.reportOpenSerial || book !== this.state.fingerprint || !this.state.opened) {
      return;
    }
    this.state.reportPending = 0;
    if (!loaded) {
      this.setAnalysisBusy(false);
      return;
    }
    this.state.activeReportID = report.id;
    const cached = this.state.reportCache.get(book, report.query);
    if (cached && this.activateResult(cached) && this.state.activeResultQuery) {
      this.pivotView.renderResult(cached.result, this.state.activeResultQuery);
      this.setAnalysisBusy(false);
    } else {
      await this.run();
    }
  }

  async reopenJob(job: string, status: AnalysisQueryStatusResponse): Promise<boolean> {
    await this.open();
    const source = status.query?.source ?? '';
    return this.adoptAnalysisPlan(
      { job, result: status, current: () => true },
      { ref: { source }, stamp: status.sourceStamp },
      () => this.state.opened,
    );
  }

  diagnostics(): AnalysisWorkspaceDiagnostics {
    return {
      opened: this.state.opened,
      tab: this.state.tab,
      source: this.state.source,
      sourceMode: this.state.query?.sourceScope?.mode ?? 'full',
      layout: this.state.query?.layout ?? '',
      tree: this.tree.diagnostics(),
      fields: this.state.fields.length,
      fieldsReady: this.state.fieldsReady,
      fieldsLoading: this.state.fieldsLoading,
      resultJob: this.state.jobID,
      hasResult: Boolean(this.state.activeResult),
      pendingWrites: this.sheetsController.pendingWrites,
      drafts: this.drafts.size,
      activeJobs: this.analysisJobs.tasks.size,
    };
  }

  private bind(): void {
    requireButton('analysisCompositionOpen').addEventListener('click', () => {
      void this.composition.open();
    });
    byId('analysisOpenBtn')?.addEventListener('click', () => {
      void this.open();
    });
    requireButton('analysisClose').addEventListener('click', () => this.close());
    this.root.querySelectorAll<HTMLElement>('[data-analysis-tab]').forEach((el) => {
      el.addEventListener('click', () => {
        const tab = el.dataset.analysisTab ?? '';
        if (VALID_TABS.has(tab)) void this.setTab(tab as AnalysisTab);
      });
    });
    requireButton('analysisBackupExport').addEventListener('click', () => {
      void this.backupExport();
    });
    const backupFile = requireInput('analysisBackupFile');
    requireButton('analysisBackupImport').addEventListener('click', () => backupFile.click());
    backupFile.addEventListener('change', () => {
      const file = backupFile.files?.[0];
      void this.backupImport(file).finally(() => {
        backupFile.value = '';
      });
    });

    const sourceSelect = requireSelect('analysisSourceSelect');
    sourceSelect.addEventListener('change', () => {
      ++this.state.reportOpenSerial;
      this.analysisJobs.cancelMain();
      this.sourceRows.reset();
      this.state.activeReportID = '';
      this.state.source = sourceSelect.value;
      const definition =
        this.state.workspace?.reports.find((report) => report.query.source === this.state.source)
          ?.query.composition ??
        (this.state.compositionDraft?.source === this.state.source
          ? this.state.compositionDraft.definition
          : null);
      this.state.headerRow = this.state.source.startsWith('append:') ? 0 : 1;
      requireInput('analysisHeaderRow').value = String(this.state.headerRow);
      this.state.query = this.queryBase(this.state.source);
      if (this.state.source.startsWith('append:') && definition) {
        this.state.query.composition = cloneJson(definition);
      }
      void this.loadFields();
    });

    const selectScope = (mode: AnalysisSourceMode): void => {
      ++this.state.reportOpenSerial;
      this.analysisJobs.cancelMain();
      this.state.activeReportID = '';
      void this.loadFields({ mode });
    };
    const scopeSelect = requireSelect('analysisSourceScope');
    scopeSelect.addEventListener('change', () => selectScope(parseSourceMode(scopeSelect.value)));
    requireButton('analysisSourceRefresh').addEventListener('click', () => {
      selectScope(this.state.query?.sourceScope?.mode ?? 'full');
    });
    requireButton('analysisSourceCancel').addEventListener('click', () => {
      this.invalidateSourceRequests();
      this.state.fieldsReady = this.state.loadedFieldsKey === this.fieldsKey();
      this.renderSourceScope();
      this.setAnalysisBusy(Boolean(this.analysisJobs.main));
      this.setText('analysisSourceInfo', '已取消选择，保留原范围');
    });

    const headerInput = requireInput('analysisHeaderRow');
    headerInput.addEventListener('change', () => {
      ++this.state.reportOpenSerial;
      this.analysisJobs.cancelMain();
      this.state.activeReportID = '';
      const maxHeader = Number(headerInput.max) || Number.MAX_SAFE_INTEGER;
      this.state.headerRow = Math.max(1, Math.min(maxHeader, Number(headerInput.value) || 1));
      if (this.state.query) this.state.query.headerRow = this.state.headerRow;
      void this.loadFields();
    });

    requireButton('analysisLoadFields').addEventListener('click', () => {
      void this.loadFields();
    });
    requireInput('analysisFieldSearch').addEventListener('input', () => this.renderFields());
    this.root.querySelectorAll<HTMLElement>('[data-analysis-zone]').forEach((zone) => {
      zone.addEventListener('dragover', (e: DragEvent) => {
        e.preventDefault();
        zone.classList.add('drag-over');
      });
      zone.addEventListener('dragleave', () => zone.classList.remove('drag-over'));
      zone.addEventListener('drop', (e: DragEvent) => {
        e.preventDefault();
        zone.classList.remove('drag-over');
        const id = e.dataTransfer?.getData('text/plain') ?? '';
        const targetZone = zone.dataset.analysisZone ?? '';
        if (id && VALID_ZONES.has(targetZone)) {
          this.addField(id, targetZone as 'rows' | 'columns' | 'values');
        }
      });
    });

    requireSelect('analysisLayout').addEventListener('change', () => {
      if (this.state.query) {
        this.state.query.layout = requireSelect('analysisLayout').value === 'tree' ? 'tree' : '';
      }
    });
    requireSelect('analysisAggregate').addEventListener('change', () => {
      const q = this.state.query;
      if (!q) return;
      q.aggregate = parseAggregateKind(requireSelect('analysisAggregate').value);
      const first = q.metrics[0];
      if (first) first.aggregate = q.aggregate;
    });
    requireSelect('analysisSort').addEventListener('change', () => {
      if (this.state.query) {
        this.state.query.sort = parseSortOrder(requireSelect('analysisSort').value);
      }
    });
    requireInput('analysisTop').addEventListener('change', () => {
      if (this.state.query) {
        this.state.query.top = Number(requireInput('analysisTop').value);
      }
    });
    requireInput('analysisPrecision').addEventListener('change', () => {
      if (this.state.query) {
        this.state.query.precision = Number(requireInput('analysisPrecision').value);
      }
    });

    requireButton('analysisChartImage').addEventListener('click', () =>
      this.pivotView.exportChartImage(),
    );
    queryButton('analysisSnapshotsOpen')?.addEventListener('click', () => this.snapshots.open());
    queryButton('analysisSnapshotSave')?.addEventListener('click', () => {
      const c = this.state.chartCapture;
      if (c?.current()) {
        this.snapshots.open({
          job: c.snapshot.job,
          metric: c.metric,
          workbookKey: this.state.fingerprint,
          book: this.state.book,
          title: requireEl('analysisReportTitle').textContent || '分析快照',
          current: c.current,
        });
      }
    });
    requireSelect('analysisChartSelect').addEventListener('change', () =>
      this.pivotView.setChartMode(),
    );
    requireSelect('analysisChartMetric').addEventListener('change', () => {
      const idx = Number(requireSelect('analysisChartMetric').value) || 0;
      if (this.state.query) this.state.query.chartMetric = idx;
      if (this.state.activeResultQuery) this.state.activeResultQuery.chartMetric = idx;
      this.pivotView.renderChart();
    });

    requireButton('analysisAddMetric').addEventListener('click', () => this.addMetric(''));
    requireButton('analysisAddFilter').addEventListener('click', () => {
      const firstField = this.state.fields[0];
      const q = this.state.query;
      if (!firstField || !q) return;
      if (q.filters.length >= 8) {
        this.toast('最多8条筛选条件', 'warning');
        return;
      }
      q.filters.push({ field: firstField.id, op: 'eq', value: '' });
      this.renderRules();
    });
    requireButton('analysisAddConversion').addEventListener('click', () => {
      const firstField = this.state.fields[0];
      const q = this.state.query;
      if (!firstField || !q) return;
      if (q.transforms.length >= 8) {
        this.toast('最多8个计算列', 'warning');
        return;
      }
      q.transforms.push({
        id: `D_${Date.now().toString(36)}_${q.transforms.length}`,
        name: '转换日期',
        source: firstField.id,
        op: 'date',
        arg: 'auto',
      });
      this.renderFields();
      this.renderBuilder();
    });
    requireButton('analysisAddTransform').addEventListener('click', () => {
      const firstField = this.state.fields[0];
      const q = this.state.query;
      if (!firstField || !q) return;
      if (q.transforms.length >= 8) {
        this.toast('最多8个计算列', 'warning');
        return;
      }
      q.transforms.push({
        id: `D_${Date.now().toString(36)}_${q.transforms.length}`,
        name: `计算列${q.transforms.length + 1}`,
        source: firstField.id,
        op: 'trim',
        arg: '',
      });
      this.renderFields();
      this.renderBuilder();
    });

    requireButton('analysisRun').addEventListener('click', () => {
      void this.run();
    });
    requireButton('analysisTreeCollapse').addEventListener('click', () => this.tree.collapseAll());
    requireButton('analysisTreePrevious').addEventListener('click', () => {
      this.tree.page = Math.max(0, this.tree.page - 1);
      this.pivotView.renderPivot();
    });
    requireButton('analysisTreeNext').addEventListener('click', () => {
      this.tree.page++;
      this.pivotView.renderPivot();
    });
    requireButton('analysisCancel').addEventListener('click', () => {
      if (this.analysisJobs.cancelMain()) this.toast('已取消分析任务');
    });
    requireButton('analysisSaveReport').addEventListener('click', () => {
      void this.saveReport();
    });
    requireButton('analysisMaterialize').addEventListener('click', () => {
      void this.materialize();
    });
    requireButton('analysisMaterializeDetails').addEventListener('click', () => {
      void this.materializeDetails(false);
    });
    requireButton('analysisDrillSave').addEventListener('click', () => {
      void this.materializeDetails(true);
    });
    requireButton('analysisExport').addEventListener('click', () => {
      void this.exportCSV();
    });
    requireButton('analysisDrillClose').addEventListener('click', () => {
      this.state.drillAbort?.abort();
      requireEl('analysisDrill').hidden = true;
    });
    requireButton('analysisDashboardRefresh').addEventListener('click', () => {
      this.state.reportCache.clear();
      this.dashboardController.renderDashboard();
    });
    const templateSearch = requireInput('analysisTemplateSearch');
    templateSearch.addEventListener('input', () => {
      this.state.templateText = templateSearch.value;
      this.dashboardController.renderTemplates();
    });

    this.sheetsController.bindSheetGrid();
    requireButton('analysisUndo').addEventListener('click', () => {
      void this.sheetsController.changeSheetHistory('undo');
    });
    requireButton('analysisRedo').addEventListener('click', () => {
      void this.sheetsController.changeSheetHistory('redo');
    });
    requireButton('analysisRetrySave').addEventListener('click', () => {
      void this.sheetsController.retryDrafts();
    });
    requireButton('analysisCreateSheet').addEventListener('click', () => {
      void this.sheetsController.createSheet();
    });
    requireButton('analysisAddColumn').addEventListener('click', () => {
      void this.sheetsController.addColumn();
    });
    requireButton('analysisAddRows').addEventListener('click', () => {
      void this.sheetsController.addRows();
    });
    requireButton('analysisDeleteSheet').addEventListener('click', () => {
      void this.sheetsController.deleteSheet();
    });
    requireButton('analysisPasteSpecial').addEventListener('click', () => {
      void this.sheetsController.openPasteSpecial();
    });

    this.root.addEventListener('keydown', (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (document.querySelector('dialog:modal')) return;
        const drillEl = requireEl('analysisDrill');
        if (!drillEl.hidden) {
          this.state.drillAbort?.abort();
          drillEl.hidden = true;
          e.stopPropagation();
          return;
        }
        this.close();
        e.stopPropagation();
      }
    });
    window.addEventListener('resize', () => {
      if (this.state.opened) resizeCharts();
    });
    window.addEventListener('beforeunload', (event: BeforeUnloadEvent) => {
      if (this.hasUnsaved() && !this.session?.cacheCleared) {
        event.preventDefault();
      }
    });
    document.addEventListener('excellens:analysisnumberchange', () => {
      if (this.state.opened && this.state.activeResult && this.state.activeResultQuery) {
        this.pivotView.renderMetrics(this.state.activeResult, this.state.activeResultQuery);
        this.pivotView.renderPivot(this.state.pivotLimit || 150);
      }
    });
  }
}

export function createAnalysisWorkspace(
  options: AnalysisWorkspaceOptions,
): AnalysisWorkspace | null {
  const root = byId('analysisRoot');
  if (!root) return null;
  return new AnalysisWorkspace(root, options);
}
