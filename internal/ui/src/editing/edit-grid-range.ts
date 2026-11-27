/**
 * Multi-cell range editing operations for the live editable worksheet grid:
 * range context menu, Tab/Enter selection cycling, clear, fill down/right/entry,
 * cut-source marching border, typed/portable/TSV clipboard copy & paste.
 */
import type { ActivityHandle, HttpError, MergeRange, SelectionArea } from '../contracts/common.ts';
import type {
  EditAddress,
  EditBatchPreviewResponse,
  EditChange,
  EditClipboard,
  EditSheetMeta,
  EditStructure,
} from '../contracts/edit.ts';
import type { EditLimits } from '../contracts/formats.ts';
import type { SelectedCell } from '../contracts/workbook.ts';
import { requireEl } from '../core/dom.ts';
import { parseClipboardTSV } from '../grid/clipboard.ts';
import type { ClipboardSnapshot } from '../grid/grid-clipboard.ts';
import { boundedChanges, buildGridValue, parseEditTarget } from './edit-copy-model.ts';
import {
  fetchNativePaste,
  identifyPasteClipboard,
  isEditClipboard,
  pastePortableRange,
  readPortableFromClip,
  resolvePasteDestination,
  type EditCopiedSnapshot,
  type GridPasteRect,
  type GridPasteTarget,
  type IdentifiedClipboard,
} from './edit-grid-paste.ts';

export type GridRangeBounds = GridPasteRect;

export interface CutSourceCellElement extends HTMLElement {
  _xlCutMask?: number;
}

export interface GridRangeEditorView {
  ready(): boolean;
  filtered(): boolean;
  mappedRows(): boolean;
  clipboardIdentity(): string;
  areas(): readonly SelectionArea[];
  expand(area: SelectionArea): SelectionArea;
  mergeAt(row: number, col: number): MergeRange | null;
  nextRow(row: number): number;
  previousRow(row: number): number;
  nextCol(col: number): number;
  previousCol(col: number): number;
  moveInside(row: number, col: number): Promise<void>;
  visibleSelection(): ClipboardSnapshot;
  pasteRows(count: number): Promise<readonly number[]>;
  copy(plain?: boolean, cut?: boolean): Promise<boolean | undefined>;
  positionMenu(menu: HTMLElement, x: number, y: number): void;
  redraw(): void;
}

export interface GridRangeEditorOwner {
  readonly limits: EditLimits;
  readonly pending: unknown;
  readonly data: {
    readonly encoding?: string;
    readonly document: {
      readonly id: string;
      readonly binding: string;
      readonly revision: string;
    };
  } | null;
  readonly tools: {
    tableAt(cell: SelectedCell | null): unknown;
  };
  colName(col: number): string;
  advanced(tool?: string): unknown;
  post<T = unknown>(command: string, body: unknown, signal?: AbortSignal): Promise<T>;
  commit(
    action: 'change' | 'structure',
    changes: readonly EditChange[] | undefined,
    options?: {
      readonly proof?: string;
      readonly direct?: boolean;
      readonly structure?: EditStructure;
      readonly inferNumberFormat?: boolean;
      readonly entryAnchor?: EditAddress;
      readonly expandTable?: boolean;
    },
  ): Promise<boolean | undefined>;
}

export interface GridRangeEditorHost {
  readonly active: boolean;
  readonly blocked: boolean;
  readonly stale: boolean;
  readonly selected: SelectedCell | null;
  readonly shape: EditSheetMeta | undefined;
  readonly queue: readonly object[];
  readonly owner: GridRangeEditorOwner;
  readonly view: GridRangeEditorView;
  focus(): void;
  status(message: string, error?: boolean): void;
  copyForCut(): Promise<void>;
  act(
    next: ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined) | null,
    confirmDraft?: boolean,
    label?: string,
  ): Promise<boolean>;
}

function bounds(area: SelectionArea): GridRangeBounds {
  return {
    top: Math.min(area.row1, area.row2),
    bottom: Math.max(area.row1, area.row2),
    left: Math.min(area.col1, area.col2),
    right: Math.max(area.col1, area.col2),
  };
}

function httpStatus(error: unknown): number | undefined {
  if (typeof error === 'object' && error !== null && 'status' in error) {
    const status = (error as HttpError).status;
    if (typeof status === 'number') return status;
  }
  return undefined;
}

