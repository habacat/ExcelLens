/**
 * Detached, bounded paper painter for print preview and print manuscript export.
 * Source CellData and style IDs remain immutable; the shared CellPresenter owns
 * display values, rich text, and conditional formatting ink.
 */
import { t as localize } from '../core/i18n.ts';
import type { MergeRange } from '../contracts/common.ts';
import type { PrintingLimits, RichTextLimits } from '../contracts/formats.ts';
import type { CellData, RichFont, VisualStyle } from '../contracts/workbook.ts';
import { columnName } from '../core/cell-address.ts';
import {
  CellPresenter,
  isTextCell,
  type ColumnLayoutItem,
} from '../grid/cell-view.ts';
import { conditionalColor } from '../grid/conditional.ts';
import {
  chooseBorder,
  parseBorder,
  resolveBorderCorner,
  resolveBorders,
} from '../grid/grid-borders.ts';
import { richFontProperties } from '../grid/richtext.ts';
import type { GridCellElement } from '../grid/state.ts';
import {
  printPixels,
  printSourceFlag,
  printUnits,
  projectPrintPage,
  type PrintPageResponse,
  type PrintSummaryResponse,
  type ProjectedPrintAxis,
  type RenderedPrintPage,
} from './printing-model.ts';

export interface PrintingRendererOptions {
  readonly limits: PrintingLimits;
  readonly richLimits?: RichTextLimits | null;
  readonly fontStack: (name: string) => string;
}

interface PrintCellElement extends GridCellElement {
  _xlSourceCell?: CellData | null;
}

interface MergedColumnSpan {
  readonly first: number;
  readonly last: number;
  readonly merge: MergeRange;
}

interface EdgeSegment {
  readonly side: 'left' | 'right' | 'top' | 'bottom';
  readonly css: string;
}

interface EdgeEvent {
  readonly at: bigint;
  readonly segment: EdgeSegment;
  readonly start: boolean;
}

interface EdgeGroup {
  readonly vertical: boolean;
  readonly position: bigint;
  readonly events: EdgeEvent[];
}

const HORIZONTAL_JUSTIFY: Readonly<Record<string, string>> = {
  left: 'flex-start',
  right: 'flex-end',
  center: 'center',
  centerContinuous: 'center',
  justify: 'space-between',
  distributed: 'space-around',
};

const VERTICAL_ALIGN: Readonly<Record<string, string>> = {
  top: 'flex-start',
  center: 'center',
  bottom: 'flex-end',
};

function makeDiv(cls: string, text = ''): HTMLDivElement {
  const n = document.createElement('div');
  n.className = cls;
  n.textContent = text;
  return n;
}

function putPx(
  node: HTMLElement,
  values: Partial<Record<'left' | 'top' | 'width' | 'height', number>>,
): void {
  if (values.left !== undefined) node.style.left = `${values.left}px`;
  if (values.top !== undefined) node.style.top = `${values.top}px`;
  if (values.width !== undefined) node.style.width = `${values.width}px`;
  if (values.height !== undefined) node.style.height = `${values.height}px`;
}

function compareBigInt(a: bigint, b: bigint): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function lowerBoundNumber(values: readonly number[], target: number): number {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = (low + high) >>> 1;
    const item = values[middle];
    if (item !== undefined && item < target) low = middle + 1;
    else high = middle;
  }
  return low;
}

function lowerBoundMerge(values: readonly MergedColumnSpan[], target: number): number {
  let low = 0;
  let high = values.length;
  while (low < high) {
    const middle = (low + high) >>> 1;
    const item = values[middle];
    if (item !== undefined && item.first < target) low = middle + 1;
    else high = middle;
  }
  return low;
}

class PrintPresenter extends CellPresenter {
  override style(cell: CellData | null | undefined, rowStyle = 0): VisualStyle {
    const base = super.style(cell, rowStyle) ?? {};
    return {
      ...base,
      fontSize: base.fontSize || 11,
      fontName: base.fontName || 'Arial',
    };
  }
}

export class PrintingRenderer {
  private readonly limits: PrintingLimits;
  private readonly richLimits?: RichTextLimits | null;
  private readonly fontStack: (name: string) => string;

  constructor(options: PrintingRendererOptions) {
    this.limits = options.limits;
    this.richLimits = options.richLimits;
    this.fontStack = options.fontStack;
  }

