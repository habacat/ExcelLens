/**
 * Visible selection perimeter & crosshair tail painter across normal and
 * frozen panes. Projects rectangles into each pane without spurious seams.
 */
import type { SelectionArea } from '../contracts/common.ts';

export interface FrameArea extends SelectionArea {
  readonly color?: string;
}

export interface SeamSegment {
  readonly from: number;
  readonly to: number;
  readonly offset: number;
}

export interface SeamEdges {
  readonly top: readonly SeamSegment[];
  readonly bottom: readonly SeamSegment[];
  readonly left: readonly SeamSegment[];
  readonly right: readonly SeamSegment[];
  readonly raster?: {
    readonly step: number;
    readonly origin: { readonly left: number; readonly top: number };
  };
}

export interface RangeFrameView {
  readonly rowHead: number;
  readonly header: number;
  readonly frozenWidth: number;
  readonly frozenHeight: number;
  readonly left: number;
  readonly top: number;
  readonly width: number;
  readonly height: number;
  colLeft(col: number): number;
  rowBounds(row: number, visual?: number): readonly [number, number];
  seams?(x1: number, x2: number, y1: number, y2: number): SeamEdges | null | undefined;
}

interface AxisPiece {
  readonly from: number;
  readonly to: number;
  readonly origin: number;
  readonly leading: boolean;
  readonly trailing: boolean;
}

interface PositionedElement extends HTMLElement {
  _position?: string;
}

interface PositionedSvgRect extends SVGRectElement {
  _kind?: string;
  _position?: string;
}

function axisPieces(
  start: number,
  end: number,
  header: number,
  frozen: number,
  scroll: number,
  limit: number,
): AxisPiece[] {
  const pieces: AxisPiece[] = [];
  const split = header + frozen;
  const project = (lo: number, hi: number, shift: number): void => {
    const first = header + start - shift;
    const last = header + end - shift;
    const from = Math.max(lo, first);
    const to = Math.min(hi, last);
    if (from < to) {
      pieces.push({
        from,
        to,
        origin: header - shift,
        leading: first >= lo,
        trailing: last <= hi,
      });
    }
  };
  if (frozen > 0) project(header, Math.min(split, limit), 0);
  project(split, limit, scroll);
  return pieces;
}

const SELECTION_WIDTH = 2;
const HANDLE_SIZE = 7;

interface SeamRun {
  readonly start: number;
  readonly end: number;
  readonly offset: number;
}

