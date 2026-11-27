/**
 * Source merge range interval tree and DOM cell merge decorator.
 * Values remain at their original anchor coordinates.
 */
import type { MergeRange, SelectionArea } from '../contracts/common.ts';
import type { RowVisibility } from './row-visibility.ts';

interface MergeTreeNode {
  readonly range: MergeRange;
  readonly left: MergeTreeNode | null;
  readonly right: MergeTreeNode | null;
  readonly firstRow: number;
  readonly max: number;
  readonly firstCol: number;
  readonly lastCol: number;
}

export interface MergeCrosshairPoint {
  readonly row?: number;
  readonly col?: number;
  readonly rowSpans?: readonly (readonly [number, number])[];
  readonly colSpans?: readonly (readonly [number, number])[];
}

export interface MergeCopyAnchor {
  readonly endRow: number;
  readonly endCol: number;
  readonly visibleRows: number;
}

export interface MergedCellMeta {
  _xlMerge?: MergeRange | null;
  _xlCovered?: boolean;
}

export type MergedCellElement = HTMLElement & MergedCellMeta;

export class MergedCells {
  public readonly maxRows: number;
  public readonly maxColumns: number;
  public root: MergeTreeNode | null = null;

  constructor(maxRows = Number.MAX_SAFE_INTEGER, maxColumns = Number.MAX_SAFE_INTEGER) {
    this.maxRows = maxRows;
    this.maxColumns = maxColumns;
  }

  load(ranges?: readonly MergeRange[] | null): void {
    const sorted = (ranges ?? [])
      .filter(
        (r) =>
          [r.row, r.col, r.endRow, r.endCol].every(Number.isInteger) &&
          r.row > 0 &&
          r.col > 0 &&
          r.endRow >= r.row &&
          r.endCol >= r.col &&
          r.endRow <= this.maxRows &&
          r.endCol <= this.maxColumns,
      )
      .map((r) => ({ ...r }))
      .sort((a, b) => a.row - b.row || a.col - b.col);

    const build = (lo: number, hi: number): MergeTreeNode | null => {
      if (lo >= hi) return null;
      const mid = (lo + hi) >>> 1;
      const range = sorted[mid];
      const first = sorted[lo];
      if (!range || !first) return null;
      const left = build(lo, mid);
      const right = build(mid + 1, hi);
      return {
        range,
        left,
        right,
        firstRow: first.row,
        max: Math.max(range.endRow, left?.max ?? 0, right?.max ?? 0),
        firstCol: Math.min(range.col, left?.firstCol ?? Infinity, right?.firstCol ?? Infinity),
        lastCol: Math.max(range.endCol, left?.lastCol ?? 0, right?.lastCol ?? 0),
      };
    };
    this.root = build(0, sorted.length);
  }

  at(row: number, col: number): MergeRange | null {
    if (!this.root) return null;
    const find = (node: MergeTreeNode | null): MergeRange | null => {
      if (
        !node ||
        node.max < row ||
        node.firstRow > row ||
        col < node.firstCol ||
        col > node.lastCol
      ) {
        return null;
      }
      const r = node.range;
      const left = find(node.left);
      if (left) return left;
      if (r.row > row) return null;
      if (row <= r.endRow && col >= r.col && col <= r.endCol) return r;
      return find(node.right);
    };
    return find(this.root);
  }

  intersect(top: number, bottom: number, left: number, right: number): MergeRange[] {
    const ranges: MergeRange[] = [];
    const visit = (node: MergeTreeNode | null): void => {
      if (
        !node ||
        node.max < top ||
        node.firstRow > bottom ||
        node.lastCol < left ||
        node.firstCol > right
      ) {
        return;
      }
      const m = node.range;
      if (m.row <= bottom && m.endRow >= top && m.col <= right && m.endCol >= left) {
        ranges.push(m);
      }
      visit(node.left);
      visit(node.right);
    };
    visit(this.root);
    return ranges;
  }

