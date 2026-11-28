/**
 * Column-header span selection, drag selection, and serialized column visibility overrides.
 */
import { t as localize } from '../core/i18n.ts';
import type { ApiClient, ToastFn } from '../contracts/common.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput } from '../core/dom.ts';
import type { Geometry } from './geometry.ts';
import type { ColumnSelectionSpan, WorkbookState } from './state.ts';

export interface ColumnControllerOptions {
  readonly state: WorkbookState;
  readonly api: ApiClient;
  readonly toast: ToastFn;
  readonly geometry: Geometry;
  readonly baseRow: number;
  readonly baseCol: number;
  readonly colName: (col: number) => string;
  readonly parseColumn: (value: string) => number;
  readonly colLimit: () => number;
  readonly relayoutGrid: () => void;
  readonly selectCellByAddress: (ref: string, visualIndex: number) => void;
  readonly hideCellMenu: () => void;
  readonly closeColumnsMenu?: () => void;
  readonly selectionStats: () => { clear(): void } | null | undefined;
  readonly calculator: () =>
    | { readonly picking?: boolean; captureRange(start: string, end?: string): void }
    | null
    | undefined;
}

export class ColumnController {
  private readonly s: WorkbookState;
  private readonly api: ApiClient;
  private readonly toast: ToastFn;
  private readonly geometry: Geometry;
  private readonly baseRow: number;
  private readonly baseCol: number;
  private readonly colName: (col: number) => string;
  private readonly parseColumn: (value: string) => number;
  private readonly colLimit: () => number;
  private readonly relayoutGrid: () => void;
  private readonly selectCellByAddress: (ref: string, visualIndex: number) => void;
  private readonly hideCellMenu: () => void;
  private readonly closeColumnsMenu?: () => void;
  private readonly selectionStats: () => { clear(): void } | null | undefined;
  private readonly calculator: () =>
    | { readonly picking?: boolean; captureRange(start: string, end?: string): void }
    | null
    | undefined;

  private dragAnchor = 0;
  private dragEnd = 0;
  private dragFrame = 0;
  private visibilitySaveQueue: Promise<unknown> = Promise.resolve();
  private visibilityRevision = 0;

  constructor(options: ColumnControllerOptions) {
    this.s = options.state;
    this.api = options.api;
    this.toast = options.toast;
    this.geometry = options.geometry;
    this.baseRow = options.baseRow;
    this.baseCol = options.baseCol;
    this.colName = options.colName;
    this.parseColumn = options.parseColumn;
    this.colLimit = options.colLimit;
    this.relayoutGrid = options.relayoutGrid;
    this.selectCellByAddress = options.selectCellByAddress;
    this.hideCellMenu = options.hideCellMenu;
    this.closeColumnsMenu = options.closeColumnsMenu;
    this.selectionStats = options.selectionStats;
    this.calculator = options.calculator;
  }

  selectedSpan(): ColumnSelectionSpan | null {
    const p = this.s.columnSelection;
    if (!p) return null;
    return { start: Math.min(p.start, p.end), end: Math.max(p.start, p.end) };
  }

  spanLabel(span: ColumnSelectionSpan | null | undefined): string {
    if (!span) return '';
    return span.start === span.end
      ? this.colName(span.start)
      : `${this.colName(span.start)}:${this.colName(span.end)}`;
  }

  parseSpan(value: string): ColumnSelectionSpan | null {
    const parts = value.replaceAll('$', '').trim().split(/\s*:\s*/);
    if (parts.length < 1 || parts.length > 2) return null;
    const first = this.parseColumn(parts[0] ?? '');
    const last = this.parseColumn(parts.at(-1) ?? '');
    return first && last ? { start: Math.min(first, last), end: Math.max(first, last) } : null;
  }

  hasOfType(span: ColumnSelectionSpan | null | undefined, hidden: boolean): boolean {
    if (!span) return false;
    for (let col = span.start; col <= span.end; col++) {
      if (this.s.hiddenColumns.has(col) === hidden) return true;
    }
    return false;
  }

  paintSelection(): void {
    const span = this.selectedSpan();
    for (const head of document.querySelectorAll<HTMLElement>('.grid-region .column-head[data-col]')) {
      const col = Number(head.dataset.col);
      head.classList.toggle('column-selected', Boolean(span && col >= span.start && col <= span.end));
    }
    for (const row of this.s.renderedRows.values()) {
      for (const [col, cell] of row._xlCells ?? []) {
        cell.classList.toggle(
          'column-range-cell',
          Boolean(span && col >= span.start && col <= span.end),
        );
      }
    }
    for (const cell of document.querySelectorAll<HTMLElement>('.grid-region .pinned-cell[data-col]')) {
      const col = Number(cell.dataset.col);
      cell.classList.toggle(
        'column-range-cell',
        Boolean(span && col >= span.start && col <= span.end),
      );
    }
  }

