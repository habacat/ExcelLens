/**
 * Direct in-grid editable worksheet overlay: live cell/formula-bar editor,
 * projection cache for edited/calculated/generated/affected cells, keyboard
 * & mouse orchestration, queued actions, fill handle, and validation dropdown.
 */
import type {
  ActivityHandle,
  ApiClient,
  CellPoint,
  HttpError,
  MergeRange,
  SelectionArea,
} from '../contracts/common.ts';
import type {
  EditAddress,
  EditChange,
  EditGridProjectionResponse,
  EditKey,
  EditOperationAction,
  EditReadResponse,
  EditSheetMeta,
  EditStateResponse,
  EditStructure,
  EditValue,
  EditValueKind,
  GeneratedArea,
} from '../contracts/edit.ts';
import type { EditLimits } from '../contracts/formats.ts';
import type { CellData, SelectedCell } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import {
  byId,
  requireAnchor,
  requireButton,
  requireEl,
  requireSelect,
  requireTextArea,
} from '../core/dom.ts';
import type { ClipboardSnapshot } from '../grid/grid-clipboard.ts';
import {
  buildGridValue,
  decodeEditText,
  encodeEditText,
  parseEditTarget,
  type EditGridInputKind,
} from './edit-copy-model.ts';
import { FillHandle } from './edit-fill-handle.ts';
import {
  WorksheetFormulaEntry,
  type FormulaEntryDraft,
  type FormulaEntrySelectedCell,
  type ReferenceHighlightArea,
} from './edit-formula-entry.ts';
import { GridValidationList } from './edit-grid-list.ts';
import type { IdentifiedClipboard } from './edit-grid-paste.ts';
import { GridRangeEditor } from './edit-grid-range.ts';
import type { DragPointKind, DragPointResult } from './edit-reference-view.ts';
import { closeFormula, displayFormula, type FormulaPoint } from './formula-entry-model.ts';

export interface SheetViewportRect {
  readonly left: number;
  readonly right: number;
  readonly top: number;
  readonly bottom: number;
}

export interface SheetCellRect extends SheetViewportRect {
  readonly viewport: SheetViewportRect;
}

export interface SheetCellBounds {
  readonly rect: SheetCellRect;
  readonly node: HTMLElement;
}

export interface SheetEditorOrigin {
  readonly range: SelectionArea | null;
  readonly multi: SelectionArea[];
  readonly anchor: CellPoint | null;
  readonly filter: string;
  readonly view: string | undefined;
}

export interface SheetEditorDraft extends FormulaEntryDraft {
  readonly revision: string;
  initial: string;
  escaped: boolean;
  kind: EditValueKind;
  readonly origin: SheetEditorOrigin;
  ready?: Promise<boolean>;
}

export interface SheetEditorBridge {
  selected(): SelectedCell | null;
  ready(): boolean;
  filtered(): boolean;
  mappedRows(): boolean;
  clipboardIdentity(): string;
  paintReferences(): void;
  selectArea(area: ReferenceHighlightArea): void;
  captureOrigin(): SheetEditorOrigin;
  restoreOrigin(draft: SheetEditorDraft): Promise<boolean>;
  dragPoint(x: number, y: number, kind: DragPointKind, scroll?: boolean): DragPointResult;
  referenceStep(
    base: FormulaEntrySelectedCell,
    key: string,
  ): FormulaPoint | null | Promise<FormulaPoint | null>;
  dragged(): boolean;
  selectPoint(node: HTMLElement, event: MouseEvent): void;
  range(): string;
  merged(): MergeRange | null;
  areas(): readonly SelectionArea[];
  expand(area: SelectionArea): SelectionArea;
  mergeAt(row: number, col: number): MergeRange | null;
  nextRow(row: number): number;
  previousRow(row: number): number;
  nextCol(col: number): number;
  previousCol(col: number): number;
  moveInside(row: number, col: number): Promise<void>;
  switchSheet(sheet: string): Promise<void> | void;
  pasteRows(count: number): Promise<readonly number[]>;
  visibleSelection(): ClipboardSnapshot;
  move(deltaRow: number, deltaCol: number, extend?: boolean): Promise<void> | void;
  select(ref: string, visual?: number, force?: boolean): void;
  copy(plain?: boolean, cut?: boolean): Promise<boolean | undefined>;
  positionMenu(menu: HTMLElement, x: number, y: number): void;
  bounds(ref: string): SheetCellBounds | null;
  redraw(): void;
}

export interface SheetEditorOwner {
  readonly api: ApiClient;
  readonly limits: EditLimits;
  data: EditStateResponse | null;
  readonly pending: unknown;
  readonly abort: AbortController | null;
  invalidated: boolean;
  readonly tools: {
    tableAt(cell: SelectedCell | null): unknown;
    pivotAt(cell: SelectedCell | null): unknown;
  };
  context(): { readonly book: string; readonly sheet: string | null; readonly ref?: string };
  colName(col: number): string;
  post<T = unknown>(command: string, body: unknown, signal?: AbortSignal): Promise<T>;
  run(
    message: string,
    action: (signal: AbortSignal) => Promise<void>,
    cancellable?: boolean,
  ): Promise<boolean>;
  commit(
    action?: EditOperationAction,
    changes?: readonly EditChange[],
    options?: {
      readonly proof?: string;
      readonly direct?: boolean;
      readonly structure?: EditStructure;
      readonly inferNumberFormat?: boolean;
      readonly entryAnchor?: EditAddress;
      readonly expandTable?: boolean;
    },
  ): Promise<boolean | undefined>;
  save(): Promise<boolean | undefined> | undefined;
  exitGrid(): Promise<boolean> | boolean;
  advanced(tool?: string): unknown;
  bindGridBase(
    data: EditStateResponse,
    signal: AbortSignal,
    preferredSheet?: string | null,
    ref?: string,
  ): Promise<boolean>;
}

