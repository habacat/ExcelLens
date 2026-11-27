/**
 * Read-only Data Validation Rules inspector dialog with bounded pagination,
 * inline list and range browsing, and guarded source-area navigation.
 */
import type { ApiClient, MergeRange, ToastFn } from '../contracts/common.ts';
import type {
  CatalogueArea,
  DataRuleDetailPage,
  DataRuleListPage,
  DataRuleSummary,
} from '../contracts/features.ts';
import type { DataRulesLimits } from '../contracts/formats.ts';
import { AsyncJobs, type AsyncJobStatusEnvelope } from '../core/async-jobs.ts';
import { columnName } from '../core/cell-address.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireEl,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';

export interface DataRulesOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly supported: () => boolean;
  readonly navigate: (
    area: Pick<CatalogueArea, 'sheet' | 'start' | 'end'>,
    current: () => boolean,
  ) => Promise<boolean>;
  readonly limits: DataRulesLimits;
  readonly canNavigate?: () => boolean;
  readonly sleep?: (ms: number) => Promise<void>;
}

interface DataRulesJobStatus extends AsyncJobStatusEnvelope<DataRulesJobStatus> {
  readonly workbookKey?: string;
  readonly book?: string;
  readonly sheet?: string;
}

interface DataRuleItemsEnvelope {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly ref: string;
  readonly summary: DataRuleSummary;
  readonly page: DataRuleListPage;
}

interface DataRuleDetailEnvelope {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly detail: DataRuleDetailPage;
}

interface DataRuleResolveEnvelope {
  readonly workbookKey: string;
  readonly book: string;
  readonly sheet: string;
  readonly area: CatalogueArea;
}

const RULE_TYPES: Readonly<Record<string, string>> = {
  none: '任意值',
  whole: '整数',
  decimal: '小数',
  list: '列表',
  date: '日期',
  time: '时间',
  textLength: '文本长度',
  custom: '自定义公式',
};

const RULE_OPERATORS: Readonly<Record<string, string>> = {
  between: '介于',
  notBetween: '不介于',
  equal: '等于',
  notEqual: '不等于',
  greaterThan: '大于',
  lessThan: '小于',
  greaterThanOrEqual: '大于或等于',
  lessThanOrEqual: '小于或等于',
};

const RULE_STATES: Readonly<Record<string, string>> = {
  described: '已读取定义',
  unresolved: '未完整解释',
  invalid: '定义需核对',
};

const ERROR_STYLES: Readonly<Record<string, string>> = {
  stop: '停止',
  warning: '警告',
  information: '信息',
};

function labelFrom(
  dictionary: Readonly<Record<string, string>>,
  value: string,
  fallback = value,
): string {
  return Object.hasOwn(dictionary, value) ? (dictionary[value] ?? fallback) : fallback;
}

