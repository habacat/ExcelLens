/**
 * Analytics Dashboard (lazy-loaded report cards, drag-and-drop ordering) and
 * Metadata-driven Template Gallery controller.
 */
import type {
  AnalysisAggregateKind,
  AnalysisField,
  AnalysisMetric,
  AnalysisReport,
  AnalysisResult,
  AnalysisTransformOp,
  AnalysisWorkspaceResponse,
  SourceScope,
} from '../contracts/analysis.ts';
import type { PostClient, ToastFn } from '../contracts/common.ts';
import { errorMessage, escapeHtml } from '../core/display-format.ts';
import { requireEl } from '../core/dom.ts';
import { revalidateChartExport } from './chart-export.ts';
import { drawChart, isChartMode, type ChartMode } from './charts.ts';
import {
  projectMetric,
  type AnalysisResultCache,
  type AnalysisResultSnapshot,
  type WorkspaceAnalysisQuery,
} from './results.ts';
import type { AnalysisTemplate } from './templates.ts';

export interface MutableAnalysisWorkspace {
  readonly sheets: AnalysisWorkspaceResponse['sheets'];
  readonly reports: AnalysisWorkspaceResponse['reports'];
  dashboardOrder: readonly string[] | null;
  readonly sourceSheets: AnalysisWorkspaceResponse['sourceSheets'];
  readonly workbook: string;
  readonly fingerprint: string;
}

export interface DashboardFieldChoice {
  readonly id: string;
  readonly name: string;
  readonly numeric?: boolean;
}

export interface ExecutedAnalysisQuery {
  readonly result: AnalysisResult;
  readonly job: string;
  readonly sourceFields?: readonly AnalysisField[];
  readonly query?: WorkspaceAnalysisQuery;
  readonly current: () => boolean;
}

export interface AnalysisDashboardState {
  opened: boolean;
  tab: string;
  fingerprint: string;
  book: string;
  openSerial: number;
  source: string;
  activeReportID: string;
  workspace: MutableAnalysisWorkspace | null;
  dashboardObserver: IntersectionObserver | null;
  reportCache: AnalysisResultCache;
  fields: readonly AnalysisField[];
  query: WorkspaceAnalysisQuery | null;
  activeResultQuery: WorkspaceAnalysisQuery | null;
  templateCategory: string;
  templateText: string;
}

export interface AnalysisDashboardDeps {
  readonly root: HTMLElement;
  readonly state: AnalysisDashboardState;
  readonly templates: readonly AnalysisTemplate[];
  readonly post: PostClient;
  readonly toast: ToastFn;
  readonly askText: (title: string, initial?: string, hint?: string) => Promise<string | null>;
  readonly refreshWorkspace: () => Promise<boolean | undefined>;
  readonly openReport: (report: AnalysisReport) => Promise<void>;
  readonly executeQuery: (query: WorkspaceAnalysisQuery) => Promise<ExecutedAnalysisQuery>;
  readonly effectiveMetrics: (
    query: Pick<WorkspaceAnalysisQuery, 'metrics' | 'value' | 'aggregate'> | null | undefined,
  ) => readonly AnalysisMetric[];
  readonly activateResult: (snapshot: AnalysisResultSnapshot) => boolean;
  readonly renderResult: (result: AnalysisResult, query: WorkspaceAnalysisQuery) => void;
  readonly drill: (
    keys: readonly string[],
    colKey: string,
    offset?: number,
    columnSelected?: boolean,
  ) => Promise<void>;
  readonly openResultImage: (
    snapshot: AnalysisResultSnapshot,
    canvas: HTMLCanvasElement,
    expectedResult: AnalysisResult,
    mode: ChartMode,
    index: number,
    title: string,
    current: () => boolean,
  ) => void;
  readonly allFields: () => readonly DashboardFieldChoice[];
  readonly setTab: (tab: 'pivot' | 'dashboard' | 'sheets' | 'templates') => Promise<void> | void;
  readonly queryBase: (source: string) => WorkspaceAnalysisQuery;
  readonly renderFields: () => void;
  readonly renderBuilder: () => void;
  readonly run: () => Promise<void>;
}

interface DashboardChartToken {
  readonly snapshot: AnalysisResultSnapshot;
  readonly mode: ChartMode;
  readonly metric: number;
}

