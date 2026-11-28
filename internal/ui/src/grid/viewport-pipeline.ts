/**
 * Grid viewport rendering pipeline: font stacks, cell/border styling,
 * virtual geometry & canvas sizing, row/column resize & autofit, tile
 * fetching/projection/scheduling, canvas fallback tiles, incremental DOM
 * rendering, range frames, selection highlighting, and initial preview.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import { GridAppearance } from './grid-appearance.ts';
import type { ApiClient, MergeRange, SelectionArea, ToastTone } from '../contracts/common.ts';
import type { CellData, RowData, SheetViewportResponse, VisualStyle } from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage } from '../core/display-format.ts';
import { byId, requireButton, requireEl } from '../core/dom.ts';
import type { FileFormatsRegistry } from '../core/formats.ts';
import { CellPresenter, isTextCell, selectionFill, type ColumnLayoutItem } from './cell-view.ts';
import { conditionalColor, sameConditional } from './conditional.ts';
import type { FrozenPanes } from './freeze.ts';
import { BASE_ROW, Geometry } from './geometry.ts';
import { fontStack } from './display-font.ts';
import { GridSizing } from './grid-sizing.ts';
import { paintMissingCells, type PaintMissingTheme } from './grid-backdrop.ts';
import { usedBorderWidth } from './grid-borders.ts';
import type { Pictures } from './images.ts';
import type { MergedCells } from './merges.ts';
import { MultiSelection, type SelectionHighlight } from './multi-selection.ts';
import { hyperlinkVisitKey } from './navigation.ts';
import {
  paintCrosshairTails,
  paintRangeFrames as paintRangeFrameBoxes,
  type FrameArea,
  type RangeFrameView,
  type SeamEdges,
  type SeamSegment,
} from './range-frame.ts';
import { sameRichText } from './richtext.ts';
import { RowVisibility } from './row-visibility.ts';
import type {
  ColumnSelectionSpan,
  GridCellElement,
  GridRowElement,
  ViewportEntry,
  ViewportRequest,
  ViewportRequestPosition,
  WorkbookState,
} from './state.ts';
import { isTableHeader } from './table-headers.ts';
import {
  estimateViewportBytes,
  rowColumns,
  sameViewportRows,
  viewportCoversVisible,
  viewportHasHeadroom,
  viewportKey as makeViewportKey,
  type TileCache,
} from './viewport-cache.ts';
import { ViewportScheduler, type SchedulerItem } from './viewport-scheduler.ts';
import type { ScrollMotion, VirtualScroll } from './virtual-scroll.ts';
import { decodeSheetPreview, decodeSheetViewport } from '../core/viewport-response.ts';
export { BASE_ROW, BASE_COL } from './geometry.ts';
export { fontStack, isKnownFont } from './display-font.ts';

const reread = <T>(read: () => T): T => read();

/** Column layout used by the row mount loop: source column plus its letter and absolute offset. */
interface GridColumnLayout extends ColumnLayoutItem {
  readonly letter: string;
  readonly left: number;
}
export const HEADER_BASE = 27;
export const DOM_MARGIN_BUDGET = 2600;
export const PREFETCH_CELL_BUDGET = 12000;
export const TEXT_SPILL_LOOKAHEAD = 12;
export const MAX_ROWS_PER_REQUEST = 150;
export const MAX_COLS_PER_REQUEST = 80;
export const VIEWPORT_CACHE_BUDGET = 24 * 1024 * 1024;

export function sameRenderedCell(
  a: CellData | null | undefined,
  b: CellData | null | undefined,
): boolean {
  if (a === b) return true;
  if (!a || !b) return !a && !b;
  return (
    a.display === b.display &&
    a.raw === b.raw &&
    a.type === b.type &&
    a.style === b.style &&
    a.formula === b.formula &&
    a.hasFormula === b.hasFormula &&
    a.valueColor === b.valueColor &&
    sameRichText(a.richText, b.richText) &&
    sameConditional(a.conditional, b.conditional) &&
    a.presentationOnly === b.presentationOnly &&
    a.cacheMissing === b.cacheMissing &&
    a.hyperlink?.kind === b.hyperlink?.kind &&
    a.hyperlink?.sheet === b.hyperlink?.sheet &&
    a.hyperlink?.ref === b.hyperlink?.ref &&
    a.hyperlink?.url === b.hyperlink?.url
  );
}

interface TileJobItem extends SchedulerItem {
  readonly request: ViewportRequest;
  readonly sheet: string;
  readonly epoch: number;
  foreground?: boolean;
}

interface GridWorkJob {
  readonly entry: ViewportEntry;
  readonly resolve: () => void;
  readonly promise: Promise<void>;
  frame: number;
  queued: boolean;
  step(): void;
}

interface CachedCanvasTile {
  readonly canvas: HTMLCanvasElement;
  readonly y: number;
  readonly minVisual: number;
  readonly maxVisual: number;
  readonly firstCol: number;
  readonly lastCol: number;
  dirty: boolean;
}

interface CanvasThemePalette extends PaintMissingTheme {
  readonly key: string;
  readonly tableHead: string;
  readonly band: string;
}

interface FinishedCellTemplate {
  readonly node: GridCellElement;
  readonly metadata: {
    readonly _xlColumnLeft?: number;
    readonly _xlColumnWidth?: number;
    readonly _xlPaintKey?: string;
    readonly _xlRichAllowed?: boolean;
    readonly _xlRowPresentationNotice?: string;
    readonly _xlborderRight?: string;
    readonly _xlborderBottom?: string;
    readonly _xlOuterLeft?: string;
    readonly _xlOuterTop?: string;
    readonly _xlNextCol?: number;
  };
}

interface BrowserScheduler {
  postTask?(callback: () => void, options?: { readonly priority?: string }): Promise<void>;
}

interface BrowserInputScheduling {
  isInputPending?(options?: { readonly includeContinuous?: boolean }): boolean;
}

export interface VisibleRectInfo {
  readonly left: number;
  readonly top: number;
  readonly right: number;
  readonly bottom: number;
  readonly pane: 'body' | 'corner' | 'top' | 'left';
  viewport?: { readonly left: number; readonly top: number; readonly right: number; readonly bottom: number };
}

export interface ReferenceOverlayArea {
  readonly row1: number;
  readonly row2: number;
  readonly col1: number;
  readonly col2: number;
  readonly visual1?: number;
  readonly visual2?: number;
  readonly color?: string;
}

export interface ViewportPipelinePanels {
  readonly editCopy?: {
    readonly data?: { readonly document?: { readonly revision?: string | number } } | null;
    readonly grid: {
      readonly active: boolean;
      project(sheet: string, ref: string, source: CellData | undefined): CellData | null | undefined;
      schedulePosition(): void;
      readonly ranges: {
        paintCut(node: GridCellElement, sheet: string, row: number, col: number): void;
      };
      readonly entry: {
        areas(sheet: string): readonly ReferenceOverlayArea[];
      };
      readonly fillHandle?: { readonly area?: ReferenceOverlayArea | null } | null;
    };
  } | null;
  readonly traceUI?: { refreshPulse(): void } | null;
  readonly bookmarkCenter?: {
    readonly keys?: ReadonlySet<string>;
    has(sheet: string, ref: string): boolean;
  } | null;
  readonly formulaInspector?: {
    readonly active?: boolean;
    decorateCell(cell: Element, row: number, col: number, sheet: string): void;
  } | null;
  readonly selectionInspector?: {
    readonly hits?: ReadonlySet<string>;
    decorateCell(cell: Element, ref: string, sheet: string): void;
  } | null;
  readonly selectionQuick?: { changed(): void } | null;
  readonly transposeCenter?: { changed(): void } | null;
  readonly cellExplanation?: { changed(): void } | null;
  readonly profileCenter?: { changed(): void } | null;
  readonly pictures?: Pictures | null;
}

export interface ViewportPipelineDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly fileFormats: FileFormatsRegistry;
  readonly geometry: Geometry;
  readonly mergedCells: MergedCells;
  readonly virtualScroll: VirtualScroll;
  readonly motion: ScrollMotion;
  readonly tiles: TileCache;
  readonly toast: (message: string, tone?: ToastTone) => void;
  readonly colName: (col: number) => string;
  readonly sourceRowLimit: () => number;
  readonly sourceColLimit: () => number;
  readonly getFrozenPanes: () => FrozenPanes;
  readonly selectedColumnSpan: () => ColumnSelectionSpan | null;
  readonly paintColumnSelection: () => void;
  readonly selectColumnSpan: (anchor: number, end: number) => void;
  readonly captureColumnSelection: () => void;
  readonly crosshairCell: () => { readonly row: number; readonly col: number } | null;
  readonly selectionHighlight: (areas?: readonly SelectionArea[]) => SelectionHighlight;
  readonly isPrecedent: (row: number, col: number) => boolean;
  readonly isSearchMatch: (sheet: string, ref: string) => boolean;
  readonly updateSearchNavButtons: () => void;
  readonly syncFilteredSelection: (through: number) => void;
  readonly syncTableColumnHeaders: () => void;
  readonly decorateTableColumnHeader: (head: HTMLElement, col: number) => boolean;
  readonly finishSheetSwitch: (epoch: number, ready?: boolean) => void;
  readonly startConditionalStats: (
    sheetName: string,
    epoch: number,
    options?: { readonly refreshOnReady?: boolean },
  ) => Promise<void>;
  readonly visibleReferenceAreas?: (
    refs: readonly ReferenceOverlayArea[],
    filtered: boolean,
    pinnedRows: number,
    rowNumberByVisual: ReadonlyMap<number, number>,
    activeMerge: (row: number, col: number) => MergeRange | null,
  ) => readonly FrameArea[];
  readonly panels: ViewportPipelinePanels;
}

export class ViewportPipeline {
  readonly sizing: GridSizing;
  readonly appearance: GridAppearance;
  private readonly d: ViewportPipelineDeps;
  public readonly cellPresenter: CellPresenter;
  public readonly frozenPresenter: CellPresenter;
  public readonly scheduler: ViewportScheduler<TileJobItem, ViewportEntry | undefined>;
  public rowHeadWidth = 26;
  private bytesPerCell = 240;
  private columnVisibility: RowVisibility | null = null;
  private columnVisibilityKey = '';
  public foregroundLookups = 0;
  public foregroundHits = 0;
  private fallbackPalette: CanvasThemePalette | null = null;
  private readonly styleTiles = new Map<string, CachedCanvasTile>();
  private styleIdentity = '';
  private gridWork: GridWorkJob | null = null;
  private lastViewportDemand: ViewportRequest | null = null;
  private readonly finishedCells = new Map<string, FinishedCellTemplate>();
  private finishedCellContext = '';
  private readonly rowPool = new Map<number, GridRowElement>();
  private poolCells = 0;
  private selectionRasterRatio = 0;
  private selectionRasterStep = 1;

  constructor(deps: ViewportPipelineDeps) {
    this.d = deps;
    this.appearance = new GridAppearance({
      S: deps.S, api: deps.api, colName: deps.colName, toast: deps.toast,
      rowTop: row => this.rowTop(row), colLeft: col => this.colLeft(col),
      activeMerge: (row, col) => this.activeMerge(row, col),
      nextVisibleRow: row => this.nextVisibleRow(row),
      repaint: () => {
        this.renderGridFromCurrentWindow();
        deps.getFrozenPanes().schedule(true);
      },
    });
    const presenterOptions = {
      state: deps.S,
      applyStyle: (node: HTMLElement, cell: Partial<CellData> | null | undefined) =>
        this.appearance.applyCellStyle(node, cell),
      colName: deps.colName,
      scale: () => this.scale(),
      fontStack,
      merge: (row: number, col: number) => this.activeMerge(row, col),
      richLimits: deps.fileFormats.richText,
      isHyperlinkVisited: (ref: string) =>
        Boolean(deps.S.sheet && deps.S.visitedHyperlinks.has(hyperlinkVisitKey(deps.S.sheet, ref))),
      numericPadding: (node: HTMLElement) =>
        node.classList.contains('auto-filter-header')
          ? 32
          : deps.S.showStyles && deps.fileFormats.source(deps.S.source?.format).styled
            ? 5 * this.scale()
            : 14,
    };
    this.cellPresenter = new CellPresenter(presenterOptions);
    this.frozenPresenter = new CellPresenter(presenterOptions);
    this.scheduler = new ViewportScheduler<TileJobItem, ViewportEntry | undefined>((job, signal) =>
      this.readViewportTile(job, signal),
    );
    this.sizing = new GridSizing({
      S: deps.S, geometry: deps.geometry, api: deps.api, toast: deps.toast,
      colName: deps.colName, sourceColLimit: deps.sourceColLimit,
      scale: () => this.scale(), rowHeight: row => this.rowHeight(row),
      colWidth: col => this.colWidth(col), relayoutGrid: () => this.relayoutGrid(),
    });
  }

  // ---- Geometry & Virtual Scroll ----

  scale(): number {
    return this.d.S.zoom / 100;
  }

  snapPixel(value: number): number {
    const ratio = window.devicePixelRatio || 1;
    return Math.round(value * ratio) / ratio;
  }

  headerHeight(): number {
    return this.snapPixel(HEADER_BASE * this.scale());
  }

  getVirtualScrollTop(): number {
    return this.d.virtualScroll.logical(requireEl('grid').scrollTop);
  }

  scrollTopToPhysical(logical: number): number {
    return this.d.virtualScroll.physical(logical);
  }

  setVirtualScrollTop(logical: number): void {
    requireEl('grid').scrollTop = this.scrollTopToPhysical(logical);
  }

  updateGeometryBase(): void {
    this.d.geometry.rows.setBase(this.d.S.sourceDefaultRow || BASE_ROW);
  }

  physicalCanvasHeight(fullHeight: number, grid: HTMLElement): number {
    const physical = this.d.virtualScroll.configure(fullHeight, grid.clientHeight);
    this.d.S.virtualScrollScale = this.d.virtualScroll.scale;
    return physical;
  }

  sourceRowTop(row: number): number {
    return this.d.geometry.rows.paintOffset(row, this.scale(), window.devicePixelRatio || 1);
  }

  sourceRowHeight(row: number): number {
    return this.sourceRowTop(row + 1) - this.sourceRowTop(row);
  }

  rowTop(row: number): number {
    return this.d.S.filtered
      ? this.snapPixel((row - 1) * this.d.geometry.rows.base * this.scale())
      : this.sourceRowTop(row);
  }

  rowHeight(row = 0): number {
    return row ? this.rowTop(row + 1) - this.rowTop(row) : this.d.geometry.rows.base * this.scale();
  }

