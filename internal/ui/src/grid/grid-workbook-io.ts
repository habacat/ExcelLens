/**
 * Workbook I/O lifecycle: sheet tabs & picker rendering, status bootstrap,
 * file upload/opening, workbook handoff/reset across panels, and index progress.
 */
import type {
  ActivityHandle,
  ApiClient,
  SessionService,
} from '../contracts/common.ts';
import type {
  ArchiveSummary,
  IndexStatusResponse,
  SourceInfo,
  WorkbookStatusResponse,
} from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl } from '../core/dom.ts';
import type { FileFormats } from '../core/formats.ts';
import type { TransportService } from '../core/transport.ts';
import type { WorkbookState } from './state.ts';

export interface ResettablePanel {
  resetBook(): void;
}

export interface ControllablePanel {
  controls(): void;
}

export interface ChangeablePanel {
  changed(): void;
}

export interface WorkbookIOPanels {
  readonly archiveCenter?: {
    use(archive: ArchiveSummary | null | undefined): void;
    present(archive: ArchiveSummary | null | undefined): void;
  } | null;
  readonly bookmarkCenter?: {
    resetBook(): void;
    load(quiet?: boolean): Promise<unknown>;
  } | null;
  readonly calculator?: {
    bookChanged(): void;
  } | null;
  readonly recentManager?: {
    load(): Promise<void> | void;
  } | null;
  readonly editCopy?: {
    readonly grid: {
      readonly active: boolean;
      readonly blocked?: boolean;
      leave(): void;
    };
  } | null;
  readonly frozenPanes?: {
    reset(options?: { readonly preserveDOM?: boolean }): void;
  } | null;
  readonly searchPanel?: {
    resetWorkbook(): void;
  } | null;
  readonly traceUI?: {
    reset(): void;
  } | null;
  readonly formulaInspector?: {
    resetWorkbook(): void;
  } | null;
  readonly selectionInspector?: {
    resetWorkbook(): void;
  } | null;
  readonly analytics?: {
    bookChanged(workbookKey: string, book: string): void;
    hasUnsaved(): boolean;
  } | null;
  readonly catalogueCenter?: Partial<ResettablePanel & ControllablePanel> | null;
  readonly dataRulesCenter?: Partial<ResettablePanel & ControllablePanel> | null;
  readonly printingCenter?: Partial<ResettablePanel & ControllablePanel> | null;
  readonly recordCardCenter?: Partial<ResettablePanel & ControllablePanel> | null;
  readonly selectionQuick?: Partial<ResettablePanel & ChangeablePanel> | null;
  readonly transposeCenter?: Partial<ResettablePanel & ChangeablePanel> | null;
  readonly cellExplanation?: Partial<ResettablePanel & ChangeablePanel> | null;
  readonly profileCenter?: Partial<ResettablePanel & ChangeablePanel> | null;
  readonly exportCenter?: Partial<ResettablePanel> | null;
  readonly reconcileCenter?: Partial<ResettablePanel> | null;
  readonly relationsCenter?: Partial<ResettablePanel> | null;
  readonly namedViews?: Partial<ResettablePanel> | null;
  readonly rowViews?: Partial<ResettablePanel> | null;
  readonly selectionStats?: Partial<ResettablePanel> | null;
}

export interface AcceptWorkbookOptions {
  readonly fallbackName?: string;
  readonly fallbackSize?: number;
  readonly activity?: ActivityHandle | null;
  readonly revisionRef?: string;
}

export interface WorkbookIODeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly transport: TransportService;
  readonly session?: SessionService | null;
  readonly getWorkbookReady?: () => Promise<void> | null | undefined;
  readonly toast: (message: string, tone?: 'info' | 'success' | 'warning' | 'error') => void;
  readonly fmtBytes: (value: unknown) => string;
  readonly fileFormats: FileFormats;
  readonly setStatus: (text: string) => void;
  readonly showWelcome: () => void;
  readonly showWorkspace: () => void;
  readonly loadStylePalette: () => Promise<void>;
  readonly selectSheet: (
    name: string,
    targetRef?: string,
    preserveTrace?: boolean,
    navigationGuard?: (() => boolean) | null,
    openingActivity?: ActivityHandle | null,
  ) => Promise<boolean | undefined>;
  readonly selectSheetTab: (name: string) => Promise<boolean | undefined>;
  readonly refreshSearchHistory: () => Promise<void>;
  readonly invalidateViewport: (options?: { readonly preserveDOM?: boolean }) => void;
  readonly cancelFilter: (jobID: string) => Promise<void>;
  readonly cancelConditionalStats?: (sendCancel?: boolean, showCancelled?: boolean) => void;
  readonly panels: WorkbookIOPanels;
}

