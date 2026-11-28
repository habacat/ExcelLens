/**
 * Server-backed source-row visibility & outline-group controller with
 * bounded LRU caching, task cancellation, and safe recovery mode.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { AxisViewSettings } from '../contracts/analysis.ts';
import type { ApiClient, ApiRequestOptions, ToastFn } from '../contracts/common.ts';
import type { RowViewChange, RowViewState } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { RowVisibility } from '../grid/row-visibility.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeRowViewGroupsResponse,
  decodeRowViewStartResponse,
  decodeRowViewState,
  decodeRowViewStatusResponse,
} from '../core/http-decoders.generated.ts';
import { decodeWorkbookStatus } from '../core/workbook-response.ts';

export interface RowViewsOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly maximum: () => number;
  readonly onBusyChange?: () => void;
  readonly commit: (data: RowViewState, current: () => boolean) => Promise<void> | void;
  readonly toast: ToastFn;
  readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  readonly reload: () => Promise<unknown>;
  readonly openDialog: (id: string) => void;
}

export interface RowViewBuildOptions {
  readonly sheet?: string;
  readonly filterId?: string;
  readonly baseId?: string;
  readonly settings?: AxisViewSettings | null;
  readonly change?: RowViewChange;
  readonly persist?: boolean;
  readonly revision?: number;
  readonly signal?: AbortSignal;
  readonly guard?: () => boolean;
}

export interface RowViewPrepareOptions {
  readonly signal?: AbortSignal;
  readonly settings?: AxisViewSettings | null;
}

export interface RowViewChangeOptions {
  readonly temporary?: boolean;
  readonly guard?: () => boolean;
}

/** @httpResponse RowViewStartResponse */
export interface RowViewStartResponse {
  readonly jobId?: string;
}

function interrupted(): DOMException {
  return new DOMException(localize('ui.rowViews.disabledRowVisibilityUpdate'), 'AbortError');
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function errorStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as { readonly status?: unknown }).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

export class RowViews {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly maximum: () => number;
  public commit: RowViewsOptions['commit'];
  private readonly toast: ToastFn;
  private readonly reload: () => Promise<unknown>;
  private readonly openDialog: (id: string) => void;

  public serial = 0;
  private changeSerial = 0;
  private job = '';
  private abort: AbortController | null = null;
  public readonly cache = new Map<string, RowViewState>();
  public busy = false;
  private readonly onBusyChange?: () => void;
  public page = 0;
  private pageSerial = 0;
  public groupTotal = 0;
  private recoverySettings: AxisViewSettings | null = null;

  constructor(options: RowViewsOptions) {
    this.s = options.state;
    this.api = options.api;
    this.maximum = options.maximum;
    this.onBusyChange = options.onBusyChange;
    this.commit = options.commit;
    this.toast = options.toast;
    this.reload = options.reload;
    this.openDialog = options.openDialog;

    requireButton('rowsBtn').addEventListener('click', () => {
      void this.open();
    });
    requireDialog('rowsDialog').addEventListener('close', () => {
      this.pageSerial++;
    });
    requireButton('rowsCancel').addEventListener('click', () => this.cancel());
    const quickActions: readonly (readonly [string, string])[] = [
      ['rowsOriginal', 'original'],
      ['rowsShowAll', 'show-all'],
    ];
    for (const [id, action] of quickActions) {
      requireButton(id).addEventListener('click', () => {
        void this.change({ action });
      });
    }
    requireButton('rowsHideRange').addEventListener('click', () => this.range(true));
    requireButton('rowsShowRange').addEventListener('click', () => this.range(false));
    requireButton('rowsDepthApply').addEventListener('click', () => {
      void this.change({ action: 'depth', depth: Number(requireSelect('rowsDepth').value) });
    });
    requireButton('rowsPrevious').addEventListener('click', () => {
      this.page = Math.max(0, this.page - 1);
      void this.groups();
    });
    requireButton('rowsNext').addEventListener('click', () => {
      this.page++;
      void this.groups();
    });
    requireButton('showRowsRecoveryBtn').addEventListener('click', () => {
      this.recoverySettings = { mode: 'show-all' };
      void this.reload();
    });
  }