  colWidth(col = 0): number {
    return col
      ? this.d.geometry.cols.paintSize(col, this.scale(), window.devicePixelRatio || 1)
      : this.d.geometry.cols.base * this.scale();
  }

  colLeft(col: number): number {
    return this.d.geometry.cols.paintOffset(col, this.scale(), window.devicePixelRatio || 1);
  }

  rowAtOffset(y: number): number {
    return this.d.S.filtered
      ? Math.max(1, Math.floor(y / this.rowHeight()) + 1)
      : this.d.geometry.rows.at(Math.max(0, y) / this.scale());
  }

  colAtOffset(x: number): number {
    return this.d.geometry.cols.at(Math.max(0, x) / this.scale());
  }

  rowTotal(count: number): number {
    return this.rowTop(count + 1);
  }

  rowVisible(row: number): boolean {
    return !this.d.S.rowVisibility || this.d.S.rowVisibility.visible(row);
  }

  nextVisibleRow(row: number): number {
    return this.d.S.rowVisibility ? this.d.S.rowVisibility.next(row) : Math.max(1, row);
  }

  previousVisibleRow(row: number): number {
    return this.d.S.rowVisibility ? this.d.S.rowVisibility.previous(row) : Math.max(0, row);
  }

  advanceVisibleRow(row: number, delta: number): number {
    return this.d.S.rowVisibility
      ? this.d.S.rowVisibility.advance(row, delta)
      : Math.max(1, Math.min(this.d.sourceRowLimit(), row + delta));
  }

  visibleSourceCount(first: number, last: number): number {
    return this.d.S.rowVisibility
      ? this.d.S.rowVisibility.count(first, last)
      : Math.max(0, last - first + 1);
  }

  filteredBodyCount(): number {
    const { S, getFrozenPanes } = this.d;
    if (S.rowView?.filterId === S.filterId) {
      const pinnedRows = getFrozenPanes().effective().rows;
      const pinnedCount = (S.rowView.matchedPinnedRows ?? []).filter((r) => r <= pinnedRows).length;
      return Math.max(0, S.rowView.totalRows - pinnedCount);
    }
    return S.filterTotal;
  }

  sheetHeight(count: number): number {
    return (
      this.rowTotal(count) +
      (this.d.S.filtered ? this.sourceRowTop(this.d.getFrozenPanes().effective().rows + 1) : 0)
    );
  }

  colTotal(count: number): number {
    return this.colLeft(count + 1);
  }

  // ---- Canvas Sizing & Resize / AutoFit ----

  syncFreezeScrollGutters(): void {
    const grid = requireEl('grid');
    const vertical = Math.max(0, grid.offsetWidth - grid.clientWidth - grid.clientLeft);
    const horizontal = Math.max(0, grid.offsetHeight - grid.clientHeight - grid.clientTop);
    const region = document.querySelector<HTMLElement>('.grid-region');
    if (region) {
      region.style.setProperty('--grid-v-scrollbar', `${vertical}px`);
      region.style.setProperty('--grid-h-scrollbar', `${horizontal}px`);
    }
    this.d.syncTableColumnHeaders();
  }

  updateCanvasSize(): void {
    const { S, panels } = this.d;
    if (S.filtered) S.filterTotal = this.filteredBodyCount();
    const rows = S.filtered ? Math.max(0, S.filterTotal) : S.maxRow;
    this.updateGeometryBase();
    this.rowHeadWidth = Geometry.rowHeaderWidth(S.maxRow, window.devicePixelRatio || 1);
    const width = this.rowHeadWidth + this.colTotal(S.maxCol);
    const height = this.headerHeight() + this.sheetHeight(rows);
    const canvas = requireEl('gridCanvas');
    const grid = requireEl('grid');
    const oldLogicalScroll = this.getVirtualScrollTop();
    canvas.style.width = `${Math.max(width, grid.clientWidth)}px`;
    canvas.style.height = `${this.physicalCanvasHeight(height, grid)}px`;
    this.setVirtualScrollTop(oldLogicalScroll);
    canvas.style.setProperty('--canvas-width', `${Math.max(width, grid.clientWidth)}px`);
    canvas.style.setProperty('--row-height', `${this.rowHeight()}px`);
    canvas.style.setProperty('--col-width', `${this.colWidth()}px`);
    canvas.style.setProperty('--header-height', `${this.headerHeight()}px`);
    canvas.style.setProperty('--row-header-width', `${this.rowHeadWidth}px`);
    requireEl('columnHeaders').style.height = `${this.headerHeight()}px`;
    const rowsLayer = requireEl('rowsLayer');
    rowsLayer.style.top = `${this.headerHeight()}px`;
    rowsLayer.style.width = `${width}px`;
    requireEl('gridStatus').textContent = localize('ui.viewportPipeline.usedAread2284fe', { detail: S.sheet ?? '', dimension: S.dimension });
    this.syncFreezeScrollGutters();
    panels.pictures?.schedule();
  }

  relayoutGrid(): void {
    this.invalidateViewport({ preserveDOM: true });
    this.updateCanvasSize();
    const frozenPanes = this.d.getFrozenPanes();
    frozenPanes.reset({ preserveDOM: true });
    frozenPanes.schedule(true);
    this.scheduleViewport(true);
    this.paintRangeFrames();
  }

  ensureExtent(row: number, col: number): void {
    const { S, sourceRowLimit, sourceColLimit } = this.d;
    if (!S.indexReady) return;
    const neededRows = Math.min(sourceRowLimit(), Math.max(S.maxRow, row + 18));
    const neededCols = Math.min(sourceColLimit(), Math.max(S.maxCol, col + 3));
    if (neededRows !== S.maxRow || neededCols !== S.maxCol) {
      S.maxRow = neededRows;
      S.maxCol = neededCols;
      this.updateCanvasSize();
    }
  }

  // ---- Viewport Tile Request, Projection & Scheduling ----

  private columnOrder(): RowVisibility | null {
    const { S, geometry } = this.d;
    if (!S.hiddenColumns.size) return null;
    const key = `${geometry.cols.version}|${S.maxCol}`;
    if (this.columnVisibilityKey !== key) {
      const ranges: { min: number; max: number }[] = [];
      for (const col of [...S.hiddenColumns].filter((c) => c <= S.maxCol).sort((a, b) => a - b)) {
        const last = ranges.at(-1);
        if (last?.max === col - 1) last.max = col;
        else ranges.push({ min: col, max: col });
      }
      this.columnVisibility = new RowVisibility(S.maxCol, ranges);
      this.columnVisibilityKey = key;
    }
    return this.columnVisibility;
  }

  viewportContext(): string {
    const { S, panels } = this.d;
    return JSON.stringify([
      S.book,
      S.workbookKey,
      S.sheet,
      panels.editCopy?.grid.active ? (panels.editCopy.data?.document?.revision ?? 0) : null,
    ]);
  }

  viewportKey(request: ViewportRequest): string {
    return makeViewportKey(this.d.S.sheetEpoch, request);
  }

  windowContains(request: ViewportRequest): boolean {
    return viewportCoversVisible(this.d.S.window, request);
  }

  private viewportBand(
    base: Omit<
      ViewportRequest,
      | 'r1'
      | 'r2'
      | 'c1'
      | 'c2'
      | 'displayStart'
      | 'displayEnd'
      | 'row'
      | 'offset'
      | 'limit'
      | 'startCol'
      | 'cols'
      | 'selectedCols'
      | 'leadRow'
      | 'leadCol'
    >,
    rawR1: number,
    rawR2: number,
    rawC1: number,
    rawC2: number,
  ): ViewportRequest {
    const rowCount = base.rowCount ?? 0;
    const colCount = base.colCount ?? 0;
    const rowOrder = base.rowOrder;
    const colOrder = base.colOrder;
    const r1 = Math.max(1, Math.min(rowCount || 1, rawR1));
    const r2 = Math.max(r1, Math.min(rowCount || 1, rawR2));
    const c1 = Math.max(1, Math.min(colCount || 1, rawC1));
    const c2 = Math.max(c1, Math.min(colCount || 1, rawC2));
    const displayStart = rowOrder?.at(r1 - 1) ?? r1;
    const displayEnd = rowCount ? (rowOrder?.at(r2 - 1) ?? r2) : displayStart - 1;
    const selectedCols = colOrder
      ? Array.from({ length: c2 - c1 + 1 }, (_, i) => colOrder.at(c1 + i - 1))
      : null;
    return {
      ...base,
      r1,
      r2,
      c1,
      c2,
      displayStart,
      displayEnd,
      row: displayStart,
      offset: r1 - 1,
      limit: r2 - r1 + 1,
      startCol: selectedCols?.[0] ?? c1,
      cols: c2 - c1 + 1,
      selectedCols,
      leadRow: displayStart,
      leadCol: selectedCols?.[0] ?? c1,
    };
  }

  getViewportRequest(): ViewportRequest {
    const { S, motion, getFrozenPanes } = this.d;
    const grid = requireEl('grid');
    const pinned = getFrozenPanes().effective();
    const w = Math.max(1, grid.clientWidth - this.rowHeadWidth);
    const h = Math.max(1, grid.clientHeight - this.headerHeight());
    const top = this.getVirtualScrollTop();
    const left = grid.scrollLeft;
    const ph = this.sourceRowTop(pinned.rows + 1);
    const pw = this.colLeft(pinned.cols + 1);
    if (S.filtered) S.filterTotal = this.filteredBodyCount();
    const rowOrder = S.filtered ? null : S.rowVisibility;
    const colOrder = this.columnOrder();
    const rowCount = S.filtered ? S.filterTotal : this.visibleSourceCount(1, S.maxRow);
    const colCount = colOrder ? colOrder.length : S.maxCol;
    const rowRank = (px: number): number => {
      const row = this.rowAtOffset(px);
      return Math.min(rowCount || 1, rowOrder ? rowOrder.before(row) + 1 : row);
    };
    const colRank = (px: number): number => {
      const col = this.colAtOffset(px);
      return Math.min(colCount || 1, colOrder ? colOrder.before(col) + 1 : col);
    };
    const y0 = top + (S.filtered ? 0 : ph);
    const y1 = top + h - (S.filtered ? ph : 0);
    const x0 = left + pw;
    const x1 = left + w;
    const v1 = rowRank(y0);
    const v2 = Math.max(v1, rowRank(y1 - 0.01));
    const u1 = colRank(x0);
    const u2 = Math.max(u1, colRank(x1 - 0.01));
    const visibleStart = rowOrder?.at(v1 - 1) ?? v1;
    const visibleEnd = rowCount ? (rowOrder?.at(v2 - 1) ?? v2) : 0;
    const visibleColStart = colOrder?.at(u1 - 1) ?? u1;
    const visibleColEnd = colCount ? (colOrder?.at(u2 - 1) ?? u2) : 0;
    const position: ViewportRequestPosition = {
      x: left,
      y: top,
      physical: grid.scrollTop,
      w: w + this.rowHeadWidth,
      h: h + this.headerHeight(),
      pinned,
      ph,
      pw,
    };
    const base = {
      position,
      book: S.book,
      workbookKey: S.workbookKey,
      sheet: S.sheet,
      context: this.viewportContext(),
      filtered: S.filtered,
      filterId: S.filtered ? S.filterId : '',
      viewId: S.rowView?.id ?? '',
      excludeThroughRow: S.filtered ? pinned.rows : 0,
      rowOrder,
      colOrder,
      rowCount,
      colCount,
      visibleStart,
      visibleEnd,
      visibleColStart,
      visibleColEnd,
    };
    const rowStep = Math.max(
      1,
      Math.min(4, Math.floor((DOM_MARGIN_BUDGET / (u2 - u1 + TEXT_SPILL_LOOKAHEAD + 6) - (v2 - v1 + 1)) / 3)),
    );
    const r1 = Math.floor((v1 - 1) / rowStep) * rowStep + 1 - rowStep;
    const r2 = Math.ceil(v2 / rowStep) * rowStep + rowStep;
    const now = performance.now();
    const lead = motion.predict(w, h, now);
    const moving =
      motion.time !== null &&
      now - motion.time < 280 &&
      Math.abs(motion.dx) < w * 1.5 &&
      Math.abs(motion.dy) < h * 1.5;
    const vx = moving ? motion.vx : 0;
    const vy = moving ? motion.vy : 0;
    const renderHorizon = Math.max(48, Math.min(220, motion.renderMs + 36));
    const colLead = Math.min(w * 0.4, Math.abs(vx) * Math.min(72, renderHorizon));
    const mountRank = Math.max(1, Math.min(Math.floor((u1 - 1) / 4) * 4 - 1, colRank(x0 - (vx < 0 ? colLead : 0))));
    const mountEnd = Math.max(Math.ceil(u2 / 4) * 4 + 3, colRank(x1 + (vx > 0 ? colLead : 0)));
    const cellRoom = Math.max(0, Math.floor(DOM_MARGIN_BUDGET / (mountEnd - mountRank + 1)) - (r2 - r1 + 1));
    const rowLead = Math.min(h * 0.55, Math.abs(vy) * Math.min(72, renderHorizon));
    const mountR1 = Math.min(r1, Math.max(r1 - cellRoom, rowRank(y0 - (vy < 0 ? rowLead : 0))));
    const mountR2 = Math.max(r2, Math.min(r2 + cellRoom, rowRank(y1 + (vy > 0 ? rowLead : 0))));
    const req = this.viewportBand(base, mountR1, mountR2, mountRank - TEXT_SPILL_LOOKAHEAD, mountEnd);
    const reqR1 = req.r1 ?? 1;
    const reqR2 = req.r2 ?? reqR1;
    const reqC2 = req.c2 ?? mountEnd;
    const aheadR = vy > 0 ? v2 + 0.6 * (reqR2 - v2) : v1 - 0.6 * (v1 - reqR1);
    const aheadC = vx > 0 ? u2 + 0.6 * (reqC2 - u2) : u1 - 0.6 * (u1 - mountRank);
    const headRow = Math.max(1, Math.round(aheadR));
    const headCol = Math.max(1, Math.round(aheadC));
    let tileRows = Math.min(96, Math.max(32, Math.ceil(h / Math.max(10, this.rowHeight()) / 16) * 32));
    const tileCols = Math.min(48, Math.max(16, Math.ceil(w / Math.max(16, this.colWidth()) / 12) * 16));
    while (tileRows * tileCols * this.bytesPerCell > VIEWPORT_CACHE_BUDGET / 8 && tileRows > 4) {
      tileRows = Math.ceil(tileRows / 2);
    }
    return {
      ...req,
      mountColStart: colOrder?.at(mountRank - 1) ?? mountRank,
      visibleRanks: [v1, v2, u1, u2],
      leadRow: rowOrder?.at(headRow - 1) ?? headRow,
      leadCol: colOrder?.at(headCol - 1) ?? headCol,
      prediction: [
        rowRank(y0 - lead.y[0]),
        rowRank(y1 + lead.y[1]),
        colRank(x0 - lead.x[0]),
        colRank(x1 + lead.x[1]),
      ],
      tileRows,
      tileCols,
    };
  }

