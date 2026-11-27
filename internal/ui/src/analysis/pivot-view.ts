/**
 * Focused Pivot / Metrics / Chart / Tree / Drill-down rendering view and
 * shared query/metric formatting helpers for the Analysis workspace.
 */
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
  DrillResult,
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

export const AGGREGATIONS: readonly (readonly [AnalysisAggregateKind, string])[] = Object.freeze([
  ['sum', '求和'] as const,
  ['count', '计数'] as const,
  ['average', '平均'] as const,
  ['min', '最小'] as const,
  ['max', '最大'] as const,
  ['distinct', '去重计数'] as const,
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

export type RestorableAnalysisQuery = Partial<WorkspaceAnalysisQuery> & {
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
  const base = m.name || `${m.field ? lookup(m.field) : '记录数'} · ${aggLabel}`;
  return m.comparison ? `${base} · ${comparisonLabel(m)}` : base;
}

export function queryScopeCaption(q: Pick<AnalysisQuery, 'composition' | 'sourceScope'>): string {
  return q.composition
    ? `${q.composition.inputs.length} 个来源`
    : sourceScopeCaption(q.sourceScope);
}

export function restoreReportQuery(query: RestorableAnalysisQuery): EditableAnalysisQuery {
  const raw = JSON.parse(JSON.stringify(query)) as RestorableAnalysisQuery;
  const rows = Array.isArray(raw.rows) ? [...raw.rows] : [];
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
    if (!id) return '记录数';
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
    setText('analysisReportTitle', '透视分析');
    setText('analysisReportSubtitle', '从左侧拖入字段，或从模板库快速开始。');
    setText('analysisChartSummary', '');
    setText('analysisTableSummary', '尚未运行分析');
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
      '记录数';
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
          '分析工作表'));
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
      source: `${state.workspace?.workbook || caption || '当前工作簿'} / ${sourceName}`,
      scope: `${queryScopeCaption(q)}${q.filters?.length ? ' · 已筛选' : ''}`,
      metric: metricLabel(metric, lookup),
      unit: percent
        ? '百分比'
        : metric.aggregate === 'count' || metric.aggregate === 'distinct'
          ? '个'
          : '源指标原单位（未换算）',
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
          `${result.tree ? '本层范围汇总' : '全量汇总'} · ${i + 1}`,
          comparisonTitle(cell),
        ] as const;
      });
    stats.push(
      ['参与记录', formatCountNumber(result.included), '实际纳入统计', String(result.included)],
      [
        result.tree ? '首层分类' : '分类数',
        formatCountNumber(result.rows.length),
        '透视结果行',
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
    setText('analysisReportTitle', q.title || '透视分析');
    setText(
      'analysisReportSubtitle',
      `${queryScopeCaption(q)} · ${result.tree ? '层级展开 · ' : ''}${q.rows.map((id) => this.resultFieldName(id)).join(' / ') || '全部记录'} · ${measures.map((m) => this.resultMetricCaption(m)).join(' / ')}`,
    );
    this.renderMetrics(result, q);
    const alerts: string[] = [];
    if (result.invalidNumbers) {
      alerts.push(`${formatCountNumber(result.invalidNumbers)} 个非数值参与字段未计入数值聚合`);
    }
    if (result.invalidConversions) {
      alerts.push(
        `扫描中有 ${formatCountNumber(result.invalidConversions)} 个值转换失败，转换列保留原文；请检查输入格式`,
      );
    }
    if (result.missingFormulaCaches) {
      alerts.push(`${formatCountNumber(result.missingFormulaCaches)} 个公式缺少缓存值，已排除`);
    }
    if (result.truncated) alerts.push('仅显示排名前 N 项，合计仍为全量结果');
    const cmpScope = comparisonScope(q);
    if (cmpScope) alerts.push(cmpScope);
    if (
      result.rows.some((row) =>
        row.values.some((c) => /^[+-]?\d{16,}(?:\.|$)/.test(c.exact)),
      )
    ) {
      alerts.push('图表按屏幕像素近似绘制；透视表及导出保留精确数值');
    }
    setText('analysisChartSummary', alerts.join('；') || '点击图表或透视数值查看来源记录');
    setText(
      'analysisTableSummary',
      `${formatCountNumber(result.rows.length)} 个分类 · ${formatCountNumber(result.included)} 条记录`,
    );
    const saveBtn = requireButton('analysisSaveReport');
    const matBtn = requireButton('analysisMaterialize');
    const matDetailBtn = requireButton('analysisMaterializeDetails');
    const expBtn = requireButton('analysisExport');
    saveBtn.disabled = false;
    matBtn.disabled = false;
    matDetailBtn.disabled = false;
    expBtn.disabled = false;
    saveBtn.textContent = state.activeReportID ? '更新报表' : '保存报表';
    matBtn.textContent = result.tree ? '首层编辑表' : '生成编辑表';
    expBtn.textContent = result.tree ? '导出首层' : '导出结果';
    setText(
      'analysisTableHint',
      result.tree ? '箭头展开分组，数值查看来源明细' : '点击数值查看对应明细 ↗',
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
      toast('本次分析存在需核对项目，请查看图表上方提示', 'warning');
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
        page.last ? `${formatCountNumber(page.first)}–${formatCountNumber(page.last)} 行` : '暂无分组',
      );
      setText(
        'analysisTableSummary',
        `${formatCountNumber(result.rows.length)} 个首层分组 · ${formatCountNumber(result.included)} 条记录`,
      );
      return;
    }

    table.replaceChildren();
    table.removeAttribute('role');
    table.removeAttribute('aria-label');
    const measures = effectiveMetrics(q);
    const withColumns = Boolean(q.columns.length);
    const dimCount = Math.max(1, q.rows.length);
    const headers: string[] = q.rows.length ? q.rows.map((id) => this.resultFieldName(id)) : ['分类'];
    if (withColumns) {
      for (const k of result.columnKeys) {
        for (const m of measures) {
          headers.push(`${k || '（空白）'} · ${this.resultMetricCaption(m)}`);
        }
      }
      for (const m of measures) {
        headers.push(`合计 · ${this.resultMetricCaption(m)}`);
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
        btn.title = `${td.title} · 查看当前分组来源明细`;
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
      const keys = q.rows.length ? row.keys : ['全部'];
      for (const k of keys) {
        const td = document.createElement('td');
        td.textContent = k || '（空白）';
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
      td.textContent = i === 0 ? '全量总计' : '';
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
      button.textContent = `继续显示（还有 ${formatCountNumber(result.rows.length - rowLimit)} 行）`;
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
        c.snapshot.query.title || '透视分析',
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
    setText('analysisDrillTitle', `${keys.join(' / ') || '全部记录'}${colKey ? ` · ${colKey}` : ''}`);
    setText('analysisDrillInfo', '正在读取源数据…');
    setText('analysisDrillTable', '');
    setText('analysisDrillPaging', '');
    try {
      const data = await api<DrillResult>('/api/analysis/drill', {
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
      });
      if (!current() || !isDrawerVisible()) return;
      const rows = data.rows ?? [];
      setText(
        'analysisDrillInfo',
        `已扫描 ${formatCountNumber(data.scanned)} 行 · ${
          rows.length ? `显示第 ${offset + 1}–${offset + rows.length} 条` : '没有匹配明细'
        }`,
      );
      const wrap = requireEl('analysisDrillTable');
      const table = document.createElement('table');
      const head = document.createElement('thead');
      const hr = document.createElement('tr');
      for (const title of [
        ...(q.composition ? ['来源文件', '来源表'] : []),
        '原行号',
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
        position.title = '转到来源行';
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
      prev.textContent = '上一页';
      prev.disabled = offset === 0;
      prev.onclick = (): void => {
        void this.drill(keys, colKey, Math.max(0, offset - 100), columnSelected);
      };
      paging.append(prev);
      const next = document.createElement('button');
      next.type = 'button';
      next.textContent = '下一页';
      next.disabled = !data.hasMore;
      next.onclick = (): void => {
        void this.drill(keys, colKey, offset + 100, columnSelected);
      };
      paging.append(next);
    } catch (err: unknown) {
      if (current() && isDrawerVisible() && !isAbortError(err)) {
        const msg = errorMessage(err);
        setText('analysisDrillInfo', msg);
        toast(`下钻失败：${msg}`, 'error');
      }
    }
  }
}
