/**
 * Bounded transposed reading dialog for a contiguous grid selection.
 * Original CellData and coordinates remain intact in both grid and detail pane.
 */
import type { ToastFn } from '../contracts/common.ts';
import type { RichTextLimits, TransposeLimits } from '../contracts/formats.ts';
import type { CellData, VisualStyle } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireSelect } from '../core/dom.ts';
import {
  CellPresenter,
  cellSourceTypeLabel,
  isTextCell,
} from '../grid/cell-view.ts';
import { styleOfCell } from '../grid/clipboard.ts';
import type { ClipboardSnapshot } from '../grid/grid-clipboard.ts';
import type { GridCellElement } from '../grid/state.ts';
import {
  transposeAt,
  transposePageSize,
  transposeValue,
  validateTranspose,
  type TransposedItem,
  type TransposedModelData,
  type TransposeViewMode,
} from './transpose-model.ts';

export interface TransposeCaptureOptions {
  readonly purpose: string;
  readonly limits: TransposeLimits;
}

export interface TransposeOptions {
  readonly capture: (options: TransposeCaptureOptions) => ClipboardSnapshot;
  readonly toast: ToastFn;
  readonly copy: (text: string) => Promise<boolean | undefined>;
  readonly navigate: (sheet: string, ref: string, guard: () => boolean) => Promise<unknown>;
  readonly colName: (col: number) => string;
  readonly fontStack: (name: string) => string;
  readonly limits: TransposeLimits;
  readonly richLimits?: RichTextLimits | null;
}

interface TransposeCursor {
  column: number;
  index: number;
}

function makeEl<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  cls = '',
  text = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = cls;
  node.textContent = text;
  return node;
}

function makeSpanCell(cls: string): GridCellElement {
  const el: HTMLElement = document.createElement('span');
  el.className = cls;
  return el as GridCellElement;
}

