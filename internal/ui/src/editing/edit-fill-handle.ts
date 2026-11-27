/**
 * Drag-to-fill corner handle controller for live worksheet editing.
 * Tracks vertical or horizontal fill drags, renders a preview outline, and
 * commits bounded auto-fill previews via the editable copy API.
 */
import type { ActivityHandle, SelectionArea } from '../contracts/common.ts';
import type { EditChange } from '../contracts/edit.ts';
import type { DragPointKind, DragPointResult } from './edit-reference-view.ts';
import type { ReferenceHighlightArea } from './edit-formula-entry.ts';
import type { FormulaPoint } from './formula-entry-model.ts';
import { decodeEditBatchPreviewResponse } from '../core/http-decoders.generated.ts';

export interface FillHandleTarget {
  readonly document: { readonly id: string; readonly binding: string };
  readonly revision: string;
  readonly sheet: string;
}

export interface FillHandleHost {
  readonly active: boolean;
  readonly blocked: boolean;
  readonly stale: boolean;
  readonly region: HTMLElement;
  readonly owner: {
    readonly data: { readonly encoding?: string } | null;
    colName(col: number): string;
    post(command: string, body: unknown, signal?: AbortSignal): Promise<unknown>;
    commit(
      action: 'change',
      changes: readonly EditChange[],
      options?: { readonly direct?: boolean; readonly proof?: string },
    ): Promise<boolean | undefined>;
  };
  readonly ranges: {
    target(): FillHandleTarget;
    current(target: FillHandleTarget): void;
    checkSize(top: number, bottom: number, left: number, right: number): void;
  };
  readonly view: {
    filtered(): boolean;
    areas(): readonly SelectionArea[];
    dragPoint(x: number, y: number, kind: DragPointKind, scroll?: boolean): DragPointResult;
    paintReferences(): void;
    selectArea(area: ReferenceHighlightArea): void;
  };
  readonly entry: {
    hit(x: number, y: number): FormulaPoint | null;
  };
  focus(): void;
  act(
    next: ((activity?: ActivityHandle | null) => Promise<boolean | undefined> | boolean | undefined) | null,
    confirmDraft?: boolean,
    label?: string,
  ): Promise<boolean>;
}

interface FillDragState {
  readonly id: number;
  readonly target: FillHandleTarget;
  readonly top: number;
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  x: number;
  y: number;
  destination: { readonly row: number; readonly col: number } | null;
}

export class FillHandle {
  private readonly grid: FillHandleHost;
  public drag: FillDragState | null = null;
  public area: ReferenceHighlightArea | null = null;
  private frame = 0;

  constructor(grid: FillHandleHost) {
    this.grid = grid;
    const region = grid.region;
    region.addEventListener('pointerdown', (e: PointerEvent) => this.start(e), true);
    region.addEventListener('pointermove', (e: PointerEvent) => {
      if (this.drag?.id !== e.pointerId) return;
      this.drag.x = e.clientX;
      this.drag.y = e.clientY;
      if (!this.frame) this.frame = requestAnimationFrame(() => this.track(true));
    });
    region.addEventListener('pointerup', (e: PointerEvent) => this.end(e), true);
    for (const type of ['lostpointercapture', 'pointercancel'] as const) {
      region.addEventListener(type, (e: PointerEvent) => {
        if (this.drag?.id === e.pointerId) this.reset();
      });
    }
    document.addEventListener(
      'keydown',
      (e: KeyboardEvent) => {
        if (this.drag && e.key === 'Escape') {
          e.preventDefault();
          e.stopImmediatePropagation();
          this.reset();
          grid.focus();
        }
      },
      true,
    );
  }

  private start(e: PointerEvent): void {
    const g = this.grid;
    const target = e.target;
    if (
      !g.active ||
      e.button !== 0 ||
      !(target instanceof Element) ||
      !target.closest('#rangeFrames .range-corner')
    ) {
      return;
    }
    e.preventDefault();
    e.stopImmediatePropagation();
    if (g.blocked || g.stale || g.owner.data?.encoding || g.view.filtered()) return;
    const selection = g.view.areas();
    const s = selection.length === 1 ? selection[0] : undefined;
    if (!s) return;
    this.drag = {
      id: e.pointerId,
      target: g.ranges.target(),
      top: Math.min(s.row1, s.row2),
      bottom: Math.max(s.row1, s.row2),
      left: Math.min(s.col1, s.col2),
      right: Math.max(s.col1, s.col2),
      x: e.clientX,
      y: e.clientY,
      destination: null,
    };
    g.region.setPointerCapture(e.pointerId);
  }

  private track(scroll: boolean): void {
    this.frame = 0;
    const d = this.drag;
    const g = this.grid;
    if (!d) return;
    const pos = g.view.dragPoint(d.x, d.y, 'cell', scroll);
    const p = g.entry.hit(pos.x, pos.y);
    if (p?.kind === 'cell') {
      const dr = p.row < d.top ? p.row - d.top : Math.max(0, p.row - d.bottom);
      const dc = p.col < d.left ? p.col - d.left : Math.max(0, p.col - d.right);
      if (!dr && !dc) {
        d.destination = null;
        this.area = null;
      } else {
        const vertical = Boolean(dr) && (!dc || Math.abs(dr) >= Math.abs(dc));
        const row = vertical ? p.row : d.bottom;
        const col = vertical ? d.right : p.col;
        d.destination = { row, col };
        this.area = {
          row1: Math.min(d.top, row),
          row2: Math.max(d.bottom, row),
          col1: Math.min(d.left, col),
          col2: Math.max(d.right, col),
          color: 'var(--selection-color)',
        };
      }
      g.view.paintReferences();
    }
    if (pos.scrolling && scroll) this.frame = requestAnimationFrame(() => this.track(true));
  }

  reset(): void {
    const id = this.drag?.id;
    this.drag = null;
    this.area = null;
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    if (id !== undefined && this.grid.region.hasPointerCapture(id)) {
      this.grid.region.releasePointerCapture(id);
    }
    this.grid.view.paintReferences();
  }

  private end(e: PointerEvent): void {
    const d = this.drag;
    if (d?.id !== e.pointerId) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    cancelAnimationFrame(this.frame);
    d.x = e.clientX;
    d.y = e.clientY;
    this.track(false);
    const area = this.area;
    const destination = d.destination;
    const g = this.grid;
    const alternate = e.ctrlKey || e.metaKey;
    this.reset();
    if (!area || !destination) return;
    void g.act(
      async () => {
        const ref = (row: number, col: number): string => `${g.owner.colName(col)}${row}`;
        g.ranges.current(d.target);
        g.ranges.checkSize(area.row1, area.row2, area.col1, area.col2);
        const plan = await g.owner.post('preview', {
          ...d.target,
          mode: 'auto-fill',
          ref: ref(d.top, d.left),
          end: ref(d.bottom, d.right),
          destination: ref(destination.row, destination.col),
          alternate,
          direct: true,
        }).then(decodeEditBatchPreviewResponse);
        g.ranges.current(d.target);
        if (
          plan.changes?.length &&
          !(await g.owner.commit('change', plan.changes, { direct: true, proof: plan.proof }))
        ) {
          return false;
        }
        g.view.selectArea(area);
        g.focus();
        return undefined;
      },
      false,
      '正在填充选区',
    );
  }
}
