import { decodeNamedViewFilterJobStatus } from '../core/http-decoders.generated.ts';
import type { AsyncJobProgress } from '../contracts/common.ts';
/**
 * Named Views manager: save, update, delete, resolve, and replay workbook
 * filter/sort/freeze/layout/row-visibility views.
 */
import type {
  ApiClient,
  ApiRequestOptions,
  AsyncJobStatusKind,
  ToastFn,
} from '../contracts/common.ts';
import type {
  NamedView,
  NamedViewConfig,
  NamedViewStore,
} from '../contracts/features.ts';
import type {
  FilterStatusResponse,
  SourceFilterSpec,
} from '../contracts/workbook.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeNamedViewCatalogResponse,
  decodeNamedViewResolveResponse,
} from '../core/http-decoders.generated.ts';

export interface NamedViewCapturePayload {
  readonly sheet: string;
  readonly filterId: string;
  readonly viewId?: string;
  readonly headerRow: number;
  readonly config: NamedViewConfig;
}

/** @httpResponse NamedViewCatalogResponse */
export interface NamedViewCatalogResponse {
  readonly book: string;
  readonly store: NamedViewStore;
  readonly id?: string;
}

/** @httpResponse NamedViewResolveResponse */
export interface NamedViewResolveResponse {
  readonly view: NamedView;
  readonly book?: string;
  readonly workbookKey?: string;
}

/** @httpResponse NamedViewFilterJobStatus */
export interface NamedViewFilterJobStatus
  extends Omit<FilterStatusResponse, 'status' | 'id'>,
    AsyncJobProgress {
  readonly id?: string;
  readonly status?: AsyncJobStatusKind;
}

export interface NamedViewsOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly capture: () => NamedViewCapturePayload;
  readonly apply: (
    prepared: NamedViewResolveResponse,
    data: NamedViewFilterJobStatus | null,
    current: () => boolean,
  ) => Promise<boolean>;
  readonly canApply?: () => boolean;
  readonly supportsRowVisibility?: () => boolean;
  readonly sleep?: (ms: number) => Promise<void>;
}

function copyValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export function isNamedViewFilterActive(query: SourceFilterSpec): boolean {
  return Boolean(query.rules?.length || query.sortKeys?.length || query.sortColumn);
}

export function namedViewSummary(view: NamedView): string {
  const q = view.query;
  const c = view.config;
  const parts: string[] = [view.source.sheet];
  const sort = q.sortKeys?.length || (q.sortColumn ? 1 : 0);
  if (q.rules?.length) parts.push(`${q.rules.length}项筛选`);
  if (sort) parts.push(`${sort}级排序`);
  if (c.freeze.rows || c.freeze.cols) parts.push(`冻结${c.freeze.rows}行${c.freeze.cols}列`);
  if (c.zoom !== 100) parts.push(`${c.zoom}%`);
  return parts.join(' · ');
}

export const NamedViewsModel = Object.freeze({
  summary: namedViewSummary,
  active: isNamedViewFilterActive,
});

export class NamedViews {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly capture: () => NamedViewCapturePayload;
  private readonly applyView: NamedViewsOptions['apply'];
  private readonly canApply: () => boolean;
  private readonly supportsRowVisibility: () => boolean;
  private readonly jobs: AsyncJobs<NamedViewFilterJobStatus, NamedViewFilterJobStatus>;

  public readonly dialog: HTMLDialogElement;
  private epoch = 0;
  private serial = 0;
  private catalogSerial = 0;
  public busy = false;
  private mode: '' | 'save' | 'delete' | 'apply' | 'commit' = '';
  private controller: AbortController | null = null;
  private catalogController: AbortController | null = null;
  private book = '';
  public store: NamedViewStore | null = null;

