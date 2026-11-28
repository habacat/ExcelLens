/**
 * Shared-edge border conflict resolution and segmented border painting.
 * Every shared edge between adjacent cells is drawn exactly once.
 */
import type { VisualStyle } from '../contracts/workbook.ts';
import type { BorderLayerElement, BorderSegment, GridCellElement } from './state.ts';

const RANKS: Readonly<Record<string, number>> = {
  double: 6,
  solid: 5,
  dashed: 4,
  dotted: 3,
  groove: 2,
  ridge: 2,
};

export interface ParsedBorder {
  readonly css: string;
  readonly width: number;
  readonly kind: string;
  readonly color: string;
  readonly score: number;
}

export interface BorderCorner {
  readonly width: number;
  readonly height: number;
  readonly kind: 'solid' | 'double';
  readonly color: string;
}

export interface InnerBorderCorner {
  readonly corner: BorderCorner;
  readonly rightOffset?: number;
  readonly bottomOffset?: number;
}

const BORDER_CSS_PATTERN = /^([\d.]+)px\s+(solid|double|dashed|dotted|groove|ridge)\s+(.+)$/i;

const weights = new Map<string, number>();
const parsedBorders = new Map<string, ParsedBorder | null>();

export function parseBorder(value: string | undefined): ParsedBorder | null {
  if (!value || value === '0px' || value === 'none') return null;
  const cached = parsedBorders.get(value);
  if (cached !== undefined) return cached;
  const match = BORDER_CSS_PATTERN.exec(value);
  let result: ParsedBorder | null = null;
  if (match?.[1] && match[2] && match[3]) {
    const width = Number(match[1]);
    const color = match[3].trim();
    if (width > 0 && color) {
      const kind = match[2].toLowerCase();
      result = {
        css: value,
        width,
        kind,
        color,
        score: width * 100 + (RANKS[kind] ?? 1),
      };
    }
  }
  if (parsedBorders.size >= 512) parsedBorders.clear();
  parsedBorders.set(value, result);
  return result;
}

function score(value: string | undefined): number {
  if (!value) return 0;
  const parsed = parseBorder(value);
  if (parsed) return parsed.score;
  const cached = weights.get(value);
  if (cached !== undefined) return cached;
  const parts = value.split(/\s+/);
  const width = parseFloat(parts[0] ?? '') || 0.5;
  const kind = parts.find((p) => Object.hasOwn(RANKS, p)) ?? 'solid';
  const result = width * 100 + (RANKS[kind] ?? 1);
  if (weights.size >= 512) weights.clear();
  weights.set(value, result);
  return result;
}

export function chooseBorder(primary?: string, secondary?: string): string {
  return score(secondary) > score(primary) ? (secondary ?? '') : (primary ?? '');
}

export function resolveBorders(
  current: VisualStyle = {},
  right: VisualStyle = {},
  below: VisualStyle = {},
): { readonly right: string; readonly bottom: string } {
  return {
    right: chooseBorder(current.borderRight, right.borderLeft),
    bottom: chooseBorder(current.borderBottom, below.borderTop),
  };
}

/**
 * Resolves the 2D corner cap needed at a 4-seam vertex where `east` (horizontal)
 * and `south` (vertical) meet inside the bottom-right corner of the cell owning
 * `north` (right seam) and `west` (bottom seam).
 *
 * Returns `null` when either `east` or `south` is absent or when `north`/`west`
 * already solidly covers the `[X - south.width, X] x [Y - east.width, Y]` junction.
 */
export function resolveBorderCorner(
  north?: string,
  west?: string,
  east?: string,
  south?: string,
): BorderCorner | null {
  const e = parseBorder(east);
  const s = parseBorder(south);
  if (!e || !s) return null;
  const winner = s.score > e.score ? s : e;
  const doubleCorner = e.kind === 'double' && s.kind === 'double' && e.width >= 2 && s.width >= 2;
  const n = parseBorder(north);
  const w = parseBorder(west);
  const coveredBy = (existing: ParsedBorder | null, requiredSpan: number): boolean => {
    if (!existing || existing.width < requiredSpan) return false;
    if (doubleCorner) return existing.kind === 'double' && existing.score >= winner.score;
    if (existing.kind !== 'solid') return false;
    return (
      existing.score >= winner.score ||
      existing.color.toLowerCase() === winner.color.toLowerCase()
    );
  };
  if (coveredBy(n, s.width) || coveredBy(w, e.width)) return null;
  return {
    width: s.width,
    height: e.width,
    kind: doubleCorner ? 'double' : 'solid',
    color: winner.color,
  };
}

