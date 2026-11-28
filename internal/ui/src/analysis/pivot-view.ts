/**
 * Focused Pivot / Metrics / Chart / Tree / Drill-down rendering view and
 * shared query/metric formatting helpers for the Analysis workspace.
 */
import { t as localize } from '../core/i18n.ts';
import type {
  AnalysisAggregateKind,
  AnalysisCell,
  AnalysisField,
  AnalysisFilter,
  AnalysisMetric,
  AnalysisQuery,
  AnalysisResult,
  AnalysisSortOrder,
  AnalysisTransform,
  AnalysisWorkspaceResponse,
  DrillRow,
} from '../contracts/analysis.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import { errorMessage, escapeHtml } from '../core/display-format.ts';
import { byId, queryButton, requireButton, requireEl, requireSelect } from '../core/dom.ts';
import { openChartExport, revalidateChartExport } from './chart-export.ts';
import { drawChart, isChartMode, type ChartMode } from './charts.ts';
import { comparisonLabel, comparisonScope, comparisonTitle } from './comparisons.ts';
import type { AnalysisIssues } from './issues.ts';
import { formatAnalysisNumber } from './numbers.ts';
import {
  metricCell,
  projectMetric,
  type AnalysisResultSnapshot,
  type WorkspaceAnalysisQuery,
} from './results.ts';
import type { SnapshotsController } from './snapshots.ts';
import { sourceScopeCaption } from './sources.ts';
import type { AnalysisTree, AnalysisTreeRenderer } from './tree.ts';
import { decodeDrillResult } from '../core/http-decoders.generated.ts';

export const AGGREGATIONS: readonly (readonly [AnalysisAggregateKind, string])[] = Object.freeze([
  ['sum', localize('ui.common.sum')] as const,
  ['count', localize('ui.common.count')] as const,
  ['average', localize('ui.common.average')] as const,
  ['min', localize('ui.common.minimum')] as const,
  ['max', localize('ui.common.maximum')] as const,
  ['distinct', localize('ui.pivotView.deductionDuplicateCounts')] as const,
]);

export interface EditableAnalysisQuery extends WorkspaceAnalysisQuery {
  source: string;
  headerRow: number;
  rows: string[];
  columns: string[];
  value: string;
  aggregate: AnalysisAggregateKind;
  metrics: AnalysisMetric[];
  filters: AnalysisFilter[];
  transforms: AnalysisTransform[];
  sort: AnalysisSortOrder;
  top: number;
  precision: number;
  chart: string;
  chartMetric: number;
  layout: '' | 'tree';
}

export type RestorableAnalysisQuery = Partial<Omit<WorkspaceAnalysisQuery, 'rows'>> & {
  readonly rows?: readonly string[] | null;
  readonly source: string;
};

export interface ChartCaptureState {
  readonly current: () => boolean;
  readonly snapshot: AnalysisResultSnapshot;
  readonly canvas: HTMLCanvasElement;
  readonly projected: AnalysisResult;
  readonly mode: ChartMode;
  readonly metric: number;
}

export interface AnalysisPivotViewState {
  opened: boolean;
  tab: string;
  fingerprint: string;
  book: string;
  openSerial: number;
  reportOpenSerial: number;
  reportPending: number;
  jobID: string;
  activeReportID: string;
  chartBusy: boolean;
  pivotLimit: number;
  workspace: Pick<AnalysisWorkspaceResponse, 'workbook' | 'sheets'> | null;
  query: EditableAnalysisQuery | null;
  activeResult: AnalysisResult | null;
  activeResultQuery: EditableAnalysisQuery | null;
  activeResultFields: AnalysisField[];
  chartCapture: ChartCaptureState | null;
  drillQuery: EditableAnalysisQuery | null;
  drillJob: string;
  drillKeys: string[];
  drillColumn: string;
  drillColumnSelected: boolean | undefined;
  drillOffset: number;
  drillAbort: AbortController | null;
}