type SheetEditorQueueTask =
  | { readonly navigate: () => Promise<void> | void }
  | { readonly paste: string; readonly typed: IdentifiedClipboard | null }
  | { readonly text: string }
  | { readonly key: string; readonly shift: boolean };

function keyOf({ id, binding }: EditKey): EditKey {
  return { id, binding };
}

function textOf(cell: SelectedCell | null | undefined): string {
  return (cell?.hasFormula ? displayFormula(cell.formula ?? '') : cell?.raw) ?? '';
}

const CELL_TYPE_KINDS: Readonly<Record<string, EditValueKind>> = {
  s: 'text',
  inlineStr: 'text',
  str: 'text',
  text: 'text',
  b: 'boolean',
  d: 'date',
  e: 'error',
};

function kindOf(cell: SelectedCell | null | undefined): EditValueKind {
  if (cell?.hasFormula) return 'formula';
  const mapped = cell?.type ? CELL_TYPE_KINDS[cell.type] : undefined;
  if (mapped) return mapped;
  return cell?.raw ? 'number' : 'blank';
}

function readGridKind(): EditGridInputKind {
  const value = requireSelect('editGridKind').value;
  if (
    value === 'auto' ||
    value === 'text' ||
    value === 'number' ||
    value === 'boolean' ||
    value === 'formula' ||
    value === 'blank' ||
    value === 'date' ||
    value === 'error' ||
    value === 'missing'
  ) {
    return value;
  }
  return 'auto';
}

function httpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

const ALIGN_MAP: Readonly<Record<string, string>> = {
  center: 'center',
  'flex-end': 'right',
  'flex-start': 'left',
};

export class SheetEditor {
  public readonly owner: SheetEditorOwner;
  public readonly view: SheetEditorBridge;
  public readonly ranges: GridRangeEditor;
  public readonly input: HTMLTextAreaElement;
  public readonly formula: HTMLTextAreaElement;
  private readonly measure: HTMLElement;
  public readonly queue: SheetEditorQueueTask[] = [];
  public readonly region: HTMLElement;
  public readonly entry: WorksheetFormulaEntry;
  public readonly fillHandle: FillHandle;
  public readonly choices: GridValidationList;
  private readonly statusNode: HTMLElement;

  public book = '';
  public stale = false;
  public moving = false;
  public composing = false;
  public rebinding = false;
  public selecting = false;
  public draft: SheetEditorDraft | null = null;
  private patches: Map<string, Map<string, CellData>> | null = null;
  private calculated: Map<string, Map<string, CellData | null>> | null = null;
  private affected: Set<string> | null = null;
  private generated: Map<string, GeneratedArea[]> | null = null;
  private frame = 0;
  private measuredText: string | null = null;

  private getDraft(): SheetEditorDraft | null {
    return this.draft;
  }

