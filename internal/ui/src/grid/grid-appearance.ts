import { t as localize } from '../core/i18n.ts';
import type { ApiClient, MergeRange, ToastTone } from '../contracts/common.ts';
import type { CellData, RowData, VisualStyle } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireSelect } from '../core/dom.ts';
import { conditionalColor, conditionalVisual } from './conditional.ts';
import type { BorderEdgeContext } from './freeze.ts';
import { fontStack, isKnownFont } from './display-font.ts';
import {
  borderRuns,
  chooseBorder,
  paintBorder,
  paintBorderCorner,
  paintInnerBorderCorners,
  parseBorder,
  resolveBorderCorner,
  type BorderCorner,
  type InnerBorderCorner,
} from './grid-borders.ts';
import type { GridCellElement, WorkbookState } from './state.ts';
import { decodeStylesWire } from '../core/http-decoders.generated.ts';

const STYLE_ALIGN: Readonly<Record<string, string>> = {
  left: 'flex-start',
  center: 'center',
  right: 'flex-end',
  justify: 'space-between',
  distributed: 'space-around',
  centerContinuous: 'center',
};

const STYLE_VERTICAL: Readonly<Record<string, string>> = {
  top: 'flex-start',
  center: 'center',
  bottom: 'flex-end',
};

interface GridAppearanceDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly colName: (col: number) => string;
  readonly toast: (text: string, tone?: ToastTone) => void;
  readonly repaint: () => void;
  readonly activeMerge: (row: number, column: number) => MergeRange | null;
  readonly rowTop: (row: number) => number;
  readonly colLeft: (col: number) => number;
  readonly nextVisibleRow: (row: number) => number;
}

/** Shares source styles and border decisions across regular and frozen cells. */
export class GridAppearance {
  private readonly d: GridAppearanceDeps;

  constructor(deps: GridAppearanceDeps) {
    this.d = deps;
  }

  clearCache(): void {
    this.cellStyleCache.clear();
  }

  private readonly cellStyleCache = new Map<string, string>();

  async loadStylePalette(): Promise<void> {
    const { S, api, toast } = this.d;
    try {
      const response = await api('/api/styles', undefined, decodeStylesWire);
      S.styles = [...(response.styles ?? [])];
      if (response.richTextNotice) toast(response.richTextNotice, 'warning');
    } catch (error: unknown) {
      S.styles = [];
      toast(localize('ui.gridAppearance.originalFormatTemporarilyUnavailable', { detail: errorMessage(error) }), 'error');
    }
  }

  applyDisplayFont(node: HTMLElement, sourceName: string | undefined): void {
    const { S } = this.d;
    const chosen = S.fontOverride || (S.showStyles ? (sourceName ?? '') : '');
    if (chosen) node.style.fontFamily = fontStack(chosen);
    else node.style.removeProperty('font-family');
  }

  repaintDisplayFonts(): void {
    this.d.repaint();
  }

  setDisplayFontSelection(font: string): void {
    this.d.S.fontOverride = font;
    const picker = requireSelect('fontSelect');
    picker.value = font;
    const label = localize('ui.gridAppearance.displayFont', { detail: picker.selectedOptions[0]?.text || font });
    picker.title = label;
    const field = picker.closest('.ribbon-field');
    if (field instanceof HTMLElement) {
      field.classList.toggle('is-active', Boolean(font));
      field.title = label;
    }
  }

