/**
 * Two-table Relation & Extraction dialog: left/inner/unmatched/invalid joins,
 * bounded field pickers, preview paging, source navigation, and export handoff.
 */
import type { CompositionSourceInfo } from '../contracts/analysis.ts';
import type { ApiClient, ApiRequestOptions, ToastFn } from '../contracts/common.ts';
import type {
  ReconcileBookChoice,
  RelationCounts,
  RelationLimits,
  RelationQuery,
  RelationResultResponse,
} from '../contracts/features.ts';
import type {
  CompositionLimits,
  FileFormatsRegistry,
  RelationExportOption,
} from '../contracts/formats.ts';
import { labelColumn, suggestFieldMatches } from '../core/composition-model.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { FieldPicker } from '../core/field-picker.ts';
import { SourceTasks, type SourceTaskContext } from '../core/source-tasks.ts';
import { cellDisplayValue } from '../grid/cell-view.ts';
import { previewText } from './record-card-model.ts';
import {
  copyValue,
  formatRelationCount,
  parseRelationCount,
  RELATION_MODES,
  relationCell,
  relationColumns,
  relationFieldRef,
  relationMode,
  validateRelationFields,
  validateRelationLimits,
  validateRelationQuery,
  validateRelationResult,
  type RelationOutputModeId,
} from './relations-model.ts';
import {
  decodeCompositionNavigationResponse,
  decodeRelationSourcesResponse,
  decodeRelationUploadResponse,
  decodeSourceTaskResultEnvelope,
} from '../core/http-decoders.generated.ts';

export interface RelationContext extends SourceTaskContext {
  readonly source: string;
}

export interface RelationExportPayload {
  readonly jobId: string;
  readonly mode: string;
  readonly maximum: string;
  readonly protectCSV: boolean;
  readonly format?: string;
}

export interface RelationsOptions {
  readonly api: ApiClient;
  readonly context: () => RelationContext;
  readonly formats: FileFormatsRegistry;
  readonly fieldLimits: Pick<CompositionLimits, 'pickerRows'>;
  readonly onExport: (payload: RelationExportPayload, name: string) => unknown;
  readonly navigate: (sheet: string, ref: string) => Promise<unknown>;
  readonly navigateAnalysis?: (sheet: string, row: number, col: number) => Promise<void> | void;
  readonly closeAnalysis?: () => void;
  readonly toast: ToastFn;
  readonly canRun?: () => boolean;
}

interface RelationInputSlot {
  book: string;
  source: string;
  headerRow: number;
  info: CompositionSourceInfo | null;
  columns: number[];
}

interface RelationKeyDraft {
  left: number | null;
  right: number | null;
  mode: string;
  trim: boolean;
  ignoreCase: boolean;
}

interface AcceptedRelation {
  readonly data: RelationResultResponse;
  readonly infos: readonly [CompositionSourceInfo, CompositionSourceInfo];
}

interface BuiltTableView {
  readonly fragment: DocumentFragment;
  readonly all: number;
  readonly columnPage: number;
  readonly rowPage: number;
}

interface BuiltResultView {
  readonly counts: DocumentFragment;
  readonly options: DocumentFragment;
  readonly source: string;
  readonly total: string;
  readonly table: BuiltTableView;
}

/** @httpResponse RelationSourcesResponse */
export interface RelationSourcesResponse {
  readonly currentBook: string;
  readonly books: readonly ReconcileBookChoice[];
}

/** @httpResponse RelationUploadResponse */
export interface RelationUploadResponse {
  readonly book: string;
}

function makeNode<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text?: string,
  cls?: string,
): HTMLElementTagNameMap[K] {
  const value = document.createElement(tag);
  if (text !== undefined) value.textContent = text;
  if (cls) value.className = cls;
  return value;
}