export interface AnalysisPivotViewDeps {
  readonly state: AnalysisPivotViewState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly setText: (id: string, text: string) => void;
  readonly tree: AnalysisTree;
  readonly treeRenderer: AnalysisTreeRenderer;
  readonly issuePanel: AnalysisIssues | null;
  readonly snapshots: SnapshotsController | null;
  readonly navigateComposition: (
    record: DrillRow,
    column: number | undefined,
    job?: string,
  ) => Promise<void>;
  readonly navigateSheet: (id: string, row: number, column: number) => Promise<void>;
  readonly navigateWorkbook: (sheet: string, ref: string) => void;
  readonly close: () => void;
  readonly drawChart?: typeof drawChart;
  readonly openChartExport?: typeof openChartExport;
  readonly revalidateChartExport?: typeof revalidateChartExport;
}

const VALID_AGGREGATES = new Set<string>(['sum', 'count', 'average', 'min', 'max', 'distinct']);
const VALID_SORTS = new Set<string>(['', 'value_desc', 'value_asc', 'label_asc', 'label_desc']);

function parseAggregateOrDefault(value: string | undefined): AnalysisAggregateKind {
  if (value && VALID_AGGREGATES.has(value)) return value as AnalysisAggregateKind;
  return 'count';
}

function parseSortOrDefault(value: string | undefined): AnalysisSortOrder {
  if (value && VALID_SORTS.has(value)) return value as AnalysisSortOrder;
  return 'value_desc';
}

function requireCanvas(id: string): HTMLCanvasElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLCanvasElement)) {
    throw new Error(`Expected HTMLCanvasElement for #${id}`);
  }
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export function formatCountNumber(n: unknown): string {
  const num = Number(n);
  if (!Number.isFinite(num)) {
    if (n === null || n === undefined) return '—';
    return typeof n === 'string' || typeof n === 'number' || typeof n === 'boolean'
      ? String(n)
      : '—';
  }
  const text = String(num);
  return formatAnalysisNumber({ exact: text, display: text });
}

export function effectiveMetrics(
  q: Pick<AnalysisQuery, 'metrics' | 'value' | 'aggregate'> | null | undefined,
): [AnalysisMetric, ...AnalysisMetric[]] {
  const metrics = q?.metrics;
  if (metrics?.length) {
    const [first, ...rest] = metrics;
    if (first) return [first, ...rest];
  }
  return [{ field: q?.value || '', aggregate: q?.aggregate ?? 'count', name: '' }];
}

export function metricLabel(m: AnalysisMetric, lookup: (id: string) => string): string {
  const aggLabel = AGGREGATIONS.find((a) => a[0] === m.aggregate)?.[1] ?? m.aggregate;
  const base = m.name || `${m.field ? lookup(m.field) : localize('ui.common.numberRecords')} · ${aggLabel}`;
  return m.comparison ? `${base} · ${comparisonLabel(m)}` : base;
}

export function queryScopeCaption(q: Pick<AnalysisQuery, 'composition' | 'sourceScope'>): string {
  return q.composition
    ? localize('ui.pivotView.sources', { count: q.composition.inputs.length })
    : sourceScopeCaption(q.sourceScope);
}

export function restoreReportQuery(query: RestorableAnalysisQuery): EditableAnalysisQuery {
  const raw = structuredClone(query);
  const rows = [...(raw.rows ?? [])];
  const columns = Array.isArray(raw.columns) ? [...raw.columns] : [];
  const metrics = Array.isArray(raw.metrics) ? raw.metrics.map((m) => ({ ...m })) : [];
  const filters = Array.isArray(raw.filters) ? raw.filters.map((f) => ({ ...f })) : [];
  const transforms = Array.isArray(raw.transforms) ? raw.transforms.map((t) => ({ ...t })) : [];
  // Go omits zero-valued scalar settings. A saved precision/top of zero
  // must survive restoration instead of adopting new-query defaults.
  return {
    ...raw,
    source: raw.source,
    rows,
    columns,
    metrics,
    filters,
    transforms,
    value: raw.value ?? '',
    aggregate: parseAggregateOrDefault(raw.aggregate),
    top: raw.top ?? 0,
    precision: raw.precision ?? 0,
    headerRow: raw.composition ? 0 : raw.headerRow || 1,
    chart: raw.chart || 'bar',
    chartMetric: raw.chartMetric ?? 0,
    sort: parseSortOrDefault(raw.sort),
    layout: raw.layout === 'tree' ? 'tree' : '',
  };
}