  constructor(options: NamedViewsOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.capture = options.capture;
    this.applyView = options.apply;
    this.canApply = options.canApply ?? (() => true);
    this.supportsRowVisibility = options.supportsRowVisibility ?? (() => false);

    this.dialog = requireDialog('namedViewDialog');
    this.jobs = new AsyncJobs<NamedViewFilterJobStatus, NamedViewFilterJobStatus>({
      decodeStatus: decodeNamedViewFilterJobStatus,
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.s.workbookKey,
      sleep: options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms))),
      endpoint: '/api/filter',
      label: '视图查询',
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) =>
        this.status(
          `${status.stage || '正在恢复视图'}${status.total ? ` · ${(status.scanned || 0).toLocaleString()} / ${status.total.toLocaleString()}行` : ''}`,
        ),
    });

    requireButton('namedViewOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('namedViewRefresh').addEventListener('click', () => {
      void this.refresh();
    });
    requireSelect('namedViewList').addEventListener('change', () => this.select());
    requireButton('namedViewSave').addEventListener('click', () => {
      void this.save(false);
    });
    requireButton('namedViewUpdate').addEventListener('click', () => {
      void this.save(true);
    });
    requireButton('namedViewDelete').addEventListener('click', () => {
      void this.remove();
    });
    requireButton('namedViewApply').addEventListener('click', () => {
      void this.apply();
    });
    requireButton('namedViewCancel').addEventListener('click', () => this.cancel());
  }

  private identity(): string {
    return JSON.stringify([this.s.workbookKey, this.s.sheet, this.s.sheetEpoch]);
  }

  private post(
    url: string,
    data: unknown,
    options: ApiRequestOptions = {},
  ): Promise<unknown> {
    return this.api(url, { method: 'POST', body: JSON.stringify(data), ...options });
  }

  private selected(): NamedView | undefined {
    const id = requireSelect('namedViewList').value;
    return this.store?.items.find((item) => item.id === id);
  }

  private status(text: string, error = false): void {
    const el = requireEl('namedViewStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
  }

  controls(): void {
    const selected = this.selected() !== undefined;
    requireButton('namedViewRefresh').disabled = this.busy;
    requireSelect('namedViewList').disabled = this.busy || this.store === null;
    requireInput('namedViewName').disabled = this.busy || this.store === null;
    requireButton('namedViewSave').disabled = this.busy || this.store === null;
    for (const id of ['namedViewApply', 'namedViewUpdate', 'namedViewDelete']) {
      requireButton(id).disabled = this.busy || !selected;
    }
    requireButton('namedViewCancel').hidden = !(this.busy && this.mode === 'apply');
  }

  private stopCatalog(): void {
    ++this.catalogSerial;
    this.catalogController?.abort();
    this.catalogController = null;
  }

  resetBook(): void {
    ++this.epoch;
    ++this.serial;
    ++this.catalogSerial;
    this.jobs.reset();
    this.controller?.abort();
    this.catalogController?.abort();
    this.controller = null;
    this.catalogController = null;
    this.book = '';
    this.store = null;
    this.busy = false;
    this.mode = '';
    requireSelect('namedViewList').replaceChildren();
    requireInput('namedViewName').value = '';
    requireEl('namedViewSummary').textContent = '';
    this.status('');
    this.dialog.close();
    this.controls();
  }

  async open(): Promise<void> {
    if (!this.s.opened || !this.s.indexReady) {
      this.toast('请先打开工作表', 'warning');
      return;
    }
    if (!this.dialog.open) this.dialog.showModal();
    await this.refresh();
  }

  private setCatalog(data: NamedViewCatalogResponse, selected = ''): void {
    this.book = data.book;
    this.store = data.store;
    const list = requireSelect('namedViewList');
    const previous = selected || list.value;
    list.replaceChildren();
    const empty = new Option(this.store.items.length ? '选择已保存视图' : '尚无已保存视图', '');
    list.append(empty);
    for (const view of this.store.items) {
      list.append(new Option(`${view.name} · ${view.source.sheet}`, view.id));
    }
    list.value = this.store.items.some((item) => item.id === previous) ? previous : '';
    this.select(false);
    this.controls();
  }

  async refresh(): Promise<void> {
    if (this.busy) return;
    const serial = ++this.catalogSerial;
    const epoch = this.epoch;
    const book = this.s.workbookKey;
    this.catalogController?.abort();
    const controller = new AbortController();
    this.catalogController = controller;
    try {
      const data = await this.api('/api/named-views', {
        signal: controller.signal,
      }, decodeNamedViewCatalogResponse);
      if (serial !== this.catalogSerial || epoch !== this.epoch || book !== this.s.workbookKey) {
        return;
      }
      this.setCatalog(data);
      this.status(`${this.store?.items.length ?? 0} 个视图`);
    } catch (error: unknown) {
      if (serial === this.catalogSerial && epoch === this.epoch && !isAbortError(error)) {
        this.store = null;
        this.status(errorMessage(error), true);
        this.controls();
      }
    } finally {
      if (this.catalogController === controller) this.catalogController = null;
    }
  }

  private select(fillName = true): void {
    const view = this.selected();
    if (view && fillName) requireInput('namedViewName').value = view.name;
    requireEl('namedViewSummary').textContent = view ? namedViewSummary(view) : '';
    this.controls();
  }

  private current(serial: number, epoch: number, book: string): boolean {
    return serial === this.serial && epoch === this.epoch && book === this.s.workbookKey;
  }

  async save(update: boolean): Promise<void> {
    if (this.busy || !this.store) return;
    const selected = this.selected();
    if (update && !selected) return;
    let captured: NamedViewCapturePayload;
    try {
      captured = this.capture();
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }
    const nameInput = requireInput('namedViewName');
    const name = nameInput.value.trim();
    if (!name) {
      this.toast('请输入视图名称', 'warning');
      nameInput.focus();
      return;
    }
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const book = this.s.workbookKey;
    this.busy = true;
    this.mode = 'save';
    this.controls();
    try {
      const request = {
        action: 'save',
        id: update && selected ? selected.id : '',
        name,
        book: this.book,
        workbookKey: book,
        expectedRevision: this.store.revision,
        ...copyValue(captured),
      };
      if (new TextEncoder().encode(JSON.stringify(request)).length > 256 * 1024 + 8192) {
        throw new Error('视图配置过大，请减少尺寸覆盖或筛选值');
      }
      const data = await this.post('/api/named-views', request).then(decodeNamedViewCatalogResponse);
      if (!this.current(serial, epoch, book)) return;
      this.setCatalog(data, data.id ?? '');
      this.status('已保存到本机');
    } catch (error: unknown) {
      if (this.current(serial, epoch, book)) this.status(errorMessage(error), true);
    } finally {
      if (this.current(serial, epoch, book)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
    }
  }

  async remove(): Promise<void> {
    const selected = this.selected();
    if (this.busy || !selected || !this.store) return;
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const book = this.s.workbookKey;
    this.busy = true;
    this.mode = 'delete';
    this.controls();
    try {
      const data = await this.post('/api/named-views', {
        action: 'delete',
        id: selected.id,
        book: this.book,
        workbookKey: book,
        expectedRevision: this.store.revision,
      }).then(decodeNamedViewCatalogResponse);
      if (!this.current(serial, epoch, book)) return;
      this.setCatalog(data);
      this.status('已删除所选视图');
    } catch (error: unknown) {
      if (this.current(serial, epoch, book)) this.status(errorMessage(error), true);
    } finally {
      if (this.current(serial, epoch, book)) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
    }
  }

  cancel(): void {
    if (this.mode !== 'apply') return;
    ++this.serial;
    this.controller?.abort();
    this.jobs.cancelMain();
    this.busy = false;
    this.mode = '';
    this.status('已请求取消应用，当前视图保留');
    this.controls();
  }

  async apply(): Promise<void> {
    const selected = this.selected();
    if (this.busy || !selected || !this.canApply()) return;
    this.stopCatalog();
    const serial = ++this.serial;
    const epoch = this.epoch;
    const book = this.s.workbookKey;
    const identity = this.identity();
    const controller = new AbortController();
    this.controller = controller;
    this.busy = true;
    this.mode = 'apply';
    this.controls();
    this.status('正在核对来源与字段…');
    const current = (): boolean => this.current(serial, epoch, book);
    const unchanged = (): boolean => current() && identity === this.identity();
    let owned = '';
    let adopted = false;
    try {
      const prepared = await this.post(
        '/api/named-views/resolve',
        { id: selected.id, book: this.book, workbookKey: book },
        { signal: controller.signal },
      ).then(decodeNamedViewResolveResponse);
      if (!unchanged()) return;
      const version = prepared.view.ruleVersion || 1;
      if (version !== 1 && !(version === 2 && this.supportsRowVisibility())) {
        throw new Error('此视图版本或行显隐设置尚不支持，未应用');
      }
      let data: NamedViewFilterJobStatus | null = null;
      if (isNamedViewFilterActive(prepared.view.query)) {
        const completed = await this.jobs.execute(
          {
            ...copyValue(prepared.view.query),
            sheet: prepared.view.source.sheet,
            book: this.book,
            workbookKey: book,
          },
          { main: true },
        );
        owned = completed.job;
        if (!unchanged() || !completed.current()) return;
        data = completed.result;
      }
      if (!unchanged()) return;
      this.mode = 'commit';
      this.controls();
      this.status('正在显示已核对的视图…');
      adopted = await this.applyView(prepared, data, current);
      if (!adopted) return;
      if (current()) {
        this.status(`已应用「${prepared.view.name}」`);
        this.dialog.close();
        this.toast(`已应用视图：${prepared.view.name}`, 'success');
      }
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) this.status(errorMessage(error), true);
    } finally {
      if (owned && !adopted) {
        this.post('/api/filter/cancel', { id: owned }).catch(() => {
          // Best-effort cancellation.
        });
      }
      if (current()) {
        this.busy = false;
        this.mode = '';
        this.controls();
      }
      if (this.controller === controller) this.controller = null;
    }
  }
}