function previewClip(text: string): string {
  let end = Math.min(text.length, 240);
  if (end < text.length) {
    const ch = text[end - 1];
    if (ch !== undefined && /[\uD800-\uDBFF]/.test(ch)) --end;
  }
  return text.slice(0, end) + (end < text.length ? '…' : '');
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

function isTransposeMode(value: string): value is TransposeViewMode {
  return value === 'display' || value === 'raw' || value === 'formula';
}

const KEY_STEPS: Readonly<Record<string, readonly [number, number]>> = {
  ArrowLeft: [0, -1],
  ArrowRight: [0, 1],
  ArrowUp: [-1, 0],
  ArrowDown: [1, 0],
};

export class Transpose {
  private readonly capture: TransposeOptions['capture'];
  private readonly toast: ToastFn;
  private readonly copy: TransposeOptions['copy'];
  private readonly navigate: TransposeOptions['navigate'];
  private readonly colName: (col: number) => string;
  private readonly fontStack: (name: string) => string;
  private readonly limits: TransposeLimits;
  private readonly richLimits?: RichTextLimits | null;

  public readonly dialog: HTMLDialogElement;
  public readonly resize: ResizeObserver;

  private serial = 0;
  public navigation = 0;
  public busy = false;
  public mode: TransposeViewMode = 'display';
  private start = 0;
  private columns = 1;
  public snapshot: ClipboardSnapshot | null = null;
  public data: TransposedModelData | null = null;
  private selected: TransposeCursor | null = null;
  public selectedItem: TransposedItem | null = null;
  private abort: AbortController | null = null;

  constructor(options: TransposeOptions) {
    this.capture = options.capture;
    this.toast = options.toast;
    this.copy = options.copy;
    this.navigate = options.navigate;
    this.colName = options.colName;
    this.fontStack = options.fontStack;
    this.limits = options.limits;
    this.richLimits = options.richLimits;

    if (!this.limits.cells || !this.limits.bytes || !this.limits.pageColumns) {
      throw new Error('转置容量配置不可用');
    }

    this.dialog = this.create();
    requireButton('transposeClose').onclick = () => this.close();
    requireButton('transposeReload').onclick = () => {
      void this.load();
    };
    requireButton('transposeCancel').onclick = () => this.cancel();
    requireButton('transposePrevious').onclick = () => this.page(-1);
    requireButton('transposeNext').onclick = () => this.page(1);
    requireSelect('transposeMode').onchange = () => {
      const value = requireSelect('transposeMode').value;
      this.mode = isTransposeMode(value) ? value : 'display';
      this.render();
    };
    requireButton('transposeCopy').onclick = () => {
      void this.copySelected();
    };
    requireButton('transposeLocate').onclick = () => {
      void this.locate();
    };
    this.dialog.addEventListener('cancel', (event: Event) => {
      event.preventDefault();
      this.close();
    });
    this.dialog.addEventListener('close', () => {
      if (!this.dialog.open && this.snapshot) this.clear();
    });
    requireEl('transposeTable').addEventListener('keydown', (event: KeyboardEvent) =>
      this.key(event),
    );
    this.resize = new ResizeObserver(() => {
      if (!this.dialog.open || !this.data || !this.selected) return;
      const size = transposePageSize(requireEl('transposeBody').clientWidth, this.limits.pageColumns);
      if (size !== this.columns) {
        this.columns = size;
        this.start = Math.floor(this.selected.index / size) * size;
        this.render();
      }
    });
    this.resize.observe(requireEl('transposeBody'));
    for (const name of ['pointerdown', 'keydown'] as const) {
      document.addEventListener(
        name,
        () => {
          ++this.navigation;
        },
        true,
      );
    }
  }

  private create(): HTMLDialogElement {
    const d = makeEl('dialog', 'float-dialog transpose-dialog');
    d.id = 'transposeDialog';
    d.setAttribute('aria-labelledby', 'transposeTitle');
    d.innerHTML =
      '<div class="dialog-head"><div id="transposeTitle" class="dialog-title">转置阅读</div><button id="transposeClose" class="dialog-close" type="button" aria-label="关闭转置阅读">×</button></div><div class="transpose-tools"><span id="transposeSource"></span><select id="transposeMode" class="field-control" aria-label="显示内容"><option value="display">显示值</option><option value="raw">原值</option><option value="formula">公式</option></select><button id="transposeReload" type="button" class="mini-button">重新读取</button><button id="transposeCancel" type="button" class="mini-button" hidden>取消</button></div><div id="transposeBody" class="transpose-body"><table id="transposeTable" role="grid" aria-label="转置选区"></table></div><div class="transpose-pager"><button id="transposePrevious" class="mini-button" type="button" disabled>上一页</button><span id="transposePosition"></span><button id="transposeNext" class="mini-button" type="button" disabled>下一页</button></div><section id="transposeDetail" class="transpose-detail" hidden><div class="transpose-detail-head"><strong id="transposeRef"></strong><span id="transposeType"></span><span class="status-spacer"></span><button id="transposeCopy" class="mini-button" type="button">复制</button><button id="transposeLocate" class="mini-button" type="button">定位原格</button></div><pre id="transposeValue" tabindex="0"></pre><p id="transposeNotice" hidden></p></section><p id="transposeStatus" class="transpose-status" role="status" aria-live="polite" hidden></p>';
    document.body.append(d);
    return d;
  }

  private status(text: string, error = false): void {
    const el = requireEl('transposeStatus');
    el.textContent = text;
    el.hidden = !text;
    el.classList.toggle('is-error', error);
  }

  private controls(): void {
    requireButton('transposeReload').disabled = this.busy;
    requireButton('transposeCancel').hidden = !this.busy;
    requireSelect('transposeMode').disabled = this.busy || this.data === null;
    requireButton('transposePrevious').disabled = this.busy || !this.data || this.start === 0;
    requireButton('transposeNext').disabled =
      this.busy || !this.data || this.start + this.columns >= this.data.rows.length;
    requireButton('transposeCopy').disabled =
      this.busy ||
      !this.selectedItem ||
      transposeValue(this.selectedItem, this.mode, Boolean(this.snapshot?.styled)) === null;
    requireButton('transposeLocate').disabled = this.busy || !this.selectedItem;
  }

  open(): void {
    if (this.dialog.open) return;
    try {
      this.snapshot = this.capture({ purpose: '转置', limits: this.limits });
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'warning');
      return;
    }
    this.start = 0;
    this.selected = null;
    this.data = null;
    this.mode = 'display';
    requireSelect('transposeMode').value = this.mode;
    requireEl('transposeSource').textContent =
      `${this.snapshot.sheet}!${this.snapshot.start}:${this.snapshot.end}`;
    this.dialog.showModal();
    void this.load();
  }

  private stop(): void {
    ++this.serial;
    this.abort?.abort();
    this.abort = null;
    this.busy = false;
  }

  private clear(): void {
    this.stop();
    ++this.navigation;
    this.snapshot = null;
    this.data = null;
    this.selected = null;
    this.selectedItem = null;
    requireEl('transposeTable').replaceChildren();
    requireEl('transposeValue').textContent = '';
    requireEl('transposeNotice').textContent = '';
    requireEl('transposeDetail').hidden = true;
    requireEl('transposePosition').textContent = '';
    this.status('');
  }

  close(): void {
    this.clear();
    if (this.dialog.open) this.dialog.close();
  }

  resetBook(): void {
    this.close();
  }

  changed(): void {
    if (this.snapshot && !this.snapshot.current()) this.close();
  }

  cancel(): void {
    this.stop();
    this.status('已取消');
    this.controls();
  }

  private currentSnapshot(): ClipboardSnapshot | null {
    return this.snapshot;
  }

  async load(): Promise<void> {
    if (!this.snapshot || !this.dialog.open || this.busy) return;
    this.changed();
    const snapshot = this.currentSnapshot();
    if (!snapshot) return;
    this.stop();
    const serial = this.serial;
    const controller = new AbortController();
    this.abort = controller;
    this.busy = true;
    this.controls();
    this.status('正在读取…');
    const current = (): boolean =>
      serial === this.serial &&
      !controller.signal.aborted &&
      this.dialog.open &&
      snapshot === this.snapshot &&
      snapshot.current();
    try {
      const payload = await snapshot.read(
        { force: true, richText: snapshot.styled, conditional: snapshot.styled },
        controller.signal,
      );
      if (!current()) return;
      const data = validateTranspose(payload, snapshot, this.limits, this.colName);
      if (!current()) return;
      this.data = data;
      this.columns = transposePageSize(
        requireEl('transposeBody').clientWidth,
        this.limits.pageColumns,
      );
      this.start = 0;
      this.selected = { column: data.firstColumn, index: 0 };
      this.render();
      this.status('');
    } catch (error: unknown) {
      if (current() && !isAbortError(error)) {
        this.status(`${errorMessage(error)}${this.data ? '（保留上次内容）' : ''}`, true);
      }
    } finally {
      if (serial === this.serial) {
        this.abort = null;
        this.busy = false;
        this.controls();
      }
    }
  }

  page(direction: -1 | 1): void {
    if (!this.data || !this.selected || this.busy) return;
    this.start = Math.max(
      0,
      Math.min(
        Math.floor((this.data.rows.length - 1) / this.columns) * this.columns,
        this.start + direction * this.columns,
      ),
    );
    this.selected.index = this.start;
    this.render();
  }

  render(focus = false): void {
    this.changed();
    if (!this.data || !this.snapshot || !this.selected || !this.dialog.open) return;
    const stylesCopy: VisualStyle[] = [...this.snapshot.styles];
    const state = {
      showStyles: this.snapshot.styled,
      showFormulas: false,
      fontOverride: this.snapshot.fontOverride,
      styles: stylesCopy,
      autoFilter: null,
      indexReady: true,
    };
    const fragment = document.createDocumentFragment();
    const head = makeEl('thead');
    const line = makeEl('tr');
    const width = Math.min(this.columns, this.data.rows.length - this.start);
    const warnings = new Set<string>();
    line.append(makeEl('th', '', '源列 / 源行'));
    for (let index = this.start; index < this.start + width; index++) {
      const row = this.data.rows[index];
      if (!row) continue;
      const th = makeEl('th', '', String(row.number));
      th.scope = 'col';
      line.append(th);
    }
    head.append(line);
    fragment.append(head);

    const body = makeEl('tbody');
    const presenter = new CellPresenter({
      state,
      richLimits: this.richLimits,
      fontStack: this.fontStack,
      scale: () => 1,
      colName: this.colName,
      applyStyle: (node: GridCellElement, cell: Partial<CellData>) => {
        node.style.cssText = styleOfCell(cell, state.styles, state.showStyles, state.fontOverride);
        // Explicit source ink/fill belongs to the workbook's white paper, not
        // the surrounding dark UI. Preserve specified colors and supply only
        // their missing default counterpart, like the detached paper painter.
        if (state.showStyles) {
          const ink = Boolean(node.style.color);
          const fill = Boolean(node.style.backgroundColor);
          if (fill && !ink) node.style.color = '#000000';
          if (ink && !fill) node.style.backgroundColor = '#ffffff';
        }
        if (!node.style.textAlign) {
          const fullCell = cell as CellData;
          node.style.textAlign =
            cell.type === 'n' || (!cell.type && !isTextCell(fullCell)) ? 'right' : 'left';
        }
      },
    });

    const items: TransposedItem[] = [];
    for (let column = this.data.firstColumn; column <= this.data.lastColumn; column++) {
      for (let index = this.start; index < this.start + width; index++) {
        const item = transposeAt(this.data, column, index, this.colName);
        if (item) items.push(item);
      }
    }
    presenter.prepare(
      items.map((item) => ({
        number: item.row,
        style: item.style,
        cells: item.cell ? [item.cell] : [],
      })),
    );

    let position = 0;
    const table = requireEl('transposeTable');
    for (let column = this.data.firstColumn; column <= this.data.lastColumn; column++) {
      const tr = makeEl('tr');
      const th = makeEl('th', '', this.colName(column));
      th.scope = 'row';
      tr.append(th);
      for (let index = this.start; index < this.start + width; index++) {
        const item = items[position++];
        if (!item) continue;
        const cell = item.cell;
        const value = transposeValue(item, this.mode, state.showStyles);
        const text =
          value === null ? (this.mode === 'formula' ? '公式不可还原' : '无保存结果') : value;
        const td = makeEl('td');
        const button = makeEl('button', 'transpose-cell');
        const node = makeSpanCell('transpose-cell-value');
        button.type = 'button';
        button.dataset.column = String(column);
        button.dataset.index = String(index);
        button.dataset.ref = item.ref;
        node.dataset.row = String(item.row);
        node.dataset.col = String(column);
        node.dataset.ref = item.ref;
        if (cell) {
          // The proxy exists only for bounded DOM text. The original cell is
          // retained unchanged in the model and in the full-content panel.
          const clipped = previewClip(text);
          const rich = this.mode === 'display' && value !== null && clipped === text;
          const painted: CellData = rich
            ? cell
            : {
                ...cell,
                display: clipped,
                richText: undefined,
                conditional: cell.conditional
                  ? { ...cell.conditional, display: clipped }
                  : undefined,
              };
          presenter.paint(node, painted, { number: item.row, cells: [], style: item.style });
          if (value === null || this.mode !== 'display' || clipped !== text) {
            node.textContent = clipped;
          }
        } else {
          presenter.paint(node, null, { number: item.row, cells: [], style: item.style });
          presenter.o.applyStyle(node, { style: item.style });
        }
        for (const notice of [item.notice, node._xlRichNotice, node._xlPresentationNotice]) {
          if (notice) warnings.add(notice);
        }
        const isSelected = this.selected.column === column && this.selected.index === index;
        button.classList.toggle('is-selected', isSelected);
        button.setAttribute(
          'aria-label',
          `${item.ref} ${cell ? cellSourceTypeLabel(cell) : '空白'}`,
        );
        button.setAttribute('aria-selected', String(isSelected));
        button.onclick = () => {
          this.selected = { column, index };
          this.detail();
          for (const other of table.querySelectorAll('.transpose-cell')) {
            const active = other === button;
            other.classList.toggle('is-selected', active);
            other.setAttribute('aria-selected', String(active));
          }
        };
        button.append(node);
        td.append(button);
        tr.append(td);
      }
      body.append(tr);
    }

    fragment.append(body);
    table.replaceChildren(fragment);
    requireEl('transposePosition').textContent =
      `${this.start + 1}–${this.start + width} / ${this.data.rows.length}`;
    this.detail();
    this.controls();
    if (warnings.size) {
      const noticeEl = requireEl('transposeNotice');
      noticeEl.textContent = [...warnings].join('；');
      noticeEl.hidden = false;
    }
    if (focus) {
      const focusTarget = table.querySelector(
        `[data-column="${this.selected.column}"][data-index="${this.selected.index}"]`,
      );
      if (focusTarget instanceof HTMLElement) focusTarget.focus({ preventScroll: true });
    }
  }

  private detail(): void {
    if (!this.data || !this.selected || !this.snapshot) return;
    const item = transposeAt(this.data, this.selected.column, this.selected.index, this.colName);
    if (!item) return;
    const value = transposeValue(item, this.mode, this.snapshot.styled);
    this.selectedItem = item;
    requireEl('transposeDetail').hidden = false;
    requireEl('transposeRef').textContent = item.ref;
    requireEl('transposeType').textContent = item.cell ? cellSourceTypeLabel(item.cell) : '空白';
    requireEl('transposeValue').textContent =
      value === null ? (this.mode === 'formula' ? '公式不可还原' : '公式无保存结果') : value;
    const notes = [item.notice, item.cell?.conditional?.notice].filter((n): n is string =>
      Boolean(n),
    );
    const noticeEl = requireEl('transposeNotice');
    noticeEl.textContent = notes.join('；');
    noticeEl.hidden = !notes.length;
    this.controls();
  }

  private key(event: KeyboardEvent): void {
    if (
      !(event.target instanceof Element && event.target.closest('.transpose-cell')) ||
      !this.data ||
      !this.selected ||
      this.busy
    ) {
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'c') {
      event.preventDefault();
      event.stopPropagation();
      void this.copySelected();
      return;
    }
    const step = KEY_STEPS[event.key];
    if (!step) return;
    event.preventDefault();
    event.stopPropagation();
    this.selected = {
      column: Math.max(
        this.data.firstColumn,
        Math.min(this.data.lastColumn, this.selected.column + step[0]),
      ),
      index: Math.max(0, Math.min(this.data.rows.length - 1, this.selected.index + step[1])),
    };
    this.start = Math.floor(this.selected.index / this.columns) * this.columns;
    this.render(true);
  }

  async copySelected(): Promise<void> {
    if (!this.selectedItem || this.busy || !this.snapshot?.current()) return;
    const value = transposeValue(this.selectedItem, this.mode, this.snapshot.styled);
    if (value === null) return;
    try {
      await this.copy(value);
    } catch (error: unknown) {
      this.toast(errorMessage(error), 'error');
    }
  }

  async locate(): Promise<void> {
    if (!this.selectedItem || this.busy || !this.snapshot?.current()) return;
    const snapshot = this.snapshot;
    const ref = this.selectedItem.ref;
    this.close();
    const ticket = ++this.navigation;
    const guard = (): boolean =>
      !this.dialog.open && ticket === this.navigation && snapshot.sourceCurrent();
    try {
      await this.navigate(snapshot.sheet, ref, guard);
    } catch (error: unknown) {
      if (guard()) this.toast(errorMessage(error), 'error');
    }
  }
}
