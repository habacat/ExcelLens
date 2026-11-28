/**
 * Grid pointer/drag selection, keyboard routing, ribbon access sequences,
 * toolbar/dialog event bindings, drag-and-drop, and DPR/resize observers.
 */
import { t as localize } from '../core/i18n.ts';
import type { CellPoint, CellRef, MergeRange, SelectionArea } from '../contracts/common.ts';
import type { CellData, CellHyperlink } from '../contracts/workbook.ts';
import { errorMessage, isComposing } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl, requireInput } from '../core/dom.ts';
import type { ApiClient } from '../core/transport.ts';
import type { AutoFilterDropdown } from './autofilter.ts';
import type { ColumnController } from './columns.ts';
import type { FrozenPanes } from './freeze.ts';
import type { GridClipboard } from './grid-clipboard.ts';
import type { GridControls } from './grid-context-menu.ts';
import type { WorkbookIO } from './grid-workbook-io.ts';
import type { HighlightMode } from './highlight-mode.ts';
import type { MergedCells } from './merges.ts';
import { MultiSelection } from './multi-selection.ts';
import type { HyperlinkOrigin } from './navigation.ts';
import type { ReadingMode } from './reading-mode.ts';
import type { GridCellElement, ViewportRequest, WorkbookState } from './state.ts';
import type { SourcePointHit, TileCache } from './viewport-cache.ts';
import type { ScrollMotion, VirtualScroll } from './virtual-scroll.ts';

interface DragAnchorState {
  readonly row: number;
  readonly col: number;
  readonly ref: string;
  readonly visual: number;
  readonly cell?: CellData | null;
  readonly crosshair?: { readonly row: number; readonly col: number } | null;
  readonly clientX: number;
  readonly clientY: number;
}

interface ScrollPressRecord {
  readonly ref: string;
  readonly epoch: number;
  readonly context: string;
  readonly view: string | undefined;
  readonly filter: string;
}

export interface GridInteractionPanels {
  readonly editCopy?: {
    readonly grid: {
      readonly active: boolean;
      focus(): void;
      navigate(fn: () => Promise<void>): void;
    };
  } | null;
  readonly calculator?: {
    readonly picking: boolean;
    toggle(): void;
    handleKey(event: KeyboardEvent): boolean;
    captureCell(ref: string, event: MouseEvent): boolean;
    captureRange(start: string, end?: string): boolean;
  } | null;
  readonly frozenPanes: FrozenPanes;
  readonly recentManager: { load(): Promise<void> | void };
  readonly profileCenter: { open(): void };
  readonly exportCenter: { open(mode?: string): void };
  readonly copySpecial: { open(): void };
  readonly bookmarkCenter: { open(): void };
  readonly autoFilterUI: AutoFilterDropdown;
  readonly traceUI?: { minimize(): Promise<void> | void } | null;
  readonly selectionInspector: { open(mode?: string): void };
  readonly multiSort: { show(): void };
  readonly selectionStats?: { schedule(): void } | null;
  readonly readingMode?: ReadingMode | null;
  readonly highlightMode?: HighlightMode | null;
  readonly dataRulesCenter: { open(): void };
  readonly recordCardCenter?: { open(): void } | null;
  readonly printingCenter?: { open(): void } | null;
  readonly selectionQuick?: { open(): void } | null;
  readonly transposeCenter?: { open(): void } | null;
  readonly catalogueCenter: { open(query?: string): Promise<void> | void };
  readonly taskRecovery: { open(): void };
  readonly searchPanel: { bind?: () => void };
  readonly commandPalette: { bind(): void };
  readonly columnController: ColumnController;
  readonly databasePanel: { open(): Promise<void> | void };
  readonly databaseSnapshots: { show(key?: string): void };
  readonly snapshots?: { open(): void } | null;
}

export interface GridInteractionsDeps {
  readonly S: WorkbookState;
  readonly api: ApiClient;
  readonly toast: (message: string, tone?: 'info' | 'success' | 'warning' | 'error') => void;
  readonly setStatus: (text: string) => void;
  readonly colName: (col: number) => string;
  readonly parseRef: (text: string | null | undefined) => CellRef | null;
  readonly cellAt: (row: number, col: number) => CellData;
  readonly tiles: TileCache;
  readonly mergedCells: MergedCells;
  readonly virtualScroll: VirtualScroll;
  readonly motion: ScrollMotion;
  readonly controls: GridControls;
  readonly clipboard: GridClipboard;
  readonly workbookIO: WorkbookIO;
  readonly panels: GridInteractionPanels;
  readonly rowHead: () => number;
  readonly rowHeight: (row?: number) => number;
  readonly colWidth: (col?: number) => number;
  readonly headerHeight: () => number;
  readonly rowTop: (row: number) => number;
  readonly colLeft: (col: number) => number;
  readonly getVirtualScrollTop: () => number;
  readonly getViewportRequest: () => ViewportRequest;
  readonly viewportContext: () => string;
  readonly activeMerge: (row: number, col: number) => MergeRange | null;
  readonly selectCell: (
    cell: CellData | null | undefined,
    rowNumber: number,
    colNumber: number,
    ref: string,
    visualIndex: number,
    preserveTrace?: boolean,
    keepSelection?: boolean,
    crosshair?: { readonly row: number; readonly col: number } | null,
  ) => void;
  readonly extendRange: (anchor: CellPoint | null, row: number, col: number, visualIndex?: number) => void;
  readonly applySelectionClasses: () => void;
  readonly selectWholeRow: (row: number, visualIndex?: number) => void;
  readonly selectColumnSpan: (anchor: number, end: number) => void;
  readonly captureColumnSelection: () => void;
  readonly selectSheet: (name: string) => Promise<boolean | undefined>;
  readonly selectSheetTab: (name: string) => Promise<boolean | undefined>;
  readonly navigateToCell: (sheet: string, ref: string) => Promise<boolean>;
  readonly navigateNameBox: (value: string) => Promise<void>;
  readonly moveSelection: (
    rowDelta: number,
    colDelta: number,
    extend?: boolean,
    keepAnchor?: boolean,
  ) => Promise<void>;
  readonly jumpDataEdge: (direction: 'up' | 'down' | 'left' | 'right', extend?: boolean) => Promise<void>;
  readonly goHistory: (delta: number) => Promise<void>;
  readonly followCellHyperlink: (
    link: CellHyperlink | null | undefined,
    origin?: HyperlinkOrigin | null,
  ) => Promise<void>;
  readonly showWelcome: () => void;
  readonly showCellDetail: () => void;
  readonly openCommandPalette: () => void;
  readonly openSearch: (mode?: string | null, query?: string | null) => void;
  readonly stepSearch: (delta: number) => void;
  readonly toggleAutoFilter: () => Promise<void>;
  readonly toggleFormulaView: () => void;
  readonly toggleOriginalFormat: () => void;
  readonly changeDisplayFont: (font: string) => Promise<void>;
  readonly openFilter: (column?: number) => void;
  readonly addFilterRule: (column?: number, operator?: string, value?: string) => void;
  readonly applyFilter: () => Promise<void>;
  readonly cancelFilter: (jobID: string | null) => Promise<void>;
  readonly clearFilter: () => Promise<void>;
  readonly filterSelectedValue: () => Promise<void>;
  readonly openFreezeDialog: () => void;
  readonly applyFreeze: (rows: number, cols: number) => Promise<void>;
  readonly applyTextTableHeader: (number: number, automatic?: boolean) => Promise<void>;
  readonly disableTextTableHeader: () => Promise<void>;
  readonly promptResize: (kind: 'row' | 'col', explicitIndex?: number) => Promise<void>;
  readonly commitResize: (kind: 'row' | 'col', index: number, size: number) => Promise<void>;
  readonly autoFitSize: (kind: 'row' | 'col', index: number) => Promise<void>;
  readonly setColumnVisibility: (
    mode: string,
    index: number,
    hidden: boolean,
    end?: number,
  ) => Promise<void>;
  readonly tracePrecedents: () => Promise<void>;
  readonly traceDependents: () => Promise<void>;
  readonly followGridScroll: () => void;
  readonly scheduleRangeFrames: () => void;
  readonly syncTableColumnHeaders: () => void;
  readonly syncFreezeScrollGutters: () => void;
  readonly updateCanvasSize: () => void;
  readonly scheduleInitialPreview: () => void;
  readonly renderGridFromCurrentWindow: () => void;
  readonly paintRangeFrames: () => void;
  readonly scheduleViewport: (force?: boolean) => void;
}