  pointerCell(
    event: Pick<MouseEvent, 'type' | 'detail' | 'target' | 'clientX' | 'clientY'>,
    colLeft: (col: number) => number,
    rowTop: (row: number) => number,
  ): { readonly row: number; readonly col: number } | null {
    const target = event.target;
    const rawNode = target instanceof Element ? target.closest('.merged-cell') : null;
    const node = rawNode instanceof HTMLElement ? (rawNode as MergedCellElement) : null;
    const range = node?._xlMerge;
    if (!node || !range || (event.type === 'click' && !event.detail)) return null;
    const rect = node.getBoundingClientRect();
    const band = (
      firstStart: number,
      lastEnd: number,
      offset: number,
      edge: (idx: number) => number,
    ): number => {
      let first = firstStart;
      let last = lastEnd;
      const coordinate = edge(first) + offset;
      while (first < last) {
        const mid = Math.floor((first + last) / 2);
        if (edge(mid + 1) <= coordinate) first = mid + 1;
        else last = mid;
      }
      return first;
    };
    return {
      col: band(range.col, range.endCol, event.clientX - rect.left, colLeft),
      row: band(range.row, range.endRow, event.clientY - rect.top, rowTop),
    };
  }

  paintCrosshair(
    node: MergedCellElement,
    point: MergeCrosshairPoint | null | undefined,
    colLeft: (col: number) => number,
    rowTop: (row: number) => number,
  ): void {
    const range = node._xlMerge;
    if (!range) return;
    const clearImage = (): void => {
      if (node.style.getPropertyValue('--merge-band-image')) {
        node.style.removeProperty('--merge-band-image');
      }
    };
    const rawRows: readonly (readonly [number, number])[] =
      point?.rowSpans ?? (point?.row ? [[point.row, point.row]] : []);
    const rawCols: readonly (readonly [number, number])[] =
      point?.colSpans ?? (point?.col ? [[point.col, point.col]] : []);
    const clip = (
      spans: readonly (readonly [number, number])[],
      min: number,
      max: number,
    ): [number, number][] => {
      const ordered =
        spans.length > 1
          ? [...spans].sort((a, b) => a[0] - b[0] || a[1] - b[1])
          : spans;
      const out: [number, number][] = [];
      for (const [a, b] of ordered) {
        const start = Math.max(min, a);
        const end = Math.min(max, b);
        if (start <= end) {
          const prev = out[out.length - 1];
          if (prev && start <= prev[1] + 1) prev[1] = Math.max(prev[1], end);
          else out.push([start, end]);
        }
      }
      return out;
    };
    const rows = clip(rawRows, range.row, range.endRow);
    const cols = clip(rawCols, range.col, range.endCol);
    const inRow = rows.length > 0;
    const inCol = cols.length > 0;
    node.classList.toggle('merge-crosshair', inRow || inCol);
    if (!inRow && !inCol) {
      clearImage();
      return;
    }
    if (cols.length <= 1 && rows.length <= 1) {
      clearImage();
      const c = cols[0];
      const r = rows[0];
      const x = c ? colLeft(c[0]) : 0;
      const y = r ? rowTop(r[0]) : 0;
      const position = `${c ? x - colLeft(range.col) : 0}px 0, 0 ${r ? y - rowTop(range.row) : 0}px`;
      const size = `${c ? colLeft(c[1] + 1) - x : 0}px 100%, 100% ${r ? rowTop(r[1] + 1) - y : 0}px`;
      if (node.style.getPropertyValue('--merge-band-position') !== position) {
        node.style.setProperty('--merge-band-position', position);
      }
      if (node.style.getPropertyValue('--merge-band-size') !== size) {
        node.style.setProperty('--merge-band-size', size);
      }
      return;
    }
    const images: string[] = [];
    const positions: string[] = [];
    const sizes: string[] = [];
    const originX = colLeft(range.col);
    const originY = rowTop(range.row);
    for (const [c1, c2] of cols) {
      const x = colLeft(c1);
      const w = colLeft(c2 + 1) - x;
      if (w > 0) {
        images.push('linear-gradient(var(--crosshair-column-fill), var(--crosshair-column-fill))');
        positions.push(`${x - originX}px 0`);
        sizes.push(`${w}px 100%`);
      }
    }
    for (const [r1, r2] of rows) {
      const y = rowTop(r1);
      const h = rowTop(r2 + 1) - y;
      if (h > 0) {
        images.push('linear-gradient(var(--crosshair-row-fill), var(--crosshair-row-fill))');
        positions.push(`0 ${y - originY}px`);
        sizes.push(`100% ${h}px`);
      }
    }
    if (!images.length) {
      clearImage();
      node.classList.toggle('merge-crosshair', false);
      return;
    }
    const image = images.join(', ');
    const position = positions.join(', ');
    const size = sizes.join(', ');
    if (node.style.getPropertyValue('--merge-band-image') !== image) {
      node.style.setProperty('--merge-band-image', image);
    }
    if (node.style.getPropertyValue('--merge-band-position') !== position) {
      node.style.setProperty('--merge-band-position', position);
    }
    if (node.style.getPropertyValue('--merge-band-size') !== size) {
      node.style.setProperty('--merge-band-size', size);
    }
  }