  private mergeParts(req: ViewportRequest) {
    if (req.mergeParts) return req.mergeParts;
    const { S, mergedCells } = this.d;
    const groups = new Map<number, Set<number>>();
    const anchors = new Map<number, number[]>();
    const right = req.selectedCols?.at(-1) ?? req.startCol + req.cols - 1;
    if (!req.filtered && mergedCells.root) {
      for (const m of mergedCells.intersect(
        req.displayStart,
        req.displayEnd,
        req.mountColStart ?? req.startCol,
        right,
      )) {
        if (!this.activeMerge(m.row, m.col) || S.hiddenColumns.has(m.col)) continue;
        if (m.row < req.displayStart || m.col < req.startCol) {
          let list = anchors.get(m.row);
          if (!list) {
            list = [];
            anchors.set(m.row, list);
          }
          list.push(m.col);
        }
        for (const r of [m.row, m.endRow]) {
          for (const c of [m.col, m.endCol]) {
            if (
              !this.rowVisible(r) ||
              S.hiddenColumns.has(c) ||
              (r >= req.displayStart && r <= req.displayEnd && c >= req.startCol && c <= right)
            ) {
              continue;
            }
            let set = groups.get(r);
            if (!set) {
              set = new Set<number>();
              groups.set(r, set);
            }
            set.add(c);
          }
        }
      }
    }
    const parts: ViewportRequest[] = [];
    for (const [row, set] of groups) {
      const columns = [...set].sort((a, b) => a - b);
      for (let i = 0; i < columns.length; i += MAX_COLS_PER_REQUEST) {
        const selectedCols = columns.slice(i, i + MAX_COLS_PER_REQUEST);
        parts.push({
          ...req,
          displayStart: row,
          displayEnd: row,
          row,
          limit: 1,
          startCol: selectedCols[0] ?? 1,
          cols: selectedCols.length,
          selectedCols,
        });
      }
    }
    req.mergeParts = { anchors, parts };
    return req.mergeParts;
  }

  private projectViewport(
    request: ViewportRequest,
    received?: readonly ViewportEntry[],
  ): ViewportEntry | null {
    const { tiles } = this.d;
    const r1 = request.r1 ?? request.displayStart;
    const visuals = Array.from(
      { length: request.rowCount ? request.limit : 0 },
      (_, i) => request.rowOrder?.at(r1 + i - 1) ?? r1 + i,
    );
    const projected = tiles.project(request, visuals, received);
    if (!projected) return null;
    const bounds = {
      ...projected.bounds,
      startCol: Math.max(projected.bounds.startCol, request.mountColStart ?? projected.bounds.startCol),
    };
    const data: SheetViewportResponse = { ...projected.data };
    const { parts, anchors } = this.mergeParts(request);
    if (parts.length) {
      const lookup = tiles.reader(request, received);
      const rows = new Map<number, RowData>(data.rows.map((r) => [r.number, r]));
      for (const part of parts) {
        for (const col of part.selectedCols ?? []) {
          const sampled = lookup(part.row, col);
          if (!sampled) return null;
          let row = rows.get(part.row);
          if (!row) {
            row = { ...sampled.row, number: part.row, cells: [] };
            rows.set(part.row, row);
          }
          if (sampled.cell) row.cells.push(sampled.cell);
        }
      }
      data.rows = [...rows.values()];
      data.anchors = anchors;
    }
    return { request: projected.request, data, bounds, bytes: estimateViewportBytes(data) };
  }

  private planViewport(request: ViewportRequest, needCurrent: boolean): Promise<ViewportEntry[]> {
    const { S, tiles, motion } = this.d;
    const tileRows = request.tileRows ?? 32;
    const tileCols = request.tileCols ?? 16;
    const [pr1, pr2, pc1, pc2] = request.prediction ?? [1, 1, 1, 1];
    const reqR1 = request.r1 ?? 1;
    const reqR2 = request.r2 ?? reqR1;
    const reqC1 = request.c1 ?? 1;
    const reqC2 = request.c2 ?? reqC1;
    const current: TileJobItem[] = [];
    const predicted: TileJobItem[] = [];
    const resident: ViewportEntry[] = [];
    const first = (rank: number, step: number): number =>
      Math.floor((Math.max(1, rank) - 1) / step) * step + 1;
    const currentMinR = first(reqR1, tileRows);
    const currentMinC = first(reqC1, tileCols);
    const available = (r: number, c: number): boolean =>
      r >= currentMinR && r <= reqR2 && c >= currentMinC && c <= reqC2;
    const sheet = S.sheet ?? '';
    const add = (list: TileJobItem[], r: number, c: number): void => {
      const band = this.viewportBand(request, r, r + tileRows - 1, c, c + tileCols - 1);
      const key = this.viewportKey(band);
      const hit = tiles.find(key, band);
      if (hit) {
        if (list === current) resident.push(hit);
      } else {
        list.push({ key, request: band, sheet, epoch: S.sheetEpoch });
      }
    };
    if (needCurrent) {
      for (const band of this.mergeParts(request).parts) {
        const key = this.viewportKey(band);
        const hit = tiles.find(key, band);
        if (hit) resident.push(hit);
        else current.push({ key, request: band, sheet, epoch: S.sheetEpoch });
      }
      for (let r = currentMinR; r <= reqR2; r += tileRows) {
        for (let c = currentMinC; c <= reqC2; c += tileCols) add(current, r, c);
      }
    }
    if (!needCurrent && motion.time !== null) {
      for (let r = currentMinR; r <= reqR2; r += tileRows) {
        for (let c = currentMinC; c <= reqC2; c += tileCols) add(predicted, r, c);
      }
    }
    const cap = Math.min(
      PREFETCH_CELL_BUDGET,
      Math.floor(VIEWPORT_CACHE_BUDGET / 4 / this.bytesPerCell),
    );
    const candidates: { readonly r: number; readonly c: number; readonly d: number }[] = [];
    for (let r = first(pr1, tileRows); r <= pr2; r += tileRows) {
      for (let c = first(pc1, tileCols); c <= pc2; c += tileCols) {
        if (!available(r, c)) {
          candidates.push({
            r,
            c,
            d:
              Math.abs(r - (motion.vy < 0 ? reqR1 : reqR2)) / tileRows +
              Math.abs(c - (motion.vx < 0 ? reqC1 : reqC2)) / tileCols,
          });
        }
      }
    }
    candidates.sort((a, b) => a.d - b.d);
    const allowance = Math.max(0, Math.floor(cap / (tileRows * tileCols)) - predicted.length);
    for (const { r, c } of candidates.slice(0, allowance)) add(predicted, r, c);
    return this.scheduler
      .plan(current, predicted.slice(0, Math.floor(cap / (tileRows * tileCols))))
      .then((results) =>
        resident.concat(results.filter((entry): entry is ViewportEntry => Boolean(entry))),
      );
  }

  // ---- Fallback Style Tiles ----

  private canvasTheme(): CanvasThemePalette {
    const root = document.documentElement;
    const key = root.getAttribute('data-reading-preset') ?? '';
    if (this.fallbackPalette?.key === key) return this.fallbackPalette;
    const css = getComputedStyle(root);
    const color = (name: string): string => css.getPropertyValue(name).trim();
    const paper = color('--grid-paper');
    const surface = color('--grid-head');
    this.fallbackPalette = {
      key,
      paper,
      surface,
      neutral: `color-mix(in srgb, ${paper} 50%, ${surface})`,
      line: color('--grid-line-color'),
      muted: color('--grid-head-ink'),
      tableHead: color('--grid-accent-soft'),
      band: color('--grid-band'),
      dark: root.getAttribute('data-reading-tone') === 'dark',
    };
    return this.fallbackPalette;
  }

  paintViewportFallback(req: ViewportRequest | null): void {
    const { S, geometry, motion, virtualScroll, tiles, colName, panels } = this.d;
    const host = requireEl('viewportFallback');
    if (!req) {
      cancelAnimationFrame(S.fallbackFrame);
      S.fallbackFrame = 0;
      this.styleTiles.clear();
      this.styleIdentity = '';
      S.viewportActivity?.end();
      S.viewportActivity = null;
      host.replaceChildren();
      host.hidden = true;
      return;
    }
    if (viewportCoversVisible(S.window, req)) {
      S.viewportActivity?.end();
      S.viewportActivity = null;
    } else {
      S.viewportActivity ??= beginActivity(localize('ui.viewportPipeline.readingVisibleAreas'), { slot: 'viewportActivitySlot' });
    }
    const pos = req.position;
    const vw = pos?.w ?? 0;
    const vh = pos?.h ?? 0;
    if (!pos || !vw || !vh) return;
    const { x, y, pw, ph } = pos;
    const physical = pos.physical ?? requireEl('grid').scrollTop;
    const pinned = pos.pinned ?? { rows: 0, cols: 0 };
    const theme = this.canvasTheme();
    const hh = this.headerHeight();
    const cap = 4 * 1024 * 1024;
    const ratio = Math.min(2, window.devicePixelRatio || 1, Math.sqrt((cap * 0.48) / (vw * vh)));
    const pixelW = Math.max(1, Math.ceil((vw * ratio) / 3));
    const pixelH = Math.max(1, Math.ceil((vh * ratio) / 3));
    const tw = pixelW / ratio;
    const th = pixelH / ratio;
    const slots = Math.max(1, Math.min(48, Math.floor(cap / (pixelW * pixelH))));
    const identity = [
      req.context,
      req.filterId,
      req.viewId,
      req.excludeThroughRow,
      S.zoom,
      geometry.version,
      S.showStyles,
      S.sourceGridLines,
      S.textHeaderRevision,
      theme.key,
      this.rowHeadWidth,
      pw,
      ph,
      ratio,
      pixelW,
      pixelH,
    ].join('|');
    if (this.styleIdentity !== identity) {
      this.styleTiles.clear();
      host.replaceChildren();
      this.styleIdentity = identity;
    }
    const lead = motion.predict(vw, vh, performance.now());
    const firstX = Math.floor(x / tw);
    const firstY = Math.floor(y / th);
    const endX = Math.floor((x + vw - 0.01) / tw);
    const endY = Math.floor((y + vh - 0.01) / th);
    const fromX = Math.max(0, Math.floor((x - Math.min(vw * 1.5, lead.x[0])) / tw));
    const toX = Math.floor((x + vw + Math.min(vw * 1.5, lead.x[1])) / tw);
    const fromY = Math.max(0, Math.floor((y - Math.min(vh * 2, lead.y[0])) / th));
    const toY = Math.floor((y + vh + Math.min(vh * 2, lead.y[1])) / th);
    const desired: { readonly tx: number; readonly ty: number; readonly key: string; readonly visible: boolean; readonly d: number }[] = [];
    for (let ty = fromY; ty <= toY; ty++) {
      for (let tx = fromX; tx <= toX; tx++) {
        const dx = Math.max(firstX - tx, 0, tx - endX);
        const dy = Math.max(firstY - ty, 0, ty - endY);
        const behindX = motion.vx > 0 ? tx < firstX : motion.vx < 0 && tx > endX;
        const behindY = motion.vy > 0 ? ty < firstY : motion.vy < 0 && ty > endY;
        desired.push({
          tx,
          ty,
          key: `${tx},${ty}`,
          visible: !dx && !dy,
          d: dx * dx + dy * dy + (behindX ? dx : 0) + (behindY ? dy : 0),
        });
      }
    }
    desired.sort((a, b) => a.d - b.d);
    desired.length = Math.min(slots, desired.length);
    const wanted = new Set(desired.map((t) => t.key));
    const spares: HTMLCanvasElement[] = [];
    for (const [key, tile] of this.styleTiles) {
      if (!wanted.has(key)) {
        this.styleTiles.delete(key);
        tile.canvas.remove();
        spares.push(tile.canvas);
      } else if (virtualScroll.scale !== 1) {
        tile.canvas.style.top = `${virtualScroll.rowPosition(tile.y, physical)}px`;
      }
    }
    const started = performance.now();
    const lookup = tiles.reader(req);
    const offset = S.filtered ? ph : 0;
    const styleFor = (
      record: { readonly cell: CellData | undefined; readonly row: RowData | undefined },
      c: number,
      r: number,
    ): VisualStyle => {
      const sheet = S.sheet ?? '';
      const data =
        panels.editCopy?.grid.active && record.row
          ? (panels.editCopy.grid.project(sheet, `${colName(c)}${record.row.number}`, record.cell) ??
            undefined)
          : record.cell;
      if (!theme.dark && record.row && isTableHeader(S.autoFilter)) {
        if (record.row.number === S.autoFilter.row) return { background: theme.tableHead };
        if (record.row.number > S.autoFilter.row && (r - S.autoFilter.row) % 2 === 0) {
          return { background: theme.band };
        }
      }
      return this.appearance.visualForBorder(data, record.row);
    };
    let deferred = false;
    for (const { key, tx, ty, visible } of desired) {
      const old = this.styleTiles.get(key);
      if (old && !old.dirty) continue;
      if (!visible && performance.now() - started >= 6) {
        deferred = true;
        break;
      }
      const left = tx * tw;
      const top = ty * th;
      const canvas = old?.canvas ?? spares.pop() ?? document.createElement('canvas');
      if (canvas.width !== pixelW) canvas.width = pixelW;
      if (canvas.height !== pixelH) canvas.height = pixelH;
      const ctx = canvas.getContext('2d', { alpha: true });
      if (!ctx) continue;
      ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
      ctx.clearRect(0, 0, tw, th);
      if (!old) {
        canvas.className = 'viewport-fallback';
        canvas.style.width = `${tw}px`;
        canvas.style.height = `${th}px`;
        canvas.style.left = `${left}px`;
        canvas.style.top = `${virtualScroll.rowPosition(top, physical)}px`;
      }
      const minVisual = this.rowAtOffset(Math.max(0, top - hh - offset));
      const maxVisual = Math.min(
        S.filtered ? S.filterTotal : S.maxRow,
        this.rowAtOffset(top + th - hh - offset),
      );
      const firstCol = Math.max(1, this.colAtOffset(Math.max(0, left - this.rowHeadWidth)));
      const lastCol = Math.min(S.maxCol, this.colAtOffset(left + tw - this.rowHeadWidth));
      const colsToPaint: number[] = [];
      for (
        let c = req.colOrder ? req.colOrder.next(firstCol) : firstCol;
        c && c <= lastCol;
        c = req.colOrder ? req.colOrder.next(c + 1) : c + 1
      ) {
        colsToPaint.push(c);
      }
      paintMissingCells({
        ctx,
        pinned,
        top: Math.max(0, hh + ph - top),
        left: Math.max(0, this.rowHeadWidth + pw - left),
        minVisual,
        maxVisual,
        colsToPaint,
        scrollLeft: left,
        w: tw,
        h: th,
        hh,
        rowHead: this.rowHeadWidth,
        state: S,
        scrollTop: top,
        sourceRowTop: (row) => this.sourceRowTop(row),
        colWidth: (col) => this.colWidth(col),
        colLeft: (col) => this.colLeft(col),
        rowHeight: (row) => this.rowHeight(row),
        rowTop: (row) => this.rowTop(row),
        nextVisibleRow: (row) => this.nextVisibleRow(row),
        rowVisible: (row) => this.rowVisible(row),
        lookup,
        mergeAt: (row, col) => this.activeMerge(row, col),
        theme,
        styleFor,
      });
      if (!old) host.append(canvas);
      this.styleTiles.set(key, {
        canvas,
        y: top,
        minVisual,
        maxVisual,
        firstCol,
        lastCol,
        dirty: false,
      });
    }
    host.hidden = false;
    if (deferred) this.scheduleStylePaint();
  }