export class GridInteractions {
  private readonly d: GridInteractionsDeps;
  private dragAnchor: DragAnchorState | null = null;
  private dragMoved = false;
  private dragBase: SelectionArea[] = [];
  public suppressGridClick = false;
  private dragFrame = 0;
  private dragPending: { readonly row: number; readonly col: number; readonly visual: number } | null =
    null;
  private scrollPress: ScrollPressRecord | null = null;
  private ribbonSequence = '';
  public ribbonExpires = 0;

  constructor(deps: GridInteractionsDeps) {
    this.d = deps;
  }

  isDragging(): boolean {
    return Boolean(this.dragAnchor);
  }

  selectPointedCell(
    cell: CellData,
    point: CellRef & { readonly visual: number },
    event: MouseEvent,
  ): void {
    const { selectCell, mergedCells, colLeft, rowTop } = this.d;
    selectCell(
      cell,
      point.row,
      point.col,
      point.ref,
      point.visual,
      false,
      false,
      mergedCells.pointerCell(event, colLeft, rowTop),
    );
  }

  selectWithModifiers(
    dataCell: CellData,
    point: CellRef & { readonly visual: number },
    event: MouseEvent,
  ): void {
    const { S, colName, toast, cellAt, selectCell, extendRange, applySelectionClasses, panels } =
      this.d;
    const additive = event.ctrlKey || event.metaKey;
    const extend = event.shiftKey;
    const oldAnchor: CellPoint | null = extend
      ? (S.rangeAnchor ?? {
          row: S.selected?.row ?? point.row,
          col: S.selected?.col ?? point.col,
          visual: S.selectedVisualIndex || point.visual,
        })
      : null;
    const previous: SelectionArea[] = additive
      ? extend
        ? S.multiRanges.slice()
        : MultiSelection.areas(S, true)
      : extend
        ? S.multiRanges.slice()
        : [];
    if (additive && !extend) {
      const removed = MultiSelection.removeSingleton(
        previous,
        point.row,
        point.col,
        point.visual,
        S.filtered,
      );
      if (removed) {
        if (removed.length === 0) {
          panels.selectionStats?.schedule();
          return;
        }
        const last = removed[removed.length - 1];
        if (last) {
          const row = last.row2;
          const col = last.col2;
          const visual = last.visual2 || row;
          selectCell(cellAt(row, col), row, col, `${colName(col)}${row}`, visual);
          S.multiRanges = removed.slice(0, -1);
          S.range = last;
          requireEl('selectionStatus').textContent =
            removed.length > 1 ? localize('ui.gridInteractions.votingDistrictLocatedAt', { count: removed.length }) : localize('ui.gridInteractions.selectedArea');
          applySelectionClasses();
          panels.selectionStats?.schedule();
        }
        return;
      }
    }
    if (previous.length >= MultiSelection.LIMIT) {
      toast(localize('ui.gridInteractions.upRegionsCanBeSelected'), 'warning');
      return;
    }
    if (extend && oldAnchor && panels.editCopy?.grid.active && !additive) {
      extendRange(oldAnchor, point.row, point.col, point.visual);
      return;
    }
    this.selectPointedCell(dataCell, point, event);
    S.multiRanges = previous;
    if (extend) {
      extendRange(oldAnchor, point.row, point.col, point.visual);
    } else if (additive) {
      S.range = MultiSelection.rectangle(point, point, S.filtered);
      requireEl('selectionStatus').textContent = localize('ui.gridInteractions.votingDistrictLocatedAt', { count: previous.length + 1 });
      applySelectionClasses();
      panels.selectionStats?.schedule();
    }
  }

  private flushRangeDrag(): void {
    const { S, cellAt, selectCell, extendRange } = this.d;
    this.dragFrame = 0;
    const pending = this.dragPending;
    this.dragPending = null;
    if (!pending || !this.dragAnchor) return;
    if (!this.dragMoved) {
      this.dragMoved = true;
      selectCell(
        this.dragAnchor.cell ?? cellAt(this.dragAnchor.row, this.dragAnchor.col),
        this.dragAnchor.row,
        this.dragAnchor.col,
        this.dragAnchor.ref,
        this.dragAnchor.visual,
        false,
        false,
        this.dragAnchor.crosshair,
      );
    }
    S.multiRanges = this.dragBase;
    extendRange(this.dragAnchor, pending.row, pending.col, pending.visual);
  }

  private cachedBodyPress(event: PointerEvent): SourcePointHit | null {
    const {
      S,
      tiles,
      getViewportRequest,
      rowHead,
      headerHeight,
      rowTop,
      colLeft,
      activeMerge,
      colName,
    } = this.d;
    const target = event.target;
    if (
      !(target instanceof HTMLElement) ||
      !['rowsLayer', 'gridCanvas', 'viewportFallback', 'grid'].includes(target.id)
    ) {
      return null;
    }
    const grid = requireEl('grid');
    const rect = grid.getBoundingClientRect();
    const request = getViewportRequest();
    const pos = request.position;
    if (!pos) return null;
    const { pw, ph } = pos;
    const rh = rowHead();
    const localX = event.clientX - rect.left - grid.clientLeft - rh;
    const localY = event.clientY - rect.top - grid.clientTop - headerHeight();
    if (
      localX < pw ||
      localY < ph ||
      localX + rh >= grid.clientWidth ||
      localY + headerHeight() >= grid.clientHeight
    ) {
      return null;
    }
    const hit = tiles.sourceAtPoint(request, {
      x: pos.x + localX,
      y: pos.y + localY - (S.filtered ? ph : 0),
      lastRow: S.filtered ? S.filterTotal : S.maxRow,
      lastCol: S.maxCol,
      rowTop,
      colLeft,
      mergeAt: activeMerge,
    });
    if (hit) hit.ref = `${colName(hit.col)}${hit.row}`;
    return hit;
  }

