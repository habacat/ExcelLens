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

const weights = new Map<string, number>();

function score(value: string | undefined): number {
  if (!value) return 0;
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
): void {
  const mixed = Array.isArray(stroke);
  const property: 'borderRight' | 'borderBottom' = side === 'right' ? 'borderRight' : 'borderBottom';
  const insetDefault = !mixed && !stroke && Boolean(crossing) && crossing !== '0px';
  const value = mixed || insetDefault ? '0px' : (stroke as string);

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

  if (!mixed && !insetDefault) {
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

  const className = insetDefault ? `grid-default-${side}` : 'grid-border-segments';
  if (layer.className !== className) {
    layer.className = className;
    if (insetDefault && layer.children.length) layer.replaceChildren();
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
  choose: chooseBorder,
  resolve: resolveBorders,
  runs: borderRuns,
  paint: paintBorder,
  usedWidth: usedBorderWidth,
});