  updateViewportLoading(): void {
    const { S } = this.d;
    this.paintViewportFallback(
      S.indexReady && S.sheet ? (this.lastViewportDemand ?? this.getViewportRequest()) : null,
    );
  }

  scheduleStylePaint(): void {
    const { S } = this.d;
    if (!S.fallbackFrame) {
      S.fallbackFrame = requestAnimationFrame(() => {
        S.fallbackFrame = 0;
        this.updateViewportLoading();
      });
    }
  }

  invalidateViewport(options: { readonly preserveDOM?: boolean } = {}): void {
    const { preserveDOM = false } = options;
    const { S, motion, tiles } = this.d;
    S.previewAbort?.abort();
    S.previewAbort = null;
    S.previewOnly = false;
    S.previewData = null;
    cancelAnimationFrame(S.previewFrame);
    S.previewFrame = 0;
    cancelAnimationFrame(S.viewportFrame);
    S.viewportFrame = 0;
    this.paintViewportFallback(null);
    S.viewportForce = false;
    this.cancelGridWork();
    this.rowPool.clear();
    this.poolCells = 0;
    this.appearance.clearCache();
    this.finishedCells.clear();
    this.scheduler.reset();
    motion.reset();
    this.columnVisibilityKey = '';
    this.lastViewportDemand = null;
    clearTimeout(S.viewportRetryTimer);
    S.viewportRetryTimer = 0;
    S.viewportRetryAttempt = 0;
    ++S.viewportRequest;
    S.viewportData = null;
    tiles.clear();
    S.window = null;
    if (!preserveDOM) this.resetGridDOM();
  }

  private publishViewport(entry: ViewportEntry): void {
    const { S, syncFilteredSelection, getFrozenPanes } = this.d;
    S.window = entry.bounds;
    S.viewportData = entry;
    clearTimeout(S.viewportRetryTimer);
    S.viewportRetryTimer = 0;
    S.viewportRetryAttempt = 0;
    if (entry.request.filtered) {
      S.filterTotal = entry.data.totalRows;
      syncFilteredSelection(entry.request.excludeThroughRow);
    }
    const frozenPanes = getFrozenPanes();
    if (S.freeze.rows || S.freeze.cols || frozenPanes.base) frozenPanes.schedule();
    this.scheduleStylePaint();
  }

  cancelGridWork(): void {
    if (!this.gridWork) return;
    cancelAnimationFrame(this.gridWork.frame);
    this.gridWork.resolve();
    this.gridWork = null;
  }

  private applyViewport(entry: ViewportEntry, sliced: boolean, urgent = false): Promise<void> | undefined {
    const { S, panels } = this.d;
    this.cancelGridWork();
    if (!sliced || panels.editCopy?.grid.active) {
      this.renderGrid(entry.data, entry.request);
      this.publishViewport(entry);
      return undefined;
    }
    if (urgent) {
      const demand = entry.request;
      const keep = this.viewportBand(
        demand,
        (demand.r1 ?? 1) - 6,
        (demand.r2 ?? demand.displayEnd) + 6,
        (demand.c1 ?? 1) - 4,
        (demand.c2 ?? demand.startCol + demand.cols - 1) + 4,
      );
      const { displayStart, displayEnd, startCol } = keep;
      const endCol = keep.selectedCols?.at(-1) ?? startCol + keep.cols - 1;
      for (const [v, row] of S.renderedRows) {
        const anchors = entry.data.anchors?.get(v);
        if ((v < displayStart || v > displayEnd) && !anchors) {
          this.retireRow(row);
          S.renderedRows.delete(v);
          S.rowNumberByVisual.delete(v);
          for (const node of row._xlCells?.values() ?? []) {
            if (node.dataset.ref) S.cellMap.delete(node.dataset.ref);
          }
          continue;
        }
        for (const [col, node] of row._xlCells ?? []) {
          if ((col >= startCol && col <= endCol) || anchors?.includes(col)) continue;
          node.remove();
          row._xlCells?.delete(col);
          if (node.dataset.ref) S.cellMap.delete(node.dataset.ref);
        }
      }
      if (S.window) {
        S.window = {
          ...S.window,
          displayStart: Math.max(displayStart, S.window.displayStart),
          displayEnd: Math.min(displayEnd, S.window.displayEnd),
          startCol: Math.max(S.window.startCol, startCol),
          endCol: Math.min(S.window.endCol, endCol),
        };
      }
    }
    const iterator = this.gridSteps(entry.data, entry.request, true, urgent ? 24 : 6, urgent);
    let resolveFn: () => void = () => undefined;
    let rejectFn: (reason?: unknown) => void = () => undefined;
    const promise = new Promise<void>((ok, fail) => {
      resolveFn = ok;
      rejectFn = fail;
    });
    const browserScheduler = (window as { readonly scheduler?: BrowserScheduler }).scheduler;
    const job: GridWorkJob = {
      entry,
      resolve: resolveFn,
      promise,
      frame: 0,
      queued: false,
      step: () => {
        if (this.gridWork !== job) return;
        try {
          const latest = this.lastViewportDemand ?? entry.request;
          const b = entry.bounds;
          if (
            !sameViewportRows(b, latest) ||
            b.displayEnd < latest.visibleStart ||
            b.displayStart > latest.visibleEnd ||
            b.endCol < latest.visibleColStart ||
            b.startCol > latest.visibleColEnd
          ) {
            this.cancelGridWork();
            this.scheduleViewport();
            return;
          }
          if (!iterator.next().done) {
            if (!job.queued) {
              job.queued = true;
              const resume = (): void => {
                job.queued = false;
                job.step();
              };
              if (typeof browserScheduler?.postTask === 'function') {
                browserScheduler.postTask(resume, { priority: 'user-visible' }).catch(rejectFn);
              } else {
                job.frame = requestAnimationFrame(resume);
              }
            }
            return;
          }
          this.gridWork = null;
          this.publishViewport(entry);
          resolveFn();
          if (!viewportCoversVisible(b, latest)) this.scheduleViewport();
        } catch (error: unknown) {
          this.gridWork = null;
          rejectFn(error);
        }
      },
    };
    this.gridWork = job;
    job.step();
    return promise;
  }

  private mountCachedLeadingEdge(request: ViewportRequest): boolean {
    const { S, motion, toast, panels } = this.d;
    if (!S.window || !sameViewportRows(S.window, request) || panels.editCopy?.grid.active) {
      return false;
    }
    if (this.gridWork && viewportCoversVisible(this.gridWork.entry.bounds, request)) {
      if (!viewportCoversVisible(S.window, request)) this.gridWork.step();
      return true;
    }
    const ranks = request.visibleRanks;
    const prior = S.viewportData?.request;
    const pos = request.position;
    if (
      !ranks ||
      !prior ||
      !pos?.h ||
      !pos.w ||
      Math.abs(motion.dy) > pos.h * 1.5 ||
      Math.abs(motion.dx) > pos.w * 1.5
    ) {
      return false;
    }
    const [v1, v2, u1, u2] = ranks;
    const from = Math.max(1, u1 - (motion.vx < 0 ? 2 : 1));
    const core = this.viewportBand(
      request,
      v1 - (motion.vy < 0 ? 4 : 1),
      v2 + (motion.vy > 0 ? 4 : 1),
      from - TEXT_SPILL_LOOKAHEAD,
      u2 + (motion.vx > 0 ? 2 : 1),
    );
    core.mountColStart = request.colOrder?.at(from - 1) ?? from;
    this.foregroundLookups++;
    const entry = this.projectViewport(core);
    if (!entry) return false;
    this.foregroundHits++;
    this.applyViewport(entry, true, true)?.catch((error: unknown) => {
      toast(errorMessage(error), 'error');
    });
    return true;
  }

  scheduleViewport(force = false): void {
    const { S, toast } = this.d;
    if (S.previewOnly) {
      this.scheduleInitialPreview();
      return;
    }
    if (!S.indexReady || !S.sheet) return;
    S.viewportForce ||= force;
    const target = (this.lastViewportDemand = this.getViewportRequest());
    if (!S.viewportForce) {
      const covered = viewportCoversVisible(S.window, target);
      if (
        covered
          ? Boolean(this.gridWork) || viewportHasHeadroom(S.window, target)
          : this.mountCachedLeadingEdge(target)
      ) {
        void this.planViewport(target, false);
        this.paintViewportFallback(target);
        return;
      }
    }
    this.paintViewportFallback(target);
    if (S.viewportFrame) return;
    S.viewportFrame = requestAnimationFrame(() => {
      S.viewportFrame = 0;
      const shouldForce = S.viewportForce;
      S.viewportForce = false;
      this.loadViewport(shouldForce).catch((error: unknown) => {
        toast(errorMessage(error), 'error');
      });
    });
  }

  private async readViewportTile(
    job: TileJobItem,
    signal: AbortSignal,
  ): Promise<ViewportEntry | undefined> {
    const { S, api, tiles, motion, startConditionalStats } = this.d;
    const request = job.request;
    const t = performance.now();
    const withConditional = S.showStyles;
    const params = new URLSearchParams({
      sheet: job.sheet,
      limit: String(request.limit),
      col: String(request.startCol),
      cols: String(request.cols),
      compact: '1',
      rowWindow: '1',
    });
    if (!withConditional) params.set('conditional', '0');
    if (request.selectedCols) params.set('columns', request.selectedCols.join(','));
    if (request.viewId) params.set('viewId', request.viewId);
    if (request.filtered) {
      params.set('offset', String(request.offset));
      params.set('filterId', request.filterId);
      params.set('excludeThroughRow', String(request.excludeThroughRow));
    } else {
      params.set('row', String(request.row));
    }
    const data = await api(`/api/sheet?${params.toString()}`, {
      signal,
      activity: false,
    }, decodeSheetViewport);
    if (signal.aborted || job.epoch !== S.sheetEpoch || request.context !== this.viewportContext()) {
      return undefined;
    }
    if (
      data.sheet !== job.sheet ||
      (data.viewId ?? '') !== request.viewId ||
      (data.filterId ?? '') !== request.filterId
    ) {
      throw new Error(localize('ui.viewportPipeline.worksheetDisplayAreaChangedReload'));
    }
    if (!withConditional) {
      if (S.showStyles) return undefined;
      S.tilesSkippedConditional = true;
    }
    if (!job.foreground && estimateViewportBytes(data) > VIEWPORT_CACHE_BUDGET / 4) {
      return undefined;
    }
    const entry = tiles.store(job.key, data, request);
    this.bytesPerCell =
      this.bytesPerCell * 0.75 + Math.max(64, entry.bytes / (request.cols * request.limit)) * 0.25;
    motion.observe('read', performance.now() - t);
    const b = entry.bounds;
    for (const tile of this.styleTiles.values()) {
      if (
        b.displayEnd >= tile.minVisual &&
        b.displayStart <= tile.maxVisual &&
        b.endCol >= tile.firstCol &&
        b.startCol <= tile.lastCol
      ) {
        tile.dirty = true;
      }
    }
    this.scheduleStylePaint();
    if (
      S.showStyles &&
      data.conditionalStatsPending &&
      !S.conditionalStatsJob &&
      S.conditionalStatsState !== 'cancelled' &&
      S.conditionalStatsState !== 'error'
    ) {
      void startConditionalStats(job.sheet, job.epoch, { refreshOnReady: true });
    }
    return entry;
  }

  async loadViewport(
    force = false,
    expectedEpoch: number = this.d.S.sheetEpoch,
    strict = false,
  ): Promise<void> {
    const { S } = this.d;
    if (!S.indexReady || !S.sheet || expectedEpoch !== S.sheetEpoch) return;
    const request = (this.lastViewportDemand = this.getViewportRequest());
    const ticket = S.viewportRequest;
    if (!force && viewportHasHeadroom(S.window, request)) {
      void this.planViewport(request, false);
      this.updateViewportLoading();
      return;
    }
    if (!force && this.gridWork && viewportCoversVisible(this.gridWork.entry.bounds, request)) {
      await this.gridWork.promise;
      return;
    }
    let entry = this.projectViewport(request);
    this.foregroundLookups++;
    if (entry) {
      this.foregroundHits++;
      void this.planViewport(request, false);
    } else {
      let received: ViewportEntry[];
      try {
        received = await this.planViewport(request, true);
      } catch (error: unknown) {
        if (
          ticket !== S.viewportRequest ||
          expectedEpoch !== S.sheetEpoch ||
          (error instanceof Error && error.name === 'AbortError')
        ) {
          return;
        }
        if (strict) throw error;
        if (!S.viewportRetryTimer && ++S.viewportRetryAttempt <= 2) {
          S.viewportRetryTimer = window.setTimeout(() => {
            S.viewportRetryTimer = 0;
            this.scheduleViewport(true);
          }, S.viewportRetryAttempt * 160);
        } else if (!S.viewportRetryTimer) {
          throw error;
        }
        return;
      }
      if (ticket !== S.viewportRequest || expectedEpoch !== S.sheetEpoch || !reread(() => S.indexReady)) return;
      const latest = this.getViewportRequest();
      if (!force && viewportHasHeadroom(S.window, latest)) return;
      entry = this.projectViewport(latest, received);
      if (!entry) {
        this.scheduleViewport();
        return;
      }
    }
    if (!force && this.gridWork) {
      const b = this.gridWork.entry.bounds;
      if (
        sameViewportRows(b, request) &&
        b.displayEnd >= request.visibleStart &&
        b.displayStart <= request.visibleEnd &&
        b.endCol >= request.visibleColStart &&
        b.startCol <= request.visibleColEnd
      ) {
        await this.gridWork.promise;
        return;
      }
    }
    await this.applyViewport(entry, !force && !strict);
  }

