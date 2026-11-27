/**
 * Visible reference rectangle projection (preserving sort/filter visibility runs)
 * and edge auto-scrolling drag coordinate clamping for formula pointing and fill handle.
 */
import type { MergeRange } from '../contracts/common.ts';
import type { FrameArea } from '../grid/range-frame.ts';

export interface ReferenceArea {
  readonly row1: number;
  readonly row2: number;
  readonly col1: number;
  readonly col2: number;
  readonly visual1?: number;
  readonly visual2?: number;
  readonly color?: string;
}

interface MutableFrameArea {
  row1: number;
  row2: number;
  col1: number;
  col2: number;
  visual1: number;
  visual2: number;
  color?: string;
}

export type DragPointKind = 'cell' | 'row' | 'column';

export interface DragPointResult {
  readonly x: number;
  readonly y: number;
  readonly scrolling: boolean;
}

export function visibleAreas(
  areas: readonly ReferenceArea[],
  filtered: boolean,
  pinned: number,
  rows: Iterable<readonly [number, number]>,
  mergeAt: (row: number, col: number) => MergeRange | null | undefined,
): FrameArea[] {
  const result: FrameArea[] = [];
  for (const inputArea of areas) {
    let area: FrameArea = {
      row1: inputArea.row1,
      row2: inputArea.row2,
      col1: inputArea.col1,
      col2: inputArea.col2,
      visual1: inputArea.visual1 ?? 0,
      visual2: inputArea.visual2 ?? 0,
      color: inputArea.color,
    };
    if (area.row1 === area.row2 && area.col1 === area.col2) {
      const merge = mergeAt(area.row1, area.col1);
      if (merge) {
        area = {
          ...area,
          row1: merge.row,
          row2: merge.endRow,
          col1: merge.col,
          col2: merge.endCol,
        };
      }
    }
    if (!filtered) {
      result.push(area);
      continue;
    }
    if (area.row1 <= pinned) {
      result.push({ ...area, row2: Math.min(pinned, area.row2) });
    }
    let run: MutableFrameArea | null = null;
    // Sorting can put distant source rows next to each other. Draw the
    // matching visible runs without inventing source rows from screen offsets.
    for (const [visual, source] of rows) {
      if (source <= pinned || source < area.row1 || source > area.row2) {
        run = null;
        continue;
      }
      if (run && visual === run.visual2 + 1) {
        run.row2 = source;
        run.visual2 = visual;
      } else {
        run = {
          ...area,
          row1: source,
          row2: source,
          visual1: visual,
          visual2: visual,
        };
        result.push(run);
      }
    }
  }
  return result;
}

export function dragPoint(
  x: number,
  y: number,
  kind: DragPointKind,
  grid: HTMLElement,
  header: number,
  rowHead: number,
  scrollVertical: (dy: number) => boolean,
  scroll = true,
): DragPointResult {
  const r = grid.getBoundingClientRect();
  const left = r.left + (kind === 'row' ? 0 : rowHead);
  const top = r.top + (kind === 'column' ? 0 : header);
  const right = r.left + grid.clientWidth;
  const bottom = r.top + grid.clientHeight;
  const speed = (v: number, lo: number, hi: number): number =>
    v < lo + 14
      ? -Math.min(36, Math.ceil((lo + 14 - v) / 2))
      : v > hi - 14
        ? Math.min(36, Math.ceil((v - hi + 14) / 2))
        : 0;
  const dx = kind === 'row' ? 0 : speed(x, left, right);
  const dy = kind === 'column' ? 0 : speed(y, top, bottom);
  const oldLeft = grid.scrollLeft;
  if (scroll && dx) grid.scrollLeft += dx;
  const vertical = Boolean(scroll && dy && scrollVertical(dy));
  return {
    x: kind === 'row' ? r.left + rowHead / 2 : Math.max(left + 1, Math.min(right - 1, x)),
    y: kind === 'column' ? r.top + header / 2 : Math.max(top + 1, Math.min(bottom - 1, y)),
    scrolling: vertical || oldLeft !== grid.scrollLeft,
  };
}

export const EditReferenceView = Object.freeze({
  visibleAreas,
  dragPoint,
});