function paintSeams(
  frame: HTMLElement,
  edges: SeamEdges,
  col: AxisPiece,
  row: AxisPiece,
  showHandle: boolean,
): void {
  const half = SELECTION_WIDTH / 2;
  const ns = 'http://www.w3.org/2000/svg';
  const existingSvg = frame.firstElementChild;
  const svg =
    existingSvg instanceof SVGSVGElement
      ? existingSvg
      : frame.appendChild(document.createElementNS(ns, 'svg'));
  let count = 0;

  const rect = (x: number, y: number, w: number, h: number, kind = ''): void => {
    if (w <= 0 || h <= 0) return;
    const existing = svg.children[count++];
    const node = (
      existing instanceof SVGRectElement
        ? existing
        : svg.appendChild(document.createElementNS(ns, 'rect'))
    ) as PositionedSvgRect;
    if ((node._kind ?? '') !== kind) {
      if (kind) node.setAttribute('class', kind);
      else node.removeAttribute('class');
      node._kind = kind;
    }
    const key = `${x},${y},${w},${h}`;
    if (node._position === key) return;
    node.setAttribute('x', String(x));
    node.setAttribute('y', String(y));
    node.setAttribute('width', String(w));
    node.setAttribute('height', String(h));
    node._position = key;
  };

  const distance = (axis: AxisPiece, position: number): number => {
    if (!edges.raster) return position - axis.from;
    const { step, origin } = edges.raster;
    const base = axis === col ? origin.left : origin.top;
    return (Math.round((base + position) / step) - Math.round((base + axis.from) / step)) * step;
  };

  const runs = (segments: readonly SeamSegment[], axis: AxisPiece): SeamRun[] => {
    const parts: SeamRun[] = [];
    let cursor = 0;
    const length = distance(axis, axis.to);
    for (const s of segments) {
      const start = Math.max(0, distance(axis, s.from + axis.origin));
      const end = Math.min(length, distance(axis, s.to + axis.origin));
      if (end <= start) continue;
      if (start > cursor) parts.push({ start: cursor, end: start, offset: 0 });
      parts.push({ start, end, offset: s.offset });
      cursor = end;
    }
    if (cursor < length) parts.push({ start: cursor, end: length, offset: 0 });
    if (!parts.length) parts.push({ start: 0, end: length, offset: 0 });
    return parts;
  };

  const top = row.leading ? runs(edges.top, col) : null;
  const bottom = row.trailing ? runs(edges.bottom, col) : null;
  const left = col.leading ? runs(edges.left, row) : null;
  const right = col.trailing ? runs(edges.right, row) : null;
  const w = distance(col, col.to);
  const h = distance(row, row.to);

  const side = (
    parts: readonly SeamRun[] | null,
    horizontal: boolean,
    cross: number,
    start: number,
    end: number,
  ): void => {
    if (!parts) return;
    for (let i = 0; i < parts.length; i++) {
      const s = parts[i];
      if (!s) continue;
      const a = i === 0 ? start : s.start;
      const b = i === parts.length - 1 ? end : s.end;
      const offset = cross + s.offset - half;
      if (horizontal) rect(a, offset, b - a, SELECTION_WIDTH);
      else rect(offset, a, SELECTION_WIDTH, b - a);
      const next = parts[i + 1];
      if (next && Math.abs(next.offset - s.offset) > SELECTION_WIDTH) {
        const from = cross + Math.min(s.offset, next.offset);
        const length = Math.abs(next.offset - s.offset);
        if (horizontal) rect(b - half, from, SELECTION_WIDTH, length);
        else rect(from, b - half, length, SELECTION_WIDTH);
      }
    }
  };

  side(top, true, 0, left?.[0] ? left[0].offset - half : 0, w + (right?.[0] ? right[0].offset + half : 0));
  side(
    bottom,
    true,
    h,
    left?.at(-1) ? (left.at(-1)?.offset ?? 0) - half : 0,
    w + (right?.at(-1) ? (right.at(-1)?.offset ?? 0) + half : 0),
  );
  side(left, false, 0, top?.[0] ? top[0].offset + half : 0, h + (bottom?.[0] ? bottom[0].offset - half : 0));
  side(
    right,
    false,
    w,
    top?.at(-1) ? (top.at(-1)?.offset ?? 0) + half : 0,
    h + (bottom?.at(-1) ? (bottom.at(-1)?.offset ?? 0) - half : 0),
  );

  if (showHandle && right && bottom) {
    const x = w + (right.at(-1)?.offset ?? 0) - HANDLE_SIZE / 2;
    const y = h + (bottom.at(-1)?.offset ?? 0) - HANDLE_SIZE / 2;
    rect(x, y, HANDLE_SIZE, HANDLE_SIZE, 'range-corner');
    rect(x + 1, y + 1, HANDLE_SIZE - 2, HANDLE_SIZE - 2, 'range-corner-face');
  }
  while (svg.children.length > count) svg.lastElementChild?.remove();
}

export function paintRangeFrames(
  layer: HTMLElement,
  areas: readonly FrameArea[],
  view: RangeFrameView,
  stroke = SELECTION_WIDTH,
): void {
  let count = 0;
  const thickness = (piece: AxisPiece): number =>
    Math.min(stroke, (piece.to - piece.from) / (piece.leading && piece.trailing ? 2 : 1));
  const box = (className: string, x: number, y: number, geometry = ''): PositionedElement => {
    const existing = layer.children[count++];
    const node = (
      existing instanceof HTMLElement ? existing : layer.appendChild(document.createElement('i'))
    ) as PositionedElement;
    if (node.className !== className) node.className = className;
    const style = `left:${x}px;top:${y}px;${geometry}`;
    if (node._position !== style) {
      node.style.cssText = style;
      node._position = style;
    }
    return node;
  };

  for (const area of areas) {
    const x1 = view.colLeft(Math.min(area.col1, area.col2));
    const x2 = view.colLeft(Math.max(area.col1, area.col2) + 1);
    const x = axisPieces(x1, x2, view.rowHead, view.frozenWidth, view.left, view.width);
    const a = view.rowBounds(area.row1, area.visual1);
    const b = view.rowBounds(area.row2, area.visual2);
    const y1 = Math.min(a[0], b[0]);
    const y2 = Math.max(a[1], b[1]);
    const y = axisPieces(y1, y2, view.header, view.frozenHeight, view.top, view.height);
    const seams = x.length && y.length && !area.color ? view.seams?.(x1, x2, y1, y2) : null;

    for (const col of x) {
      for (const row of y) {
        if (!(row.leading || col.trailing || row.trailing || col.leading)) continue;
        const xStroke = thickness(col);
        const yStroke = thickness(row);
        const edges = [
          row.leading ? yStroke : 0,
          col.trailing ? xStroke : 0,
          row.trailing ? yStroke : 0,
          col.leading ? xStroke : 0,
        ]
          .map((n) => (n ? `${n}px` : '0'))
          .join(' ');
        const color = area.color ? `;--reference-color:${area.color}` : '';
        const frame = box(
          'range-frame',
          col.from,
          row.from,
          `width:${col.to - col.from}px;height:${row.to - row.from}px;border-width:${seams ? '0' : edges}${color}`,
        );
        const showHandle = !area.color && areas.length === 1 && col.trailing && row.trailing;
        if (seams) paintSeams(frame, seams, col, row, showHandle);
        else if (frame.childElementCount) frame.replaceChildren();
        if (showHandle && !seams) {
          const cx = col.to - xStroke / 2;
          const cy = row.to - yStroke / 2;
          const corner = box(
            'range-corner',
            cx - HANDLE_SIZE / 2,
            cy - HANDLE_SIZE / 2,
            `width:${HANDLE_SIZE}px;height:${HANDLE_SIZE}px`,
          );
          if (corner.childElementCount) corner.replaceChildren();
        }
      }
    }
  }
  while (layer.children.length > count) layer.lastElementChild?.remove();
}