export function renderSourceIntegrity(source: SourceInfo | null | undefined): void {
  const notice = requireEl('sourceIntegrityNotice');
  const integrity =
    source?.archiveNotice ||
    (source?.incomplete ? source.notice || '当前数据为不完整片段，不能代表全量账目。' : '');
  const message = [integrity, source?.presentationNotice].filter(Boolean).join('；');
  requireEl('sourceIntegrityMessage').textContent = message;
  const retainBtn = document.getElementById('retainDatabaseSnapshot');
  if (retainBtn) {
    retainBtn.hidden = !source?.databaseSnapshot?.legacy || source.databaseSnapshot.retained;
  }
  notice.hidden = !message;
}

export class WorkbookIO {
  private readonly d: WorkbookIODeps;

  constructor(deps: WorkbookIODeps) {
    this.d = deps;
  }

  reportIndexProgress(
    activity: ActivityHandle | null | undefined,
    progress: Pick<IndexStatusResponse, 'statisticsProgress' | 'stage' | 'doneBytes' | 'totalBytes'>,
  ): void {
    const { fmtBytes } = this.d;
    const stats = progress.statisticsProgress;
    if (stats && stats.total > 0) {
      activity?.report(
        stats.done,
        stats.total,
        progress.stage,
        `${stats.done.toLocaleString()} / ${stats.total.toLocaleString()} 行`,
      );
    } else {
      activity?.report(
        progress.doneBytes,
        progress.totalBytes,
        progress.stage || '正在建立索引',
        progress.totalBytes > 0
          ? `${fmtBytes(progress.doneBytes)} / ${fmtBytes(progress.totalBytes)}`
          : '',
      );
    }
  }