export class AnalysisPivotView {
  private readonly d: AnalysisPivotViewDeps;

  constructor(deps: AnalysisPivotViewDeps) {
    this.d = deps;
  }

  resultFieldName(id: string | undefined): string {
    if (!id) return localize('ui.common.numberRecords');
    const { state } = this.d;
    return (
      state.activeResultQuery?.transforms.find((field) => field.id === id)?.name ||
      state.activeResultFields.find((field) => field.id === id)?.name ||
      id
    );
  }

  resultMetricCaption(m: AnalysisMetric): string {
    return metricLabel(m, (id) => this.resultFieldName(id));
  }

  bindTreeResult(): void {
    const { state, tree } = this.d;
    if (!state.activeResult || !state.activeResultQuery) {
      tree.setRoot(null);
      return;
    }
    tree.setRoot({
      bookKey: state.fingerprint,
      book: state.book,
      job: state.jobID,
      query: state.activeResultQuery,
      result: state.activeResult,
    });
  }

  clearResultDisplay(): void {
    const { state, setText, tree, snapshots } = this.d;
    snapshots?.changed();
    const snapshotSave = queryButton('analysisSnapshotSave');
    if (snapshotSave) snapshotSave.disabled = true;
    state.chartCapture = null;
    revalidateChartExport();
    requireButton('analysisChartImage').disabled = true;
    tree.reset();
    requireEl('analysisTreeTools').hidden = true;
    requireEl('analysisMetrics').hidden = true;
    requireEl('analysisPivotTable').replaceChildren();
    drawChart(requireCanvas('analysisChart'), null);
    requireEl('analysisChartHint').hidden = false;
    setText('analysisReportTitle', localize('ui.common.p'));
    setText('analysisReportSubtitle', localize('ui.pivotView.dragFieldsFromLeftOr'));
    setText('analysisChartSummary', '');
    setText('analysisTableSummary', localize('ui.pivotView.noAnalysisRunYet'));
    for (const id of [
      'analysisSaveReport',
      'analysisMaterialize',
      'analysisMaterializeDetails',
      'analysisExport',
    ] as const) {
      requireButton(id).disabled = true;
    }
  }

  openResultImage(
    snapshot: AnalysisResultSnapshot,
    canvas: HTMLCanvasElement,
    expectedResult: AnalysisResult,
    mode: ChartMode,
    index: number,
    title: string,
    current: () => boolean,
  ): void {
    const { state, toast } = this.d;
    const q = snapshot.query;
    const fields = snapshot.sourceFields ?? [];
    const lookup = (id: string): string =>
      q.transforms?.find((f) => f.id === id)?.name ||
      fields.find((f) => f.id === id)?.name ||
      id ||
      localize('ui.common.numberRecords');
    const measures = effectiveMetrics(q);
    const metric = measures[index] ?? measures[0];
    const percent = expectedResult.rows.some((row) =>
      row.values.some((cell) => cell.comparison?.percent === true),
    );
    const sourceName =
      q.composition?.name ||
      (q.source.startsWith('wb:')
        ? q.source.slice(3)
        : (state.workspace?.sheets.find((sheet) => `scratch:${sheet.id}` === q.source)?.name ??
          localize('ui.pivotView.analyzeWorksheet')));
    const caption = byId('analysisSourceCaption')?.textContent ?? '';
    (this.d.openChartExport ?? openChartExport)({
      kind: 'analysis',
      canvas,
      expectedResult,
      mode,
      metricIndex: index,
      result: snapshot.result,
      query: q,
      fields,
      origin: {
        workbookKey: state.fingerprint,
        book: state.book,
        job: snapshot.job,
        source: q.source,
      },
      title,
      source: `${state.workspace?.workbook || caption || localize('ui.common.currentWorkbook')} / ${sourceName}`,
      scope: `${queryScopeCaption(q)}${q.filters?.length ? ` ${localize('ui.pivotView.filtered')}` : ''}`,
      metric: metricLabel(metric, lookup),
      unit: percent
        ? localize('ui.pivotView.percentage')
        : metric.aggregate === 'count' || metric.aggregate === 'distinct'
          ? localize('ui.pivotView.individual')
          : localize('ui.pivotView.originalUnits'),
      current,
      notify: toast,
    });
  }

