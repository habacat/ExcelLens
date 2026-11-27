/**
 * Deterministic multi-area selection model. Operates purely on rectangle spans
 * without allocating per-cell bitmaps, even for whole-column selections.
 */
import type { CellPoint, SelectionArea } from '../contracts/common.ts';

export const MULTI_SELECTION_LIMIT = 256;

export interface SelectionStateLike {
  readonly multiRanges?: readonly SelectionArea[];
  readonly range?: SelectionArea | null;
  readonly selected?: { readonly row: number; readonly col: number } | null;
  readonly selectedVisualIndex?: number;
  readonly filtered?: boolean;
  readonly selectedFrozenThrough?: number;
  readonly wholeRowSelected?: boolean;
}

export interface SelectionEndpoints {
  readonly start: string;
  readonly end: string;
  readonly visualStart?: number;
  readonly visualEnd?: number;
}

export interface SelectionHighlight {
  readonly rowSpans: readonly (readonly [number, number])[];
  readonly colSpans: readonly (readonly [number, number])[];
  readonly singleCol: number;
  readonly hasMultiCol: boolean;
  readonly key: string;
  readonly mergeTarget: {
    readonly rowSpans: readonly (readonly [number, number])[];
    readonly colSpans: readonly (readonly [number, number])[];
  } | null;
  hasRow(row: number, visual?: number): boolean;
  hasCol(col: number): boolean;
}

export function selectionCell(row: number, col: number, visual = row): CellPoint {
  return { row, col, visual };
}

export function selectionRectangle(
  a: CellPoint | null | undefined,
  b: CellPoint | null | undefined,
  filtered = false,
): SelectionArea | null {
  if (!a || !b) return null;
  return {
    row1: a.row,
    row2: b.row,
    col1: a.col,
    col2: b.col,
    visual1: filtered ? (a.visual || a.row) : 0,
    visual2: filtered ? (b.visual || b.row) : 0,
  };
}

function position(row: number, visual: number, through: number): number {
  return row <= through ? row : through + visual;
}

function mergeSpans(spans: [number, number][]): [number, number][] {
  if (spans.length <= 1) return spans;
  spans.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const first = spans[0];
  if (!first) return [];
  const out: [number, number][] = [[first[0], first[1]]];
  for (let i = 1; i < spans.length; i++) {
    const span = spans[i];
    const prev = out[out.length - 1];
    if (!span || !prev) continue;
    if (span[0] <= prev[1] + 1) prev[1] = Math.max(prev[1], span[1]);
    else out.push([span[0], span[1]]);
  }
  return out;
}

function inSpans(spans: readonly (readonly [number, number])[], value: number): boolean {
  const first = spans[0];
  if (spans.length === 1 && first) return value >= first[0] && value <= first[1];
  for (const span of spans) {
    if (value >= span[0] && value <= span[1]) return true;
  }
  return false;
}

export function selectionContainsCol(area: SelectionArea | null | undefined, col: number): boolean {
  return Boolean(area) && col >= Math.min(area?.col1 ?? 0, area?.col2 ?? 0) && col <= Math.max(area?.col1 ?? 0, area?.col2 ?? 0);
}

export function selectionContainsRow(
  area: SelectionArea | null | undefined,
  row: number,
  visual = 0,
  filtered = false,
  through = 0,
): boolean {
  if (!area) return false;
  if (!filtered) return row >= Math.min(area.row1, area.row2) && row <= Math.max(area.row1, area.row2);
  if (row > through && !(visual > 0)) return false;
  const a = position(area.row1, area.visual1, through);
  const b = position(area.row2, area.visual2, through);
  const index = position(row, visual, through);
  return index >= Math.min(a, b) && index <= Math.max(a, b);
}

export function selectionContains(
  area: SelectionArea | null | undefined,
  row: number,
  col: number,
  visual = 0,
  filtered = false,
  through = 0,
): boolean {
  return selectionContainsCol(area, col) && selectionContainsRow(area, row, visual, filtered, through);
}

export function selectionAreas(
  state: SelectionStateLike | null | undefined,
  includeSingle = false,
): SelectionArea[] {
  const result: SelectionArea[] = [...(state?.multiRanges ?? [])];
  if (state?.range) {
    result.push(state.range);
  } else if (includeSingle && state?.selected) {
    const pt = selectionCell(
      state.selected.row,
      state.selected.col,
      state.selectedVisualIndex ?? state.selected.row,
    );
    const rect = selectionRectangle(pt, pt, Boolean(state.filtered));
    if (rect) result.push(rect);
  }
  return result;
}

export function selectionPoints(
  area: SelectionArea,
  colName: (col: number) => string,
): SelectionEndpoints {
  const visual = Boolean(area.visual1) || Boolean(area.visual2);
  const r1 = visual ? area.row1 : Math.min(area.row1, area.row2);
  const r2 = visual ? area.row2 : Math.max(area.row1, area.row2);
  const c1 = Math.min(area.col1, area.col2);
  const c2 = Math.max(area.col1, area.col2);
  const result: SelectionEndpoints = {
    start: `${colName(c1)}${r1}`,
    end: `${colName(c2)}${r2}`,
    ...(area.visual1 ? { visualStart: area.visual1 } : {}),
    ...(area.visual2 ? { visualEnd: area.visual2 } : {}),
  };
  return result;
}

