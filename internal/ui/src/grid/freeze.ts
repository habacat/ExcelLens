/**
 * Frozen-pane renderer (top, left, corner, column headers, row headers).
 * The main grid owns the scrollbars; frozen panes render only pinned slices.
 */
import type { ApiClient } from '../contracts/common.ts';
import type { CellData, RowData, SheetViewportResponse } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import type { ColumnLayoutItem } from './cell-view.ts';
import type { GridCellElement, ViewportBounds, ViewportRequest, WorkbookState } from './state.ts';
import { viewportCoversVisible } from './viewport-cache.ts';

export interface BorderEdgeContext {
  readonly nextCol?: number;
  readonly mergeFree?: boolean;
  readonly columns?: readonly number[];
  readonly firstRow?: number;
  readonly lastRow?: number;
  readonly rows?: ReadonlyMap<number, RowData>;
  readonly cellMap?: ReadonlyMap<string, CellData>;
  readonly outerLeft?: boolean;
  readonly outerTop?: boolean;
}

export interface FrozenPanesOptions {
  readonly state: WorkbookState;
  readonly grid: HTMLElement;
  readonly rowHead: number;
  readonly api: ApiClient;
  readonly getRequest: () => ViewportRequest;
  readonly rowHeight: (row?: number) => number;
  readonly colWidth: (col?: number) => number;
  readonly rowTop: (row: number) => number;
  readonly colLeft: (col: number) => number;
  readonly headerHeight: () => number;
  readonly colName: (col: number) => string;
  readonly bindResizeGrip: (grip: HTMLElement, kind: 'row' | 'col', index: number) => void;
  readonly sourceRowTop?: (row: number) => number;
  readonly sourceRowHeight?: (row: number) => number;
  readonly getVirtualScrollTop: () => number;
  readonly updateSharedBorders: (
    el: GridCellElement,
    cell: CellData | undefined,
    row: RowData | undefined,
    rightCell: CellData | undefined,
    rightRow: RowData | undefined,
    belowCell: CellData | undefined,
    belowRow: RowData | undefined,
    context: BorderEdgeContext,
  ) => void;
  readonly isPrecedent: (row: number, col: number) => boolean;
  readonly isSearchMatch: (sheet: string, ref: string) => boolean;
  readonly applyCellStyle: (node: GridCellElement, cell: Partial<CellData>) => void;
  readonly onRender: () => void;
  readonly decorateFormulaRef?: (el: GridCellElement, row: number, col: number, sheet: string) => void;
  readonly beforePaint?: (rows: readonly RowData[]) => void;
  readonly paintCell: (el: GridCellElement, cell: CellData | undefined, row?: RowData) => void;
  readonly extendTextIntoBlanks?: (
    el: GridCellElement,
    cell: CellData,
    values: Readonly<Record<number, CellData | undefined>> | null,
    layouts: readonly ColumnLayoutItem[],
    position: number,
  ) => void;
  readonly projectCell?: (ref: string, cell: CellData | undefined) => CellData | undefined;
  readonly cachedPart?: (
    name: 'corner' | 'top' | 'left',
    params: URLSearchParams,
    request: ViewportRequest,
  ) => SheetViewportResponse | null | undefined;
  readonly estimateBytes?: (data: SheetViewportResponse) => number;
  readonly cacheBudget?: number;
  readonly decorateMergedCell?: (el: GridCellElement, row: number, col: number) => void;
  readonly decorateColumnHeader?: (el: HTMLElement, col: number) => boolean;
  readonly isBookmarked?: (sheet: string, ref: string) => boolean;
  readonly onError: (error: Error) => void;
}

interface ActivePartRead {
  readonly key: string;
  readonly controller: AbortController;
  promise: Promise<SheetViewportResponse>;
}

interface PendingComposite {
  readonly key: string;
  readonly abort: AbortController;
  promise: Promise<void>;
}