  renderMetrics(result: AnalysisResult, q: WorkspaceAnalysisQuery): void {
    const stats: (readonly [string, string, string, string])[] = effectiveMetrics(q)
      .slice(0, 6)
      .map((m, i) => {
        const cell = metricCell(result, null, 0, i, true);
        return [
          this.resultMetricCaption(m),
          formatAnalysisNumber(cell),
          `${result.tree ? localize('ui.pivotView.summaryThisLayer') : localize('ui.pivotView.fullSummary')} · ${i + 1}`,
          comparisonTitle(cell),
        ] as const;
      });
    stats.push(
      [localize('ui.pivotView.participatingRecords'), formatCountNumber(result.included), localize('ui.pivotView.actualInclusionStatistics'), String(result.included)],
      [
        result.tree ? localize('ui.pivotView.firstLevelClassification') : localize('ui.pivotView.numberCategories'),
        formatCountNumber(result.rows.length),
        localize('ui.pivotView.perspectiveResultRow'),
        String(result.rows.length),
      ],
    );
    const el = requireEl('analysisMetrics');
    el.replaceChildren();
    for (const [name, value, note, exact] of stats) {
      const box = document.createElement('div');
      box.className = 'analysis-metric';
      box.innerHTML = `<div class="analysis-metric-label">${escapeHtml(name)}</div><div class="analysis-metric-value" title="${escapeHtml(exact)}">${escapeHtml(value)}</div><div class="analysis-metric-note">${escapeHtml(note)}</div>`;
      el.append(box);
    }
    el.hidden = false;
  }

