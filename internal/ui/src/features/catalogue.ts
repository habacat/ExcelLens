import { decodeCatalogueJobStatus } from '../core/http-decoders.generated.ts';
import type { AsyncJobProgress } from '../contracts/common.ts';
/**
 * Workbook Defined Names & Table Objects Catalogue dialog with bounded paging,
 * table-object confirmation, and guarded area navigation.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type {
  CatalogueArea,
  CatalogueItem,
  CatalogueItemsResponse,
  CatalogueListItem,
} from '../contracts/features.ts';
import type { CatalogueLimits } from '../contracts/formats.ts';
import { AsyncJobs } from '../core/async-jobs.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  decodeCatalogueDetailResponse,
  decodeCatalogueItemsResponse,
  decodeCatalogueResolveResponse,
} from '../core/http-decoders.generated.ts';

export interface CatalogueOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly supported: () => boolean;
  readonly navigate: (
    area: Pick<CatalogueArea, 'sheet' | 'start' | 'end'>,
    current: () => boolean,
  ) => Promise<boolean>;
  readonly limits: CatalogueLimits;
  readonly canNavigate?: () => boolean;
  readonly sleep?: (ms: number) => Promise<void>;
}

/** @httpResponse CatalogueJobStatus */
export interface CatalogueJobStatus extends AsyncJobProgress {
  readonly workbookKey?: string;
  readonly book?: string;
}

/** @httpResponse CatalogueDetailResponse */
export interface CatalogueDetailResponse {
  readonly workbookKey?: string;
  readonly book?: string;
  readonly item?: CatalogueItem;
}

/** @httpResponse CatalogueResolveResponse */
export interface CatalogueResolveResponse {
  readonly workbookKey?: string;
  readonly book?: string;
  readonly area: CatalogueArea;
}

export function catalogueItemLabel(item: Pick<CatalogueListItem, 'status' | 'kind'>): string {
  return item.status === 'ready'
    ? '可定位'
    : item.kind === 'table' && item.status === 'unresolved'
      ? '待确认'
      : '不可定位';
}

