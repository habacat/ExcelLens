/**
 * Analytics Dashboard (lazy-loaded report cards, drag-and-drop ordering) and
 * Metadata-driven Template Gallery controller.
 */
import { t as localize, th as localizeHTML } from '../core/i18n.ts';
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
import { templateCategoryLabel, type AnalysisTemplate } from './templates.ts';

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
      el.textContent = localize('ui.dashboard.thereNoReportsYetClick');
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
      card.title = localize('ui.dashboard.dragAdjustDashboardOrder');
      card.innerHTML = `<div class="analysis-dashboard-card-head"><h3><span class="analysis-dashboard-grip" aria-hidden="true"><svg class="icon icon-small" aria-hidden="true" focusable="false"><use href="#i-grip"/></svg></span><span>${escapeHtml(report.name)}</span></h3><div class="analysis-dashboard-actions"><button type="button" data-report-image disabled title="${localizeHTML('ui.dashboard.exportingLoadedChartsPreciseAttachments')}">${localizeHTML('ui.dashboard.images')}</button><button type="button" data-report-open>${localizeHTML('ui.dashboard.open')}</button><button type="button" class="analysis-dashboard-remove" data-report-remove title="${localizeHTML('ui.dashboard.deleteReport')}" aria-label="${localizeHTML('ui.dashboard.deleteReport98c7fb0')}">×</button></div></div><div class="analysis-dashboard-plot"><canvas></canvas><div class="analysis-chart-tooltip" hidden></div></div>`;
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
            toast(localize('ui.dashboard.dashboardOrderSaved'), 'success');
          })
          .catch((e: unknown) => {
            toast(localize('ui.dashboard.sortingSavingFailed', { errorMessage: errorMessage(e) }), 'error');
            this.renderDashboard();
          });
      });

      const removeButton = card.querySelector<HTMLButtonElement>('[data-report-remove]');
      if (removeButton) {
        removeButton.onclick = (): void => {
          void (async () => {
            const typed = await askText(localize('ui.dashboard.deleteAnalysisReport'), '', localize('ui.dashboard.enterConfirm', { name: report.name }));
            if (typed !== report.name) return;
            try {
              await post('/api/analysis/delete', { kind: 'report', id: report.id });
              await refreshWorkspace();
              toast(localize('ui.dashboard.reportDeleted'), 'success');
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
      info.textContent = localize('ui.dashboard.loadWhenScrollingVisibleArea');
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
        info.textContent = localize('ui.dashboard.calculating');
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
            info.textContent = localize('ui.dashboard.unableUpdate', { errorMessage: errorMessage(e) });
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
    const categories = ['all', ...new Set(templates.map((t) => t.category))];
    const cats = requireEl('analysisTemplateCategories');
    cats.replaceChildren();
    for (const name of categories) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = name === 'all' ? localize('ui.common.all') : templateCategoryLabel(name);
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
        (state.templateCategory !== 'all' && t.category !== state.templateCategory) ||
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
      toast(localize('ui.dashboard.selectWorksheetContainingDataFirst'), 'warning');
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
      template.dateGrouping &&
      /日期|时间|日|月|年/.test(main.name)
    ) {
      const fieldID = 'D_template_date';
      const op: AnalysisTransformOp = template.dateGrouping;
      q.transforms = [
        { id: fieldID, name: op === 'year' ? localize('ui.dashboard.years') : localize('ui.dashboard.monthYear'), source: main.id, op, arg: '' },
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
        localize('ui.dashboard.templateLoadedNoMatchFound', { detail: !main ? localize('ui.dashboard.categoryField') : !metric ? localize('ui.dashboard.valueMetrics') : localize('ui.dashboard.requiredFields') }),
        'warning',
      );
    }
  }
}