  renderResult(result: AnalysisResult, q: WorkspaceAnalysisQuery): void {
    const { state, setText, toast, issuePanel } = this.d;
    issuePanel?.setSnapshot({ book: state.fingerprint, job: state.jobID, query: q, result });
    const measures = effectiveMetrics(q);
    setText('analysisReportTitle', q.title || localize('ui.common.p'));
    setText(
      'analysisReportSubtitle',
      `${queryScopeCaption(q)} · ${result.tree ? `${localize('ui.pivotView.expandLevels')} ` : ''}${q.rows.map((id) => this.resultFieldName(id)).join(' / ') || localize('ui.pivotView.allRecordsRecorded')} · ${measures.map((m) => this.resultMetricCaption(m)).join(' / ')}`,
    );
    this.renderMetrics(result, q);
    const alerts: string[] = [];
    if (result.invalidNumbers) {
      alerts.push(localize('ui.pivotView.nonNumericFieldsWereNot', { formatCountNumber: formatCountNumber(result.invalidNumbers) }));
    }
    if (result.invalidConversions) {
      alerts.push(
        localize('ui.pivotView.duringScanningValuesFailedBe', { formatCountNumber: formatCountNumber(result.invalidConversions) }),
      );
    }
    if (result.missingFormulaCaches) {
      alerts.push(localize('ui.pivotView.formulaLacksCachedValuesRuled', { formatCountNumber: formatCountNumber(result.missingFormulaCaches) }));
    }
    if (result.truncated) alerts.push(localize('ui.pivotView.onlyTopNItemsDisplayed'));
    const cmpScope = comparisonScope(q);
    if (cmpScope) alerts.push(cmpScope);
    setText('analysisChartSummary', alerts.join('；') || localize('ui.pivotView.clickOnChartsOrPivot'));
    setText(
      'analysisTableSummary',
      localize('ui.pivotView.categoriesRecords', { count: formatCountNumber(result.rows.length), formatCountNumber: formatCountNumber(result.included) }),
    );
    const saveBtn = requireButton('analysisSaveReport');
    const matBtn = requireButton('analysisMaterialize');
    const matDetailBtn = requireButton('analysisMaterializeDetails');
    const expBtn = requireButton('analysisExport');
    saveBtn.disabled = false;
    matBtn.disabled = false;
    matDetailBtn.disabled = false;
    expBtn.disabled = false;
    saveBtn.textContent = state.activeReportID ? localize('ui.pivotView.updateReports') : localize('ui.pivotView.saveReport');
    matBtn.textContent = result.tree ? localize('ui.pivotView.firstLevelEditingTable') : localize('ui.pivotView.generatingEditedTable');
    expBtn.textContent = result.tree ? localize('ui.pivotView.firstLayerExport') : localize('ui.pivotView.exportResults');
    setText(
      'analysisTableHint',
      result.tree ? localize('ui.pivotView.arrowsExpandGroupingAllowingView') : localize('ui.pivotView.clickOnValueViewCorresponding'),
    );

    const chartMetric = requireSelect('analysisChartMetric');
    const chartLabel = requireEl('analysisChartMetricLabel');
    chartMetric.replaceChildren();
    measures.forEach((m, i) => {
      chartMetric.append(new Option(this.resultMetricCaption(m), String(i)));
    });
    chartMetric.value = String(Math.min(measures.length - 1, Math.max(0, Number(q.chartMetric) || 0)));
    chartMetric.hidden = chartLabel.hidden = measures.length <= 1;
    requireEl('analysisChartHint').hidden = Boolean(result.rows.length);
    this.renderPivot();
    this.renderChart();
    if (result.invalidNumbers || result.missingFormulaCaches || result.invalidConversions) {
      toast(localize('ui.pivotView.thereItemsThatNeedVerification'), 'warning');
    }
  }