  constructor(owner: SheetEditorOwner, bridge: SheetEditorBridge) {
    this.owner = owner;
    this.view = bridge;
    this.ranges = new GridRangeEditor(this);
    this.input = requireTextArea('editGridInput');
    this.formula = requireTextArea('editGridFormula');
    this.measure = requireEl('editGridMeasure');
    const region = document.querySelector('.grid-region');
    if (!(region instanceof HTMLElement)) {
      throw new Error('Missing required DOM element: .grid-region');
    }
    this.region = region;
    this.entry = new WorksheetFormulaEntry(this);
    this.fillHandle = new FillHandle(this);
    this.choices = new GridValidationList(this);
    this.statusNode = requireEl('editGridStatus');

    for (const field of [this.input, this.formula]) {
      field.addEventListener('beforeinput', (event: InputEvent) => {
        if (!this.active || this.moving) return;
        if (!this.draft && !this.begin(field === this.input, field)) {
          event.preventDefault();
          return;
        }
        this.entry.beforeInput(event, field);
      });
      field.addEventListener('compositionstart', () => {
        if (!this.draft && !this.moving) this.begin(field === this.input, field);
        if (this.entry.enabled) this.entry.record(field);
        this.composing = true;
      });
      field.addEventListener('compositionend', () => {
        this.composing = false;
        if (this.draft) this.entry.changed(field);
      });
      field.addEventListener('input', () => {
        if (!this.draft) return;
        this.draft.text = field.value;
        this.draft.typed = true;
        (field === this.input ? this.formula : this.input).value = field.value;
        this.entry.changed(field);
        this.schedulePosition();
      });
    }
    this.formula.addEventListener('focus', () => {
      if (this.active && !this.draft && !this.moving) this.begin(false, this.formula);
      if (this.draft) this.draft.field = this.formula;
    });
    requireSelect('editGridKind').addEventListener('change', () => {
      if (!this.draft) this.begin(false, this.input);
      this.focus();
      if (this.draft) this.entry.changed(this.entry.field);
    });
    requireButton('editGridUndo').onclick = (): void => {
      void this.history('undo');
    };
    requireButton('editGridRedo').onclick = (): void => {
      void this.history('redo');
    };
    requireButton('editGridSave').onclick = (): void => {
      void this.act(() => owner.save());
    };
    requireButton('editGridExit').onclick = (): void => {
      void this.act(() => owner.exitGrid());
    };
    requireButton('editGridMore').onclick = (): void => {
      void this.act(() => {
        owner.advanced();
        return undefined;
      });
    };
    requireButton('editGridRange').onclick = (): void => {
      void this.act(() => {
        owner.advanced('Range');
        return undefined;
      });
    };
    requireButton('editGridRefresh').onclick = (): void => {
      void this.reload();
    };
    requireButton('editGridRetry').onclick = (): void => {
      void this.act(null);
    };
    this.region.addEventListener(
      'pointerdown',
      (e: PointerEvent) => {
        if (this.active && this.entry.begin(e)) {
          e.stopImmediatePropagation();
          e.preventDefault();
          return;
        }
        const target = e.target;
        if (!this.active || !this.blocked || !(target instanceof Element) || !target.closest('.grid-cell')) {
          return;
        }
        // Resolve the clicked source coordinate on click, after the previous
        // draft has been confirmed. The reader's drag must not change it first.
        e.stopImmediatePropagation();
        e.preventDefault();
      },
      true,
    );
    this.region.addEventListener(
      'click',
      (e: MouseEvent) => {
        if (this.active && this.entry.handledClick) {
          this.entry.handledClick = false;
          e.preventDefault();
          e.stopImmediatePropagation();
          return;
        }
        const target = e.target;
        const node = target instanceof Element ? target.closest('.grid-cell') : null;
        if (!this.active || !(node instanceof HTMLElement)) return;
        e.preventDefault();
        e.stopImmediatePropagation();
        if (
          e.detail === 0 &&
          this.view.ready() &&
          !this.moving &&
          !this.owner.pending &&
          !this.stale &&
          this.entry.canPoint()
        ) {
          const point = this.entry.point(node);
          if (point) {
            this.entry.writePoint(point, point);
            this.focus();
          }
          return;
        }
        if (this.view.dragged()) {
          this.focus();
          return;
        }
        if (this.view.ready()) {
          this.navigate(() => {
            this.view.selectPoint(node, e);
            if (e.detail > 1) this.begin(false, this.input);
            this.focus();
          });
        }
      },
      true,
    );
    this.region.addEventListener(
      'dblclick',
      (e: MouseEvent) => {
        const target = e.target;
        if (this.active && target instanceof Element && target.closest('.grid-cell')) {
          e.preventDefault();
          e.stopImmediatePropagation();
          if (!this.draft && !this.moving) this.begin(false, this.input);
        }
      },
      true,
    );
    this.region.addEventListener(
      'contextmenu',
      (e: MouseEvent) => {
        const target = e.target;
        if (
          this.active &&
          !(target instanceof Element && target.matches('textarea')) &&
          (this.blocked || !(target instanceof Element && target.closest('.grid-cell,.pinned-cell')))
        ) {
          e.stopImmediatePropagation();
          e.preventDefault();
        }
      },
      true,
    );
    requireEl('grid').addEventListener('scroll', () => this.schedulePosition(), { passive: true });
    new ResizeObserver(() => this.schedulePosition()).observe(this.region);
    document.addEventListener('keydown', (e: KeyboardEvent) => this.key(e), true);
    this.input.addEventListener('paste', (e: ClipboardEvent) => this.paste(e));
    this.input.addEventListener('copy', (e: ClipboardEvent) => {
      if (this.active && !this.draft) {
        e.preventDefault();
        if (!this.moving) void this.view.copy();
      }
    });
    this.input.addEventListener('cut', (e: ClipboardEvent) => {
      if (!this.active || this.draft) return;
      // System clipboard consumers cannot acknowledge a workbook move. Never
      // erase the source on cut, including when writing the clipboard fails.
      e.preventDefault();
      if (!this.moving) void this.copyForCut();
    });
  }

  get active(): boolean {
    return Boolean(this.book) && this.book === this.owner.context().book;
  }

  get selected(): SelectedCell | null {
    return this.view.selected();
  }

  get shape(): EditSheetMeta | undefined {
    return this.owner.data?.sheets.find((s) => s.name === this.owner.context().sheet);
  }

  get blocked(): boolean {
    return Boolean(this.draft || this.moving || this.owner.pending || this.owner.abort);
  }

  get dirty(): boolean {
    return (
      Boolean(this.draft) &&
      (this.draft?.text !== this.draft?.initial ||
        (Boolean(this.draft?.typed) && Boolean(this.draft?.text.startsWith('='))) ||
        requireSelect('editGridKind').value !== 'auto')
    );
  }