  private post(
    url: string,
    body: unknown,
    options: ApiRequestOptions = {},
  ): Promise<unknown> {
    return this.api(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      ...options,
    });
  }

  cleanup(id: string): void {
    if (!id) return;
    this.post('/api/row-view/clear', { id }).catch(() => {
      // Best-effort cleanup of abandoned row view snapshot.
    });
  }

  private setBusy(busy: boolean): void {
    if (this.busy === busy) return;
    this.busy = busy;
    this.onBusyChange?.();
  }

  cancel(keepBusy = false): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
    if (this.job) {
      this.post('/api/row-view/cancel', { id: this.job }).catch(() => {
        // Best-effort server cancellation.
      });
    }
    this.job = '';
    if (!keepBusy) {
      this.changeSerial++;
      if (this.busy) this.status(localize('ui.rowViews.updateCanceled'));
      this.setBusy(false);
    }
    this.controls();
  }

  resetBook(): void {
    this.cancel();
    ++this.pageSerial;
    for (const data of this.cache.values()) this.cleanup(data.id);
    this.cache.clear();
    this.s.rowView = null;
    this.s.rowVisibility = null;
    this.s.rowViewFailure = false;
    this.recoverySettings = null;
    requireDialog('rowsDialog').close();
  }

  private current(ticket: number, book: string, sheet: string | null, epoch: number): boolean {
    return (
      ticket === this.serial &&
      book === this.s.workbookKey &&
      sheet === this.s.sheet &&
      epoch === this.s.sheetEpoch
    );
  }

  validate(data: RowViewState | null | undefined, sheet: string, filterId: string): RowVisibility {
    const max = this.maximum();
    if (
      data?.workbookKey !== this.s.workbookKey ||
      (this.s.book && data.book !== this.s.book) ||
      data.sheet !== sheet ||
      data.filterId !== filterId ||
      !data.id ||
      !data.digest ||
      !Number.isSafeInteger(data.revision) ||
      data.revision < 0 ||
      !Number.isSafeInteger(data.maxRow) ||
      data.maxRow < 0 ||
      data.maxRow > max ||
      !Number.isSafeInteger(data.totalRows) ||
      data.totalRows < 0 ||
      data.totalRows > max
    ) {
      throw new Error(localize('ui.rowViews.invalidSourceOrQuantityVisible'));
    }
    const visibility = new RowVisibility(max, data.hiddenRanges ?? []);
    if (!filterId && data.totalRows !== visibility.count(1, data.maxRow)) {
      throw new Error(localize('ui.rowViews.numberVisibleRowsDoesNot'));
    }
    return visibility;
  }

  install(data: RowViewState): void {
    const visibility = this.validate(data, this.s.sheet ?? '', this.s.filterId);
    this.s.rowView = data;
    this.s.rowVisibility = visibility;
    this.s.rowViewFailure = false;
    if (data.settingsWarning) requireInput('rowsRemember').checked = false;
    const previous = this.cache.get(data.sheet);
    if (previous && previous.id !== data.id) this.cleanup(previous.id);
    this.cache.delete(data.sheet);
    this.cache.set(data.sheet, data);

    const estimateBytes = (item: RowViewState): number =>
      (item.hiddenRanges?.length ?? 0) * 96 + JSON.stringify(item.settings).length * 2 + 512;
    let bytes = 0;
    for (const item of this.cache.values()) bytes += estimateBytes(item);
    while (this.cache.size > 1 && (this.cache.size > 4 || bytes > 16 * 1024 * 1024)) {
      const oldestKey = this.cache.keys().next().value;
      if (oldestKey === undefined) break;
      const old = this.cache.get(oldestKey);
      this.cache.delete(oldestKey);
      if (old) {
        bytes -= estimateBytes(old);
        this.cleanup(old.id);
      }
    }
  }

  async build(options: RowViewBuildOptions = {}): Promise<RowViewState> {
    const {
      sheet = this.s.sheet ?? '',
      filterId = this.s.filterId,
      baseId = '',
      settings,
      change,
      persist = false,
      revision,
      signal,
      guard = () => true,
    } = options;
    this.cancel(true);
    const ticket = this.serial;
    const book = this.s.workbookKey;
    const origin = this.s.sheet;
    const epoch = this.s.sheetEpoch;
    const abort = new AbortController();
    this.abort = abort;
    let owned = '';
    let accepted = false;
    const activity = beginActivity(localize('ui.rowViews.visibleRowsBeingUpdated'), { signal: abort.signal });
    const current = (): boolean =>
      this.current(ticket, book, origin, epoch) &&
      !abort.signal.aborted &&
      !signal?.aborted &&
      guard();
    const stop = (): void => {
      abort.abort();
      if (owned) {
        this.post('/api/row-view/cancel', { id: owned }).catch(() => {
          // Best-effort cancellation.
        });
      }
    };
    signal?.addEventListener('abort', stop, { once: true });
    try {
      if (!this.s.book) {
        const status = await this.api('/api/status', {
          signal: abort.signal,
        }, decodeWorkbookStatus);
        if (!current() || status.workbookKey !== book) {
          throw new DOMException(localize('ui.rowViews.sourceSwitched'), 'AbortError');
        }
        this.s.book = status.book ?? '';
      }
      if (!current()) throw new DOMException(localize('ui.common.canceled'), 'AbortError');
      const request: Record<string, unknown> = {
        workbookKey: book,
        book: this.s.book,
        sheet,
        filterId,
        baseId,
        settings,
        change,
        persist,
      };
      if (persist) request.expectedRevision = revision;
      // Keep ownership of a late start response even if the caller is gone;
      // its server task must be cancelled, rather than losing the returned ID.
      const started = await this.post('/api/row-view/start', request).then(decodeRowViewStartResponse);
      owned = started.jobId ?? '';
      if (!owned) {
        owned = '';
        throw new Error(localize('ui.rowViews.visibleRowTaskIdentifierInvalid'));
      }
      if (!current()) throw new DOMException(localize('ui.common.canceled'), 'AbortError');
      this.job = owned;
      for (;;) {
        const state = await this.api(
          `/api/row-view/status?id=${encodeURIComponent(owned)}`,
          { signal: abort.signal },
          decodeRowViewStatusResponse,
        );
        if (!current()) throw new DOMException(localize('ui.common.canceled'), 'AbortError');
        activity?.job({ scanned: state.scanned, total: state.total });
        if (state.status === 'done') break;
        if (state.status === 'error' || state.status === 'cancelled') {
          throw new Error(state.error || localize('ui.rowViews.visibleRowTasksHaveCanceled'));
        }
        if (state.status !== 'queued' && state.status !== 'running') {
          throw new Error(localize('ui.rowViews.visibleRowTaskStatusInvalid'));
        }
        if (this.busy) {
          this.status(
            localize('ui.rowViews.visibleRowsBeingUpdated89d9aa8', { detail: state.total ? ` · ${(state.scanned || 0).toLocaleString(interfaceLocale())} / ${state.total.toLocaleString(interfaceLocale())}` : '' }),
          );
        }
        await new Promise<void>((resolve) => setTimeout(resolve, 60));
      }
      const data = await this.api(`/api/row-view?id=${encodeURIComponent(owned)}`, {
        signal: abort.signal,
      }, decodeRowViewState);
      if (!current()) throw interrupted();
      this.validate(data, sheet, filterId);
      if (data.id !== owned) throw new Error(localize('ui.rowViews.resultsVisibleRowTasksInconsistent'));
      accepted = true;
      return data;
    } catch (error: unknown) {
      if (!current()) throw interrupted();
      throw error;
    } finally {
      activity?.end();
      signal?.removeEventListener('abort', stop);
      if (!accepted && owned) {
        this.post('/api/row-view/cancel', { id: owned }).catch(() => {
          // Best-effort cancellation.
        });
        this.cleanup(owned);
      }
      if (ticket === this.serial) {
        this.job = '';
        if (this.abort === abort) this.abort = null;
      }
    }
  }

  async prepare(options: RowViewPrepareOptions = {}): Promise<RowViewState> {
    const { signal, settings: requestedSettings } = options;
    const sheet = this.s.sheet ?? '';
    const filterId = this.s.filterId;
    const cached = this.cache.get(sheet);
    const book = this.s.workbookKey;
    const epoch = this.s.sheetEpoch;
    const current = (): boolean =>
      book === this.s.workbookKey &&
      sheet === this.s.sheet &&
      epoch === this.s.sheetEpoch &&
      filterId === this.s.filterId &&
      !signal?.aborted;
    let ticket = this.serial;
    let data: RowViewState;
    this.s.rowViewFailure = false;
    try {
      if (cached?.filterId === filterId && !this.recoverySettings && !requestedSettings) {
        try {
          data = await this.api(
            `/api/row-view?id=${encodeURIComponent(cached.id)}`,
            { signal },
            decodeRowViewState,
          );
          if (!current() || ticket !== this.serial) throw interrupted();
          if (data.id !== cached.id) throw new Error(localize('ui.rowViews.inconsistentSnapshotsVisibleRowsTry'));
          this.install(data);
          return data;
        } catch (error: unknown) {
          if (errorStatus(error) !== 404) throw error;
          this.cache.delete(sheet);
        }
      }
      const settings = requestedSettings ?? this.recoverySettings ?? cached?.settings;
      this.recoverySettings = null;
      const pending = this.build({ filterId, settings, signal, guard: current });
      ticket = this.serial;
      data = await pending;
      if (!current() || ticket !== this.serial) {
        this.cleanup(data.id);
        throw interrupted();
      }
      this.install(data);
      return data;
    } catch (error: unknown) {
      if (!isAbortError(error) && current() && ticket === this.serial) {
        this.s.rowViewFailure = true;
      }
      throw error;
    }
  }

  prepareFilter(filterId: string, options: RowViewBuildOptions = {}): Promise<RowViewState> {
    return this.build({ filterId, baseId: this.s.rowView?.id ?? '', ...options });
  }

  controls(): void {
    for (const id of [
      'rowsOriginal',
      'rowsShowAll',
      'rowsHideRange',
      'rowsShowRange',
      'rowsDepthApply',
    ]) {
      requireButton(id).disabled = this.busy;
    }
    requireInput('rowsFirst').disabled = this.busy;
    requireInput('rowsLast').disabled = this.busy;
    requireSelect('rowsDepth').disabled = this.busy;
    const remember = requireInput('rowsRemember');
    remember.disabled = this.busy || Boolean(this.s.rowView?.settingsWarning);
    remember.title = this.s.rowView?.settingsWarning
      ? localize('ui.rowViews.originalSettingsNotReadableCurrent')
      : '';
    requireButton('rowsCancel').hidden = !this.busy;
    requireEl('rowsGroupList').inert = this.busy;
    requireButton('rowsPrevious').disabled = this.busy || this.page === 0;
    requireButton('rowsNext').disabled = this.busy || (this.page + 1) * 200 >= this.groupTotal;
  }

  status(text: string, error = false): void {
    const el = requireEl('rowsStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
  }

  async open(): Promise<void> {
    if (!this.s.indexReady) {
      this.toast(localize('ui.common.openWorksheetFirst'), 'warning');
      return;
    }
    const range = this.s.range;
    const selected = this.s.selected?.row || 1;
    requireInput('rowsFirst').value = String(range ? Math.min(range.row1, range.row2) : selected);
    requireInput('rowsLast').value = String(range ? Math.max(range.row1, range.row2) : selected);
    this.page = 0;
    this.status(this.s.rowView?.notice ?? '');
    this.controls();
    this.openDialog('rowsDialog');
    await this.groups();
  }

  async groups(): Promise<void> {
    const id = this.s.rowView?.id;
    const ticket = ++this.pageSerial;
    if (!id) return;
    try {
      const data = await this.api(
        `/api/row-view/groups?id=${encodeURIComponent(id)}&offset=${this.page * 200}&limit=200`,
        undefined,
        decodeRowViewGroupsResponse,
      );
      if (ticket !== this.pageSerial || id !== this.s.rowView?.id) return;
      const list = requireEl('rowsGroupList');
      list.replaceChildren();
      for (const group of data.groups ?? []) {
        const row = document.createElement('div');
        row.className = 'outline-item';
        row.style.setProperty('--outline-level', String(group.level));
        const label = document.createElement('span');
        label.textContent = localize('ui.rowViews.row', { min: group.min, max: group.max });
        const level = document.createElement('small');
        level.textContent = localize('ui.rowViews.level', { level: group.level });
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'tool-button';
        const expanded = Boolean(group.visible) && !group.collapsed;
        button.textContent = expanded ? localize('ui.rowViews.collapse') : localize('ui.rowViews.expand');
        button.setAttribute('aria-expanded', String(expanded));
        button.addEventListener('click', () => {
          void this.change({
            action: 'group',
            group: {
              min: group.min,
              max: group.max,
              level: group.level,
              collapsed: expanded,
            },
          });
        });
        row.append(label, level, button);
        list.append(row);
      }
      if (!data.total) {
        const empty = document.createElement('p');
        empty.className = 'dialog-summary';
        empty.textContent = localize('ui.rowViews.thisWorksheetNoGroupedOutlines');
        list.append(empty);
      }
      this.groupTotal = data.total;
      requireEl('rowsPage').textContent = data.total
        ? `${this.page + 1} / ${Math.ceil(data.total / 200)}`
        : '';
      this.controls();
    } catch (error: unknown) {
      if (ticket === this.pageSerial && id === this.s.rowView?.id) {
        this.status(errorMessage(error), true);
      }
    }
  }

  private range(hidden: boolean): void {
    const first = Number(requireInput('rowsFirst').value);
    const last = Number(requireInput('rowsLast').value);
    if (
      !Number.isSafeInteger(first) ||
      !Number.isSafeInteger(last) ||
      first < 1 ||
      last < first ||
      last > this.maximum()
    ) {
      this.status(localize('ui.rowViews.enterValidSourceRange'), true);
      return;
    }
    void this.change({ action: 'range', first, last, hidden });
  }

  async change(change: RowViewChange, options: RowViewChangeOptions = {}): Promise<boolean> {
    const { temporary = false, guard = () => true } = options;
    if (this.busy) return false;
    const base = this.s.rowView;
    if (!base) return false;
    const intent = ++this.changeSerial;
    const book = this.s.workbookKey;
    const sheet = this.s.sheet;
    const epoch = this.s.sheetEpoch;
    const filterId = this.s.filterId;
    let ticket: number | undefined;
    let data: RowViewState | undefined;
    const current = (): boolean =>
      intent === this.changeSerial &&
      (ticket === undefined || ticket === this.serial) &&
      this.current(this.serial, book, sheet, epoch) &&
      filterId === this.s.filterId &&
      guard();
    this.setBusy(true);
    this.controls();
    this.status(localize('ui.rowViews.updatingVisibleRows'));
    try {
      const pending = this.build({
        baseId: base.id,
        change,
        persist: !temporary && !base.settingsWarning && requireInput('rowsRemember').checked,
        revision: base.revision,
        guard: current,
      });
      ticket = this.serial;
      data = await pending;
      if (!current()) throw interrupted();
      await this.commit(data, current);
      if (!current() || this.s.rowView?.id !== data.id) throw interrupted();
      this.status(
        temporary
          ? localize('ui.rowViews.temporarilyDisplayedSavedSettingsHave')
          : data.settingsWarning
            ? localize('ui.rowViews.temporarilyUpdatedOriginalSettingsFile')
            : localize('ui.rowViews.updatedRowDisplayHide'),
      );
      this.page = 0;
      if (requireDialog('rowsDialog').open) await this.groups();
      return true;
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) this.status(errorMessage(error), true);
      return false;
    } finally {
      if (data && !Array.from(this.cache.values()).some((item) => item.id === data?.id)) {
        this.cleanup(data.id);
      }
      if (intent === this.changeSerial) {
        this.setBusy(false);
        this.controls();
      }
    }
  }

  reveal(row: number, guard: () => boolean = () => true): Promise<boolean> {
    return this.change({ action: 'range', first: row, last: row, hidden: false }, { temporary: true, guard });
  }
}