export function selectionRowCount(
  area: SelectionArea,
  filtered = false,
  through = 0,
): number {
  if (!filtered) return Math.abs(area.row2 - area.row1) + 1;
  const pinned1 = area.row1 <= through;
  const pinned2 = area.row2 <= through;
  if (pinned1 && pinned2) return Math.abs(area.row2 - area.row1) + 1;
  if (pinned1) return through - area.row1 + 1 + area.visual2;
  if (pinned2) return through - area.row2 + 1 + area.visual1;
  return Math.abs(area.visual2 - area.visual1) + 1;
}

export function removeSingletonArea(
  areas: readonly SelectionArea[],
  row: number,
  col: number,
  visual = 0,
  filtered = false,
): SelectionArea[] | null {
  const found = areas.findIndex(
    (a) =>
      a.row1 === a.row2 &&
      a.col1 === a.col2 &&
      selectionContains(a, row, col, visual, filtered),
  );
  if (found === -1) return null;
  return areas.filter((_, i) => i !== found);
}

export function computeSelectionHighlight(
  state: SelectionStateLike | null | undefined,
  focus: { readonly row: number; readonly col: number } | null | undefined = state?.selected,
  list: readonly SelectionArea[] = selectionAreas(state),
): SelectionHighlight {
  const filtered = Boolean(state?.filtered);
  const through = filtered ? (state?.selectedFrozenThrough ?? 0) : 0;
  let rowSpans: [number, number][] = [];
  let colSpans: [number, number][] = [];
  let hasRow: (row: number, visual?: number) => boolean = () => false;
  let hasCol: (col: number) => boolean = () => false;

  if (state?.wholeRowSelected && state.selected) {
    const r = state.selected.row;
    const v = state.selectedVisualIndex ?? 0;
    if (r && (!filtered || r <= through || v > 0)) {
      const p = filtered ? position(r, v, through) : r;
      rowSpans = [[p, p]];
      hasRow = (row: number, visual = 0): boolean =>
        filtered ? (row <= through || visual > 0) && position(row, visual, through) === p : row === r;
    }
  } else if (list.length) {
    colSpans = mergeSpans(
      list.map((a): [number, number] => [Math.min(a.col1, a.col2), Math.max(a.col1, a.col2)]),
    );
    rowSpans = mergeSpans(
      list
        .map((a): [number, number] => {
          const r1 = filtered ? position(a.row1, a.visual1, through) : a.row1;
          const r2 = filtered ? position(a.row2, a.visual2, through) : a.row2;
          return [Math.min(r1, r2), Math.max(r1, r2)];
        })
        .filter(([a, b]) => Number.isFinite(a) && Number.isFinite(b)),
    );
    hasCol = (col: number): boolean => inSpans(colSpans, col);
    hasRow = (row: number, visual = 0): boolean => {
      if (!filtered) return inSpans(rowSpans, row);
      return (row <= through || visual > 0) && inSpans(rowSpans, position(row, visual, through));
    };
  } else if (focus) {
    if (focus.row) {
      rowSpans = [[focus.row, focus.row]];
      hasRow = (row: number): boolean => row === focus.row;
    }
    if (focus.col) {
      colSpans = [[focus.col, focus.col]];
      hasCol = (col: number): boolean => col === focus.col;
    }
  }

  const firstColSpan = colSpans[0];
  const singleCol =
    colSpans.length === 1 && firstColSpan && firstColSpan[0] === firstColSpan[1]
      ? firstColSpan[0]
      : 0;
  const hasMultiCol =
    colSpans.length > 1 ||
    (colSpans.length === 1 && Boolean(firstColSpan) && firstColSpan?.[0] !== firstColSpan?.[1]);
  const fmt = (spans: readonly (readonly [number, number])[]): string =>
    spans.map(([a, b]) => (a === b ? String(a) : `${a}-${b}`)).join(',');
  const key = `${filtered ? `f${through}` : 's'}:${fmt(rowSpans)}|${fmt(colSpans)}`;
  const mergeTarget = rowSpans.length || colSpans.length ? { rowSpans, colSpans } : null;
  return { rowSpans, colSpans, singleCol, hasMultiCol, key, mergeTarget, hasRow, hasCol };
}

export const MultiSelection = Object.freeze({
  LIMIT: MULTI_SELECTION_LIMIT,
  cell: selectionCell,
  rectangle: selectionRectangle,
  contains: selectionContains,
  containsRow: selectionContainsRow,
  containsCol: selectionContainsCol,
  areas: selectionAreas,
  points: selectionPoints,
  rowCount: selectionRowCount,
  removeSingleton: removeSingletonArea,
  highlight: computeSelectionHighlight,
});
