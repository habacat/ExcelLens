/**
 * Workbook I/O lifecycle: sheet tabs & picker rendering, status bootstrap,
 * file upload/opening, workbook handoff/reset across panels, and index progress.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
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
import { collapseBannerToBadgeMotion, enterMotion, stopMotion } from '../core/motion.ts';
import type { TransportService } from '../core/transport.ts';
import type { WorkbookState } from './state.ts';
import { decodeOpenResult, decodeWorkbookStatus } from '../core/workbook-response.ts';

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
  readonly setStatus: (text: string, hidden?: boolean) => void;
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

interface SourceIntegrityState {
  message: string;
  key: string;
  dismissedKey: string;
}

const sourceIntegrityStates = new WeakMap<HTMLElement, SourceIntegrityState>();

function integrityState(notice: HTMLElement): SourceIntegrityState {
  let state = sourceIntegrityStates.get(notice);
  if (!state) {
    state = { message: '', key: '', dismissedKey: '' };
    sourceIntegrityStates.set(notice, state);
  }
  return state;
}

export function renderSourceIntegrity(
  source: SourceInfo | null | undefined,
  workbookKey = '',
): void {
  const notice = requireEl('sourceIntegrityNotice');
  const badge = document.getElementById('sourceIntegrityBadge');
  const state = integrityState(notice);
  stopMotion(notice);
  stopMotion(badge);
  const integrity =
    source?.archiveNotice ||
    (source?.incomplete ? source.notice || localize('ui.gridWorkbookIo.currentDataIncompleteFragmentCannot') : '');
  const message = [integrity, source?.presentationNotice].filter(Boolean).join('；');
  requireEl('sourceIntegrityMessage').textContent = message;
  const retainBtn = document.getElementById('retainDatabaseSnapshot');
  if (retainBtn) {
    retainBtn.hidden = !source?.databaseSnapshot?.legacy || source.databaseSnapshot.retained;
  }
  if (!message) {
    state.message = '';
    state.key = '';
    state.dismissedKey = '';
    notice.hidden = true;
    if (badge) {
      badge.hidden = true;
      badge.setAttribute('aria-expanded', 'false');
    }
    return;
  }
  const key = `${workbookKey}|${message}`;
  state.message = message;
  state.key = key;
  const collapsed = Boolean(state.dismissedKey && state.dismissedKey === key);
  if (!collapsed) state.dismissedKey = '';
  notice.hidden = collapsed;
  if (badge) {
    badge.hidden = !collapsed;
    badge.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
  }
}

export function hideSourceIntegrity(): void {
  const notice = requireEl('sourceIntegrityNotice');
  const badge = document.getElementById('sourceIntegrityBadge');
  const state = integrityState(notice);
  stopMotion(notice);
  stopMotion(badge);
  state.message = '';
  state.key = '';
  state.dismissedKey = '';
  notice.hidden = true;
  if (badge) {
    badge.hidden = true;
    badge.setAttribute('aria-expanded', 'false');
  }
}

export function collapseSourceIntegrity(): void {
  const notice = requireEl('sourceIntegrityNotice');
  const badge = document.getElementById('sourceIntegrityBadge');
  const state = integrityState(notice);
  if (!state.message || notice.hidden) return;
  const from = notice.getBoundingClientRect();
  state.dismissedKey = state.key;
  stopMotion(notice);
  notice.hidden = true;
  if (badge) {
    badge.hidden = false;
    badge.setAttribute('aria-expanded', 'false');
    collapseBannerToBadgeMotion(from, badge, 'source-integrity-flight');
  }
}

export function expandSourceIntegrity(): void {
  const notice = requireEl('sourceIntegrityNotice');
  const badge = document.getElementById('sourceIntegrityBadge');
  const state = integrityState(notice);
  if (!state.message) return;
  stopMotion(badge);
  state.dismissedKey = '';
  if (badge) {
    badge.hidden = true;
    badge.setAttribute('aria-expanded', 'true');
  }
  notice.hidden = false;
  enterMotion(notice, { distance: -6, duration: 150 });
}

export function bindSourceIntegrityControls(): void {
  const closeBtn = document.getElementById('sourceIntegrityCloseBtn');
  const badge = document.getElementById('sourceIntegrityBadge');
  closeBtn?.addEventListener('click', () => {
    const moveFocus = document.activeElement === closeBtn;
    collapseSourceIntegrity();
    if (moveFocus) badge?.focus();
  });
  badge?.addEventListener('click', () => {
    const moveFocus = document.activeElement === badge;
    expandSourceIntegrity();
    if (moveFocus) closeBtn?.focus();
  });
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
        localize('ui.gridWorkbookIo.row', { done: stats.done.toLocaleString(interfaceLocale()), total: stats.total.toLocaleString(interfaceLocale()) }),
      );
    } else {
      activity?.report(
        progress.doneBytes,
        progress.totalBytes,
        progress.stage || localize('ui.gridWorkbookIo.creatingIndexes'),
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
        (sheet.state && sheet.state !== 'visible' ? localize('ui.gridWorkbookIo.hiddenWorksheets') : '');
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
      empty.textContent = localize('ui.gridWorkbookIo.noMatchingWorksheetsFound');
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
    const activity = launch ? beginActivity(localize('ui.common.openingFile'), { priority: 2 }) : null;
    if (launch) {
      S.opening = true;
      requireButton('openBtn').disabled = true;
      requireButton('chooseFileBtn').disabled = true;
      activity?.stage(localize('ui.common.openingFile'), 0, 60);
    }
    try {
      const data = await api('/api/status', undefined, decodeWorkbookStatus);
      panels.archiveCenter?.use(data.archive);
      if (!data.opened) {
        if (data.archive) panels.archiveCenter?.present(data.archive);
        S.opened = false;
        S.visitedHyperlinks.clear();
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
          setStatus(localize('ui.gridWorkbookIo.notOpenFile'));
        } else {
          setStatus(localize('ui.gridWorkbookIo.chooseFileOpen'), S.indexReady);
        }
        return;
      }
      S.opened = true;
      activity?.stage(localize('ui.common.preparingScheduleProgress'), 60, 65);
      S.sheets = data.sheets ? [...data.sheets] : [];
      S.activeTab = typeof data.activeTab === 'number' && Number.isInteger(data.activeTab)
        ? data.activeTab
        : 0;
      S.fileName = data.fileName ?? '';
      S.book = data.book ?? '';
      S.fileSize = data.fileSize ?? 0;
      S.source = data.source ?? { format: 'xlsx' };
      const nextKey = data.workbookKey ?? '';
      if (S.workbookKey !== nextKey) {
        S.visitedHyperlinks.clear();
        panels.bookmarkCenter?.resetBook();
      }
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
        setStatus(localize('ui.gridWorkbookIo.noWorksheetsWorkbook'));
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
      setStatus(localize('ui.gridWorkbookIo.localServiceConnectionFailed'));
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

  canOpenWorkbook(action = localize('ui.gridWorkbookIo.switchFile')): boolean {
    const { S, toast, panels } = this.d;
    if (S.opening) return false;
    if (panels.editCopy?.grid.active && panels.editCopy.grid.blocked) {
      toast(localize('ui.gridWorkbookIo.confirmInputCurrentCellFirst'));
      return false;
    }
    if (panels.analytics?.hasUnsaved()) {
      toast(localize('ui.gridWorkbookIo.analysisTableStillUnsavedContent', { action }), 'warning');
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
    setStatus(localize('ui.common.openingFile'));
    const activity = beginActivity(localize('ui.common.openingFile'), { priority: 2 });
    activity?.stage(localize('ui.common.openingFile'), 0, 60);
    try {
      await session?.ready;
      const data = await load(activity);
      if (!current()) return false;
      activity?.stage(localize('ui.common.preparingScheduleProgress'), 60, 65);
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
      setStatus(localize('ui.gridWorkbookIo.notOpenFile'));
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
      toast(localize('ui.gridWorkbookIo.selectSupportedDataFileOr'), 'warning');
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
    if (!data.opened && !data.archiveOnly) throw new Error(data.error || localize('ui.gridWorkbookIo.serviceDidNotConfirmThat'));
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
      setStatus(localize('ui.gridWorkbookIo.chooseFileWithinCompressedPackage'));
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
    S.visitedHyperlinks.clear();
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
      setStatus(localize('ui.gridWorkbookIo.noWorksheetsWorkbook'));
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
        api('/api/tasks/open', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        }, decodeOpenResult),
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
