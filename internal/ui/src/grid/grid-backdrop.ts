/**
 * Grid backdrop pane clipping, fade masks, and canvas fallback painting for
 * unmounted off-tile cells during rapid scroll.
 */
import type { MergeRange } from '../contracts/common.ts';
import type { VisualStyle } from '../contracts/workbook.ts';
import {
  chooseBorder,
  parseBorder,
  resolveBorderCorner,
  type BorderCorner,
  type ParsedBorder,
} from './grid-borders.ts';
import type { ViewportBounds, WorkbookState } from './state.ts';

export interface BackdropBounds {
  readonly width: number;
  readonly height: number;
  readonly rowHead: number;
  readonly header: number;
  readonly pinWidth: number;
  readonly pinHeight: number;
  readonly right: number;
  readonly bottom: number;
  readonly fixedRight: number;
  readonly fixedBottom: number;
}

export interface BackdropPane {
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
  readonly right: number;
  readonly bottom: number;
}

export function gridBackdropPanes(bounds: BackdropBounds): BackdropPane[] {
  const { width, height, rowHead, header, pinWidth, pinHeight, right, bottom, fixedRight, fixedBottom } =
    bounds;
  const xs: readonly (readonly [number, number, boolean])[] = [
    [rowHead, pinWidth, true],
    [rowHead + pinWidth, Math.max(0, width - rowHead - pinWidth), false],
  ];
  const ys: readonly (readonly [number, number, boolean])[] = [
    [header, pinHeight, true],
    [header + pinHeight, Math.max(0, height - header - pinHeight), false],
  ];
  const panes: BackdropPane[] = [];
  for (const [x, w, fixedX] of xs) {
    for (const [y, h, fixedY] of ys) {
      if (w <= 0 || h <= 0) continue;
      panes.push({
        x,
        y,
        width: w,
        height: h,
        right: Math.max(-96, Math.min(w + 96, (fixedX ? fixedRight : right) - x)),
        bottom: Math.max(-80, Math.min(h + 80, (fixedY ? fixedBottom : bottom) - y)),
      });
    }
  }
  return panes;
}

export function paintBackdrop(backdrop: HTMLElement, bounds: BackdropBounds): void {
  const right = Math.max(-96, Math.min(bounds.width, bounds.right));
  const bottom = Math.max(-80, Math.min(bounds.height, bounds.bottom));
  const vars: readonly (readonly [string, number])[] = [
    ['header', bounds.header],
    ['row-head', bounds.rowHead],
    ['data-right', right],
    ['data-bottom', bottom],
  ];
  for (const [name, value] of vars) {
    const property = `--backdrop-${name}`;
    const px = `${value}px`;
    if (backdrop.style.getPropertyValue(property) !== px) {
      backdrop.style.setProperty(property, px);
    }
  }
  const panes = gridBackdropPanes(bounds);
  const masks: string[] = [];
  const sizes: string[] = [];
  const positions: string[] = [];
  for (const pane of panes) {
    const fades: readonly (readonly ['right' | 'bottom', number, number])[] = [
      ['right', pane.right, 96],
      ['bottom', pane.bottom, 80],
    ];
    for (const [direction, start, fade] of fades) {
      if (start >= (direction === 'right' ? pane.width : pane.height)) continue;
      masks.push(`linear-gradient(to ${direction},transparent ${start}px,#000 ${start + fade}px)`);
      sizes.push(`${pane.width}px ${pane.height}px`);
      positions.push(`${pane.x}px ${pane.y}px`);
    }
  }
  const styles: readonly (readonly [string, string])[] = [
    ['mask-image', masks.join(',') || 'linear-gradient(transparent,transparent)'],
    ['mask-size', sizes.join(',') || '100% 100%'],
    ['mask-position', positions.join(',') || '0px 0px'],
  ];
  for (const [key, value] of styles) {
    if (backdrop.style.getPropertyValue(key) !== value) {
      backdrop.style.setProperty(key, value);
    }
  }
}

export interface PaintMissingTheme {
  readonly paper: string;
  readonly neutral: string;
  readonly line: string;
  readonly surface: string;
  readonly muted: string;
  readonly dark: boolean;
}

export interface PaintMissingParams<TRecord> {
  readonly ctx: CanvasRenderingContext2D;
  readonly bounds?: ViewportBounds | null;
  readonly pinned: { readonly rows: number; readonly cols: number };
  readonly top: number;
  readonly left: number;
  readonly minVisual: number;
  readonly maxVisual: number;
  readonly colsToPaint: readonly number[];
  readonly bufferedColumns?: ReadonlySet<number> | null;
  readonly scrollLeft: number;
  readonly w: number;
  readonly h: number;
  readonly hh: number;
  readonly rowHead: number;
  readonly state: Pick<
    WorkbookState,
    'showStyles' | 'sourceGridLines' | 'maxCol' | 'hiddenColumns' | 'filtered'
  >;
  readonly scrollTop: number;
  readonly sourceRowTop: (row: number) => number;
  readonly colWidth: (col: number) => number;
  readonly colLeft: (col: number) => number;
  readonly rowHeight: (row: number) => number;
  readonly rowTop: (row: number) => number;
  readonly nextVisibleRow: (row: number) => number;
  readonly rowVisible: (row: number) => boolean;
  readonly lookup: (visual: number, col: number) => TRecord | null;
  readonly mergeAt: (row: number, col: number) => MergeRange | null;
  readonly styleFor: (record: TRecord, col: number, row: number) => VisualStyle | null;
  readonly theme: PaintMissingTheme;
}