  // ---- Initial Preview ----

  async paintInitialPreview(name: string, epoch: number): Promise<void> {
    const { S, api, geometry, finishSheetSwitch } = this.d;
    if (S.filtered) return;
    const controller = new AbortController();
    S.previewAbort = controller;
    try {
      const data = await api(
        `/api/preview?compact=1&sheet=${encodeURIComponent(name)}`,
        { signal: controller.signal },
        decodeSheetPreview,
      );
      if (controller.signal.aborted || epoch !== S.sheetEpoch || S.indexReady || !data.available) {
        return;
      }
      S.previewOnly = true;
      S.previewData = data;
      S.rowSizes = {};
      S.columnWidths = {};
      S.hiddenColumns = new Set();
      geometry.rows.load({});
      geometry.cols.load({});
      S.maxRow = Math.max(1, Math.min(120, data.maxRow ?? 80));
      S.maxCol = Math.max(12, Math.min(40, (data.maxCol ?? 1) + 3));
      S.virtualScrollScale = 1;
      this.updateCanvasSize();
      this.renderInitialPreview();
      finishSheetSwitch(epoch);
      requireEl('gridStatus').textContent = localize('ui.viewportPipeline.firstScreenPreviewCompleteIndex', { name });
    } catch {
      // Full indexing remains the authoritative validation path.
    }
  }

  renderInitialPreview(): void {
    const { S } = this.d;
    if (!S.previewOnly || !S.previewData || S.indexReady) return;
    const request = this.getViewportRequest();
    const first = Math.max(1, request.visibleColStart - TEXT_SPILL_LOOKAHEAD);
    const last = Math.min(S.maxCol, request.visibleColEnd + TEXT_SPILL_LOOKAHEAD);
    const columns = Array.from({ length: Math.max(0, last - first + 1) }, (_, i) => first + i);
    const data: SheetViewportResponse = {
      sheet: S.previewData.sheet ?? S.sheet ?? '',
      offset: 0,
      limit: S.maxRow,
      totalRows: S.previewData.totalRows ?? S.maxRow,
      filtered: false,
      maxRow: S.previewData.maxRow ?? S.maxRow,
      maxCol: S.previewData.maxCol ?? S.maxCol,
      startCol: first,
      colCount: columns.length,
      columns,
      rows: S.previewData.rows ?? [],
    };
    this.renderGrid(data, {
      ...request,
      filtered: false,
      displayStart: Math.max(1, request.visibleStart - 8),
      displayEnd: Math.min(S.maxRow, request.visibleEnd + 8),
    });
  }

  scheduleInitialPreview(): void {
    const { S } = this.d;
    if (S.previewFrame) return;
    S.previewFrame = requestAnimationFrame(() => {
      S.previewFrame = 0;
      this.renderInitialPreview();
    });
  }

  // ---- DOM Rendering, Merges, Selection Classes & Range Frames ----

  resetGridDOM(): void {
    const { S } = this.d;
    S.renderedRows = new Map();
    S.renderedScrollTop = requireEl('grid').scrollTop;
    S.renderedHeadersKey = '';
    requireEl('rowsLayer').replaceChildren();
    requireEl('columnHeaders').replaceChildren();
  }

  followGridScroll(): void {
    const { S, virtualScroll, getFrozenPanes } = this.d;
    if (virtualScroll.scale === 1 || !S.renderedRows.size) return;
    const physical = requireEl('grid').scrollTop;
    if (physical === S.renderedScrollTop) return;
    const frozenHeight = S.filtered ? this.sourceRowTop(getFrozenPanes().effective().rows + 1) : 0;
    for (const [visual, row] of S.renderedRows) {
      row.style.top = `${virtualScroll.rowPosition(this.rowTop(visual) + frozenHeight, physical)}px`;
    }
    S.renderedScrollTop = physical;
  }

  activeMerge(row: number, col: number): MergeRange | null {
    const { S, mergedCells } = this.d;
    if (!S.indexReady || S.filtered) return null;
    const range = mergedCells.at(row, col);
    if (!range) return null;
    if (S.rowVisibility && !S.rowVisibility.visible(range.row)) return null;
    const { rows, cols } = S.freeze;
    if ((range.row <= rows && range.endRow > rows) || (range.col <= cols && range.endCol > cols)) {
      return null;
    }
    return range;
  }

  decorateMergedCell(
    node: GridCellElement,
    row: number,
    col: number,
    focus?: SelectionHighlight | null,
  ): void {
    const { mergedCells, selectionHighlight } = this.d;
    mergedCells.decorate(
      node,
      this.activeMerge(row, col),
      row,
      col,
      (c) => this.colLeft(c),
      (r) => this.rowTop(r),
      (c) => this.colWidth(c),
    );
    if (node._xlMerge) {
      const target = focus ? focus.mergeTarget : selectionHighlight().mergeTarget;
      mergedCells.paintCrosshair(node, target, (c) => this.colLeft(c), (r) => this.rowTop(r));
    }
  }

  private replaceCellText(node: HTMLElement, value: string): void {
    if (node.firstElementChild?.classList.contains('cell-overflow-content')) {
      node.firstElementChild.textContent = value;
    } else if (node.firstChild?.nodeType === Node.TEXT_NODE) {
      node.firstChild.nodeValue = value;
    } else {
      node.insertBefore(document.createTextNode(value), node.firstChild);
    }
  }

  toggleFormulaView(): void {
    const { S, toast, getFrozenPanes } = this.d;
    S.showFormulas = !S.showFormulas;
    requireButton('formulaViewBtn').setAttribute('aria-pressed', String(S.showFormulas));
    if (S.viewportData) {
      this.renderGrid(S.viewportData.data, S.viewportData.request);
      getFrozenPanes().schedule(true);
    } else {
      for (const el of document.querySelectorAll<HTMLElement>('.grid-region .grid-cell')) {
        const ref = el.dataset.ref ?? '';
        const data = S.cellMap.get(ref) ?? S.frozenCellMap.get(ref);
        this.replaceCellText(el, this.cellPresenter.value(data));
        el.classList.toggle('formula-view-cell', Boolean(S.showFormulas && data?.hasFormula));
      }
    }
    toast(S.showFormulas ? localize('ui.viewportPipeline.displayFormula') : localize('ui.viewportPipeline.displayValue'));
  }

  private updateGridCellGeometry(cell: GridCellElement, layout: GridColumnLayout): void {
    if (cell._xlColumnLeft !== layout.left) {
      cell.style.left = `${layout.left}px`;
      cell._xlColumnLeft = layout.left;
    }
    if (cell._xlColumnWidth !== layout.width) {
      cell.style.width = `${layout.width}px`;
      cell._xlColumnWidth = layout.width;
    }
  }

  makeGridCell(
    cellData: CellData | undefined,
    rowData: RowData | undefined,
    rowNumber: number,
    visual: number,
    layout: GridColumnLayout,
  ): GridCellElement {
    const { col, letter } = layout;
    const ref = `${letter}${rowNumber}`;
    const cell = this.cellPresenter.create(cellData, rowData, rowNumber, col);
    cell.dataset.ref = ref;
    cell.dataset.row = String(rowNumber);
    cell.dataset.col = String(col);
    cell.dataset.visualIndex = String(visual);
    this.updateGridCellGeometry(cell, layout);
    return cell;
  }

  paintRangeCell(
    node: GridCellElement,
    row: number,
    col: number,
    visual: number,
    list: readonly SelectionArea[] = MultiSelection.areas(this.d.S),
  ): void {
    const { S, panels } = this.d;
    if (panels.editCopy?.grid.active && S.sheet) {
      panels.editCopy.grid.ranges.paintCut(node, S.sheet, row, col);
    }
    const through = S.filtered ? S.selectedFrozenThrough || 0 : 0;
    const inside = list.some((area) =>
      MultiSelection.contains(area, row, col, visual, S.filtered, through),
    );
    if (inside) {
      const style = this.cellPresenter.style(node._xlModel, node._xlRowStyle) ?? {};
      const foreground =
        S.showFormulas && node._xlModel?.hasFormula
          ? '#315d8b'
          : S.showStyles
            ? conditionalColor(node._xlModel, style)
            : undefined;
      const colorKey = `${style.background ?? ''}|${foreground ?? ''}`;
      if (node._xlRangeColor !== colorKey) {
        node._xlRangeColor = colorKey;
        node.style.setProperty('--range-fill', selectionFill(style.background, foreground));
      }
    }
    if (node._xlInRange !== inside) {
      node._xlInRange = inside;
      node.classList.toggle('range-cell', inside);
    }
  }

  private selectionSeams(
    view: RangeFrameView,
    pinned: { readonly rows: number; readonly cols: number },
  ): (x1: number, x2: number, y1: number, y2: number) => SeamEdges {
    const { S, colName, getFrozenPanes } = this.d;
    const rowBands = new Map<
      number,
      { readonly row: number; readonly visual: number; readonly from: number; readonly to: number }
    >();
    const columns = new Set<number>();
    const styles = new WeakMap<Element, CSSStyleDeclaration>();
    const ratio = window.devicePixelRatio || 1;
    const layer = requireEl('rangeFrames');
    if (ratio !== this.selectionRasterRatio) {
      this.selectionRasterStep =
        parseFloat(getComputedStyle(layer, '::before').borderTopWidth) || 1 / ratio;
      this.selectionRasterRatio = ratio;
    }
    const raster =
      Math.abs(this.selectionRasterStep * ratio - 1) > 0.001
        ? { step: this.selectionRasterStep, origin: layer.getBoundingClientRect() }
        : undefined;
    const addRow = (row: number, visual: number): void => {
      const [from, to] = view.rowBounds(row, visual);
      if (to > from) rowBands.set(from, { row, visual, from, to });
    };
    for (const [visual, node] of S.renderedRows) addRow(Number(node.dataset.row), visual);
    for (let row = 1; row <= pinned.rows; row++) if (this.rowVisible(row)) addRow(row, row);
    const first = S.renderedRows.values().next().value;
    for (const col of first?._xlCells?.keys() ?? []) columns.add(col);
    for (let col = 1; col <= pinned.cols; col++) columns.add(col);
    const rows = [...rowBands.values()].sort((a, b) => a.from - b.from);
    const cols = [...columns]
      .sort((a, b) => a - b)
      .map((col) => ({ col, from: this.colLeft(col), to: this.colLeft(col + 1) }))
      .filter((c) => c.to > c.from);
    const frozenPanes = getFrozenPanes();
    const owner = (
      band: { readonly row: number; readonly visual: number },
      col: number,
    ): Element | undefined => {
      const merge = this.activeMerge(band.row, col);
      const r = merge?.row ?? band.row;
      const c = merge?.col ?? col;
      const frozen = r <= pinned.rows || c <= pinned.cols;
      return (
        (frozen ? frozenPanes.cells.get(`${colName(c)}${r}`) : undefined) ??
        S.renderedRows.get(merge ? r : band.visual)?._xlCells?.get(c)
      );
    };
    const edge = (
      horizontal: boolean,
      position: number,
      start: number,
      end: number,
    ): SeamSegment[] => {
      const leading = position === 0;
      const segments: { readonly from: number; to: number; readonly offset: number }[] = [];
      if (horizontal) {
        const rowBand = rows.find((b) => (leading ? b.from === 0 : b.to === position));
        for (const colBand of cols) {
          if (colBand.to <= start || colBand.from >= end) continue;
          let width = 0;
          let inside = false;
          if (rowBand) {
            const cell = owner(rowBand, colBand.col);
            const side = leading ? 'Top' : 'Bottom';
            width = usedBorderWidth(cell, side, colBand.col, styles);
            inside = leading && width > 0;
            if (position > 0 && position === view.frozenHeight) {
              const paneId = colBand.col <= pinned.cols ? 'freezeCorner' : 'freezeTop';
              width = usedBorderWidth(requireEl(paneId), 'Bottom', 0, styles) || width;
            }
            if (leading && !width) {
              width = usedBorderWidth(requireEl('columnHeaders'), 'Bottom', 0, styles);
            }
          }
          const offset = (inside ? width : -width) / 2;
          const from = Math.max(start, colBand.from);
          const to = Math.min(end, colBand.to);
          const last = segments.at(-1);
          if (last?.to === from && last.offset === offset) last.to = to;
          else segments.push({ from, to, offset });
        }
      } else {
        const colBand = cols.find((b) => (leading ? b.from === 0 : b.to === position));
        for (const rowBand of rows) {
          if (rowBand.to <= start || rowBand.from >= end) continue;
          let width = 0;
          let inside = false;
          if (colBand) {
            const cell = owner(rowBand, colBand.col);
            const side = leading ? 'Left' : 'Right';
            width = usedBorderWidth(cell, side, rowBand.row, styles);
            inside = leading && width > 0;
            if (position > 0 && position === view.frozenWidth) {
              const paneId = rowBand.row <= pinned.rows ? 'freezeCorner' : 'freezeLeft';
              width = usedBorderWidth(requireEl(paneId), 'Right', 0, styles) || width;
            }
            if (leading && !width) {
              const header = S.renderedRows.get(rowBand.visual)?.firstElementChild;
              width = usedBorderWidth(header, 'Right', 0, styles);
            }
          }
          const offset = (inside ? width : -width) / 2;
          const from = Math.max(start, rowBand.from);
          const to = Math.min(end, rowBand.to);
          const last = segments.at(-1);
          if (last?.to === from && last.offset === offset) last.to = to;
          else segments.push({ from, to, offset });
        }
      }
      return segments;
    };
    return (x1, x2, y1, y2) => ({
      raster,
      top: edge(true, y1, x1, x2),
      bottom: edge(true, y2, x1, x2),
      left: edge(false, x1, y1, y2),
      right: edge(false, x2, y1, y2),
    });
  }

  scheduleRangeFrames(): void {
    const { S } = this.d;
    if (S.rangeFrameRaf) return;
    S.rangeFrameRaf = requestAnimationFrame(() => {
      S.rangeFrameRaf = 0;
      this.paintRangeFrames();
    });
  }