  selectSpan(anchor: number, end: number): void {
    const limit = this.colLimit();
    if (
      !this.s.indexReady ||
      !Number.isInteger(anchor) ||
      !Number.isInteger(end) ||
      anchor < 1 ||
      end < 1 ||
      anchor > limit ||
      end > limit
    ) {
      return;
    }
    this.s.columnSelection = { start: anchor, end };
    this.s.columnAnchor = anchor;
    this.s.range = null;
    this.s.multiRanges = [];
    this.s.rangeAnchor = null;
    this.s.wholeRowSelected = false;
    const span = this.selectedSpan();
    if (span) {
      requireEl('selectionStatus').textContent =
        localize('ui.columns.column', { spanLabel: this.spanLabel(span), end: span.end - span.start + 1 });
    }
    this.selectionStats()?.clear();
    this.paintSelection();
  }

  beginDrag(event: PointerEvent): void {
    const target = event.target;
    if (
      event.button !== 0 ||
      !this.s.indexReady ||
      !(target instanceof Element) ||
      target.closest('.column-resize-grip,.column-filter,.auto-filter-button')
    ) {
      return;
    }
    const head = target.closest('.column-head[data-col]');
    if (!(head instanceof HTMLElement)) return;
    const col = Number(head.dataset.col);
    if (!col) return;
    this.hideCellMenu();
    this.hideMenu();
    const anchor = event.shiftKey ? this.s.columnAnchor || col : col;
    this.dragAnchor = anchor;
    this.dragEnd = col;
    this.selectSpan(anchor, col);
    event.preventDefault();
  }

  updateDrag(event: PointerEvent): void {
    if (!this.dragAnchor || !(event.buttons & 1)) return;
    const el = document.elementFromPoint(event.clientX, event.clientY);
    const head = el?.closest('.column-head[data-col]');
    if (!(head instanceof HTMLElement)) return;
    const col = Number(head.dataset.col);
    if (!col || col === this.dragEnd) return;
    this.dragEnd = col;
    if (this.dragFrame) return;
    this.dragFrame = requestAnimationFrame(() => {
      this.dragFrame = 0;
      if (this.dragAnchor) this.selectSpan(this.dragAnchor, this.dragEnd);
    });
  }

  captureSelection(): void {
    const span = this.selectedSpan();
    const calc = this.calculator();
    if (span && calc?.picking) {
      calc.captureRange(`${this.colName(span.start)}:${this.colName(span.end)}`);
    }
  }

  endDrag(event?: PointerEvent): void {
    if (this.dragFrame) {
      cancelAnimationFrame(this.dragFrame);
      this.dragFrame = 0;
    }
    if (this.dragAnchor) {
      this.selectSpan(this.dragAnchor, this.dragEnd);
      if (event?.type !== 'pointercancel') this.captureSelection();
    }
    this.dragAnchor = 0;
    this.dragEnd = 0;
  }

  applyVisibility(): void {
    const hidden = new Set<number>();
    const maxCol = this.colLimit();
    if (this.s.columnMode === 'hide-all') {
      for (let c = 1; c <= maxCol; c++) hidden.add(c);
    } else if (this.s.columnMode !== 'show-all') {
      for (const range of this.s.columnSourceRanges) {
        const min = Math.max(1, range.min || 1);
        const max = Math.min(maxCol, range.max || 0);
        for (let c = min; c <= max; c++) hidden.add(c);
      }
    }
    for (const [raw, flag] of Object.entries(this.s.columnOverrides)) {
      const c = Number(raw);
      if (!Number.isInteger(c) || c < 1 || c > maxCol) continue;
      if (flag) hidden.add(c);
      else hidden.delete(c);
    }
    this.s.hiddenColumns = hidden;
    const effective: Record<string, number> = {
      ...this.s.sourceColumnWidths,
      ...this.s.columnWidths,
    };
    for (const col of hidden) effective[String(col)] = 0;
    this.geometry.rows.setBase(this.s.sourceDefaultRow || this.baseRow);
    this.geometry.cols.setBase(this.s.sourceDefaultCol || this.baseCol);
    this.geometry.rows.load(this.s.rowSizes, this.s.sourceRowSizes);
    this.geometry.cols.load(effective);
  }

  nextVisible(from: number): number {
    for (let c = Math.max(1, Math.trunc(from) || 1); c <= Math.max(1, this.s.maxCol); c++) {
      if (!this.s.hiddenColumns.has(c)) return c;
    }
    return Math.min(this.colLimit(), Math.max(1, Math.trunc(from) || 1));
  }

  previousVisible(from: number): number {
    for (let c = Math.min(this.s.maxCol, Math.trunc(from) || 1); c >= 1; c--) {
      if (!this.s.hiddenColumns.has(c)) return c;
    }
    return 0;
  }

  hideMenu(): void {
    this.closeColumnsMenu?.();
  }