function makeButton(text: string, action: () => void, cls = 'mini-button'): HTMLButtonElement {
  const value = makeNode('button', text, cls);
  value.type = 'button';
  value.onclick = action;
  return value;
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isRelationOutputMode(value: string): value is RelationOutputModeId {
  return relationMode(value) !== undefined;
}

const COUNT_DISPLAY_ITEMS: readonly (readonly [keyof RelationCounts, string])[] = [
  ['leftMatched', '左匹配'],
  ['rightMatched', '右匹配'],
  ['leftOnly', '左未匹配'],
  ['rightOnly', '右未匹配'],
  ['leftInvalid', '左无效键'],
  ['rightInvalid', '右无效键'],
];

export class Relations {
  private readonly api: ApiClient;
  private readonly context: () => RelationContext;
  private readonly formats: FileFormatsRegistry;
  private readonly onExport: RelationsOptions['onExport'];
  private readonly navigate: RelationsOptions['navigate'];
  private readonly navigateAnalysis?: RelationsOptions['navigateAnalysis'];
  private readonly closeAnalysis?: RelationsOptions['closeAnalysis'];
  private readonly toast: ToastFn;
  private readonly canRun: () => boolean;

  public readonly fieldsTasks: SourceTasks;
  public readonly tasks: SourceTasks;
  public readonly dialog: HTMLDialogElement;
  public readonly picker: FieldPicker;
  public readonly observer: ResizeObserver;

  public exportOptions: readonly RelationExportOption[];
  private exportFormatTouched = false;
  public serial = 0;
  private navSerial = 0;
  private books: ReconcileBookChoice[] = [];
  public inputs: RelationInputSlot[] = [];
  public keys: RelationKeyDraft[] = [];
  private rowPage = 0;
  private columnPage = 0;
  private visibleColumns = 1;
  public accepted: AcceptedRelation | null = null;
  public limits: RelationLimits | null = null;
  public busy = false;
  private controller: AbortController | null = null;
  public identityKey = '';

  constructor(options: RelationsOptions) {
    this.api = options.api;
    this.context = options.context;
    this.formats = options.formats;
    this.onExport = options.onExport;
    this.navigate = options.navigate;
    this.navigateAnalysis = options.navigateAnalysis;
    this.closeAnalysis = options.closeAnalysis;
    this.toast = options.toast;
    this.canRun = options.canRun ?? (() => true);

    this.fieldsTasks = new SourceTasks({ api: this.api, context: this.context });
    this.tasks = new SourceTasks({
      api: this.api,
      context: this.context,
      endpoint: '/api/relation',
      label: '关联',
    });

    this.dialog = makeNode('dialog', undefined, 'float-dialog relations-dialog');
    this.dialog.id = 'relationsDialog';
    this.dialog.setAttribute('aria-labelledby', 'relationsTitle');
    this.dialog.innerHTML = `<form method="dialog" class="dialog-head"><div id="relationsTitle" class="dialog-title">关联与提取</div><button class="dialog-close" value="cancel" aria-label="关闭关联与提取">×</button></form>
        <div class="relations-body"><fieldset id="relationsConfig"><div class="relations-toolbar"><button id="relationsRefresh" class="mini-button" type="button">刷新来源</button><button id="relationsUpload" class="mini-button" type="button">添加文件</button><input id="relationsFile" type="file" hidden><button id="relationsSwap" class="mini-button" type="button">交换左右表</button><button id="relationsRead" class="mini-button" type="button">读取字段</button></div>
        <div id="relationsSources" class="relations-sources"></div><div class="relations-key-heading"><strong>关联键</strong><button id="relationsAddKey" class="mini-button" type="button">添加键</button></div><div id="relationsKeys"></div></fieldset>
        <section id="relationsResults" hidden><div class="relations-result-heading"><strong>结果</strong><span id="relationsPrevious" class="relations-previous" hidden>上次结果</span><span id="relationsResultSources"></span><button id="relationsClear" type="button" class="mini-button">清除</button></div>
        <div id="relationsCounts" class="relations-counts"></div><div class="relations-results-tools"><label>提取<select id="relationsMode" class="field-control"></select></label><span id="relationsTotal"></span></div>
        <div id="relationsTableWrap" class="relations-table-wrap"><table id="relationsTable" class="relations-table"></table></div><div class="relations-paging"><div id="relationsRowPaging"></div><div id="relationsColumnPaging"></div></div>
        <div class="relations-export"><label>导出上限<input id="relationsMaximum" class="field-control" inputmode="numeric" aria-label="导出记录上限"></label><label>格式<select id="relationsFormat" class="field-control" aria-label="关联导出格式"></select></label><label><input id="relationsProtect" type="checkbox" checked>文本保护</label><button id="relationsExport" type="button" class="mini-button">导出结果</button></div></section></div>
        <div class="relations-footer"><div id="relationsStatus" role="status" aria-live="polite"></div><button id="relationsCancel" class="mini-button" type="button" hidden>取消任务</button><button id="relationsRun" class="primary-button compact" type="button">运行关联</button></div>`;
    document.body.append(this.dialog);

    this.picker = new FieldPicker({ id: 'relationsPicker', pageRows: options.fieldLimits.pickerRows });
    this.exportOptions = this.formats.relationExports;
    const formatSelect = requireSelect('relationsFormat');
    for (const option of this.exportOptions) {
      formatSelect.append(new Option(option.name, option.format));
    }
    formatSelect.value = this.exportOptions[0]?.format ?? '';
    formatSelect.addEventListener('change', () => {
      this.exportFormatTouched = true;
    });
    this.bind();

    this.observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? 0;
      const count = Math.max(
        1,
        Math.min(this.limits?.viewColumns ?? 1, Math.floor(Math.max(1, width - 126) / 150)),
      );
      if (count !== this.visibleColumns) {
        this.visibleColumns = count;
        this.columnPage = 0;
        if (this.accepted) this.renderTable();
      }
    });
    this.observer.observe(requireEl('relationsTableWrap'));
  }

  identity(): string {
    const c = this.context();
    return JSON.stringify([c.bookKey, c.book]);
  }

  private post(
    path: string,
    value: unknown,
    options: ApiRequestOptions = {},
  ): Promise<unknown> {
    return this.api(path, { method: 'POST', body: JSON.stringify(value), ...options });
  }

  private bind(): void {
    this.dialog.addEventListener('keydown', (event: KeyboardEvent) => event.stopPropagation());
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.cancel(false);
    });
    requireButton('relationsOpenBtn').addEventListener('click', () => {
      void this.open();
    });
    requireButton('relationsRefresh').onclick = () => {
      void this.action((current) => this.refresh(current));
    };
    requireButton('relationsRead').onclick = () => {
      void this.action(async (current) => {
        for (let i = 0; i < 2 && current(); i++) await this.read(i as 0 | 1, current);
      });
    };
    requireButton('relationsUpload').onclick = () => requireInput('relationsFile').click();
    const fileInput = requireInput('relationsFile');
    fileInput.accept = this.formats.raw.extensions.join(',');
    fileInput.onchange = () => {
      const file = fileInput.files?.[0];
      fileInput.value = '';
      if (file) void this.action((current) => this.upload(file, current));
    };
    requireButton('relationsSwap').onclick = () => this.swap();
    requireButton('relationsAddKey').onclick = () => {
      this.keys.push({ left: null, right: null, mode: '', trim: false, ignoreCase: false });
      this.changed();
      this.renderKeys();
    };
    requireButton('relationsRun').onclick = () => {
      void this.action((current) => this.run(current));
    };
    requireButton('relationsCancel').onclick = () => this.cancel(true);
    requireSelect('relationsMode').onchange = () => {
      const selected = requireSelect('relationsMode').value;
      if (isRelationOutputMode(selected)) {
        void this.action((current) => this.changeMode(selected, current));
      }
    };
    requireButton('relationsClear').onclick = () => {
      void this.clear();
    };
    requireButton('relationsExport').onclick = () => {
      void this.export();
    };
  }

  private message(text: string, error = false): void {
    const status = requireEl('relationsStatus');
    status.textContent = text;
    status.setAttribute('role', error ? 'alert' : 'status');
  }

  controls(): void {
    requireFieldSet('relationsConfig').disabled = this.busy;
    requireButton('relationsCancel').hidden = !this.busy;
    requireButton('relationsRun').disabled =
      this.busy || !this.inputs.length || this.inputs.some((input) => !input.info);
    requireButton('relationsAddKey').disabled =
      this.busy || !this.limits || this.keys.length >= this.limits.keys;
    requireSelect('relationsMode').disabled = this.busy || !this.accepted;
    requireButton('relationsClear').disabled = this.busy || !this.accepted;
    requireButton('relationsExport').disabled =
      this.busy ||
      !this.accepted ||
      !this.limits ||
      parseRelationCount(this.accepted.data.preview.total) > BigInt(this.limits.maximumRows);
  }

  async action(operation: (current: () => boolean) => Promise<void>): Promise<void> {
    if (this.busy) return;
    const ticket = ++this.serial;
    const identity = this.identity();
    const current = (): boolean =>
      ticket === this.serial &&
      identity === this.identity() &&
      this.context().opened &&
      this.dialog.open;
    this.busy = true;
    this.controls();
    try {
      await operation(current);
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) this.message(errorMessage(error), true);
    } finally {
      if (ticket === this.serial) {
        this.busy = false;
        this.controls();
      }
    }
  }

  cancel(report: boolean): void {
    const pending = this.busy;
    ++this.serial;
    ++this.navSerial;
    this.fieldsTasks.reset();
    this.tasks.reset();
    this.controller?.abort();
    this.controller = null;
    this.picker.close();
    this.busy = false;
    if (this.accepted) requireSelect('relationsMode').value = this.accepted.data.outputMode;
    if (report && pending) this.message('已取消');
    this.controls();
  }

  resetBook(): void {
    this.cancel(false);
    if (this.dialog.open) this.dialog.close();
    this.releaseAccepted();
    this.inputs = [];
    this.keys = [];
    this.books = [];
    this.identityKey = '';
    for (const id of [
      'relationsSources',
      'relationsKeys',
      'relationsTable',
      'relationsCounts',
      'relationsMode',
    ]) {
      requireEl(id).replaceChildren();
    }
    requireEl('relationsResults').hidden = true;
    this.message('');
  }

  private releaseAccepted(): void {
    if (this.accepted) {
      void this.tasks.clear(this.accepted.data.id);
      this.accepted = null;
    }
  }

  async open(): Promise<void> {
    if (!this.context().opened) {
      this.toast('请先打开数据文件', 'warning');
      return;
    }
    if (this.identityKey !== this.identity()) {
      this.resetBook();
      this.identityKey = this.identity();
    }
    this.cancel(false);
    if (!this.dialog.open) this.dialog.showModal();
    await this.action(async (current) => {
      if (!this.limits) {
        this.limits = validateRelationLimits(await this.api('/api/relation/limits'));
        if (!current()) return;
        requireInput('relationsMaximum').value = String(this.limits.defaultRows);
      }
      await this.refresh(current);
      if (!current()) return;
      if (!this.inputs.length) {
        const c = this.context();
        const other = this.books.find((book) => book.id !== c.book);
        const primary = this.books.find((book) => book.id === c.book);
        const second =
          other?.sheets?.[0] ?? primary?.sheets?.find((sheet) => `wb:${sheet.name}` !== c.source);
        this.inputs = [
          { book: c.book, source: c.source || '', headerRow: 1, info: null, columns: [] },
          {
            book: other?.id ?? c.book,
            source: second ? `wb:${second.name}` : '',
            headerRow: 1,
            info: null,
            columns: [],
          },
        ];
        this.renderSources();
        this.renderKeys();
      }
      this.message('');
    });
  }

  async refresh(current: () => boolean, focus = ''): Promise<void> {
    this.controller?.abort();
    const controller = new AbortController();
    this.controller = controller;
    try {
      const data = await this.api(
        `/api/relation/sources${focus ? `?book=${encodeURIComponent(focus)}` : ''}`,
        { signal: controller.signal },
        decodeRelationSourcesResponse,
      );
      if (!current()) return;
      if (data.currentBook !== this.context().book) {
        throw new Error('来源列表已变化，请刷新');
      }
      const previous = new Map(this.books.map((book) => [book.id, book]));
      this.books = data.books.map((book) => {
        const old = previous.get(book.id);
        return !book.scratchLoaded && old?.scratchLoaded
          ? { ...book, scratch: old.scratch, scratchLoaded: true }
          : book;
      });
      for (const input of this.inputs) {
        const book = this.books.find((item) => item.id === input.book);
        const exists = Boolean(
          book &&
            (book.sheets?.some((sheet) => `wb:${sheet.name}` === input.source) ||
              (book.scratch ?? []).some((sheet) => `scratch:${sheet.id}` === input.source) ||
              (!book.scratchLoaded && input.source.startsWith('scratch:'))),
        );
        if (!exists) {
          input.info = null;
          input.columns = [];
        }
      }
      this.renderSources();
    } finally {
      if (this.controller === controller) this.controller = null;
    }
  }

  private changed(): void {
    if (this.accepted) requireEl('relationsPrevious').hidden = false;
    this.message('');
    this.controls();
  }

  private invalidate(side: number): void {
    const input = this.inputs[side];
    if (!input) return;
    input.info = null;
    input.columns = [];
    for (const key of this.keys) {
      if (side) key.right = null;
      else key.left = null;
    }
    this.changed();
    this.renderSources();
    this.renderKeys();
  }

  private renderSources(): void {
    const host = requireEl('relationsSources');
    host.replaceChildren();
    this.inputs.forEach((input, side) => {
      const pane = makeNode('section', undefined, 'relations-source');
      const head = makeNode('div', undefined, 'relations-source-heading');
      const title = makeNode('strong', side ? '右表' : '左表');
      head.append(title);
      const meta = this.books.find((book) => book.id === input.book);
      if (meta?.attached) {
        head.append(
          makeButton('移除文件', () => {
            void this.action(async (current) => {
              await this.post('/api/reconcile/source/remove', { book: input.book });
              if (!current()) return;
              input.source = '';
              this.invalidate(side);
              await this.refresh(current);
            });
          }),
        );
      }

      const book = makeNode('select', undefined, 'field-control');
      book.setAttribute('aria-label', `${side ? '右' : '左'}表文件`);
      book.dataset.side = String(side);
      book.dataset.choice = 'book';
      for (const item of this.books) {
        const option = new Option(item.name, item.id);
        option.disabled = item.incomplete;
        book.append(option);
      }
      book.value = input.book;

      const sheet = makeNode('select', undefined, 'field-control');
      sheet.setAttribute('aria-label', `${side ? '右' : '左'}表工作表`);
      sheet.dataset.side = String(side);
      sheet.dataset.choice = 'source';
      sheet.append(new Option('选择工作表', ''));
      for (const item of meta?.sheets ?? []) {
        sheet.append(new Option(item.name, `wb:${item.name}`));
      }
      for (const item of meta?.scratch ?? []) {
        sheet.append(new Option(`${item.name}（分析表）`, `scratch:${item.id}`));
      }
      sheet.value = input.source;

      book.onchange = () => {
        input.book = book.value;
        input.source = '';
        input.headerRow = 1;
        this.invalidate(side);
        const chosen = this.books.find((item) => item.id === input.book);
        if (chosen && !chosen.scratchLoaded) {
          void this.action((current) => this.refresh(current, input.book));
        }
      };
      sheet.onchange = () => {
        input.source = sheet.value;
        input.headerRow = input.source.startsWith('scratch:') ? 0 : 1;
        this.invalidate(side);
      };

      const bottom = makeNode('div', undefined, 'relations-source-tools');
      const header = makeNode('label', '表头行');
      const number = makeNode('input', undefined, 'field-control');
      number.type = 'number';
      number.min = '0';
      number.max = String(this.formats.maxRows);
      number.value = String(input.headerRow);
      number.title = '0：无表头';
      number.disabled = input.source.startsWith('scratch:');
      number.dataset.side = String(side);
      number.dataset.choice = 'header';
      number.setAttribute('aria-label', `${side ? '右' : '左'}表表头行`);
      number.onchange = () => {
        input.headerRow = Number(number.value);
        this.invalidate(side);
      };
      header.append(number);

      const selected = makeButton(`结果字段 ${input.columns.length}`, () => this.pickColumns(side));
      selected.disabled = !input.info;
      selected.dataset.side = String(side);
      selected.dataset.choice = 'columns';
      const info = makeNode(
        'span',
        input.info
          ? `${formatRelationCount(input.info.rows)} 条 / ${(input.info.fields ?? []).length} 列`
          : '未读取字段',
        'relations-source-state',
      );
      bottom.append(header, selected, info);
      pane.append(head, book, sheet, bottom);
      host.append(pane);
    });
    this.controls();
  }

  async read(side: 0 | 1, current: () => boolean): Promise<void> {
    const input = this.inputs[side];
    if (!input?.book || !input.source) throw new Error('请选择两侧文件和工作表');
    if (
      !Number.isSafeInteger(input.headerRow) ||
      input.headerRow < 0 ||
      input.headerRow > this.formats.maxRows
    ) {
      throw new Error('表头行号无效');
    }
    const choice = { book: input.book, source: input.source, headerRow: input.headerRow };
    const identity = { ...this.context() };
    let result: { readonly id: string } | undefined;
    try {
      this.message(`正在读取${side ? '右' : '左'}表字段…`);
      result = await this.fieldsTasks.run(decodeSourceTaskResultEnvelope, { mode: 'fields', choice }, current);
      if (!current() || this.inputs[side] !== input) return;
      const info = validateRelationFields(result, choice, identity, this.formats);
      const fields = info.fields ?? [];
      const available = new Set(fields.map((field) => field.column));
      input.info = info;
      input.columns = input.columns.filter((column) => available.has(column));
      if (!input.columns.length && this.limits) {
        input.columns = fields.slice(0, this.limits.initialFields).map((field) => field.column);
      }
      const leftInfo = this.inputs[0]?.info;
      const rightInfo = this.inputs[1]?.info;
      if (leftInfo && rightInfo) {
        if (!this.keys.length) {
          this.keys = [{ left: null, right: null, mode: '', trim: false, ignoreCase: false }];
        }
        const leftFields = leftInfo.fields ?? [];
        const rightFields = rightInfo.fields ?? [];
        const suggestions = suggestFieldMatches(leftFields, rightFields);
        const first = suggestions.findIndex((column) => column !== null && column > 0);
        for (const key of this.keys) {
          if (!key.left && !key.right) {
            const leftField = first >= 0 ? leftFields[first] : undefined;
            const rightCol = first >= 0 ? suggestions[first] : null;
            if (leftField && rightCol !== null && rightCol !== undefined) {
              key.left = leftField.column;
              key.right = rightCol;
            }
          } else {
            if (!key.left && key.right) {
              const otherField = rightFields.find((field) => field.column === key.right);
              if (otherField) key.left = suggestFieldMatches([otherField], leftFields)[0] ?? null;
            }
            if (!key.right && key.left) {
              const otherField = leftFields.find((field) => field.column === key.left);
              if (otherField) key.right = suggestFieldMatches([otherField], rightFields)[0] ?? null;
            }
          }
        }
      }
      this.changed();
      this.renderSources();
      this.renderKeys();
    } finally {
      if (result) await this.fieldsTasks.clear(result.id);
    }
  }

  private pickColumns(side: number): void {
    const input = this.inputs[side];
    const info = input?.info;
    if (!input || !info || !this.limits) return;
    this.picker.open({
      title: `${side ? '右' : '左'}表结果字段`,
      fields: info.fields ?? [],
      selected: input.columns,
      multiple: true,
      limit: this.limits.columns,
      current: () => this.dialog.open && this.inputs[side] === input && input.info === info,
      onChoose: (columns) => {
        input.columns = columns;
        this.changed();
        this.renderSources();
      },
    });
  }

  private pickKey(side: 0 | 1, index: number): void {
    const input = this.inputs[side];
    const info = input?.info;
    const pair = this.keys[index];
    if (!input || !info || !pair) return;
    const disabled = this.keys
      .filter((_, i) => i !== index)
      .map((key) => (side ? key.right : key.left))
      .filter((col): col is number => col !== null);
    this.picker.open({
      title: `${side ? '右' : '左'}表关联键`,
      fields: info.fields ?? [],
      disabled,
      current: () => this.dialog.open && this.keys[index] === pair && input.info === info,
      onChoose: (column) => {
        if (side) pair.right = column;
        else pair.left = column;
        this.changed();
        this.renderKeys();
      },
    });
  }

  private renderKeys(): void {
    const host = requireEl('relationsKeys');
    host.replaceChildren();
    if (!this.keys.length) {
      host.append(makeNode('span', '读取字段后选择关联键', 'relations-source-state'));
      return;
    }
    this.keys.forEach((pair, index) => {
      const row = makeNode('div', undefined, 'relations-key');
      for (const side of [0, 1] as const) {
        const chosenCol = side ? pair.right : pair.left;
        const sideInfo = this.inputs[side]?.info;
        const field = sideInfo?.fields?.find((f) => f.column === chosenCol);
        const pick = makeButton(
          field ? `${labelColumn(field.column)} ${field.name}` : `${side ? '右' : '左'}表字段`,
          () => this.pickKey(side, index),
          'relations-field-button',
        );
        pick.disabled = !sideInfo;
        pick.setAttribute('aria-label', `${side ? '右' : '左'}表第${index + 1}个关联键`);
        row.append(pick);
      }

      const mode = makeNode('select', undefined, 'field-control');
      mode.setAttribute('aria-label', `第${index + 1}个键的匹配方式`);
      const modeChoices: readonly (readonly [string, string])[] = [
        ['', '保留源类型'],
        ['text', '按显示文本'],
        ['number', '按精确数值'],
      ];
      for (const [value, text] of modeChoices) mode.append(new Option(text, value));
      mode.value = pair.mode;
      mode.onchange = () => {
        pair.mode = mode.value;
        if (pair.mode === 'number') {
          pair.trim = false;
          pair.ignoreCase = false;
        }
        this.changed();
        this.renderKeys();
      };
      row.append(mode);

      const flags = makeNode('div', undefined, 'relations-key-flags');
      const flagSpecs: readonly (readonly ['trim' | 'ignoreCase', string])[] = [
        ['trim', '去空格'],
        ['ignoreCase', '忽略大小写'],
      ];
      for (const [key, text] of flagSpecs) {
        const wrap = makeNode('label');
        const check = makeNode('input');
        check.type = 'checkbox';
        check.checked = pair[key];
        check.disabled = pair.mode === 'number';
        check.onchange = () => {
          pair[key] = check.checked;
          this.changed();
        };
        wrap.append(check, document.createTextNode(text));
        flags.append(wrap);
      }
      row.append(flags);

      const remove = makeButton('×', () => {
        this.keys.splice(index, 1);
        this.changed();
        this.renderKeys();
      });
      remove.title = '移除关联键';
      remove.disabled = this.keys.length === 1;
      row.append(remove);
      host.append(row);
    });
    this.controls();
  }

  private query(): RelationQuery {
    const left = this.inputs[0];
    const right = this.inputs[1];
    if (!left?.info || !right?.info || !this.limits) {
      throw new Error('请先读取两侧字段');
    }
    return validateRelationQuery(
      {
        keys: copyValue(this.keys),
        leftColumns: [...left.columns],
        rightColumns: [...right.columns],
      },
      [left.info, right.info],
      this.limits,
    );
  }

  async run(current: () => boolean): Promise<void> {
    if (!this.canRun()) return;
    const left = this.inputs[0]?.info;
    const right = this.inputs[1]?.info;
    if (!left || !right) throw new Error('请先读取两侧字段');
    const query = this.query();
    const infos: readonly [CompositionSourceInfo, CompositionSourceInfo] = [
      copyValue(left),
      copyValue(right),
    ];
    const identity = { ...this.context() };
    await this.prepare(
      {
        mode: 'prepare',
        left: relationFieldRef(infos[0]),
        right: relationFieldRef(infos[1]),
        query,
        outputMode: 'left',
      },
      { query, infos, identity, mode: 'left', operation: 'prepare' },
      current,
    );
  }

  async changeMode(id: RelationOutputModeId, current: () => boolean): Promise<void> {
    const accepted = this.accepted;
    if (!accepted) return;
    await this.prepare(
      { mode: 'preview', parentId: accepted.data.id, outputMode: id },
      {
        query: accepted.data.query,
        infos: accepted.infos,
        identity: { ...this.context() },
        mode: id,
        operation: 'preview',
        parentId: accepted.data.id,
      },
      current,
    );
  }

  private async prepare(
    request: Record<string, unknown>,
    expected: {
      readonly query: RelationQuery;
      readonly infos: readonly [CompositionSourceInfo, CompositionSourceInfo];
      readonly identity: RelationContext;
      readonly mode: RelationOutputModeId;
      readonly operation: 'prepare' | 'preview';
      readonly parentId?: string;
    },
    current: () => boolean,
  ): Promise<void> {
    if (!this.limits) return;
    let response: { readonly id: string } | undefined;
    let published = false;
    try {
      this.message('正在关联…');
      response = await this.tasks.run(decodeSourceTaskResultEnvelope,
        { ...request, ...(this.accepted ? { keepId: this.accepted.data.id } : {}) },
        current,
      );
      if (!current()) return;
      const data = validateRelationResult(
        response,
        { ...expected, id: response.id },
        this.limits,
        this.formats,
      );
      const candidate: AcceptedRelation = { data, infos: copyValue(expected.infos) };
      const view = this.buildResult(candidate);
      if (!current()) return;
      const old = this.accepted;
      this.accepted = candidate;
      this.rowPage = 0;
      this.columnPage = 0;
      this.chooseExportFormat();
      this.renderResult(view);
      published = true;
      this.message('');
      if (old) await this.tasks.clear(old.data.id);
    } finally {
      if (response && !published) await this.tasks.clear(response.id);
      if (!published && current() && this.accepted) {
        requireSelect('relationsMode').value = this.accepted.data.outputMode;
      }
    }
  }

  buildResult(candidate: AcceptedRelation): BuiltResultView {
    const data = candidate.data;
    const counts = document.createDocumentFragment();
    const options = document.createDocumentFragment();
    for (const [key, text] of COUNT_DISPLAY_ITEMS) {
      const item = makeNode('span', text);
      const value = makeNode('b', formatRelationCount(data.counts[key]));
      item.append(value);
      counts.append(item);
    }
    if (parseRelationCount(data.counts.manyToManyGroups) > 0n) {
      counts.append(
        makeNode(
          'span',
          `多对多 ${formatRelationCount(data.counts.manyToManyGroups)} 组`,
          'relations-multiple',
        ),
      );
    }
    for (const item of RELATION_MODES) options.append(new Option(item.label, item.id));
    return {
      counts,
      options,
      source: `${data.left.name} ↔ ${data.right.name}`,
      total: `共 ${formatRelationCount(data.preview.total)} 条`,
      table: this.buildTable(candidate, 0, 0),
    };
  }

  renderResult(view: BuiltResultView): void {
    if (!this.accepted) return;
    requireEl('relationsResults').hidden = false;
    requireEl('relationsPrevious').hidden = true;
    requireEl('relationsResultSources').textContent = view.source;
    requireEl('relationsCounts').replaceChildren(view.counts);
    const modeSelect = requireSelect('relationsMode');
    modeSelect.replaceChildren(view.options);
    modeSelect.value = this.accepted.data.outputMode;
    requireEl('relationsTotal').textContent = view.total;
    this.commitTable(view.table);
    this.controls();
  }

  private buildTable(
    accepted: AcceptedRelation,
    rowPage: number,
    columnPage: number,
  ): BuiltTableView {
    const limits = this.limits;
    const data = accepted.data;
    const all = relationColumns(data);
    const width = Math.max(1, this.visibleColumns);
    const begin = columnPage * width;
    const columns = all.slice(begin, begin + width);
    const viewRows = limits?.viewRows ?? 1;
    const snippetUnits = limits?.snippetUnits ?? 120;
    const rows = data.preview.rows.slice(rowPage * viewRows, (rowPage + 1) * viewRows);
    const fragment = document.createDocumentFragment();
    const head = makeNode('thead');
    const hr = makeNode('tr');
    hr.append(makeNode('th', '源行'), makeNode('th', '状态'));
    for (const item of columns) {
      hr.append(makeNode('th', `${item.side ? '右 ' : '左 '}${labelColumn(item.column)} ${item.field.name}`));
    }
    head.append(hr);
    fragment.append(head);

    const body = makeNode('tbody');
    const sides = relationMode(data.outputMode)?.sides ?? [];
    const firstKey = data.query.keys[0];
    for (const row of rows) {
      const tr = makeNode('tr');
      const origin = makeNode('td', undefined, 'relations-origins');
      for (const side of sides) {
        const record = side ? row.right : row.left;
        if (record && firstKey) {
          const keyCol = side ? firstKey.right : firstKey.left;
          origin.append(
            makeButton(
              `${side ? '右 ' : '左 '}${record.number}`,
              () => {
                void this.goSource(accepted, side, record.number, keyCol);
              },
              'relations-source-link',
            ),
          );
        }
      }
      tr.append(origin);
      const state = makeNode(
        'td',
        row.status === 'matched' ? '已匹配' : row.status === 'invalid_key' ? '无效键' : '未匹配',
        'relations-row-state',
      );
      state.dataset.status = row.status;
      if (row.reason) state.title = previewText(row.reason, snippetUnits);
      tr.append(state);

      for (const item of columns) {
        const record = item.side ? row.right : row.left;
        const value = record?.values[String(item.column)];
        const cell = makeNode('td');
        if (record) {
          const raw = value?.missingCache
            ? '缺缓存'
            : value?.exists
              ? cellDisplayValue(relationCell(value), false, true)
              : '';
          const snippet = previewText(raw, snippetUnits);
          const link = makeButton(
            snippet + (snippet.length < raw.length || value?.truncated ? '…' : ''),
            () => {
              if (!window.getSelection()?.toString()) {
                void this.goSource(accepted, item.side, record.number, item.column);
              }
            },
            'relations-value',
          );
          link.title = `${item.side ? '右 ' : '左 '}${labelColumn(item.column)}${record.number}`;
          if (value?.typeMissing || value?.uncertain || value?.truncated) {
            link.classList.add('relations-value-notice');
          }
          cell.append(link);
        }
        tr.append(cell);
      }
      body.append(tr);
    }

    if (!rows.length) {
      const tr = makeNode('tr');
      const empty = makeNode(
        'td',
        parseRelationCount(data.preview.total) === 0n ? '没有此类记录' : '预览内容过大，请导出查看',
        'relations-empty',
      );
      empty.colSpan = columns.length + 2;
      tr.append(empty);
      body.append(tr);
    }
    fragment.append(body);
    return { fragment, all: all.length, columnPage, rowPage };
  }

  private commitTable(table: BuiltTableView): void {
    if (!this.accepted || !this.limits) return;
    requireEl('relationsTable').replaceChildren(table.fragment);
    const previewCount = this.accepted.data.preview.rows.length;
    this.paging(
      requireEl('relationsRowPaging'),
      previewCount,
      this.rowPage,
      this.limits.viewRows,
      (page) => {
        this.rowPage = page;
        this.renderTable();
      },
      `预览 ${previewCount} 条`,
    );
    this.paging(
      requireEl('relationsColumnPaging'),
      table.all,
      this.columnPage,
      this.visibleColumns,
      (page) => {
        this.columnPage = page;
        this.renderTable();
      },
      '字段',
    );
  }

  private renderTable(): void {
    if (!this.accepted || !this.limits) return;
    const all = relationColumns(this.accepted.data).length;
    this.columnPage = Math.min(this.columnPage, Math.max(0, Math.ceil(all / this.visibleColumns) - 1));
    this.rowPage = Math.min(
      this.rowPage,
      Math.max(0, Math.ceil(this.accepted.data.preview.rows.length / this.limits.viewRows) - 1),
    );
    this.commitTable(this.buildTable(this.accepted, this.rowPage, this.columnPage));
  }

  private paging(
    host: HTMLElement,
    count: number,
    page: number,
    size: number,
    change: (page: number) => void,
    text: string,
  ): void {
    host.replaceChildren();
    const info = makeNode('span', text + (count > size ? `　${page + 1} / ${Math.ceil(count / size)}` : ''));
    host.append(info);
    if (count <= size) return;
    const previous = makeButton('上一页', () => change(page - 1));
    const next = makeButton('下一页', () => change(page + 1));
    previous.disabled = page === 0;
    next.disabled = (page + 1) * size >= count;
    host.append(previous, next);
  }

  private swap(): void {
    if (this.busy) return;
    this.inputs.reverse();
    for (const key of this.keys) {
      const prevLeft = key.left;
      key.left = key.right;
      key.right = prevLeft;
    }
    this.changed();
    this.renderSources();
    this.renderKeys();
  }

  async upload(file: File, current: () => boolean): Promise<void> {
    const c = { ...this.context() };
    const form = new FormData();
    form.append('file', file, file.name);
    form.append('reconcileSource', '1');
    form.append('workbookKey', c.bookKey);
    form.append('primaryBook', c.book);
    const data = await this.api('/api/open', { method: 'POST', body: form }, decodeRelationUploadResponse);
    if (!current()) return;
    await this.refresh(current, data.book);
    if (!current()) return;
    const book = this.books.find((item) => item.id === data.book);
    const firstSheet = book?.sheets?.[0];
    if (!firstSheet) throw new Error('新文件没有可用工作表');
    this.inputs[1] = {
      book: data.book,
      source: `wb:${firstSheet.name}`,
      headerRow: 1,
      info: null,
      columns: [],
    };
    for (const key of this.keys) key.right = null;
    this.changed();
    this.renderSources();
    this.renderKeys();
  }

  async clear(): Promise<void> {
    if (this.busy || !this.accepted) return;
    const accepted = this.accepted;
    await this.action(async (current) => {
      await this.post('/api/relation/clear', { id: accepted.data.id });
      if (!current() || this.accepted !== accepted) return;
      this.accepted = null;
      requireEl('relationsResults').hidden = true;
      this.message('');
      this.controls();
    });
  }

  async export(): Promise<void> {
    const accepted = this.accepted;
    if (!accepted || this.busy || !this.limits) return;
    try {
      const maximum = requireInput('relationsMaximum').value.replace(/,/g, '').trim();
      if (!/^[1-9]\d*$/.test(maximum) || BigInt(maximum) > BigInt(this.limits.maximumRows)) {
        throw new Error('导出上限无效');
      }
      if (parseRelationCount(accepted.data.preview.total) > BigInt(maximum)) {
        throw new Error(`结果 ${formatRelationCount(accepted.data.preview.total)} 条，超过导出上限`);
      }
      const format = requireSelect('relationsFormat').value;
      if (format && !this.exportOptions.some((option) => option.format === format)) {
        throw new Error('关联导出格式无效');
      }
      const payload: RelationExportPayload = {
        jobId: accepted.data.id,
        mode: accepted.data.outputMode,
        maximum,
        protectCSV: requireInput('relationsProtect').checked,
        ...(format ? { format } : {}),
      };
      const name = `${accepted.data.left.name} ↔ ${accepted.data.right.name}`;
      this.dialog.close();
      await this.onExport(payload, name);
    } catch (error: unknown) {
      this.message(errorMessage(error), true);
    }
  }

  chooseExportFormat(): void {
    if (this.exportFormatTouched || !this.accepted || !this.limits || !this.exportOptions.length) {
      return;
    }
    const compressed =
      Boolean(this.limits.zipRows) &&
      parseRelationCount(this.accepted.data.preview.total) >= BigInt(this.limits.zipRows);
    const option =
      this.exportOptions.find((item) => item.compressed === compressed) ?? this.exportOptions[0];
    if (option) requireSelect('relationsFormat').value = option.format;
  }

  async goSource(
    accepted: AcceptedRelation,
    side: 0 | 1,
    row: number,
    col: number,
  ): Promise<void> {
    if (this.accepted !== accepted) return;
    const ticket = ++this.navSerial;
    const identity = this.identity();
    const book = accepted.infos[side].ref.book;
    const foreign = book !== this.context().book;
    const popup = foreign ? window.open('about:blank', '_blank') : null;
    if (popup) popup.opener = null;
    let handled = false;
    try {
      const target = await this.post('/api/relation/navigate', {
        jobId: accepted.data.id,
        book,
        side,
        row,
        col,
      }).then(decodeCompositionNavigationResponse);
      if (
        ticket !== this.navSerial ||
        identity !== this.identity() ||
        this.accepted !== accepted ||
        !this.dialog.open
      ) {
        return;
      }
      if (target.sameBook) {
        popup?.close();
        handled = true;
        this.dialog.close();
        if (target.kind === 'analysis') {
          await this.navigateAnalysis?.(target.analysisSheet, row, col);
        } else {
          this.closeAnalysis?.();
          await this.navigate(target.sheet, `${labelColumn(col)}${row}`);
        }
      } else if (popup) {
        popup.location.assign(target.url);
        handled = true;
      } else {
        this.toast('请允许弹出窗口后重试', 'warning');
      }
    } catch (error: unknown) {
      if (identity === this.identity() && ticket === this.navSerial) {
        this.message(errorMessage(error), true);
      }
    } finally {
      if (popup && !handled) popup.close();
    }
  }
}