  paintRangeFrames(areas: readonly SelectionArea[] = MultiSelection.areas(this.d.S)): void {
    const { S, crosshairCell, getFrozenPanes, visibleReferenceAreas, panels } = this.d;
    if (S.rangeFrameRaf) {
      cancelAnimationFrame(S.rangeFrameRaf);
      S.rangeFrameRaf = 0;
    }
    const layer = requireEl('rangeFrames');
    const visible = S.indexReady || S.previewOnly;
    const rangeAreas = areas;
    layer.parentElement?.classList.toggle('has-range', visible && rangeAreas.length > 0);
    const single = visible && !rangeAreas.length ? S.selected : null;
    const hasCrosshair = visible && (rangeAreas.length > 0 || Boolean(S.selected));
    const tails = requireEl('crosshairTails');
    tails.hidden = !hasCrosshair;
    layer.parentElement?.classList.toggle('has-cell-selection', Boolean(single));
    let frameAreas: readonly SelectionArea[] = rangeAreas;
    if (single) {
      const merge = this.activeMerge(single.row, single.col);
      frameAreas = [
        {
          row1: merge?.row ?? single.row,
          col1: merge?.col ?? single.col,
          row2: merge?.endRow ?? single.row,
          col2: merge?.endCol ?? single.col,
          visual1: S.selectedVisualIndex,
          visual2: S.selectedVisualIndex,
        },
      ];
    }
    const references = byId('formulaEntryFrames');
    const refs: ReferenceOverlayArea[] =
      visible && panels.editCopy?.grid.active && S.sheet
        ? [...panels.editCopy.grid.entry.areas(S.sheet)]
        : [];
    if (visible && panels.editCopy?.grid.active && panels.editCopy.grid.fillHandle?.area) {
      refs.push(panels.editCopy.grid.fillHandle.area);
    }
    if (
      !layer.childElementCount &&
      !frameAreas.length &&
      !references?.childElementCount &&
      !refs.length
    ) {
      return;
    }
    const pinned = S.indexReady ? getFrozenPanes().effective() : { rows: 0, cols: 0 };
    const grid = requireEl('grid');
    const frozenHeight = this.sourceRowTop(pinned.rows + 1);
    let seamsFn: ((x1: number, x2: number, y1: number, y2: number) => SeamEdges) | null = null;
    const view: RangeFrameView = {
      width: grid.clientWidth,
      height: grid.clientHeight,
      left: grid.scrollLeft,
      top: this.getVirtualScrollTop(),
      header: this.headerHeight(),
      rowHead: this.rowHeadWidth,
      frozenWidth: this.colLeft(pinned.cols + 1),
      frozenHeight,
      colLeft: (c) => this.colLeft(c),
      rowBounds: (row, visual = row) =>
        S.filtered && row > pinned.rows
          ? [frozenHeight + this.rowTop(visual), frozenHeight + this.rowTop(visual + 1)]
          : [this.sourceRowTop(row), this.sourceRowTop(row + 1)],
      seams: (x1, x2, y1, y2) => {
        seamsFn ??= this.selectionSeams(view, pinned);
        return seamsFn(x1, x2, y1, y2);
      },
    };
    paintRangeFrameBoxes(layer, visible ? frameAreas : [], view);
    if (hasCrosshair) {
      let tailAreas: readonly SelectionArea[];
      if (single) {
        const focus = crosshairCell() ?? single;
        const visual = S.filtered ? S.selectedVisualIndex : focus.row;
        const showRow = !S.filtered || focus.row <= pinned.rows || visual > 0;
        tailAreas = [
          {
            row1: showRow ? focus.row : 0,
            row2: showRow ? focus.row : 0,
            col1: focus.col,
            col2: focus.col,
            visual1: visual,
            visual2: visual,
          },
        ];
      } else if (S.wholeRowSelected && S.selected) {
        const visual = S.filtered ? S.selectedVisualIndex : S.selected.row;
        const showRow = !S.filtered || S.selected.row <= pinned.rows || visual > 0;
        tailAreas = [
          {
            row1: showRow ? S.selected.row : 0,
            row2: showRow ? S.selected.row : 0,
            col1: 0,
            col2: 0,
            visual1: visual,
            visual2: visual,
          },
        ];
      } else {
        tailAreas = rangeAreas;
      }
      paintCrosshairTails(
        tails,
        tailAreas,
        view,
        this.colTotal(S.maxCol),
        this.sheetHeight(S.filtered ? S.filterTotal : S.maxRow),
      );
    }
    if (references && (refs.length || references.childElementCount)) {
      const shown: readonly FrameArea[] = visibleReferenceAreas
        ? visibleReferenceAreas(refs, S.filtered, pinned.rows, S.rowNumberByVisual, (r, c) =>
            this.activeMerge(r, c),
          )
        : refs.map((r) => ({ ...r, visual1: r.visual1 ?? 0, visual2: r.visual2 ?? 0 }));
      paintRangeFrameBoxes(references, shown, view, 2);
    }
  }

  private decorateGridCell(
    cell: GridCellElement,
    row: number,
    col: number,
    ref: string,
    focus: SelectionHighlight,
  ): void {
    const { S, isPrecedent, isSearchMatch, selectedColumnSpan, panels } = this.d;
    const sheet = S.sheet ?? '';
    cell.classList.toggle('selected', S.selected?.ref === ref);
    cell.classList.toggle('active-column', focus.hasCol(col));
    cell.classList.toggle('precedent', S.traceRefs.length > 0 && isPrecedent(row, col));
    cell.classList.toggle('search-match', isSearchMatch(sheet, ref));
    this.paintRangeCell(cell, row, col, Number(cell.dataset.visualIndex));
    const columnSpan = selectedColumnSpan();
    cell.classList.toggle(
      'column-range-cell',
      Boolean(columnSpan && col >= columnSpan.start && col <= columnSpan.end),
    );
    cell.classList.toggle('audit-marked', Boolean(panels.bookmarkCenter?.has(sheet, ref)));
    panels.formulaInspector?.decorateCell(cell, row, col, sheet);
    panels.selectionInspector?.decorateCell(cell, ref, sheet);
  }

  private retireRow(row: GridRowElement): void {
    const { S } = this.d;
    row.remove();
    const cells = row._xlCells?.size ?? 0;
    if (!cells || row._xlSheetEpoch !== S.sheetEpoch || cells > 384 || row._xlHeavy) return;
    const visual = Number(row.dataset.visualIndex);
    const old = this.rowPool.get(visual);
    if (old) this.poolCells -= old._xlCells?.size ?? 0;
    this.rowPool.delete(visual);
    this.rowPool.set(visual, row);
    this.poolCells += cells;
    while (this.poolCells > 384) {
      const oldest = this.rowPool.entries().next().value;
      if (!oldest) break;
      const [k, r] = oldest;
      this.rowPool.delete(k);
      this.poolCells -= r._xlCells?.size ?? 0;
    }
  }

  renderGrid(data: SheetViewportResponse, request: ViewportRequest): void {
    const interrupted = Boolean(this.gridWork);
    this.cancelGridWork();
    const steps = this.gridSteps(data, request, false);
    steps.next();
    if (interrupted) this.scheduleViewport();
  }