  private beginRangeDrag(event: PointerEvent): void {
    const { S, parseRef, cellAt, selectCell, viewportContext, toast, controls, panels } = this.d;
    this.scrollPress = null;
    const target = event.target;
    if (
      event.button !== 0 ||
      !S.indexReady ||
      !(target instanceof Element) ||
      target.closest('.column-filter,.auto-filter-button,.column-resize-grip')
    ) {
      return;
    }
    const cellNode = target.closest('.grid-cell');
    const cell = cellNode instanceof HTMLDivElement ? (cellNode as GridCellElement) : null;
    const readingPress =
      event.pointerType === 'mouse' &&
      !event.shiftKey &&
      !event.ctrlKey &&
      !event.metaKey &&
      !event.altKey &&
      !panels.editCopy?.grid.active &&
      !panels.calculator?.picking &&
      !S.sheetLoadingEpoch &&
      Boolean(target.closest('#grid'));
    const cached = !cell && readingPress ? this.cachedBodyPress(event) : null;
    const parsed = cached
      ? { row: cached.row, col: cached.col, ref: cached.ref ?? '' }
      : parseRef(cell?.dataset.ref);
    if (!parsed?.ref) return;
    controls.hideCellMenu();
    requireEl('cellTooltip').hidden = true;
    this.dragAnchor = {
      row: parsed.row,
      col: parsed.col,
      ref: parsed.ref,
      visual: cached?.visual || Number(cell?.dataset.visualIndex) || parsed.row,
      cell: cached?.cell ?? cell?._xlModel,
      crosshair: cached?.crosshair,
      clientX: event.clientX,
      clientY: event.clientY,
    };
    this.dragBase =
      event.ctrlKey || event.metaKey
        ? MultiSelection.areas(S, true)
        : event.shiftKey
          ? S.multiRanges.slice()
          : [];
    if (this.dragBase.length >= MultiSelection.LIMIT) {
      this.dragAnchor = null;
      toast(localize('ui.gridInteractions.upRegionsCanBeSelected'), 'warning');
      return;
    }
    this.dragPending = null;
    if (this.dragFrame) {
      cancelAnimationFrame(this.dragFrame);
      this.dragFrame = 0;
    }
    this.dragMoved = false;
    if (readingPress) {
      if (cached) {
        selectCell(
          cached.cell,
          cached.row,
          cached.col,
          parsed.ref,
          cached.visual,
          false,
          false,
          cached.crosshair,
        );
      } else if (cell) {
        this.selectWithModifiers(
          cell._xlModel ?? cellAt(parsed.row, parsed.col),
          this.dragAnchor,
          event,
        );
      }
      this.scrollPress = {
        ref: parsed.ref,
        epoch: S.sheetEpoch,
        context: viewportContext(),
        view: S.rowView?.id,
        filter: S.filterId,
      };
      requireEl('grid').focus({ preventScroll: true });
    }
  }

  private updateRangeDrag(event: PointerEvent): void {
    const { parseRef } = this.d;
    if (!this.dragAnchor || !(event.buttons & 1)) return;
    if (
      !this.dragMoved &&
      event.clientX === this.dragAnchor.clientX &&
      event.clientY === this.dragAnchor.clientY
    ) {
      return;
    }
    const target = event.target;
    const cell = target instanceof Element ? target.closest('.grid-cell') : null;
    if (!(cell instanceof HTMLElement)) return;
    const parsed = parseRef(cell.dataset.ref);
    if (!parsed || (parsed.row === this.dragAnchor.row && parsed.col === this.dragAnchor.col)) {
      return;
    }
    this.dragPending = {
      row: parsed.row,
      col: parsed.col,
      visual: Number(cell.dataset.visualIndex) || parsed.row,
    };
    if (!this.dragFrame) this.dragFrame = requestAnimationFrame(() => this.flushRangeDrag());
  }

  private endRangeDrag(event?: PointerEvent): void {
    const { S, colName, panels } = this.d;
    if (!this.dragAnchor) return;
    if (this.dragFrame) {
      cancelAnimationFrame(this.dragFrame);
      this.dragFrame = 0;
    }
    this.flushRangeDrag();
    if (this.dragMoved) {
      if (event?.type !== 'pointercancel' && panels.calculator?.picking && S.range) {
        const area = S.range;
        panels.calculator.captureRange(
          `${colName(Math.min(area.col1, area.col2))}${Math.min(area.row1, area.row2)}`,
          `${colName(Math.max(area.col1, area.col2))}${Math.max(area.row1, area.row2)}`,
        );
      }
      this.suppressGridClick = true;
      setTimeout(() => {
        this.suppressGridClick = false;
      }, 0);
    }
    if (this.dragMoved || event?.type === 'pointercancel') this.scrollPress = null;
    this.dragAnchor = null;
    this.dragBase = [];
  }

  private selectNativeRange(kind: 'row' | 'column' | 'all'): void {
    const { S, extendRange } = this.d;
    if (!S.indexReady || !S.selected) return;
    const row = S.selected.row;
    const col = S.selected.col;
    const visual = S.filtered ? S.selectedVisualIndex : row;
    if (kind === 'row') {
      extendRange({ row, col: 1, visual }, row, Math.max(1, S.usedMaxCol), visual);
    } else if (kind === 'column') {
      extendRange(
        { row: S.filtered ? row : 1, col, visual: 1 },
        S.filtered ? row : Math.max(1, S.usedMaxRow),
        col,
        S.filtered ? S.filterTotal : S.usedMaxRow,
      );
    } else {
      extendRange(
        { row: 1, col: 1, visual: 1 },
        Math.max(1, S.usedMaxRow),
        Math.max(1, S.usedMaxCol),
        S.filtered ? S.filterTotal : S.usedMaxRow,
      );
    }
    requireEl('grid').focus({ preventScroll: true });
  }

  private switchSheet(delta: number): void {
    const { S, selectSheetTab, toast } = this.d;
    if (!S.sheet || !S.sheets.length) return;
    const index = S.sheets.findIndex((x) => x.name === S.sheet);
    const next = (index + delta + S.sheets.length) % S.sheets.length;
    const target = S.sheets[next];
    if (!target) return;
    selectSheetTab(target.name).catch((error: unknown) => {
      toast(errorMessage(error), 'error');
    });
  }