  renderPivot(rowLimit = 150): void {
    const { state, setText, tree, treeRenderer } = this.d;
    const result = state.activeResult;
    const q = state.activeResultQuery;
    const table = requireEl('analysisPivotTable');
    if (!result || !q) {
      table.replaceChildren();
      return;
    }
    state.pivotLimit = rowLimit;
    requireEl('analysisTreeTools').hidden = !result.tree;
    if (result.tree) {
      this.bindTreeResult();
      const page = treeRenderer.render(table, tree, {
        fieldName: (id) => this.resultFieldName(id),
        metricName: (m) => this.resultMetricCaption(m),
        metricCell,
        drill: (keys, colKey, offset, columnSelected) => {
          void this.drill(keys, colKey, offset, columnSelected);
        },
        formatNumber: formatAnalysisNumber,
      });
      requireButton('analysisTreePrevious').disabled = tree.page === 0;
      requireButton('analysisTreeNext').disabled = !page.more;
      setText(
        'analysisTreePage',
        page.last ? localize('ui.pivotView.row', { formatCountNumber: formatCountNumber(page.first), formatCountNumber2: formatCountNumber(page.last) }) : localize('ui.pivotView.noGroupingAvailable'),
      );
      setText(
        'analysisTableSummary',
        localize('ui.pivotView.groupsRecords', { count: formatCountNumber(result.rows.length), formatCountNumber: formatCountNumber(result.included) }),
      );
      return;
    }

    table.replaceChildren();
    table.removeAttribute('role');
    table.removeAttribute('aria-label');
    const measures = effectiveMetrics(q);
    const withColumns = Boolean(q.columns.length);
    const dimCount = Math.max(1, q.rows.length);
    const headers: string[] = q.rows.length ? q.rows.map((id) => this.resultFieldName(id)) : [localize('ui.common.classification')];
    if (withColumns) {
      for (const k of result.columnKeys) {
        for (const m of measures) {
          headers.push(`${k || localize('ui.common.empty')} · ${this.resultMetricCaption(m)}`);
        }
      }
      for (const m of measures) {
        headers.push(localize('ui.pivotView.total', { resultMetricCaption: this.resultMetricCaption(m) }));
      }
    } else {
      for (const m of measures) {
        headers.push(this.resultMetricCaption(m));
      }
    }

    const head = document.createElement('thead');
    const headerRow = document.createElement('tr');
    for (const name of headers) {
      const th = document.createElement('th');
      th.textContent = name;
      headerRow.append(th);
    }
    head.append(headerRow);
    table.append(head);

    const body = document.createElement('tbody');
    const visible = result.rows.slice(0, rowLimit);
    const addValue = (
      tr: HTMLTableRowElement,
      cell: AnalysisCell,
      keys: readonly string[],
      colKey: string,
      clickable: boolean,
    ): void => {
      const td = document.createElement('td');
      td.className = 'analysis-number';
      td.title = comparisonTitle(cell);
      if (clickable && cell.exact !== '') {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'analysis-drill-link';
        btn.textContent = formatAnalysisNumber(cell);
        btn.title = localize('ui.pivotView.viewCurrentGroupSourceDetails', { title: td.title });
        btn.onclick = (): void => {
          void this.drill(keys, colKey, 0);
        };
        td.append(btn);
      } else {
        td.textContent = formatAnalysisNumber(cell);
      }
      tr.append(td);
    };

    for (const row of visible) {
      const tr = document.createElement('tr');
      const keys = q.rows.length ? row.keys : [localize('ui.common.all')];
      for (const k of keys) {
        const td = document.createElement('td');
        td.textContent = k || localize('ui.common.empty');
        tr.append(td);
      }
      if (withColumns) {
        result.columnKeys.forEach((colKey, j) => {
          measures.forEach((_, k) => {
            addValue(tr, metricCell(result, row, j, k), row.keys, colKey, true);
          });
        });
      }
      measures.forEach((_, k) => {
        addValue(tr, metricCell(result, row, 0, k, true), row.keys, '', !withColumns);
      });
      body.append(tr);
    }
    table.append(body);

    const foot = document.createElement('tfoot');
    const footer = document.createElement('tr');
    for (let i = 0; i < dimCount; i++) {
      const td = document.createElement('td');
      td.textContent = i === 0 ? localize('ui.pivotView.totalAmount') : '';
      footer.append(td);
    }
    if (withColumns) {
      result.columnKeys.forEach((_, j) => {
        measures.forEach((_, k) => {
          addValue(footer, metricCell(result, null, j, k), [], '', false);
        });
      });
    }
    measures.forEach((_, k) => {
      addValue(footer, metricCell(result, null, 0, k, true), [], '', false);
    });
    foot.append(footer);
    table.append(foot);

    if (result.rows.length > rowLimit) {
      const extra = document.createElement('tr');
      const cell = document.createElement('td');
      cell.colSpan = headers.length;
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'analysis-quiet-button';
      button.textContent = localize('ui.pivotView.continueDisplayingThereStillRows', { formatCountNumber: formatCountNumber(result.rows.length - rowLimit) });
      button.onclick = (): void => this.renderPivot(Math.min(result.rows.length, rowLimit + 150));
      cell.append(button);
      extra.append(cell);
      body.append(extra);
    }
  }