  private *gridSteps(
    data: SheetViewportResponse,
    request: ViewportRequest,
    sliced: boolean,
    sliceBudget = sliced ? 6 : 0,
    retain = false,
  ): Generator<void, void, void> {
    const {
      S,
      fileFormats,
      geometry,
      mergedCells,
      motion,
      virtualScroll,
      colName,
      toast,
      selectedColumnSpan,
      selectionHighlight,
      syncTableColumnHeaders,
      updateSearchNavButtons,
      getFrozenPanes,
      panels,
    } = this.d;
    const started = performance.now();
    let sliceStart = started;
    let cpuMs = 0;
    const initialDecor = S.decorVersion;
    this.cellPresenter.prepare(data.rows);
    if (data.hyperlinkError && S.linkErrorSheet !== S.sheet) {
      S.linkErrorSheet = S.sheet ?? '';
      toast(localize('ui.viewportPipeline.failureReadWorksheetHyperlinkMetadata'), 'error');
    }
    const hh = this.headerHeight();
    const canvas = requireEl('gridCanvas');
    const rowExtent = S.filtered ? data.totalRows : S.maxRow;
    const width = this.rowHeadWidth + this.colTotal(S.maxCol);
    const height = hh + this.sheetHeight(rowExtent);
    const grid = requireEl('grid');
    const frozen = getFrozenPanes();
    const pinned = request.position?.pinned ?? frozen.effective();
    const pinnedHeight = S.filtered ? this.sourceRowTop(pinned.rows + 1) : 0;
    const measured = sliced ? request.position : null;
    const canvasWidth = `${Math.max(width, measured?.w ?? grid.clientWidth)}px`;
    const canvasHeight = `${measured?.h ? virtualScroll.configure(height, measured.h) : this.physicalCanvasHeight(height, grid)}px`;
    S.virtualScrollScale = virtualScroll.scale;
    if (canvas.hasAttribute('data-viewport') !== S.indexReady) {
      canvas.toggleAttribute('data-viewport', S.indexReady);
    }
    if (canvas.style.width !== canvasWidth) canvas.style.width = canvasWidth;
    if (canvas.style.height !== canvasHeight) canvas.style.height = canvasHeight;
    if (canvas.style.getPropertyValue('--canvas-width') !== canvasWidth) {
      canvas.style.setProperty('--canvas-width', canvasWidth);
    }

    const columns: readonly number[] = data.columns?.length
      ? data.columns
      : Array.from({ length: data.colCount }, (_, i) => data.startCol + i);
    const visibleColumns = columns.filter((col) => !S.hiddenColumns.has(col));
    const focus = selectionHighlight();
    const headerKey = `${visibleColumns.join(',')}|${S.zoom}|${geometry.version}|${window.devicePixelRatio}|${S.indexReady}|${S.textHeaderRevision}|${this.rowHeadWidth}|${S.sheetEpoch}`;
    const commitHeaders = (): void => {
      if (S.renderedHeadersKey !== headerKey) {
        this.renderColumnHeaders(visibleColumns, width, selectionHighlight());
        S.renderedHeadersKey = headerKey;
      }
    };
    const rowDataByNumber = new Map<number, RowData>();
    const visualRows = new Map<number, RowData>();
    if (S.filtered) {
      data.rows.forEach((row, i) => visualRows.set(request.displayStart + i, row));
    } else {
      for (const row of data.rows) rowDataByNumber.set(row.number, row);
    }

    const models = new Map<string, CellData>();
    const rowNumbers = new Map<number, number>();
    for (const row of data.rows) {
      for (const cell of row.cells) models.set(`${colName(cell.col)}${row.number}`, cell);
    }
    const previous = S.renderedRows;
    const next = retain ? new Map(previous) : new Map<number, GridRowElement>();
    const newNodes = document.createDocumentFragment();
    const firstVisual = request.displayStart;
    const lastVisual = S.filtered
      ? Math.min(data.totalRows, request.displayStart + data.rows.length - 1)
      : Math.min(S.maxRow, request.displayEnd);
    let reusedRows = 0;
    let createdRows = 0;
    let createdCells = 0;
    let liveRoom = sliced && sameViewportRows(S.window, request) ? DOM_MARGIN_BUDGET : 0;
    if (liveRoom) {
      for (const row of previous.values()) liveRoom -= row._xlCells?.size ?? 0;
    }
    const lastCol = (columns.at(-1) ?? data.startCol) + 1;
    const rowPaintWidth = `${this.rowHeadWidth + this.colLeft(lastCol)}px`;
    const layoutFor = (col: number): GridColumnLayout => ({
      col,
      letter: colName(col),
      left: this.rowHeadWidth + this.colLeft(col),
      width: this.colWidth(col),
    });
    const bodyLayouts = visibleColumns.map(layoutFor);
    const layoutVersion = `${geometry.version}|${S.zoom}|${window.devicePixelRatio || 1}`;
    const paintKey = this.cellPresenter.paintKey();
    const cellContext = [
      S.sheetEpoch,
      layoutVersion,
      paintKey,
      this.rowHeadWidth,
      S.source?.format ?? '',
    ].join('|');
    if (cellContext !== this.finishedCellContext) {
      this.finishedCells.clear();
      this.finishedCellContext = cellContext;
    }
    const numericPadding =
      S.showStyles && fileFormats.source(S.source?.format).styled ? 5 * this.scale() : 14;
    const extraMarks = Boolean(
      S.range ||
        S.multiRanges.length ||
        selectedColumnSpan() ||
        S.traceRefs.length ||
        (S.sheet && S.searchHitMap.get(S.sheet)?.size) ||
        panels.bookmarkCenter?.keys?.size ||
        panels.formulaInspector?.active ||
        panels.selectionInspector?.hits?.size ||
        panels.editCopy?.grid.active,
    );
    const mountStart = request.mountColStart ?? visibleColumns[0] ?? 1;
    const mountLeft = this.rowHeadWidth + this.colLeft(mountStart);
    const replacedRows: GridRowElement[] = [];
    const updates: {
      readonly row: GridRowElement;
      readonly cells: Map<number, GridCellElement>;
      readonly newCells: DocumentFragment;
      readonly remove: GridCellElement[];
      readonly below: number | undefined;
    }[] = [];
    const reusable = S.reusableSheetRows;
    const restoring = Boolean(reusable && S.reusableSheetData === data);
    S.reusableSheetRows = null;
    S.reusableSheetData = null;
    const rowsLayer = requireEl('rowsLayer');
    const physicalScroll = measured?.physical ?? grid.scrollTop;
    const visuals: number[] = [...(data.anchors?.keys() ?? [])].filter((r) => r < firstVisual);
    for (
      let r = firstVisual;
      r <= lastVisual;
      r = S.filtered ? r + 1 : this.nextVisibleRow(r + 1) || Infinity
    ) {
      visuals.push(r);
    }
    if (sliced) {
      const priority = (v: number): number => {
        if (v < firstVisual) return -3;
        if (v >= request.visibleStart && v <= request.visibleEnd) {
          const row = previous.get(v);
          return !row?._xlCells?.has(request.visibleColStart) ||
            !row._xlCells.has(request.visibleColEnd)
            ? 0
            : 2;
        }
        return (motion.vy < 0 ? v < request.visibleStart : v > request.visibleEnd) ? 1 : 3;
      };
      const order = new Map<number, number>(visuals.map((v) => [v, priority(v)]));
      visuals.sort(
        (a, b) =>
          (order.get(a) ?? 0) - (order.get(b) ?? 0) || (motion.vy > 0 ? b - a : a - b),
      );
    }
    const navScheduling = (navigator as { readonly scheduling?: BrowserInputScheduling }).scheduling;
    const sheet = S.sheet ?? '';

    for (const visual of visuals) {
      const extra = data.anchors?.get(visual);
      const columnLayouts = extra
        ? [
            ...extra
              .filter((c) => visual < firstVisual || c < data.startCol)
              .sort((a, b) => a - b)
              .map(layoutFor),
            ...(visual < firstVisual ? [] : bodyLayouts),
          ]
        : bodyLayouts;
      if (!S.filtered && !this.rowVisible(visual)) continue;
      const rowData = S.filtered ? visualRows.get(visual) : rowDataByNumber.get(visual);
      if (S.filtered && !rowData) continue;
      const rowNumber = rowData?.number ?? visual;
      rowNumbers.set(visual, rowNumber);
      const oldRow = previous.get(visual);
      const savedRow = reusable?.get(visual);
      let row: GridRowElement | undefined = savedRow ?? oldRow;
      const pooled = !row ? this.rowPool.get(visual) : undefined;
      if (pooled) {
        this.rowPool.delete(visual);
        this.poolCells -= pooled._xlCells?.size ?? 0;
        if (pooled.dataset.row === String(rowNumber)) {
          row = pooled;
          row._xlDecorVersion = -1;
          newNodes.appendChild(row);
        }
      }
      if (savedRow?.dataset.row === String(rowNumber)) {
        savedRow._xlSheetEpoch = S.sheetEpoch;
        savedRow._xlDecorVersion = -1;
        if (savedRow.parentElement !== rowsLayer) newNodes.appendChild(savedRow);
      }
      if (oldRow && oldRow !== row) replacedRows.push(oldRow);
      if (row && (row.dataset.row !== String(rowNumber) || row._xlSheetEpoch !== S.sheetEpoch)) {
        replacedRows.push(row);
        row = undefined;
      }
      if (!row) {
        createdRows++;
        row = document.createElement('div');
        row.className = 'grid-row';
        row.dataset.visualIndex = String(visual);
        row.dataset.row = String(rowNumber);
        row._xlSheetEpoch = S.sheetEpoch;
        row.style.width = rowPaintWidth;
        row._xlCells = new Map<number, GridCellElement>();
        const rowHead = document.createElement('div');
        rowHead.className = 'row-head';
        rowHead.textContent = String(rowNumber);
        const grip = document.createElement('span');
        grip.className = 'row-resize-grip';
        this.sizing.bindResizeGrip(grip, 'row', rowNumber);
        rowHead.append(grip);
        row.appendChild(rowHead);
        newNodes.appendChild(row);
      } else {
        reusedRows++;
        if (row.style.width !== rowPaintWidth) row.style.width = rowPaintWidth;
      }
      const logicalTop = this.rowTop(visual) + pinnedHeight;
      const topPx = `${virtualScroll.rowPosition(logicalTop, physicalScroll)}px`;
      const heightPx = `${this.rowHeight(visual)}px`;
      if (row.style.top !== topPx) row.style.top = topPx;
      if (row.style.height !== heightPx) {
        row.style.height = heightPx;
        row.style.setProperty('--row-height', heightPx);
      }
      const headChild = row.firstElementChild;
      if (headChild instanceof HTMLElement && headChild.style.height !== heightPx) {
        headChild.style.height = heightPx;
      }
      row.classList.toggle('active-row', focus.hasRow(rowNumber, visual));
      row.classList.toggle('whole-row-selected', S.wholeRowSelected && S.selected?.row === rowNumber);
      const tableMode = isTableHeader(S.autoFilter);
      row.classList.toggle('table-header-row', tableMode && S.autoFilter?.row === rowNumber);
      row.classList.toggle(
        'table-band',
        Boolean(
          tableMode &&
            S.autoFilter &&
            rowNumber > S.autoFilter.row &&
            (visual - S.autoFilter.row) % 2 === 0,
        ),
      );
      let byColumn: Record<number, CellData | undefined> = rowColumns(rowData) ?? {};
      if (panels.editCopy?.grid.active) {
        byColumn = { ...byColumn };
        for (const { col, letter } of columnLayouts) {
          byColumn[col] =
            panels.editCopy.grid.project(sheet, `${letter}${rowNumber}`, byColumn[col]) ??
            undefined;
        }
      }
      const belowRow = S.filtered
        ? visualRows.get(visual + 1)
        : rowDataByNumber.get(this.nextVisibleRow(rowNumber + 1));
      const belowColumns: Record<number, CellData | undefined> = rowColumns(belowRow) ?? {};
      const redecorate = row._xlDecorVersion !== S.decorVersion;
      const newCells = document.createDocumentFragment();
      const existingCells = (row._xlCells ??= new Map<number, GridCellElement>());
      const cells = retain ? new Map(existingCells) : new Map<number, GridCellElement>();
      const remove: GridCellElement[] = [];
      const stableRows =
        row._xlBelow === belowRow?.number &&
        row._xlLayoutVersion === layoutVersion &&
        !panels.editCopy?.grid.active;
      const stableColumns = row._xlGridKey === headerKey;
      const mergeFree =
        S.filtered ||
        !mergedCells.root ||
        !mergedCells.intersect(
          rowNumber,
          belowRow?.number ?? rowNumber + 1,
          bodyLayouts[0]?.col ?? 1,
          lastCol,
        ).length;
      row._xlHeavy = Boolean(
        rowData?.cells.some(
          (c) =>
            Boolean(c.richText) ||
            Boolean(c.conditional) ||
            c.raw.length + c.display.length + (c.formula?.length ?? 0) > 512,
        ),
      );

      for (let layoutIndex = 0; layoutIndex < columnLayouts.length; layoutIndex++) {
        if (
          sliced &&
          columnLayouts.length > 48 &&
          (layoutIndex & 7) === 0 &&
          performance.now() - sliceStart >= sliceBudget
        ) {
          cpuMs += performance.now() - sliceStart;
          yield;
          sliceStart = performance.now();
        }
        const layout = columnLayouts[layoutIndex];
        if (!layout) continue;
        const col = layout.col;
        const cellData = byColumn[col];
        const ref = `${layout.letter}${rowNumber}`;
        if (col < mountStart) {
          const merge = this.activeMerge(rowNumber, col);
          if (
            !(merge?.row === rowNumber && merge.col === col && merge.endCol >= mountStart) &&
            !(
              cellData &&
              layout.left +
                this.cellPresenter.spillWidth(
                  cellData,
                  byColumn,
                  columnLayouts,
                  layoutIndex,
                  rowData?.style,
                  rowNumber,
                ) >
                mountLeft
            )
          ) {
            continue;
          }
        }
        let cell = existingCells.get(col);
        if (cell) cells.set(col, cell);
        const nextCol = columnLayouts[layoutIndex + 1]?.col;
        if (
          sliced &&
          cell &&
          stableRows &&
          !redecorate &&
          !cellData?.richText &&
          !cellData?.conditional &&
          !cellData?.hyperlink &&
          cell._xlRowPresentationNotice === (rowData?.presentationNotice ?? '') &&
          cell._xlRowStyle === (rowData?.style ?? 0) &&
          cell._xlModel === (cellData ?? null) &&
          (mergeFree ||
            (!this.activeMerge(rowNumber, col) &&
              !this.activeMerge(rowNumber, nextCol ?? col + 1) &&
              !this.activeMerge(belowRow?.number ?? rowNumber + 1, col) &&
              !this.activeMerge(belowRow?.number ?? rowNumber + 1, nextCol ?? col + 1)))
        ) {
          const interior =
            layoutIndex > 0 &&
            cell._xlNextCol === nextCol &&
            cell._xlColumnLeft === layout.left &&
            cell._xlColumnWidth === layout.width;
          if (stableColumns || (interior && !isTextCell(cellData))) continue;
        }
        const replaced = Boolean(
          cell &&
            (!sameRenderedCell(cell._xlModel, cellData) ||
              cell._xlRowStyle !== (rowData?.style ?? 0) ||
              this.cellPresenter.needsPaint(cell, cellData, rowData)),
        );
        if (replaced && cell) {
          const freshNode = this.makeGridCell(cellData, rowData, rowNumber, visual, layout);
          remove.push(cell);
          newCells.appendChild(freshNode);
          cells.set(col, freshNode);
          cell = freshNode;
          createdCells++;
        }
        const fresh = !cell || replaced;
        const needsDecoration = fresh || redecorate;
        let templateKey: string | null = null;
        let cloned = false;
        if (
          !cell &&
          mergeFree &&
          belowRow &&
          nextCol &&
          cellData &&
          (!cellData.type || cellData.type === 'n') &&
          !cellData.conditional &&
          !cellData.richText &&
          !cellData.hyperlink &&
          !cellData.cacheMissing &&
          !byColumn[nextCol]?.conditional &&
          !belowColumns[col]?.conditional &&
          !belowColumns[nextCol]?.conditional &&
          !rowData?.presentationNotice &&
          !S.showFormulas &&
          !panels.editCopy?.grid.active &&
          rowNumber !== S.autoFilter?.row
        ) {
          const metric = this.cellPresenter.numberMetrics(cellData, rowData?.style);
          if (metric && metric.width <= layout.width - numericPadding + 0.25) {
            const outerLeft = layoutIndex === 0 && col > 1 && layout.left === this.rowHeadWidth;
            templateKey = [
              cellData.style ?? rowData?.style ?? 0,
              cellData.valueColor ?? '',
              cellData.hasFormula,
              byColumn[nextCol]?.style ?? rowData?.style ?? 0,
              belowColumns[col]?.style ?? belowRow.style ?? 0,
              belowColumns[nextCol]?.style ?? belowRow.style ?? 0,
              this.rowTop(visual) === 0,
              outerLeft,
            ].join('|');
            const saved = this.finishedCells.get(templateKey);
            if (saved) {
              cell = saved.node.cloneNode(false) as GridCellElement;
              cell.textContent = metric.text;
              cell.dataset.ref = ref;
              cell.dataset.row = String(rowNumber);
              cell.dataset.col = String(col);
              cell.dataset.visualIndex = String(visual);
              Object.assign(cell, saved.metadata, {
                _xlModel: cellData,
                _xlRowStyle: rowData?.style ?? 0,
              });
              cells.set(col, cell);
              newCells.appendChild(cell);
              createdCells++;
              cloned = true;
            }
          }
        }
        if (!cell) {
          cell = this.makeGridCell(cellData, rowData, rowNumber, visual, layout);
          cells.set(col, cell);
          newCells.appendChild(cell);
          createdCells++;
        }
        this.updateGridCellGeometry(cell, layout);
        cell._xlNextCol = nextCol;
        if (!cloned) {
          this.appearance.updateSharedBorders(
            cell,
            cellData,
            rowData,
            nextCol ? byColumn[nextCol] : null,
            rowData,
            belowColumns[col],
            belowRow,
            {
              nextCol,
              belowRightCell: nextCol ? belowColumns[nextCol] : null,
              mergeFree,
              columns: visibleColumns,
              firstRow: firstVisual,
              lastRow: lastVisual,
              rows: rowDataByNumber,
              cellMap: models,
              outerLeft: layoutIndex === 0 && col > 1,
              outerTop: this.rowTop(visual) === 0,
            },
          );
        }
        if (layoutIndex === 0 && headChild instanceof HTMLElement &&
            (!pinned.cols || !frozen.paintLeftRowHeader(headChild, visual, rowNumber))) {
          if (col === 1) {
            this.appearance.paintRowHeaderBorder(headChild, cellData, rowData, belowColumns[1], belowRow);
          } else if (headChild.classList.contains('row-head-bordered')) {
            this.appearance.paintRowHeaderBorder(headChild);
          }
        }
        if (!mergeFree || cell._xlMerge || cell._xlCovered) {
          this.decorateMergedCell(cell, rowNumber, col, focus);
        }
        if (
          !templateKey &&
          (!restoring || !savedRow || replaced) &&
          (cellData || cell._xlOverflowSpan || cell._xlNumericSpan)
        ) {
          this.cellPresenter.overflow(cell, cellData, byColumn, columnLayouts, layoutIndex);
        }
        if (templateKey && !cloned && !cell.childElementCount && !cell.title) {
          const metadata: FinishedCellTemplate['metadata'] = {
            _xlColumnLeft: cell._xlColumnLeft,
            _xlColumnWidth: cell._xlColumnWidth,
            _xlPaintKey: cell._xlPaintKey,
            _xlRichAllowed: cell._xlRichAllowed,
            _xlRowPresentationNotice: cell._xlRowPresentationNotice,
            _xlborderRight: cell._xlborderRight,
            _xlborderBottom: cell._xlborderBottom,
            _xlOuterLeft: cell._xlOuterLeft,
            _xlOuterTop: cell._xlOuterTop,
            _xlNextCol: cell._xlNextCol,
          };
          if (this.finishedCells.size >= 1024) this.finishedCells.clear();
          this.finishedCells.set(templateKey, {
            node: cell.cloneNode(false) as GridCellElement,
            metadata,
          });
        }
        if (fresh && !extraMarks) {
          if (S.selected?.ref === ref) cell.classList.add('selected');
          if (focus.hasCol(col)) cell.classList.add('active-column');
        } else if (needsDecoration) {
          this.decorateGridCell(cell, rowNumber, col, ref, focus);
        }
      }
      if (!oldRow && newCells.childNodes.length) row.appendChild(newCells);
      for (const [col, existingCell] of existingCells) {
        if (!cells.has(col)) remove.push(existingCell);
      }
      if (sliced && liveRoom > 0 && (!oldRow || row === oldRow)) {
        const additions: [number, GridCellElement][] = [];
        for (const [col, node] of cells) {
          if (!oldRow?._xlCells?.has(col)) additions.push([col, node]);
        }
        if (additions.length && additions.length <= liveRoom) {
          liveRoom -= additions.length;
          if (!oldRow) {
            if (virtualScroll.scale !== 1) {
              row.style.top = `${virtualScroll.rowPosition(logicalTop, S.renderedScrollTop)}px`;
            }
            rowsLayer.append(row);
            previous.set(visual, row);
          }
          const ready = document.createDocumentFragment();
          for (const [col, node] of additions) {
            if (node.parentNode !== row) ready.appendChild(node);
            existingCells.set(col, node);
            if (node._xlModel && node.dataset.ref) S.cellMap.set(node.dataset.ref, node._xlModel);
          }
          if (ready.childNodes.length) row.appendChild(ready);
          S.rowNumberByVisual.set(visual, rowNumber);
        }
      }
      updates.push({ row, cells, newCells, remove, below: belowRow?.number });
      next.set(visual, row);
      const spent = sliced ? performance.now() - sliceStart : 0;
      if (
        sliced &&
        (spent >= sliceBudget ||
          (spent >= 3 && navScheduling?.isInputPending?.({ includeContinuous: true })))
      ) {
        cpuMs += performance.now() - sliceStart;
        yield;
        sliceStart = performance.now();
      }
    }

    commitHeaders();
    for (const { row, cells, newCells, remove, below } of updates) {
      if (newCells.childNodes.length) row.appendChild(newCells);
      for (const cell of remove) {
        if (cell.parentNode === row) cell.remove();
      }
      row._xlCells = cells;
      row._xlGridKey = headerKey;
      row._xlBelow = below;
      row._xlLayoutVersion = layoutVersion;
      row._xlDecorVersion = initialDecor;
    }
    if (retain) {
      for (const [visual, row] of next) {
        if (!rowNumbers.has(visual)) rowNumbers.set(visual, Number(row.dataset.row));
        for (const node of row._xlCells?.values() ?? []) {
          if (node._xlModel && node.dataset.ref && !models.has(node.dataset.ref)) {
            models.set(node.dataset.ref, node._xlModel);
          }
        }
      }
    }
    S.cellMap = models;
    S.rowNumberByVisual = rowNumbers;
    rowsLayer.appendChild(newNodes);
    for (const row of replacedRows) row.remove();
    for (const [visual, row] of previous) {
      if (!next.has(visual)) this.retireRow(row);
    }
    S.renderedRows = next;
    S.renderedScrollTop = physicalScroll;
    if (sliced) this.followGridScroll();
    if (!sliced) syncTableColumnHeaders();
    if (sliced && S.decorVersion !== initialDecor) {
      const current = selectionHighlight();
      for (const [visual, row] of next) {
        const number = Number(row.dataset.row);
        row.classList.toggle('active-row', current.hasRow(number, visual));
        row.classList.toggle(
          'whole-row-selected',
          S.wholeRowSelected && S.selected?.row === number,
        );
        for (const [col, node] of row._xlCells ?? []) {
          this.decorateMergedCell(node, number, col, current);
          this.decorateGridCell(node, number, col, node.dataset.ref ?? '', current);
        }
        row._xlDecorVersion = S.decorVersion;
      }
    }
    if (S.selected || S.range || S.multiRanges.length) {
      if (sliced) this.scheduleRangeFrames();
      else this.paintRangeFrames();
    }
    const hasVisibleRows = S.filtered ? data.rows.length > 0 : this.visibleSourceCount(1, S.maxRow) > 0;
    const gridEmpty = requireEl('gridEmpty');
    gridEmpty.hidden = hasVisibleRows;
    if (!hasVisibleRows) {
      gridEmpty.textContent = S.filtered
        ? localize('ui.viewportPipeline.thereNoVisibleFilterRecords')
        : localize('ui.viewportPipeline.thereNoVisibleRowsAll');
    }
    this.updateGridStatus(data);
    panels.editCopy?.grid.schedulePosition();
    panels.traceUI?.refreshPulse();
    updateSearchNavButtons();
    S.renderMetrics = {
      commits: S.renderMetrics.commits + 1,
      reusedRows,
      createdRows,
      createdCells,
      lastMs: Math.round((cpuMs + performance.now() - sliceStart) * 100) / 100,
    };
    motion.observe('render', performance.now() - started);
  }