  async enter(data: EditStateResponse, signal?: AbortSignal): Promise<void> {
    this.book = this.owner.context().book;
    this.patches = new Map();
    try {
      await this.sync(undefined, signal);
    } catch (error: unknown) {
      this.leave();
      throw error;
    }
    document.body.classList.add('editing-copy');
    for (const id of ['editGridBar', 'editGridInput', 'editGridFormula'] as const) {
      requireEl(id).hidden = false;
    }
    const kindSelect = requireSelect('editGridKind');
    kindSelect.hidden = Boolean(data.encoding);
    const formulaOption = kindSelect.querySelector('[value="formula"]');
    if (formulaOption instanceof HTMLOptionElement) {
      formulaOption.disabled = Boolean(data.encoding);
    }
    this.view.redraw();
    this.selection();
    this.controls();
    this.focus();
  }

  leave(): void {
    if (this.fillHandle.drag) this.fillHandle.reset();
    this.choices.reset();
    this.ranges.copied = null;
    this.ranges.menu.hidden = true;
    this.book = '';
    this.patches = null;
    this.calculated = null;
    this.stale = false;
    if (!this.rebinding) this.queue.length = 0;
    this.cancelDraft();
    this.selecting = false;
    document.body.classList.remove('editing-copy');
    for (const id of ['editGridBar', 'editGridInput', 'editGridFormula', 'editGridKind'] as const) {
      requireEl(id).hidden = true;
    }
    this.view.redraw();
    requireEl('grid').focus({ preventScroll: true });
  }

  async sync(changes?: readonly EditChange[], signal?: AbortSignal): Promise<void> {
    const data = this.owner.data;
    if (!data) throw new Error('文件或修订已切换，请刷新修订');
    const doc = data.document;
    const book = this.book;
    const cells = changes?.map(({ sheet, row, col }) => ({ sheet, row, col }));
    const result = await this.owner.post<EditGridProjectionResponse>(
      'grid',
      { document: keyOf(doc), revision: doc.revision, book, cells, mode: 'projection' },
      signal,
    );
    const current = this.owner.data?.document;
    if (
      this.book !== book ||
      this.owner.context().book !== book ||
      current?.id !== doc.id ||
      current.binding !== doc.binding ||
      current.revision !== doc.revision
    ) {
      throw new Error('文件或修订已切换，请刷新修订');
    }
    const patches = this.patches ?? new Map<string, Map<string, CellData>>();
    this.patches = patches;
    if (!cells) patches.clear();
    for (const [sheet, values] of Object.entries(result.cells)) {
      let items = patches.get(sheet);
      if (!items) {
        items = new Map();
        patches.set(sheet, items);
      }
      for (const [ref, cell] of Object.entries(values)) {
        if (cell) items.set(ref, cell);
        else items.delete(ref);
      }
    }
    this.affected = new Set(Object.keys(result.affected ?? {}));
    this.calculated = new Map(
      Object.entries(result.calculated ?? {}).map(([sheet, items]) => [
        sheet,
        new Map(Object.entries(items)),
      ]),
    );
    this.generated = new Map();
    for (const area of result.generated ?? []) {
      const sheet = area.start.sheet;
      let list = this.generated.get(sheet);
      if (!list) {
        list = [];
        this.generated.set(sheet, list);
      }
      list.push(area);
    }
    this.choices.setRules(result.rules);
    this.stale = false;
    this.owner.invalidated = false;
    if (document.body.classList.contains('editing-copy')) this.view.redraw();
  }

  // Both painters ask by original row and column, including a missing source
  // cell that now has a value. Their immutable tile payloads stay reusable.
  project(sheet: string, ref: string, base: CellData): CellData;
  project(sheet: string, ref: string, base: CellData | null | undefined): CellData | null | undefined;
  project(
    sheet: string,
    ref: string,
    base: CellData | null | undefined,
  ): CellData | null | undefined {
    if (!this.active) return base;
    const patched = this.patches?.get(sheet)?.get(ref) ?? base;
    const calculated = this.calculated?.get(sheet)?.get(ref);
    if (calculated) {
      return {
        ...(patched ?? { col: calculated.col, ref, raw: '', display: '', hasFormula: false }),
        ...calculated,
        cacheMissing: Boolean(calculated.cacheMissing),
        richText: undefined,
        valueColor: calculated.valueColor || '',
        conditional: undefined,
      };
    }
    if (!patched) return patched;
    const areas = this.generated?.get(sheet);
    if (areas) {
      const row = Number(ref.slice(ref.search(/[0-9]/)));
      const col = patched.col;
      const area = areas.find(
        (a) =>
          row >= a.start.row &&
          row <= a.endRow &&
          col >= a.start.col &&
          col <= a.endCol &&
          (a.pivot || row !== a.start.row || col !== a.start.col),
      );
      if (area) {
        return {
          ...patched,
          raw: '',
          display: area.pivot ? '待刷新' : '待重算',
          richText: undefined,
          valueColor: '',
          conditional: undefined,
        };
      }
    }
    if (patched.hasFormula && this.affected?.has(sheet)) {
      return {
        ...patched,
        raw: '',
        display: '待重算',
        cacheMissing: true,
        richText: undefined,
        valueColor: '',
        conditional: undefined,
      };
    }
    return patched;
  }