export function paintMissingCells<TRecord>(params: PaintMissingParams<TRecord>): void {
  const {
    ctx,
    bounds,
    pinned,
    top,
    left,
    minVisual,
    maxVisual,
    colsToPaint,
    bufferedColumns,
    scrollLeft,
    w,
    h,
    hh,
    rowHead,
    state: S,
    scrollTop,
    sourceRowTop,
    colWidth,
    colLeft,
    rowHeight,
    rowTop,
    nextVisibleRow,
    rowVisible,
    lookup,
    mergeAt,
    styleFor,
    theme,
  } = params;

  const { paper, neutral, line, surface, muted, dark } = theme;
  const drawGrid = !S.showStyles || S.sourceGridLines;
  const lines = new Map<ParsedBorder, Path2D>();
  const corners: { readonly x: number; readonly y: number; readonly corner: BorderCorner }[] = [];
  const labels: { readonly r: number; readonly y: number; readonly height: number }[] = [];

  const addLine = (
    css: string | null | undefined,
    xStart: number,
    yStart: number,
    xEnd: number,
    yEnd: number,
    vertical: boolean,
  ): void => {
    const stroke = parseBorder(css ?? undefined);
    if (!stroke) return;
    let path = lines.get(stroke);
    if (!path) {
      path = new Path2D();
      lines.set(stroke, path);
    }
    const inset = stroke.width / 2;
    let x = xStart;
    let y = yStart;
    let endX = xEnd;
    let endY = yEnd;
    if (vertical) {
      x = x - inset;
      endX = x;
    } else {
      y = y - inset;
      endY = y;
    }
    path.moveTo(x, y);
    path.lineTo(endX, endY);
  };

  interface ColumnPaintMeta {
    readonly c: number;
    readonly x: number;
    readonly end: number;
    readonly mounted: boolean;
  }
  const meta: ColumnPaintMeta[] = [];
  for (const c of colsToPaint) {
    if (c > S.maxCol || S.hiddenColumns.has(c)) continue;
    const x = rowHead + colLeft(c) - scrollLeft;
    const end = x + colWidth(c);
    if (end > left && x < w) {
      meta.push({
        c,
        x,
        end,
        mounted: Boolean(
          bounds &&
            c >= bounds.startCol &&
            c <= bounds.endCol &&
            (!bufferedColumns || bufferedColumns.has(c)),
        ),
      });
    }
  }

  const filteredOffset = S.filtered ? sourceRowTop(pinned.rows + 1) : 0;
  const gridLine = `1px solid ${line}`;
  const styles = new Map<string, VisualStyle | null>();
  const sample = (r: number, c: number): VisualStyle | null => {
    const key = `${r},${c}`;
    const cached = styles.get(key);
    if (cached !== undefined) return cached;
    const record = lookup(r, c);
    const style = record !== null ? styleFor(record, c, r) : null;
    styles.set(key, style);
    return style;
  };

  ctx.save();
  ctx.beginPath();
  ctx.rect(left, top, Math.max(0, w - left), Math.max(0, h - top));
  ctx.clip();

  for (
    let r = minVisual;
    r <= maxVisual;
    r = S.filtered ? r + 1 : nextVisibleRow(r + 1) || Infinity
  ) {
    if (!S.filtered && !rowVisible(r)) continue;
    const y = hh + rowTop(r) + filteredOffset - scrollTop;
    const endY = y + rowHeight(r);
    if (endY <= top || y >= h) continue;
    const rowMounted = Boolean(bounds && r >= bounds.displayStart && r <= bounds.displayEnd);
    let missing = false;
    for (let i = 0; i < meta.length; i++) {
      const colMeta = meta[i];
      if (!colMeta) continue;
      const { c, x, end, mounted } = colMeta;
      if (rowMounted && mounted) continue;
      const merge = mergeAt(r, c);
      const anchorR = merge?.row ?? r;
      const anchorC = merge?.col ?? c;
      if (
        merge &&
        bounds &&
        anchorR >= bounds.displayStart &&
        anchorR <= bounds.displayEnd &&
        anchorC >= bounds.startCol &&
        anchorC <= bounds.endCol &&
        (!bufferedColumns || bufferedColumns.has(anchorC))
      ) {
        continue;
      }
      missing = true;
      const own = sample(anchorR, anchorC);
      const fill = own?.background;
      ctx.fillStyle = own
        ? fill && dark
          ? `color-mix(in srgb, ${fill} 14%, ${paper})`
          : fill || paper
        : neutral;
      ctx.fillRect(x, y, end - x, endY - y);
      const edge = merge ? sample(r, c) : own;
      const right = meta[i + 1]?.c ?? c + 1;
      const below = S.filtered ? r + 1 : nextVisibleRow(r + 1) || r + 1;

      const rightStyle = sample(r, right);
      const belowStyle = sample(below, c);
      const explicitRight =
        !merge || right > merge.endCol
          ? chooseBorder(edge?.borderRight || own?.borderRight, rightStyle?.borderLeft)
          : '';
      const explicitBottom =
        !merge || below > merge.endRow
          ? chooseBorder(edge?.borderBottom || own?.borderBottom, belowStyle?.borderTop)
          : '';
      if (!merge || right > merge.endCol) {
        addLine(
          explicitRight || (!fill && !rightStyle?.background && drawGrid ? gridLine : null),
          end,
          y,
          end,
          endY,
          true,
        );
      }
      if (!merge || below > merge.endRow) {
        addLine(
          explicitBottom || (!fill && !belowStyle?.background && drawGrid ? gridLine : null),
          x,
          endY,
          end,
          endY,
          false,
        );
      }
      if (!merge || (right > merge.endCol && below > merge.endRow)) {
        const trMerge = mergeAt(r, right);
        const blMerge = mergeAt(below, c);
        const brMerge = mergeAt(below, right);
        const belowRightStyle = sample(below, right);
        const east =
          trMerge && trMerge === brMerge
            ? ''
            : chooseBorder(
                !trMerge || trMerge.endRow < below ? rightStyle?.borderBottom : undefined,
                !brMerge || brMerge.row > r ? belowRightStyle?.borderTop : undefined,
              );
        const south =
          east && !(blMerge && blMerge === brMerge)
            ? chooseBorder(
                !blMerge || blMerge.endCol < right ? belowStyle?.borderRight : undefined,
                !brMerge || brMerge.col > c ? belowRightStyle?.borderLeft : undefined,
              )
            : '';
        const corner = east && south ? resolveBorderCorner(explicitRight, explicitBottom, east, south) : null;
        if (corner) corners.push({ x: end, y: endY, corner });
      }
      const leftPen = parseBorder(own?.borderLeft);
      if (colLeft(c) === 0 && leftPen) {
        addLine(own?.borderLeft, x + leftPen.width, y, x, endY, true);
      }
      const topPen = parseBorder(own?.borderTop);
      if (rowTop(r) === 0 && topPen) {
        addLine(own?.borderTop, x, y + topPen.width, end, y, false);
      }
    }
    if (scrollLeft === 0 && missing && !rowMounted && !S.filtered && r > pinned.rows) {
      labels.push({ r, y, height: endY - y });
    }
  }

  for (const [stroke, path] of lines) {
    ctx.strokeStyle = stroke.color;
    ctx.lineWidth = stroke.width;
    ctx.setLineDash(
      stroke.kind === 'dashed'
        ? [3 * stroke.width, 2 * stroke.width]
        : stroke.kind === 'dotted'
          ? [stroke.width, stroke.width]
          : [],
    );
    if (stroke.kind === 'double' && stroke.width >= 2) {
      ctx.lineWidth = stroke.width / 3;
      ctx.save();
      ctx.translate(-stroke.width / 3, -stroke.width / 3);
      ctx.stroke(path);
      ctx.restore();
      ctx.save();
      ctx.translate(stroke.width / 3, stroke.width / 3);
      ctx.stroke(path);
      ctx.restore();
    } else {
      ctx.stroke(path);
    }
  }
  for (const { x: cx, y: cy, corner } of corners) {
    ctx.fillStyle = corner.color;
    if (corner.kind === 'double') {
      const tw = corner.width / 3;
      const th = corner.height / 3;
      ctx.fillRect(cx - corner.width, cy - corner.height, corner.width, th);
      ctx.fillRect(cx - corner.width, cy - corner.height, tw, corner.height);
      ctx.fillRect(cx - tw, cy - th, tw, th);
    } else {
      ctx.fillRect(cx - corner.width, cy - corner.height, corner.width, corner.height);
    }
  }
  ctx.restore();

  ctx.font = '10px Consolas, monospace';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  for (const { r, y, height } of labels) {
    ctx.fillStyle = surface;
    ctx.fillRect(0, Math.max(top, y), rowHead, Math.min(h, y + height) - Math.max(top, y));
    ctx.fillStyle = muted;
    ctx.fillText(String(r), rowHead - 8, y + height / 2);
  }
}

export const GridBackdrop = Object.freeze({
  paintMissing: paintMissingCells,
  panes: gridBackdropPanes,
  paint: paintBackdrop,
});