function isNonNegativeInt(n: unknown): n is number {
  return typeof n === 'number' && Number.isSafeInteger(n) && n >= 0;
}

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  n.className = cls;
  n.textContent = text;
  return n;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export class DataRules {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly supported: () => boolean;
  private readonly navigateArea: DataRulesOptions['navigate'];
  private readonly limits: DataRulesLimits;
  private readonly canNavigate: () => boolean;
  private readonly jobs: AsyncJobs<DataRulesJobStatus, DataRulesJobStatus>;

  public readonly dialog: HTMLDialogElement;
  private epoch = 0;
  private request = 0;
  private pageRequest = 0;
  private detailRequest = 0;
  private nav = 0;
  private navigating = false;
  public job = '';
  private sheet = '';
  private ref = '';
  private boundSource = '';
  public page: DataRuleItemsEnvelope | null = null;
  public detail: DataRuleDetailPage | null = null;
  private selected = 0;
  public busy = false;
  private loading = false;
  private pageController: AbortController | null = null;
  private detailController: AbortController | null = null;

  constructor(options: DataRulesOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.supported = options.supported;
    this.navigateArea = options.navigate;
    this.limits = options.limits;
    this.canNavigate = options.canNavigate ?? (() => true);

    if (!Number.isSafeInteger(this.limits.pageRows) || this.limits.pageRows < 1) {
      throw new Error('数据规则分页容量无效');
    }

    const sleep = options.sleep ?? ((ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
    this.dialog = this.create();
    this.jobs = new AsyncJobs<DataRulesJobStatus, DataRulesJobStatus>({
      api: this.api,
      identity: () => this.identity(),
      requestIdentity: () => this.s.workbookKey,
      sleep,
      endpoint: '/api/data-rules',
      label: '数据规则',
      maxPolls: 0,
      resultFromStatus: (status) => status,
      onProgress: (status) => {
        this.validSource(status);
        const done = status.doneBytes ?? 0;
        const total = status.totalBytes ?? 0;
        this.status(
          (status.stage || '正在读取数据规则') +
            (total > 0 ? ` · ${Math.min(100, Math.round((done / total) * 100))}%` : ''),
        );
      },
    });

    requireButton('dataRulesOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('dataRuleClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.stop();
    });
    requireButton('dataRuleRefresh').addEventListener('click', () => {
      void this.refresh();
    });
    requireButton('dataRuleCancel').addEventListener('click', () => this.cancel());
    for (const id of ['dataRuleScope', 'dataRuleType', 'dataRuleState']) {
      requireSelect(id).addEventListener('change', () => {
        void this.loadPage(0);
      });
    }
    requireButton('dataRulePrev').addEventListener('click', () => {
      void this.loadPage(this.page?.page.previous ?? 0);
    });
    requireButton('dataRuleNext').addEventListener('click', () => {
      void this.loadPage(this.page?.page.next ?? 0);
    });
    requireEl('dataRuleList').addEventListener('keydown', (event: KeyboardEvent) =>
      this.key(event),
    );
    document.addEventListener('pointerdown', () => this.cancelNavigation(), true);
    document.addEventListener('keydown', () => this.cancelNavigation(), true);
    this.controls();
  }

  private create(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog data-rules-dialog');
    d.id = 'dataRuleDialog';
    d.setAttribute('aria-labelledby', 'dataRuleTitle');
    // Workbook content is inserted exclusively as text, never as markup.
    d.innerHTML =
      '<div class="dialog-head"><div id="dataRuleTitle" class="dialog-title">数据规则</div><button id="dataRuleClose" class="dialog-close" type="button" aria-label="关闭数据规则">×</button></div><p id="dataRuleSource" class="data-rule-source"></p><div class="data-rule-tools"><select id="dataRuleScope" class="field-control" aria-label="规则范围"><option value="cell">当前单元格</option><option value="sheet">本表全部规则</option></select><select id="dataRuleType" class="field-control" aria-label="规则类型"><option value="">全部类型</option></select><select id="dataRuleState" class="field-control" aria-label="规则定义状态"><option value="">全部状态</option><option value="issues">需核对的定义</option><option value="described">已读取的定义</option></select><button id="dataRuleRefresh" class="mini-button" type="button">重新读取</button></div><p id="dataRuleSummary" class="data-rule-summary" role="status"></p><div class="data-rule-main"><div class="data-rule-master"><div id="dataRuleList" class="data-rule-list" role="listbox" aria-label="保存的数据规则" tabindex="0"></div><div class="data-rule-paging"><button id="dataRulePrev" class="mini-button" type="button">上一页</button><span id="dataRuleCount"></span><button id="dataRuleNext" class="mini-button" type="button">下一页</button></div></div><section id="dataRuleDetail" class="data-rule-detail" aria-label="规则定义详情"></section></div><div class="data-rule-footer"><p id="dataRuleStatus" role="status" aria-live="polite"></p><button id="dataRuleCancel" class="mini-button" type="button" hidden>取消读取</button><span class="data-rule-readonly">仅查看保存的规则，未校验数据</span></div>';
    document.body.append(d);
    const typeSelect = requireSelect('dataRuleType');
    for (const [value, text] of Object.entries(RULE_TYPES)) {
      typeSelect.append(new Option(text, value));
    }
    return d;
  }

  private identity(): string {
    return JSON.stringify([
      this.s.workbookKey,
      this.s.book,
      this.s.sheet,
      this.s.sheetEpoch,
      this.epoch,
    ]);
  }

  private validSource(value: {
    readonly workbookKey?: string;
    readonly book?: string;
    readonly sheet?: string;
  }): void {
    if (
      value.workbookKey !== this.s.workbookKey ||
      value.book !== this.s.book ||
      value.sheet !== this.sheet ||
      this.sheet !== this.s.sheet
    ) {
      throw new Error('数据规则来源已切换，请重新读取');
    }
  }

  private release(id: string): void {
    if (!id) return;
    this.api('/api/data-rules/clear', {
      method: 'POST',
      body: JSON.stringify({ id }),
    }).catch(() => {
      // Best-effort cleanup of abandoned data-rules job.
    });
  }

  private status(text: string, error = false): void {
    const el = requireEl('dataRuleStatus');
    el.textContent = text;
    el.classList.toggle('is-error', error);
    el.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    const enabled =
      this.s.opened &&
      Boolean(this.s.sheet) &&
      this.s.indexReady &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      this.supported();
    const pending = this.busy || this.loading;
    requireButton('dataRulesOpenBtn').disabled = !enabled;
    const menuBtn = byId('menuDataRules');
    if (menuBtn instanceof HTMLButtonElement) menuBtn.disabled = !enabled;
    requireButton('dataRuleRefresh').disabled = !enabled || this.busy;
    requireButton('dataRuleCancel').hidden = !pending;
    for (const id of ['dataRuleScope', 'dataRuleType', 'dataRuleState']) {
      requireSelect(id).disabled = pending;
    }
    requireButton('dataRulePrev').disabled = pending || !this.page || this.page.page.offset === 0;
    requireButton('dataRuleNext').disabled =
      pending || !this.page || this.page.page.next >= this.page.page.matched;
    for (const button of requireEl('dataRuleList').querySelectorAll<HTMLButtonElement>(
      'button[data-id]',
    )) {
      button.disabled = pending;
    }
    const locate = byId('dataRuleLocate');
    if (locate instanceof HTMLButtonElement) {
      locate.disabled = pending || !this.detail?.item.ranges.length || !this.canNavigate();
    }
  }

  async open(): Promise<void> {
    if (
      !this.s.opened ||
      !this.supported() ||
      !this.s.sheet ||
      !this.s.indexReady ||
      this.s.opening ||
      this.s.sheetLoadingEpoch
    ) {
      this.toast('请先打开并完成支持数据规则的工作表加载', 'warning');
      return;
    }
    const source = JSON.stringify([this.s.workbookKey, this.s.book, this.s.sheet]);
    if (this.boundSource && this.boundSource !== source) this.resetBook();
    this.boundSource = source;
    this.sheet = this.s.sheet;
    this.ref = this.s.selected?.ref || 'A1';
    requireEl('dataRuleSource').textContent = `${this.s.fileName || '当前工作簿'} / ${this.sheet}`;
    const scopeSelect = requireSelect('dataRuleScope');
    const firstOption = scopeSelect.options[0];
    if (firstOption) firstOption.textContent = `当前单元格 ${this.ref}`;
    if (!this.dialog.open) this.dialog.showModal();
    this.controls();
    if (this.job && !this.busy) await this.loadPage(0);
    else if (!this.busy) await this.refresh();
    if (this.dialog.open) scopeSelect.focus({ preventScroll: true });
  }

  private stop(): void {
    ++this.epoch;
    ++this.request;
    ++this.pageRequest;
    ++this.detailRequest;
    this.jobs.reset();
    this.pageController?.abort();
    this.detailController?.abort();
    this.busy = false;
    this.loading = false;
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
    this.sheet = '';
    this.ref = '';
    this.boundSource = '';
    this.page = null;
    this.detail = null;
    this.selected = 0;
    requireEl('dataRuleList').replaceChildren();
    requireEl('dataRuleDetail').replaceChildren();
    requireEl('dataRuleSummary').textContent = '';
    requireSelect('dataRuleScope').value = 'cell';
    requireSelect('dataRuleType').value = '';
    requireSelect('dataRuleState').value = '';
    this.status('');
    if (this.dialog.open) this.dialog.close();
    this.controls();
  }

  cancel(): void {
    this.stop();
    this.status('已取消读取，原规则结果保留');
  }

  async refresh(): Promise<void> {
    if (this.busy) return;
    const request = ++this.request;
    const identity = this.identity();
    this.pageController?.abort();
    this.detailController?.abort();
    ++this.pageRequest;
    ++this.detailRequest;
    this.busy = true;
    this.loading = false;
    this.controls();
    this.status('正在读取保存的数据规则…');
    const current = (): boolean =>
      request === this.request && identity === this.identity() && this.dialog.open;
    let owned = '';
    let adopted = false;
    try {
      const done = await this.jobs.execute(
        { workbookKey: this.s.workbookKey, book: this.s.book, sheet: this.sheet },
        { main: true },
      );
      owned = done.job;
      if (!current() || !done.current()) return;
      const controller = new AbortController();
      this.pageController = controller;
      const page = await this.readPage(done.job, 0, controller.signal);
      if (!current() || controller.signal.aborted) return;
      const old = this.job;
      this.job = done.job;
      adopted = true;
      this.detail = null;
      this.selected = 0;
      this.adoptPage(page);
      this.release(old);
      const first = page.page.items[0];
      if (first) await this.select(first.id);
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

  private async readPage(
    job: string,
    offset: number,
    signal: AbortSignal,
  ): Promise<DataRuleItemsEnvelope> {
    const ref = requireSelect('dataRuleScope').value === 'cell' ? this.ref : '';
    const q = new URLSearchParams({
      id: job,
      offset: String(offset),
      ref,
      type: requireSelect('dataRuleType').value,
      status: requireSelect('dataRuleState').value,
    });
    const value = await this.api<DataRuleItemsEnvelope>(`/api/data-rules/items?${q.toString()}`, {
      signal,
    });
    this.validSource(value);
    const p = value.page;
    if (
      p.items.length > this.limits.pageRows ||
      ![p.offset, p.previous, p.next, p.matched, p.total].every(isNonNegativeInt) ||
      p.offset !== offset ||
      p.previous !== Math.max(0, offset - this.limits.pageRows) ||
      p.next !== offset + p.items.length ||
      p.next > p.matched ||
      p.matched > p.total ||
      offset > p.matched ||
      typeof p.scopeComplete !== 'boolean' ||
      value.ref !== ref ||
      !isNonNegativeInt(value.summary.rules)
    ) {
      throw new Error('数据规则分页响应无效');
    }
    const ids = new Set<number>();
    for (const item of p.items) {
      if (
        !Number.isSafeInteger(item.id) ||
        item.id < 1 ||
        item.id > p.total ||
        ids.has(item.id) ||
        !isNonNegativeInt(item.rangeCount) ||
        !isNonNegativeInt(item.listItems) ||
        !Object.hasOwn(RULE_STATES, item.status)
      ) {
        throw new Error('数据规则列表条目无效');
      }
      ids.add(item.id);
    }
    return value;
  }

  private isDialogOpen(): boolean {
    return this.dialog.open;
  }

  async loadPage(offset: number): Promise<void> {
    if (!this.job || !this.dialog.open || this.busy) return;
    const serial = ++this.pageRequest;
    const identity = this.identity();
    const job = this.job;
    this.pageController?.abort();
    this.detailController?.abort();
    ++this.detailRequest;
    const controller = new AbortController();
    this.pageController = controller;
    this.loading = true;
    this.detail = null;
    this.selected = 0;
    this.controls();
    try {
      const value = await this.readPage(job, offset, controller.signal);
      if (
        serial !== this.pageRequest ||
        identity !== this.identity() ||
        job !== this.job ||
        !this.isDialogOpen()
      ) {
        return;
      }
      this.adoptPage(value);
      const first = value.page.items[0];
      if (first) await this.select(first.id);
    } catch (error: unknown) {
      if (serial === this.pageRequest && identity === this.identity() && !isAbortError(error)) {
        this.status(`${errorMessage(error)}；列表未更新，请重试`, true);
        requireEl('dataRuleDetail').textContent = '规则列表读取失败，请重试';
      }
    } finally {
      if (serial === this.pageRequest && identity === this.identity()) {
        this.loading = false;
        this.controls();
      }
    }
  }

  private adoptPage(value: DataRuleItemsEnvelope): void {
    this.page = value;
    this.renderList();
    const summary = value.summary;
    requireEl('dataRuleSummary').textContent =
      `本表 ${summary.rules} 条规则` +
      (summary.unresolved ? `，${summary.unresolved} 条定义需核对` : '') +
      (!summary.promptSettingsKnown
        ? '；不同规则容器的提示开关不一致'
        : summary.disablePrompts
          ? '；本表全局关闭输入提示'
          : '') +
      (summary.inputWindowClosed ? '；源提示窗口已关闭' : '');
    this.status(
      (value.page.scopeComplete ? '' : '存在未完整读取或范围未知的规则。') +
        (summary.notices ?? []).join('；'),
    );
    requireEl('dataRuleDetail').textContent = '选择规则查看保存的条件、提示与列表项';
    this.controls();
  }

  private renderList(): void {
    if (!this.page) return;
    const list = requireEl('dataRuleList');
    const p = this.page.page;
    list.replaceChildren();
    if (!p.items.length) {
      const message = !p.scopeComplete
        ? '未找到已识别的规则；仍有未读取或范围未知的定义。'
        : p.total
          ? '没有符合条件的规则。'
          : this.page.ref
            ? `${this.ref} 没有保存的数据规则。`
            : '本表没有保存的数据规则。';
      list.append(makeEl('p', 'data-rule-empty', message));
    }
    for (const item of p.items) {
      const button = makeEl('button', 'data-rule-item');
      button.type = 'button';
      button.id = `dataRuleItem${item.id}`;
      button.dataset.id = String(item.id);
      button.tabIndex = -1;
      button.setAttribute('role', 'option');
      button.setAttribute('aria-selected', String(this.selected === item.id));
      button.append(
        makeEl(
          'span',
          'data-rule-item-title',
          `#${item.id} ${labelFrom(RULE_TYPES, item.type, `未知类型：${item.type}`)}`,
        ),
        makeEl('span', 'data-rule-item-range', item.sqref || '适用范围未知'),
        makeEl('span', 'data-rule-item-state', labelFrom(RULE_STATES, item.status)),
      );
      button.addEventListener('click', () => {
        void this.select(item.id, true);
      });
      list.append(button);
    }
    if (this.selected) list.setAttribute('aria-activedescendant', `dataRuleItem${this.selected}`);
    else list.removeAttribute('aria-activedescendant');
    requireEl('dataRuleCount').textContent = p.matched
      ? `${p.offset + 1}–${p.next} / ${p.matched}`
      : '0 条';
  }

  async select(
    id: number,
    focus = false,
    rangeOffset = 0,
    listOffset = 0,
    focusID = '',
  ): Promise<void> {
    if (!this.page?.page.items.some((item) => item.id === id)) return;
    const serial = ++this.detailRequest;
    const identity = this.identity();
    const job = this.job;
    this.detailController?.abort();
    const controller = new AbortController();
    this.detailController = controller;
    this.selected = id;
    this.detail = null;
    this.renderList();
    if (focus) requireEl('dataRuleList').focus({ preventScroll: true });
    requireEl('dataRuleDetail').textContent = '正在读取规则定义…';
    this.controls();
    try {
      const value = await this.api<DataRuleDetailEnvelope>(
        `/api/data-rules/detail?${new URLSearchParams({
          id: job,
          item: String(id),
          rangeOffset: String(rangeOffset),
          listOffset: String(listOffset),
        }).toString()}`,
        { signal: controller.signal },
      );
      if (
        serial !== this.detailRequest ||
        identity !== this.identity() ||
        job !== this.job ||
        !this.dialog.open
      ) {
        return;
      }
      this.validSource(value);
      const d = value.detail;
      const ranges = d.item.ranges;
      const items = d.item.inlineList ?? [];
      if (
        d.item.id !== id ||
        ![
          d.rangeCount,
          d.rangeOffset,
          d.rangePrevious,
          d.rangeNext,
          d.listCount,
          d.listOffset,
          d.listPrevious,
          d.listNext,
        ].every(isNonNegativeInt) ||
        d.rangeOffset !== rangeOffset ||
        d.listOffset !== listOffset ||
        d.rangeNext !== rangeOffset + ranges.length ||
        d.listNext !== listOffset + items.length ||
        ranges.length > this.limits.pageRows ||
        items.length > this.limits.pageRows ||
        d.rangeNext > d.rangeCount ||
        d.listNext > d.listCount ||
        d.rangePrevious !== Math.max(0, rangeOffset - this.limits.pageRows) ||
        d.listPrevious !== Math.max(0, listOffset - this.limits.pageRows)
      ) {
        throw new Error('数据规则详情响应无效');
      }
      this.detail = d;
      this.renderDetail();
      this.controls();
      if (focusID) byId(focusID)?.focus({ preventScroll: true });
    } catch (error: unknown) {
      if (serial === this.detailRequest && identity === this.identity() && !isAbortError(error)) {
        this.status(errorMessage(error), true);
        requireEl('dataRuleDetail').textContent = '定义未读取完成，请重新选择或重试';
      }
    }
  }

  private field(body: HTMLElement, label: string, text: string, pre = false): void {
    body.append(
      makeEl('h4', '', label),
      makeEl(pre ? 'pre' : 'p', pre ? 'data-rule-definition' : '', text),
    );
  }

  private pager(
    body: HTMLElement,
    kind: 'Range' | 'List',
    offset: number,
    next: number,
    total: number,
  ): void {
    if (total <= this.limits.pageRows || !this.detail) return;
    const row = makeEl('div', 'data-rule-paging');
    const prev = makeEl('button', 'mini-button', '上一页');
    const more = makeEl('button', 'mini-button', '下一页');
    prev.type = 'button';
    more.type = 'button';
    prev.id = `dataRule${kind}Prev`;
    more.id = `dataRule${kind}Next`;
    prev.disabled = offset === 0;
    more.disabled = next >= total;
    const go = (at: number, focusID: string): void => {
      if (!this.detail) return;
      void this.select(
        this.selected,
        false,
        kind === 'Range' ? at : this.detail.rangeOffset,
        kind === 'List' ? at : this.detail.listOffset,
        focusID,
      );
    };
    prev.addEventListener('click', () => go(Math.max(0, offset - this.limits.pageRows), prev.id));
    more.addEventListener('click', () => go(next, more.id));
    row.append(prev, makeEl('span', '', `${offset + 1}–${next} / ${total}`), more);
    body.append(row);
  }

  private renderDetail(): void {
    const d = this.detail;
    if (!d) return;
    const item = d.item;
    const body = requireEl('dataRuleDetail');
    body.replaceChildren(
      makeEl('h3', '', `#${item.id} ${labelFrom(RULE_TYPES, item.type)}`),
      makeEl('p', 'data-rule-definition-state', labelFrom(RULE_STATES, item.status)),
    );
    if (item.reason) body.append(makeEl('p', 'data-rule-reason', item.reason));
    if (item.type === 'list' && item.listSource === 'inline') {
      body.append(makeEl('h4', '', `保存的列表项（${d.listCount}）`));
      const list = makeEl('ol', 'data-rule-values');
      list.id = 'dataRuleValues';
      list.start = d.listOffset + 1;
      for (const text of item.inlineList ?? []) {
        const n = makeEl('li', '', text === '' ? '（空白项）' : text);
        n.dataset.empty = String(text === '');
        list.append(n);
      }
      body.append(list);
      this.pager(body, 'List', d.listOffset, d.listNext, d.listCount);
    }
    if (item.type !== 'none' && item.type !== 'list' && item.type !== 'custom') {
      this.field(body, '条件', labelFrom(RULE_OPERATORS, item.operator));
    }
    if (
      item.formula1 !== undefined &&
      item.formula1 !== '' &&
      !(item.type === 'list' && item.listSource === 'inline')
    ) {
      this.field(body, '条件定义 1', item.formula1, true);
    }
    if (item.formula2 !== undefined && item.formula2 !== '') {
      this.field(body, '条件定义 2', item.formula2, true);
    }
    const flag = (value: boolean): string => (item.status === 'invalid' ? '未确认' : value ? '是' : '否');
    this.field(
      body,
      '保存的设置',
      `允许空白：${flag(item.allowBlank)}` +
        (item.type === 'list' ? `；隐藏下拉箭头：${flag(item.suppressDropDown)}` : '') +
        `；显示输入提示：${flag(item.showInputMessage)}；显示错误提示：${flag(item.showErrorMessage)}`,
    );
    if (item.promptTitle || item.prompt) {
      this.field(
        body,
        `输入提示${item.promptTitle ? ` · ${item.promptTitle}` : ''}`,
        item.prompt || '（空白）',
      );
    }
    if (item.errorTitle || item.error || item.showErrorMessage) {
      this.field(
        body,
        `错误提示 · ${labelFrom(ERROR_STYLES, item.errorStyle)}${item.errorTitle ? ` · ${item.errorTitle}` : ''}`,
        item.error || '（未保存提示文字）',
      );
    }
    body.append(makeEl('h4', '', `适用源区域（${d.rangeCount}）`));
    const ranges = makeEl('div', 'data-rule-areas');
    const select = makeEl('select', 'field-control');
    select.id = 'dataRuleArea';
    select.setAttribute('aria-label', '选择数据规则源区域');
    for (const [at, area] of item.ranges.entries()) {
      select.append(new Option(this.areaText(area), String(d.rangeOffset + at)));
    }
    const go = makeEl('button', 'primary-button compact', '定位区域');
    go.type = 'button';
    go.id = 'dataRuleLocate';
    go.addEventListener('click', () => {
      void this.locate();
    });
    select.disabled = !item.ranges.length;
    ranges.append(select, go);
    body.append(ranges);
    if (!d.rangeCount) body.append(makeEl('p', 'data-rule-reason', '源范围未确认，不能定位。'));
    this.pager(body, 'Range', d.rangeOffset, d.rangeNext, d.rangeCount);

    if (item.type === 'list' && item.listSource !== 'inline') {
      body.append(makeEl('h4', '', `保存的列表项（${d.listCount}）`));
      body.append(makeEl('p', 'data-rule-reason', '列表来自引用或未确认的定义，尚未读取候选值。'));
    }

    const raw = makeEl('details', 'data-rule-raw');
    raw.append(makeEl('summary', '', '原始定义'));
    if (item.type === 'list' && item.listSource === 'inline' && item.formula1) {
      this.field(raw, '列表源表达式', item.formula1, true);
    }
    const rawEntries: readonly (readonly [string, string | undefined])[] = [
      ['源区域文字', item.sqref],
      ['源命名空间', item.namespace],
      ['原始 XML', item.rawXml],
      ['BIFF 记录', item.recordHex],
      ['条件 1 原始令牌', item.formula1Tokens],
      ['条件 2 原始令牌', item.formula2Tokens],
      ['扩展列表定义', item.listDefinition],
    ];
    for (const [label, text] of rawEntries) {
      if (text) this.field(raw, label, text, true);
    }
    if (item.attributes) this.field(raw, '原始属性', JSON.stringify(item.attributes, null, 2), true);
    for (const [at, text] of (item.continuationHex ?? []).entries()) {
      this.field(raw, `续记录 ${at + 1}`, text, true);
    }
    body.append(raw);
  }

  private areaText(area: MergeRange): string {
    const first = `${columnName(area.col)}${area.row}`;
    const last = `${columnName(area.endCol)}${area.endRow}`;
    return first === last ? first : `${first}:${last}`;
  }

  private key(event: KeyboardEvent): void {
    const items = this.page?.page.items;
    if (!items?.length || isComposing(event)) return;
    let at = Math.max(
      0,
      items.findIndex((item) => item.id === this.selected),
    );
    if (
      event.key === 'ArrowDown' ||
      event.key === 'ArrowUp' ||
      event.key === 'Home' ||
      event.key === 'End'
    ) {
      event.preventDefault();
      at =
        event.key === 'Home'
          ? 0
          : event.key === 'End'
            ? items.length - 1
            : Math.max(0, Math.min(items.length - 1, at + (event.key === 'ArrowDown' ? 1 : -1)));
      const target = items[at];
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

  async locate(): Promise<void> {
    if (
      this.busy ||
      this.loading ||
      !this.detail?.item.ranges.length ||
      this.detail.item.id !== this.selected ||
      !this.canNavigate()
    ) {
      return;
    }
    const areaSelect = byId('dataRuleArea');
    if (!(areaSelect instanceof HTMLSelectElement)) return;
    const area = Number(areaSelect.value);
    if (
      !Number.isSafeInteger(area) ||
      area < this.detail.rangeOffset ||
      area >= this.detail.rangeNext
    ) {
      return;
    }
    const nav = ++this.nav;
    const identity = this.identity();
    const book = this.s.book;
    const key = this.s.workbookKey;
    const sheet = this.sheet;
    const origin = JSON.stringify([this.s.sheetEpoch, this.s.selected?.ref]);
    this.navigating = true;
    const resolving = (): boolean =>
      nav === this.nav && identity === this.identity() && this.dialog.open;
    const arriving = (): boolean =>
      nav === this.nav &&
      book === this.s.book &&
      key === this.s.workbookKey &&
      sheet === this.s.sheet;
    try {
      const value = await this.api<DataRuleResolveEnvelope>(
        `/api/data-rules/resolve?${new URLSearchParams({
          id: this.job,
          item: String(this.selected),
          area: String(area),
          workbookKey: key,
          book,
        }).toString()}`,
      );
      if (!resolving() || origin !== JSON.stringify([this.s.sheetEpoch, this.s.selected?.ref])) {
        return;
      }
      this.validSource(value);
      this.close();
      if ((await this.navigateArea(value.area, arriving)) && arriving()) {
        this.toast(
          `已定位：${value.area.sheet}!${value.area.start}${value.area.end === value.area.start ? '' : `:${value.area.end}`}`,
          'success',
        );
      }
    } catch (error: unknown) {
      if (arriving()) {
        if (this.dialog.open) this.status(errorMessage(error), true);
        else this.toast(errorMessage(error), 'warning');
      }
    } finally {
      if (nav === this.nav) this.navigating = false;
    }
  }
}