function cloneJson<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

export class AnalysisDashboardController {
  private readonly d: AnalysisDashboardDeps;

  constructor(deps: AnalysisDashboardDeps) {
    this.d = deps;
  }

  renderDashboard(): void {
    const {
      root,
      state,
      post,
      toast,
      askText,
      refreshWorkspace,
      openReport,
      executeQuery,
      effectiveMetrics,
      activateResult,
      renderResult,
      drill,
      openResultImage,
    } = this.d;
    if (!state.opened) return;
    const book = state.fingerprint;
    const pathBook = state.book;
    const opened = state.openSerial;
    const host = requireEl('analysisDashboardCards');
    host.replaceChildren();
    revalidateChartExport();

    const reports = [...(state.workspace?.reports ?? [])].reverse();
    const savedOrder = state.workspace?.dashboardOrder ?? [];
    if (savedOrder.length) {
      reports.sort((a, b) => {
        const ai = savedOrder.indexOf(a.id);
        const bi = savedOrder.indexOf(b.id);
        return ai < 0 && bi < 0 ? 0 : ai < 0 ? 1 : bi < 0 ? -1 : ai - bi;
      });
    }

    // Render all report shells, but only query charts as they approach the
    // viewport. A large dashboard must not sequentially rescan 80 workbooks.
    state.dashboardObserver?.disconnect();
    state.dashboardObserver = null;
    if (!reports.length) {
      const el = document.createElement('div');
      el.className = 'analysis-empty';
      el.textContent = '还没有报表。在“透视分析”里点“保存报表”，报表会出现在这里。';
      host.append(el);
      return;
    }

    const cardLoaders = new WeakMap<Element, () => Promise<void>>();
    const contentScroll = root.querySelector('.analysis-main');
    let lazyObserver: IntersectionObserver | null = null;
    if (typeof IntersectionObserver !== 'undefined') {
      const observer = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            if (entry.isIntersecting) {
              observer.unobserve(entry.target);
              const loader = cardLoaders.get(entry.target);
              if (loader) void loader();
            }
          }
        },
        { root: contentScroll, rootMargin: '200px' },
      );
      lazyObserver = observer;
    }
    state.dashboardObserver = lazyObserver;

    if (state.tab !== 'dashboard') return;
    for (const report of reports) {
      const card = document.createElement('div');
      card.className = 'analysis-dashboard-card';
      card.dataset.reportId = report.id;
      card.draggable = true;
      card.title = '拖动调整仪表盘顺序';
      card.innerHTML = `<div class="analysis-dashboard-card-head"><h3><span class="analysis-dashboard-grip" aria-hidden="true"><svg class="icon icon-small" aria-hidden="true" focusable="false"><use href="#i-grip"/></svg></span><span>${escapeHtml(report.name)}</span></h3><div class="analysis-dashboard-actions"><button type="button" data-report-image disabled title="导出已加载图表及精确附件">图片</button><button type="button" data-report-open>打开 ↗</button><button type="button" class="analysis-dashboard-remove" data-report-remove title="删除该报表" aria-label="删除报表">×</button></div></div><div class="analysis-dashboard-plot"><canvas></canvas><div class="analysis-chart-tooltip" hidden></div></div>`;
      host.append(card);

      card.addEventListener('dragstart', (event: DragEvent) => {
        if (event.target instanceof Element && event.target.closest('button')) {
          event.preventDefault();
          return;
        }
        event.dataTransfer?.setData('text/plain', report.id);
        if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move';
        card.classList.add('is-dragging');
      });
      card.addEventListener('dragend', () => {
        host.querySelectorAll('.analysis-dashboard-card').forEach((c) => {
          c.classList.remove('is-dragging', 'is-drop-target');
        });
      });
      card.addEventListener('dragover', (event: DragEvent) => {
        event.preventDefault();
        card.classList.add('is-drop-target');
      });
      card.addEventListener('dragleave', () => card.classList.remove('is-drop-target'));
      card.addEventListener('drop', (event: DragEvent) => {
        event.preventDefault();
        card.classList.remove('is-drop-target');
        const draggedID = event.dataTransfer?.getData('text/plain') ?? '';
        const selected = draggedID
          ? host.querySelector<HTMLElement>(
              `.analysis-dashboard-card[data-report-id="${CSS.escape(draggedID)}"]`,
            )
          : null;
        if (!selected || selected === card) return;
        const bounds = card.getBoundingClientRect();
        host.insertBefore(
          selected,
          event.clientX < bounds.left + bounds.width / 2 ? card : card.nextSibling,
        );
        const ids = [...host.querySelectorAll<HTMLElement>('.analysis-dashboard-card')]
          .map((el) => el.dataset.reportId ?? '')
          .filter(Boolean);
        void post('/api/analysis/dashboard/order', { ids })
          .then(() => {
            if (state.workspace) state.workspace.dashboardOrder = ids;
            toast('仪表盘顺序已保存', 'success');
          })
          .catch((e: unknown) => {
            toast(`排序保存失败：${errorMessage(e)}`, 'error');
            this.renderDashboard();
          });
      });

      const removeButton = card.querySelector<HTMLButtonElement>('[data-report-remove]');
      if (removeButton) {
        removeButton.onclick = (): void => {
          void (async () => {
            const typed = await askText('删除分析报表', '', `输入“${report.name}”以确认`);
            if (typed !== report.name) return;
            try {
              await post('/api/analysis/delete', { kind: 'report', id: report.id });
              await refreshWorkspace();
              toast('报表已删除', 'success');
            } catch (e: unknown) {
              toast(errorMessage(e), 'error');
            }
          })();
        };
      }

      const openButton = card.querySelector<HTMLButtonElement>('[data-report-open]');
      if (openButton) {
        openButton.onclick = (): void => {
          void openReport(report);
        };
      }

      const info = document.createElement('span');
      info.className = 'analysis-card-loading';
      info.textContent = '滚动到可见区域时加载';
      const plot = card.querySelector('.analysis-dashboard-plot');
      plot?.append(info);

      let chartLoad = 0;
      let chartImageToken: DashboardChartToken | null = null;
      const imageButton = card.querySelector<HTMLButtonElement>('[data-report-image]');
      const canvas = card.querySelector('canvas');

      const isCardConnected = (): boolean => card.isConnected;
      const isDashboardTab = (): boolean => state.tab === 'dashboard';

      const loadCard = async (): Promise<void> => {
        if (!isCardConnected() || !isDashboardTab()) return;
        const load = ++chartLoad;
        const viewQuery: WorkspaceAnalysisQuery = cloneJson({ ...report.query, rows: report.query.rows ?? [] });
        chartImageToken = null;
        if (imageButton) imageButton.disabled = true;
        revalidateChartExport();
        info.textContent = '正在计算…';
        try {
          let snapshot = state.reportCache.get(book, viewQuery);
          if (!snapshot) {
            const fetched = await executeQuery(viewQuery);
            if (!fetched.current() || !isCardConnected()) return;
            snapshot = state.reportCache.set(book, fetched.query ?? viewQuery, fetched);
          }
          if (
            isCardConnected() &&
            isDashboardTab() &&
            book === state.fingerprint &&
            pathBook === state.book &&
            opened === state.openSerial &&
            load === chartLoad &&
            canvas
          ) {
            const rawMode = viewQuery.chart || 'bar';
            const mode: ChartMode = isChartMode(rawMode) ? rawMode : 'bar';
            const metric = Math.min(
              effectiveMetrics(snapshot.query).length - 1,
              Math.max(0, Number(viewQuery.chartMetric) || 0),
            );
            const projected = projectMetric(snapshot.result, metric);
            const token: DashboardChartToken = { snapshot, mode, metric };
            info.remove();
            const activeSnapshot = snapshot;
            drawChart(canvas, projected, mode, (keys, col) => {
              if (activateResult(activeSnapshot) && state.activeResultQuery) {
                renderResult(activeSnapshot.result, state.activeResultQuery);
                void drill(keys, col, 0, Boolean(state.activeResultQuery.columns?.length));
              }
            });
            chartImageToken = token;
            revalidateChartExport();
            const current = (): boolean =>
              state.opened &&
              isDashboardTab() &&
              isCardConnected() &&
              book === state.fingerprint &&
              pathBook === state.book &&
              opened === state.openSerial &&
              chartImageToken === token;
            if (imageButton) {
              imageButton.disabled = false;
              imageButton.onclick = (): void => {
                if (current()) {
                  openResultImage(activeSnapshot, canvas, projected, mode, metric, report.name, current);
                }
              };
            }
          }
        } catch (e: unknown) {
          if (isCardConnected()) {
            info.textContent = `无法更新：${errorMessage(e)}`;
            info.classList.add('analysis-card-error');
          }
        }
      };

      cardLoaders.set(card, loadCard);
      if (lazyObserver) lazyObserver.observe(card);
      else void loadCard();
    }
  }

  findMatch(hints: readonly string[], exclude: readonly string[] = []): DashboardFieldChoice | null {
    const all = this.d.allFields().filter((f) => !exclude.includes(f.id));
    for (const hint of hints) {
      const lower = hint.toLocaleLowerCase();
      const match = all.find((f) => f.name.toLocaleLowerCase().includes(lower));
      if (match) return match;
    }
    return null;
  }

  renderTemplates(): void {
    const { state, templates } = this.d;
    const categories = ['全部', ...new Set(templates.map((t) => t.category))];
    const cats = requireEl('analysisTemplateCategories');
    cats.replaceChildren();
    for (const name of categories) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = name;
      btn.className = name === state.templateCategory ? 'is-active' : '';
      btn.onclick = (): void => {
        state.templateCategory = name;
        this.renderTemplates();
      };
      cats.append(btn);
    }

    const container = requireEl('analysisTemplateGrid');
    container.replaceChildren();
    const search = state.templateText.toLocaleLowerCase();
    for (const t of templates) {
      if (
        (state.templateCategory !== '全部' && t.category !== state.templateCategory) ||
        (search && !`${t.title} ${t.description}`.toLocaleLowerCase().includes(search))
      ) {
        continue;
      }
      const card = document.createElement('button');
      card.type = 'button';
      card.className = 'analysis-template-card';
      card.innerHTML = `<span class="analysis-template-ico">${escapeHtml(t.icon)}</span><strong>${escapeHtml(t.title)}</strong><small>${escapeHtml(t.description)}</small>`;
      card.onclick = (): void => this.applyTemplate(t);
      container.append(card);
    }
  }

  applyTemplate(template: AnalysisTemplate): void {
    const { state, toast, setTab, queryBase, renderFields, renderBuilder, run } = this.d;
    if (!state.fields.length) {
      toast('请先选择一张包含数据的工作表', 'warning');
      void setTab('pivot');
      return;
    }
    state.activeReportID = '';
    const q = queryBase(state.source);
    q.layout = state.query?.layout || '';
    if (state.query?.sourceScope) {
      q.sourceScope = cloneJson<SourceScope>(state.query.sourceScope);
    }
    const main = this.findMatch(template.row);
    // No silent substitution of an unrelated numerical column. Audit and
    // financial templates must never produce a plausible but wrong report.
    const metric = this.findMatch(template.value, main ? [main.id] : []);
    if (main) q.rows = [main.id];
    const column = this.findMatch(template.col, q.rows);
    if (column) q.columns = [column.id];
    const aggregate: AnalysisAggregateKind = template.aggregate;
    q.value = aggregate === 'count' ? '' : (metric?.id ?? '');
    q.aggregate = aggregate;
    const ready = Boolean(main) && (q.aggregate === 'count' || Boolean(metric));
    q.chart = template.chart;
    q.top = template.top || 30;
    q.title = template.title;
    if (
      main &&
      (template.title.includes('月度') || template.title.includes('到期年份')) &&
      /日期|时间|日|月|年/.test(main.name)
    ) {
      const fieldID = 'D_template_date';
      const op: AnalysisTransformOp = template.title.includes('到期年份') ? 'year' : 'yearmonth';
      q.transforms = [
        { id: fieldID, name: op === 'year' ? '年份' : '年月', source: main.id, op, arg: '' },
      ];
      q.rows = [fieldID];
    }
    state.query = q;
    void setTab('pivot');
    renderFields();
    renderBuilder();
    if (ready) {
      void run();
    } else {
      toast(
        `模板已载入；没有匹配到${!main ? '分类字段' : !metric ? '数值指标' : '必需字段'}，请在左侧确认字段后运行`,
        'warning',
      );
    }
  }
}