export function borderRuns(
  first: number,
  last: number,
  visibleEnd: number,
  next: (index: number) => number,
  styleAt: (index: number) => string,
  fallback: string,
  visibleStart = first,
): string | BorderSegment[] {
  const parts: BorderSegment[] = [];
  const add = (start: number, end: number, style: string): void => {
    if (start >= end) return;
    const prev = parts.at(-1);
    if (prev?.style === style) prev.end = end;
    else parts.push({ start, end, style });
  };
  let index = Math.max(first, Math.min(last + 1, visibleStart));
  add(first, index, fallback);
  while (index <= last && index <= visibleEnd) {
    const following = Math.min(last + 1, next(index + 1) || last + 1);
    add(index, following, styleAt(index));
    index = following;
  }
  add(index, last + 1, fallback);
  if (parts.length === 1 && parts[0]) return parts[0].style;
  return parts;
}

export function paintBorder(
  node: GridCellElement,
  side: 'right' | 'bottom',
  stroke: string | readonly BorderSegment[],
  offset: (index: number) => number,
  crossing = '',
  insetStroke = false,
): void {
  const mixed = Array.isArray(stroke);
  const property: 'borderRight' | 'borderBottom' = side === 'right' ? 'borderRight' : 'borderBottom';
  const insetDefault = !mixed && !stroke && Boolean(crossing) && crossing !== '0px';
  const insetSolid = !mixed && insetStroke && Boolean(stroke) && stroke !== '0px';
  const value = mixed || insetDefault || insetSolid ? '0px' : (stroke as string);

  if (property === 'borderRight') {
    if (node._xlborderRight !== value) {
      node._xlborderRight = value;
      if (value) node.style.borderRight = value;
      else node.style.removeProperty('border-right');
    }
  } else if (node._xlborderBottom !== value) {
    node._xlborderBottom = value;
    if (value) node.style.borderBottom = value;
    else node.style.removeProperty('border-bottom');
  }

  let layer: BorderLayerElement | null | undefined =
    property === 'borderRight' ? node._xlborderRightParts : node._xlborderBottomParts;

  if (!mixed && !insetDefault && !insetSolid) {
    if (layer) {
      layer.remove();
      if (property === 'borderRight') node._xlborderRightParts = null;
      else node._xlborderBottomParts = null;
    }
    return;
  }

  if (layer?.parentNode !== node) {
    layer = document.createElement('span');
    layer.setAttribute('aria-hidden', 'true');
    node.appendChild(layer);
    if (property === 'borderRight') node._xlborderRightParts = layer;
    else node._xlborderBottomParts = layer;
  }

  const className = insetDefault
    ? `grid-default-${side}`
    : insetSolid
      ? 'grid-inset-border'
      : 'grid-border-segments';
  if (layer.className !== className) {
    layer.className = className;
    if (!mixed && layer.children.length) layer.replaceChildren();
    if (!insetSolid) {
      layer.style.removeProperty('border-right');
      layer.style.removeProperty('border-bottom');
    }
  }
  if (insetSolid) {
    const strokeCss = stroke as string;
    if (layer.style[property] !== strokeCss) layer.style[property] = strokeCss;
    layer.style.removeProperty(property === 'borderRight' ? 'border-bottom' : 'border-right');
    return;
  }
  if (insetDefault || !Array.isArray(stroke)) return;

  const segments = stroke as readonly BorderSegment[];
  const firstSegment = segments[0];
  if (!firstSegment) return;
  const vertical = side === 'right';
  const origin = offset(firstSegment.start);
  layer._xlRuns = segments;
  segments.forEach(({ start, end, style }, i) => {
    const existing = layer.children[i];
    const line =
      existing instanceof HTMLElement
        ? existing
        : layer.appendChild(document.createElement('i'));
    line.style[side] = '0';
    if (vertical) {
      line.style.top = `${offset(start) - origin}px`;
      line.style.height = `${offset(end) - offset(start)}px`;
    } else {
      line.style.left = `${offset(start) - origin}px`;
      line.style.width = `${offset(end) - offset(start)}px`;
    }
    line.style[property] = style || 'var(--grid-default-seam)';
  });
  while (layer.children.length > segments.length) {
    layer.lastChild?.remove();
  }
}

function applyCornerStyle(cap: HTMLElement, corner: BorderCorner): void {
  if (corner.kind === 'double') {
    cap.style.backgroundColor = '';
    cap.style.borderRight = '';
    cap.style.borderBottom = '';
    cap.style.borderTop = `${corner.height}px double ${corner.color}`;
    cap.style.borderLeft = `${corner.width}px double ${corner.color}`;
  } else {
    cap.style.borderTop = '';
    cap.style.borderLeft = '';
    cap.style.borderRight = `${corner.width}px solid transparent`;
    cap.style.borderBottom = `${corner.height}px solid transparent`;
    cap.style.backgroundColor = corner.color;
  }
}