  private renderColumnHeaders(
    columns: readonly number[],
    width: number,
    focus: SelectionHighlight,
  ): void {
    const {
      S,
      colName,
      selectedColumnSpan,
      decorateTableColumnHeader,
      selectColumnSpan,
      captureColumnSelection,
      paintColumnSelection,
    } = this.d;
    const fragment = document.createDocumentFragment();
    const corner = document.createElement('div');
    corner.className = 'column-head corner';
    corner.textContent = '';
    corner.style.width = `${this.rowHeadWidth}px`;
    corner.style.height = `${this.headerHeight()}px`;
    fragment.appendChild(corner);
    for (const col of columns) {
      const head = document.createElement('div');
      const span = selectedColumnSpan();
      const activeCol = focus.hasCol(col);
      head.className =
        'column-head' +
        (activeCol ? ' active' : '') +
        (span && col >= span.start && col <= span.end ? ' column-selected' : '');
      head.style.left = `${this.rowHeadWidth + this.colLeft(col)}px`;
      head.style.width = `${this.colWidth(col)}px`;
      head.style.height = `${this.headerHeight()}px`;
      head.dataset.col = String(col);
      if (!decorateTableColumnHeader(head, col)) {
        const label = document.createElement('span');
        label.textContent = colName(col);
        head.appendChild(label);
      }
      const grip = document.createElement('span');
      grip.className = 'column-resize-grip';
      this.sizing.bindResizeGrip(grip, 'col', col);
      head.append(grip);
      head.addEventListener('click', (event: MouseEvent) => {
        if (
          event.target instanceof Element &&
          event.target.closest('.column-resize-grip,.column-filter,.auto-filter-button')
        ) {
          return;
        }
        if (event.detail === 0) {
          selectColumnSpan(event.shiftKey ? (S.columnAnchor || col) : col, col);
          captureColumnSelection();
        }
      });
      fragment.appendChild(head);
    }
    const headers = requireEl('columnHeaders');
    headers.style.width = `${width}px`;
    headers.style.height = `${this.headerHeight()}px`;
    headers.replaceChildren(fragment);
    paintColumnSelection();
  }

  private updateGridStatus(data: SheetViewportResponse): void {
    const { S } = this.d;
    const sheet = S.sheet ?? '';
    if (S.previewOnly) {
      requireEl('gridStatus').textContent = localize('ui.viewportPipeline.firstScreenPreviewCompleteIndex7822e2f', { sheet });
      return;
    }
    const total = S.filtered ? data.totalRows : S.maxRow;
    const filterText = S.filtered ? ` ${localize('ui.viewportPipeline.rowsAfterFilteringMarked', { total: total.toLocaleString(interfaceLocale()) })}` : '';
    const tableText = isTableHeader(S.autoFilter)
      ? ` ${localize('ui.viewportPipeline.headerRow', { row: S.autoFilter.row, detail: S.autoFilter.detected ? localize('ui.viewportPipeline.automatic') : '' })}`
      : '';
    const label = localize('ui.viewportPipeline.usedArea', { sheet, dimension: S.dimension, filterText, tableText });
    const gridStatus = requireEl('gridStatus');
    if (gridStatus.textContent !== label) gridStatus.textContent = label;
    requireEl('filterStatus').hidden = !S.filtered;
    requireButton('clearFilterBtn').disabled = !S.filtered;
  }

  applySelectionClasses(): void {
    const { S, mergedCells, crosshairCell, getFrozenPanes, panels } = this.d;
    ++S.decorVersion;
    panels.selectionQuick?.changed();
    panels.transposeCenter?.changed();
    panels.cellExplanation?.changed();
    panels.profileCenter?.changed();
    const old = S.lastHighlight;
    const areas = MultiSelection.areas(S);
    const focus = crosshairCell();
    const hl = MultiSelection.highlight(S, focus, areas);
    const selectedCol = hl.singleCol;
    const anchorCol = S.selected?.col ?? 0;
    const selectedRef = S.selected?.ref ?? '';
    const multiCol = old.hadMultiCol || hl.hasMultiCol;
    for (const [visual, row] of S.renderedRows) {
      const rowNumber = Number(row.dataset.row);
      const active = hl.hasRow(rowNumber, visual);
      row.classList.toggle('active-row', active);
      row.classList.toggle('whole-row-selected', S.wholeRowSelected && rowNumber === S.selected?.row);
      const cells = row._xlCells;
      if (!cells) continue;
      if (multiCol) {
        for (const [col, cell] of cells) cell.classList.toggle('active-column', hl.hasCol(col));
      } else {
        if (old.col && old.col !== selectedCol) cells.get(old.col)?.classList.remove('active-column');
        if (selectedCol) cells.get(selectedCol)?.classList.add('active-column');
      }
      if (old.ref && old.ref !== selectedRef) {
        const previous = cells.get(old.anchorCol);
        if (previous?.dataset.ref === old.ref) previous.classList.remove('selected');
      }
      if (rowNumber === S.selected?.row) cells.get(anchorCol)?.classList.add('selected');
    }
    if (mergedCells.root && old.key !== hl.key) {
      for (const node of document.getElementsByClassName('merged-cell')) {
        if (node instanceof HTMLElement) {
          mergedCells.paintCrosshair(
            node,
            hl.mergeTarget,
            (c) => this.colLeft(c),
            (r) => this.rowTop(r),
          );
        }
      }
    }
    this.paintRangeFrames(areas);
    const frozenPanes = getFrozenPanes();
    const hasFreeze = S.freeze.rows > 0 || S.freeze.cols > 0 || frozenPanes.cells.size > 0;
    const pinnedCells = hasFreeze
      ? [...document.querySelectorAll<HTMLDivElement>('.grid-region .pinned-cell')]
      : [];
    for (const node of pinnedCells) {
      const row = Number(node.dataset.row);
      const col = Number(node.dataset.col);
      const visual = Number(node.dataset.visualIndex) || row;
      node.classList.toggle('selected', node.dataset.ref === selectedRef);
      node.classList.toggle('crosshair-row', hl.hasRow(row, visual));
      node.classList.toggle('active-column', hl.hasCol(col));
    }
    if (old.hadRange || areas.length) {
      for (const [visual, row] of S.renderedRows) {
        for (const [col, cell] of row._xlCells ?? []) {
          this.paintRangeCell(cell, Number(row.dataset.row), col, visual, areas);
        }
      }
      for (const node of pinnedCells) {
        this.paintRangeCell(
          node,
          Number(node.dataset.row),
          Number(node.dataset.col),
          Number(node.dataset.visualIndex),
          areas,
        );
      }
    }
    if (multiCol) {
      for (const node of document.querySelectorAll<HTMLElement>('.grid-region .column-head[data-col]')) {
        node.classList.toggle('active', hl.hasCol(Number(node.dataset.col)));
      }
    } else {
      if (old.col && old.col !== selectedCol) {
        for (const node of document.querySelectorAll(
          `.grid-region .column-head[data-col="${old.col}"]`,
        )) {
          node.classList.remove('active');
        }
      }
      if (selectedCol) {
        for (const node of document.querySelectorAll(
          `.grid-region .column-head[data-col="${selectedCol}"]`,
        )) {
          node.classList.add('active');
        }
      }
    }
    if (hasFreeze) {
      for (const node of document.querySelectorAll<HTMLElement>('.grid-region .pinned-row-head')) {
        const r = Number(node.dataset.row);
        node.classList.toggle('active', hl.hasRow(r, r));
      }
    }
    S.lastHighlight = {
      key: hl.key,
      col: selectedCol,
      hadMultiCol: hl.hasMultiCol,
      anchorCol,
      ref: selectedRef,
      hadRange: areas.length > 0,
    };
  }

  inSelectedRange(row: number, col: number, visualIndex = 0): boolean {
    const { S, getFrozenPanes } = this.d;
    const through = S.filtered ? getFrozenPanes().effective().rows : 0;
    return MultiSelection.areas(S).some((area) =>
      MultiSelection.contains(area, row, col, visualIndex, S.filtered, through),
    );
  }

  traceVisibleRect(
    node: Element,
    forEditor: true,
  ): (VisibleRectInfo & {
    readonly viewport: {
      readonly left: number;
      readonly top: number;
      readonly right: number;
      readonly bottom: number;
    };
  }) | null;
  traceVisibleRect(node: Element, forEditor?: false): VisibleRectInfo | null;
  traceVisibleRect(node: Element, forEditor = false): VisibleRectInfo | null {
    const { S, getFrozenPanes } = this.d;
    const grid = requireEl('grid');
    if (!S.indexReady || grid.inert) return null;
    const row = node.closest<GridRowElement>('.grid-row');
    if (row && row._xlSheetEpoch !== S.sheetEpoch) return null;
    const rect = node.getBoundingClientRect();
    if (!rect.width || !rect.height) return null;
    const g = grid.getBoundingClientRect();
    const p = getFrozenPanes().effective();
    const fw = this.colLeft(p.cols + 1);
    const fh = this.sourceRowTop(p.rows + 1);
    const hh = this.headerHeight();
    let left = g.left + this.rowHeadWidth + fw;
    let top = g.top + hh + fh;
    let right = g.left + grid.clientWidth;
    let bottom = g.top + grid.clientHeight;
    let pane: 'body' | 'corner' | 'top' | 'left' = 'body';
    if (node.closest('#freezeCorner')) {
      pane = 'corner';
      left = g.left + this.rowHeadWidth;
      top = g.top + hh;
      right = left + fw;
      bottom = top + fh;
    } else if (node.closest('#freezeTop')) {
      pane = 'top';
      top = g.top + hh;
      bottom = top + fh;
    } else if (node.closest('#freezeLeft')) {
      pane = 'left';
      left = g.left + this.rowHeadWidth;
      right = left + fw;
    }
    const visible: VisibleRectInfo = {
      left: Math.max(left, rect.left),
      top: Math.max(top, rect.top),
      right: Math.min(right, rect.right),
      bottom: Math.min(bottom, rect.bottom),
      pane,
    };
    if (visible.right <= visible.left || visible.bottom <= visible.top) return null;
    if (forEditor) {
      visible.viewport = {
        left,
        top,
        right: g.left + grid.clientWidth,
        bottom: g.top + grid.clientHeight,
      };
    }
    return visible;
  }

  renderGridFromCurrentWindow(): void {
    const { S, tiles } = this.d;
    let entry = S.viewportData;
    if (!entry) return;
    const current = this.getViewportRequest();
    if (!sameViewportRows(entry.bounds, current)) {
      const prior = entry.request;
      if (
        prior.book !== S.book ||
        prior.workbookKey !== S.workbookKey ||
        prior.sheet !== S.sheet ||
        !sameViewportRows({ ...entry.bounds, context: current.context }, current)
      ) {
        return;
      }
      this.scheduler.reset();
      ++S.viewportRequest;
      tiles.clear();
      const request: ViewportRequest = { ...prior, context: current.context };
      const bounds = { ...entry.bounds, context: current.context };
      entry = { ...entry, request, bounds };
      S.viewportData = entry;
      S.window = bounds;
      tiles.store(this.viewportKey(request), entry.data, request);
    }
    this.renderGrid(entry.data, entry.request);
  }
}