function isCopiedSnapshot(
  value: IdentifiedClipboard | null | undefined,
): value is EditCopiedSnapshot {
  return Boolean(value && 'marker' in value);
}

export class GridRangeEditor {
  public readonly grid: GridRangeEditorHost;
  public readonly owner: GridRangeEditorOwner;
  public readonly view: GridRangeEditorView;
  public readonly menu: HTMLElement;
  public copied: EditCopiedSnapshot | null = null;
  public copyEpoch = 0;

  constructor(editor: GridRangeEditorHost) {
    this.grid = editor;
    this.owner = editor.owner;
    this.view = editor.view;
    this.menu = requireEl('editGridMenu');
    this.menu.addEventListener('click', (event: MouseEvent) => {
      const target = event.target;
      const button = target instanceof Element ? target.closest('button') : null;
      const action = button instanceof HTMLButtonElement ? button.dataset.editAction : undefined;
      if (!action) return;
      this.menu.hidden = true;
      editor.focus();
      if (action === 'cut') void editor.copyForCut();
      if (action === 'copy') void this.view.copy();
      if (action === 'resize-table') this.owner.advanced('Table');
      if (action === 'paste') {
        void editor.act((activity) => this.pasteClipboard(activity), true, '正在粘贴选区');
      }
      if (action === 'clear') {
        void editor.act(() => this.clear(), true, '正在清除选区');
      }
      if (action === 'down' || action === 'right') {
        void editor.act(() => this.fill(`fill-${action}`), true, '正在填充选区');
      }
    });
    this.menu.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        this.menu.hidden = true;
        editor.focus();
      } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        const buttons = [
          ...this.menu.querySelectorAll<HTMLButtonElement>('button:enabled:not([hidden])'),
        ];
        if (!buttons.length) return;
        const active =
          document.activeElement instanceof HTMLButtonElement ? document.activeElement : null;
        const i = active ? buttons.indexOf(active) : -1;
        const next = buttons[(i + buttons.length + (event.key === 'ArrowDown' ? 1 : -1)) % buttons.length];
        next?.focus();
      } else {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
    });
    this.menu.addEventListener('focusout', (event: FocusEvent) => {
      const related = event.relatedTarget;
      if (!(related instanceof Node) || !this.menu.contains(related)) this.menu.hidden = true;
    });
  }

  showMenu(x: number, y: number): void {
    if (this.grid.blocked || this.grid.stale) return;
    const cutBtn = this.menu.querySelector('[data-edit-action="cut"]');
    if (cutBtn instanceof HTMLButtonElement) {
      cutBtn.disabled =
        Boolean(this.owner.data?.encoding) ||
        this.view.filtered() ||
        this.view.areas().length !== 1;
    }
    for (const action of ['down', 'right'] as const) {
      const btn = this.menu.querySelector(`[data-edit-action="${action}"]`);
      if (btn instanceof HTMLButtonElement) {
        btn.disabled = this.view.filtered() || this.view.areas().length !== 1;
      }
    }
    const resizeBtn = this.menu.querySelector('[data-edit-action="resize-table"]');
    if (resizeBtn instanceof HTMLElement) {
      resizeBtn.hidden = !this.owner.tools.tableAt(this.grid.selected);
    }
    this.view.positionMenu(this.menu, x, y);
    const firstBtn = this.menu.querySelector('button');
    if (firstBtn instanceof HTMLElement) firstBtn.focus({ preventScroll: true });
  }

  typedClipboard(raw: string, html: string): IdentifiedClipboard | null {
    return identifyPasteClipboard(raw, html, this.copied, this.owner.limits.requestBytes);
  }

  async pasteClipboard(activity?: ActivityHandle | null): Promise<boolean | undefined> {
    const target = this.pasteTarget();
    const selection = this.rectangle(true);
    let raw = '';
    let html = '';
    try {
      if (typeof navigator.clipboard.read === 'function') {
        for (const item of await navigator.clipboard.read()) {
          if (!item.types.includes('text/plain')) continue;
          raw = await (await item.getType('text/plain')).text();
          if (item.types.includes('text/html')) {
            html = await (await item.getType('text/html')).text();
          }
          break;
        }
      } else {
        raw = await navigator.clipboard.readText();
      }
    } catch {
      throw new Error('浏览器未读取剪贴板，请按 Ctrl+V 粘贴');
    }
    this.current(target);
    const now = this.rectangle(true);
    if (
      selection.top !== now.top ||
      selection.bottom !== now.bottom ||
      selection.left !== now.left ||
      selection.right !== now.right
    ) {
      throw new Error('选区已变化，请重新粘贴');
    }
    const typed = this.typedClipboard(raw, html);
    if (!raw && !typed) throw new Error('剪贴板没有可粘贴的单元格或文字');
    return this.paste(raw, typed, activity);
  }

  target(copyOnly = false): GridPasteTarget {
    const data = this.owner.data;
    if (
      !this.grid.active ||
      !this.view.ready() ||
      !this.grid.selected ||
      this.grid.stale ||
      this.owner.pending ||
      !data
    ) {
      throw new Error('请先核对当前修订');
    }
    if (!copyOnly && this.grid.shape?.readOnly) throw new Error(this.grid.shape.readOnly);
    const { id, binding, revision } = data.document;
    return { document: { id, binding }, revision, sheet: this.grid.selected.sheet };
  }

  pasteTarget(): GridPasteTarget {
    const target = this.target();
    Object.defineProperty(target, 'selection', { value: this.view.clipboardIdentity() });
    return target;
  }

  current(target: GridPasteTarget): void {
    if (
      target.selection &&
      (target.selection !== this.view.clipboardIdentity() ||
        this.grid.queue.some((task) => 'navigate' in task && Boolean(task.navigate)))
    ) {
      throw new Error('选区或筛选已变化，请重新粘贴');
    }
    const current = this.target();
    if (
      current.sheet !== target.sheet ||
      current.document.id !== target.document.id ||
      current.document.binding !== target.document.binding ||
      current.revision !== target.revision
    ) {
      throw new Error('选区或修订已变化，请重新操作');
    }
  }

  rectangle(allowVisible = false): GridRangeBounds {
    const ranges = this.view.areas();
    const first = ranges.length === 1 ? ranges[0] : undefined;
    if (!first) throw new Error('请选择一个连续区域');
    if (
      !allowVisible &&
      this.view.filtered() &&
      (first.visual1 !== first.visual2 || first.col1 !== first.col2)
    ) {
      throw new Error('请清除筛选或排序后按原地址填充或粘贴');
    }
    const rect = bounds(first);
    const merge = this.view.mergeAt(rect.top, rect.left);
    if (
      merge?.row === rect.top &&
      merge.col === rect.left &&
      merge.endRow === rect.bottom &&
      merge.endCol === rect.right
    ) {
      return { ...rect, bottom: rect.top, right: rect.left };
    }
    return rect;
  }

  checkSize(top: number, bottom: number, left: number, right: number): void {
    if ((bottom - top + 1) * (right - left + 1) > this.owner.limits.batchCells) {
      throw new Error('区域超出一次编辑容量，请缩小范围');
    }
    const shape = this.grid.shape;
    if (!shape || top < shape.firstRow || left < shape.firstCol || bottom > shape.rows || right > shape.cols) {
      throw new Error('选区超出可编辑区域');
    }
  }

  async cycle(key: string, backward: boolean): Promise<boolean> {
    const ranges = this.view.areas();
    const area = ranges.length === 1 ? ranges[0] : undefined;
    const selected = this.grid.selected;
    if (!area || !selected || this.view.filtered()) return false;
    const { top, bottom, left, right } = bounds(area);
    if (top === bottom && left === right) return false;
    const horizontal = key === 'Tab';
    const view = this.view;
    const only = view.mergeAt(top, left);
    if (only && only.endRow >= bottom && only.endCol >= right) return false;
    const advance = (
      value: number,
      lo: number,
      hi: number,
      next: (v: number) => number,
      previous: (v: number) => number,
    ): [number, boolean] => {
      const n = backward ? previous(value - 1) : next(value + 1);
      if (n >= lo && n <= hi && n !== value) return [n, false];
      return [backward ? previous(hi) : next(lo), true];
    };
    let row = selected.row;
    let col = selected.col;
    const start = { row, col };
    do {
      const merge = view.mergeAt(row, col);
      let wrapped: boolean;
      if (horizontal) {
        if (merge) col = backward ? merge.col : merge.endCol;
        [col, wrapped] = advance(
          col,
          left,
          right,
          (v) => view.nextCol(v),
          (v) => view.previousCol(v),
        );
        if (wrapped) {
          if (merge && merge.col <= left && merge.endCol >= right) {
            row = backward ? merge.row : merge.endRow;
          }
          [row] = advance(
            row,
            top,
            bottom,
            (v) => view.nextRow(v),
            (v) => view.previousRow(v),
          );
        }
      } else {
        if (merge) row = backward ? merge.row : merge.endRow;
        [row, wrapped] = advance(
          row,
          top,
          bottom,
          (v) => view.nextRow(v),
          (v) => view.previousRow(v),
        );
        if (wrapped) {
          if (merge && merge.row <= top && merge.endRow >= bottom) {
            col = backward ? merge.col : merge.endCol;
          }
          [col] = advance(
            col,
            left,
            right,
            (v) => view.nextCol(v),
            (v) => view.previousCol(v),
          );
        }
      }
      if (row < top || row > bottom || col < left || col > right) return true;
      const owner = view.mergeAt(row, col);
      if (!owner || (row === owner.row && col === owner.col)) break;
      if (backward && (horizontal ? row === owner.row : col === owner.col)) {
        row = owner.row;
        col = owner.col;
        break;
      }
    } while (row !== start.row || col !== start.col);
    await view.moveInside(row, col);
    return true;
  }

  async clear(): Promise<boolean | undefined> {
    const target = this.target();
    const sheet = target.sheet;
    const changes = new Map<string, EditChange>();
    const add = (row: number, col: number): void => {
      const merge = this.view.mergeAt(row, col);
      if (merge && (merge.row !== row || merge.col !== col)) return;
      changes.set(`${row},${col}`, { sheet, row, col, value: { kind: 'blank', raw: '' } });
      if (changes.size > this.owner.limits.batchCells) {
        throw new Error('选区超出一次编辑容量，请缩小范围');
      }
    };
    const ranges = this.view.areas();
    if (this.view.filtered()) {
      const snapshot = this.view.visibleSelection();
      const data = await snapshot.read({ richText: false, conditional: false });
      if (!snapshot.current()) throw new Error('选区已变化，请重新操作');
      for (const row of data.rows) {
        for (let col = snapshot.firstColumn; col <= snapshot.lastColumn; col++) {
          add(row.row, col);
        }
      }
    } else {
      for (const range of ranges) {
        const { top, bottom, left, right } = bounds(this.view.expand(range));
        this.checkSize(top, bottom, left, right);
        for (let row = top; row <= bottom; row++) {
          for (let col = left; col <= right; col++) add(row, col);
        }
      }
    }
    this.current(target);
    return this.owner.commit('change', boundedChanges(changes.values(), this.owner.limits), {
      direct: true,
    });
  }

  async fill(mode: string, input?: EditChange): Promise<boolean | undefined> {
    const target = this.target();
    if (this.view.filtered() && mode !== 'fill-entry') {
      throw new Error('请清除筛选或排序后按原地址填充');
    }
    const { top, bottom, left, right } = this.rectangle();
    this.checkSize(top, bottom, left, right);
    const ref = (r: number, c: number): string => `${this.owner.colName(c)}${r}`;
    const args: Record<string, unknown> = {
      ...target,
      mode,
      ref: ref(top, left),
      end: ref(bottom, right),
      direct: true,
    };
    if (input) args.operation = { changes: [input] };
    const preview = await this.owner.post<EditBatchPreviewResponse>('preview', args);
    this.current(target);
    if (!preview.changes?.length) return true;
    const entryAnchor = input
      ? { sheet: input.sheet, row: input.row, col: input.col }
      : undefined;
    return this.owner.commit('change', preview.changes, {
      proof: preview.proof,
      direct: true,
      inferNumberFormat: Boolean(input),
      entryAnchor,
    });
  }

  hasMerge(top: number, bottom: number, left: number, right: number): boolean {
    for (let row = top; row <= bottom; row++) {
      for (let col = left; col <= right; col++) {
        if (this.view.mergeAt(row, col)) return true;
      }
    }
    return false;
  }

  cancelCut(): void {
    this.copyEpoch = (this.copyEpoch || 0) + 1;
    if (!this.copied?.cut || this.copied.inactive) return;
    this.copied.inactive = true;
    this.view.redraw();
    this.grid.status('');
  }

  paintCut(node: CutSourceCellElement, sheet: string, row: number, col: number): void {
    const source = this.copied;
    let mask = 0;
    if (
      source?.cut &&
      !source.inactive &&
      source.sheet === sheet &&
      source.revision === this.owner.data?.document.revision &&
      source.first &&
      source.last &&
      row >= source.first.row &&
      row <= source.last.row &&
      col >= source.first.col &&
      col <= source.last.col
    ) {
      const merge = this.view.mergeAt(row, col);
      mask =
        (row === source.first.row ? 1 : 0) |
        ((merge?.endCol ?? col) === source.last.col ? 2 : 0) |
        ((merge?.endRow ?? row) === source.last.row ? 4 : 0) |
        (col === source.first.col ? 8 : 0);
    }
    if (mask === node._xlCutMask || (!mask && !node._xlCutMask)) return;
    node._xlCutMask = mask;
    node.classList.toggle('cut-source', Boolean(mask));
    if (mask) {
      node.style.setProperty(
        '--cut-edges',
        [1, 2, 4, 8].map((bit) => (mask & bit ? '2px' : '0')).join(' '),
      );
    } else {
      node.style.removeProperty('--cut-edges');
    }
  }

  async copySnapshot(
    snapshot: ClipboardSnapshot,
    cut = false,
    visibleRows?: readonly number[],
  ): Promise<EditCopiedSnapshot | null> {
    if (
      this.owner.data?.encoding ||
      (cut && this.view.filtered()) ||
      snapshot.rows * (snapshot.lastColumn - snapshot.firstColumn + 1) > this.owner.limits.batchCells
    ) {
      return null;
    }
    const first = parseEditTarget(snapshot.start, this.grid.shape);
    const last = parseEditTarget(snapshot.end, this.grid.shape);
    if (!visibleRows && last.row - first.row + 1 !== snapshot.rows) return null;
    const target = this.target(!cut);
    const merged = !visibleRows && this.hasMerge(first.row, last.row, first.col, last.col);
    const source: EditCopiedSnapshot = {
      marker: `excellens-copy-${crypto.randomUUID()}`,
      cut,
      merged,
      first,
      last,
      sheet: target.sheet,
      start: snapshot.start,
      end: snapshot.end,
      id: target.document.id,
      binding: target.document.binding,
      revision: target.revision,
    };
    try {
      const clip = await this.owner.post<EditClipboard>('copy', {
        ...target,
        ref: snapshot.start,
        end: snapshot.end,
        mode: 'grid',
        visibleRows,
      });
      return { ...source, clip };
    } catch (error: unknown) {
      // The normal HTML/TSV copy still works for a large typed payload.
      if (httpStatus(error) === 413) return merged || cut ? source : null;
      throw error;
    }
  }

  async paste(
    raw: string,
    typed: IdentifiedClipboard | null,
    activity?: ActivityHandle | null,
  ): Promise<boolean | undefined> {
    const target = this.pasteTarget();
    const rect = this.rectangle(true);
    const { top, bottom, left, right } = rect;
    const { limits, data } = this.owner;
    const textFile = Boolean(data?.encoding);
    const copied = isCopiedSnapshot(typed) ? typed : null;
    const local = copied?.id === target.document.id ? copied : null;
    const clipPortable = copied ? readPortableFromClip(copied.clip) : undefined;
    const hasPortableSourceRows = Boolean(clipPortable?.range?.sourceRows);
    if (
      !textFile &&
      (!local || (this.view.mappedRows() && !copied?.cut) || hasPortableSourceRows)
    ) {
      let portable = typed && 'portable' in typed ? typed.portable : clipPortable;
      if (typed && 'external' in typed) {
        portable = (await fetchNativePaste(this.owner, target, raw)) ?? undefined;
      }
      this.current(target);
      if (portable) return pastePortableRange(this, target, rect, portable, activity);
    }
    if (local && !textFile) {
      const ref = `${this.owner.colName(left)}${top}`;
      const end = top === bottom && left === right ? undefined : `${this.owner.colName(right)}${bottom}`;
      if (local.inactive) return true;
      if (local.binding !== target.document.binding) throw new Error('副本已切换，请重新复制');
      if (!local.first || !local.last || !local.sheet || !local.start || !local.end || !local.revision) {
        throw new Error('选区或修订已变化，请重新操作');
      }
      const height = local.last.row - local.first.row + 1;
      const width = local.last.col - local.first.col + 1;
      if (this.view.filtered() && (end || height > 1 || local.cut)) {
        throw new Error('请清除筛选或排序后按原地址粘贴');
      }
      const lastRow = local.cut ? top + height - 1 : Math.max(bottom, top + height - 1);
      const lastCol = local.cut ? left + width - 1 : Math.max(right, left + width - 1);
      this.checkSize(top, lastRow, left, lastCol);
      if (
        local.cut ||
        local.merged ||
        local.structural ||
        this.hasMerge(top, lastRow, left, lastCol)
      ) {
        if ((local.acceptedRevision ?? local.revision) !== target.revision) {
          throw new Error('来源修订已变化，请重新复制或剪切');
        }
        const structure: EditStructure = {
          kind: local.cut ? 'cutRange' : 'copyRange',
          sheet: local.sheet,
          range: `${local.start}:${local.end}`,
          destinationSheet: target.sheet,
          destination: end ? `${ref}:${end}` : ref,
          sourceRevision: local.revision,
        };
        if (
          target.sheet === local.sheet &&
          ref === local.start &&
          (!end || end === local.end) &&
          target.revision === local.revision
        ) {
          if (local.cut) this.cancelCut();
          return true;
        }
        const ok = await this.owner.commit('structure', undefined, { structure, direct: true });
        if (ok && this.owner.data) {
          local.inactive = local.cut;
          local.structural = true;
          local.acceptedRevision = this.owner.data.document.revision;
          this.copied = local;
          this.view.redraw();
        }
        return ok;
      }
      const clipPayload = isEditClipboard(local.clip)
        ? { ...local.clip, portable: undefined }
        : undefined;
      const plan = await this.owner.post<EditBatchPreviewResponse>('preview', {
        ...target,
        mode: 'all',
        ref,
        end,
        clipboard: clipPayload,
        direct: true,
      });
      this.current(target);
      if (!plan.changes?.length) return true;
      return this.owner.commit('change', plan.changes, {
        proof: plan.proof,
        direct: true,
        expandTable: !this.view.filtered(),
      });
    }
    if (this.copied?.cut) this.cancelCut();
    const shape = this.grid.shape;
    if (!shape) throw new Error('选区超出可编辑区域');
    const rows = /[\r\n\t]/.test(raw)
      ? parseClipboardTSV(raw, {
          maxRows: limits.batchCells,
          maxCols: shape.cols,
          maxCells: limits.batchCells,
          maxCellLength: limits.cellBytes,
          maxTextLength: limits.requestBytes,
        })
      : [[raw]];
    const height = rows.length;
    const width = Math.max(...rows.map((r) => r.length));
    const dest = await resolvePasteDestination(this, rect, height, width);
    this.current(target);
    const h = dest.rows.length;
    const w = dest.width;
    const addresses: EditAddress[] = [];
    const values: string[] = [];
    for (let i = 0; i < h; i++) {
      const destRow = dest.rows[i];
      if (destRow === undefined) continue;
      for (let j = 0; j < w; j++) {
        addresses.push({ sheet: target.sheet, row: destRow, col: left + j });
        values.push(rows[i % height]?.[j % width] ?? '');
      }
    }
    const kinds: readonly string[] = textFile
      ? []
      : await this.owner.post<readonly string[]>('input-kinds', { ...target, cells: addresses });
    this.current(target);
    function* changes(): Generator<EditChange> {
      for (const [i, a] of addresses.entries()) {
        yield {
          ...a,
          value: buildGridValue(values[i] ?? '', 'auto', kinds[i], textFile, limits),
        };
      }
    }
    return this.owner.commit('change', boundedChanges(changes(), limits), {
      direct: true,
      inferNumberFormat: true,
      expandTable: !this.view.filtered(),
    });
  }
}