  projectRow(
    sheet: string,
    row: { readonly row: number; readonly cells: readonly CellData[] },
    firstCol: number,
    lastCol: number,
  ): CellData[] {
    const source = new Map(row.cells.map((cell) => [cell.col, cell]));
    const cells: CellData[] = [];
    for (let col = firstCol; col <= lastCol; col++) {
      const ref = `${this.owner.colName(col)}${row.row}`;
      const cell = this.project(sheet, ref, source.get(col));
      if (cell) cells.push(cell);
    }
    return cells;
  }

  status(message: string, error = false): void {
    this.statusNode.textContent = message || this.summary();
    this.statusNode.classList.toggle('is-error', error);
  }

  summary(): string {
    const doc = this.owner.data?.document;
    return (
      this.shape?.readOnly ||
      (doc
        ? `修订 ${doc.revision} · ${doc.changed} 格修改${doc.structuresChanged ? ` · ${doc.structuresChanged} 项结构操作` : ''}`
        : '')
    );
  }

  controls(): void {
    if (!this.active || !this.owner.data) return;
    const data = this.owner.data;
    const doc = data.document;
    const busy = Boolean(this.owner.abort || this.moving);
    requireButton('editGridUndo').disabled =
      busy || !doc.undo || Boolean(this.owner.pending) || this.stale;
    requireButton('editGridRedo').disabled =
      busy || !doc.redo || Boolean(this.owner.pending) || this.stale;
    const saveBtn = requireButton('editGridSave');
    saveBtn.disabled = busy || Boolean(this.owner.pending) || this.stale;
    const moreBtn = requireButton('editGridMore');
    moreBtn.disabled = saveBtn.disabled || !this.view.ready();
    const rangeBtn = requireButton('editGridRange');
    rangeBtn.hidden = Boolean(data.encoding);
    rangeBtn.disabled = moreBtn.disabled || this.view.filtered();
    requireButton('editGridExit').disabled = busy || Boolean(this.owner.pending);
    const refreshBtn = requireButton('editGridRefresh');
    const retryBtn = requireButton('editGridRetry');
    refreshBtn.hidden = !this.stale;
    retryBtn.hidden = !this.owner.pending;
    retryBtn.disabled = busy;
    refreshBtn.disabled = busy;
    const output = data.artifacts.find((a) => a.revision === doc.revision);
    const link = requireAnchor('editGridDownload');
    link.hidden = !output;
    if (output) {
      link.href = output.downloadUrl;
      link.download = output.name;
    }
  }

  focus(): void {
    if (this.active && !document.querySelector('dialog:modal')) {
      (this.draft?.field ?? this.input).focus({ preventScroll: true });
    }
  }

  selection(): void {
    if (!this.active || this.draft) return;
    const cell = this.selected;
    const raw = textOf(cell);
    (byId('editGridMoreLabel') ?? requireButton('editGridMore')).textContent =
      this.owner.tools.pivotAt(cell) ? '透视布局' : '更多编辑';
    this.formula.value = encodeEditText(raw, raw.includes('\r'));
    this.input.value = '';
    requireSelect('editGridKind').value = 'auto';
    if (!this.stale && !this.owner.pending) this.status('');
    this.choices.selection();
    this.schedulePosition();
  }

  schedulePosition(): void {
    if (
      this.active &&
      (this.draft || !this.choices.button.hidden || this.choices.tree?.root) &&
      !this.frame
    ) {
      this.frame = requestAnimationFrame(() => {
        this.frame = 0;
        this.position();
        this.choices.position();
        this.entry.completion.position();
      });
    }
  }

  position(): void {
    const draft = this.draft;
    if (!draft) return;
    const found =
      draft.address.sheet === this.owner.context().sheet ? this.view.bounds(draft.ref) : null;
    this.input.style.opacity = found ? '' : '0';
    this.input.style.pointerEvents = found ? '' : 'none';
    if (!found) return;
    const { rect, node } = found;
    const view = rect.viewport;
    const region = this.region.getBoundingClientRect();
    const style = getComputedStyle(node);
    const cellWidth = rect.right - rect.left;
    const cellHeight = rect.bottom - rect.top;
    const font = [style.fontStyle, style.fontWeight, `${style.fontSize}/1.25`, style.fontFamily].join(
      ' ',
    );
    const formula = draft.text.startsWith('=');
    const align = formula ? 'left' : ALIGN_MAP[style.justifyContent] || style.textAlign;
    const wrapped = !formula && ['pre-wrap', 'normal'].includes(style.whiteSpace);
    const limitWidth = wrapped ? cellWidth : view.right - view.left;
    // Let the browser shape the exact font, tabs, CJK and explicit breaks.
    // A final zero-width space gives an empty last line its caret height.
    Object.assign(this.measure.style, {
      font,
      minWidth: `${cellWidth}px`,
      maxWidth: `${limitWidth}px`,
      maxHeight: `${view.bottom - view.top}px`,
    });
    if (this.measuredText !== draft.text) {
      this.measure.textContent = `${draft.text}\u200b`;
      this.measuredText = draft.text;
    }
    const measured = this.measure.getBoundingClientRect();
    const width = Math.min(limitWidth, Math.max(cellWidth, Math.ceil(measured.width)));
    const height = Math.min(view.bottom - view.top, Math.max(cellHeight, Math.ceil(measured.height)));
    const extra = Math.max(0, height - measured.height);
    const factor = align === 'center' ? 0.5 : align === 'right' || align === 'end' ? 1 : 0;
    const left = Math.max(view.left, Math.min(view.right - width, rect.left - (width - cellWidth) * factor));
    const top = Math.max(view.top, Math.min(view.bottom - height, rect.top));
    Object.assign(this.input.style, {
      left: `${left - region.left}px`,
      top: `${top - region.top}px`,
      width: `${width}px`,
      height: `${height}px`,
      font,
      textAlign: align,
      paddingTop: `${style.alignItems === 'flex-end' ? extra : style.alignItems === 'center' ? extra / 2 : 0}px`,
    });
  }