  refreshMenuTarget(): void {
    const span = this.parseSpan(requireInput('columnTarget').value);
    const hideBtn = requireButton('hideSelectedColumnBtn');
    const showBtn = requireButton('showSelectedColumnBtn');
    hideBtn.disabled = !this.hasOfType(span, false);
    showBtn.disabled = !this.hasOfType(span, true);
    const multiple = Boolean(span && span.start !== span.end);
    hideBtn.textContent = multiple ? localize('ui.columns.hideSelectedColumns') : localize('ui.columns.hideThisColumn');
    showBtn.textContent = localize('ui.columns.displayHiddenColumns');
  }

  targetSpan(): ColumnSelectionSpan | null {
    const span = this.parseSpan(requireInput('columnTarget').value);
    if (!span) this.toast(localize('ui.columns.enterValidColumnOrRange'), 'warning');
    return span;
  }

  prepareMenu(event: Event): void {
    if (!this.s.indexReady) {
      event.preventDefault();
      return;
    }
    const selectedCol = this.s.selected?.col || this.nextVisible(1);
    const label = this.spanLabel(this.selectedSpan()) || this.colName(selectedCol);
    requireEl('columnMenuSelection').textContent = localize('ui.columns.selectColumns');
    requireInput('columnTarget').value = label;
    this.refreshMenuTarget();
  }

  async setVisibility(
    mode: string,
    rawIndex = 0,
    hidden = false,
    rawEnd = rawIndex,
  ): Promise<void> {
    if (!this.s.indexReady || !this.s.sheet) return;
    const sheet = this.s.sheet;
    const epoch = this.s.sheetEpoch;
    const previousMode = this.s.columnMode;
    const previousOverrides = { ...this.s.columnOverrides };
    let index = rawIndex;
    let end = rawEnd;
    if (mode === 'column' || mode === 'range') {
      const first = Math.min(index, end);
      const last = Math.max(index, end);
      if (!Number.isInteger(first) || !Number.isInteger(last) || first < 1 || last > this.colLimit()) {
        return;
      }
      for (let col = first; col <= last; col++) {
        this.s.columnOverrides[String(col)] = hidden;
      }
      index = first;
      end = last;
    } else {
      this.s.columnMode = mode;
      this.s.columnOverrides = {};
    }
    this.applyVisibility();
    this.hideMenu();
    if (this.s.selected && this.s.hiddenColumns.has(this.s.selected.col)) {
      let next = this.nextVisible(this.s.selected.col + 1);
      if (this.s.hiddenColumns.has(next)) {
        next = this.previousVisible(this.s.selected.col - 1) || this.s.selected.col;
      }
      if (!this.s.hiddenColumns.has(next)) {
        this.selectCellByAddress(
          `${this.colName(next)}${this.s.selected.row}`,
          this.s.selectedVisualIndex,
        );
      }
    }
    this.relayoutGrid();
    const revision = ++this.visibilityRevision;
    const save = (): Promise<unknown> =>
      this.api('/api/layout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sheet,
          kind: 'visibility',
          mode,
          index: index || 0,
          end: end || 0,
          hidden,
        }),
      });
    const pending = this.visibilitySaveQueue.then(save, save);
    this.visibilitySaveQueue = pending.catch(() => {
      // Keep serial queue healthy after a failure.
    });
    try {
      await pending;
    } catch (error: unknown) {
      if (
        this.s.sheet === sheet &&
        this.s.sheetEpoch === epoch &&
        this.visibilityRevision === revision
      ) {
        this.s.columnMode = previousMode;
        this.s.columnOverrides = previousOverrides;
        this.applyVisibility();
        this.relayoutGrid();
      }
      this.toast(localize('ui.columns.failedSaveColumnDisplay', { detail: errorMessage(error) }), 'error');
    }
  }

  bind(region: HTMLElement): void {
    requireEl('columnsMenu').addEventListener('menuopen', (event: Event) => this.prepareMenu(event));
    requireInput('columnTarget').addEventListener('input', () => this.refreshMenuTarget());
    requireButton('hideSelectedColumnBtn').addEventListener('click', () => {
      const span = this.targetSpan();
      if (span) {
        void this.setVisibility(span.start === span.end ? 'column' : 'range', span.start, true, span.end);
      }
    });
    requireButton('showSelectedColumnBtn').addEventListener('click', () => {
      const span = this.targetSpan();
      if (span) {
        void this.setVisibility(span.start === span.end ? 'column' : 'range', span.start, false, span.end);
      }
    });
    requireButton('showAllColumnsBtn').addEventListener('click', () => {
      void this.setVisibility('show-all');
    });
    requireButton('hideAllColumnsBtn').addEventListener('click', () => {
      void this.setVisibility('hide-all');
    });
    requireButton('restoreColumnsBtn').addEventListener('click', () => {
      void this.setVisibility('original');
    });
    region.addEventListener('pointerdown', (event: PointerEvent) => this.beginDrag(event), true);
    document.addEventListener('pointermove', (event: PointerEvent) => this.updateDrag(event), true);
    document.addEventListener('pointerup', (event: PointerEvent) => this.endDrag(event), true);
    document.addEventListener('pointercancel', (event: PointerEvent) => this.endDrag(event), true);
  }
}