interface SimpleSpan {
  from: number;
  to: number;
}

function mergePieces(pieces: readonly AxisPiece[]): SimpleSpan[] {
  if (pieces.length <= 1) return pieces.map((p) => ({ from: p.from, to: p.to }));
  const sorted = [...pieces].sort((a, b) => a.from - b.from || a.to - b.to);
  const first = sorted[0];
  if (!first) return [];
  const out: SimpleSpan[] = [{ from: first.from, to: first.to }];
  for (let i = 1; i < sorted.length; i++) {
    const p = sorted[i];
    const prev = out[out.length - 1];
    if (!p || !prev) continue;
    if (p.from <= prev.to) prev.to = Math.max(prev.to, p.to);
    else out.push({ from: p.from, to: p.to });
  }
  return out;
}

export function paintCrosshairTails(
  layer: HTMLElement,
  areas: readonly FrameArea[],
  view: RangeFrameView,
  dataWidth: number,
  dataHeight: number,
): void {
  const cutX = Math.max(view.rowHead + view.frozenWidth, view.rowHead + dataWidth - view.left);
  const cutY = Math.max(view.header + view.frozenHeight, view.header + dataHeight - view.top);
  const rowPieces: AxisPiece[] = [];
  const colPieces: AxisPiece[] = [];

  if (cutX < view.width) {
    for (const area of areas) {
      if (!area.row1 && !area.row2) continue;
      const a = view.rowBounds(area.row1, area.visual1);
      const b = view.rowBounds(area.row2, area.visual2);
      const y1 = Math.min(a[0], b[0]);
      const y2 = Math.max(a[1], b[1]);
      if (y2 > y1) {
        rowPieces.push(...axisPieces(y1, y2, view.header, view.frozenHeight, view.top, view.height));
      }
    }
  }
  if (cutY < view.height) {
    for (const area of areas) {
      if (!area.col1 && !area.col2) continue;
      const x1 = view.colLeft(Math.min(area.col1, area.col2));
      const x2 = view.colLeft(Math.max(area.col1, area.col2) + 1);
      if (x2 > x1) {
        colPieces.push(...axisPieces(x1, x2, view.rowHead, view.frozenWidth, view.left, view.width));
      }
    }
  }

  const rows = mergePieces(rowPieces);
  const cols = mergePieces(colPieces);
  const strip = (
    node: PositionedElement,
    className: string,
    x: number | undefined,
    y: number | undefined,
    w: number,
    h: number,
  ): void => {
    if (node.className !== className) node.className = className;
    node.hidden = !(w > 0 && h > 0);
    if (node.hidden) return;
    const style = `left:${x ?? 0}px;top:${y ?? 0}px;width:${w}px;height:${h}px`;
    if (node._position !== style) {
      node.style.cssText = style;
      node._position = style;
    }
  };
  const ensure = (i: number): PositionedElement => {
    const existing = layer.children[i];
    return existing instanceof HTMLElement ? existing : layer.appendChild(document.createElement('i'));
  };

  if (rows.length <= 1 && cols.length <= 1) {
    const row = rows[0];
    const col = cols[0];
    strip(ensure(0), 'row-tail', cutX, row?.from, view.width - cutX, row ? row.to - row.from : 0);
    strip(ensure(1), 'column-tail', col?.from, cutY, col ? col.to - col.from : 0, view.height - cutY);
    while (layer.children.length > 2) layer.lastElementChild?.remove();
    return;
  }
  let count = 0;
  for (const row of rows) {
    strip(ensure(count++), 'row-tail', cutX, row.from, view.width - cutX, row.to - row.from);
  }
  for (const col of cols) {
    strip(ensure(count++), 'column-tail', col.from, cutY, col.to - col.from, view.height - cutY);
  }
  while (layer.children.length > count) layer.lastElementChild?.remove();
}

export const RangeFrame = Object.freeze({
  paint: paintRangeFrames,
  paintCrosshair: paintCrosshairTails,
});