  copyAnchor(
    row: number,
    col: number,
    visibility?: Pick<RowVisibility, 'count'> | null,
  ): MergeCopyAnchor | null {
    const m = this.at(row, col);
    if (m?.row !== row || m.col !== col) return null;
    const visibleRows = visibility ? 1 + visibility.count(row + 1, m.endRow) : m.endRow - row + 1;
    return { endRow: m.endRow, endCol: m.endCol, visibleRows };
  }

  expand(area: SelectionArea, visibility?: Pick<RowVisibility, 'visible' | 'previous'> | null): SelectionArea {
    let top = Math.min(area.row1, area.row2);
    let bottom = Math.max(area.row1, area.row2);
    let left = Math.min(area.col1, area.col2);
    let right = Math.max(area.col1, area.col2);

    const visit = (node: MergeTreeNode | null): boolean => {
      if (
        !node ||
        node.max < top ||
        node.firstRow > bottom ||
        node.lastCol < left ||
        node.firstCol > right
      ) {
        return false;
      }
      if (
        node.firstRow >= top &&
        node.max <= bottom &&
        node.firstCol >= left &&
        node.lastCol <= right
      ) {
        return false;
      }
      let expanded = false;
      const m = node.range;
      if (
        m.row <= bottom &&
        m.endRow >= top &&
        m.col <= right &&
        m.endCol >= left &&
        (!visibility || visibility.visible(m.row))
      ) {
        const endRow = visibility ? visibility.previous(m.endRow) : m.endRow;
        if (
          endRow >= top &&
          (m.row < top || endRow > bottom || m.col < left || m.endCol > right)
        ) {
          top = Math.min(top, m.row);
          bottom = Math.max(bottom, endRow);
          left = Math.min(left, m.col);
          right = Math.max(right, m.endCol);
          expanded = true;
        }
      }
      if (visit(node.left)) expanded = true;
      if (visit(node.right)) expanded = true;
      return expanded;
    };

    while (visit(this.root)) {
      // Expand until fixed point.
    }

    return {
      ...area,
      row1: area.row1 <= area.row2 ? top : bottom,
      row2: area.row1 <= area.row2 ? bottom : top,
      col1: area.col1 <= area.col2 ? left : right,
      col2: area.col1 <= area.col2 ? right : left,
    };
  }

  decorate(
    node: MergedCellElement,
    range: MergeRange | null | undefined,
    row: number,
    col: number,
    colLeft: (col: number) => number,
    rowTop: (row: number) => number,
    colWidth: (col: number) => number,
  ): void {
    if (!range && !node._xlMerge && !node._xlCovered) return;
    const covered = Boolean(range && (range.row !== row || range.col !== col));
    if (covered !== Boolean(node._xlCovered)) {
      node.style.display = covered ? 'none' : '';
      node._xlCovered = covered;
    }
    if (range && !covered) {
      const width = `${colLeft(range.endCol + 1) - colLeft(col)}px`;
      const height = `${rowTop(range.endRow + 1) - rowTop(row)}px`;
      if (node.style.width !== width) node.style.width = width;
      if (node.style.height !== height) node.style.height = height;
      node.style.zIndex = '2';
      node._xlMerge = range;
    } else if (node._xlMerge) {
      node.style.width = `${colWidth(col)}px`;
      node.style.height = '';
      node.style.zIndex = '';
      node._xlMerge = null;
      node.classList.toggle('merge-crosshair', false);
      if (node.style.getPropertyValue('--merge-band-image')) {
        node.style.removeProperty('--merge-band-image');
      }
    }
    node.classList.toggle('merged-cell', Boolean(range && !covered));
  }
}