  build(summaryValue: PrintSummaryResponse, pageValue: PrintPageResponse): RenderedPrintPage {
    const s = summaryValue.summary;
    const c = pageValue.content;
    const v = projectPrintPage(summaryValue, pageValue, this.limits);
    const warnings = new Set<string>(c.notices);

    const paper = makeDiv('print-paper');
    const body = makeDiv('print-body');
    const data = makeDiv('print-data');
    const edges = makeDiv('print-edges');
    paper.setAttribute('aria-label', localize('ui.printingRender.sourceTablePage', { page: pageValue.page + 1 }));
    putPx(paper, { width: v.width, height: v.height });
    putPx(body, { left: v.x, top: v.y, width: v.contentWidth, height: v.contentHeight });
    putPx(data, { width: v.contentWidth, height: v.contentHeight });
    body.append(data, edges);
    paper.append(body);

    const stylesPalette: VisualStyle[] = [];
    for (const [key, styleVal] of Object.entries(c.styles)) {
      const idx = Number(key);
      if (Number.isSafeInteger(idx) && idx >= 0) {
        stylesPalette[idx] = styleVal;
      }
    }

    const models: (CellData | null)[] = v.cells.map(({ item }) =>
      item.cell ? { ...item.cell, style: item.style } : null,
    );

    const rowValues = new Map<number, Record<number, CellData | undefined>>();
    const layouts = new Map<number, ColumnLayoutItem[]>();
    const columnAt = new Map<string, ProjectedPrintAxis>();
    const columnPositions = new Map<string, number>();
    const mergedRows = new Map<number, MergedColumnSpan[]>();
    const uniqueMerges = new Map<string, MergeRange>();

    for (const column of v.columns) {
      const group = layouts.get(column.item.part) ?? [];
      columnAt.set(`${column.item.number}:${column.start}`, column);
      columnPositions.set(`${column.item.part}:${column.item.number}`, group.length);
      group.push({ col: column.item.number, width: column.size });
      layouts.set(column.item.part, group);
    }

    for (let at = 0; at < v.cells.length; at++) {
      const cellEntry = v.cells[at];
      if (!cellEntry) continue;
      const item = cellEntry.item;
      const cell = models[at] ?? null;
      let rowRecord = rowValues.get(item.row);
      if (!rowRecord) {
        rowRecord = {};
        rowValues.set(item.row, rowRecord);
      }
      if (cell) rowRecord[item.col] = cell;
      if (item.merge) uniqueMerges.set(JSON.stringify(item.merge), item.merge);
    }

    const sourceRows = [...new Set(c.rows.map((row) => row.number))].sort((a, b) => a - b);
    for (const merge of uniqueMerges.values()) {
      for (let at = lowerBoundNumber(sourceRows, merge.row); at < sourceRows.length; at++) {
        const row = sourceRows[at];
        if (row === undefined || row > merge.endRow) break;
        const ranges = mergedRows.get(row) ?? [];
        ranges.push({ first: merge.col, last: merge.endCol, merge });
        mergedRows.set(row, ranges);
      }
    }
    for (const ranges of mergedRows.values()) {
      ranges.sort((a, b) => a.first - b.first);
    }

    const findMerge = (row: number, col: number): MergeRange | null => {
      const ranges = mergedRows.get(row);
      if (!ranges) return null;
      const at = lowerBoundMerge(ranges, col + 1) - 1;
      const hit = at >= 0 ? ranges[at] : undefined;
      return hit && hit.last >= col ? hit.merge : null;
    };

    const state = {
      showStyles: true,
      showFormulas: false,
      fontOverride: '',
      styles: stylesPalette,
      autoFilter: null,
      indexReady: true,
    };
    const fontStack = this.fontStack;
    const richLimits = this.richLimits;
    const scale = v.scale;

    let presenterRef: PrintPresenter | null = null;
    const presenter = new PrintPresenter({
      state,
      scale: () => scale,
      fontStack,
      richLimits,
      colName: columnName,
      merge: findMerge,
      numericPadding: () => 6 * scale,
      applyStyle: (node: GridCellElement, cell: Partial<CellData>) => {
        const fullCell = cell as CellData;
        const style = presenterRef ? presenterRef.style(fullCell) : { fontSize: 11, fontName: 'Arial' };
        const font: RichFont = {
          name: style.fontName,
          size: style.fontSize,
          bold: Boolean(style.bold),
          italic: Boolean(style.italic),
          strike: Boolean(style.strike),
          underline: style.underlineStyle || (style.underline ? 'single' : 'none'),
          vertical: style.fontVertical || 'baseline',
          color: conditionalColor(fullCell, style) || '#000000',
        };
        const props = richFontProperties(font, style, {
          dom: true,
          scale,
          fontStack,
          fontOverlay: cell.conditional?.style?.font,
        });
        for (const [property, value] of Object.entries(props)) {
          node.style.setProperty(property, value);
        }
        node.style.backgroundColor = style.background || 'transparent';
        node.style.padding = `${scale}px ${3 * scale}px`;
        const hAlign = style.horizontal ? HORIZONTAL_JUSTIFY[style.horizontal] : undefined;
        node.style.justifyContent =
          hAlign ||
          (cell.type === 'n' || (!cell.type && !isTextCell(fullCell)) ? 'flex-end' : 'flex-start');
        const vAlign = style.vertical ? VERTICAL_ALIGN[style.vertical] : undefined;
        node.style.alignItems = vAlign || 'flex-end';
        node.style.whiteSpace = style.wrap ? 'pre-wrap' : 'pre';
        node.style.wordBreak = style.wrap ? 'break-word' : 'normal';
        if (style.rotation) warnings.add(localize('ui.printingRender.rotateTextPreviewAsRegular'));
      },
    });
    presenterRef = presenter;

    const presentCells = models.filter((cell): cell is CellData => cell !== null);
    presenter.prepare([{ number: 0, cells: presentCells }]);

    const edgeGroups = new Map<string, EdgeGroup>();
    const n = BigInt(s.scale.numerator);
    const d = BigInt(s.scale.denominator);
    const px = (x: string | bigint): number => printPixels(x, this.limits, n, d);
    const grid = printSourceFlag(summaryValue.sourceProperties, 'printOptions', 'gridLines');
    const gridBorder = grid ? '0.5px solid #b8b8b8' : '';

    const addEdge = (
      vertical: boolean,
      position: bigint,
      first: bigint,
      last: bigint,
      side: EdgeSegment['side'],
      css: string,
    ): void => {
      if (!css || last <= first) return;
      const key = `${vertical ? 'v' : 'h'}${position.toString()}`;
      let group = edgeGroups.get(key);
      if (!group) {
        group = { vertical, position, events: [] };
        edgeGroups.set(key, group);
      }
      const segment: EdgeSegment = { side, css };
      group.events.push({ at: first, segment, start: true }, { at: last, segment, start: false });
    };

    for (let at = 0; at < v.cells.length; at++) {
      const box = v.cells[at];
      if (!box) continue;
      const item = box.item;
      const cell = models[at] ?? null;
      const clip = makeDiv('print-cell-clip');
      const node = makeDiv('print-cell') as PrintCellElement;
      clip.dataset.ref = item.ref;
      node.dataset.row = String(item.row);
      node.dataset.col = String(item.col);
      node.dataset.ref = item.ref;
      node._xlSourceCell = item.cell ?? null;
      putPx(clip, { left: box.x, top: box.y, width: box.width, height: box.height });
      putPx(node, {
        left: box.innerX,
        top: box.innerY,
        width: box.innerWidth,
        height: box.innerHeight,
      });
      presenter.paint(node, cell, { number: item.row, cells: [], style: item.style });
      if (!cell) presenter.o.applyStyle(node, { style: item.style });
      if (item.hiddenAnchor) {
        node.replaceChildren();
        warnings.add(localize('ui.printingRender.hiddenMergeStartingPointsRetain'));
      }
      const messages = [node._xlRichNotice, node._xlPresentationNotice].filter(
        (msg): msg is string => Boolean(msg),
      );
      for (const message of messages) warnings.add(message);
      if (item.cell?.cacheMissing) {
        node.setAttribute('data-missing-cache', localize('ui.printingRender.lackCache'));
        warnings.add(localize('ui.printingRender.thereFormulasOnThisPage'));
      } else if (
        item.cell?.hasFormula &&
        !item.cell.raw &&
        (!item.cell.type || item.cell.type === 'n')
      ) {
        node.setAttribute('data-missing-cache', localize('ui.printingRender.valueCacheEmpty'));
        warnings.add(localize('ui.printingRender.valuesCachedOnThisPage'));
      }
      node.title =
        item.ref +
        (item.hiddenAnchor ? localize('ui.printingRender.sourceMergeStartPointHidden') : '') +
        (item.cell ? `\n${item.cell.raw.slice(0, 2048)}` : localize('ui.printingRender.blankSourceCoordinates')) +
        (item.cell?.cacheMissing ? `
${localize('ui.printingRender.sourceFormulaDidNotSave')}` : '') +
        (messages.length ? `\n${messages.join('；')}` : '');

      const numericOverflow =
        !item.hiddenAnchor &&
        presenter.fitNumber(node, cell, Math.min(box.width, box.innerWidth), 6 * scale);
      if (
        !numericOverflow &&
        !item.merge &&
        !item.hiddenAnchor &&
        box.innerX === 0 &&
        box.innerY === 0 &&
        box.width === box.innerWidth &&
        box.height === box.innerHeight
      ) {
        const column = columnAt.get(`${item.col}:${box.x}`);
        const group = column ? (layouts.get(column.item.part) ?? []) : [];
        const position = column
          ? columnPositions.get(`${column.item.part}:${item.col}`)
          : undefined;
        if (position !== undefined) {
          presenter.overflow(node, cell, rowValues.get(item.row), group, position);
        }
        clip.classList.toggle('print-can-spill', Boolean(node._xlOverflowSpan));
      }
      clip.append(node);
      data.append(clip);

      const style: VisualStyle = {
        ...presenter.style(cell, item.style),
        ...(item.borders ?? {}),
      };
      const b = item.box;
      const k = item.clip;
      const x = printUnits(b.x, true);
      const y = printUnits(b.y, true);
      const w = printUnits(b.width);
      const h = printUnits(b.height);
      const cx = printUnits(k.x);
      const cy = printUnits(k.y);
      const cw = printUnits(k.width);
      const ch = printUnits(k.height);
      if (x >= cx && x <= cx + cw) addEdge(true, x, cy, cy + ch, 'left', style.borderLeft || gridBorder);
      if (x + w >= cx && x + w <= cx + cw) addEdge(true, x + w, cy, cy + ch, 'right', style.borderRight || gridBorder);
      if (y >= cy && y <= cy + ch) addEdge(false, y, cx, cx + cw, 'top', style.borderTop || gridBorder);
      if (y + h >= cy && y + h <= cy + ch) addEdge(false, y + h, cx, cx + cw, 'bottom', style.borderBottom || gridBorder);
    }

    // Collapse physical seam segments, including a large merged edge beside
    // several small cells, using the same source-border precedence as grid.
    interface PrintVertex {
      readonly x: bigint;
      readonly y: bigint;
      north?: string;
      south?: string;
      west?: string;
      east?: string;
    }
    const vertices = new Map<string, PrintVertex>();
    const vertexAt = (vx: bigint, vy: bigint): PrintVertex => {
      const key = `${vx.toString()}:${vy.toString()}`;
      let vertex = vertices.get(key);
      if (!vertex) {
        vertex = { x: vx, y: vy };
        vertices.set(key, vertex);
      }
      return vertex;
    };

    for (const group of edgeGroups.values()) {
      group.events.sort((a, b) => compareBigInt(a.at, b.at));
      const first = group.events[0];
      if (!first) continue;
      let last = first.at;
      const active = new Set<EdgeSegment>();
      for (let at = 0; at < group.events.length; ) {
        const currentEvent = group.events[at];
        if (!currentEvent) break;
        const position = currentEvent.at;
        if (position > last && active.size) {
          let own = '';
          let other = '';
          const ownSide = group.vertical ? 'right' : 'bottom';
          for (const segment of active) {
            if (segment.side === ownSide) own = segment.css;
            else other = segment.css;
          }
          const css = group.vertical
            ? resolveBorders({ borderRight: own }, { borderLeft: other }, {}).right
            : resolveBorders({ borderBottom: own }, {}, { borderTop: other }).bottom;
          const parsed = parseBorder(css);
          if (parsed) {
            const stroke = parsed.width * scale;
            const line = makeDiv('print-edge');
            if (group.vertical) {
              putPx(line, {
                left: px(group.position) - stroke / 2,
                top: px(last),
                height: px(position - last),
              });
              line.style.borderLeft = `${stroke}px ${parsed.kind} ${parsed.color}`;
              const topV = vertexAt(group.position, last);
              topV.south = chooseBorder(topV.south, css);
              const botV = vertexAt(group.position, position);
              botV.north = chooseBorder(botV.north, css);
            } else {
              putPx(line, {
                left: px(last),
                top: px(group.position) - stroke / 2,
                width: px(position - last),
              });
              line.style.borderTop = `${stroke}px ${parsed.kind} ${parsed.color}`;
              const leftV = vertexAt(last, group.position);
              leftV.east = chooseBorder(leftV.east, css);
              const rightV = vertexAt(position, group.position);
              rightV.west = chooseBorder(rightV.west, css);
            }
            edges.append(line);
          }
        }
        while (at < group.events.length) {
          const ev = group.events[at];
          if (ev?.at !== position) break;
          at++;
          if (ev.start) active.add(ev.segment);
          else active.delete(ev.segment);
        }
        last = position;
      }
    }

    // Close centered corner junctions across all four quadrants using the same
    // vertex corner model as the interactive grid.
    const paintPrintCorner = (
      vx: number,
      vy: number,
      leftSide: boolean,
      topSide: boolean,
      north: string | undefined,
      west: string | undefined,
      east: string | undefined,
      south: string | undefined,
    ): void => {
      const corner = resolveBorderCorner(north, west, east, south);
      if (!corner) return;
      const halfW = (corner.width * scale) / 2;
      const halfH = (corner.height * scale) / 2;
      if (halfW <= 0 || halfH <= 0) return;
      const cap = makeDiv('print-edge-corner');
      putPx(cap, {
        left: leftSide ? vx - halfW : vx,
        top: topSide ? vy - halfH : vy,
        width: halfW,
        height: halfH,
      });
      if (corner.kind === 'double') {
        const strokeX = (corner.width * scale) / 3;
        const strokeY = (corner.height * scale) / 3;
        if (topSide) cap.style.borderTop = `${strokeY}px solid ${corner.color}`;
        else cap.style.borderBottom = `${strokeY}px solid ${corner.color}`;
        if (leftSide) cap.style.borderLeft = `${strokeX}px solid ${corner.color}`;
        else cap.style.borderRight = `${strokeX}px solid ${corner.color}`;
      } else {
        cap.style.backgroundColor = corner.color;
      }
      edges.append(cap);
    };

    for (const vertex of vertices.values()) {
      if (!(vertex.north || vertex.south) || !(vertex.west || vertex.east)) continue;
      const vx = px(vertex.x);
      const vy = px(vertex.y);
      paintPrintCorner(vx, vy, true, true, vertex.north, vertex.west, vertex.east, vertex.south);
      paintPrintCorner(vx, vy, false, true, vertex.north, vertex.east, vertex.west, vertex.south);
      paintPrintCorner(vx, vy, true, false, vertex.south, vertex.west, vertex.east, vertex.north);
      paintPrintCorner(vx, vy, false, false, vertex.south, vertex.east, vertex.west, vertex.north);
    }

    const headingWidth = printPixels(s.headings.width, this.limits);
    const headingHeight = printPixels(s.headings.height, this.limits);
    if (headingWidth) {
      for (const row of v.rows) {
        const label = makeDiv('print-source-heading', String(row.item.number));
        putPx(label, {
          left: v.x - headingWidth,
          top: v.y + row.start,
          width: headingWidth,
          height: row.size,
        });
        label.style.fontSize = `${Math.min(11, Math.max(0.1, row.size * 0.75))}px`;
        paper.append(label);
      }
    }
    if (headingHeight) {
      for (const column of v.columns) {
        const label = makeDiv('print-source-heading', columnName(column.item.number));
        putPx(label, {
          left: v.x + column.start,
          top: v.y - headingHeight,
          width: column.size,
          height: headingHeight,
        });
        paper.append(label);
      }
    }

    return { node: paper, width: v.width, height: v.height, warnings: [...warnings] };
  }
}
