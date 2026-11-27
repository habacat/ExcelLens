/**
 * Single-record vertical card dialog with bounded field pagination,
 * full-text chunk viewer, and source cell navigation.
 */
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import type { RecordCardResponse } from '../contracts/features.ts';
import type { RecordCardLimits, SourceCapabilities } from '../contracts/formats.ts';
import type { IndexStartResponse, IndexStatusResponse } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import type { WorkbookState } from '../grid/state.ts';
import {
  previewText,
  recordCellFormula,
  recordCellType,
  recordCellValue,
  recordTextOffsets,
  validateRecordCard,
  type RecordCardRequest,
  type RecordCardValueMode,
} from './record-card-model.ts';

export interface RecordCardOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly copy: (text: string) => Promise<boolean | undefined>;
  readonly navigate: (sheet: string, ref: string, guard: () => boolean) => Promise<unknown>;
  readonly colName: (col: number) => string;
  readonly parseColumn: (value: string) => number;
  readonly pinnedThroughRow: () => number;
  readonly source: () => SourceCapabilities;
  readonly limits: RecordCardLimits;
  readonly canRead?: () => boolean;
}

interface RecordCardBinding {
  readonly request: {
    readonly workbookKey: string;
    readonly book: string;
    readonly sheet: string;
    readonly viewId: string;
    readonly filterId: string;
    readonly pinnedThroughRow: number;
  };
  readonly index: IndexStartResponse | IndexStatusResponse | null;
  readonly key: string;
  readonly totalColumns: number;
}

interface RecordCardDetailState {
  readonly text: string;
  readonly offsets: readonly number[];
  index: number;
  readonly page: RecordCardResponse;
  readonly bound: RecordCardBinding;
}

type RecordCardField = RecordCardResponse['fields'][number];

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