  public handleRibbonSequence(
    event: Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'key'>,
    key: string,
    gridFocused: boolean,
    take: (callback: () => unknown) => void,
  ): boolean {
    const { S, autoFitSize, promptResize } = this.d;
    if (!gridFocused || event.ctrlKey || event.metaKey || event.shiftKey) return false;
    const now = Date.now();
    if (now > this.ribbonExpires) this.ribbonSequence = '';
    if (event.altKey && key === 'h') {
      this.ribbonSequence = 'H';
      this.ribbonExpires = now + 3000;
      take(() => undefined);
      return true;
    }
    if (!this.ribbonSequence || event.key.length !== 1) return false;
    const next = this.ribbonSequence + key.toUpperCase();
    if (next === 'HO') {
      this.ribbonSequence = next;
      this.ribbonExpires = now + 3000;
      take(() => undefined);
      return true;
    }
    if (this.ribbonSequence === 'HO') {
      this.ribbonSequence = '';
      const actions: Readonly<Record<string, () => Promise<void>>> = {
        I: () => autoFitSize('col', S.selected?.col ?? 1),
        A: () => autoFitSize('row', S.selected?.row ?? 1),
        W: () => promptResize('col', S.selected?.col ?? 1),
        H: () => promptResize('row', S.selected?.row ?? 1),
      };
      const target = actions[key.toUpperCase()];
      if (target) {
        take(target);
        return true;
      }
    } else {
      this.ribbonSequence = '';
    }
    return false;
  }