  renderChart(): void {
    const { state, snapshots } = this.d;
    const result = state.activeResult;
    const q = state.activeResultQuery;
    if (!state.opened || state.tab !== 'pivot' || !result || !q) return;
    const book = state.fingerprint;
    const path = state.book;
    const opened = state.openSerial;
    const job = state.jobID;
    const metricSelect = requireSelect('analysisChartMetric');
    const chartSelect = requireSelect('analysisChartSelect');
    const metric = Math.max(
      0,
      Math.min(effectiveMetrics(q).length - 1, Number(metricSelect.value) || 0),
    );
    const rawMode = chartSelect.value;
    const mode: ChartMode = isChartMode(rawMode) ? rawMode : 'bar';
    const current = (): boolean =>
      state.opened &&
      state.tab === 'pivot' &&
      state.activeResult === result &&
      state.activeResultQuery === q &&
      state.fingerprint === book &&
      state.book === path &&
      state.openSerial === opened &&
      state.jobID === job &&
      !state.chartBusy &&
      state.reportPending !== state.reportOpenSerial &&
      chartSelect.value === rawMode &&
      (Number(metricSelect.value) || 0) === metric;
    state.chartCapture = null;
    requireButton('analysisChartImage').disabled = true;
    const snapSave = queryButton('analysisSnapshotSave');
    if (snapSave) snapSave.disabled = true;
    (this.d.revalidateChartExport ?? revalidateChartExport)();
    requestAnimationFrame(() => {
      if (!current()) return;
      const projected = projectMetric(result, metric);
      const canvas = requireCanvas('analysisChart');
      (this.d.drawChart ?? drawChart)(canvas, projected, mode, (keys, colKey) => {
        void this.drill(keys, colKey, 0, Boolean(q.columns.length));
      });
      state.chartCapture = {
        current,
        snapshot: { book, query: q, result, job, sourceFields: state.activeResultFields },
        canvas,
        projected,
        mode,
        metric,
      };
      requireButton('analysisChartImage').disabled = false;
      const saveButton = queryButton('analysisSnapshotSave');
      if (saveButton) saveButton.disabled = false;
      snapshots?.changed();
    });
  }

  exportChartImage(): void {
    const c = this.d.state.chartCapture;
    if (c?.current()) {
      this.openResultImage(
        c.snapshot,
        c.canvas,
        c.projected,
        c.mode,
        c.metric,
        c.snapshot.query.title || localize('ui.common.p'),
        c.current,
      );
    }
  }

  setChartMode(): void {
    const { state } = this.d;
    const mode = requireSelect('analysisChartSelect').value;
    if (state.query) state.query.chart = mode;
    if (state.activeResultQuery) state.activeResultQuery.chart = mode;
    this.renderChart();
  }