export class FrozenPanes {
  private readonly o: FrozenPanesOptions;
  public key = '';
  public readonly cache = new Map<string, SheetViewportResponse>();
  private readonly partReads = new Map<string, ActivePartRead>();
  public readonly cells = new Map<string, GridCellElement>();
  public readonly cacheSizes = new Map<string, number>();
  public cacheBytes = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private force = false;
  private abort: AbortController | null = null;
  private pending: PendingComposite | null = null;
  private serial = 0;
  private coverage: { readonly key: string; readonly bounds: ViewportBounds } | null = null;
  public base: { readonly left: number; readonly top: number } | null = null;

  constructor(options: FrozenPanesOptions) {
    this.o = options;
  }

  effective(): { readonly rows: number; readonly cols: number } {
    const { state: s, grid, rowHeight, colWidth, headerHeight, rowHead } = this.o;
    let rows = 0;
    let cols = 0;
    let rowUsed = 0;
    let colUsed = 0;
    const heightLimit = s.freeze.rows ? grid.clientHeight - headerHeight() - 105 : 0;
    const widthLimit = s.freeze.cols ? grid.clientWidth - rowHead - 230 : 0;
    for (let i = 1; i <= s.freeze.rows; i++) {
      const size = (this.o.sourceRowHeight ?? rowHeight)(i);
      if (rowUsed + size > heightLimit) break;
      rowUsed += size;
      rows++;
    }
    for (let i = 1; i <= Math.min(s.freeze.cols, 80); i++) {
      const size = colWidth(i);
      if (colUsed + size > widthLimit) break;
      colUsed += size;
      cols++;
    }
    return { rows, cols };
  }

  reset(options: { readonly preserveDOM?: boolean } = {}): void {
    ++this.serial;
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    this.force = false;
    this.abort?.abort();
    this.abort = null;
    this.pending = null;
    this.stopParts();
    this.coverage = null;
    this.key = '';
    this.cache.clear();
    this.cacheSizes.clear();
    this.cacheBytes = 0;
    if (options.preserveDOM) return;
    this.cells.clear();
    this.o.state.frozenCellMap.clear();
    for (const id of ['freezeTop', 'freezeLeft', 'freezeCorner', 'freezeHeader', 'freezeRowHeads']) {
      const el = document.getElementById(id);
      if (el) el.hidden = true;
    }
    for (const id of [
      'freezeTopCells',
      'freezeLeftCells',
      'freezeCorner',
      'freezeHeader',
      'freezeRowHeads',
    ]) {
      document.getElementById(id)?.replaceChildren();
    }
    this.base = null;
    const topCells = document.getElementById('freezeTopCells');
    if (topCells) topCells.style.transform = '';
    const leftCells = document.getElementById('freezeLeftCells');
    if (leftCells) leftCells.style.transform = '';
  }

  followScroll(): void {
    if (!this.base) return;
    const topCells = document.getElementById('freezeTopCells');
    if (topCells) {
      topCells.style.transform = `translateX(${-(this.o.grid.scrollLeft - this.base.left)}px)`;
    }
    const leftCells = document.getElementById('freezeLeftCells');
    if (leftCells) {
      leftCells.style.transform = `translateY(${-(this.o.getVirtualScrollTop() - this.base.top)}px)`;
    }
  }