function requireForm(id: string): HTMLFormElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFormElement)) {
    throw new Error(`Expected HTMLFormElement for #${id}`);
  }
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export class RecordCard {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly copy: (text: string) => Promise<boolean | undefined>;
  private readonly navigate: (sheet: string, ref: string, guard: () => boolean) => Promise<unknown>;
  private readonly colName: (col: number) => string;
  private readonly parseColumn: (value: string) => number;
  private readonly pinnedThroughRow: () => number;
  private readonly source: () => SourceCapabilities;
  private readonly limits: RecordCardLimits;
  private readonly canRead: () => boolean;

  public readonly dialog: HTMLDialogElement;
  public readonly detailDialog: HTMLDialogElement;

  private serial = 0;
  private nav = 0;
  private navigating = false;
  private busy = false;
  private mode: RecordCardValueMode = 'display';
  private bound: RecordCardBinding | null = null;
  private page: RecordCardResponse | null = null;
  private accepted: RecordCardRequest | null = null;
  private detail: RecordCardDetailState | null = null;
  private abort: AbortController | null = null;

  constructor(options: RecordCardOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.copy = options.copy;
    this.navigate = options.navigate;
    this.colName = options.colName;
    this.parseColumn = options.parseColumn;
    this.pinnedThroughRow = options.pinnedThroughRow;
    this.source = options.source;
    this.limits = options.limits;
    this.canRead = options.canRead ?? (() => true);

    if (
      !Number.isSafeInteger(this.limits.columns) ||
      this.limits.columns < 1 ||
      !Number.isSafeInteger(this.limits.textUnits) ||
      this.limits.textUnits < 2 ||
      !this.limits.bytes ||
      !this.limits.requestBytes
    ) {
      throw new Error('记录卡容量配置不可用');
    }

    this.dialog = this.create();
    this.detailDialog = this.createDetail();

    requireButton('recordCardOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('recordCardClose').addEventListener('click', () => this.close());
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.clear();
    });
    requireForm('recordCardForm').addEventListener('submit', (event: SubmitEvent) => {
      event.preventDefault();
      void this.apply();
    });
    requireButton('recordCardCancel').addEventListener('click', () => this.cancel());
    requireButton('recordCardPrevious').addEventListener('click', () => {
      void this.record(-1);
    });
    requireButton('recordCardNext').addEventListener('click', () => {
      void this.record(1);
    });
    requireButton('recordFieldPrevious').addEventListener('click', () => {
      void this.fields(-1);
    });
    requireButton('recordFieldNext').addEventListener('click', () => {
      void this.fields(1);
    });
    requireSelect('recordCardMode').addEventListener('change', () => {
      this.mode = requireSelect('recordCardMode').value === 'raw' ? 'raw' : 'display';
      this.closeDetail();
      if (this.page) this.render();
    });
    requireButton('recordTextClose').addEventListener('click', () => this.closeDetail());
    this.detailDialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.closeDetail();
    });
    this.detailDialog.addEventListener('close', () => {
      if (!this.detailDialog.open) {
        this.detail = null;
        requireEl('recordTextValue').textContent = '';
      }
    });
    requireButton('recordTextPrevious').addEventListener('click', () => this.textPart(-1));
    requireButton('recordTextNext').addEventListener('click', () => this.textPart(1));
    requireButton('recordTextCopy').addEventListener('click', () => {
      void this.copyDetail();
    });
    this.dialog.addEventListener('keydown', (event: KeyboardEvent) => {
      if (
        (event.target instanceof Element && event.target.closest('input,select,textarea')) ||
        event.ctrlKey ||
        event.altKey ||
        event.metaKey
      ) {
        return;
      }
      if (event.key === 'PageUp' || event.key === 'PageDown') {
        event.preventDefault();
        void this.record(event.key === 'PageUp' ? -1 : 1);
      }
    });
    document.addEventListener('pointerdown', () => this.cancelNavigation(), true);
    document.addEventListener('keydown', () => this.cancelNavigation(), true);
    window.addEventListener('resize', () => this.controls());
    this.controls();
  }

  private create(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog record-card-dialog');
    d.id = 'recordCardDialog';
    d.setAttribute('aria-labelledby', 'recordCardTitle');
    // All source strings use textContent; this template is static.
    d.innerHTML = `<div class="dialog-head"><div id="recordCardTitle" class="dialog-title">记录卡片</div><button id="recordCardClose" class="dialog-close" type="button" aria-label="关闭记录卡片">×</button></div>
        <p id="recordCardSource" class="record-card-source"></p><div class="record-card-tools"><form id="recordCardForm"><label>表头行<input id="recordHeaderRow" class="field-control" type="number" min="0" value="1" title="0 表示使用列字母"></label><label>起始列<input id="recordStartColumn" class="field-control" value="A" autocomplete="off" aria-label="起始列字母或列号"></label><label>每页字段<select id="recordPageSize" class="field-control"></select></label><button id="recordCardApply" class="mini-button" type="submit">读取字段</button></form><label>内容<select id="recordCardMode" class="field-control"><option value="display">显示值</option><option value="raw">原值</option></select></label></div>
        <div class="record-card-position"><strong id="recordCardPosition"></strong><span id="recordCardHeader"></span></div><div id="recordCardFields" class="record-card-fields" tabindex="0" aria-label="当前记录字段"></div><p id="recordCardNotices" class="record-card-notices" hidden></p>
        <div class="record-card-paging"><div><button id="recordCardPrevious" class="mini-button" type="button">上一条</button><button id="recordCardNext" class="mini-button" type="button">下一条</button></div><div><button id="recordFieldPrevious" class="mini-button" type="button">上一页字段</button><span id="recordFieldCount"></span><button id="recordFieldNext" class="mini-button" type="button">下一页字段</button></div></div><div class="record-card-footer"><p id="recordCardStatus" role="status" aria-live="polite"></p><button id="recordCardCancel" class="mini-button" type="button" hidden>取消读取</button><span>PageUp / PageDown</span></div>`;
    document.body.append(d);
    const sizes = [
      ...new Set([
        this.limits.columns,
        Math.max(1, Math.floor(this.limits.columns / 2)),
        Math.max(1, Math.floor(this.limits.columns / 4)),
        1,
      ]),
    ];
    const pageSize = requireSelect('recordPageSize');
    for (const size of sizes) pageSize.append(new Option(String(size), String(size)));
    pageSize.value = String(this.limits.columns);
    return d;
  }

  private createDetail(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog record-text-dialog');
    d.id = 'recordTextDialog';
    d.setAttribute('aria-labelledby', 'recordTextTitle');
    d.innerHTML =
      '<div class="dialog-head"><div id="recordTextTitle" class="dialog-title">完整内容</div><button id="recordTextClose" class="dialog-close" type="button" aria-label="关闭完整内容">×</button></div><p id="recordTextSource" class="record-card-source"></p><pre id="recordTextValue" class="record-text-value" tabindex="0"></pre><div class="record-card-paging"><button id="recordTextPrevious" class="mini-button" type="button">上一段</button><span id="recordTextPosition"></span><button id="recordTextNext" class="mini-button" type="button">下一段</button><button id="recordTextCopy" class="mini-button" type="button">复制完整内容</button></div><p id="recordTextStatus" role="status" aria-live="polite"></p>';
    document.body.append(d);
    return d;
  }

  private binding(): RecordCardBinding {
    const req = {
      workbookKey: this.s.workbookKey,
      book: this.s.book,
      sheet: this.s.sheet ?? '',
      viewId: this.s.rowView?.id ?? '',
      filterId: this.s.filtered ? this.s.filterId : '',
      pinnedThroughRow: this.pinnedThroughRow(),
    };
    return {
      request: req,
      index: this.s.index,
      key: JSON.stringify([req, this.s.sheetEpoch, this.s.opened, this.s.indexReady]),
      totalColumns: this.s.index?.maxCol ?? 0,
    };
  }

  private sameSource(bound: RecordCardBinding | null): boolean {
    const now = this.binding();
    return bound !== null && now.key === bound.key && now.index === bound.index;
  }

  enabled(): boolean {
    const maxRow = this.s.index?.maxRow ?? 0;
    const maxCol = this.s.index?.maxCol ?? 0;
    const totalRows = this.s.index?.totalRows ?? 0;
    return (
      this.s.opened &&
      this.s.indexReady &&
      !this.s.opening &&
      !this.s.sheetLoadingEpoch &&
      !this.s.rowViewFailure &&
      Boolean(this.s.rowView?.id) &&
      Boolean(this.s.book) &&
      Boolean(this.s.sheet) &&
      this.s.selected !== null &&
      this.s.selected.row >= 1 &&
      this.s.selected.row <= maxRow &&
      maxCol > 0 &&
      totalRows > 0 &&
      this.canRead()
    );
  }

  private status(text: string, error = false): void {
    const node = requireEl('recordCardStatus');
    node.textContent = text;
    node.classList.toggle('is-error', error);
    node.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    if (this.bound && !this.sameSource(this.bound)) {
      this.resetBook();
      return;
    }
    const enabled = this.enabled();
    const ready = enabled && this.page !== null && !this.busy;
    requireButton('recordCardOpenBtn').disabled = !enabled;
    const menuBtn = byId('menuRecordCard');
    if (menuBtn instanceof HTMLButtonElement) menuBtn.disabled = !enabled;
    requireButton('recordCardApply').disabled = !enabled || this.busy;
    requireButton('recordCardCancel').hidden = !this.busy;
    const fieldsHost = requireEl('recordCardFields');
    fieldsHost.setAttribute('aria-busy', String(this.busy));
    requireInput('recordHeaderRow').disabled = this.busy;
    requireInput('recordStartColumn').disabled = this.busy;
    requireSelect('recordPageSize').disabled = this.busy;
    requireButton('recordCardPrevious').disabled = !ready || !this.page?.previousRow;
    requireButton('recordCardNext').disabled = !ready || !this.page?.nextRow;
    requireButton('recordFieldPrevious').disabled = !ready || (this.page?.firstColumn ?? 1) <= 1;
    requireButton('recordFieldNext').disabled =
      !ready ||
      (this.page ? this.page.firstColumn + this.page.columns > this.page.totalColumns : true);
    for (const node of fieldsHost.querySelectorAll('button')) {
      node.disabled = !ready || node.dataset.unavailable === 'true';
    }
  }

  async open(): Promise<void> {
    this.controls();
    if (!this.enabled() || !this.s.selected) {
      this.toast('请先在已完成加载的工作表中选择一条可见记录', 'warning');
      return;
    }
    if (this.dialog.open) return;
    this.cancelNavigation();
    this.clear();
    this.bound = this.binding();
    this.mode = 'display';
    requireSelect('recordCardMode').value = 'display';
    const header = this.s.autoFilter?.row || 1;
    const columns = this.limits.columns;
    const column = Math.max(1, Math.min(this.bound.totalColumns, this.s.selected.col || 1));
    const headerInput = requireInput('recordHeaderRow');
    headerInput.value = String(header);
    headerInput.max = String(this.source().maxRows);
    requireSelect('recordPageSize').value = String(columns);
    requireInput('recordStartColumn').value = this.colName(
      Math.floor((column - 1) / columns) * columns + 1,
    );
    requireEl('recordCardSource').textContent = `${this.s.fileName || '当前工作簿'} / ${this.s.sheet ?? ''}`;
    requireEl('recordCardFields').textContent = '正在读取记录…';
    this.dialog.showModal();
    await this.apply(this.s.selected.row);
    if (this.isDialogOpen()) requireEl('recordCardFields').focus({ preventScroll: true });
  }

  private isDialogOpen(): boolean {
    return this.dialog.open;
  }

  private stop(): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
    this.busy = false;
  }

  private clear(): void {
    this.stop();
    this.closeDetail();
    this.bound = null;
    this.page = null;
    this.accepted = null;
    requireEl('recordCardFields').replaceChildren();
    for (const id of [
      'recordCardPosition',
      'recordCardHeader',
      'recordFieldCount',
      'recordCardNotices',
    ]) {
      requireEl(id).textContent = '';
    }
    requireEl('recordCardNotices').hidden = true;
    this.status('');
  }

  close(preserveNavigation = false): void {
    if (!preserveNavigation) this.cancelNavigation();
    this.clear();
    if (this.dialog.open) this.dialog.close();
    this.controls();
  }

  resetBook(): void {
    this.close();
  }

  cancel(): void {
    this.stop();
    if (!this.page) requireEl('recordCardFields').textContent = '读取已取消，可重新读取字段。';
    this.status(this.page ? '已取消读取，保留上一次完整记录' : '已取消读取');
    this.controls();
  }

  async apply(row = this.page?.row ?? this.s.selected?.row ?? 0): Promise<void> {
    if (!this.dialog.open || this.busy || !row) return;
    const headerText = requireInput('recordHeaderRow').value.trim();
    const headerRow = Number(headerText);
    const columnText = requireInput('recordStartColumn').value.trim();
    const firstColumn = this.parseColumn(columnText);
    const columns = Number(requireSelect('recordPageSize').value);
    const maxCol = this.s.index?.maxCol ?? 0;
    if (
      !/^\d+$/.test(headerText) ||
      !Number.isSafeInteger(headerRow) ||
      headerRow < 0 ||
      headerRow > this.source().maxRows ||
      !columnText ||
      !Number.isSafeInteger(firstColumn) ||
      firstColumn < 1 ||
      firstColumn > maxCol ||
      !Number.isSafeInteger(columns) ||
      columns < 1 ||
      columns > this.limits.columns
    ) {
      this.status('请输入有效字段行和已用范围内的起始列；字段行 0 表示列字母', true);
      return;
    }
    await this.load({ row, headerRow, firstColumn, columns });
  }

  private async load(spec: {
    readonly row: number;
    readonly headerRow: number;
    readonly firstColumn: number;
    readonly columns: number;
  }): Promise<void> {
    this.controls();
    if (!this.enabled() || !this.dialog.open || !this.bound || !this.sameSource(this.bound)) return;
    this.stop();
    const serial = this.serial;
    const bound = this.bound;
    const request: RecordCardRequest = { ...bound.request, ...spec };
    const controller = new AbortController();
    this.abort = controller;
    this.busy = true;
    this.closeDetail();
    this.controls();
    this.status('正在读取记录…');
    const current = (): boolean =>
      serial === this.serial &&
      !controller.signal.aborted &&
      this.dialog.open &&
      this.sameSource(bound);
    try {
      const body = JSON.stringify(request);
      if (new TextEncoder().encode(body).length > this.limits.requestBytes) {
        throw new Error('记录卡请求超过容量');
      }
      const rawPage = await this.api('/api/record', {
        method: 'POST',
        body,
        signal: controller.signal,
      });
      if (!current()) return;
      const page = validateRecordCard(
        rawPage,
        request,
        this.limits,
        this.source(),
        this.colName,
        bound.totalColumns,
      );
      const fields = this.build(page);
      if (!current()) return;
      this.page = page;
      this.accepted = request;
      this.adopt(fields);
      this.status('');
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        if (!this.page) {
          requireEl('recordCardFields').textContent = '未能读取记录，请调整设置后重试。';
        }
        this.status(`${this.page ? '保留上一次完整记录。' : ''}${errorMessage(error)}`, true);
      }
    } finally {
      if (serial === this.serial) {
        this.abort = null;
        this.busy = false;
        this.controls();
      }
    }
  }

  private record(direction: -1 | 1): Promise<void> | undefined {
    if (!this.page || !this.accepted || this.busy || !this.enabled()) return undefined;
    const row = direction < 0 ? this.page.previousRow : this.page.nextRow;
    if (!row) return undefined;
    return this.load({
      row,
      headerRow: this.accepted.headerRow,
      firstColumn: this.accepted.firstColumn,
      columns: this.accepted.columns,
    });
  }

  private fields(direction: -1 | 1): Promise<void> | undefined {
    if (!this.page || !this.accepted || this.busy || !this.enabled()) return undefined;
    const firstColumn =
      direction < 0
        ? Math.max(1, this.page.firstColumn - this.accepted.columns)
        : this.page.firstColumn + this.page.columns;
    if (firstColumn < 1 || firstColumn > this.page.totalColumns) return undefined;
    return this.load({
      row: this.accepted.row,
      headerRow: this.accepted.headerRow,
      firstColumn,
      columns: this.accepted.columns,
    });
  }

  private build(page: RecordCardResponse): DocumentFragment {
    const fragment = document.createDocumentFragment();
    for (const field of page.fields) {
      const card = makeEl('article', 'record-field');
      card.dataset.column = String(field.column);
      card.dataset.ref = field.cell.ref;
      const name = makeEl('div', 'record-field-name');
      const column = this.colName(field.column);
      const header =
        page.headerRow && field.header ? recordCellValue(field.header, 'display') : null;
      const label =
        header === null
          ? page.headerRow
            ? `列 ${column}（字段名无保存结果）`
            : `列 ${column}`
          : header === ''
            ? `列 ${column}（空白字段名）`
            : previewText(header, this.limits.textUnits);
      name.append(
        makeEl('span', 'record-field-label', label),
        makeEl('small', 'record-field-column', `源列 ${column}`),
      );
      if (header !== null && header.length > this.limits.textUnits) {
        name.append(
          this.button('查看完整字段名', () => this.full(field, 'header'), 'record-header-full'),
        );
      }

      const content = makeEl('div', 'record-field-content');
      const text = recordCellValue(field.cell, this.mode);
      const value = makeEl(
        'pre',
        'record-field-value',
        text === null
          ? '公式未保存计算结果'
          : text === ''
            ? '（空白）'
            : previewText(text, this.limits.textUnits),
      );
      content.append(value);
      if (text !== null && text.length > this.limits.textUnits) {
        content.append(
          makeEl('span', 'record-field-preview-note', '当前显示首段；查看全文可逐段核对。'),
        );
      }

      const meta = makeEl('div', 'record-field-meta');
      meta.append(
        makeEl(
          'span',
          'record-field-type',
          recordCellType(field.cell) + (field.cell.hasFormula ? ' · 公式' : ''),
        ),
      );
      const formulaText = recordCellFormula(field.cell);
      if (field.cell.hasFormula && !formulaText) {
        meta.append(makeEl('span', 'record-field-notice', '公式无法还原'));
      }
      if (field.cell.conditional?.notice) {
        meta.append(
          makeEl(
            'span',
            'record-field-notice',
            previewText(field.cell.conditional.notice, this.limits.textUnits),
          ),
        );
      }
      if (field.cell.richText?.notice) {
        meta.append(
          makeEl(
            'span',
            'record-field-notice',
            previewText(field.cell.richText.notice, this.limits.textUnits),
          ),
        );
      }
      content.append(meta);

      const actions = makeEl('div', 'record-field-actions');
      actions.append(
        this.button(
          `定位 ${field.cell.ref}`,
          () => {
            void this.locate(field.column);
          },
          'record-field-locate',
        ),
      );
      const copyBtn = this.button(
        `复制${this.mode === 'raw' ? '原值' : '显示值'}`,
        () => {
          void this.copyField(field);
        },
        'record-field-copy',
      );
      copyBtn.disabled = text === null;
      copyBtn.dataset.unavailable = text === null ? 'true' : '';
      actions.append(copyBtn);

      const fullBtn = this.button(
        '查看全文',
        () => this.full(field, 'value'),
        'record-field-full',
      );
      fullBtn.disabled = text === null;
      fullBtn.dataset.unavailable = text === null ? 'true' : '';
      actions.append(fullBtn);

      if (formulaText) {
        actions.append(
          this.button('公式文字', () => this.full(field, 'formula'), 'record-field-formula'),
        );
      }
      content.append(actions);
      card.append(name, content);
      fragment.append(card);
    }
    return fragment;
  }

  private button(text: string, run: () => void, cls = ''): HTMLButtonElement {
    const b = makeEl('button', `mini-button ${cls}`, text);
    b.type = 'button';
    b.addEventListener('click', run);
    return b;
  }

  private adopt(fields: DocumentFragment): void {
    const page = this.page;
    const accepted = this.accepted;
    if (!page || !accepted) return;
    const host = requireEl('recordCardFields');
    host.replaceChildren(fields);
    host.scrollTop = 0;
    requireEl('recordCardPosition').textContent =
      `第 ${page.position.toLocaleString()} / ${page.totalRecords.toLocaleString()} 条 · 源行 ${page.row}`;
    requireEl('recordCardHeader').textContent = page.headerRow
      ? `字段行 ${page.headerRow}`
      : '字段名使用列字母';
    requireEl('recordFieldCount').textContent =
      `${this.colName(page.firstColumn)}–${this.colName(page.firstColumn + page.columns - 1)} / ${page.totalColumns.toLocaleString()} 列`;
    requireInput('recordHeaderRow').value = String(page.headerRow);
    requireInput('recordStartColumn').value = this.colName(page.firstColumn);
    requireSelect('recordPageSize').value = String(accepted.columns);
    const notices = requireEl('recordCardNotices');
    notices.textContent = page.notices
      .map((text) => previewText(text, this.limits.textUnits))
      .join('；');
    notices.hidden = !page.notices.length;
  }

  private render(): void {
    if (!this.page || !this.sameSource(this.bound)) return;
    this.adopt(this.build(this.page));
    this.controls();
  }

  private usable(): boolean {
    this.controls();
    return this.page !== null && this.sameSource(this.bound) && this.enabled() && !this.busy;
  }

  private async copyField(field: RecordCardField): Promise<void> {
    if (!this.usable() || !this.page?.fields.includes(field)) return;
    const text = recordCellValue(field.cell, this.mode);
    if (text === null) return;
    const page = this.page;
    const bound = this.bound;
    const mode = this.mode;
    try {
      const ok = await this.copy(text);
      if (this.page === page && this.sameSource(bound) && this.mode === mode) {
        this.status(
          ok === true ? `已复制完整${mode === 'raw' ? '原值' : '显示值'}` : '复制失败，请重试',
          ok !== true,
        );
      }
    } catch (error: unknown) {
      if (this.page === page && this.sameSource(bound) && this.mode === mode) {
        this.status(`复制失败：${errorMessage(error)}`, true);
      }
    }
  }

  private full(field: RecordCardField, kind: 'header' | 'formula' | 'value'): void {
    if (!this.usable() || !this.page || !this.bound || !this.page.fields.includes(field)) return;
    const text =
      kind === 'header'
        ? field.header
          ? recordCellValue(field.header, 'display')
          : null
        : kind === 'formula'
          ? recordCellFormula(field.cell)
          : recordCellValue(field.cell, this.mode);
    if (text === null) return;
    this.closeDetail();
    this.detail = {
      text,
      offsets: recordTextOffsets(text, this.limits.textUnits),
      index: 0,
      page: this.page,
      bound: this.bound,
    };
    requireEl('recordTextTitle').textContent =
      kind === 'header'
        ? '完整字段名'
        : kind === 'formula'
          ? '公式文字'
          : `完整${this.mode === 'raw' ? '原值' : '显示值'}`;
    const ref = kind === 'header' ? (field.header?.ref ?? field.cell.ref) : field.cell.ref;
    requireEl('recordTextSource').textContent = `${this.s.sheet ?? ''}!${ref}`;
    this.renderText();
    this.detailDialog.showModal();
    requireEl('recordTextValue').focus({ preventScroll: true });
  }

  private renderText(): void {
    const d = this.detail;
    if (!d) return;
    const start = d.offsets[d.index] ?? 0;
    const end = d.offsets[d.index + 1] ?? start;
    requireEl('recordTextValue').textContent = d.text.slice(start, end);
    requireEl('recordTextPosition').textContent = `第 ${d.index + 1} / ${d.offsets.length - 1} 段`;
    requireButton('recordTextPrevious').disabled = d.index === 0;
    requireButton('recordTextNext').disabled = d.index + 2 === d.offsets.length;
    requireEl('recordTextStatus').textContent = '';
  }

  private textPart(direction: -1 | 1): void {
    if (!this.detail || !this.usable()) return;
    const d = this.detail;
    const index = d.index + direction;
    if (index >= 0 && index < d.offsets.length - 1) {
      d.index = index;
      this.renderText();
    }
  }

  closeDetail(): void {
    this.detail = null;
    if (this.detailDialog.open) this.detailDialog.close();
    const val = byId('recordTextValue');
    if (val) val.textContent = '';
  }

  private async copyDetail(): Promise<void> {
    const d = this.detail;
    if (!d || !this.usable() || this.page !== d.page || !this.sameSource(d.bound)) return;
    try {
      const ok = await this.copy(d.text);
      if (this.detail === d) {
        const status = requireEl('recordTextStatus');
        status.textContent = ok === true ? '已复制完整内容' : '复制失败，请重试';
        status.setAttribute('role', ok === true ? 'status' : 'alert');
      }
    } catch (error: unknown) {
      if (this.detail === d) {
        const status = requireEl('recordTextStatus');
        status.textContent = `复制失败：${errorMessage(error)}`;
        status.setAttribute('role', 'alert');
      }
    }
  }

  private cancelNavigation(): void {
    if (this.navigating) {
      ++this.nav;
      this.navigating = false;
    }
  }

  private async locate(column: number): Promise<void> {
    if (!this.usable() || !this.page || !this.bound) return;
    const field = this.page.fields.find((f) => f.column === column);
    if (!field) return;
    const bound = this.bound;
    const ref = field.cell.ref;
    const nav = ++this.nav;
    this.navigating = true;
    this.close(true);
    const current = (): boolean => nav === this.nav && this.sameSource(bound) && this.canRead();
    try {
      if (current()) await this.navigate(bound.request.sheet, ref, current);
    } catch (error: unknown) {
      if (current()) this.toast(errorMessage(error), 'error');
    } finally {
      if (nav === this.nav) this.navigating = false;
    }
  }
}