  async drill(
    keys: readonly string[],
    colKey: string,
    offset = 0,
    columnSelected?: boolean,
  ): Promise<void> {
    const {
      state,
      api,
      toast,
      setText,
      navigateComposition,
      navigateSheet,
      navigateWorkbook,
      close,
    } = this.d;
    const q = state.activeResultQuery;
    if (!q) return;
    state.drillAbort?.abort();
    const controller = new AbortController();
    state.drillAbort = controller;
    const jobId = state.jobID;
    const book = state.fingerprint;
    const pathBook = state.book;
    const current = (): boolean =>
      state.opened &&
      !controller.signal.aborted &&
      state.drillAbort === controller &&
      state.activeResultQuery === q &&
      state.jobID === jobId &&
      book === state.fingerprint &&
      pathBook === state.book;
    const drawer = requireEl('analysisDrill');
    const isDrawerVisible = (): boolean => !drawer.hidden;
    drawer.hidden = false;
    state.drillQuery = q;
    state.drillJob = jobId;
    state.drillKeys = [...keys];
    state.drillColumn = colKey;
    state.drillColumnSelected = columnSelected;
    state.drillOffset = offset;
    setText('analysisDrillTitle', `${keys.join(' / ') || localize('ui.pivotView.allRecordsRecorded')}${colKey ? ` · ${colKey}` : ''}`);
    setText('analysisDrillInfo', localize('ui.pivotView.readingSourceData'));
    setText('analysisDrillTable', '');
    setText('analysisDrillPaging', '');
    try {
      const data = await api('/api/analysis/drill', {
        method: 'POST',
        body: JSON.stringify({
          query: q,
          jobId,
          keys,
          columnKey: colKey,
          ...(columnSelected !== undefined ? { columnSelected } : {}),
          offset,
          limit: 100,
        }),
        signal: controller.signal,
      }, decodeDrillResult);
      if (!current() || !isDrawerVisible()) return;
      const rows = data.rows ?? [];
      setText(
        'analysisDrillInfo',
        localize('ui.pivotView.scannedRows', { formatCountNumber: formatCountNumber(data.scanned), detail: rows.length ? localize('ui.pivotView.displayThItem', { offset: offset + 1, count: offset + rows.length }) : localize('ui.pivotView.noMatchingDetails') }),
      );
      const wrap = requireEl('analysisDrillTable');
      const table = document.createElement('table');
      const head = document.createElement('thead');
      const hr = document.createElement('tr');
      for (const title of [
        ...(q.composition ? [localize('ui.pivotView.sourceFiles'), localize('ui.pivotView.sourceTable')] : []),
        localize('ui.pivotView.originalLineNumber'),
        ...data.fields.map((x) => x.name),
      ]) {
        const th = document.createElement('th');
        th.textContent = title;
        hr.append(th);
      }
      head.append(hr);
      table.append(head);

      const body = document.createElement('tbody');
      for (const row of rows) {
        const tr = document.createElement('tr');
        const pos = document.createElement('td');
        const position = document.createElement('button');
        position.type = 'button';
        position.className = 'analysis-source-row analysis-drill-link';
        position.textContent = String(row.row);
        position.title = localize('ui.pivotView.transferSourceRow');
        pos.append(position);
        if (q.composition) {
          for (const text of [row.origin?.file, row.origin?.sheet]) {
            const td = document.createElement('td');
            td.className = 'composition-origin';
            td.textContent = text || '';
            tr.append(td);
          }
        }
        position.onclick = (): void => {
          if (q.composition) {
            void navigateComposition(
              row,
              row.origin?.columns.find((column) => column > 0),
              jobId,
            );
          } else if (q.source.startsWith('wb:')) {
            close();
            navigateWorkbook(q.source.slice(3), `A${row.row}`);
          } else {
            void navigateSheet(q.source.slice(8), row.row, 1).catch((error: unknown) => {
              toast(errorMessage(error), 'error');
            });
          }
        };
        tr.append(pos);
        row.cells.forEach((v, i) => {
          const td = document.createElement('td');
          td.title = v;
          const fieldCol = data.fields[i]?.column;
          const originCol =
            q.composition && fieldCol !== undefined && fieldCol > 0
              ? row.origin?.columns[fieldCol - 1]
              : undefined;
          if (originCol !== undefined && originCol > 0) {
            const jump = document.createElement('button');
            jump.type = 'button';
            jump.className = 'analysis-source-row analysis-drill-link';
            jump.textContent = v;
            jump.onclick = (): void => {
              void navigateComposition(row, originCol, jobId);
            };
            td.append(jump);
          } else {
            td.textContent = v;
          }
          tr.append(td);
        });
        body.append(tr);
      }
      table.append(body);
      wrap.replaceChildren(table);

      const paging = requireEl('analysisDrillPaging');
      const prev = document.createElement('button');
      prev.type = 'button';
      prev.textContent = localize('ui.common.previousPage');
      prev.disabled = offset === 0;
      prev.onclick = (): void => {
        void this.drill(keys, colKey, Math.max(0, offset - 100), columnSelected);
      };
      paging.append(prev);
      const next = document.createElement('button');
      next.type = 'button';
      next.textContent = localize('ui.common.nextPage');
      next.disabled = !data.hasMore;
      next.onclick = (): void => {
        void this.drill(keys, colKey, offset + 100, columnSelected);
      };
      paging.append(next);
    } catch (err: unknown) {
      if (current() && isDrawerVisible() && !isAbortError(err)) {
        const msg = errorMessage(err);
        setText('analysisDrillInfo', msg);
        toast(localize('ui.pivotView.detailReadingFailed', { msg }), 'error');
      }
    }
  }
}