  renderSheetTabs(): void {
    const { S, selectSheetTab } = this.d;
    const tabs = requireEl('sheetTabs');
    const fragment = document.createDocumentFragment();
    S.sheets.forEach((sheet, index) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className =
        'sheet-tab' +
        (S.sheet === sheet.name ? ' active' : '') +
        (sheet.state && sheet.state !== 'visible' ? ' hidden-sheet' : '');
      if (sheet.tabColor && /^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor)) {
        button.classList.add('has-tab-color');
        button.style.setProperty('--sheet-tab-color', sheet.tabColor);
      }
      button.textContent = sheet.name;
      button.title =
        (sheet.sourceName || sheet.name) +
        (sheet.state && sheet.state !== 'visible' ? '（隐藏工作表）' : '');
      button.setAttribute('role', 'tab');
      button.setAttribute('aria-selected', S.sheet === sheet.name ? 'true' : 'false');
      button.addEventListener('click', () => {
        void selectSheetTab(sheet.name);
      });
      fragment.appendChild(button);
      if (index === S.activeTab) button.dataset.activeIndex = String(index);
    });
    tabs.replaceChildren(fragment);
    const active = tabs.querySelector('.sheet-tab.active');
    if (active) active.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    requestAnimationFrame(() => this.updateSheetPager());
  }

  updateSheetPager(): void {
    const tabs = requireEl('sheetTabs');
    const hasOverflow = tabs.scrollWidth > tabs.clientWidth + 2;
    const prevBtn = requireButton('sheetPrevBtn');
    const nextBtn = requireButton('sheetNextBtn');
    prevBtn.hidden = !hasOverflow;
    nextBtn.hidden = !hasOverflow;
    prevBtn.disabled = tabs.scrollLeft <= 1;
    nextBtn.disabled = tabs.scrollLeft >= tabs.scrollWidth - tabs.clientWidth - 2;
  }

  pageSheetTabs(direction: number): void {
    const tabs = requireEl('sheetTabs');
    const reduced =
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    tabs.scrollBy({
      left: direction * Math.max(110, Math.round(tabs.clientWidth * 0.72)),
      behavior: reduced ? 'auto' : 'smooth',
    });
  }

  renderSheetPicker(query = ''): void {
    const { S, selectSheetTab } = this.d;
    const list = requireEl('sheetPickerList');
    const q = query.trim().toLocaleLowerCase();
    const fragment = document.createDocumentFragment();
    let shown = 0;
    S.sheets.forEach((sheet, index) => {
      if (q && !sheet.name.toLocaleLowerCase().includes(q)) return;
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `sheet-picker-item${S.sheet === sheet.name ? ' active' : ''}`;
      const number = document.createElement('span');
      number.className = 'sheet-number';
      number.textContent = String(index + 1).padStart(2, '0');
      const name = document.createElement('span');
      name.textContent = sheet.name;
      if (sheet.tabColor && /^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor)) {
        const swatch = document.createElement('span');
        swatch.className = 'sheet-picker-color';
        swatch.style.backgroundColor = sheet.tabColor;
        button.append(number, swatch, name);
      } else {
        button.append(number, name);
      }
      button.addEventListener('click', () => {
        requireDialog('sheetDialog').close();
        void selectSheetTab(sheet.name);
      });
      fragment.appendChild(button);
      shown++;
    });
    if (!shown) {
      const empty = document.createElement('div');
      empty.className = 'dialog-summary';
      empty.textContent = '没有匹配的工作表';
      fragment.appendChild(empty);
    }
    list.replaceChildren(fragment);
  }

  updateFileTitle(): void {
    const { S } = this.d;
    const badge = requireEl('fileBadge');
    badge.textContent = S.fileName;
    badge.title = S.fileName;
    document.title = S.fileName || 'ExcelLens';
  }

  async loadStatus(): Promise<void> {
    const {
      S,
      api,
      session,
      toast,
      setStatus,
      showWelcome,
      showWorkspace,
      loadStylePalette,
      selectSheet,
      refreshSearchHistory,
      panels,
    } = this.d;
    const launch = Boolean(session?.nativeLaunch || session?.restoring);
    const activity = launch ? beginActivity('正在打开文件', { priority: 2 }) : null;
    if (launch) {
      S.opening = true;
      requireButton('openBtn').disabled = true;
      requireButton('chooseFileBtn').disabled = true;
      activity?.stage('正在打开文件', 0, 60);
    }
    try {
      const data = await api<WorkbookStatusResponse>('/api/status');
      panels.archiveCenter?.use(data.archive);
      if (!data.opened) {
        if (data.archive) panels.archiveCenter?.present(data.archive);
        S.opened = false;
        this.d.cancelConditionalStats?.(false, false);
        panels.catalogueCenter?.resetBook?.();
        panels.dataRulesCenter?.resetBook?.();
        panels.printingCenter?.resetBook?.();
        panels.recordCardCenter?.resetBook?.();
        panels.selectionQuick?.resetBook?.();
        panels.transposeCenter?.resetBook?.();
        panels.cellExplanation?.resetBook?.();
        showWelcome();
        void panels.recentManager?.load();
        if (data.error) {
          const welcomeError = requireEl('welcomeError');
          welcomeError.textContent = data.error;
          welcomeError.hidden = false;
          setStatus('打开失败');
        } else {
          setStatus('等待选择文件');
        }
        return;
      }
      S.opened = true;
      activity?.stage('正在准备工作表', 60, 65);
      S.sheets = data.sheets ? [...data.sheets] : [];
      S.activeTab = typeof data.activeTab === 'number' && Number.isInteger(data.activeTab)
        ? data.activeTab
        : 0;
      S.fileName = data.fileName ?? '';
      S.book = data.book ?? '';
      S.fileSize = data.fileSize ?? 0;
      S.source = data.source ?? { format: 'xlsx' };
      const nextKey = data.workbookKey ?? '';
      if (S.workbookKey !== nextKey) panels.bookmarkCenter?.resetBook();
      S.workbookKey = nextKey;
      panels.calculator?.bookChanged();
      panels.analytics?.bookChanged(S.workbookKey, S.book);
      panels.catalogueCenter?.controls?.();
      panels.dataRulesCenter?.controls?.();
      panels.printingCenter?.controls?.();
      panels.recordCardCenter?.controls?.();
      panels.selectionQuick?.changed?.();
      panels.transposeCenter?.changed?.();
      panels.cellExplanation?.changed?.();
      panels.profileCenter?.changed?.();
      this.updateFileTitle();
      showWorkspace();
      await loadStylePalette();
      const launchParams = new URLSearchParams(window.location.search || '');
      const requested = launchParams.get('sourceSheet');
      const active =
        S.sheets.find((sheet) => sheet.name === requested) ?? S.sheets[S.activeTab] ?? S.sheets[0];
      if (active) {
        await selectSheet(
          active.name,
          requested === active.name ? (launchParams.get('sourceRef') ?? '') : '',
          false,
          null,
          activity,
        );
      } else {
        this.renderSheetTabs();
        setStatus('工作簿中没有工作表');
      }
      panels.bookmarkCenter?.load(true).catch(() => {
        // Ignore initial bookmark load errors.
      });
      refreshSearchHistory().catch(() => {
        // Ignore initial search history errors.
      });
    } catch (error: unknown) {
      showWelcome();
      void panels.recentManager?.load();
      setStatus('本机服务连接失败');
      toast(errorMessage(error), 'error');
    } finally {
      activity?.end();
      if (launch) {
        this.finishOpening();
        requireButton('openBtn').disabled = false;
        requireButton('chooseFileBtn').disabled = false;
      }
    }
  }

  finishOpening(): void {
    const { S, panels } = this.d;
    S.opening = false;
    panels.dataRulesCenter?.controls?.();
    panels.printingCenter?.controls?.();
    panels.recordCardCenter?.controls?.();
    panels.selectionQuick?.changed?.();
    panels.cellExplanation?.changed?.();
    panels.profileCenter?.changed?.();
  }

  chooseFile(): void {
    const { S } = this.d;
    if (!S.opening && this.canOpenWorkbook()) requireEl('fileInput').click();
  }

  canOpenWorkbook(action = '切换文件'): boolean {
    const { S, toast, panels } = this.d;
    if (S.opening) return false;
    if (panels.editCopy?.grid.active && panels.editCopy.grid.blocked) {
      toast('请先确认当前单元格的输入');
      return false;
    }
    if (panels.analytics?.hasUnsaved()) {
      toast(`分析表仍有未保存内容，请先在分析中重试保存，再${action}`, 'warning');
      return false;
    }
    return true;
  }

  async openWorkbook(
    load: (activity: ActivityHandle | null) => Promise<WorkbookStatusResponse>,
    name = '',
    size = 0,
    current: () => boolean = () => true,
  ): Promise<boolean> {
    const { S, session, setStatus } = this.d;
    if (S.opening || !this.canOpenWorkbook()) return false;
    S.opening = true;
    requireButton('openBtn').disabled = true;
    requireButton('chooseFileBtn').disabled = true;
    requireButton('homeBtn').disabled = true;
    requireEl('welcomeError').hidden = true;
    setStatus('正在打开文件');
    const activity = beginActivity('正在打开文件', { priority: 2 });
    activity?.stage('正在打开文件', 0, 60);
    try {
      await session?.ready;
      const data = await load(activity);
      if (!current()) return false;
      activity?.stage('正在准备工作表', 60, 65);
      await this.acceptOpenedWorkbook(data, {
        fallbackName: name,
        fallbackSize: size,
        activity,
      });
      await activity?.paintIfDue();
      return true;
    } catch (error: unknown) {
      if (!current()) return false;
      const welcomeError = requireEl('welcomeError');
      welcomeError.textContent = errorMessage(error);
      welcomeError.hidden = false;
      setStatus('打开失败');
      throw error;
    } finally {
      activity?.end();
      this.finishOpening();
      requireButton('openBtn').disabled = false;
      requireButton('chooseFileBtn').disabled = false;
      requireButton('homeBtn').disabled = false;
    }
  }

  async uploadFile(file: File | null | undefined): Promise<boolean | undefined> {
    const { S, fileFormats, fmtBytes, toast, transport, getWorkbookReady } = this.d;
    if (!file) return undefined;
    await getWorkbookReady?.();
    if (S.opening) return undefined;
    if (!fileFormats.supports(file.name)) {
      toast('请选择受支持的数据文件或压缩包', 'warning');
      return undefined;
    }
    return this.openWorkbook(
      (activity) => transport.upload(file, activity, fmtBytes),
      file.name,
      file.size,
    ).catch((error: unknown) => {
      toast(errorMessage(error), 'error');
      return undefined;
    });
  }

  async acceptOpenedWorkbook(
    data: WorkbookStatusResponse,
    options: AcceptWorkbookOptions = {},
  ): Promise<void> {
    const { fallbackName = '', fallbackSize = 0, activity, revisionRef = '' } = options;
    const {
      S,
      setStatus,
      invalidateViewport,
      cancelFilter,
      showWorkspace,
      loadStylePalette,
      selectSheet,
      refreshSearchHistory,
      panels,
    } = this.d;
    if (panels.editCopy?.grid.active) panels.editCopy.grid.leave();
    panels.archiveCenter?.use(data.archive);
    if (data.archiveOnly) {
      panels.archiveCenter?.present(data.archive);
      setStatus('请选择压缩包内的文件');
      return;
    }
    S.opened = true;
    S.sheets = data.sheets ? [...data.sheets] : [];
    S.activeTab =
      typeof data.activeTab === 'number' && Number.isInteger(data.activeTab) ? data.activeTab : 0;
    S.fileName = data.fileName || fallbackName;
    S.fileSize = data.fileSize || fallbackSize;
    ++S.sheetEpoch;
    S.sheet = null;
    S.indexReady = false;
    S.index = null;
    this.d.cancelConditionalStats?.(false, false);
    S.tilesSkippedConditional = false;
    invalidateViewport();
    panels.frozenPanes?.reset();
    panels.searchPanel?.resetWorkbook();
    panels.traceUI?.reset();
    panels.formulaInspector?.resetWorkbook();
    panels.selectionInspector?.resetWorkbook();
    panels.profileCenter?.resetBook?.();
    panels.exportCenter?.resetBook?.();
    panels.reconcileCenter?.resetBook?.();
    panels.relationsCenter?.resetBook?.();
    panels.namedViews?.resetBook?.();
    panels.catalogueCenter?.resetBook?.();
    panels.dataRulesCenter?.resetBook?.();
    panels.printingCenter?.resetBook?.();
    panels.recordCardCenter?.resetBook?.();
    panels.selectionQuick?.resetBook?.();
    panels.transposeCenter?.resetBook?.();
    panels.cellExplanation?.resetBook?.();
    panels.rowViews?.resetBook?.();
    if (S.filterJob) void cancelFilter(S.filterJob);
    S.history = [];
    S.historyIndex = -1;
    S.filtersBySheet.clear();
    S.positionsBySheet.clear();
    S.sheetViews.clear();
    S.sheetViewsBytes = 0;
    S.filterTotalsBySheet.clear();
    S.autoFiltersBySheet.clear();
    panels.bookmarkCenter?.resetBook();
    S.autoFilter = null;
    S.sortKeysBySheet.clear();
    S.sortHeaderBySheet.clear();
    S.lastRulesBySheet.clear();
    panels.selectionStats?.resetBook?.();
    S.filterId = '';
    S.filtered = false;

    S.source = data.source ?? { format: 'xlsx' };
    S.workbookKey = data.workbookKey ?? '';
    panels.calculator?.bookChanged();
    S.book = data.book ?? '';
    panels.catalogueCenter?.controls?.();
    panels.dataRulesCenter?.controls?.();
    panels.printingCenter?.controls?.();
    panels.recordCardCenter?.controls?.();
    panels.selectionQuick?.changed?.();
    panels.transposeCenter?.changed?.();
    panels.cellExplanation?.changed?.();
    panels.profileCenter?.changed?.();
    panels.analytics?.bookChanged(S.workbookKey, S.book);
    this.updateFileTitle();
    showWorkspace();
    await loadStylePalette();
    const active = S.sheets[S.activeTab] ?? S.sheets[0];
    if (active) {
      await selectSheet(active.name, revisionRef, false, null, activity);
    } else {
      this.renderSheetTabs();
      setStatus('工作簿中没有工作表');
    }
    panels.bookmarkCenter?.load(true).catch(() => {
      // Ignore initial bookmark load errors.
    });
    refreshSearchHistory().catch(() => {
      // Ignore initial search history errors.
    });
  }

  async openTaskSource(payload: unknown, current?: () => boolean): Promise<boolean> {
    const { api } = this.d;
    return this.openWorkbook(
      () =>
        api<WorkbookStatusResponse>('/api/tasks/open', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        }),
      '',
      0,
      current,
    );
  }

  async cancelIndex(jobID: string | null | undefined): Promise<void> {
    const { api } = this.d;
    if (!jobID) return;
    try {
      await api('/api/index/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: jobID }),
      });
    } catch {
      // Job may have already finished.
    }
  }
}