  async changeDisplayFont(font: string): Promise<void> {
    const { S, api, toast } = this.d;
    if (!S.indexReady || !S.sheet || (!isKnownFont(font) && font !== '')) return;
    this.setDisplayFontSelection(font);
    this.repaintDisplayFonts();
    const sheet = S.sheet;
    try {
      await api('/api/layout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sheet, kind: 'font', font }),
      });
    } catch (err: unknown) {
      toast(localize('ui.gridAppearance.fontSettingSavingFailed', { detail: errorMessage(err) }), 'error');
    }
  }

  applyCellStyle(node: HTMLElement, cell: Partial<CellData> | null | undefined): void {
    const { S } = this.d;
    if (!cell || node.style.cssText || cell.conditional) {
      this.writeCellStyle(node, cell);
      return;
    }
    const numeric =
      cell.type === 'n' || (!cell.type && Boolean(cell.raw) && Number.isFinite(Number(cell.raw)));
    const key = [
      cell.style ?? 0,
      S.showStyles,
      S.zoom,
      S.fontOverride,
      cell.valueColor ?? '',
      numeric,
    ].join('|');
    let css = this.cellStyleCache.get(key);
    if (css === undefined) {
      this.writeCellStyle(node, cell);
      css = node.style.cssText;
      if (this.cellStyleCache.size >= 512) this.cellStyleCache.clear();
      this.cellStyleCache.set(key, css);
    } else if (css) {
      node.style.cssText = css;
    }
  }

  writeCellStyle(node: HTMLElement, cell: Partial<CellData> | null | undefined): void {
    const { S } = this.d;
    const p = node.style;
    for (const property of ['--xl-fill', '--xl-color']) p.removeProperty(property);
    p.fontSize = '';
    p.fontWeight = '';
    p.fontStyle = '';
    p.textDecoration = '';
    p.textDecorationStyle = '';
    p.textUnderlinePosition = '';
    p.justifyContent = '';
    p.alignItems = '';
    p.textAlign = '';
    p.whiteSpace = '';
    p.wordBreak = '';
    if (!cell) {
      this.applyDisplayFont(node, '');
      return;
    }
    const visual = conditionalVisual(cell, S.styles, 0, S.showStyles);
    if (!S.showStyles || !visual) {
      this.applyDisplayFont(node, '');
      return;
    }
    if (visual.background) p.setProperty('--xl-fill', visual.background);
    const ink = conditionalColor(cell, visual);
    if (ink) p.setProperty('--xl-color', ink);
    this.applyDisplayFont(node, visual.fontName);
    if (visual.fontSize) p.fontSize = `${((visual.fontSize * (4 / 3) * S.zoom) / 100)}px`;
    if (visual.bold) p.fontWeight = '700';
    if (visual.italic) p.fontStyle = 'italic';
    if (visual.underline || visual.strike) {
      p.textDecoration = [visual.underline && 'underline', visual.strike && 'line-through']
        .filter(Boolean)
        .join(' ');
    }
    const underline = cell.conditional?.style?.font.underline || visual.underlineStyle || '';
    if (underline === 'double' || underline === 'doubleAccounting') {
      p.textDecorationStyle = 'double';
    }
    if (underline.endsWith('Accounting')) p.textUnderlinePosition = 'under';
    const horiz = visual.horizontal ? STYLE_ALIGN[visual.horizontal] : undefined;
    if (horiz) {
      p.justifyContent = horiz;
      if (visual.wrap && visual.horizontal) {
        p.textAlign = visual.horizontal === 'centerContinuous' ? 'center' : visual.horizontal;
      }
    } else if (
      (!visual.horizontal || visual.horizontal === 'general') &&
      (cell.type === 'n' || (!cell.type && Boolean(cell.raw) && Number.isFinite(Number(cell.raw))))
    ) {
      p.justifyContent = 'flex-end';
    }
    const vert = visual.vertical ? STYLE_VERTICAL[visual.vertical] : undefined;
    if (vert) p.alignItems = vert;
    if (visual.wrap) {
      p.whiteSpace = 'normal';
      p.wordBreak = 'break-word';
    }
  }

  visualForBorder(cell: CellData | null | undefined, row?: RowData | null): VisualStyle {
    const { S } = this.d;
    if (!S.showStyles) return {};
    return conditionalVisual(cell, S.styles, row?.style ?? 0) ?? {};
  }

  paintRowHeaderBorder(
    header: HTMLElement,
    cell?: CellData,
    row?: RowData,
    belowCell?: CellData,
    belowRow?: RowData,
  ): void {
    const node = header as GridCellElement;
    const own = this.visualForBorder(cell, row);
    const below = this.visualForBorder(belowCell, belowRow);
    const border = own.borderLeft ?? '';
    const east = chooseBorder(own.borderBottom, below.borderTop);
    const corner = resolveBorderCorner(border, '', east, below.borderLeft);
    header.classList.toggle('row-head-bordered', Boolean(border || corner));
    paintBorder(node, 'right', border, () => 0, corner ? '1px' : '', Boolean(corner && border));
    paintBorderCorner(node, corner);
  }

  private mergedVisualBorder(
    own: VisualStyle,
    range: MergeRange,
    models: ReadonlyMap<string, CellData> = this.d.S.cellMap,
  ): VisualStyle {
    const { S, colName } = this.d;
    const at = (r: number, c: number): VisualStyle => {
      const ref = `${colName(c)}${r}`;
      return this.visualForBorder(models.get(ref) ?? S.frozenCellMap.get(ref));
    };
    const tr = at(range.row, range.endCol);
    const bl = at(range.endRow, range.col);
    const br = at(range.endRow, range.endCol);
    return {
      ...own,
      borderTop: own.borderTop || tr.borderTop,
      borderLeft: own.borderLeft || bl.borderLeft,
      borderRight: tr.borderRight || br.borderRight || own.borderRight,
      borderBottom: bl.borderBottom || br.borderBottom || own.borderBottom,
    };
  }

  updateSharedBorders(
    node: GridCellElement,
    cell: CellData | null | undefined,
    row: RowData | null | undefined,
    rightCell: CellData | null | undefined,
    rightRow: RowData | null | undefined,
    belowCell: CellData | null | undefined,
    belowRow: RowData | null | undefined,
    edge: BorderEdgeContext = {},
  ): void {
    const { S, colName } = this.d;
    const r = Number(node.dataset.row);
    const c = Number(node.dataset.col);
    const merge = edge.mergeFree ? null : this.d.activeMerge(r, c);
    const anchor = merge?.row === r && merge.col === c;
    if (merge && !anchor) return;
    if (!S.showStyles) {
      paintBorder(node, 'right', '', (idx) => this.d.rowTop(idx));
      paintBorder(node, 'bottom', '', (idx) => this.d.colLeft(idx));
      paintBorderCorner(node, null);
      paintInnerBorderCorners(node, []);
      if (node._xlOuterLeft) {
        node.style.removeProperty('border-left');
        node._xlOuterLeft = '';
      }
      if (node._xlOuterTop) {
        node.style.removeProperty('border-top');
        node._xlOuterTop = '';
      }
      return;
    }
    if (edge.mergeFree && edge.nextCol && belowRow && !edge.outerTop && !edge.outerLeft) {
      const own = this.visualForBorder(cell, row);
      const right = this.visualForBorder(rightCell, rightRow);
      const below = this.visualForBorder(belowCell, belowRow);
      const x =
        chooseBorder(own.borderRight, right.borderLeft) ||
        (own.background || right.background ? '0px' : '');
      const y =
        chooseBorder(own.borderBottom, below.borderTop) ||
        (own.background || below.background ? '0px' : '');
      let corner: BorderCorner | null = null;
      if (right.borderBottom || below.borderRight || edge.belowRightCell || belowRow.style) {
        const belowRight = this.visualForBorder(edge.belowRightCell, belowRow);
        const east = chooseBorder(right.borderBottom, belowRight.borderTop);
        const south = chooseBorder(below.borderRight, belowRight.borderLeft);
        if (east && south) corner = resolveBorderCorner(x, y, east, south);
      }
      const cornerFlag = corner ? '1px' : '';
      const insetRight = Boolean(corner && x && x !== '0px');
      const insetBottom = Boolean(corner && y && y !== '0px');
      paintBorder(node, 'right', x, (idx) => this.d.rowTop(idx), y || cornerFlag, insetRight);
      paintBorder(node, 'bottom', y, (idx) => this.d.colLeft(idx), x || cornerFlag, insetBottom);
      paintBorderCorner(node, corner);
      paintInnerBorderCorners(node, []);
      if (node._xlOuterLeft) {
        node.style.removeProperty('border-left');
        node._xlOuterLeft = '';
      }
      if (node._xlOuterTop) {
        node.style.removeProperty('border-top');
        node._xlOuterTop = '';
      }
      return;
    }
    const models = edge.cellMap ?? S.cellMap;
    const own = anchor
      ? this.mergedVisualBorder(this.visualForBorder(cell, row), merge, models)
      : this.visualForBorder(cell, row);
    const endRow = merge?.endRow ?? r;
    const endCol = merge?.endCol ?? c;
    const rightCol = anchor
      ? (edge.columns?.find((col) => col > endCol) ?? endCol + 1)
      : (edge.nextCol ?? c + 1);
    const under = !anchor && belowRow ? belowRow.number : this.d.nextVisibleRow(endRow + 1) || endRow + 1;
    const opposite = (
      nr: number,
      nc: number,
      data: RowData | null | undefined,
      direct: CellData | null | undefined,
    ): VisualStyle => {
      const range = edge.mergeFree ? null : this.d.activeMerge(nr, nc);
      if (!range && direct !== undefined) return this.visualForBorder(direct, data);
      const rr = range?.row ?? nr;
      const cc = range?.col ?? nc;
      const ref = `${colName(cc)}${rr}`;
      const record = data?.number === rr ? data : edge.rows?.get(rr);
      const model =
        (rr === nr && cc === nc ? direct : null) ??
        models.get(ref) ??
        S.frozenCellMap.get(ref) ??
        record?.cells.find((item) => item.col === cc);
      const visual = this.visualForBorder(model, record);
      return range ? this.mergedVisualBorder(visual, range, models) : visual;
    };
    const directRight = endCol === c && edge.nextCol ? (rightCell ?? null) : undefined;
    const directBelow = endRow === r && belowRow ? (belowCell ?? null) : undefined;
    const directBelowRight =
      endRow === r && endCol === c && edge.nextCol && belowRow
        ? (edge.belowRightCell ?? null)
        : undefined;
    const seam = (
      primary: string | undefined,
      neighbor: VisualStyle,
      side: 'borderLeft' | 'borderTop',
    ): string => chooseBorder(primary, neighbor[side]) || (own.background || neighbor.background ? '0px' : '');
    const rightAt = (rr: number): string =>
      seam(
        own.borderRight,
        opposite(rr, rightCol, rr === r ? rightRow : null, rr === r ? directRight : undefined),
        'borderLeft',
      );
    const bottomAt = (cc: number): string =>
      seam(
        own.borderBottom,
        opposite(under, cc, belowRow, cc === c ? directBelow : undefined),
        'borderTop',
      );
    const fallback = own.background ? '0px' : '';
    const right =
      endRow === r
        ? rightAt(r)
        : borderRuns(
            r,
            endRow,
            edge.lastRow ?? endRow,
            (idx) => this.d.nextVisibleRow(idx),
            rightAt,
            own.borderRight || fallback,
            edge.firstRow,
          );
    const bottom =
      endCol === c
        ? bottomAt(c)
        : borderRuns(
            c,
            endCol,
            edge.columns?.at(-1) ?? endCol,
            (i) => i,
            bottomAt,
            own.borderBottom || fallback,
            edge.columns?.[0],
          );
    const horizontalBetween = (
      topRow: number,
      botRow: number,
      colIndex: number,
      topRowData: RowData | null | undefined,
      topDirect: CellData | null | undefined,
      botRowData: RowData | null | undefined,
      botDirect: CellData | null | undefined,
    ): string => {
      const topMerge = edge.mergeFree ? null : this.d.activeMerge(topRow, colIndex);
      const botMerge = edge.mergeFree ? null : this.d.activeMerge(botRow, colIndex);
      if (topMerge && topMerge === botMerge) return '';
      const topHasBottom = !topMerge || topMerge.endRow < botRow;
      const botHasTop = !botMerge || botMerge.row > topRow;
      if (!topHasBottom && !botHasTop) return '';
      const topVisual = topHasBottom ? opposite(topRow, colIndex, topRowData, topDirect) : {};
      const botVisual = botHasTop ? opposite(botRow, colIndex, botRowData, botDirect) : {};
      return chooseBorder(topVisual.borderBottom, botVisual.borderTop);
    };
    const verticalBetween = (
      rowIndex: number,
      leftCol: number,
      nextColIndex: number,
      rowData: RowData | null | undefined,
      leftDirect: CellData | null | undefined,
      rightDirect: CellData | null | undefined,
    ): string => {
      const leftMerge = edge.mergeFree ? null : this.d.activeMerge(rowIndex, leftCol);
      const rightMerge = edge.mergeFree ? null : this.d.activeMerge(rowIndex, nextColIndex);
      if (leftMerge && leftMerge === rightMerge) return '';
      const leftHasRight = !leftMerge || leftMerge.endCol < nextColIndex;
      const rightHasLeft = !rightMerge || rightMerge.col > leftCol;
      if (!leftHasRight && !rightHasLeft) return '';
      const leftVisual = leftHasRight ? opposite(rowIndex, leftCol, rowData, leftDirect) : {};
      const rightVisual = rightHasLeft ? opposite(rowIndex, nextColIndex, rowData, rightDirect) : {};
      return chooseBorder(leftVisual.borderRight, rightVisual.borderLeft);
    };
    const rightEnd = typeof right === 'string' ? right : (right.at(-1)?.style ?? '');
    const bottomEnd = typeof bottom === 'string' ? bottom : (bottom.at(-1)?.style ?? '');
    const cornerEast = horizontalBetween(
      endRow,
      under,
      rightCol,
      endRow === r ? rightRow : null,
      endRow === r ? directRight : undefined,
      belowRow,
      directBelowRight,
    );
    const cornerSouth = cornerEast
      ? verticalBetween(
          under,
          endCol,
          rightCol,
          belowRow,
          endCol === c ? directBelow : undefined,
          directBelowRight,
        )
      : '';
    const corner =
      cornerEast && cornerSouth
        ? resolveBorderCorner(rightEnd, bottomEnd, cornerEast, cornerSouth)
        : null;
    const innerCorners: InnerBorderCorner[] = [];
    if (Array.isArray(right)) {
      const originY = this.d.rowTop(r);
      for (let i = 0; i < right.length - 1; i++) {
        const seg = right[i];
        const nextSeg = right[i + 1];
        if (!seg || !nextSeg || !parseBorder(nextSeg.style)) continue;
        const prevRow = Math.max(seg.start, nextSeg.start - 1);
        const eastInner = horizontalBetween(
          prevRow,
          nextSeg.start,
          rightCol,
          prevRow === r ? rightRow : null,
          prevRow === r ? directRight : undefined,
          null,
          undefined,
        );
        const inner = eastInner
          ? resolveBorderCorner(seg.style, '', eastInner, nextSeg.style)
          : null;
        if (inner) {
          innerCorners.push({
            corner: inner,
            bottomOffset: this.d.rowTop(nextSeg.start) - originY,
          });
        }
      }
    }
    if (Array.isArray(bottom)) {
      const originX = this.d.colLeft(c);
      for (let i = 0; i < bottom.length - 1; i++) {
        const seg = bottom[i];
        const nextSeg = bottom[i + 1];
        if (!seg || !nextSeg || !parseBorder(nextSeg.style)) continue;
        const prevCol = Math.max(seg.start, nextSeg.start - 1);
        const southInner = verticalBetween(
          under,
          prevCol,
          nextSeg.start,
          belowRow,
          prevCol === c ? directBelow : undefined,
          undefined,
        );
        const inner = southInner
          ? resolveBorderCorner('', seg.style, nextSeg.style, southInner)
          : null;
        if (inner) {
          innerCorners.push({
            corner: inner,
            rightOffset: this.d.colLeft(nextSeg.start) - originX,
          });
        }
      }
    }
    const left = edge.outerLeft && this.d.colLeft(c) === 0 ? (own.borderLeft ?? '') : '';
    const top = edge.outerTop ? (own.borderTop ?? '') : '';
    const bottomCross = typeof bottom === 'string' ? bottom : '';
    const rightCross = typeof right === 'string' ? right : '';
    const cornerFlag = corner ? '1px' : '';
    const insetRight = typeof right === 'string' && Boolean(corner && right && right !== '0px');
    const insetBottom = typeof bottom === 'string' && Boolean(corner && bottom && bottom !== '0px');
    paintBorder(
      node,
      'right',
      right,
      (idx) => this.d.rowTop(idx),
      top || bottomCross || cornerFlag,
      insetRight,
    );
    paintBorder(
      node,
      'bottom',
      bottom,
      (idx) => this.d.colLeft(idx),
      left || rightCross || cornerFlag,
      insetBottom,
    );
    paintBorderCorner(node, corner);
    paintInnerBorderCorners(node, innerCorners);
    if (node._xlOuterLeft !== left) {
      node._xlOuterLeft = left;
      if (left) node.style.borderLeft = left;
      else node.style.removeProperty('border-left');
    }
    if (node._xlOuterTop !== top) {
      node._xlOuterTop = top;
      if (top) node.style.borderTop = top;
      else node.style.removeProperty('border-top');
    }
  }
}