export function paintBorderCorner(
  node: GridCellElement,
  corner: BorderCorner | null,
): void {
  if (!corner) {
    if (node._xlCornerCap) {
      node._xlCornerCap.remove();
      node._xlCornerCap = null;
      node._xlCornerKey = '';
    }
    return;
  }
  const key = `${corner.width}|${corner.height}|${corner.kind}|${corner.color}`;
  let cap = node._xlCornerCap;
  if (cap?.parentNode === node && node._xlCornerKey === key) return;
  if (cap?.parentNode !== node) {
    cap = document.createElement('span');
    cap.className = 'grid-border-corner';
    cap.setAttribute('aria-hidden', 'true');
    node.appendChild(cap);
    node._xlCornerCap = cap;
  }
  applyCornerStyle(cap, corner);
  node._xlCornerKey = key;
}

export function paintInnerBorderCorners(
  node: GridCellElement,
  corners: readonly InnerBorderCorner[],
): void {
  if (!corners.length) {
    if (node._xlInnerCorners) {
      node._xlInnerCorners.remove();
      node._xlInnerCorners = null;
      node._xlInnerCornerKey = '';
    }
    return;
  }
  const key = corners
    .map(
      ({ corner, rightOffset, bottomOffset }) =>
        `${corner.width},${corner.height},${corner.kind},${corner.color},${rightOffset ?? ''},${bottomOffset ?? ''}`,
    )
    .join(';');
  let layer = node._xlInnerCorners;
  if (layer?.parentNode === node && node._xlInnerCornerKey === key) return;
  if (layer?.parentNode !== node) {
    layer = document.createElement('span');
    layer.className = 'grid-border-corners';
    layer.setAttribute('aria-hidden', 'true');
    node.appendChild(layer);
    node._xlInnerCorners = layer;
  }
  corners.forEach(({ corner, rightOffset, bottomOffset }, i) => {
    const existing = layer.children[i];
    const cap =
      existing instanceof HTMLElement
        ? existing
        : layer.appendChild(document.createElement('i'));
    if (cap.className !== 'grid-border-corner') cap.className = 'grid-border-corner';
    applyCornerStyle(cap, corner);
    cap.style.right = rightOffset !== undefined ? `calc(100% - ${rightOffset}px)` : '0';
    cap.style.bottom = bottomOffset !== undefined ? `calc(100% - ${bottomOffset}px)` : '0';
  });
  while (layer.children.length > corners.length) {
    layer.lastChild?.remove();
  }
  node._xlInnerCornerKey = key;
}

export type BorderSideCapitalized = 'Top' | 'Bottom' | 'Left' | 'Right';

export interface StyleLookupCache {
  get(key: Element): CSSStyleDeclaration | undefined;
  set(key: Element, value: CSSStyleDeclaration): unknown;
}

export function usedBorderWidth(
  cell: Element | null | undefined,
  side: BorderSideCapitalized,
  index: number,
  styles: StyleLookupCache,
): number {
  if (!cell) return 0;
  const gridCell = cell as GridCellElement;
  const parts =
    side === 'Right'
      ? gridCell._xlborderRightParts
      : side === 'Bottom'
        ? gridCell._xlborderBottomParts
        : null;
  let node: Element = cell;
  if (parts?.parentNode === cell) {
    if (parts.className === 'grid-border-segments') {
      const i = parts._xlRuns?.findIndex((run) => run.start <= index && index < run.end) ?? -1;
      const child = i >= 0 ? parts.children[i] : undefined;
      if (child) node = child;
    } else {
      node = parts;
    }
  }
  let css = styles.get(node);
  if (!css) {
    css = getComputedStyle(node);
    styles.set(node, css);
  }
  const widthStr =
    side === 'Top'
      ? css.borderTopWidth
      : side === 'Bottom'
        ? css.borderBottomWidth
        : side === 'Left'
          ? css.borderLeftWidth
          : css.borderRightWidth;
  return parseFloat(widthStr) || 0;
}

export const GridBorders = Object.freeze({
  parse: parseBorder,
  choose: chooseBorder,
  resolve: resolveBorders,
  corner: resolveBorderCorner,
  runs: borderRuns,
  paint: paintBorder,
  paintCorner: paintBorderCorner,
  paintInnerCorners: paintInnerBorderCorners,
  usedWidth: usedBorderWidth,
});