  begin(replace: boolean, field: HTMLTextAreaElement): boolean {
    if (this.draft) return true;
    const cell = this.selected;
    const shape = this.shape;
    const doc = this.owner.data?.document;
    try {
      if (!this.active || !cell || !this.view.ready() || this.stale || this.owner.pending || !doc) {
        throw new Error('请先核对当前修订');
      }
      if (this.ranges.copied?.cut) this.ranges.cancelCut();
      const address = parseEditTarget(cell.ref, shape);
      if (shape?.readOnly) throw new Error(shape.readOnly);
      const raw = textOf(cell);
      const escaped = raw.includes('\r');
      const initial = encodeEditText(raw, escaped);
      const d: SheetEditorDraft = {
        address,
        ref: cell.ref,
        revision: doc.revision,
        initial,
        escaped,
        text: replace ? '' : initial,
        kind: kindOf(cell),
        field,
        pointKeys: replace,
        origin: this.view.captureOrigin(),
      };
      this.draft = d;
      this.choices.dismiss();
      this.choices.position();
      this.input.value = d.text;
      this.formula.value = d.text;
      this.input.classList.add('writing');
      this.position();
      if (field === this.input) {
        this.focus();
        this.input.setSelectionRange(d.text.length, d.text.length);
      }
      this.entry.changed(field);
      const request = { document: keyOf(doc), revision: doc.revision, sheet: address.sheet, ref: d.ref };
      d.ready = this.owner
        .post<EditReadResponse>('read', request)
        .then((source) => {
          if (this.draft !== d) return false;
          if (source.readOnly || (this.owner.data?.encoding && source.value.kind === 'missing')) {
            this.cancelDraft();
            this.status(source.readOnly || '该记录缺少此列', true);
            return false;
          }
          const unchanged = !replace && d.text === d.initial;
          d.kind = source.value.kind;
          const actual =
            source.value.kind === 'formula' ? displayFormula(source.value.raw) : source.value.raw;
          d.escaped = actual.includes('\r');
          d.initial = encodeEditText(actual, d.escaped);
          if (unchanged && d.text !== d.initial) {
            d.text = d.initial;
            this.input.value = d.text;
            this.formula.value = d.text;
            this.entry.changed(d.field);
            this.schedulePosition();
          }
          return true;
        })
        .catch((error: unknown) => {
          if (this.draft === d) {
            this.stale = true;
            this.controls();
            this.status(errorMessage(error), true);
          }
          return false;
        });
      if (escaped) this.status('格内回车显示为 \\r，反斜线显示为 \\\\');
      return true;
    } catch (error: unknown) {
      this.status(errorMessage(error), true);
      return false;
    }
  }

  cancelDraft(): void {
    this.draft = null;
    this.composing = false;
    this.entry.reset();
    this.measuredText = null;
    this.measure.textContent = '';
    this.input.classList.remove('writing');
    this.input.removeAttribute('style');
    this.selection();
  }

  async returnToDraft(d: SheetEditorDraft | null): Promise<boolean> {
    return !d || d.address.sheet === this.owner.context().sheet || this.view.restoreOrigin(d);
  }

  draftValue(d: SheetEditorDraft): EditValue {
    const text = this.entry.enabled ? closeFormula(d.text) : d.text;
    return buildGridValue(
      decodeEditText(text, d.escaped),
      readGridKind(),
      d.kind,
      Boolean(this.owner.data?.encoding),
      this.owner.limits,
    );
  }

  async finish(): Promise<boolean> {
    const d = this.draft;
    if (this.owner.pending) {
      if (!(await this.owner.commit(undefined, undefined, { direct: true }))) return false;
      this.cancelDraft();
      return true;
    }
    if (!d) return !this.stale;
    if (this.entry.pendingStep && (await this.entry.pendingStep) === false) return false;
    if (!(await this.returnToDraft(d)) || this.draft !== d) return false;
    if (!this.dirty) {
      this.cancelDraft();
      return true;
    }
    if (!(await d.ready) || this.draft !== d || this.stale) return false;
    const value = this.draftValue(d);
    const initial = decodeEditText(d.initial, d.escaped);
    if (d.kind !== value.kind || initial !== value.raw || (d.typed && value.kind === 'formula')) {
      const saved = await this.owner.commit('change', [{ ...d.address, value }], {
        direct: true,
        inferNumberFormat: true,
        expandTable: !this.view.filtered(),
      });
      if (!saved) {
        if (this.owner.data?.document.revision !== d.revision) this.cancelDraft();
        return false;
      }
    }
    this.cancelDraft();
    return true;
  }