  schedule(force = false): void {
    if (!this.o.state.indexReady) return;
    this.force ||= force;
    if (this.timer !== undefined) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      const refresh = this.force;
      this.force = false;
      this.refresh(refresh).catch((err: unknown) => {
        if (!(err instanceof Error && err.name === 'AbortError')) {
          this.o.onError(err instanceof Error ? err : new Error(errorMessage(err)));
        }
      });
    }, 0);
  }

  stopParts(): void {
    for (const part of this.partReads.values()) part.controller.abort();
    this.partReads.clear();
  }

  fetchPart(
    name: 'corner' | 'top' | 'left',
    params: URLSearchParams,
    request: ViewportRequest,
  ): Promise<SheetViewportResponse> | SheetViewportResponse {
    const key = `${name}|${params.toString()}`;
    const live = this.partReads.get(name);
    if (live?.key === key) return live.promise;
    live?.controller.abort();
    this.partReads.delete(name);
    const cachedHit = this.cache.get(key);
    if (cachedHit) {
      this.cache.delete(key);
      this.cache.set(key, cachedHit);
      return cachedHit;
    }
    const projected = this.o.cachedPart?.(name, params, request);
    if (projected) return projected;
    const controller = new AbortController();
    const promise = this.readPart(params, request, controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) this.cachePart(key, data);
        return data;
      })
      .finally(() => {
        if (this.partReads.get(name)?.controller === controller) this.partReads.delete(name);
      });
    this.partReads.set(name, { key, controller, promise });
    return promise;
  }

  private async readPart(
    params: URLSearchParams,
    request: ViewportRequest,
    signal: AbortSignal,
  ): Promise<SheetViewportResponse> {
    const rows = Number(params.get('limit'));
    const cols = Number(params.get('cols'));
    if (rows <= 150 && cols <= 80) {
      return this.o.api<SheetViewportResponse>(`/api/sheet?${params.toString()}`, {
        signal,
        activity: false,
      });
    }
    const selected = params.get('columns')?.split(',').map(Number);
    const firstCol = Number(params.get('col'));
    const merged: RowData[] = [];
    const basedOnOffset = params.has('offset');
    let meta: SheetViewportResponse | undefined;
    for (let ro = 0; ro < rows; ro += 150) {
      const byRow = new Map<number, RowData>();
      for (let co = 0; co < cols; co += 80) {
        const page = new URLSearchParams(params);
        const count = Math.min(80, cols - co);
        page.set('col', String(selected?.[co] ?? firstCol + co));
        page.set('cols', String(count));
        page.set('limit', String(Math.min(150, rows - ro)));
        if (selected) page.set('columns', selected.slice(co, co + count).join(','));
        if (basedOnOffset) {
          page.set('offset', String(Number(params.get('offset')) + ro));
        } else if (ro) {
          const baseRow = Number(params.get('row'));
          page.set('row', String(request.rowOrder?.advance(baseRow, ro) ?? baseRow + ro));
        }
        const data = await this.o.api<SheetViewportResponse>(`/api/sheet?${page.toString()}`, {
          signal,
          activity: false,
        });
        meta ??= data;
        for (const row of data.rows) {
          const previous = byRow.get(row.number);
          if (previous) previous.cells.push(...row.cells);
          else byRow.set(row.number, { ...row, cells: [...row.cells] });
        }
      }
      merged.push(...byRow.values());
    }
    if (!meta) {
      throw new Error('冻结窗格未返回有效数据');
    }
    return { ...meta, startCol: firstCol, colCount: cols, columns: selected ?? null, rows: merged };
  }

  cachePart(key: string, data: SheetViewportResponse): void {
    const oldBytes = this.cacheSizes.get(key);
    if (oldBytes !== undefined) this.cacheBytes -= oldBytes;
    this.cache.delete(key);
    this.cacheSizes.delete(key);
    const bytes = this.o.estimateBytes?.(data) ?? 0;
    const budget = this.o.cacheBudget ?? Infinity;
    if (bytes > budget) return;
    this.cache.set(key, data);
    this.cacheSizes.set(key, bytes);
    this.cacheBytes += bytes;
    while (this.cache.size > 1 && (this.cache.size > 48 || this.cacheBytes > budget)) {
      const oldest = this.cache.keys().next().value;
      if (oldest === undefined) break;
      this.cacheBytes -= this.cacheSizes.get(oldest) ?? 0;
      this.cache.delete(oldest);
      this.cacheSizes.delete(oldest);
    }
  }

  async refresh(force = false): Promise<void> {
    const {
      state: s,
      getRequest,
      grid,
      rowHeight,
      colWidth,
      rowTop,
      colLeft,
      headerHeight,
      rowHead,
    } = this.o;
    const sourceTop = this.o.sourceRowTop ?? rowTop;
    const sourceHeight = this.o.sourceRowHeight ?? rowHeight;
    const visible = (row: number): boolean => !s.rowVisibility || s.rowVisibility.visible(row);
    const next = (row: number): number => (s.rowVisibility ? s.rowVisibility.next(row) : row);
    if (!s.indexReady || !s.sheet) return;
    const activeSheet = s.sheet;
    const { rows: nr, cols: nc } = this.effective();
    const ids = ['freezeTop', 'freezeLeft', 'freezeCorner', 'freezeHeader', 'freezeRowHeads'];
    if (!nr && !nc) {
      if (!this.base && !this.pending && !this.cells.size && !this.key) return;
      this.coverage = null;
      this.stopParts();
      this.cells.clear();
      ++this.serial;
      this.abort?.abort();
      this.abort = null;
      this.pending = null;
      this.key = '';
      s.frozenCellMap.clear();
      for (const id of ids) {
        const e = document.getElementById(id);
        if (e) e.hidden = true;
      }
      for (const id of [
        'freezeTopCells',
        'freezeLeftCells',
        'freezeCorner',
        'freezeHeader',
        'freezeRowHeads',
      ]) {
        document.getElementById(id)?.replaceChildren();
      }
      this.base = null;
      return;
    }

    const req = getRequest();
    const topCol = Math.max(1, req.startCol);
    const viewId = s.rowView?.id ?? '';
    const partsKey = [
      activeSheet,
      s.sheetEpoch,
      req.context,
      s.filtered,
      s.filterId,
      viewId,
      rowHead,
      nr,
      nc,
      req.displayStart,
      req.displayEnd,
      topCol,
      req.cols,
      req.selectedCols?.join(',') ?? '',
      req.filtered ? req.offset : req.row,
    ].join('|');
    const coverageKey = [
      s.sheetEpoch,
      req.context,
      s.filterId,
      viewId,
      rowHead,
      nr,
      nc,
    ].join('|');

    if (
      !force &&
      (partsKey === this.key ||
        (this.coverage?.key === coverageKey && viewportCoversVisible(this.coverage.bounds, req)))
    ) {
      this.followScroll();
      return;
    }
    if (this.pending?.key === partsKey && !this.pending.abort.signal.aborted) {
      return this.pending.promise;
    }

    const ticket = ++this.serial;
    this.abort?.abort();
    const abort = new AbortController();
    this.abort = abort;

    const build = (init: Record<string, string>): URLSearchParams =>
      new URLSearchParams({
        sheet: activeSheet,
        rowWindow: '1',
        ...(s.showStyles ? {} : { conditional: '0' }),
        ...(viewId
          ? { viewId, filterId: s.filtered ? s.filterId : '', pinnedThroughRow: String(nr) }
          : {}),
        ...init,
      });

    const cornerJob: Promise<SheetViewportResponse | null> =
      nr && nc
        ? Promise.resolve(
            this.fetchPart(
              'corner',
              build({ row: '1', limit: String(nr), col: '1', cols: String(nc) }),
              req,
            ),
          )
        : Promise.resolve(null);

    const topJob: Promise<SheetViewportResponse | null> = nr
      ? (() => {
          const params = build({
            row: '1',
            limit: String(nr),
            col: String(topCol),
            cols: String(req.cols),
          });
          if (req.selectedCols) params.set('columns', req.selectedCols.join(','));
          return Promise.resolve(this.fetchPart('top', params, req));
        })()
      : Promise.resolve(null);

    const leftJob: Promise<SheetViewportResponse | null> = nc
      ? (() => {
          const params = build({ limit: String(req.limit), col: '1', cols: String(nc) });
          params.delete('pinnedThroughRow');
          if (req.filtered) {
            params.set('offset', String(req.offset));
            params.set('filterId', s.filterId);
            if (nr) params.set('excludeThroughRow', String(nr));
          } else {
            params.set('row', String(req.row));
          }
          return Promise.resolve(this.fetchPart('left', params, req));
        })()
      : Promise.resolve(null);

    const pending: PendingComposite = {
      key: partsKey,
      abort,
      promise: Promise.resolve(),
    };
    this.pending = pending;
    pending.promise = (async () => {
      try {
        const [corner, top, left] = await Promise.all([cornerJob, topJob, leftJob]);
        if (
          ticket !== this.serial ||
          abort.signal.aborted ||
          !s.indexReady ||
          viewId !== (s.rowView?.id ?? '')
        ) {
          return;
        }
        if (rowHead !== this.o.rowHead) {
          this.schedule(true);
          return;
        }
        const now = getRequest();
        const bounds: ViewportBounds = {
          context: req.context,
          filtered: req.filtered,
          filterId: req.filterId,
          viewId: req.viewId,
          excludeThroughRow: req.excludeThroughRow,
          displayStart: req.displayStart,
          displayEnd: req.displayEnd,
          startCol: req.startCol,
          endCol: req.selectedCols?.at(-1) ?? req.startCol + req.cols - 1,
          columns: req.selectedCols,
        };
        if (!viewportCoversVisible(bounds, now)) {
          this.schedule();
          return;
        }
        this.coverage = { key: coverageKey, bounds };
        this.key = partsKey;
        this.o.beforePaint?.([
          ...(corner?.rows ?? []),
          ...(top?.rows ?? []),
          ...(left?.rows ?? []),
        ]);
        this.base = { left: grid.scrollLeft, top: this.o.getVirtualScrollTop() };
        const hh = headerHeight();
        const topHeight = sourceTop(nr + 1);
        const leftWidth = colLeft(nc + 1);
        const freezeTop = document.getElementById('freezeTop');
        const freezeTopBody = document.getElementById('freezeTopBody');
        const freezeLeft = document.getElementById('freezeLeft');
        const freezeCorner = document.getElementById('freezeCorner');
        const freezeHeader = document.getElementById('freezeHeader');
        const freezeRowHeads = document.getElementById('freezeRowHeads');
        const freezeTopCells = document.getElementById('freezeTopCells');
        const freezeLeftCells = document.getElementById('freezeLeftCells');
        if (
          !freezeTop ||
          !freezeTopBody ||
          !freezeLeft ||
          !freezeCorner ||
          !freezeHeader ||
          !freezeRowHeads ||
          !freezeTopCells ||
          !freezeLeftCells
        ) {
          return;
        }

        s.frozenCellMap.clear();
        this.cells.clear();
        const borderRows = new Map<number, RowData>();
        for (const part of [corner, top, left]) {
          for (const row of part?.rows ?? []) {
            borderRows.set(row.number, row);
            for (const cell of row.cells) {
              s.frozenCellMap.set(`${this.o.colName(cell.col)}${row.number}`, cell);
            }
          }
        }

        const topCols =
          req.selectedCols ?? Array.from({ length: req.cols }, (_, i) => topCol + i);
        const topLayouts: ColumnLayoutItem[] = topCols
          .filter((c) => c > nc && !s.hiddenColumns.has(c))
          .map((c) => ({ col: c, width: colWidth(c) }));
        const pinnedLayouts: ColumnLayoutItem[] = Array.from({ length: nc }, (_, i) => i + 1)
          .filter((c) => !s.hiddenColumns.has(c))
          .map((c) => ({ col: c, width: colWidth(c) }));
        const topPositions = new Map(topLayouts.map((c, i) => [c.col, i]));
        const pinnedPositions = new Map(pinnedLayouts.map((c, i) => [c.col, i]));
        const following = (layouts: readonly ColumnLayoutItem[]): Map<number, number> =>
          new Map(layouts.map((part, i) => [part.col, layouts[i + 1]?.col ?? 0]));
        const topNext = following(topLayouts);
        const pinnedNext = following(pinnedLayouts);
        const borderColumns = [...pinnedLayouts, ...topLayouts].map((layout) => layout.col);
        const rowColumnsCache = new WeakMap<RowData, Record<number, CellData | undefined>>();
        const cellByColumn = (
          data: RowData | undefined,
        ): Record<number, CellData | undefined> | null => {
          if (!data) return null;
          let columns = rowColumnsCache.get(data);
          if (!columns) {
            columns = Object.create(null) as Record<number, CellData | undefined>;
            for (const c of data.cells) columns[c.col] = c;
            rowColumnsCache.set(data, columns);
          }
          return columns;
        };

        const make = (
          data: RowData | undefined,
          col: number,
          row: number,
          visual: number,
          x: number,
          y: number,
          type: 'top' | 'corner' | 'left',
          nextData: RowData | undefined,
          nextCol: number,
          outerLeft: boolean,
        ): GridCellElement => {
          const el = document.createElement('div') as GridCellElement;
          const ref = `${this.o.colName(col)}${row}`;
          const base = cellByColumn(data)?.[col];
          const cell = this.o.projectCell ? this.o.projectCell(ref, base) : base;
          const selectedColumns = s.columnSelection;
          const highlighted = Boolean(
            selectedColumns &&
              col >= Math.min(selectedColumns.start, selectedColumns.end) &&
              col <= Math.max(selectedColumns.start, selectedColumns.end),
          );
          el.className = `grid-cell pinned-cell${highlighted ? ' column-range-cell' : ''}`;
          el.style.left = `${x}px`;
          el.style.top = `${y}px`;
          el.style.width = `${colWidth(col)}px`;
          el.style.height = `${type === 'left' ? rowHeight(visual) : sourceHeight(row)}px`;
          el.dataset.ref = ref;
          el.dataset.row = String(row);
          this.cells.set(ref, el);
          el.dataset.col = String(col);
          el.dataset.visualIndex = String(visual);
          this.o.paintCell(el, cell, data);
          if (base) s.frozenCellMap.set(ref, base);
          const rightCell = cellByColumn(data)?.[nextCol];
          const belowCell = cellByColumn(nextData)?.[col];
          this.o.updateSharedBorders(el, cell, data, rightCell, data, belowCell, nextData, {
            nextCol,
            columns: borderColumns,
            rows: borderRows,
            lastRow: type === 'left' ? req.displayEnd : nr,
            outerLeft,
            outerTop: sourceTop(row) === 0,
          });
          this.o.decorateMergedCell?.(el, row, col);
          if (cell && this.o.extendTextIntoBlanks) {
            const pinned = type === 'corner' || type === 'left';
            const layouts = pinned ? pinnedLayouts : topLayouts;
            const position = (pinned ? pinnedPositions : topPositions).get(col);
            if (position !== undefined) {
              this.o.extendTextIntoBlanks(el, cell, cellByColumn(data), layouts, position);
            }
          }
          if (this.o.isPrecedent(row, col)) el.classList.add('precedent');
          this.o.decorateFormulaRef?.(el, row, col, activeSheet);
          if (this.o.isSearchMatch(activeSheet, ref)) el.classList.add('search-match');
          if (this.o.isBookmarked?.(activeSheet, ref)) el.classList.add('audit-marked');
          return el;
        };

        const head = document.createDocumentFragment();
        if (nc) {
          for (let col = 1; col <= nc; col++) {
            if (s.hiddenColumns.has(col)) continue;
            const el = document.createElement('div');
            const selectedColumns = s.columnSelection;
            const highlighted = Boolean(
              selectedColumns &&
                col >= Math.min(selectedColumns.start, selectedColumns.end) &&
                col <= Math.max(selectedColumns.start, selectedColumns.end),
            );
            el.className = `column-head pinned-head${highlighted ? ' column-selected' : ''}`;
            el.dataset.col = String(col);
            el.style.left = `${colLeft(col)}px`;
            el.style.width = `${colWidth(col)}px`;
            const tableHead = this.o.decorateColumnHeader?.(el, col) ?? false;
            if (!tableHead) el.textContent = this.o.colName(col);
            const resize = document.createElement('span');
            resize.className = 'column-resize-grip';
            this.o.bindResizeGrip(resize, 'col', col);
            el.append(resize);
            const filter = document.createElement('button');
            filter.type = 'button';
            filter.className = 'column-filter';
            filter.textContent = '▾';
            filter.dataset.filterCol = String(col);
            filter.title = `筛选 ${this.o.colName(col)} 列`;
            if (!tableHead) el.append(filter);
            head.append(el);
          }
        }
        freezeHeader.hidden = !nc;
        freezeHeader.style.left = `${rowHead}px`;
        freezeHeader.style.width = `${leftWidth}px`;
        freezeHeader.style.height = `${hh}px`;
        freezeHeader.replaceChildren(head);

        const topHeaders = document.createDocumentFragment();
        const topCellsFrag = document.createDocumentFragment();
        const topMap = new Map((top?.rows ?? []).map((r) => [r.number, r]));
        for (let row = 1; row <= nr; row++) {
          if (!visible(row)) continue;
          const label = document.createElement('div');
          label.className = 'pinned-row-head';
          label.textContent = String(row);
          label.dataset.row = String(row);
          label.style.top = `${sourceTop(row)}px`;
          label.style.height = `${sourceHeight(row)}px`;
          const resize = document.createElement('span');
          resize.className = 'row-resize-grip';
          this.o.bindResizeGrip(resize, 'row', row);
          label.append(resize);
          topHeaders.append(label);
          for (const col of topCols) {
            if (col <= nc || s.hiddenColumns.has(col)) continue;
            const x = colLeft(col) - leftWidth - grid.scrollLeft;
            topCellsFrag.append(
              make(
                topMap.get(row),
                col,
                row,
                row,
                x,
                sourceTop(row),
                'top',
                topMap.get(next(row + 1)),
                topNext.get(col) ?? 0,
                topLayouts[0]?.col === col && col > 1,
              ),
            );
          }
        }
        freezeTop.hidden = !nr;
        freezeRowHeads.hidden = !nr;
        freezeTop.style.top = `${hh}px`;
        freezeTop.style.height = `${topHeight}px`;
        freezeTopBody.style.left = `${rowHead + leftWidth}px`;
        freezeRowHeads.style.width = `${rowHead}px`;
        freezeRowHeads.replaceChildren(topHeaders);
        freezeTopCells.replaceChildren(topCellsFrag);

        const cornerNodes = document.createDocumentFragment();
        const cornerMap = new Map((corner?.rows ?? []).map((r) => [r.number, r]));
        for (let r = 1; r <= nr; r++) {
          if (!visible(r)) continue;
          for (let c = 1; c <= nc; c++) {
            if (s.hiddenColumns.has(c)) continue;
            cornerNodes.append(
              make(
                cornerMap.get(r),
                c,
                r,
                r,
                colLeft(c),
                sourceTop(r),
                'corner',
                cornerMap.get(next(r + 1)),
                pinnedNext.get(c) ?? 0,
                pinnedLayouts[0]?.col === c && c > 1,
              ),
            );
          }
        }
        freezeCorner.hidden = !(nr && nc);
        freezeCorner.style.top = `${hh}px`;
        freezeCorner.style.left = `${rowHead}px`;
        freezeCorner.style.width = `${leftWidth}px`;
        freezeCorner.style.height = `${topHeight}px`;
        freezeCorner.replaceChildren(cornerNodes);

        const leftNodes = document.createDocumentFragment();
        const leftRows = left?.rows ?? [];
        const firstVisual = req.displayStart;
        const lastVisual = s.filtered ? firstVisual + leftRows.length - 1 : req.displayEnd;
        const leftMap = new Map(
          leftRows.map((rd, i) => [s.filtered ? firstVisual + i : rd.number, rd]),
        );
        const origin = this.o.getVirtualScrollTop() + (s.filtered ? 0 : topHeight);
        for (
          let visual = firstVisual;
          visual <= lastVisual;
          visual = s.filtered ? visual + 1 : next(visual + 1) || Infinity
        ) {
          const rd = leftMap.get(visual);
          const row = rd?.number ?? visual;
          if (!s.filtered && !visible(row)) continue;
          const y = rowTop(visual) - origin;
          for (let c = 1; c <= nc; c++) {
            if (s.hiddenColumns.has(c)) continue;
            leftNodes.append(
              make(
                rd,
                c,
                row,
                visual,
                colLeft(c),
                y,
                'left',
                leftMap.get(s.filtered ? visual + 1 : next(visual + 1)),
                pinnedNext.get(c) ?? 0,
                pinnedLayouts[0]?.col === c && c > 1,
              ),
            );
          }
        }
        freezeLeft.hidden = !nc;
        freezeLeft.style.top = `${hh + topHeight}px`;
        freezeLeft.style.left = `${rowHead}px`;
        freezeLeft.style.width = `${leftWidth}px`;
        freezeLeftCells.replaceChildren(leftNodes);
        this.followScroll();
        this.o.onRender();
      } catch (error: unknown) {
        if (abort.signal.aborted || ticket !== this.serial) return;
        abort.abort();
        this.stopParts();
        throw error;
      } finally {
        if (this.pending === pending) this.pending = null;
        if (this.abort === abort) this.abort = null;
      }
    })();
    return pending.promise;
  }
}
