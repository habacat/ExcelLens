/**
 * Multi-table append composition modal controller.
 */
import type {
  CompositionDefinition,
  CompositionDefinitionStore,
  CompositionField,
  CompositionSourceInfo,
  CompositionTaskResult,
  SourceScope,
  TableRef,
} from '../contracts/analysis.ts';
import type { ActivityService, ApiClient, ToastFn } from '../contracts/common.ts';
import type { CompositionLimits } from '../contracts/formats.ts';
import {
  buildCompositionDefinition,
  cloneValue,
  compositionTypeWarning,
  initialCompositionRows,
  labelColumn,
  suggestFieldMatches,
  validateComposition,
  type CompositionMappingRow,
} from '../core/composition-model.ts';
import { errorMessage } from '../core/display-format.ts';
import {
  queryInput,
  requireButton,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { FieldPicker } from '../core/field-picker.ts';
import { currentFileFormats, type FileFormatsRegistry } from '../core/formats.ts';
import { SourceTasks } from '../core/source-tasks.ts';

export interface CompositionContext {
  readonly bookKey: string;
  readonly book: string;
  readonly opened: boolean;
  readonly source: string;
  readonly sourceScope?: SourceScope | null;
  readonly composition?: CompositionDefinition | null;
  readonly keepId: string;
}

export interface CompositionBookSheetMeta {
  readonly name: string;
}

export interface CompositionBookScratchMeta {
  readonly id: string;
  readonly name: string;
}

export interface CompositionBookMeta {
  readonly id: string;
  readonly name: string;
  readonly attached?: boolean;
  readonly sheets?: readonly CompositionBookSheetMeta[];
  readonly scratch?: readonly CompositionBookScratchMeta[];
  readonly scratchLoaded?: boolean;
}

interface ReconcileSourcesResponse {
  readonly books: readonly CompositionBookMeta[];
}

interface CompositionSaveResponse {
  readonly id: string;
  readonly revision: number;
}

interface OpenAttachedSourceResponse {
  readonly book: string;
}

interface CompositionSourceEntry {
  book: string;
  source: string;
  headerRow: number;
  scope: SourceScope | null;
  expectedRef: TableRef | null;
  info: CompositionSourceInfo | null;
}

export interface CompositionOptions {
  readonly api: ApiClient;
  readonly context: () => CompositionContext;
  readonly onUse: (data: CompositionTaskResult, current: () => boolean) => Promise<boolean>;
  readonly toast: ToastFn;
  readonly limits: CompositionLimits;
  readonly formats?: FileFormatsRegistry;
  readonly activity?: ActivityService;
}

const EMPTY_FIELD_PLACEHOLDER: CompositionField = Object.freeze({
  column: 0,
  name: '',
  headerLoaded: false,
  headerUnique: false,
});

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function makeButton(
  text: string,
  action: () => void,
  cls = 'mini-button',
): HTMLButtonElement {
  const node = document.createElement('button');
  node.type = 'button';
  node.className = cls;
  node.textContent = text;
  node.onclick = action;
  return node;
}

function requireFieldSet(id: string): HTMLFieldSetElement {
  const node = requireEl(id);
  if (!(node instanceof HTMLFieldSetElement)) {
    throw new Error(`Expected HTMLFieldSetElement for #${id}`);
  }
  return node;
}

export class CompositionPanel {
  private readonly api: ApiClient;
  private readonly context: () => CompositionContext;
  private readonly onUse: (data: CompositionTaskResult, current: () => boolean) => Promise<boolean>;
  private readonly limits: CompositionLimits;
  public readonly tasks: SourceTasks;

  public readonly dialog: HTMLDialogElement;
  private readonly fieldPicker: FieldPicker;
  public readonly picker: HTMLDialogElement;

  public inputs: CompositionSourceEntry[] = [];
  public rows: CompositionMappingRow[] = [];
  public books: CompositionBookMeta[] = [];
  private catalog: CompositionDefinitionStore = { version: 1, revision: 0, items: [] };
  public serial = 0;
  private page = 0;
  private name = '联合来源';
  public savedID = '';
  public identityKey = '';
  public busy = false;
  private cancellable = true;
  public preview: CompositionTaskResult | null = null;

  constructor(options: CompositionOptions) {
    this.api = options.api;
    this.context = options.context;
    this.onUse = options.onUse;
    this.limits = options.limits;
    this.tasks = new SourceTasks({
      api: this.api,
      context: () => {
        const c = this.context();
        return { bookKey: c.bookKey, book: c.book, opened: c.opened };
      },
      activity: options.activity,
    });

    this.dialog = document.createElement('dialog');
    this.dialog.id = 'compositionDialog';
    this.dialog.className = 'float-dialog composition-dialog';
    this.dialog.setAttribute('aria-labelledby', 'compositionTitle');
    this.dialog.innerHTML = `<form method="dialog" class="dialog-head"><div id="compositionTitle" class="dialog-title">多表追加</div><button class="dialog-close" aria-label="关闭多表追加" value="cancel">×</button></form>
      <div class="composition-body"><fieldset id="compositionControls"><div class="composition-library"><label for="compositionSaved">已保存</label><select id="compositionSaved" class="field-control"></select><button id="compositionRefresh" type="button" class="mini-button">刷新</button><button id="compositionDelete" type="button" class="mini-button">删除</button></div>
      <div class="composition-toolbar"><label class="composition-name"><span>名称</span><input id="compositionName" class="field-control" value="联合来源"></label><button id="compositionUpload" class="mini-button" type="button">添加文件</button><input id="compositionFile" type="file" hidden><button id="compositionAddSource" class="mini-button" type="button">添加来源</button><button id="compositionReadAll" class="mini-button" type="button">读取字段</button></div>
      <div id="compositionSources" class="composition-sources"></div><div class="composition-mapping-tools"><strong>字段对齐</strong><span id="compositionFieldCount"></span><button id="compositionAddField" class="mini-button" type="button">添加字段</button><button id="compositionMissing" class="mini-button" type="button">未匹配设为空列</button></div>
      <div class="composition-scroll"><table id="compositionMapping" class="composition-table"></table></div><div id="compositionFieldPaging" class="composition-paging"></div>
      <section id="compositionPreviewSection" hidden><div class="composition-mapping-tools"><strong>预览</strong><span id="compositionPreviewCount"></span></div><div class="composition-scroll composition-preview"><table id="compositionPreview" class="composition-table"></table></div></section></fieldset></div>
      <div class="composition-footer"><div id="compositionStatus" role="status" aria-live="polite"></div><div class="composition-actions"><button id="compositionCancel" type="button" class="mini-button" hidden>取消任务</button><button id="compositionPrepare" class="mini-button" type="button">预览</button><button id="compositionSave" class="mini-button" type="button" disabled>保存定义</button><button id="compositionUse" class="primary-button compact" type="button" disabled>用于分析</button></div></div>`;
    document.body.append(this.dialog);

    this.fieldPicker = new FieldPicker({
      id: 'compositionPicker',
      pageRows: this.limits.pickerRows,
    });
    this.picker = this.fieldPicker.dialog;

    for (const dialog of [this.dialog, this.picker]) {
      dialog.addEventListener('keydown', (e: KeyboardEvent) => e.stopPropagation());
    }
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open) this.cancel();
    });

    const registry = options.formats ?? currentFileFormats();
    const fileInput = requireInput('compositionFile');
    if (registry) fileInput.accept = registry.raw.extensions.join(',');

    requireButton('compositionUpload').onclick = (): void => fileInput.click();
    fileInput.onchange = (): void => {
      const file = fileInput.files?.[0];
      fileInput.value = '';
      if (file) void this.action((current) => this.upload(file, current), false);
    };
    requireButton('compositionAddSource').onclick = (): void => {
      this.addSource();
      this.render();
    };
    requireButton('compositionReadAll').onclick = (): void => {
      void this.action(async (current) => {
        for (let i = 0; i < this.inputs.length; i++) {
          if (!this.inputs[i]?.info) await this.read(i, current);
        }
      });
    };
    requireInput('compositionName').oninput = (e: Event): void => {
      if (e.target instanceof HTMLInputElement) {
        this.name = e.target.value;
        this.changed();
      }
    };
    requireSelect('compositionSaved').onchange = (e: Event): void => {
      if (e.target instanceof HTMLSelectElement) {
        const value = e.target.value;
        void this.action((current) => this.loadSaved(value, current));
      }
    };
    requireButton('compositionRefresh').onclick = (): void => {
      void this.action((current) => this.refresh(current));
    };
    requireButton('compositionDelete').onclick = (): void => {
      void this.action((current) => this.removeSaved(current), false);
    };
    requireButton('compositionCancel').onclick = (): void => this.cancel();
    requireButton('compositionPrepare').onclick = (): void => {
      void this.action((current) => this.prepare(current));
    };
    requireButton('compositionSave').onclick = (): void => {
      void this.action((current) => this.save(current), false);
    };
    requireButton('compositionUse').onclick = (): void => {
      void this.action((current) => this.use(current));
    };
    requireButton('compositionAddField').onclick = (): void => {
      if (this.inputs[0]?.info) this.pick(0, null);
    };
    requireButton('compositionMissing').onclick = (): void => {
      for (const row of this.rows) {
        for (let i = 0; i < this.inputs.length; i++) {
          if (row.columns[i] == null) row.columns[i] = 0;
        }
      }
      this.changed();
      this.renderMapping();
    };
  }

  identity(): string {
    const c = this.context();
    return JSON.stringify([c.bookKey, c.book]);
  }

  private post<T>(path: string, value: unknown): Promise<T> {
    return this.api<T>(path, { method: 'POST', body: JSON.stringify(value) });
  }

  message(text: string, error = false): void {
    const node = requireEl('compositionStatus');
    node.textContent = text;
    node.setAttribute('role', error ? 'alert' : 'status');
  }

  async action(
    fn: (current: () => boolean) => Promise<void>,
    cancellable = true,
  ): Promise<void> {
    if (this.busy) return;
    const serial = ++this.serial;
    const identity = this.identity();
    const current = (): boolean =>
      serial === this.serial &&
      identity === this.identity() &&
      this.context().opened &&
      this.dialog.open;
    this.busy = true;
    this.cancellable = cancellable;
    this.controls();
    try {
      await fn(current);
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.message(errorMessage(error), true);
      }
    } finally {
      if (serial === this.serial) {
        this.busy = false;
        this.controls();
      }
    }
  }

  cancel(): void {
    const pending = this.busy;
    ++this.serial;
    this.tasks.reset();
    this.busy = false;
    this.dropPreview();
    if (this.picker.open) this.picker.close();
    if (pending && this.cancellable && this.dialog.open) this.message('已取消');
    this.controls();
  }

  reset(): void {
    if (this.dialog.open) this.dialog.close();
    this.cancel();
    this.inputs = [];
    this.rows = [];
    this.books = [];
    this.savedID = '';
    this.identityKey = '';
  }

  async open(): Promise<void> {
    if (!this.context().opened) return;
    if (this.identityKey !== this.identity()) {
      this.reset();
      this.identityKey = this.identity();
      this.name = '联合来源';
    }
    if (!this.dialog.open) {
      this.cancel();
      this.dialog.showModal();
    }
    await this.action(async (current) => {
      await this.refresh(current);
      if (!current()) return;
      if (!this.inputs.length) {
        const existingComposition = this.context().composition;
        if (existingComposition) {
          await this.loadDefinition(existingComposition, current);
        } else {
          this.addSource(this.context().book);
          this.addSource();
          this.render();
        }
      }
    });
  }

  async refresh(current: () => boolean, focus = ''): Promise<void> {
    const [catalog, sources] = await Promise.all([
      this.api<CompositionDefinitionStore>('/api/composition/definitions'),
      this.api<ReconcileSourcesResponse>(
        `/api/reconcile/sources${focus ? `?book=${encodeURIComponent(focus)}` : ''}`,
      ),
    ]);
    if (!current()) return;
    const previous = new Map(this.books.map((book) => [book.id, book]));
    this.books = sources.books.map((book) => {
      const prev = previous.get(book.id);
      return !book.scratchLoaded && prev?.scratchLoaded
        ? { ...book, scratch: prev.scratch, scratchLoaded: true }
        : book;
    });
    this.catalog = catalog;
    if (this.savedID && !catalog.items.some((item) => item.id === this.savedID)) {
      this.savedID = '';
      this.message('原定义已移除，可另存');
    }
    this.renderLibrary();
    this.renderSources();
    this.controls();
  }

  addSource(book = ''): void {
    if (this.inputs.length >= this.limits.sources) return;
    const c = this.context();
    const selected =
      book || this.books.find((item) => item.id !== c.book)?.id || c.book;
    const meta = this.books.find((item) => item.id === selected);
    const available = (meta?.sheets ?? [])
      .map((sheet) => `wb:${sheet.name}`)
      .concat((meta?.scratch ?? []).map((sheet) => `scratch:${sheet.id}`));
    const source =
      !this.inputs.length && selected === c.book && available.includes(c.source)
        ? c.source
        : (available.find(
            (src) => !this.inputs.some((input) => input.book === selected && input.source === src),
          ) ?? '');
    this.inputs.push({
      book: selected,
      source,
      headerRow: source.startsWith('scratch:') ? 0 : 1,
      scope: null,
      expectedRef: null,
      info: null,
    });
    for (const row of this.rows) row.columns.push(null);
    this.changed();
  }

  dropPreview(): void {
    if (this.preview) {
      void this.tasks.clear(this.preview.id);
      this.preview = null;
    }
    requireEl('compositionPreviewSection').hidden = true;
  }

  changed(): void {
    this.dropPreview();
    this.message('');
    this.controls();
  }

  controls(): void {
    const ready = this.preview !== null && !this.busy;
    requireFieldSet('compositionControls').disabled = this.busy;
    requireButton('compositionCancel').hidden = !this.busy || !this.cancellable;
    requireButton('compositionPrepare').disabled = this.busy || this.inputs.length < 2;
    requireButton('compositionUse').disabled = !ready;
    requireButton('compositionSave').disabled = !ready;
    requireButton('compositionDelete').disabled = this.busy || !this.savedID;
    requireButton('compositionAddSource').disabled =
      this.busy || this.inputs.length >= this.limits.sources;
    requireButton('compositionAddField').disabled =
      this.busy || !this.inputs[0]?.info || this.rows.length >= this.limits.columns;
  }

  renderLibrary(): void {
    const select = requireSelect('compositionSaved');
    select.replaceChildren(new Option('新建联合来源', ''));
    for (const item of this.catalog.items) {
      select.append(new Option(item.definition.name, item.id));
    }
    select.value = this.savedID;
  }

  render(): void {
    this.renderLibrary();
    requireInput('compositionName').value = this.name;
    this.renderSources();
    this.renderMapping();
    this.controls();
  }

  renderSources(): void {
    const host = requireEl('compositionSources');
    host.replaceChildren();
    const maxHeaderRow = queryInput('analysisHeaderRow')?.max ?? '';
    this.inputs.forEach((input, index) => {
      const section = document.createElement('div');
      section.className = 'composition-source';
      const heading = document.createElement('div');
      heading.className = 'composition-source-head';
      const title = document.createElement('strong');
      title.textContent = `来源 ${index + 1}`;
      heading.append(
        title,
        makeButton('移除', () => {
          this.inputs.splice(index, 1);
          for (const row of this.rows) row.columns.splice(index, 1);
          this.changed();
          this.render();
        }),
      );
      section.append(heading);

      const book = document.createElement('select');
      book.className = 'field-control';
      book.setAttribute('aria-label', `来源 ${index + 1} 文件`);
      for (const choice of this.books) book.append(new Option(choice.name, choice.id));
      if (!this.books.some((choice) => choice.id === input.book)) {
        book.append(new Option('来源未打开', input.book));
      }
      book.value = input.book;

      const sheet = document.createElement('select');
      sheet.className = 'field-control';
      sheet.setAttribute('aria-label', `来源 ${index + 1} 工作表`);
      sheet.append(new Option('选择工作表', ''));
      const metadata = this.books.find((choice) => choice.id === input.book);
      for (const item of metadata?.sheets ?? []) {
        sheet.append(new Option(item.name, `wb:${item.name}`));
      }
      for (const item of metadata?.scratch ?? []) {
        sheet.append(new Option(item.name, `scratch:${item.id}`));
      }
      if (![...sheet.options].some((option) => option.value === input.source)) {
        sheet.append(new Option(input.source.replace(/^(wb:|scratch:)/, ''), input.source));
      }
      sheet.value = input.source;

      const invalidate = (columns = true): void => {
        input.info = null;
        input.expectedRef = null;
        if (columns) {
          for (const row of this.rows) row.columns[index] = null;
        }
        this.changed();
        this.render();
      };

      book.onchange = (): void => {
        input.book = book.value;
        const firstSheet = this.books.find((choice) => choice.id === input.book)?.sheets?.[0]?.name;
        input.source = firstSheet ? `wb:${firstSheet}` : '';
        input.scope = null;
        input.headerRow = 1;
        invalidate();
        void this.action((current) => this.refresh(current, input.book));
      };
      sheet.onchange = (): void => {
        input.source = sheet.value;
        input.scope = null;
        input.headerRow = input.source.startsWith('scratch:') ? 0 : 1;
        invalidate();
      };
      section.append(book, sheet);

      const footer = document.createElement('div');
      footer.className = 'composition-source-settings';
      const headerLabel = document.createElement('label');
      headerLabel.textContent = '表头行';
      const header = document.createElement('input');
      header.type = 'number';
      header.className = 'field-control';
      header.min = '1';
      if (maxHeaderRow) header.max = maxHeaderRow;
      header.value = String(input.headerRow);
      header.disabled = input.source.startsWith('scratch:');
      header.setAttribute('aria-label', `来源 ${index + 1} 表头行`);
      header.onchange = (): void => {
        input.headerRow = Number(header.value);
        invalidate();
      };
      headerLabel.append(header);
      footer.append(headerLabel);

      const scope = document.createElement('select');
      scope.className = 'field-control';
      scope.setAttribute('aria-label', `来源 ${index + 1} 范围`);
      scope.append(new Option('完整源表', 'full'));
      if (input.scope) scope.append(new Option('保存的范围', 'saved'));
      const c = this.context();
      if (c.sourceScope && c.book === input.book && c.source === input.source) {
        scope.append(new Option('当前分析范围', 'current'));
      }
      scope.value = input.scope ? 'saved' : 'full';
      scope.disabled = input.source.startsWith('scratch:');
      scope.onchange = (): void => {
        input.scope =
          scope.value === 'current' && c.sourceScope
            ? cloneValue(c.sourceScope)
            : scope.value === 'saved'
              ? input.scope
              : null;
        invalidate(false);
      };
      footer.append(scope);
      section.append(footer);

      const info = document.createElement('span');
      info.className = 'composition-source-state';
      info.textContent = input.info ? `${input.info.rows.toLocaleString()} 条记录` : '未读取字段';
      section.append(
        info,
        makeButton('读取字段', () => {
          void this.action((current) => this.read(index, current));
        }),
      );
      if (metadata?.attached) {
        section.append(
          makeButton('移除文件', () => {
            void this.action(async (current) => {
              await this.post('/api/reconcile/source/remove', { book: input.book });
              if (current()) {
                invalidate();
                await this.refresh(current);
              }
            });
          }),
        );
      }
      host.append(section);
    });
  }

  async read(index: number, current: () => boolean): Promise<void> {
    const input = this.inputs[index];
    if (!input) return;
    this.message(`正在读取来源 ${index + 1}…`);
    let result: (CompositionTaskResult & { readonly id: string }) | undefined;
    try {
      const keepId = this.context().keepId;
      result = await this.tasks.run<CompositionTaskResult>(
        {
          mode: 'fields',
          choice: {
            book: input.book,
            source: input.source,
            headerRow: input.headerRow,
            ...(input.scope ? { scope: input.scope } : {}),
          },
          ...(keepId ? { keepId } : {}),
        },
        current,
      );
      if (!current() || !result.info) return;
      if (
        input.expectedRef &&
        JSON.stringify(input.expectedRef) !== JSON.stringify(result.info.ref)
      ) {
        throw new Error('来源已变化，请重新选择后读取字段');
      }
      input.info = result.info;
      input.headerRow = result.info.ref.headerRow;
      input.scope = result.info.ref.scope ?? null;
      const fields = input.info.fields ?? [];
      if (index === 0 && !this.rows.length) {
        this.rows = initialCompositionRows(fields, this.limits.columns);
        for (const row of this.rows) {
          while (row.columns.length < this.inputs.length) row.columns.push(null);
        }
      }
      const firstInfoFields = this.inputs[0]?.info?.fields ?? null;
      if (firstInfoFields) {
        for (const row of this.rows) {
          if (!row.expected) {
            row.expected = firstInfoFields.find((field) => field.column === row.columns[0]);
          }
        }
      }
      for (let i = 0; i < this.inputs.length; i++) {
        const actual = this.inputs[i]?.info?.fields;
        if (!actual) continue;
        const suggestions = suggestFieldMatches(
          this.rows.map((row) => row.expected ?? EMPTY_FIELD_PLACEHOLDER),
          actual,
        );
        this.rows.forEach((row, slot) => {
          const suggested = suggestions[slot];
          if (row.columns[i] == null && suggested) row.columns[i] = suggested;
        });
      }
      this.changed();
      this.render();
      this.message(
        fields.length > this.limits.columns
          ? `已选前${this.limits.columns}个字段；可移除后添加其他字段`
          : '',
      );
    } finally {
      if (result) await this.tasks.clear(result.id);
    }
  }

  renderMapping(): void {
    const table = requireEl('compositionMapping');
    table.replaceChildren();
    requireEl('compositionFieldCount').textContent = this.rows.length
      ? `共 ${this.rows.length} 个`
      : '';
    const head = document.createElement('thead');
    const headerRow = document.createElement('tr');
    for (const text of ['结果字段', ...this.inputs.map((_, i) => `来源 ${i + 1}`), '']) {
      const th = document.createElement('th');
      th.textContent = text;
      headerRow.append(th);
    }
    head.append(headerRow);
    table.append(head);

    const body = document.createElement('tbody');
    this.page = Math.min(
      this.page,
      Math.max(0, Math.ceil(this.rows.length / this.limits.fieldPageRows) - 1),
    );
    const begin = this.page * this.limits.fieldPageRows;
    this.rows.slice(begin, begin + this.limits.fieldPageRows).forEach((row, offset) => {
      const at = begin + offset;
      const tr = document.createElement('tr');
      const nameCell = document.createElement('td');
      const name = document.createElement('input');
      name.className = 'field-control';
      name.value = row.name;
      name.setAttribute('aria-label', `结果字段 ${at + 1}`);
      name.oninput = (): void => {
        row.name = name.value;
        this.changed();
      };
      nameCell.append(name);
      const warning = compositionTypeWarning(row, this.inputs);
      if (warning) {
        const badge = document.createElement('span');
        badge.className = 'composition-type-conflict';
        badge.textContent = warning;
        nameCell.append(badge);
      }
      tr.append(nameCell);

      this.inputs.forEach((input, index) => {
        const cell = document.createElement('td');
        const column = row.columns[index];
        const field = input.info?.fields?.find((f) => f.column === column);
        const pick = makeButton(
          column === 0 ? '空列' : field && column ? `${labelColumn(column)} ${field.name}` : '选择字段',
          () => this.pick(index, at),
          'composition-field-button',
        );
        pick.classList.toggle('is-missing', column == null);
        pick.disabled = !input.info;
        pick.setAttribute('aria-label', `结果字段 ${at + 1} 来源 ${index + 1}`);
        cell.append(pick);
        tr.append(cell);
      });

      const remove = document.createElement('td');
      remove.append(
        makeButton('移除', () => {
          this.rows.splice(at, 1);
          this.changed();
          this.renderMapping();
        }),
      );
      tr.append(remove);
      body.append(tr);
    });
    table.append(body);
    this.renderPaging(
      requireEl('compositionFieldPaging'),
      this.rows.length,
      this.page,
      this.limits.fieldPageRows,
      (page) => {
        this.page = page;
        this.renderMapping();
      },
    );
    this.controls();
  }

  private renderPaging(
    host: HTMLElement,
    count: number,
    page: number,
    size: number,
    change: (nextPage: number) => void,
  ): void {
    host.replaceChildren();
    if (count <= size) return;
    const previous = makeButton('上一页', () => change(page - 1));
    const next = makeButton('下一页', () => change(page + 1));
    previous.disabled = page === 0;
    next.disabled = (page + 1) * size >= count;
    const info = document.createElement('span');
    info.textContent = `${page + 1} / ${Math.ceil(count / size)}`;
    host.append(previous, info, next);
  }

  pick(index: number, row: number | null): void {
    const input = this.inputs[index];
    const info = input?.info;
    if (!input || !info) return;
    const fields = info.fields ?? [];
    const disabled = this.rows
      .filter((_, i) => i !== row)
      .map((r) => r.columns[index])
      .filter((col): col is number => typeof col === 'number');
    this.fieldPicker.open({
      fields,
      allowEmpty: row !== null,
      disabled,
      current: () => this.dialog.open && this.inputs[index] === input && input.info === info,
      onChoose: (column) => {
        if (row === null) {
          const field = fields.find((f) => f.column === column);
          if (!field || this.rows.length >= this.limits.columns) return;
          const columns: (number | null)[] = Array.from({ length: this.inputs.length }, () => null);
          columns[index] = column;
          const newRow: CompositionMappingRow = { name: field.name, columns, expected: field };
          for (let i = 0; i < this.inputs.length; i++) {
            const otherFields = this.inputs[i]?.info?.fields;
            if (i !== index && otherFields) {
              newRow.columns[i] = suggestFieldMatches([field], otherFields)[0] ?? null;
            }
          }
          this.rows.push(newRow);
        } else {
          const targetRow = this.rows[row];
          if (targetRow) targetRow.columns[index] = column;
        }
        this.changed();
        this.renderMapping();
      },
    });
  }

  async prepare(current: () => boolean): Promise<void> {
    const error = validateComposition(this.name, this.rows, this.inputs, this.limits);
    if (error) throw new Error(error);
    this.dropPreview();
    this.message('正在准备联合来源…');
    const keepId = this.context().keepId;
    const data = await this.tasks.run<CompositionTaskResult>(
      {
        mode: 'append',
        definition: buildCompositionDefinition(this.name, this.rows, this.inputs),
        ...(keepId ? { keepId } : {}),
      },
      current,
    );
    if (!current()) {
      await this.tasks.clear(data.id);
      return;
    }
    this.preview = data;
    this.renderPreview(data);
    this.message(data.previewTruncated ? `仅预览前${data.preview?.length ?? 0}条` : '');
  }

  renderPreview(data: CompositionTaskResult): void {
    requireEl('compositionPreviewSection').hidden = false;
    requireEl('compositionPreviewCount').textContent = `${data.rows.toLocaleString()} 条记录`;
    const table = requireEl('compositionPreview');
    table.replaceChildren();
    const head = document.createElement('thead');
    const hr = document.createElement('tr');
    for (const label of ['来源', '原行号', ...(data.definition?.fields ?? [])]) {
      const th = document.createElement('th');
      th.textContent = label;
      hr.append(th);
    }
    head.append(hr);
    table.append(head);

    const body = document.createElement('tbody');
    for (const row of data.preview ?? []) {
      const tr = document.createElement('tr');
      const texts: readonly string[] = [
        String(row.input + 1),
        String(row.row),
        ...row.cells.map((cell) => (!cell.exists ? '' : cell.cacheMissing ? '缺少缓存' : cell.display)),
      ];
      for (const text of texts) {
        const td = document.createElement('td');
        td.textContent = text;
        tr.append(td);
      }
      body.append(tr);
    }
    table.append(body);
  }

  async save(current: () => boolean): Promise<void> {
    if (!this.preview) return;
    const c = this.context();
    const response = await this.post<CompositionSaveResponse>('/api/composition/definitions', {
      action: 'save',
      id: this.savedID,
      jobId: this.preview.id,
      book: c.book,
      workbookKey: c.bookKey,
      expectedRevision: this.catalog.revision,
    });
    if (!current()) return;
    this.savedID = response.id;
    this.catalog = { ...this.catalog, revision: response.revision };
    await this.refresh(current);
    if (current()) this.message('已保存');
  }

  async removeSaved(current: () => boolean): Promise<void> {
    if (!this.savedID) return;
    const c = this.context();
    await this.post('/api/composition/definitions', {
      action: 'delete',
      id: this.savedID,
      book: c.book,
      workbookKey: c.bookKey,
      expectedRevision: this.catalog.revision,
    });
    if (!current()) return;
    this.savedID = '';
    await this.refresh(current);
    if (current()) this.message('已删除定义');
  }

  async loadSaved(id: string, current: () => boolean): Promise<void> {
    this.savedID = id;
    if (!id) {
      this.dropPreview();
      this.rows = [];
      this.inputs = [];
      this.name = '联合来源';
      this.addSource(this.context().book);
      this.addSource();
      this.render();
      return;
    }
    const item = this.catalog.items.find((entry) => entry.id === id);
    if (item) await this.loadDefinition(item.definition, current);
  }

  async loadDefinition(definition: CompositionDefinition, current: () => boolean): Promise<void> {
    this.dropPreview();
    this.name = definition.name;
    this.inputs = definition.inputs.map((input) => ({
      book: input.ref.book,
      source: input.ref.source,
      headerRow: input.ref.headerRow,
      scope: input.ref.scope ?? null,
      expectedRef: input.ref,
      info: null,
    }));
    this.rows = definition.fields.map((name, i) => ({
      name,
      columns: definition.inputs.map((input) => input.columns[i] ?? null),
    }));
    this.page = 0;
    this.render();
    for (let i = 0; i < this.inputs.length && current(); i++) {
      await this.read(i, current);
    }
  }

  async use(current: () => boolean): Promise<void> {
    const data = this.preview;
    if (!data) return;
    let accepted = false;
    try {
      accepted = await this.onUse(data, current);
      if (accepted) {
        this.preview = null;
        this.dialog.close();
      }
    } finally {
      if (!accepted) await this.tasks.clear(data.id);
      if (this.preview === data) this.preview = null;
      this.controls();
    }
  }

  async upload(file: File, current: () => boolean): Promise<void> {
    const c = this.context();
    const form = new FormData();
    form.append('file', file, file.name);
    form.append('reconcileSource', '1');
    form.append('workbookKey', c.bookKey);
    form.append('primaryBook', c.book);
    const data = await this.api<OpenAttachedSourceResponse>('/api/open', {
      method: 'POST',
      body: form,
    });
    if (!current()) return;
    await this.refresh(current, data.book);
    if (!current()) return;
    const empty = this.inputs.findIndex((input, index) => index > 0 && !input.info);
    if (empty >= 0) {
      const firstSheet = this.books.find((book) => book.id === data.book)?.sheets?.[0]?.name ?? '';
      this.inputs[empty] = {
        book: data.book,
        source: firstSheet ? `wb:${firstSheet}` : '',
        headerRow: 1,
        scope: null,
        expectedRef: null,
        info: null,
      };
      for (const row of this.rows) row.columns[empty] = null;
      this.changed();
    } else if (this.inputs.length < this.limits.sources) {
      this.addSource(data.book);
    }
    this.render();
  }
}