  navigate(next: () => Promise<void> | void): void {
    if (!this.moving) {
      void this.act(async () => {
        await next();
        return undefined;
      });
      return;
    }
    const data = this.owner.data;
    if (!data) return;
    // Keep explicit cell/address navigation in input order while a long
    // operation is running. A paste still preparing must relinquish its target.
    const documentId = data.document.id;
    const sheet = this.owner.context().sheet;
    this.queue.push({
      navigate: () => {
        if (
          this.active &&
          this.owner.data?.document.id === documentId &&
          this.owner.context().sheet === sheet
        ) {
          return next();
        }
        return undefined;
      },
    });
  }

  async act(
    next?: ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined) | null,
    confirmDraft = true,
    label = '',
  ): Promise<boolean> {
    if (this.moving || this.composing) return false;
    this.moving = true;
    this.controls();
    const activity = label ? beginActivity(label, { priority: 0 }) : null;
    let ok = false;
    try {
      if (confirmDraft && !(await this.finish())) return false;
      if (next && (await next(activity)) === false) return false;
      ok = true;
      return true;
    } catch (error: unknown) {
      if (httpStatus(error) === 409) this.stale = true;
      this.status(errorMessage(error), true);
      return false;
    } finally {
      activity?.end();
      this.moving = false;
      this.controls();
      if (!ok) {
        const navigation = this.queue.findLast((task) => 'navigate' in task);
        this.queue.length = 0;
        if (navigation) this.queue.push(navigation);
      }
      if (this.queue.length) this.drain();
    }
  }

  async reload(): Promise<void> {
    if (this.moving || this.owner.pending || !this.owner.data) return;
    const d = this.draft;
    const kind = requireSelect('editGridKind').value;
    await this.owner.run('正在刷新修订…', async (signal) => {
      if (!this.owner.data) return;
      const nextData = await this.owner.post<EditStateResponse>(
        'state',
        { document: keyOf(this.owner.data.document) },
        signal,
      );
      this.owner.data = nextData;
      const { sheet, ref } = this.owner.context();
      if (await this.owner.bindGridBase(nextData, signal, sheet, ref)) {
        await this.enter(nextData, signal);
        return;
      }
      await this.sync(undefined, signal);
      this.cancelDraft();
      if (d && this.begin(false, this.input)) {
        await this.draft?.ready;
        if (this.draft) {
          this.draft.text = d.text;
          this.input.value = d.text;
          this.formula.value = d.text;
          requireSelect('editGridKind').value = kind;
          this.status('已刷新修订，请核对输入后按 Enter');
        }
      } else {
        this.selection();
      }
    });
    this.focus();
  }

  history(action: 'undo' | 'redo'): Promise<boolean> {
    return this.act(async () => {
      const ok = await this.owner.commit(action, undefined, { direct: true });
      if (ok) {
        this.selection();
        this.focus();
      }
      return ok;
    });
  }

  async copyForCut(): Promise<void> {
    if (this.owner.data?.encoding) {
      this.status('整块剪切需要 Excel 工作簿副本', true);
      return;
    }
    if (await this.view.copy(false, true)) this.status('选择粘贴位置，Esc 取消剪切');
  }

  applySelectionEntry(): Promise<boolean> {
    const draft = this.draft;
    return this.act(
      async () => {
        if (!draft) return true;
        if (this.entry.pendingStep && (await this.entry.pendingStep) === false) return false;
        if (!(await draft.ready) || this.draft !== draft || this.stale) return false;
        if (!(await this.returnToDraft(draft)) || this.draft !== draft) return false;
        const value = this.draftValue(draft);
        const saved = await this.ranges.fill('fill-entry', { ...draft.address, value });
        if (saved) {
          this.cancelDraft();
          this.focus();
        }
        return saved;
      },
      false,
      '正在填充选区',
    );
  }

  private paste(e: ClipboardEvent): void {
    if (!this.active || this.draft || !e.clipboardData) return;
    const raw = e.clipboardData.getData('text/plain');
    const typed = this.ranges.typedClipboard(raw, e.clipboardData.getData('text/html'));
    e.preventDefault();
    if (this.moving) {
      this.queue.push({ paste: raw, typed });
      return;
    }
    void this.pasteText(raw, typed);
  }

  pasteText(raw: string, typed: IdentifiedClipboard | null): Promise<boolean> | undefined {
    if (!this.selected || (!raw && !typed)) return undefined;
    return this.act(
      async (activity) => {
        const ok = await this.ranges.paste(raw, typed, activity);
        this.focus();
        return ok;
      },
      true,
      '正在粘贴选区',
    );
  }

  private drain(): void {
    while (this.active && !this.moving && this.queue.length) {
      const task = this.queue.shift();
      if (!task) break;
      if ('navigate' in task) {
        void this.act(async () => {
          await task.navigate();
          return undefined;
        });
      } else if ('paste' in task) {
        void this.pasteText(task.paste, task.typed);
      } else if ('text' in task) {
        if (!this.draft && !this.begin(true, this.input)) {
          this.queue.length = 0;
          return;
        }
        if (this.draft) {
          this.draft.text += task.text;
          this.input.value = this.draft.text;
          this.formula.value = this.draft.text;
          this.entry.changed(this.input);
          this.schedulePosition();
        }
      } else {
        void this.step(task.key, task.shift);
      }
    }
  }

  step(key: string, shift: boolean): Promise<boolean> {
    const deltas: Readonly<Record<string, readonly [number, number]>> = {
      Tab: [0, shift ? -1 : 1],
      Enter: [shift ? -1 : 1, 0],
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
    };
    const baseDelta = deltas[key];
    if (!baseDelta) return Promise.resolve(false);
    const delta: [number, number] = [baseDelta[0], baseDelta[1]];
    const entry = key === 'Enter' && !shift && this.dirty ? this.draft?.address ?? null : null;
    return this.act(async () => {
      if (!(['Tab', 'Enter'].includes(key) && (await this.ranges.cycle(key, shift)))) {
        // Appending below a totals row places the value one row above the
        // input address. Continue below the relocated total for the next entry.
        const cell = this.selected;
        if (entry && entry.sheet === this.shape?.name && cell?.col === entry.col && cell.row + 1 === entry.row) {
          delta[0]++;
        }
        await this.view.move(delta[0], delta[1], shift && key.startsWith('Arrow'));
      }
      this.focus();
      return undefined;
    });
  }

  private key(e: KeyboardEvent): void {
    if (!(this.active || (this.rebinding && this.moving)) || document.querySelector('dialog:modal')) {
      return;
    }
    const field = e.target;
    const isInput = field === this.input;
    const isFormula = field === this.formula;
    const inGrid = isInput || isFormula || field === requireEl('grid');
    if (this.composing || isComposing(e)) return;
    const take = (): void => {
      e.preventDefault();
      e.stopImmediatePropagation();
    };
    const ctrl = e.ctrlKey || e.metaKey;
    const key = e.key;
    const lower = key.toLowerCase();
    if (ctrl && !e.altKey && lower === 's') {
      take();
      void this.act(() => this.owner.save());
      return;
    }
    if (!inGrid) return;
    if (!this.draft && (key === 'ContextMenu' || (e.shiftKey && key === 'F10'))) {
      take();
      const rect = this.selected ? this.view.bounds(this.selected.ref)?.rect : undefined;
      if (rect) this.ranges.showMenu(rect.left, rect.bottom);
      return;
    }
    if (!this.draft && !ctrl && e.altKey && key === 'ArrowDown') {
      take();
      void this.choices.open();
      return;
    }
    if (this.moving) {
      if (
        !ctrl &&
        !e.altKey &&
        (key.length === 1 || ['Tab', 'Enter', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(key))
      ) {
        take();
        this.queue.push(key.length === 1 ? { text: key } : { key, shift: e.shiftKey });
      } else if (!(ctrl && lower === 'v')) {
        take();
      }
      return;
    }
    if (this.entry.key(e)) {
      take();
      return;
    }
    if (key === 'Escape') {
      take();
      if (!this.owner.pending) {
        void this.act(async () => {
          if (!(await this.returnToDraft(this.draft))) return false;
          this.cancelDraft();
          this.ranges.cancelCut();
          this.focus();
          return undefined;
        }, false);
      }
      return;
    }
    if (ctrl && !e.altKey && !e.shiftKey && !this.draft && lower === 'c') {
      take();
      void this.view.copy();
      return;
    }
    if (ctrl && !e.altKey && !e.shiftKey && !this.draft && lower === 'x') {
      take();
      void this.copyForCut();
      return;
    }
    if (ctrl && !e.altKey && !e.shiftKey && !this.draft && ['d', 'r'].includes(lower)) {
      take();
      void this.act(
        () => this.ranges.fill(lower === 'd' ? 'fill-down' : 'fill-right'),
        true,
        '正在填充选区',
      );
      return;
    }
    if (ctrl && !e.altKey && !e.shiftKey && key === 'Enter') {
      take();
      void this.applySelectionEntry();
      return;
    }
    if (ctrl && !e.altKey && !this.draft && ['z', 'y'].includes(lower)) {
      take();
      void this.history(lower === 'y' || e.shiftKey ? 'redo' : 'undo');
      return;
    }
    if (key === 'Enter' && e.altKey && this.draft && field instanceof HTMLTextAreaElement) {
      take();
      field.setRangeText('\n', field.selectionStart, field.selectionEnd, 'end');
      field.dispatchEvent(new Event('input'));
      return;
    }
    if (!ctrl && !e.altKey && (key === 'Enter' || key === 'Tab' || (!this.draft && key.startsWith('Arrow')))) {
      take();
      void this.step(key, e.shiftKey);
      return;
    }
    if (this.draft) return;
    if (key === 'F2') {
      take();
      this.begin(false, this.input);
      return;
    }
    if (key === 'Delete') {
      take();
      void this.act(() => this.ranges.clear(), true, '正在清除选区');
      return;
    }
    if (key === 'Backspace') {
      take();
      this.begin(true, this.input);
      return;
    }
    if (!ctrl && !e.altKey && key.length === 1 && !isFormula) {
      take();
      if (this.begin(true, this.input)) {
        const startedDraft = this.getDraft();
        if (startedDraft) {
          if (key === '=' && this.entry.allowsFormula) this.entry.record(this.input);
          startedDraft.text = key;
          this.input.value = key;
          this.formula.value = key;
          this.input.setSelectionRange(key.length, key.length);
          this.schedulePosition();
          this.entry.changed(this.input);
        }
      }
    }
  }
}