  private handleSpreadsheetKeydown(event: KeyboardEvent): void {
    const {
      S,
      toast,
      colName,
      rowHead,
      rowHeight,
      colWidth,
      controls,
      clipboard,
      workbookIO,
      panels,
      openCommandPalette,
      openSearch,
      toggleAutoFilter,
      toggleFormulaView,
      showCellDetail,
      stepSearch,
      goHistory,
      jumpDataEdge,
      moveSelection,
      navigateToCell,
      extendRange,
      followCellHyperlink,
    } = this.d;
    if (isComposing(event)) return;
    if (panels.editCopy?.grid.active && !document.querySelector('dialog:modal')) return;
    const key = event.key.toLowerCase();
    const ctrl = event.ctrlKey || event.metaKey;
    const alt = event.altKey;
    const shift = event.shiftKey;
    const active = document.activeElement;
    const editing =
      (active instanceof HTMLElement &&
        (active.matches('input,textarea,select,[contenteditable="true"],[role="searchbox"]') ||
          active.isContentEditable)) ||
      false;
    const modal = document.querySelector('dialog:modal');
    const formulaText = requireEl('formulaText');
    const insideFormula = active === formulaText || formulaText.contains(active);
    const gridFocused = Boolean(
      active === requireEl('grid') ||
        insideFormula ||
        (active instanceof Element && active.closest('.grid-region')),
    );
    const take = (callback: () => unknown): void => {
      event.preventDefault();
      event.stopPropagation();
      Promise.resolve()
        .then(callback)
        .catch((err: unknown) => {
          toast(errorMessage(err), 'error');
        });
    };
    const readOnlyHint = (): string =>
      requireEl('editCopyOpen').hidden
        ? localize('ui.gridInteractions.currentlyReadOnlyBrowsingMode')
        : localize('ui.gridInteractions.currentlyReadOnlyBrowsingMode1854545');

    if (!modal && ctrl && alt && !shift && key === 'c') {
      take(() => panels.calculator?.toggle());
      return;
    }
    if (!modal && panels.calculator?.handleKey(event)) return;
    if (event.key === 'Escape') {
      this.ribbonSequence = '';
      controls.hideCellMenu();
      panels.autoFilterUI.hide();
      panels.readingMode?.close();
      panels.highlightMode?.close();
      if (requireDialog('traceDialog').open && !modal) void panels.traceUI?.minimize();
      if (requireDialog('searchDialog').open && !modal) {
        event.preventDefault();
        controls.closeDialog('searchDialog');
      }
      return;
    }
    if (modal || event.defaultPrevented) {
      this.ribbonSequence = '';
      return;
    }
    if (ctrl && !alt && !shift && key === 'enter' && gridFocused && !editing && S.selected?.hyperlink) {
      const selected = S.selected;
      const link = selected.hyperlink;
      take(() => followCellHyperlink(link, { sheet: selected.sheet, ref: selected.ref }));
      return;
    }
    if (ctrl && !alt && !shift && key === 'o' && !editing) {
      take(() => workbookIO.chooseFile());
      return;
    }
    if (!requireEl('welcome').hidden || !S.opened || !S.indexReady) return;
    if (ctrl && !alt && !shift && key === 'c' && window.getSelection()?.toString()) return;
    if (insideFormula && ctrl && !alt && !shift && key === 'a') {
      take(() => {
        const range = document.createRange();
        range.selectNodeContents(formulaText);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
      });
      return;
    }
    if (editing) return;
    if (this.handleRibbonSequence(event, key, gridFocused, take)) return;

    if (ctrl && !alt && !shift && key === 'k') {
      take(openCommandPalette);
      return;
    }
    if (ctrl && !alt && !shift && key === 's') {
      take(() => panels.exportCenter.open());
      return;
    }
    if (ctrl && !alt && !shift && key === 'f') {
      take(() => openSearch());
      return;
    }
    if ((ctrl && !alt && !shift && key === 'g') || event.key === 'F5') {
      take(() => {
        const box = requireInput('nameBox');
        box.focus();
        box.select();
      });
      return;
    }
    if (ctrl && !alt && !shift && key === 'c') {
      take(() => clipboard.copySelection());
      return;
    }
    if (ctrl && alt && shift && key === 'c') {
      take(() => panels.copySpecial.open());
      return;
    }
    if (ctrl && !alt && shift && key === 'c') {
      take(() =>
        S.selected?.hasFormula
          ? clipboard.copyText(S.selected.formula ?? '')
          : toast(localize('ui.gridInteractions.currentCellNotFormula')),
      );
      return;
    }
    if (ctrl && !alt && !shift && key === 'a' && gridFocused) {
      take(() => this.selectNativeRange('all'));
      return;
    }
    if (ctrl && !alt && !shift && event.code === 'Space' && gridFocused) {
      take(() => this.selectNativeRange('column'));
      return;
    }
    if (!alt && shift && event.code === 'Space' && gridFocused) {
      take(() => this.selectNativeRange('row'));
      return;
    }
    if (ctrl && shift && !alt && key === 'l') {
      take(toggleAutoFilter);
      return;
    }
    if (ctrl && !alt && !shift && (event.code === 'Backquote' || event.key === '`')) {
      take(toggleFormulaView);
      return;
    }
    if (ctrl && alt && key === 'g') {
      take(() => panels.selectionInspector.open('formulas'));
      return;
    }
    if (ctrl && shift && !alt && key === 'd') {
      take(() => panels.selectionInspector.open('duplicates'));
      return;
    }
    if (ctrl && shift && !alt && key === 'e') {
      take(() => panels.exportCenter.open());
      return;
    }
    if (ctrl && alt && key === 's') {
      take(() => panels.profileCenter.open());
      return;
    }
    if (ctrl && alt && key === 'b') {
      take(() => panels.bookmarkCenter.open());
      return;
    }
    if (ctrl && !shift && !alt && key === 'q') {
      take(() => panels.selectionQuick?.open());
      return;
    }
    if (ctrl && !alt && !shift && key === '1') {
      take(showCellDetail);
      return;
    }
    if (event.key === 'F2') {
      take(showCellDetail);
      return;
    }
    if (ctrl && !alt && !shift && (event.key === 'PageUp' || event.key === 'PageDown')) {
      take(() => this.switchSheet(event.key === 'PageUp' ? -1 : 1));
      return;
    }
    if (!ctrl && !alt && event.key === 'F3') {
      take(() => stepSearch(shift ? -1 : 1));
      return;
    }
    if (ctrl && !alt && !shift && event.key === 'F3') {
      take(() => panels.catalogueCenter.open());
      return;
    }
    if (alt && !ctrl && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      take(() => goHistory(event.key === 'ArrowLeft' ? -1 : 1));
      return;
    }
    if (alt && !ctrl && !shift && event.key === 'ArrowDown' && S.selected) {
      const col = S.selected.col;
      take(() => {
        if (!S.autoFilter) {
          toast(localize('ui.gridInteractions.pressCtrlShiftLEnable'), 'warning');
          return undefined;
        }
        const bounds = requireEl('grid').getBoundingClientRect();
        const anchor =
          [...document.querySelectorAll(`.auto-filter-button[data-column="${col}"]`)].find(
            (node) => {
              const rect = node.getBoundingClientRect();
              return (
                rect.width > 0 &&
                rect.top >= bounds.top &&
                rect.bottom <= bounds.bottom &&
                rect.left >= bounds.left &&
                rect.right <= bounds.right
              );
            },
          ) ?? requireEl('filterOpenBtn');
        return panels.autoFilterUI.open(col, anchor);
      });
      return;
    }
    if (!gridFocused) return;
    if (
      (ctrl && !alt && ['x', 'v', 'z', 'y'].includes(key)) ||
      (!ctrl && !alt && (event.key === 'Backspace' || event.key === 'Delete'))
    ) {
      take(() => toast(readOnlyHint()));
      return;
    }
    const directions: Readonly<Record<string, 'up' | 'down' | 'left' | 'right'>> = {
      ArrowUp: 'up',
      ArrowDown: 'down',
      ArrowLeft: 'left',
      ArrowRight: 'right',
    };
    const direction = directions[event.key];
    if (ctrl && !alt && direction) {
      take(() => jumpDataEdge(direction, shift));
      return;
    }
    const deltas: Readonly<Record<string, readonly [number, number]>> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
    };
    const delta = deltas[event.key];
    if (!ctrl && !alt && delta) {
      take(() => moveSelection(delta[0], delta[1], shift));
      return;
    }
    if (!ctrl && !alt && (event.key === 'Tab' || event.key === 'Enter')) {
      const sign = shift ? -1 : 1;
      take(() => (event.key === 'Tab' ? moveSelection(0, sign) : moveSelection(sign, 0)));
      return;
    }
    if ((event.key === 'Home' || event.key === 'End') && !alt) {
      if (event.key === 'End' && !ctrl) return;
      const targetRow = event.key === 'End' ? S.usedMaxRow : ctrl ? 1 : (S.selected?.row ?? 1);
      const targetCol = event.key === 'End' ? S.usedMaxCol : 1;
      take(async () => {
        const anchor: CellPoint | null =
          shift && S.selected
            ? (S.rangeAnchor ?? {
                row: S.selected.row,
                col: S.selected.col,
                visual: S.selectedVisualIndex,
              })
            : null;
        if (anchor && S.filtered) {
          toast(localize('ui.gridInteractions.afterFilteringDragSelectExtended'), 'warning');
          return;
        }
        if (!S.sheet) return;
        await navigateToCell(S.sheet, `${colName(targetCol)}${targetRow}`);
        if (anchor) extendRange(anchor, targetRow, targetCol);
      });
      return;
    }
    if (alt && !ctrl && (event.key === 'PageUp' || event.key === 'PageDown')) {
      const width = requireEl('grid').clientWidth - rowHead();
      const count = Math.max(1, Math.floor(width / Math.max(32, colWidth(S.selected?.col ?? 1))) - 1);
      take(() => moveSelection(0, (event.key === 'PageUp' ? -1 : 1) * count, shift));
      return;
    }
    if (!ctrl && !alt && (event.key === 'PageUp' || event.key === 'PageDown')) {
      const step = Math.max(1, Math.floor(requireEl('grid').clientHeight / rowHeight()) - 2);
      take(() => moveSelection(event.key === 'PageDown' ? step : -step, 0, shift));
    }
  }

  private bindHomeAndFileEvents(): void {
    const { S, api, toast, setStatus, showWelcome, workbookIO, panels } = this.d;
    const homeMenu = requireEl('homeDataMenu') as HTMLDetailsElement;
    const menuTrigger = requireEl('homeDataTrigger');
    homeMenu.addEventListener(
      'click',
      (event: MouseEvent) => {
        const target = event.target;
        if (!(target instanceof Element) || !target.closest('[role="button"]')) return;
        const selection = window.getSelection();
        if (event.detail && selection && !selection.isCollapsed && homeMenu.contains(selection.anchorNode)) {
          event.stopPropagation();
          return;
        }
        homeMenu.open = false;
        menuTrigger.focus();
      },
      true,
    );
    homeMenu.addEventListener('keydown', (event: KeyboardEvent) => {
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        target.matches('[role="button"]') &&
        (event.key === 'Enter' || event.key === ' ')
      ) {
        event.preventDefault();
        event.stopPropagation();
        if (!event.repeat) target.click();
        return;
      }
      if (event.key === 'Escape' && homeMenu.open) {
        event.preventDefault();
        event.stopPropagation();
        homeMenu.open = false;
        menuTrigger.focus();
      }
    });
    document.addEventListener('pointerdown', (event: PointerEvent) => {
      const target = event.target;
      if (homeMenu.open && target instanceof Node && !homeMenu.contains(target)) {
        homeMenu.open = false;
      }
    });
    window.addEventListener('excellens:session-expired', (event: Event) => {
      const detail = event instanceof CustomEvent ? String(event.detail ?? '') : '';
      setStatus(detail);
      toast(detail, 'warning');
    });
    document.addEventListener('toolbar-setting-error', (event: Event) => {
      const detail = event instanceof CustomEvent ? String(event.detail ?? '') : '';
      toast(detail, 'error');
    });
    panels.commandPalette.bind();
    requireEl('openLocalMenuBtn').addEventListener('click', () => workbookIO.chooseFile());
    for (const id of ['openDatabaseBtn', 'openDatabaseHome']) {
      requireEl(id).addEventListener('click', () => {
        if (workbookIO.canOpenWorkbook(localize('ui.gridInteractions.databaseConnection'))) void panels.databasePanel.open();
      });
    }
    for (const id of ['databaseSnapshotsBtn', 'databaseSnapshotsHome']) {
      requireEl(id).addEventListener('click', () => panels.databaseSnapshots.show());
    }
    requireEl('retainDatabaseSnapshot').addEventListener('click', () => {
      panels.databaseSnapshots.show(S.workbookKey);
    });
    requireEl('homeBtn').addEventListener('click', () => {
      if (workbookIO.canOpenWorkbook(localize('ui.gridInteractions.backHome'))) {
        showWelcome();
        void panels.recentManager.load();
      }
    });
    requireEl('chooseFileBtn').addEventListener('click', () => workbookIO.chooseFile());
    requireInput('fileInput').addEventListener('change', (event: Event) => {
      const input = event.target;
      if (!(input instanceof HTMLInputElement)) return;
      const file = input.files?.[0];
      input.value = '';
      void workbookIO.uploadFile(file);
    });
    for (const id of ['exitBtn', 'homeExitBtn']) {
      requireEl(id).addEventListener('click', () => {
        if (!workbookIO.canOpenWorkbook(localize('ui.gridInteractions.exitProgram'))) return;
        api('/api/exit', { method: 'POST' })
          .catch(() => {
            // Process may close immediately.
          })
          .finally(() => {
            setStatus(localize('ui.gridInteractions.programExited'));
          });
      });
    }
  }

  private bindToolbarAndDialogEvents(): void {
    const {
      S,
      toast,
      controls,
      clipboard,
      workbookIO,
      panels,
      navigateNameBox,
      toggleFormulaView,
      openCommandPalette,
      toggleAutoFilter,
      applyTextTableHeader,
      disableTextTableHeader,
      openFilter,
      openFreezeDialog,
      toggleOriginalFormat,
      changeDisplayFont,
      applyFreeze,
      goHistory,
      addFilterRule,
      applyFilter,
      cancelFilter,
      clearFilter,
      tracePrecedents,
      traceDependents,
      showCellDetail,
      selectSheet,
    } = this.d;

    const nameBox = requireInput('nameBox');
    nameBox.addEventListener('keydown', (event: KeyboardEvent) => {
      if (isComposing(event)) return;
      if (event.key === 'Enter') {
        event.preventDefault();
        const address = nameBox.value;
        const navigate = (): Promise<void> =>
          navigateNameBox(address).then(() => {
            if (panels.editCopy?.grid.active) panels.editCopy.grid.focus();
            else requireEl('grid').focus({ preventScroll: true });
          });
        if (panels.editCopy?.grid.active) panels.editCopy.grid.navigate(navigate);
        else {
          navigate().catch((err: unknown) => {
            toast(errorMessage(err), 'error');
          });
        }
      } else if (event.key === 'Escape') {
        nameBox.value = S.selected?.ref ?? '';
        nameBox.blur();
      }
    });
    panels.searchPanel.bind?.();
    requireEl('multiSortBtn').addEventListener('click', () => panels.multiSort.show());
    requireEl('inspectOpenBtn').addEventListener('click', () => panels.selectionInspector.open());
    requireEl('duplicateOpenBtn').addEventListener('click', () => {
      panels.selectionInspector.open('duplicates');
    });
    requireEl('formulaViewBtn').addEventListener('click', toggleFormulaView);
    requireEl('exportOpenBtn').addEventListener('click', () => panels.exportCenter.open());
    requireEl('profileOpenBtn').addEventListener('click', () => panels.profileCenter.open());
    requireEl('commandOpenBtn').addEventListener('click', () => openCommandPalette());
    requireEl('selectionStatus').addEventListener('click', () => panels.profileCenter.open());
    requireEl('filterOpenBtn').addEventListener('click', () => {
      void toggleAutoFilter();
    });
    requireEl('textHeaderBtn').addEventListener('click', () => {
      if (!S.indexReady) return;
      requireInput('textHeaderRow').value = String(S.autoFilter?.row ?? S.selected?.row ?? 1);
      requireButton('textHeaderAuto').disabled = !S.source?.tableHeader;
      controls.openDialog('textHeaderDialog');
    });
    requireEl('textHeaderApply').addEventListener('click', () => {
      void applyTextTableHeader(Number(requireInput('textHeaderRow').value));
    });
    requireInput('textHeaderRow').addEventListener('keydown', (event: KeyboardEvent) => {
      if (!isComposing(event) && event.key === 'Enter') {
        event.preventDefault();
        const target = event.target;
        if (target instanceof HTMLInputElement) void applyTextTableHeader(Number(target.value));
      }
    });
    requireEl('textHeaderAuto').addEventListener('click', () => {
      void applyTextTableHeader(0, true);
    });
    requireEl('textHeaderPlain').addEventListener('click', () => {
      void disableTextTableHeader();
    });
    requireEl('advancedFilterBtn').addEventListener('click', () => openFilter());
    requireEl('freezeBtn').addEventListener('click', openFreezeDialog);
    requireEl('formatBtn').addEventListener('click', toggleOriginalFormat);
    requireEl('fontSelect').addEventListener('change', (event: Event) => {
      const target = event.target;
      if (target instanceof HTMLSelectElement) void changeDisplayFont(target.value);
    });
    requireEl('freezeFirstRowBtn').addEventListener('click', () => {
      void applyFreeze(1, S.freeze.cols);
    });
    requireEl('freezeFirstColBtn').addEventListener('click', () => {
      void applyFreeze(S.freeze.rows, 1);
    });
    requireEl('freezeAtCellBtn').addEventListener('click', () => {
      void applyFreeze(
        Math.max(0, (S.selected?.row ?? 1) - 1),
        Math.max(0, (S.selected?.col ?? 1) - 1),
      );
    });
    requireEl('freezeResetBtn').addEventListener('click', () => {
      void applyFreeze(0, 0);
    });
    requireEl('freezeApplyBtn').addEventListener('click', () => {
      void applyFreeze(
        Number(requireInput('freezeRowsInput').value),
        Number(requireInput('freezeColsInput').value),
      );
    });
    requireEl('historyBackBtn').addEventListener('click', () => {
      void goHistory(-1);
    });
    requireEl('historyForwardBtn').addEventListener('click', () => {
      void goHistory(1);
    });
    requireEl('helpBtn').addEventListener('click', () => controls.openDialog('helpDialog'));
    requireEl('addFilterRuleBtn').addEventListener('click', () => addFilterRule());
    requireEl('applyFilterBtn').addEventListener('click', () => {
      void applyFilter();
    });
    requireEl('cancelFilterJobBtn').addEventListener('click', () => {
      const jobID = S.filterJob;
      void cancelFilter(jobID).then(() => {
        requireEl('filterProgressText').textContent = localize('ui.gridInteractions.requestCancelingFilterTasksReceived');
        requireButton('applyFilterBtn').disabled = false;
        requireEl('cancelFilterJobBtn').hidden = true;
      });
    });
    requireEl('clearFilterBtn').addEventListener('click', () => {
      clearFilter().catch((error: unknown) => {
        toast(errorMessage(error), 'error');
      });
    });
    requireEl('sheetListBtn').addEventListener('click', () => {
      workbookIO.renderSheetPicker();
      controls.openDialog('sheetDialog');
      requireInput('sheetPickerSearch').focus();
    });
    requireEl('sheetPrevBtn').addEventListener('click', () => workbookIO.pageSheetTabs(-1));
    requireEl('sheetNextBtn').addEventListener('click', () => workbookIO.pageSheetTabs(1));
    const sheetTabs = requireEl('sheetTabs');
    sheetTabs.addEventListener('scroll', () => workbookIO.updateSheetPager(), { passive: true });
    new ResizeObserver(() => workbookIO.updateSheetPager()).observe(sheetTabs);
    requireInput('sheetPickerSearch').addEventListener('input', (event: Event) => {
      const target = event.target;
      if (target instanceof HTMLInputElement) workbookIO.renderSheetPicker(target.value);
    });
    requireEl('tracePrecedentsBtn').addEventListener('click', () => {
      void tracePrecedents();
    });
    requireEl('traceDependentsBtn').addEventListener('click', () => {
      void traceDependents();
    });
    requireEl('cellDetailBtn').addEventListener('click', showCellDetail);
    requireEl('copyFormulaBtn').addEventListener('click', () => {
      void clipboard.copyText(S.selected?.formula ?? '');
    });
    requireEl('copyValueBtn').addEventListener('click', () => {
      void clipboard.copyText(S.selected?.display ?? S.selected?.raw ?? '');
    });
    requireEl('copySelectionBtn').addEventListener('click', () => {
      void clipboard.copySelection();
    });
    requireEl('copyPlainBtn').addEventListener('click', () => {
      void clipboard.copySelection(true);
    });
    requireEl('copySpecialBtn').addEventListener('click', () => panels.copySpecial.open());
    requireEl('copyAddressBtn').addEventListener('click', () => {
      void clipboard.copyQualifiedAddress();
    });
    requireEl('copyEvidenceBtn').addEventListener('click', () => {
      void clipboard.copyCellEvidence();
    });
    requireEl('selectionQuickBtn').addEventListener('click', () => panels.selectionQuick?.open());
    requireEl('transposeOpenBtn').addEventListener('click', () => panels.transposeCenter?.open());
    requireEl('snapshotsOpenBtn').addEventListener('click', () => panels.snapshots?.open());
    requireEl('taskRecoveryOpenBtn').addEventListener('click', () => panels.taskRecovery.open());
    requireEl('fullscreenBtn').addEventListener('click', () => {
      void controls.toggleFullscreen();
    });
    document.addEventListener('fullscreenchange', () => controls.updateFullscreenButton());
    requireEl('zoomOutBtn').addEventListener('click', () => controls.setZoom(S.zoom - 5));
    requireEl('zoomValue').addEventListener('click', () => controls.setZoom(100));
    requireEl('zoomInBtn').addEventListener('click', () => controls.setZoom(S.zoom + 5));
    requireEl('retryIndexBtn').addEventListener('click', () => {
      if (S.sheet) void selectSheet(S.sheet);
    });
  }

  private bindGridPointerAndScrollEvents(): void {
    const {
      S,
      toast,
      virtualScroll,
      motion,
      controls,
      panels,
      rowHeight,
      headerHeight,
      getVirtualScrollTop,
      followGridScroll,
      scheduleRangeFrames,
      syncTableColumnHeaders,
      scheduleViewport,
      selectWholeRow,
      selectColumnSpan,
      captureColumnSelection,
      parseRef,
      cellAt,
      viewportContext,
      followCellHyperlink,
      showCellDetail,
    } = this.d;

    const grid = requireEl('grid');
    grid.addEventListener(
      'wheel',
      (event: WheelEvent) => {
        if (event.ctrlKey) {
          event.preventDefault();
          controls.setZoom(S.zoom + (event.deltaY < 0 ? 5 : -5));
          return;
        }
        if (virtualScroll.scale === 1 || !event.cancelable) return;
        event.preventDefault();
        const next = virtualScroll.wheel(
          event,
          grid.scrollTop,
          rowHeight(),
          grid.clientHeight - headerHeight(),
        );
        grid.scrollLeft += next.left;
        grid.scrollTop = next.top;
        followGridScroll();
      },
      { passive: false },
    );

    grid.addEventListener(
      'scroll',
      () => {
        scheduleRangeFrames();
        if (S.sheetLoadingEpoch) return;
        if (
          !requireEl('cellMenu').hidden ||
          !requireEl('sizeContextMenu').hidden ||
          !requireEl('editGridMenu').hidden
        ) {
          controls.hideCellMenu();
        }
        followGridScroll();
        syncTableColumnHeaders();
        panels.frozenPanes.followScroll();
        if (S.freeze.rows || S.freeze.cols) panels.frozenPanes.schedule();
        controls.hideTooltip();
        motion.sample(grid.scrollLeft, getVirtualScrollTop(), performance.now());
        scheduleViewport(false);
      },
      { passive: true },
    );

    grid.addEventListener('click', (event: MouseEvent) => {
      if (this.suppressGridClick) {
        this.suppressGridClick = false;
        return;
      }
      const target = event.target;
      if (!(target instanceof Element)) return;
      const rowHead = target.closest('.row-head');
      if (rowHead) {
        if (target.closest('.row-resize-grip')) return;
        const owner = rowHead.closest('.grid-row');
        if (owner instanceof HTMLElement) {
          selectWholeRow(Number(owner.dataset.row), Number(owner.dataset.visualIndex));
        }
        return;
      }
      const cell = target.closest('.grid-cell');
      if (!(cell instanceof HTMLElement)) return;
      const ref = cell.dataset.ref ?? '';
      const parsed = parseRef(ref);
      if (!parsed) return;
      const visual = Number(cell.dataset.visualIndex);
      const dataCell = cellAt(parsed.row, parsed.col);
      const pressed = this.scrollPress;
      this.scrollPress = null;
      if (pressed && event.detail) {
        if (
          pressed.epoch !== S.sheetEpoch ||
          pressed.context !== viewportContext() ||
          pressed.view !== S.rowView?.id ||
          pressed.filter !== S.filterId ||
          pressed.ref !== ref
        ) {
          return;
        }
      } else {
        this.selectWithModifiers(dataCell, { ...parsed, visual }, event);
      }
      const calculating = panels.calculator?.captureCell(parsed.ref, event) ?? false;
      if (
        !panels.calculator?.picking &&
        !event.shiftKey &&
        !event.ctrlKey &&
        !event.metaKey &&
        !event.altKey &&
        dataCell.hyperlink
      ) {
        followCellHyperlink(dataCell.hyperlink, { sheet: S.sheet ?? '', ref: parsed.ref }).catch(
          (error: unknown) => {
            toast(errorMessage(error), 'error');
          },
        );
      }
      if (!calculating) grid.focus({ preventScroll: true });
      requireEl('cellTooltip').hidden = true;
    });

    const region = document.querySelector<HTMLElement>('.grid-region');
    if (!region) return;
    region.addEventListener('click', (event: MouseEvent) => {
      if (this.suppressGridClick) {
        this.suppressGridClick = false;
        return;
      }
      const target = event.target;
      if (!(target instanceof Element) || target.closest('#grid')) return;
      const frozenRowHead = target.closest('.pinned-row-head');
      if (frozenRowHead instanceof HTMLElement) {
        if (!target.closest('.row-resize-grip')) selectWholeRow(Number(frozenRowHead.dataset.row));
        return;
      }
      const column = target.closest('.pinned-head');
      if (column instanceof HTMLElement) {
        if (target.closest('.column-resize-grip,.column-filter,.auto-filter-button')) return;
        const col = Number(column.dataset.col);
        if (event.detail === 0 || !S.columnSelection) {
          selectColumnSpan(event.shiftKey ? (S.columnAnchor || col) : col, col);
          captureColumnSelection();
        }
        return;
      }
      const cell = target.closest('.pinned-cell');
      if (!(cell instanceof HTMLElement)) return;
      const parsed = parseRef(cell.dataset.ref);
      if (!parsed) return;
      const visual = Number(cell.dataset.visualIndex) || parsed.row;
      const dataCell = cellAt(parsed.row, parsed.col);
      this.selectWithModifiers(dataCell, { ...parsed, visual }, event);
      const calculating = panels.calculator?.captureCell(parsed.ref, event) ?? false;
      if (
        !panels.calculator?.picking &&
        !event.shiftKey &&
        !event.ctrlKey &&
        !event.metaKey &&
        !event.altKey &&
        dataCell.hyperlink
      ) {
        followCellHyperlink(dataCell.hyperlink, { sheet: S.sheet ?? '', ref: parsed.ref }).catch(
          (error: unknown) => {
            toast(errorMessage(error), 'error');
          },
        );
      }
      if (!calculating) grid.focus({ preventScroll: true });
    });

    region.addEventListener('pointermove', (event: PointerEvent) => {
      if (event.target instanceof Element && event.target.closest('#grid')) return;
      controls.showTooltip(event);
    });
    region.addEventListener('contextmenu', (event: MouseEvent) => controls.openCellMenu(event));
    region.addEventListener('dblclick', (event: MouseEvent) => {
      if (panels.calculator?.picking) {
        event.preventDefault();
        return;
      }
      if (event.target instanceof Element && event.target.closest('.grid-cell')) showCellDetail();
    });
    panels.columnController.bind(region);
    region.addEventListener('pointerdown', (event: PointerEvent) => this.beginRangeDrag(event));
    document.addEventListener('pointermove', (event: PointerEvent) => this.updateRangeDrag(event));
    document.addEventListener('pointerup', (event: PointerEvent) => this.endRangeDrag(event));
    document.addEventListener('pointercancel', (event: PointerEvent) => this.endRangeDrag(event));
    grid.addEventListener('pointermove', (event: PointerEvent) => controls.showTooltip(event));
    grid.addEventListener('pointerleave', () => {
      requireEl('cellTooltip').hidden = true;
    });
  }

  private bindDragDropAndResizeEvents(): void {
    const {
      S,
      workbookIO,
      panels,
      syncFreezeScrollGutters,
      scheduleRangeFrames,
      updateCanvasSize,
      scheduleInitialPreview,
      renderGridFromCurrentWindow,
      paintRangeFrames,
      scheduleViewport,
    } = this.d;
    const dropZone = requireEl('dropZone');
    for (const type of ['dragenter', 'dragover'] as const) {
      dropZone.addEventListener(type, (event: DragEvent) => {
        event.preventDefault();
        event.stopPropagation();
        dropZone.classList.add('dragging');
      });
    }
    for (const type of ['dragleave', 'dragend'] as const) {
      dropZone.addEventListener(type, (event: DragEvent) => {
        event.preventDefault();
        event.stopPropagation();
        dropZone.classList.remove('dragging');
      });
    }
    dropZone.addEventListener('drop', (event: DragEvent) => {
      event.preventDefault();
      event.stopPropagation();
      dropZone.classList.remove('dragging');
      void workbookIO.uploadFile(event.dataTransfer?.files[0]);
    });
    document.addEventListener('dragover', (event: DragEvent) => event.preventDefault());
    document.addEventListener('drop', (event: DragEvent) => {
      if (event.target instanceof Element && event.target.closest('#dropZone')) return;
      event.preventDefault();
      const file = event.dataTransfer?.files[0];
      if (file) void workbookIO.uploadFile(file);
    });

    new ResizeObserver(() => {
      syncFreezeScrollGutters();
      scheduleRangeFrames();
    }).observe(requireEl('grid'));

    let gridPixelRatio = window.devicePixelRatio || 1;
    let resizeFrame = 0;
    const resizeWorkspace = (): void => {
      const ratio = window.devicePixelRatio || 1;
      const changed = ratio !== gridPixelRatio;
      gridPixelRatio = ratio;
      if (S.previewOnly) {
        updateCanvasSize();
        scheduleInitialPreview();
        return;
      }
      if (!S.indexReady) return;
      updateCanvasSize();
      if (changed) {
        renderGridFromCurrentWindow();
        paintRangeFrames();
      } else {
        scheduleRangeFrames();
      }
      scheduleViewport(changed);
      panels.frozenPanes.schedule(changed);
    };
    const scheduleResizeWorkspace = (): void => {
      if (resizeFrame) return;
      resizeFrame = requestAnimationFrame(() => {
        resizeFrame = 0;
        resizeWorkspace();
      });
    };
    const watchPixelRatio = (): void => {
      if ((window.devicePixelRatio || 1) !== gridPixelRatio) resizeWorkspace();
      if (typeof window.matchMedia === 'function') {
        window
          .matchMedia(`(resolution: ${gridPixelRatio}dppx)`)
          .addEventListener('change', watchPixelRatio, { once: true });
      }
    };
    watchPixelRatio();
    window.addEventListener('resize', scheduleResizeWorkspace);
  }

  bindEvents(): void {
    const {
      controls,
      clipboard,
      panels,
      promptResize,
      commitResize,
      autoFitSize,
      setColumnVisibility,
      filterSelectedValue,
      followCellHyperlink,
      openSearch,
      tracePrecedents,
      applyFreeze,
    } = this.d;
    this.bindHomeAndFileEvents();
    this.bindToolbarAndDialogEvents();
    this.bindGridPointerAndScrollEvents();
    controls.bindMenuEvents({
      promptResize,
      commitResize,
      autoFitSize,
      setColumnVisibility,
      copySelection: (plain, cut) => clipboard.copySelection(plain, cut),
      copyText: (val, msg) => clipboard.copyText(val, msg),
      copyQualifiedAddress: () => clipboard.copyQualifiedAddress(),
      copyCellEvidence: () => clipboard.copyCellEvidence(),
      openCopySpecial: () => panels.copySpecial.open(),
      filterSelectedValue,
      followCellHyperlink,
      openSearch,
      tracePrecedents,
      openInspector: (mode) => panels.selectionInspector.open(mode),
      openDataRules: () => panels.dataRulesCenter.open(),
      openRecordCard: () => panels.recordCardCenter?.open(),
      openPrintPreview: () => panels.printingCenter?.open(),
      applyFreeze,
      openExport: (mode) => panels.exportCenter.open(mode),
      openProfile: () => panels.profileCenter.open(),
      openSelectionQuick: () => panels.selectionQuick?.open(),
      openTranspose: () => panels.transposeCenter?.open(),
      openBookmark: () => panels.bookmarkCenter.open(),
    });
    this.bindDragDropAndResizeEvents();
    document.addEventListener('keydown', (event: KeyboardEvent) =>
      this.handleSpreadsheetKeydown(event),
    );
  }
}
