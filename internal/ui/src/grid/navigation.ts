/**
 * Worksheet cell/range navigation, Name Box range parsing, keyboard movement,
 * data-edge jump, browsing history, hyperlink traversal, and selection updates.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import type { CellPoint, CellRef, MergeRange, SelectionArea } from '../contracts/common.ts';
import type { CellData, CellHyperlink } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput } from '../core/dom.ts';
import type { FileFormats } from '../core/formats.ts';
import type { ApiClient } from '../core/transport.ts';
import type { FrozenPanes } from './freeze.ts';
import type { MergedCells } from './merges.ts';
import { MultiSelection, type SelectionHighlight } from './multi-selection.ts';
import type { GridCellElement, ViewportRequest, WorkbookState } from './state.ts';
import {
  decodeCellResponse,
  decodeFilterLocateResponse,
  decodeNavigateResponse,
} from '../core/http-decoders.generated.ts';

export interface ParsedNameAddress {
  readonly sheet: string;
  readonly start: CellRef;
  readonly end: CellRef;
}

export interface AdjacentGridPoint {
  readonly kind: 'cell';
  readonly row: number;
  readonly col: number;
  readonly visual: number;
}

export interface CatalogueAreaTarget {
  readonly sheet?: string;
  readonly start?: string;
  readonly end?: string;
}

export interface NavigationPanels {
  readonly editCopy?: {
    readonly grid: {
      readonly active: boolean;
      project(sheet: string, ref: string, source: CellData): CellData;
      selection(): void;
    };
  } | null;
  readonly traceUI?: { selectionChanged(): void } | null;
  readonly selectionStats?: { clear(): void; schedule(): void } | null;
  readonly formulaInspector?: { show(cell: CellData, sheet: string): void; clear(): void } | null;
  readonly recordCardCenter?: { controls(): void } | null;
  readonly catalogueCenter?: { open(query?: string): Promise<void> | void } | null;
  readonly rowViews?: {
    readonly busy: boolean;
    reveal(row: number, guard: () => boolean): Promise<boolean>;
  } | null;
  readonly calculator?: { captureRange(start: string, end?: string): boolean } | null;
}

export interface GridNavigationDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly fileFormats: FileFormats;
  readonly mergedCells: MergedCells;
  readonly toast: (message: string, tone?: 'info' | 'success' | 'warning' | 'error') => void;
  readonly setStatus: (text: string, hidden?: boolean) => void;
  readonly colName: (col: number) => string;
  readonly parseColumn: (text: string) => number;
  readonly parseRef: (text: string | null | undefined) => CellRef | null;
  readonly sourceRowLimit: () => number;
  readonly sourceColLimit: () => number;
  readonly rowVisible: (row: number) => boolean;
  readonly nextVisibleRow: (row: number) => number;
  readonly previousVisibleRow: (row: number) => number;
  readonly advanceVisibleRow: (row: number, delta: number) => number;
  readonly nextVisibleColumn: (from: number) => number;
  readonly previousVisibleColumn: (from: number) => number;
  readonly headerHeight: () => number;
  readonly rowHead: () => number;
  readonly sourceRowTop: (row: number) => number;
  readonly rowTop: (row: number) => number;
  readonly rowHeight: (row?: number) => number;
  readonly colLeft: (col: number) => number;
  readonly colWidth: (col?: number) => number;
  readonly getVirtualScrollTop: () => number;
  readonly scrollTopToPhysical: (logical: number) => number;
  readonly activeMerge: (row: number, col: number) => MergeRange | null;
  readonly ensureExtent: (row: number, col: number) => void;
  readonly getViewportRequest: () => ViewportRequest;
  readonly windowContains: (request: ViewportRequest) => boolean;
  readonly loadViewport: (
    force?: boolean,
    expectedEpoch?: number,
    strict?: boolean,
  ) => Promise<void>;
  readonly applySelectionClasses: () => void;
  readonly paintColumnSelection: () => void;
  readonly showWorkspace: () => void;
  readonly selectSheet: (
    name: string,
    targetRef?: string,
    preserveTrace?: boolean,
    navigationGuard?: (() => boolean) | null,
  ) => Promise<boolean | undefined>;
  readonly getFrozenPanes: () => FrozenPanes;
  readonly panels: NavigationPanels;
}

export interface HyperlinkOrigin {
  readonly sheet: string;
  readonly ref: string;
}

export function hyperlinkVisitKey(sheet: string, ref: string): string {
  return `${sheet}\0${ref}`;
}

export function linkDescription(link: CellHyperlink | null | undefined): string {
  if (!link) return '';
  return link.kind === 'internal' ? `${link.sheet ?? ''}!${link.ref ?? ''}` : (link.url ?? '');
}

export function revealCellOffset(
  offset: number,
  extent: number,
  inset: number,
  start: number,
  size: number,
  leadingMargin: number,
): number {
  const available = extent - inset;
  if (available <= 0) return offset;
  if (size >= available) return Math.max(0, start - inset);
  const slack = available - size;
  if (start < offset + inset) return Math.max(0, start - inset - Math.min(slack, leadingMargin));
  if (start + size > offset + extent) return start + size - extent + Math.min(slack, size);
  return offset;
}

export function parseNameAddress(
  value: string,
  deps: {
    readonly parseRef: (text: string) => CellRef | null;
    readonly parseColumn: (text: string) => number;
    readonly colName: (col: number) => string;
    readonly rowLimit: number;
    readonly usedMaxRow: number;
    readonly usedMaxCol: number;
  },
): ParsedNameAddress | null {
  let text = value.trim();
  let requestedSheet = '';
  const qualified = /^(?:'((?:[^']|'')+)'|([^'!]+))!(.+)$/.exec(text);
  if (qualified) {
    requestedSheet = (qualified[1] ?? qualified[2] ?? '').replaceAll("''", "'");
    text = (qualified[3] ?? '').trim();
  }
  text = text.replaceAll('$', '').toUpperCase();
  let first: CellRef | null = null;
  let last: CellRef | null = null;
  const parts = text.split(':');
  if (parts.length === 1) {
    first = deps.parseRef(parts[0] ?? '');
    last = first;
  } else if (parts.length === 2) {
    const p0 = parts[0] ?? '';
    const p1 = parts[1] ?? '';
    first = deps.parseRef(p0);
    last = deps.parseRef(p1);
    if (!first || !last) {
      if (parts.every((p) => /^[A-Z]{1,3}$/.test(p))) {
        const c1 = deps.parseColumn(p0);
        const c2 = deps.parseColumn(p1);
        if (c1 && c2) {
          first = deps.parseRef(`${deps.colName(c1)}1`);
          last = deps.parseRef(`${deps.colName(c2)}${deps.usedMaxRow}`);
        }
      } else if (parts.every((p) => /^[1-9][0-9]*$/.test(p))) {
        const r1 = Number(p0);
        const r2 = Number(p1);
        if (r1 <= deps.rowLimit && r2 <= deps.rowLimit) {
          first = deps.parseRef(`A${r1}`);
          last = deps.parseRef(`${deps.colName(deps.usedMaxCol)}${r2}`);
        }
      }
    }
  }
  return first && last ? { sheet: requestedSheet, start: first, end: last } : null;
}

export class GridNavigation {
  private readonly d: GridNavigationDeps;

  constructor(deps: GridNavigationDeps) {
    this.d = deps;
  }

  crosshairCell(): { readonly row: number; readonly col: number } | null {
    const { S, rowVisible, activeMerge } = this.d;
    const cell = S.selected;
    const point = cell?.crosshair;
    if (!cell) return null;
    if (!point || !rowVisible(point.row) || S.hiddenColumns.has(point.col)) return cell;
    const merge = activeMerge(point.row, point.col);
    return merge?.row === cell.row && merge.col === cell.col ? point : cell;
  }

  selectionHighlight(areas: readonly SelectionArea[] = MultiSelection.areas(this.d.S)): SelectionHighlight {
    return MultiSelection.highlight(this.d.S, this.crosshairCell(), areas);
  }

  isPrecedent(row: number, col: number): boolean {
    const { S, activeMerge } = this.d;
    const bounds = activeMerge(row, col) ?? { row, col, endRow: row, endCol: col };
    const currentLower = (S.sheet ?? '').toLocaleLowerCase();
    for (const ref of S.traceRefs) {
      if (ref.sheet.toLocaleLowerCase() !== currentLower) continue;
      if (
        bounds.row <= ref.end.row &&
        bounds.endRow >= ref.start.row &&
        bounds.col <= ref.end.col &&
        bounds.endCol >= ref.start.col
      ) {
        return true;
      }
    }
    return false;
  }

  parseNameAddress(value: string): ParsedNameAddress | null {
    const { S, parseRef, parseColumn, colName, sourceRowLimit } = this.d;
    return parseNameAddress(value, {
      parseRef,
      parseColumn,
      colName,
      rowLimit: sourceRowLimit(),
      usedMaxRow: S.usedMaxRow,
      usedMaxCol: S.usedMaxCol,
    });
  }

  async navigateNameBox(value: string): Promise<void> {
    const { S, fileFormats, toast, colName, activeMerge, panels } = this.d;
    const parsed = this.parseNameAddress(value);
    if (!parsed) {
      if (panels.catalogueCenter && fileFormats.source(S.source?.format).catalogueMetadata) {
        await panels.catalogueCenter.open(value);
        return;
      }
      toast(localize('ui.navigation.invalidAddressSupportsA1A1'), 'warning');
      return;
    }
    let sheet = S.sheet ?? '';
    if (parsed.sheet) {
      const target = S.sheets.find(
        (item) => item.name.toLocaleLowerCase() === parsed.sheet.toLocaleLowerCase(),
      );
      if (!target) {
        toast(localize('ui.navigation.worksheetNotFound', { sheet: parsed.sheet }), 'error');
        return;
      }
      sheet = target.name;
    }
    if (!sheet) return;
    const multiple = parsed.start.ref !== parsed.end.ref;
    if (S.filtered && sheet === S.sheet && multiple) {
      toast(localize('ui.navigation.filterViewDragSelectionRows'), 'warning');
      return;
    }
    if (!(await this.navigateToCell(sheet, parsed.start.ref))) return;
    const first = activeMerge(parsed.start.row, parsed.start.col);
    const expectedRef = first ? `${colName(first.col)}${first.row}` : parsed.start.ref;
    if (sheet !== S.sheet || S.selected?.ref !== expectedRef) return;
    if (multiple) {
      if (S.filtered) {
        toast(localize('ui.navigation.filteringViewDoesNotSupport'), 'warning');
        return;
      }
      this.extendRange(
        { row: parsed.start.row, col: parsed.start.col, visual: parsed.start.row },
        parsed.end.row,
        parsed.end.col,
      );
      if (S.range) {
        const points = MultiSelection.points(S.range, colName);
        requireInput('nameBox').value = `${points.start}:${points.end}`;
      }
    }
  }

  markHyperlinkVisited(origin: HyperlinkOrigin | null | undefined): void {
    const { S } = this.d;
    const sheet = origin?.sheet || S.sheet || '';
    const ref = origin?.ref || '';
    if (!sheet || !ref) return;
    S.visitedHyperlinks.add(hyperlinkVisitKey(sheet, ref));
    const apply = (node: GridCellElement | null | undefined): void => {
      if (!node?.classList.contains('hyperlink-cell')) return;
      node.classList.add('hyperlink-visited');
      node._xlHyperlinkVisited = true;
    };
    if (sheet === S.sheet) {
      for (const row of S.renderedRows.values()) {
        for (const cell of row._xlCells?.values() ?? []) {
          if (cell.dataset.ref === ref) apply(cell);
        }
      }
      for (const node of document.querySelectorAll('.pinned-cell.hyperlink-cell')) {
        if (node instanceof HTMLElement && node.dataset.ref === ref) {
          apply(node as GridCellElement);
        }
      }
    }
    const cached = S.sheetViews.get(sheet);
    if (cached) {
      for (const row of cached.rows.values()) {
        for (const cell of row._xlCells?.values() ?? []) {
          if (cell.dataset.ref === ref) apply(cell);
        }
      }
    }
  }

  async followCellHyperlink(
    link: CellHyperlink | null | undefined,
    origin?: HyperlinkOrigin | null,
  ): Promise<void> {
    const { S, parseRef, toast } = this.d;
    if (!link) return;
    const source: HyperlinkOrigin | null =
      origin ?? (S.selected ? { sheet: S.selected.sheet, ref: S.selected.ref } : null);
    if (link.kind === 'internal') {
      const target = S.sheets.find((item) => item.name === link.sheet);
      if (!target || !parseRef(link.ref) || !link.ref) {
        toast(localize('ui.navigation.jumpTargetExpired'), 'error');
        return;
      }
      this.markHyperlinkVisited(source);
      await this.navigateToCell(target.name, link.ref);
    } else {
      try {
        const target = new URL(link.url ?? '');
        if (
          !['https:', 'http:', 'mailto:'].includes(target.protocol) ||
          (target.protocol !== 'mailto:' && !target.hostname)
        ) {
          throw new Error('unsupported');
        }
        window.open(target.href, '_blank', 'noopener,noreferrer');
        this.markHyperlinkVisited(source);
      } catch {
        toast(localize('ui.navigation.thisHyperlinkProtocolNotSupported'), 'warning');
      }
    }
  }

  cellAt(row: number, col: number): CellData {
    const { S, colName } = this.d;
    const ref = `${colName(col)}${row}`;
    return (
      S.cellMap.get(ref) ??
      S.frozenCellMap.get(ref) ?? { ref, col, raw: '', display: '', hasFormula: false }
    );
  }

  selectCell(
    cell: CellData | null | undefined,
    rowNumber: number,
    colNumber: number,
    ref: string,
    visualIndex: number,
    preserveTrace = false,
    keepSelection = false,
    crosshair: { readonly row: number; readonly col: number } | null = null,
  ): void {
    const {
      S,
      colName,
      parseRef,
      applySelectionClasses,
      paintColumnSelection,
      getFrozenPanes,
      panels,
    } = this.d;
    const normalizedRef = ref || cell?.ref || `${colName(colNumber)}${rowNumber}`;
    const parsed = parseRef(normalizedRef);
    const col = colNumber || cell?.col || parsed?.col || 1;
    const row = rowNumber || parsed?.row || 1;
    const source: CellData = cell ?? { ref: normalizedRef, col, raw: '', display: '', hasFormula: false };
    const sheet = S.sheet ?? '';
    const normalized = panels.editCopy?.grid.active
      ? panels.editCopy.grid.project(sheet, normalizedRef, source)
      : source;
    if (
      !preserveTrace &&
      (S.selected?.ref !== normalizedRef || S.selected.sheet !== sheet)
    ) {
      panels.traceUI?.selectionChanged();
    }
    if (
      !preserveTrace &&
      S.traceRefs.length &&
      (S.selected?.ref !== normalizedRef || S.selected.sheet !== sheet)
    ) {
      S.traceRefs = [];
      for (const node of document.querySelectorAll('.grid-cell.precedent')) {
        node.classList.remove('precedent');
      }
    }
    S.selected = { ...normalized, ref: normalizedRef, row, col, sheet, crosshair };
    if (!keepSelection) {
      if (S.columnSelection) {
        S.columnSelection = null;
        S.columnAnchor = 0;
        paintColumnSelection();
      }
      S.wholeRowSelected = false;
      S.range = null;
      S.multiRanges = [];
      panels.selectionStats?.clear();
      S.rangeAnchor = { row, col, visual: visualIndex || row };
      requireEl('selectionStatus').textContent = '';
    }
    S.selectedVisualIndex = visualIndex || row;
    S.selectedFrozenThrough = S.filtered ? getFrozenPanes().effective().rows : 0;
    if (!keepSelection || !S.range) requireInput('nameBox').value = normalizedRef;
    panels.formulaInspector?.show(normalized, sheet);
    const formulaVal = requireEl('formulaValue');
    formulaVal.textContent = normalized.cacheMissing ? localize('ui.common.noCachingAvailable') : normalized.display;
    formulaVal.title = localize('ui.navigation.saveResultsOriginalValues', { detail: normalized.cacheMissing ? localize('ui.common.noCachingAvailable') : normalized.display, raw: normalized.raw });
    requireButton('tracePrecedentsBtn').disabled = !normalized.hasFormula || !normalized.formula;
    requireButton('traceDependentsBtn').disabled = false;
    requireButton('cellDetailBtn').disabled = false;
    requireButton('copyFormulaBtn').disabled = !normalized.hasFormula || !normalized.formula;
    requireButton('copyValueBtn').disabled = false;
    applySelectionClasses();
    panels.selectionStats?.schedule();
    panels.recordCardCenter?.controls();
    panels.editCopy?.grid.selection();
  }

  selectCellByAddress(ref: string, visualIndex?: number, keepSelection = false): void {
    const { S, parseRef } = this.d;
    const parsed = parseRef(ref);
    if (!parsed) return;
    const cell: CellData = S.cellMap.get(parsed.ref) ??
      S.frozenCellMap.get(parsed.ref) ?? {
        ref: parsed.ref,
        col: parsed.col,
        raw: '',
        display: '',
        hasFormula: false,
      };
    this.selectCell(
      cell,
      parsed.row,
      parsed.col,
      parsed.ref,
      visualIndex || parsed.row,
      keepSelection,
      keepSelection,
    );
  }

  selectInitialCell(): void {
    const {
      S,
      nextVisibleRow,
      nextVisibleColumn,
      colName,
      applySelectionClasses,
      getFrozenPanes,
      panels,
    } = this.d;
    const firstSource = nextVisibleRow(1);
    const firstMapped = S.rowNumberByVisual.values().next().value;
    const row =
      firstSource && firstSource <= getFrozenPanes().effective().rows
        ? firstSource
        : (firstMapped ?? (S.filtered ? 0 : firstSource));
    if (!row) {
      S.selected = null;
      S.range = null;
      S.multiRanges = [];
      S.rangeAnchor = null;
      S.wholeRowSelected = false;
      requireInput('nameBox').value = '';
      requireEl('selectionStatus').textContent = '';
      panels.formulaInspector?.clear();
      const formulaValue = requireEl('formulaValue');
      formulaValue.textContent = '-';
      formulaValue.title = '';
      for (const id of [
        'tracePrecedentsBtn',
        'traceDependentsBtn',
        'cellDetailBtn',
        'copyFormulaBtn',
        'copyValueBtn',
      ]) {
        requireButton(id).disabled = true;
      }
      panels.selectionStats?.clear();
      applySelectionClasses();
      return;
    }
    const column = nextVisibleColumn(1);
    const ref = `${colName(column)}${row}`;
    const visual = S.filtered
      ? ([...S.rowNumberByVisual].find(([, number]) => number === row)?.[0] ?? row)
      : row;
    this.selectCell(this.cellAt(row, column), row, column, ref, visual);
    if (S.sheet) this.recordVisit(S.sheet, ref);
    requireEl('grid').focus({ preventScroll: true });
  }

  syncFilteredSelection(through: number): void {
    const { S, applySelectionClasses, panels } = this.d;
    if (!S.selected) return;
    const changed = S.selectedFrozenThrough !== through;
    if (!changed && S.selectedVisualIndex) return;
    const { row, col } = S.selected;
    let visual = row <= through ? row : 0;
    if (!visual) {
      for (const [position, source] of S.rowNumberByVisual) {
        if (source === row) {
          visual = position;
          break;
        }
      }
    }
    S.selectedVisualIndex = visual;
    S.selectedFrozenThrough = through;
    S.rangeAnchor = { row, col, visual };
    if (changed && (S.range || S.multiRanges.length || S.wholeRowSelected)) {
      S.range = null;
      S.multiRanges = [];
      S.wholeRowSelected = false;
      requireEl('selectionStatus').textContent = '';
      panels.selectionStats?.clear();
      applySelectionClasses();
    }
  }

  beginGridNavigation(pinnedRows: number): () => boolean {
    const { S, getFrozenPanes } = this.d;
    const intent = {};
    const origin = S.selected;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const book = S.book;
    const key = S.workbookKey;
    const filterId = S.filterId;
    const viewId = S.rowView?.id;
    S.gridNavigation = intent;
    return (): boolean =>
      S.gridNavigation === intent &&
      S.selected === origin &&
      sheet === S.sheet &&
      epoch === S.sheetEpoch &&
      book === S.book &&
      key === S.workbookKey &&
      filterId === S.filterId &&
      viewId === S.rowView?.id &&
      (!S.filtered || pinnedRows === getFrozenPanes().effective().rows);
  }

  scrollCellIntoView(
    row: number,
    col: number,
    visual: number,
    pinned: { readonly rows: number; readonly cols: number },
  ): void {
    const {
      S,
      ensureExtent,
      getVirtualScrollTop,
      scrollTopToPhysical,
      headerHeight,
      sourceRowTop,
      rowTop,
      rowHeight,
      rowHead,
      colLeft,
      colWidth,
    } = this.d;
    ensureExtent(row || 1, col);
    const grid = requireEl('grid');
    const viewTop = getVirtualScrollTop();
    const viewLeft = grid.scrollLeft;
    let top = viewTop;
    let left = viewLeft;
    if (!row || row > pinned.rows) {
      const header = headerHeight();
      const frozenHeight = sourceRowTop(pinned.rows + 1);
      const cellTop = header + rowTop(visual) + (S.filtered ? frozenHeight : 0);
      const height = rowHeight(visual);
      top = revealCellOffset(viewTop, grid.clientHeight, header + frozenHeight, cellTop, height, height * 2);
    }
    if (col > pinned.cols) {
      const rh = rowHead();
      const cellLeft = rh + colLeft(col);
      const width = colWidth(col);
      left = revealCellOffset(viewLeft, grid.clientWidth, rh + colLeft(pinned.cols + 1), cellLeft, width, width);
    }
    if (top !== viewTop || left !== viewLeft) {
      grid.scrollTo({ top: scrollTopToPhysical(top), left, behavior: 'auto' });
    }
  }

  async locateFilteredVisual(row: number, through: number): Promise<number> {
    const { S, api } = this.d;
    if (row <= through) return row;
    const params = new URLSearchParams({
      sheet: S.sheet ?? '',
      filterId: S.filterId,
      row: String(row),
      excludeThroughRow: String(through),
    });
    if (S.rowView) params.set('viewId', S.rowView.id);
    const match = await api(`/api/filter/locate?${params.toString()}`, undefined, decodeFilterLocateResponse);
    return match.found && match.row === row ? match.visualIndex : 0;
  }

  async getCell(sheet: string, ref: string): Promise<CellData> {
    const { S, api } = this.d;
    const params = new URLSearchParams({ sheet, ref });
    if (!S.showStyles) params.set('conditional', '0');
    const data = await api(`/api/cell?${params.toString()}`, undefined, decodeCellResponse);
    return data.cell;
  }

  async navigateToCell(
    sheet: string,
    ref: string,
    preserveTrace = false,
    guard: (() => boolean) | null = null,
  ): Promise<boolean> {
    const {
      S,
      parseRef,
      toast,
      selectSheet,
      rowVisible,
      activeMerge,
      colName,
      getFrozenPanes,
      loadViewport,
      panels,
    } = this.d;
    const book = S.book;
    const key = S.workbookKey;
    const current = (): boolean =>
      book === S.book && key === S.workbookKey && (!guard || guard());
    if (!current()) return false;
    let parsed = parseRef(ref);
    if (!parsed) {
      toast(localize('ui.navigation.unableLocateCell', { ref }), 'error');
      return false;
    }
    if (S.selected && !S.historyNavigating) this.recordVisit(S.selected.sheet, S.selected.ref);
    if (sheet !== S.sheet) {
      return Boolean(await selectSheet(sheet, parsed.ref, preserveTrace, current));
    }
    const epoch = S.sheetEpoch;
    if (S.rowVisibility && !rowVisible(parsed.row)) {
      if (
        !panels.rowViews ||
        !(await panels.rowViews.reveal(parsed.row, () => current() && epoch === S.sheetEpoch))
      ) {
        return false;
      }
      if (!current() || epoch !== S.sheetEpoch) return false;
      toast(localize('ui.navigation.rowTemporarilyDisplayed', { row: parsed.row }), 'info');
    }
    const merged = activeMerge(parsed.row, parsed.col);
    if (merged) {
      const anchor = parseRef(`${colName(merged.col)}${merged.row}`);
      if (anchor) parsed = anchor;
    }
    const pinned = getFrozenPanes().effective();
    const readCurrent = this.beginGridNavigation(pinned.rows);
    const viewCurrent = (): boolean => current() && readCurrent();
    const filteredBody = S.filtered && parsed.row > pinned.rows;
    let visual = parsed.row;
    try {
      if (filteredBody) {
        visual = await this.locateFilteredVisual(parsed.row, pinned.rows);
        if (!viewCurrent()) return false;
        if (!visual) {
          toast(localize('ui.navigation.rowContainingThisCellHidden'), 'warning');
          return false;
        }
      }
      this.scrollCellIntoView(parsed.row, parsed.col, visual, pinned);
      await loadViewport(true);
      if (!viewCurrent()) return false;
      if (filteredBody && S.rowNumberByVisual.get(visual) !== parsed.row) {
        toast(localize('ui.navigation.targetRowNotReadYet'), 'warning');
        return false;
      }
      const cell =
        S.cellMap.get(parsed.ref) ??
        S.frozenCellMap.get(parsed.ref) ??
        (await this.getCell(sheet, parsed.ref));
      if (!viewCurrent()) return false;
      this.selectCell(cell, parsed.row, parsed.col, parsed.ref, visual, preserveTrace);
      this.recordVisit(sheet, parsed.ref);
      requireEl('grid').focus({ preventScroll: true });
      return true;
    } catch (error: unknown) {
      if (viewCurrent()) throw error;
      return false;
    }
  }

  async navigateCatalogueArea(
    area: CatalogueAreaTarget | null | undefined,
    guard: () => boolean,
  ): Promise<boolean> {
    const { S, parseRef, showWorkspace, activeMerge, colName, ensureExtent, panels } = this.d;
    if (!guard() || !S.opened) return false;
    if (S.opening || S.sheetLoadingEpoch || S.filterJob || panels.rowViews?.busy) {
      throw new Error(localize('ui.navigation.completeLoadingCurrentWorksheetFiltering'));
    }
    const target = S.sheets.find((sheet) => sheet.name === area?.sheet);
    const start = parseRef(area?.start);
    const end = parseRef(area?.end);
    if (!target || !start || !end || end.row < start.row || end.col < start.col) {
      throw new Error(localize('ui.navigation.directoryPositioningRangeInvalidRead'));
    }
    const multiple = start.ref !== end.ref;
    if (multiple && (target.name === S.sheet ? S.filtered : Boolean(S.filtersBySheet.get(target.name)))) {
      throw new Error(localize('ui.navigation.thisTableFilterViewClear'));
    }
    showWorkspace();
    await this.navigateToCell(target.name, start.ref, false, guard);
    if (!guard() || S.sheet !== target.name || !S.selected || !S.indexReady) return false;
    const merge = activeMerge(start.row, start.col);
    const anchor = merge ? `${colName(merge.col)}${merge.row}` : start.ref;
    if (S.selected.ref !== anchor) return false;
    if (multiple) {
      if (S.filtered) throw new Error(localize('ui.navigation.thisTableFilteredViewClear'));
      ensureExtent(end.row, end.col);
      S.multiRanges = [];
      this.extendRange({ row: start.row, col: start.col, visual: start.row }, end.row, end.col);
      requireInput('nameBox').value = `${start.ref}:${end.ref}`;
    }
    return true;
  }

  adjacentGridPoint(
    initialRow: number,
    initialVisual: number,
    originCol: number,
    rowDelta: number,
    colDelta: number,
    pinned: { readonly rows: number; readonly cols: number },
  ): AdjacentGridPoint {
    const {
      S,
      sourceColLimit,
      activeMerge,
      nextVisibleColumn,
      previousVisibleColumn,
      advanceVisibleRow,
      previousVisibleRow,
      nextVisibleRow,
    } = this.d;
    let targetRow = initialRow;
    let targetVisual = initialVisual;
    const originVisual = initialVisual;
    const colLimit = sourceColLimit();
    let targetCol = Math.max(1, Math.min(colLimit, originCol + colDelta));
    const fromMerge = activeMerge(targetRow, originCol);
    if (fromMerge && colDelta === 1) targetCol = Math.min(colLimit, fromMerge.endCol + 1);
    if (colDelta && S.hiddenColumns.has(targetCol)) {
      targetCol =
        (colDelta > 0 ? nextVisibleColumn(targetCol) : previousVisibleColumn(targetCol)) ||
        originCol;
    }
    if (S.filtered) {
      if (rowDelta) {
        if (targetRow <= pinned.rows) {
          const header = advanceVisibleRow(targetRow, rowDelta);
          if (header && header <= pinned.rows) {
            targetRow = header;
            targetVisual = header;
          } else if (rowDelta > 0 && S.filterTotal) {
            targetRow = 0;
            targetVisual = 1;
          }
        } else {
          const header = previousVisibleRow(pinned.rows);
          if (targetVisual + rowDelta < 1 && header) {
            targetRow = header;
            targetVisual = header;
          } else {
            targetVisual = Math.max(1, Math.min(S.filterTotal, targetVisual + rowDelta));
            if (targetVisual !== originVisual) {
              targetRow =
                S.window?.excludeThroughRow === pinned.rows
                  ? (S.rowNumberByVisual.get(targetVisual) ?? 0)
                  : 0;
            }
          }
        }
      }
    } else {
      targetRow = advanceVisibleRow(targetRow, rowDelta) || targetRow;
      if (fromMerge && rowDelta === 1) targetRow = nextVisibleRow(fromMerge.endRow + 1) || targetRow;
      const destination = activeMerge(targetRow, targetCol);
      if (destination) {
        targetRow = destination.row;
        targetCol = destination.col;
      }
      targetVisual = targetRow;
    }
    return { kind: 'cell', row: targetRow, col: targetCol, visual: targetVisual };
  }

  async moveSelection(
    rowDelta: number,
    colDelta: number,
    extend = false,
    keepAnchor = false,
  ): Promise<void> {
    const {
      S,
      colName,
      getFrozenPanes,
      getViewportRequest,
      windowContains,
      loadViewport,
    } = this.d;
    if (!S.selected) {
      this.selectInitialCell();
      return;
    }
    const editingRange = extend && keepAnchor && S.range;
    let targetRow = editingRange ? editingRange.row2 : S.selected.row;
    let targetVisual = editingRange ? editingRange.visual2 || targetRow : S.selectedVisualIndex;
    const pinned = getFrozenPanes().effective();
    const current = this.beginGridNavigation(pinned.rows);
    let preceding = extend ? S.multiRanges.slice() : [];
    let anchor: CellPoint | null = extend
      ? (S.rangeAnchor ?? { row: targetRow, col: S.selected.col, visual: targetVisual })
      : null;
    if (S.filtered && (S.selectedFrozenThrough !== pinned.rows || !targetVisual)) {
      targetVisual = await this.locateFilteredVisual(targetRow, pinned.rows);
      if (!current() || !targetVisual) return;
      preceding = [];
      if (extend) anchor = { row: targetRow, col: S.selected.col, visual: targetVisual };
    }
    const point = this.adjacentGridPoint(
      targetRow,
      targetVisual,
      editingRange ? editingRange.col2 : S.selected.col,
      rowDelta,
      colDelta,
      pinned,
    );
    targetRow = point.row;
    targetVisual = point.visual;
    const targetCol = point.col;
    this.scrollCellIntoView(targetRow, targetCol, targetVisual, pinned);
    if (!targetRow || !S.window || !windowContains(getViewportRequest())) await loadViewport(true);
    if (!current()) return;
    if (!targetRow && S.window?.excludeThroughRow === pinned.rows) {
      targetRow = S.rowNumberByVisual.get(targetVisual) ?? 0;
    }
    if (!targetRow) return;
    const targetRef = `${colName(targetCol)}${targetRow}`;
    if (!extend || !keepAnchor) {
      this.selectCell(this.cellAt(targetRow, targetCol), targetRow, targetCol, targetRef, targetVisual);
    }
    if (extend) {
      S.multiRanges = preceding;
      this.extendRange(anchor, targetRow, targetCol, targetVisual);
    }
  }

  async jumpDataEdge(
    direction: 'up' | 'down' | 'left' | 'right',
    extend = false,
  ): Promise<void> {
    const { S, api, toast, setStatus } = this.d;
    if (!S.indexReady || !S.selected || !S.sheet) return;
    if (S.largeData && (direction === 'up' || direction === 'down')) {
      toast(localize('ui.navigation.ultraLargeDataModeTurn'), 'warning');
      return;
    }
    if (S.filtered) {
      toast(localize('ui.navigation.quickNavigationDataBoundariesNot'), 'warning');
      return;
    }
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const anchor: CellPoint | null = extend
      ? (S.rangeAnchor ?? { row: S.selected.row, col: S.selected.col })
      : null;
    const preceding = extend ? S.multiRanges.slice() : [];
    S.navigationAbort?.abort();
    const abort = new AbortController();
    S.navigationAbort = abort;
    const ticket = ++S.navigationTicket;
    const origin = S.selected.ref;
    setStatus(localize('ui.navigation.searchingDataBoundaries', { detail: direction === 'up' || direction === 'down' ? localize('ui.common.column') : localize('ui.navigation.row') }));
    try {
      const params = new URLSearchParams({ sheet, ref: origin, direction });
      const result = await api(`/api/navigate?${params.toString()}`, {
        signal: abort.signal,
      }, decodeNavigateResponse);
      if (abort.signal.aborted || ticket !== S.navigationTicket || S.sheetEpoch !== epoch || S.sheet !== sheet) {
        return;
      }
      await this.navigateToCell(sheet, result.ref);
      if (anchor && S.sheetEpoch === epoch) {
        S.multiRanges = preceding;
        this.extendRange(anchor, result.row, result.col);
      }
    } catch (error: unknown) {
      if (!abort.signal.aborted) toast(errorMessage(error), 'error');
    } finally {
      if (ticket === S.navigationTicket) {
        S.navigationAbort = null;
        setStatus(localize('ui.navigation.opened', { sheet }), true);
      }
    }
  }

  recordVisit(sheet: string, ref: string): void {
    const { S } = this.d;
    if (!S.opened || !sheet || !ref || S.historyNavigating) return;
    const previous = S.history[S.historyIndex];
    if (previous?.sheet === sheet && previous.ref === ref) return;
    S.history = S.history.slice(0, S.historyIndex + 1);
    S.history.push({ sheet, ref });
    if (S.history.length > 90) S.history.shift();
    S.historyIndex = S.history.length - 1;
    this.updateHistoryButtons();
  }

  updateHistoryButtons(): void {
    const { S } = this.d;
    requireButton('historyBackBtn').disabled = S.historyIndex <= 0;
    requireButton('historyForwardBtn').disabled = S.historyIndex >= S.history.length - 1;
  }

  async goHistory(delta: number): Promise<void> {
    const { S, toast } = this.d;
    const next = S.historyIndex + delta;
    const item = S.history[next];
    if (next < 0 || !item) return;
    S.historyNavigating = true;
    try {
      if (await this.navigateToCell(item.sheet, item.ref)) S.historyIndex = next;
    } catch (error: unknown) {
      toast(errorMessage(error), 'error');
    } finally {
      S.historyNavigating = false;
      this.updateHistoryButtons();
    }
  }

  inSelectedRange(row: number, col: number, visualIndex = 0): boolean {
    const { S, getFrozenPanes } = this.d;
    const through = S.filtered ? getFrozenPanes().effective().rows : 0;
    return MultiSelection.areas(S).some((area) =>
      MultiSelection.contains(area, row, col, visualIndex, S.filtered, through),
    );
  }

  extendRange(anchor: CellPoint | null, row: number, col: number, visualIndex = 0): void {
    const { S, mergedCells, toast, applySelectionClasses, getFrozenPanes, panels } = this.d;
    if (!anchor) return;
    let v1 = 0;
    let v2 = 0;
    if (S.filtered) {
      v1 = Number(anchor.visual) || Number(S.rangeAnchor?.visual) || 0;
      const rowNode = document.querySelector<HTMLElement>(`.grid-row[data-row="${row}"]`);
      v2 = visualIndex || Number(rowNode?.dataset.visualIndex) || 0;
      if (!v2 && row === S.selected?.row) v2 = S.selectedVisualIndex;
      if (!v1 || !v2) {
        toast(localize('ui.navigation.currentFilterRowPositionNot'), 'warning');
        return;
      }
    }
    S.rangeAnchor = { ...anchor, visual: v1 || anchor.visual || anchor.row };
    const area: SelectionArea = {
      row1: anchor.row,
      col1: anchor.col,
      row2: row,
      col2: col,
      visual1: v1,
      visual2: v2,
    };
    S.range = S.filtered ? area : mergedCells.expand(area, S.rowVisibility);
    const pinnedRows = getFrozenPanes().effective().rows;
    const rows = S.rowVisibility
      ? S.rowVisibility.selectionCount(S.range, S.filtered, pinnedRows)
      : MultiSelection.rowCount(S.range, S.filtered, pinnedRows);
    const cols = Math.abs(S.range.col1 - S.range.col2) + 1;
    requireEl('selectionStatus').textContent = S.multiRanges.length
      ? localize('ui.navigation.votingDistrictLocatedAt', { count: S.multiRanges.length + 1 })
      : localize('ui.navigation.rowsColumnsCells', { rows, cols, detail: (rows * cols).toLocaleString(interfaceLocale()) });
    applySelectionClasses();
    panels.selectionStats?.schedule();
  }

  selectWholeRow(row: number, visualIndex?: number): void {
    const { S, applySelectionClasses, panels } = this.d;
    if (!Number.isSafeInteger(row) || row < 1 || !S.indexReady) return;
    const ref = `A${row}`;
    const visual = visualIndex || row;
    this.selectCell(this.cellAt(row, 1), row, 1, ref, visual);
    S.wholeRowSelected = true;
    requireEl('selectionStatus').textContent = localize('ui.navigation.rowAtPositionEntireRow', { row });
    S.range = {
      row1: row,
      row2: row,
      col1: 1,
      col2: S.usedMaxCol,
      visual1: S.filtered ? visual : 0,
      visual2: S.filtered ? visual : 0,
    };
    applySelectionClasses();
    panels.selectionStats?.schedule();
    if (!panels.calculator?.captureRange(`${row}:${row}`)) {
      requireEl('grid').focus({ preventScroll: true });
    }
  }
}