export const CatalogueModel = Object.freeze({
  label: catalogueItemLabel,
});

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  node.textContent = text;
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export class Catalogue {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly supported: () => boolean;
  private readonly navigateArea: CatalogueOptions['navigate'];
  private readonly canNavigate: () => boolean;
  private readonly limits: CatalogueLimits;
  private readonly jobs: AsyncJobs<CatalogueJobStatus, CatalogueJobStatus>;

  public readonly dialog: HTMLDialogElement;
  private epoch = 0;
  private request = 0;
  private pageRequest = 0;
  private detailRequest = 0;
  private nav = 0;
  public job = '';
  private offset = 0;
  private columnOffset = 0;
  public page: CatalogueItemsResponse | null = null;
  public item: CatalogueItem | null = null;
  private selected = '';
  public busy = false;
  private navigating = false;
  private pageController: AbortController | null = null;
  private detailController: AbortController | null = null;
  private searchTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(options: CatalogueOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.supported = options.supported;
    this.navigateArea = options.navigate;
    this.canNavigate = options.canNavigate ?? (() => true);
    this.limits = options.limits;

    if (
      !Number.isSafeInteger(this.limits.pageRows) ||
      this.limits.pageRows < 1 ||
      !Number.isSafeInteger(this.limits.searchBytes) ||
      this.limits.searchBytes < 1
    ) {
      throw new Error('目录显示容量配置无效');
    }

    const sleep = options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
    this.dialog = this.createDialog();
    this.jobs = new AsyncJobs<CatalogueJobStatus, CatalogueJobStatus>({
      decodeStatus: decodeCatalogueJobStatus,
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.s.workbookKey,
      sleep,
      endpoint: '/api/catalogue',
      label: '目录读取',
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) => {
        const done = status.doneBytes ?? 0;
        const total = status.totalBytes ?? 0;
        this.status(
          (status.stage || '正在读取') + (total > 0 ? ` · ${Math.round((done / total) * 100)}%` : ''),
        );
      },
    });

    requireButton('catalogueOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('catalogueClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.stop();
    });
    requireButton('catalogueRefresh').addEventListener('click', () => {
      void this.refresh('read');
    });
    requireButton('catalogueCancel').addEventListener('click', () => this.cancel());
    requireButton('catalogueConfirm').addEventListener('click', () => {
      void this.refresh('confirm');
    });
    requireButton('catalogueOpenSheet').addEventListener('click', () => {
      void this.openSheet();
    });
    for (const id of ['catalogueKind', 'catalogueSheet']) {
      requireSelect(id).addEventListener('change', () => {
        void this.loadPage(0);
      });
    }
    requireSelect('catalogueTargetSheet').addEventListener('change', () => this.controls());
    requireInput('catalogueSearch').addEventListener('input', () => {
      if (this.searchTimer !== undefined) clearTimeout(this.searchTimer);
      this.searchTimer = setTimeout(() => {
        void this.loadPage(0);
      }, 160);
    });
    requireButton('cataloguePrev').addEventListener('click', () => {
      void this.loadPage(this.page?.previous ?? 0);
    });
    requireButton('catalogueNext').addEventListener('click', () => {
      void this.loadPage(this.page?.next ?? 0);
    });
    requireEl('catalogueList').addEventListener('keydown', (event: KeyboardEvent) =>
      this.key(event),
    );
    // A later user action cancels a pending arrival; programmatic shared-grid
    // navigation emits neither event.
    document.addEventListener('pointerdown', () => this.cancelNavigation(), true);
    document.addEventListener('keydown', () => this.cancelNavigation(), true);
    this.controls();
  }

  private createDialog(): HTMLDialogElement {
    const dialog = makeEl('dialog', 'float-dialog catalogue-dialog');
    dialog.id = 'catalogueDialog';
    dialog.setAttribute('aria-labelledby', 'catalogueTitle');
    // Static markup only. Workbook names, definitions and reasons never enter HTML.
    dialog.innerHTML =
      '<div class="dialog-head"><div id="catalogueTitle" class="dialog-title">名称与表目录</div><button id="catalogueClose" class="dialog-close" type="button" aria-label="关闭名称与表目录">×</button></div><p id="catalogueSource" class="catalogue-source"></p><div class="catalogue-tools"><input id="catalogueSearch" class="field-control" type="search" placeholder="搜索名称或工作表" aria-label="搜索名称或工作表"><select id="catalogueKind" class="field-control" aria-label="目录类型"><option value="">全部类型</option><option value="name">定义名称</option><option value="table">表对象</option></select><select id="catalogueSheet" class="field-control" aria-label="目录工作表"></select><button id="catalogueRefresh" class="mini-button" type="button">重新读取</button></div><div class="catalogue-main"><div class="catalogue-master"><div id="catalogueList" class="catalogue-list" role="listbox" aria-label="名称与表对象" tabindex="0"></div><div class="catalogue-paging"><button id="cataloguePrev" class="mini-button" type="button">上一页</button><span id="catalogueCount"></span><button id="catalogueNext" class="mini-button" type="button">下一页</button></div></div><section id="catalogueDetail" class="catalogue-detail" aria-label="目录定义详情"></section></div><div class="catalogue-footer"><p id="catalogueStatus" class="catalogue-status" role="status" aria-live="polite"></p><button id="catalogueCancel" class="mini-button" type="button" hidden>取消读取</button><div class="catalogue-confirm"><select id="catalogueTargetSheet" class="field-control" aria-label="要确认的表对象工作表"></select><button id="catalogueOpenSheet" class="mini-button" type="button">打开工作表</button><button id="catalogueConfirm" class="mini-button" type="button">确认表对象</button></div></div>';
    document.body.append(dialog);
    return dialog;
  }

  private identity(): string {
    return JSON.stringify([this.s.workbookKey, this.s.book, this.epoch]);
  }

  private post(path: string, data: unknown): Promise<unknown> {
    return this.api(path, { method: 'POST', body: JSON.stringify(data) });
  }

  private release(id: string): void {
    if (!id) return;
    this.post('/api/catalogue/clear', { id }).catch(() => {
      // Best-effort cleanup of abandoned catalogue snapshot.
    });
  }

  private validSource(value: { readonly workbookKey?: string; readonly book?: string }): void {
    if (value.workbookKey !== this.s.workbookKey || value.book !== this.s.book) {
      throw new Error('目录来源已切换，请重新读取');
    }
  }

  private status(text: string, error = false): void {
    const el = requireEl('catalogueStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
    el.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    const enabled = this.s.opened && this.supported();
    const target = requireSelect('catalogueTargetSheet').value;
    requireButton('catalogueOpenBtn').disabled = !enabled;
    requireButton('catalogueRefresh').disabled = !enabled || this.busy;
    requireButton('catalogueCancel').hidden = !this.busy;
    requireInput('catalogueSearch').disabled = this.busy;
    requireSelect('catalogueKind').disabled = this.busy;
    requireSelect('catalogueSheet').disabled = this.busy;
    requireSelect('catalogueTargetSheet').disabled = this.busy;
    const confirmBtn = requireButton('catalogueConfirm');
    confirmBtn.disabled =
      !enabled || this.busy || !target || target !== this.s.sheet || !this.s.indexReady;
    confirmBtn.title =
      target === this.s.sheet && this.s.indexReady
        ? '核对这张工作表真实声明的表对象'
        : '先打开并完成索引，再确认这张表的对象';
    requireButton('catalogueOpenSheet').disabled = !target || this.busy || !this.canNavigate();
    requireButton('cataloguePrev').disabled = this.busy || !this.page || this.offset === 0;
    requireButton('catalogueNext').disabled =
      this.busy || !this.page || this.page.next >= this.page.matched;
  }

  private sheets(): void {
    const filter = requireSelect('catalogueSheet');
    const target = requireSelect('catalogueTargetSheet');
    const chosen = filter.value;
    filter.replaceChildren(
      new Option('全部作用域与表', ''),
      new Option('工作簿名称', 'workbook:'),
    );
    target.replaceChildren();
    for (const sheet of this.s.sheets) {
      filter.append(new Option(sheet.name, `sheet:${sheet.name}`));
      target.append(new Option(sheet.name, sheet.name));
    }
    const validChoices = ['', 'workbook:', ...this.s.sheets.map((s) => `sheet:${s.name}`)];
    filter.value = validChoices.includes(chosen) ? chosen : '';
    target.value = this.s.sheet ?? '';
  }

  async open(search = ''): Promise<void> {
    if (!this.s.opened || !this.supported()) {
      this.toast('当前来源没有工作簿名称或表对象目录', 'warning');
      return;
    }
    this.sheets();
    requireEl('catalogueSource').textContent = this.s.fileName || '当前工作簿';
    if (search) requireInput('catalogueSearch').value = search;
    if (!this.dialog.open) this.dialog.showModal();
    this.controls();
    if (this.job && !this.busy) await this.loadPage(search ? 0 : this.offset);
    else if (!this.busy) await this.refresh('read');
    if (this.dialog.open) requireInput('catalogueSearch').focus({ preventScroll: true });
  }

  private stop(): void {
    ++this.epoch;
    ++this.request;
    ++this.pageRequest;
    ++this.detailRequest;
    this.jobs.reset();
    this.pageController?.abort();
    this.detailController?.abort();
    if (this.searchTimer !== undefined) clearTimeout(this.searchTimer);
    this.busy = false;
    this.controls();
  }

  close(): void {
    this.stop();
    if (this.dialog.open) this.dialog.close();
  }

  resetBook(): void {
    this.cancelNavigation();
    this.stop();
    this.release(this.job);
    this.job = '';
    this.page = null;
    this.item = null;
    this.selected = '';
    this.offset = 0;
    requireInput('catalogueSearch').value = '';
    requireEl('catalogueList').replaceChildren();
    requireEl('catalogueDetail').replaceChildren();
    this.status('');
    if (this.dialog.open) this.dialog.close();
    this.controls();
  }

  cancel(): void {
    ++this.request;
    ++this.pageRequest;
    ++this.detailRequest;
    this.pageController?.abort();
    this.detailController?.abort();
    this.jobs.cancelMain();
    this.busy = false;
    this.status('已取消读取，原目录保留');
    this.controls();
  }

  async refresh(mode: 'read' | 'confirm'): Promise<void> {
    if (this.busy) return;
    if (mode === 'confirm' && requireButton('catalogueConfirm').disabled) return;
    const request = ++this.request;
    const identity = this.identity();
    const book = this.s.book;
    const workbookKey = this.s.workbookKey;
    const sheet = requireSelect('catalogueTargetSheet').value;
    this.pageController?.abort();
    this.detailController?.abort();
    ++this.pageRequest;
    ++this.detailRequest;
    this.busy = true;
    this.controls();
    this.status(mode === 'confirm' ? '正在确认表对象…' : '正在读取名称与表定义…');
    const current = (): boolean =>
      request === this.request && identity === this.identity() && this.dialog.open;
    let owned = '';
    let adopted = false;
    try {
      const done = await this.jobs.execute(
        { book, workbookKey, mode, ...(mode === 'confirm' ? { sheet } : {}) },
        { main: true },
      );
      owned = done.job;
      if (!current() || !done.current()) return;
      this.validSource(done.result);
      const controller = new AbortController();
      this.pageController = controller;
      const page = await this.readPage(done.job, 0, controller.signal);
      if (!current() || controller.signal.aborted) return;
      const old = this.job;
      this.job = done.job;
      adopted = true;
      this.item = null;
      this.selected = '';
      this.adoptPage(page, 0);
      this.release(old);
      const first = page.items[0];
      if (first) await this.select(first.id, false);
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) this.status(errorMessage(error), true);
    } finally {
      if (owned && !adopted) this.release(owned);
      if (current()) {
        this.busy = false;
        this.controls();
      }
    }
  }

  private isDialogOpen(): boolean {
    return this.dialog.open;
  }

  async loadPage(offset: number): Promise<void> {
    if (!this.job || !this.dialog.open || this.busy) return;
    if (new TextEncoder().encode(requireInput('catalogueSearch').value).length > this.limits.searchBytes) {
      this.status('搜索文字超过容量，请缩短后重试', true);
      return;
    }
    const serial = ++this.pageRequest;
    const identity = this.identity();
    const job = this.job;
    this.pageController?.abort();
    const controller = new AbortController();
    this.pageController = controller;
    ++this.detailRequest;
    this.detailController?.abort();
    this.item = null;
    this.selected = '';
    requireEl('catalogueDetail').textContent = '选择条目查看原始定义和定位范围';
    requireEl('catalogueList').replaceChildren(makeEl('div', 'catalogue-empty', '正在读取目录…'));
    try {
      const page = await this.readPage(job, offset, controller.signal);
      if (
        serial !== this.pageRequest ||
        identity !== this.identity() ||
        job !== this.job ||
        !this.isDialogOpen()
      ) {
        return;
      }
      this.adoptPage(page, offset);
      const first = page.items[0];
      if (first) await this.select(first.id, false);
    } catch (error: unknown) {
      if (serial === this.pageRequest && identity === this.identity() && !isAbortError(error)) {
        if (this.page) this.renderList();
        this.status(errorMessage(error), true);
        this.controls();
      }
    }
  }

  private async readPage(
    job: string,
    offset: number,
    signal: AbortSignal,
  ): Promise<CatalogueItemsResponse> {
    const scope = requireSelect('catalogueSheet').value;
    const searchVal = requireInput('catalogueSearch').value;
    const q = new URLSearchParams({
      id: job,
      offset: String(offset),
      q: searchVal,
      kind: requireSelect('catalogueKind').value,
      scope: scope === 'workbook:' ? 'workbook' : '',
      sheet: scope.startsWith('sheet:') ? scope.slice(6) : '',
    });
    if (new TextEncoder().encode(searchVal).length > this.limits.searchBytes) {
      throw new Error('搜索文字超过容量，请缩短后重试');
    }
    const page = await this.api(`/api/catalogue/items?${q.toString()}`, {
      signal,
    }, decodeCatalogueItemsResponse);
    this.validSource(page);
    if (
      page.items.length > this.limits.pageRows ||
      !Number.isSafeInteger(page.next) ||
      !Number.isSafeInteger(page.previous) ||
      !Number.isSafeInteger(page.matched) ||
      !Number.isSafeInteger(page.total) ||
      page.offset !== offset ||
      page.next !== offset + page.items.length ||
      page.previous < 0 ||
      page.previous > offset ||
      (page.next > page.matched && page.items.length > 0)
    ) {
      throw new Error('目录分页响应无效');
    }
    return page;
  }

  private adoptPage(page: CatalogueItemsResponse, offset: number): void {
    this.page = page;
    this.offset = offset;
    this.renderList();
    this.controls();
    this.status((page.complete ? '' : '目录尚不完整。') + page.notices.join('；'));
  }

  private renderList(): void {
    const page = this.page;
    if (!page) return;
    const list = requireEl('catalogueList');
    list.replaceChildren();
    if (!page.items.length) {
      list.append(
        makeEl(
          'div',
          'catalogue-empty',
          page.total
            ? '没有符合条件的名称或表对象'
            : '没有已读取的名称或表对象。可在下方确认当前工作表。',
        ),
      );
    }
    for (const item of page.items) {
      const button = makeEl('button', 'catalogue-item');
      button.type = 'button';
      button.dataset.id = item.id;
      button.setAttribute('role', 'option');
      button.setAttribute('aria-selected', String(this.selected === item.id));
      button.tabIndex = -1;
      const name = makeEl('span', 'catalogue-item-name', item.name || '未能读取名称');
      name.title = item.shortened ? '完整名称请查看条目详情' : item.name;
      const meta = makeEl('span', 'catalogue-item-meta');
      meta.append(
        makeEl(
          'span',
          'catalogue-item-scope',
          `${item.kind === 'table' ? '表对象' : '名称'} · ${item.scope || item.sheet || (item.unknownScope ? '作用域未能读取' : '工作簿')}${item.hidden ? ' · 隐藏' : ''}`,
        ),
        makeEl('span', 'catalogue-item-state', catalogueItemLabel(item)),
      );
      button.append(name, meta);
      button.addEventListener('click', () => {
        void this.select(item.id, true);
      });
      button.addEventListener('dblclick', () => {
        void this.locate();
      });
      list.append(button);
    }
    requireEl('catalogueCount').textContent = page.matched
      ? `${this.offset + 1}–${page.next} / ${page.matched}`
      : `0 / ${page.total}`;
  }

  async select(id: string, focus: boolean): Promise<void> {
    this.selected = id;
    this.item = null;
    const serial = ++this.detailRequest;
    const identity = this.identity();
    const job = this.job;
    this.detailController?.abort();
    const controller = new AbortController();
    this.detailController = controller;
    for (const button of requireEl('catalogueList').querySelectorAll<HTMLButtonElement>(
      'button[data-id]',
    )) {
      const chosen = button.dataset.id === id;
      button.setAttribute('aria-selected', String(chosen));
      button.tabIndex = chosen ? 0 : -1;
      if (chosen && focus) button.focus({ preventScroll: true });
    }
    requireEl('catalogueDetail').textContent = '正在读取定义…';
    try {
      const data = await this.api(
        `/api/catalogue/detail?${new URLSearchParams({ id: job, item: id }).toString()}`,
        { signal: controller.signal },
        decodeCatalogueDetailResponse,
      );
      if (
        serial !== this.detailRequest ||
        identity !== this.identity() ||
        job !== this.job ||
        !this.dialog.open ||
        this.selected !== id
      ) {
        return;
      }
      this.validSource(data);
      if (data.item?.id !== id) throw new Error('目录条目响应无效');
      this.item = data.item;
      this.columnOffset = 0;
      if (
        data.item.kind === 'table' &&
        data.item.sheet &&
        this.s.sheets.some((sheet) => sheet.name === data.item?.sheet)
      ) {
        requireSelect('catalogueTargetSheet').value = data.item.sheet;
      }
      this.renderDetail();
      this.controls();
    } catch (error: unknown) {
      if (serial === this.detailRequest && identity === this.identity() && !isAbortError(error)) {
        const msg = errorMessage(error);
        requireEl('catalogueDetail').textContent = msg;
        this.status(msg, true);
      }
    }
  }

  private renderDetail(): void {
    const item = this.item;
    const body = requireEl('catalogueDetail');
    body.replaceChildren();
    if (!item) return;
    body.append(
      makeEl('h3', '', item.name || '未能读取名称'),
      makeEl(
        'p',
        'catalogue-reason',
        `${item.kind === 'table' ? '表对象' : '定义名称'} · ${item.scope || item.sheet || (item.scopeId ? '作用域未能读取' : '工作簿')}${item.hidden ? ' · 隐藏' : ''}`,
      ),
    );
    body.append(
      makeEl('h4', '', '原始定义'),
      makeEl('pre', 'catalogue-definition', item.definition || '没有可读取的文本定义'),
    );
    if (item.comment) body.append(makeEl('h4', '', '注释'), makeEl('p', '', item.comment));
    body.append(makeEl('p', 'catalogue-reason', item.reason || '已确认固定范围，可按源坐标定位。'));

    const areas = makeEl('div', 'catalogue-areas');
    const pick = makeEl('select', 'field-control');
    pick.id = 'catalogueArea';
    pick.setAttribute('aria-label', '选择定位区域');
    for (const [index, area] of (item.areas ?? []).entries()) {
      pick.append(
        new Option(
          `${area.sheet}!${area.start}${area.end === area.start ? '' : `:${area.end}`}`,
          String(index),
        ),
      );
    }
    const go = makeEl('button', 'primary-button compact', '定位区域');
    go.id = 'catalogueLocate';
    go.type = 'button';
    go.disabled = item.status !== 'ready' || !item.areas?.length;
    go.addEventListener('click', () => {
      void this.locate();
    });
    pick.disabled = go.disabled;
    areas.append(pick, go);
    body.append(areas);

    if (item.tokenHex) {
      const details = makeEl('details');
      details.append(
        makeEl('summary', '', '原始公式标记'),
        makeEl('pre', 'catalogue-definition', item.tokenHex),
      );
      body.append(details);
    }
    if (item.table) {
      body.append(
        makeEl(
          'h4',
          '',
          `表头 ${item.table.headerRows} 行，合计 ${item.table.totalRows} 行，${item.table.columns?.length ?? 0} 列`,
        ),
      );
      this.renderColumns(body, item.table.columns ?? []);
    }
  }

  private renderColumns(
    body: HTMLElement,
    columns: readonly { readonly id: string; readonly name: string }[],
  ): void {
    const size = this.limits.pageRows;
    const table = makeEl('table', 'catalogue-columns');
    table.setAttribute('aria-label', '表对象列定义');
    const rows = makeEl('tbody');
    for (const [index, column] of columns.slice(this.columnOffset, this.columnOffset + size).entries()) {
      const tr = makeEl('tr');
      tr.append(makeEl('td', '', String(this.columnOffset + index + 1)), makeEl('td', '', column.name));
      rows.append(tr);
    }
    table.append(rows);
    body.append(table);
    if (columns.length > size) {
      const paging = makeEl('div', 'catalogue-paging');
      const prev = makeEl('button', 'mini-button', '上一页列');
      const next = makeEl('button', 'mini-button', '下一页列');
      prev.type = 'button';
      next.type = 'button';
      prev.disabled = this.columnOffset === 0;
      next.disabled = this.columnOffset + size >= columns.length;
      prev.onclick = () => {
        this.columnOffset = Math.max(0, this.columnOffset - size);
        this.renderDetail();
      };
      next.onclick = () => {
        this.columnOffset += size;
        this.renderDetail();
      };
      paging.append(
        prev,
        makeEl(
          'span',
          '',
          `${this.columnOffset + 1}–${Math.min(columns.length, this.columnOffset + size)} / ${columns.length}`,
        ),
        next,
      );
      body.append(paging);
    }
  }

  private key(event: KeyboardEvent): void {
    const items = this.page?.items;
    if (!items?.length || isComposing(event)) return;
    const at = Math.max(
      0,
      items.findIndex((item) => item.id === this.selected),
    );
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const target = items[Math.max(0, Math.min(items.length - 1, at + (event.key === 'ArrowDown' ? 1 : -1)))];
      if (target) void this.select(target.id, true);
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      void this.locate();
    }
  }

  private cancelNavigation(): void {
    if (this.navigating) {
      ++this.nav;
      this.navigating = false;
    }
  }

  async openSheet(): Promise<void> {
    const sheet = requireSelect('catalogueTargetSheet').value;
    if (sheet && this.canNavigate()) {
      await this.go({ sheet, start: 'A1', end: 'A1' }, null);
    }
  }

  async locate(): Promise<void> {
    if (this.item?.id !== this.selected || this.item.status !== 'ready' || !this.canNavigate()) {
      return;
    }
    const areaSelect = byId('catalogueArea');
    if (!(areaSelect instanceof HTMLSelectElement)) return;
    const index = Number(areaSelect.value);
    const area = Number.isSafeInteger(index) && index >= 0 ? this.item.areas?.[index] : undefined;
    if (!area) return;
    await this.go(area, {
      id: this.job,
      item: this.item.id,
      area: String(index),
      workbookKey: this.s.workbookKey,
      book: this.s.book,
    });
  }

  private async go(
    initialArea: Pick<CatalogueArea, 'sheet' | 'start' | 'end'>,
    resolve: Record<string, string> | null,
  ): Promise<void> {
    const nav = ++this.nav;
    const book = this.s.book;
    const key = this.s.workbookKey;
    const origin = JSON.stringify([this.s.sheet, this.s.sheetEpoch, this.s.selected?.ref]);
    this.navigating = true;
    const current = (): boolean =>
      nav === this.nav && book === this.s.book && key === this.s.workbookKey;
    try {
      let area = initialArea;
      if (resolve) {
        const result = await this.api(
          `/api/catalogue/resolve?${new URLSearchParams(resolve).toString()}`,
          undefined,
          decodeCatalogueResolveResponse,
        );
        if (
          !current() ||
          origin !== JSON.stringify([this.s.sheet, this.s.sheetEpoch, this.s.selected?.ref])
        ) {
          return;
        }
        this.validSource(result);
        area = result.area;
      }
      if (!current()) return;
      this.close();
      const arrived = await this.navigateArea(area, current);
      if (current() && arrived) {
        this.toast(
          `已定位：${area.sheet}!${area.start}${area.end === area.start ? '' : `:${area.end}`}`,
          'success',
        );
      }
    } catch (error: unknown) {
      if (current()) {
        if (this.dialog.open) this.status(errorMessage(error), true);
        else this.toast(errorMessage(error), 'warning');
      }
    } finally {
      if (current()) this.navigating = false;
    }
  }
}
