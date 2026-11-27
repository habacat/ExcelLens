/**
 * Application composition root for ExcelLens (`internal/ui/web/dist/app.js`).
 * Wires Layer 1 core services, Layer 2 grid/viewport controllers, and Layer 3
 * editing, analysis, and feature modules using explicit ES imports and
 * dependency injection. Exposes ONLY the narrow QA bridge in `runtime-bridge.ts`.
 */
import type { ToastTone } from '../contracts/common.ts';
import type {
  CellData,
  SheetViewportResponse,
  WorkbookStatusResponse,
} from '../contracts/workbook.ts';
import { createActivityService, setActivityService } from '../core/activity.ts';
import {
  columnName,
  parseColumn as parseColumnAddress,
  parseRef as parseCellRef,
} from '../core/cell-address.ts';
import { errorMessage, formatBytes, icon } from '../core/display-format.ts';
import {
  byId,
  requireButton,
  requireDialog,
  requireEl,
  requireInput,
  requireSelect,
} from '../core/dom.ts';
import { requireFileFormats } from '../core/formats.ts';
import { createToastController, initOverlays } from '../core/overlays.ts';
import { createBrowserSession } from '../core/session.ts';
import { createTransport } from '../core/transport.ts';

import { AutoFilterDropdown } from '../grid/autofilter.ts';
import { configureClipboard } from '../grid/clipboard.ts';
import { CopySpecialController } from '../grid/clipboard-panels.ts';
import { ColumnController } from '../grid/columns.ts';
import { FrozenPanes } from '../grid/freeze.ts';
import { Geometry } from '../grid/geometry.ts';
import { GridClipboard } from '../grid/grid-clipboard.ts';
import { GridControls } from '../grid/grid-context-menu.ts';
import {
  GridInteractions,
  type GridInteractionPanels,
} from '../grid/grid-interactions.ts';
import {
  renderSourceIntegrity as renderSourceIntegrityBanner,
  WorkbookIO,
  type WorkbookIOPanels,
} from '../grid/grid-workbook-io.ts';
import { HighlightMode } from '../grid/highlight-mode.ts';
import { Pictures } from '../grid/images.ts';
import { MergedCells } from '../grid/merges.ts';
import { MultiSelection } from '../grid/multi-selection.ts';
import {
  GridNavigation,
  linkDescription,
  type NavigationPanels,
} from '../grid/navigation.ts';
import { ReadingMode } from '../grid/reading-mode.ts';
import { inspectRichText } from '../grid/richtext.ts';
import {
  SheetController,
  type SheetControllerPanels,
} from '../grid/sheet-controller.ts';
import {
  createInitialWorkbookState,
  type GridCellElement,
  type TraceRefEntry,
} from '../grid/state.ts';
import {
  estimateViewportBytes,
  TileCache,
  viewportCoversVisible,
} from '../grid/viewport-cache.ts';
import {
  BASE_COL,
  BASE_ROW,
  fontStack,
  isKnownFont,
  MAX_ROWS_PER_REQUEST,
  VIEWPORT_CACHE_BUDGET,
  ViewportPipeline,
  type ViewportPipelinePanels,
} from '../grid/viewport-pipeline.ts';
import { ScrollMotion, VirtualScroll } from '../grid/virtual-scroll.ts';

import { EditCopy } from '../editing/edit-copy.ts';
import { encodePasteMarkup } from '../editing/edit-grid-paste.ts';
import {
  dragPoint as computeReferenceDragPoint,
  visibleAreas as visibleReferenceAreas,
} from '../editing/edit-reference-view.ts';

import { Archives } from '../features/archives.ts';
import { createBackgroundMonitor } from '../features/background-tasks.ts';
import { Bookmarks } from '../features/bookmarks.ts';
import { Calculator } from '../features/calculator.ts';
import { Catalogue } from '../features/catalogue.ts';
import { CellExplanation } from '../features/cell-explanation.ts';
import {
  buttonCommand,
  CommandPalette,
  type CommandAction,
} from '../features/command-palette.ts';
import { DataRules } from '../features/data-rules.ts';
import { DatabaseSnapshots } from '../features/database-snapshots.ts';
import { ExportCenter } from '../features/export.ts';
import { FormulaInspector, parseFormula } from '../features/formula-inspector.ts';
import { SelectionInspector } from '../features/inspect.ts';
import { MultiSort } from '../features/multi-sort.ts';
import { NamedViews } from '../features/named-views.ts';
import { DatabasePanel } from '../features/online-database.ts';
import { Printing } from '../features/printing.ts';
import { ProfileCenter } from '../features/profile.ts';
import { RecentWorkbooks } from '../features/recent.ts';
import { Reconcile } from '../features/reconcile.ts';
import { RecordCard } from '../features/record-card.ts';
import { Relations } from '../features/relations.ts';
import { RowViews } from '../features/row-views.ts';
import { SearchPanel } from '../features/search-panel.ts';
import { SelectionQuick } from '../features/selection-quick.ts';
import { SelectionStats } from '../features/selection-stats.ts';
import { initStorageDialog } from '../features/storage.ts';
import { TaskRecovery } from '../features/task-recovery.ts';
import { initToolbarRibbon } from '../features/toolbar.ts';
import { initToolbarSettings } from '../features/toolbar-settings.ts';
import { Tracing } from '../features/tracing.ts';
import { Transpose } from '../features/transpose.ts';

import { getChartExportController } from '../analysis/chart-export.ts';
import { AnalyticsCharts } from '../analysis/charts.ts';
import { AnalysisNumbers, formatAnalysisNumber } from '../analysis/numbers.ts';
import type { AnalysisSourceMode, GridSourceContextData } from '../analysis/sources.ts';
import { AnalysisWorkspace } from '../analysis/workspace.ts';

import {
  exposeAnalyticsDiagnosticsBridge,
  exposePerformanceBridge,
  exposeSessionBridge,
  exposeWorkbookReadyBridge,
} from './runtime-bridge.ts';

const MAX_BROWSER_SCROLL_PIXELS = 15_000_000;

const WORKBOOK_COMMANDS: readonly string[] = [
  'copyMenuBtn',
  'selectionQuickBtn',
  'transposeOpenBtn',
];

const delay = (ms: number): Promise<void> =>
  new Promise((resolve) => {
    setTimeout(resolve, ms);
  });

function requireBound<T>(value: T | null, name: string): T {
  if (value === null) {
    throw new Error(`Controller "${name}" accessed before initialization`);
  }
  return value;
}

function bootstrap(): void {
  // ── Layer 1: Core services ────────────────────────────────────────────────
  const session = createBrowserSession();
  exposeSessionBridge(session);

  const activity = createActivityService();
  setActivityService(activity);

  const transport = createTransport({ session, activity });
  const { api } = transport;

  const menus = initOverlays();
  const fileFormats = requireFileFormats();
  configureClipboard(fileFormats.clipboard);

  const ribbon = initToolbarRibbon(undefined, menus);
  if (ribbon) {
    initToolbarSettings({ ribbon, session, menus });
  }

  // ── Layer 2: Shared workbook state & geometry primitives ──────────────────
  const S = createInitialWorkbookState(
    fileFormats.source('xlsx').maxRows,
    fileFormats.maxColumns,
  );
  const geometry = new Geometry(fileFormats.maxRows, fileFormats.maxColumns);
  const mergedCells = new MergedCells(fileFormats.maxRows, fileFormats.maxColumns);
  const virtualScroll = new VirtualScroll(MAX_BROWSER_SCROLL_PIXELS);
  const motion = new ScrollMotion();
  const tiles = new TileCache(VIEWPORT_CACHE_BUDGET, 32);

  const sourceRowLimit = (): number => fileFormats.source(S.source?.format).maxRows;
  const sourceColLimit = (): number => fileFormats.source(S.source?.format).maxColumns;
  const parseColumn = (text: string | number | null | undefined): number =>
    parseColumnAddress(text, sourceColLimit());
  const parseRef = (text: string | null | undefined) =>
    parseCellRef(text, sourceRowLimit(), sourceColLimit());

  const toastController = createToastController('toast', { durationMs: 5200, animate: true });
  const toast = (message: string, tone: ToastTone = 'info'): void => {
    toastController.show(message, tone);
  };

  const setStatus = (text: string): void => {
    const el = requireEl('globalStatus');
    el.textContent = text;
    el.hidden =
      /^已打开\s*·/.test(text) || (S.indexReady && /^等待选择文件$/.test(text));
  };

  // Explicit nullable slots for mutual controller wiring (zero non-null assertions)
  let sheetControllerRef: SheetController | null = null;
  let navRef: GridNavigation | null = null;
  let columnControllerRef: ColumnController | null = null;
  let frozenPanesRef: FrozenPanes | null = null;
  let picturesRef: Pictures | null = null;
  let gridControlsRef: GridControls | null = null;
  let workbookIORef: WorkbookIO | null = null;
  let gridInteractionsRef: GridInteractions | null = null;
  let editCopy: EditCopy | null = null;
  let analyticsRef: AnalysisWorkspace | null = null;
  let readingModeRef: ReadingMode | null = null;
  let highlightModeRef: HighlightMode | null = null;
  let recentManagerRef: RecentWorkbooks | null = null;
  let autoFilterUIRef: AutoFilterDropdown | null = null;
  let exportCenterRef: ExportCenter | null = null;
  let profileCenterRef: ProfileCenter | null = null;
  let bookmarkCenterRef: Bookmarks | null = null;
  let formulaInspectorRef: FormulaInspector | null = null;
  let multiSortRef: MultiSort | null = null;
  let selectionStatsRef: SelectionStats | null = null;
  let selectionInspectorRef: SelectionInspector | null = null;
  let archiveCenterRef: Archives | null = null;
  let calculatorRef: Calculator | null = null;
  let traceUIRef: Tracing | null = null;
  let reconcileCenterRef: Reconcile | null = null;
  let relationsCenterRef: Relations | null = null;
  let namedViewsRef: NamedViews | null = null;
  let rowViewsRef: RowViews | null = null;
  let copySpecialRef: CopySpecialController | null = null;
  let catalogueCenterRef: Catalogue | null = null;
  let dataRulesCenterRef: DataRules | null = null;
  let printingCenterRef: Printing | null = null;
  let recordCardCenterRef: RecordCard | null = null;
  let selectionQuickRef: SelectionQuick | null = null;
  let cellExplanationRef: CellExplanation | null = null;
  let taskRecoveryRef: TaskRecovery | null = null;
  let transposeCenterRef: Transpose | null = null;
  let searchPanelRef: SearchPanel | null = null;
  let commandPaletteRef: CommandPalette | null = null;
  let databasePanelRef: DatabasePanel | null = null;
  let databaseSnapshotsRef: DatabaseSnapshots | null = null;
  let workbookReady: Promise<void> = Promise.resolve();

  const getSheetController = (): SheetController =>
    requireBound(sheetControllerRef, 'sheetController');
  const getNav = (): GridNavigation => requireBound(navRef, 'nav');
  const getColumnController = (): ColumnController =>
    requireBound(columnControllerRef, 'columnController');
  const getFrozenPanes = (): FrozenPanes => requireBound(frozenPanesRef, 'frozenPanes');
  const getGridControls = (): GridControls => requireBound(gridControlsRef, 'gridControls');
  const getWorkbookIO = (): WorkbookIO => requireBound(workbookIORef, 'workbookIO');
  const getSearchPanel = (): SearchPanel => requireBound(searchPanelRef, 'searchPanel');
  const getExportCenter = (): ExportCenter => requireBound(exportCenterRef, 'exportCenter');

  const renderSourceIntegrity = (): void => {
    renderSourceIntegrityBanner(S.source);
  };

  const showWorkspace = (): void => {
    renderSourceIntegrity();
    document.body.classList.remove('home-view');
    requireEl('welcome').hidden = true;
    requireEl('workspace').hidden = false;
    for (const id of [
      'searchOpenBtn',
      'filterOpenBtn',
      'multiSortBtn',
      'inspectOpenBtn',
      'duplicateOpenBtn',
      'formulaViewBtn',
      'advancedFilterBtn',
      'freezeBtn',
      'formatBtn',
      'exportOpenBtn',
      'profileOpenBtn',
      'bookmarkOpenBtn',
      'columnsBtn',
      'rowsBtn',
      ...WORKBOOK_COMMANDS,
    ]) {
      requireButton(id).disabled = false;
    }
    editCopy?.availability(true);
  };

  const showWelcome = (): void => {
    if (editCopy?.grid.active) editCopy.grid.leave();
    editCopy?.availability(false);
    requireEl('sourceIntegrityNotice').hidden = true;
    picturesRef?.close();
    document.body.classList.add('home-view');
    requireEl('workspace').hidden = true;
    requireEl('welcome').hidden = false;
    requireEl('taskRecoveryHome').hidden = true;
    void taskRecoveryRef?.refreshHome();
    for (const id of [
      'searchOpenBtn',
      'filterOpenBtn',
      'textHeaderBtn',
      'multiSortBtn',
      'inspectOpenBtn',
      'duplicateOpenBtn',
      'formulaViewBtn',
      'advancedFilterBtn',
      'freezeBtn',
      'formatBtn',
      'exportOpenBtn',
      'profileOpenBtn',
      'bookmarkOpenBtn',
      'clearFilterBtn',
      'columnsBtn',
      'rowsBtn',
      ...WORKBOOK_COMMANDS,
    ]) {
      requireButton(id).disabled = true;
    }
    selectionInspectorRef?.close();
    columnControllerRef?.hideMenu();
  };

  const parseFormulaReferences = (formula: string, currentSheet: string): TraceRefEntry[] => {
    const refs = parseFormula(formula, currentSheet, 256);
    const unique = new Set<string>();
    const out: TraceRefEntry[] = [];
    for (const r of refs) {
      if (r.external || unique.has(r.key)) continue;
      unique.add(r.key);
      out.push({
        sheet: r.sheet,
        label: r.label,
        start: {
          row: r.start.row,
          col: r.start.col,
          ref: `${columnName(r.start.col)}${r.start.row}`,
        },
        end: {
          row: r.end.row,
          col: r.end.col,
          ref: `${columnName(r.end.col)}${r.end.row}`,
        },
        range: r.range,
      });
      if (out.length >= 100) break;
    }
    return out;
  };

  // ── Dynamic panel proxies (resolved after Layer 3 instantiation) ──────────
  const pipelinePanels: ViewportPipelinePanels = {
    get editCopy() {
      return editCopy;
    },
    get traceUI() {
      return traceUIRef;
    },
    get bookmarkCenter() {
      return bookmarkCenterRef;
    },
    get formulaInspector() {
      return formulaInspectorRef;
    },
    get selectionInspector() {
      return selectionInspectorRef;
    },
    get selectionQuick() {
      return selectionQuickRef;
    },
    get transposeCenter() {
      return transposeCenterRef;
    },
    get cellExplanation() {
      return cellExplanationRef;
    },
    get profileCenter() {
      return profileCenterRef;
    },
    get pictures() {
      return picturesRef;
    },
  };

  const sheetPanels: SheetControllerPanels = {
    get editCopy() {
      return editCopy;
    },
    get traceUI() {
      return traceUIRef;
    },
    get selectionStats() {
      return selectionStatsRef;
    },
    get formulaInspector() {
      return formulaInspectorRef;
    },
    get catalogueCenter() {
      return catalogueCenterRef;
    },
    get dataRulesCenter() {
      return dataRulesCenterRef;
    },
    get printingCenter() {
      return printingCenterRef;
    },
    get recordCardCenter() {
      return recordCardCenterRef;
    },
    get selectionQuick() {
      return selectionQuickRef;
    },
    get transposeCenter() {
      return transposeCenterRef;
    },
    get cellExplanation() {
      return cellExplanationRef;
    },
    get profileCenter() {
      return profileCenterRef;
    },
    get rowViews() {
      return rowViewsRef;
    },
    get searchPanel() {
      return getSearchPanel();
    },
    get exportCenter() {
      return getExportCenter();
    },
    get autoFilterUI() {
      return autoFilterUIRef;
    },
    get readingMode() {
      return readingModeRef;
    },
    get pictures() {
      return picturesRef;
    },
  };

  const navPanels: NavigationPanels = {
    get editCopy() {
      return editCopy;
    },
    get traceUI() {
      return traceUIRef;
    },
    get selectionStats() {
      return selectionStatsRef;
    },
    get formulaInspector() {
      return formulaInspectorRef;
    },
    get recordCardCenter() {
      return recordCardCenterRef;
    },
    get catalogueCenter() {
      return catalogueCenterRef;
    },
    get rowViews() {
      return rowViewsRef;
    },
    get calculator() {
      return calculatorRef;
    },
  };

  const workbookIOPanels: WorkbookIOPanels = {
    get archiveCenter() {
      return archiveCenterRef;
    },
    get bookmarkCenter() {
      return bookmarkCenterRef;
    },
    get calculator() {
      return calculatorRef;
    },
    get recentManager() {
      return recentManagerRef;
    },
    get editCopy() {
      return editCopy;
    },
    get frozenPanes() {
      return frozenPanesRef;
    },
    get searchPanel() {
      return searchPanelRef;
    },
    get traceUI() {
      return traceUIRef;
    },
    get formulaInspector() {
      return formulaInspectorRef;
    },
    get selectionInspector() {
      return selectionInspectorRef;
    },
    get analytics() {
      return analyticsRef;
    },
    get catalogueCenter() {
      return catalogueCenterRef;
    },
    get dataRulesCenter() {
      return dataRulesCenterRef;
    },
    get printingCenter() {
      return printingCenterRef;
    },
    get recordCardCenter() {
      return recordCardCenterRef;
    },
    get selectionQuick() {
      return selectionQuickRef;
    },
    get transposeCenter() {
      return transposeCenterRef;
    },
    get cellExplanation() {
      return cellExplanationRef;
    },
    get profileCenter() {
      return profileCenterRef;
    },
    get exportCenter() {
      return exportCenterRef;
    },
    get reconcileCenter() {
      return reconcileCenterRef;
    },
    get relationsCenter() {
      return relationsCenterRef;
    },
    get namedViews() {
      return namedViewsRef;
    },
    get rowViews() {
      return rowViewsRef;
    },
    get selectionStats() {
      return selectionStatsRef;
    },
  };

  const interactionPanels: GridInteractionPanels = {
    get editCopy() {
      return editCopy;
    },
    get calculator() {
      return calculatorRef;
    },
    get frozenPanes() {
      return getFrozenPanes();
    },
    get recentManager() {
      return requireBound(recentManagerRef, 'recentManager');
    },
    get profileCenter() {
      return requireBound(profileCenterRef, 'profileCenter');
    },
    get exportCenter() {
      return getExportCenter();
    },
    get copySpecial() {
      return requireBound(copySpecialRef, 'copySpecial');
    },
    get bookmarkCenter() {
      return requireBound(bookmarkCenterRef, 'bookmarkCenter');
    },
    get autoFilterUI() {
      return requireBound(autoFilterUIRef, 'autoFilterUI');
    },
    get traceUI() {
      return traceUIRef;
    },
    get selectionInspector() {
      return requireBound(selectionInspectorRef, 'selectionInspector');
    },
    get multiSort() {
      return requireBound(multiSortRef, 'multiSort');
    },
    get selectionStats() {
      return selectionStatsRef;
    },
    get readingMode() {
      return readingModeRef;
    },
    get highlightMode() {
      return highlightModeRef;
    },
    get dataRulesCenter() {
      return requireBound(dataRulesCenterRef, 'dataRulesCenter');
    },
    get recordCardCenter() {
      return recordCardCenterRef;
    },
    get printingCenter() {
      return printingCenterRef;
    },
    get selectionQuick() {
      return selectionQuickRef;
    },
    get transposeCenter() {
      return transposeCenterRef;
    },
    get catalogueCenter() {
      return requireBound(catalogueCenterRef, 'catalogueCenter');
    },
    get taskRecovery() {
      return requireBound(taskRecoveryRef, 'taskRecovery');
    },
    get searchPanel() {
      return getSearchPanel();
    },
    get commandPalette() {
      return requireBound(commandPaletteRef, 'commandPalette');
    },
    get columnController() {
      return getColumnController();
    },
    get databasePanel() {
      return requireBound(databasePanelRef, 'databasePanel');
    },
    get databaseSnapshots() {
      return requireBound(databaseSnapshotsRef, 'databaseSnapshots');
    },
    get snapshots() {
      return analyticsRef?.snapshots ?? null;
    },
  };

  // ── Layer 2: Grid & Viewport Controllers ──────────────────────────────────
  const pipeline = new ViewportPipeline({
    S,
    api,
    fileFormats,
    geometry,
    mergedCells,
    virtualScroll,
    motion,
    tiles,
    toast,
    colName: columnName,
    sourceRowLimit,
    sourceColLimit,
    getFrozenPanes,
    selectedColumnSpan: () => getColumnController().selectedSpan(),
    paintColumnSelection: () => getColumnController().paintSelection(),
    selectColumnSpan: (anchor, end) => getColumnController().selectSpan(anchor, end),
    captureColumnSelection: () => getColumnController().captureSelection(),
    crosshairCell: () => getNav().crosshairCell(),
    selectionHighlight: (areas) => getNav().selectionHighlight(areas),
    isPrecedent: (row, col) => getNav().isPrecedent(row, col),
    isSearchMatch: (sheet, ref) => getSearchPanel().isMatch(sheet, ref),
    updateSearchNavButtons: () => getSearchPanel().updateNavButtons(),
    syncFilteredSelection: (through) => getNav().syncFilteredSelection(through),
    syncTableColumnHeaders: () => getSheetController().syncTableColumnHeaders(),
    decorateTableColumnHeader: (head, col) =>
      getSheetController().decorateTableColumnHeader(head, col),
    finishSheetSwitch: (epoch, ready) => getSheetController().finishSheetSwitch(epoch, ready),
    startConditionalStats: (sheetName, epoch, options) =>
      getSheetController().startConditionalStats(sheetName, epoch, options),
    visibleReferenceAreas: (refs, filtered, pinnedRows, rowNumberByVisual, mergeAt) =>
      visibleReferenceAreas(refs, filtered, pinnedRows, rowNumberByVisual, mergeAt),
    panels: pipelinePanels,
  });

  const nav = new GridNavigation({
    S,
    api,
    fileFormats,
    mergedCells,
    toast,
    setStatus,
    colName: columnName,
    parseColumn,
    parseRef,
    sourceRowLimit,
    sourceColLimit,
    rowVisible: (row) => pipeline.rowVisible(row),
    nextVisibleRow: (row) => pipeline.nextVisibleRow(row),
    previousVisibleRow: (row) => pipeline.previousVisibleRow(row),
    advanceVisibleRow: (row, delta) => pipeline.advanceVisibleRow(row, delta),
    nextVisibleColumn: (from) => getColumnController().nextVisible(from),
    previousVisibleColumn: (from) => getColumnController().previousVisible(from),
    headerHeight: () => pipeline.headerHeight(),
    rowHead: () => pipeline.rowHeadWidth,
    sourceRowTop: (row) => pipeline.sourceRowTop(row),
    rowTop: (row) => pipeline.rowTop(row),
    rowHeight: (row) => pipeline.rowHeight(row),
    colLeft: (col) => pipeline.colLeft(col),
    colWidth: (col) => pipeline.colWidth(col),
    getVirtualScrollTop: () => pipeline.getVirtualScrollTop(),
    scrollTopToPhysical: (logical) => pipeline.scrollTopToPhysical(logical),
    activeMerge: (row, col) => pipeline.activeMerge(row, col),
    ensureExtent: (row, col) => pipeline.ensureExtent(row, col),
    getViewportRequest: () => pipeline.getViewportRequest(),
    windowContains: (request) => pipeline.windowContains(request),
    loadViewport: (force, expectedEpoch, strict) =>
      pipeline.loadViewport(force, expectedEpoch, strict),
    applySelectionClasses: () => pipeline.applySelectionClasses(),
    paintColumnSelection: () => getColumnController().paintSelection(),
    showWorkspace,
    selectSheet: (name, targetRef, preserveTrace, navigationGuard) =>
      getSheetController().selectSheet(name, targetRef, preserveTrace, navigationGuard),
    getFrozenPanes,
    panels: navPanels,
  });
  navRef = nav;

  const sheetController = new SheetController({
    S,
    api,
    fileFormats,
    geometry,
    mergedCells,
    tiles,
    toast,
    setStatus,
    colName: columnName,
    parseColumn,
    parseRef,
    sourceRowLimit,
    sourceColLimit,
    fontExists: isKnownFont,
    setDisplayFontSelection: (font) => pipeline.setDisplayFontSelection(font),
    rowHeight: (row) => pipeline.rowHeight(row),
    colWidth: (col) => pipeline.colWidth(col),
    headerHeight: () => pipeline.headerHeight(),
    rowHead: () => pipeline.rowHeadWidth,
    sourceRowTop: (row) => pipeline.sourceRowTop(row),
    rowTop: (row) => pipeline.rowTop(row),
    rowTotal: (count) => pipeline.rowTotal(count),
    colTotal: (count) => pipeline.colTotal(count),
    rowAtOffset: (y) => pipeline.rowAtOffset(y),
    rowVisible: (row) => pipeline.rowVisible(row),
    nextVisibleRow: (row) => pipeline.nextVisibleRow(row),
    previousVisibleRow: (row) => pipeline.previousVisibleRow(row),
    filteredBodyCount: () => pipeline.filteredBodyCount(),
    getVirtualScrollTop: () => pipeline.getVirtualScrollTop(),
    setVirtualScrollTop: (logical) => pipeline.setVirtualScrollTop(logical),
    updateGeometryBase: () => pipeline.updateGeometryBase(),
    updateCanvasSize: () => pipeline.updateCanvasSize(),
    resetGridDOM: () => pipeline.resetGridDOM(),
    renderGridFromCurrentWindow: () => pipeline.renderGridFromCurrentWindow(),
    invalidateViewport: (options) => pipeline.invalidateViewport(options),
    loadViewport: (force, expectedEpoch, strict) =>
      pipeline.loadViewport(force, expectedEpoch, strict),
    viewportKey: (request) => pipeline.viewportKey(request),
    paintInitialPreview: (name, epoch) => pipeline.paintInitialPreview(name, epoch),
    paintRangeFrames: (areas) => pipeline.paintRangeFrames(areas),
    applyColumnVisibility: () => getColumnController().applyVisibility(),
    openDialog: (id) => getGridControls().openDialog(id),
    closeDialog: (id) => getGridControls().closeDialog(id),
    showWorkspace,
    getCell: (sheet, ref) => nav.getCell(sheet, ref),
    cellAt: (row, col) => nav.cellAt(row, col),
    selectCell: (cell, rowNumber, colNumber, ref, visualIndex) =>
      nav.selectCell(cell, rowNumber, colNumber, ref, visualIndex),
    selectInitialCell: () => nav.selectInitialCell(),
    navigateToCell: (sheet, ref, preserveTrace, guard) =>
      nav.navigateToCell(sheet, ref, preserveTrace, guard),
    getFrozenPanes,
    getWorkbookIO,
    panels: sheetPanels,
  });
  sheetControllerRef = sheetController;

  const columnController = new ColumnController({
    state: S,
    api,
    toast,
    geometry,
    baseRow: BASE_ROW,
    baseCol: BASE_COL,
    colName: columnName,
    parseColumn,
    colLimit: sourceColLimit,
    relayoutGrid: () => {
      pipeline.resetGridDOM();
      pipeline.updateCanvasSize();
      pipeline.invalidateViewport();
      pipeline.scheduleViewport(true);
      getFrozenPanes().schedule(true);
    },
    selectCellByAddress: (ref, visualIndex) => nav.selectCellByAddress(ref, visualIndex),
    hideCellMenu: () => getGridControls().hideCellMenu(),
    closeColumnsMenu: () => {
      if (byId('columnsMenu')?.hidden === false) menus?.close();
    },
    selectionStats: () => selectionStatsRef,
    calculator: () => calculatorRef,
  });
  columnControllerRef = columnController;

  const gridClipboard = new GridClipboard({
    S,
    api,
    colName: columnName,
    fileFormats,
    mergedCells,
    sourceRowLimit,
    toast,
    getEditCopy: () => editCopy,
    getFrozenPanes,
    getExportCenter: () => exportCenterRef,
    encodePasteMarkup,
  });

  const gridControls = new GridControls({
    S,
    icon,
    toast,
    colName: columnName,
    parseRef,
    cellAt: (row, col) => nav.cellAt(row, col),
    inSelectedRange: (row, col, visualIndex) => pipeline.inSelectedRange(row, col, visualIndex),
    linkDescription,
    selectedColumnSpan: () => columnController.selectedSpan(),
    selectColumnSpan: (anchor, end) => columnController.selectSpan(anchor, end),
    hasColumnOfType: (span, hidden) => columnController.hasOfType(span, hidden),
    selectPointedCell: (cell, point, event) =>
      requireBound(gridInteractionsRef, 'gridInteractions').selectPointedCell(cell, point, event),
    rowHeight: (row) => pipeline.rowHeight(row),
    colWidth: (col) => pipeline.colWidth(col),
    headerHeight: () => pipeline.headerHeight(),
    rowTop: (row) => pipeline.rowTop(row),
    colLeft: (col) => pipeline.colLeft(col),
    rowHead: () => pipeline.rowHeadWidth,
    resetGridDOM: () => pipeline.resetGridDOM(),
    updateCanvasSize: () => pipeline.updateCanvasSize(),
    renderInitialPreview: () => pipeline.renderInitialPreview(),
    paintRangeFrames: () => pipeline.paintRangeFrames(),
    invalidateViewport: (options) => pipeline.invalidateViewport(options),
    setVirtualScrollTop: (logical) => pipeline.setVirtualScrollTop(logical),
    scheduleViewport: (force) => pipeline.scheduleViewport(force),
    getEditCopy: () => editCopy,
    getPictures: () => picturesRef,
    getFrozenPanes,
    isDragging: () => gridInteractionsRef?.isDragging() ?? false,
  });
  gridControlsRef = gridControls;

  const workbookIO = new WorkbookIO({
    S,
    api,
    transport,
    session,
    getWorkbookReady: () => workbookReady,
    toast,
    fmtBytes: formatBytes,
    fileFormats,
    setStatus,
    showWelcome,
    showWorkspace,
    loadStylePalette: () => pipeline.loadStylePalette(),
    selectSheet: (name, targetRef, preserveTrace, navigationGuard, openingActivity) =>
      sheetController.selectSheet(
        name,
        targetRef,
        preserveTrace,
        navigationGuard,
        openingActivity,
      ),
    selectSheetTab: (name) => sheetController.selectSheetTab(name),
    refreshSearchHistory: () => getSearchPanel().refreshHistory(),
    invalidateViewport: (options) => pipeline.invalidateViewport(options),
    cancelFilter: (jobID) => sheetController.cancelFilter(jobID),
    cancelConditionalStats: (sendCancel, showCancelled) =>
      sheetController.cancelConditionalStats(sendCancel, showCancelled),
    panels: workbookIOPanels,
  });
  workbookIORef = workbookIO;

  const frozenPanes = new FrozenPanes({
    state: S,
    grid: requireEl('grid'),
    get rowHead() {
      return pipeline.rowHeadWidth;
    },
    api,
    getRequest: () => pipeline.getViewportRequest(),
    rowHeight: (row) => pipeline.rowHeight(row),
    colWidth: (col) => pipeline.colWidth(col),
    rowTop: (row) => pipeline.rowTop(row),
    colLeft: (col) => pipeline.colLeft(col),
    headerHeight: () => pipeline.headerHeight(),
    colName: columnName,
    bindResizeGrip: (grip, kind, index) => pipeline.bindResizeGrip(grip, kind, index),
    sourceRowTop: (row) => pipeline.sourceRowTop(row),
    sourceRowHeight: (row) => pipeline.sourceRowHeight(row),
    getVirtualScrollTop: () => pipeline.getVirtualScrollTop(),
    updateSharedBorders: (
      el,
      cell,
      row,
      rightCell,
      rightRow,
      belowCell,
      belowRow,
      context,
    ) =>
      pipeline.updateSharedBorders(
        el,
        cell,
        row,
        rightCell,
        rightRow,
        belowCell,
        belowRow,
        context,
      ),
    isPrecedent: (row, col) => nav.isPrecedent(row, col),
    isSearchMatch: (sheet, ref) => getSearchPanel().isMatch(sheet, ref),
    applyCellStyle: (node, cell) => pipeline.applyCellStyle(node, cell),
    onRender: () => {
      pipeline.applySelectionClasses();
      traceUIRef?.refreshPulse();
      editCopy?.grid.schedulePosition();
    },
    decorateFormulaRef: (element, row, col, sheet) => {
      formulaInspectorRef?.decorateCell(element, row, col, sheet);
      selectionInspectorRef?.decorateCell(element, element.dataset.ref ?? '', sheet);
    },
    beforePaint: (rows) => pipeline.frozenPresenter.prepare(rows),
    paintCell: (el, cell, row) => pipeline.frozenPresenter.paint(el, cell, row),
    extendTextIntoBlanks: (el, cell, values, layouts, position) =>
      pipeline.frozenPresenter.overflow(el, cell, values, layouts, position),
    projectCell: (ref, cell) =>
      editCopy?.grid.active
        ? (editCopy.grid.project(S.sheet ?? '', ref, cell) ?? undefined)
        : cell,
    cachedPart: (name, params, base) => {
      if (name !== 'left' && base.filtered) return null;
      const first = Number(params.get('col'));
      const count = Number(params.get('cols'));
      const rawColumns = params.get('columns');
      const columns = rawColumns
        ? rawColumns.split(',').map(Number)
        : Array.from({ length: count }, (_, i) => first + i);
      const startRow = base.r1 ?? 1;
      const visuals =
        name === 'left'
          ? Array.from({ length: base.rowCount ? base.limit : 0 }, (_, i) => {
              const ordered = base.rowOrder?.at(startRow + i - 1);
              return ordered || startRow + i;
            })
          : Array.from({ length: Number(params.get('limit')) }, (_, i) => i + 1).filter((r) =>
              pipeline.rowVisible(r),
            );
      const band = {
        ...base,
        displayStart: visuals[0] ?? 1,
        displayEnd: visuals.at(-1) ?? 0,
        startCol: first,
        cols: count,
        selectedCols: columns,
      };
      return tiles.project(band, visuals)?.data ?? null;
    },
    estimateBytes: estimateViewportBytes,
    cacheBudget: VIEWPORT_CACHE_BUDGET / 4,
    decorateMergedCell: (cell, row, col) =>
      pipeline.decorateMergedCell(cell, row, col, nav.selectionHighlight()),
    decorateColumnHeader: (head, col) => sheetController.decorateTableColumnHeader(head, col),
    isBookmarked: (sheet, ref) => Boolean(bookmarkCenterRef?.has(sheet, ref)),
    onError: (error) => toast(`冻结窗格读取失败：${error.message}`, 'error'),
  });
  frozenPanesRef = frozenPanes;

  const pictures = new Pictures({
    state: S,
    grid: requireEl('grid'),
    api,
    toast,
    get rowHead() {
      return pipeline.rowHeadWidth;
    },
    scale: () => pipeline.scale(),
    rowTop: (row) => pipeline.rowTop(row),
    colLeft: (col) => pipeline.colLeft(col),
    headerHeight: () => pipeline.headerHeight(),
    frozen: () => frozenPanes.effective(),
  });
  picturesRef = pictures;

  document.addEventListener('excellens:readingchange', () => pipeline.scheduleStylePaint());
  const readingMode = new ReadingMode({
    button: requireButton('readingModeBtn'),
    panel: requireEl('readingModePanel'),
    veil: requireEl('readingVeil'),
    textureToggle: requireInput('gridTextureToggle'),
    onTextureChange: () => sheetController.syncGridBackdrop(),
  });
  readingModeRef = readingMode;

  const highlightMode = new HighlightMode({
    toggle: requireInput('crosshairToggle'),
    button: requireButton('crosshairColorBtn'),
    panel: requireEl('crosshairPanel'),
    onChange: () => pipeline.applySelectionClasses(),
  });
  highlightModeRef = highlightMode;

  // ── Layer 3: Editing, Features & Analysis ─────────────────────────────────
  const recentManager = new RecentWorkbooks({
    api,
    toast,
    open: (item) =>
      workbookIO.openWorkbook(
        () =>
          api<WorkbookStatusResponse>('/api/recent', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ action: 'open', id: item.id }),
          }),
        item.name,
        item.fileSize,
      ),
  });
  recentManagerRef = recentManager;

  if (byId('editCopyDialog')) {
    editCopy = new EditCopy({
      api,
      openDialog: (id) => gridControls.openDialog(id),
      adopt: (data, options) => workbookIO.acceptOpenedWorkbook(data, options),
      canOpen: (action) => workbookIO.canOpenWorkbook(action),
      context: () => ({
        book: S.book,
        fileName: S.fileName,
        source: S.source,
        sheet: S.sheet,
        ref: S.selected?.ref,
      }),
      grid: {
        selected: () => S.selected,
        ready: () => S.indexReady && !S.sheetLoadingEpoch,
        filtered: () => S.filtered,
        mappedRows: () => S.filtered || Boolean(S.rowVisibility?.hidden.length),
        clipboardIdentity: () => gridClipboard.clipboardIdentity(),
        paintReferences: () => pipeline.paintRangeFrames(),
        selectArea: (a) => {
          nav.selectCellByAddress(`${columnName(a.col1)}${a.row1}`, a.row1);
          nav.extendRange(
            { row: a.row1, col: a.col1, visual: a.row1 },
            a.row2,
            a.col2,
            a.row2,
          );
        },
        captureOrigin: () => ({
          range: S.range ? { ...S.range } : null,
          multi: S.multiRanges.slice(),
          anchor: S.rangeAnchor ? { ...S.rangeAnchor } : null,
          filter: S.filterId,
          view: S.rowView?.id,
        }),
        restoreOrigin: async (d) => {
          const ok = await sheetController.selectSheet(
            d.address.sheet,
            d.ref,
            false,
            null,
            null,
            d,
          );
          if (!ok || editCopy?.grid.draft !== d) return false;
          const origin = d.origin;
          if (origin.filter === S.filterId && origin.view === S.rowView?.id) {
            S.range = origin.range;
            S.multiRanges = origin.multi.slice();
            S.rangeAnchor = origin.anchor;
            pipeline.applySelectionClasses();
            selectionStatsRef?.schedule();
          }
          return true;
        },
        dragPoint: (x, y, kind, scroll) =>
          computeReferenceDragPoint(
            x,
            y,
            kind,
            requireEl('grid'),
            pipeline.headerHeight(),
            pipeline.rowHeadWidth,
            (dy) => {
              const top = pipeline.getVirtualScrollTop();
              pipeline.setVirtualScrollTop(top + dy);
              return pipeline.getVirtualScrollTop() !== top;
            },
            scroll,
          ),
        referenceStep: (p, key) => {
          const pinned = frozenPanes.effective();
          const delta: readonly [number, number] =
            key === 'ArrowLeft'
              ? [0, -1]
              : key === 'ArrowRight'
                ? [0, 1]
                : key === 'ArrowUp'
                  ? [-1, 0]
                  : [1, 0];
          const point = nav.adjacentGridPoint(
            p.row,
            p.visual || S.selectedVisualIndex || p.row,
            p.col,
            delta[0],
            delta[1],
            pinned,
          );
          nav.scrollCellIntoView(point.row, point.col, point.visual, pinned);
          if (point.row) return point;
          const epoch = S.sheetEpoch;
          const filter = S.filterId;
          const view = S.rowView?.id;
          return pipeline.loadViewport(true).then(() => {
            if (
              epoch !== S.sheetEpoch ||
              filter !== S.filterId ||
              view !== S.rowView?.id ||
              S.window?.excludeThroughRow !== pinned.rows
            ) {
              return null;
            }
            const row = S.rowNumberByVisual.get(point.visual);
            return row ? { ...point, row } : null;
          });
        },
        dragged: () => gridInteractionsRef?.suppressGridClick ?? false,
        selectPoint: (node, event) => {
          const p = parseRef(node.dataset.ref);
          if (p) {
            requireBound(gridInteractionsRef, 'gridInteractions').selectWithModifiers(
              nav.cellAt(p.row, p.col),
              { ...p, visual: Number(node.dataset.visualIndex) || p.row },
              event,
            );
          }
        },
        range: () => {
          if (!S.selected || S.multiRanges.length || S.filtered) return '';
          const lastArea = MultiSelection.areas(S, true).at(-1);
          if (!lastArea) return '';
          const area = mergedCells.expand(lastArea);
          const { start, end } = MultiSelection.points(area, columnName);
          return start === end ? start : `${start}:${end}`;
        },
        merged: () => (S.selected ? mergedCells.at(S.selected.row, S.selected.col) : null),
        areas: () => MultiSelection.areas(S, true),
        expand: (area) => mergedCells.expand(area),
        mergeAt: (row, col) => mergedCells.at(row, col),
        nextRow: (row) => pipeline.nextVisibleRow(row),
        previousRow: (row) => pipeline.previousVisibleRow(row),
        nextCol: (col) => columnController.nextVisible(col),
        previousCol: (col) => columnController.previousVisible(col),
        moveInside: async (row, col) => {
          const pinned = frozenPanes.effective();
          const current = nav.beginGridNavigation(pinned.rows);
          const range = S.range;
          nav.scrollCellIntoView(row, col, row, pinned);
          if (!S.window || !pipeline.windowContains(pipeline.getViewportRequest())) {
            await pipeline.loadViewport(true);
          }
          if (current() && range === S.range) {
            nav.selectCell(
              nav.cellAt(row, col),
              row,
              col,
              `${columnName(col)}${row}`,
              row,
              true,
              true,
            );
          }
        },
        switchSheet: async (name) => {
          await sheetController.selectSheetTab(name);
        },
        pasteRows: async (count) => {
          const limits = fileFormats.editing;
          const snapshot = gridClipboard.captureClipboardSelection({
            purpose: '粘贴',
            limits: {
              rows: limits.batchCells,
              columns: limits.batchCells,
              cells: limits.batchCells,
            },
          });
          if (snapshot.rows > 1) {
            const data = await snapshot.read({ richText: false, conditional: false });
            if (!snapshot.current()) throw new Error('选区已变化，请重新粘贴');
            return data.rows.map((r) => r.row);
          }
          const through = frozenPanes.effective().rows;
          const selected = S.selected;
          if (!selected) throw new Error('请先选择目标单元格');
          const first = selected.row;
          const rows: number[] = [];
          if (!S.filtered) {
            const visibility = S.rowVisibility;
            if (!visibility) {
              for (let row = first; row <= sourceRowLimit() && rows.length < count; row++) {
                rows.push(row);
              }
              return rows;
            }
            for (let row = first; row && rows.length < count; row = visibility.next(row + 1)) {
              rows.push(row);
            }
            return rows;
          }
          let offset =
            first <= through
              ? 0
              : (S.selectedVisualIndex || (await nav.locateFilteredVisual(first, through))) - 1;
          if (offset < 0) throw new Error('当前单元格已离开筛选结果');
          if (first <= through) {
            for (
              let row = first;
              row && row <= through && rows.length < count;
              row = pipeline.nextVisibleRow(row + 1)
            ) {
              rows.push(row);
            }
          }
          while (rows.length < count) {
            if (!snapshot.current()) throw new Error('选区已变化，请重新粘贴');
            const size = Math.min(MAX_ROWS_PER_REQUEST, count - rows.length);
            const params = new URLSearchParams({
              sheet: S.sheet ?? '',
              filterId: S.filterId,
              excludeThroughRow: String(through),
              offset: String(offset),
              limit: String(size),
              col: String(selected.col),
              cols: '1',
              compact: '1',
              rowWindow: '1',
            });
            if (S.rowView) params.set('viewId', S.rowView.id);
            const data = await api<SheetViewportResponse>(`/api/sheet?${params.toString()}`);
            if (!snapshot.current()) throw new Error('选区已变化，请重新粘贴');
            rows.push(...data.rows.map((row) => row.number));
            if (data.rows.length < size) break;
            offset += size;
          }
          return rows;
        },
        visibleSelection: () => {
          const cells = fileFormats.editing.batchCells;
          return gridClipboard.captureClipboardSelection({
            purpose: '编辑',
            limits: { rows: cells, columns: cells, cells },
          });
        },
        move: (row, col, extend) => nav.moveSelection(row, col, extend, true),
        select: (ref, visual, keep) => nav.selectCellByAddress(ref, visual, keep),
        copy: (plain, cut) => gridClipboard.copySelection(plain, cut),
        positionMenu: (menu, x, y) => gridControls.positionCellMenu(menu, x, y),
        bounds: (ref) => {
          for (const node of document.querySelectorAll<HTMLElement>(
            `.grid-region .grid-cell[data-ref="${ref}"]`,
          )) {
            const rect = pipeline.traceVisibleRect(node, true);
            if (rect) return { rect, node };
          }
          return null;
        },
        redraw: () => {
          ++S.decorVersion;
          pipeline.renderGridFromCurrentWindow();
          frozenPanes.schedule(true);
          if (S.selected) nav.selectCellByAddress(S.selected.ref, S.selectedVisualIndex, true);
        },
      },
      limits: fileFormats.editing,
      colName: columnName,
      fmtBytes: formatBytes,
      session,
    });
  }

  const searchPanel = new SearchPanel({
    state: S,
    api,
    toast,
    delay,
    copyText: (val, msg) => gridClipboard.copyText(val, msg),
    navigateToCell: (sheet, ref) => nav.navigateToCell(sheet, ref),
    runSearch: (sheet) => sheetController.runSearch(sheet),
    cancelSearch: (id, updateUI) => sheetController.cancelSearch(id, updateUI),
  });
  searchPanelRef = searchPanel;

  const rowViews = new RowViews({
    state: S,
    api,
    toast,
    maximum: sourceRowLimit,
    onBusyChange: () => sheetController.refreshReadControls(),
    commit: async (data, current) => {
      await sheetController.commitRowView(data, current);
    },
    navigate: (sheet, ref) => nav.navigateToCell(sheet, ref),
    reload: () => sheetController.selectSheet(S.sheet ?? ''),
    openDialog: (id) => gridControls.openDialog(id),
  });
  rowViewsRef = rowViews;

  // Bounded read-only grid-to-analysis context provider
  const gridSourceContext = (mode: Exclude<AnalysisSourceMode, 'full'>): GridSourceContextData => {
    if (
      !S.opened ||
      !S.sheet ||
      !S.indexReady ||
      S.opening ||
      S.sheetLoadingEpoch ||
      S.filterJob ||
      rowViews.busy ||
      S.rowViewFailure
    ) {
      throw new Error('请先完成工作表加载、筛选或行显隐更新');
    }
    if (mode === 'filtered' && (!S.filtered || !S.filterId)) {
      throw new Error('当前工作表没有已完成的筛选或排序');
    }
    if (mode === 'visible' && !S.rowView?.id) {
      throw new Error('当前可见行尚未准备好');
    }
    const base = {
      workbookKey: S.workbookKey,
      book: S.book,
      source: `wb:${S.sheet}`,
      filterId: S.filtered ? S.filterId : '',
      viewId: S.rowView?.id ?? '',
      excludeThroughRow: frozenPanes.effective().rows,
    };
    if (mode === 'selection') {
      const areas = MultiSelection.areas(S, true);
      if (areas.length !== 1) throw new Error('请在工作簿中保留一个连续选区');
      const only = areas[0];
      if (!only) throw new Error('请在工作簿中保留一个连续选区');
      const pts = MultiSelection.points(only, columnName);
      return { ...base, start: pts.start, end: pts.end };
    }
    return base;
  };

  const calculator = new Calculator({
    state: S,
    api,
    toast,
    grid: requireEl('grid'),
    copy: (val, msg) => gridClipboard.copyText(val, msg),
    session,
  });
  calculatorRef = calculator;

  const selectionStats = new SelectionStats({
    state: S,
    api,
    colName: columnName,
    toast,
    profile: () => requireBound(profileCenterRef, 'profileCenter').open(),
    excludeThroughRow: () => frozenPanes.effective().rows,
    editBinding: () => {
      if (!editCopy?.grid.active || !editCopy.data?.document) return undefined;
      const { id, binding, revision } = editCopy.data.document;
      return { document: { id, binding }, revision: Number(revision), book: S.book };
    },
  });
  selectionStatsRef = selectionStats;

  const multiSort = new MultiSort({
    state: S,
    parseColumn,
    colName: columnName,
    toast,
    openDialog: (id) => gridControls.openDialog(id),
    submit: async (keys, header) => {
      const rules = sheetController.currentQueryRules();
      await sheetController.submitFilterRules(rules, {
        headerRow: header,
        sortKeys: keys,
        recoverable: requireInput('sortRecoverable').checked,
      });
    },
    clear: async () => {
      if (S.autoFilter) {
        S.autoFilter.sortColumn = 0;
        S.autoFilter.descending = false;
        await sheetController.applyAutoFilter({ preserveRules: true });
      } else if ((S.lastRulesBySheet.get(S.sheet ?? '') ?? []).length) {
        await sheetController.submitFilterRules(S.lastRulesBySheet.get(S.sheet ?? '') ?? [], {
          headerRow: 0,
        });
      } else if (S.filterId) {
        await sheetController.clearFilter();
      }
    },
  });
  multiSortRef = multiSort;

  const selectionInspector = new SelectionInspector({
    state: S,
    api,
    colName: columnName,
    parseColumn,
    toast,
    navigate: (sheet, ref) => nav.navigateToCell(sheet, ref),
    copy: (val) => gridClipboard.copyText(val),
    rowLimit: sourceRowLimit,
    colLimit: sourceColLimit,
    largeDataRows: fileFormats.largeDataRows,
    excludeThroughRow: () => frozenPanes.effective().rows,
    onChange: () => {
      ++S.decorVersion;
      pipeline.renderGridFromCurrentWindow();
      for (const node of document.querySelectorAll<HTMLElement>('.grid-region .pinned-cell')) {
        selectionInspector.decorateCell(node, node.dataset.ref ?? '', S.sheet ?? '');
      }
    },
  });
  selectionInspectorRef = selectionInspector;

  const formulaInspector = new FormulaInspector({
    element: requireEl('formulaText'),
    tooltip: requireEl('formulaRefTooltip'),
    grid: requireEl('grid'),
    state: S,
    api,
    getCell: (sheet, ref) => nav.getCell(sheet, ref),
    navigate: (sheet, ref, preserveTrace) => nav.navigateToCell(sheet, ref, preserveTrace),
    notify: toast,
    copy: (val) => gridClipboard.copyText(val),
    onLayoutChange: () => {
      if (S.indexReady) {
        pipeline.scheduleViewport(true);
        frozenPanes.schedule(true);
      }
    },
    sheetExists: (sheet) =>
      S.sheets.some((s) => s.name.toLocaleLowerCase() === sheet.toLocaleLowerCase()),
  });
  formulaInspectorRef = formulaInspector;

  const traceUI = new Tracing({
    state: S,
    api,
    panel: requireDialog('traceDialog'),
    dock: requireEl('traceDock'),
    layer: requireEl('traceArrival'),
    grid: requireEl('grid'),
    title: requireEl('traceTitle'),
    formula: requireEl('traceFormula'),
    summary: requireEl('traceSummary'),
    list: requireEl('traceList'),
    scopeRow: requireEl('traceScopeRow'),
    scope: requireSelect('traceScope'),
    cancel: requireButton('traceCancelBtn'),
    minimize: requireButton('traceMinimizeBtn'),
    restore: requireButton('traceRestoreBtn'),
    origin: requireButton('traceOriginBtn'),
    end: requireButton('traceEndBtn'),
    parse: parseFormulaReferences,
    parseRef,
    getCell: (sheet, ref) => nav.getCell(sheet, ref),
    notify: toast,
    readScope: () => requireSelect('searchScope').value,
    writeScope: (value) => {
      requireSelect('searchScope').value = value;
    },
    navigate: (sheet, ref, guard) => nav.navigateToCell(sheet, ref, true, guard),
    atTarget: (ref) => {
      const p = pipeline.activeMerge(ref.start.row, ref.start.col) ?? ref.start;
      return (
        S.indexReady &&
        S.selected?.sheet === ref.sheet &&
        S.selected.row === p.row &&
        S.selected.col === p.col
      );
    },
    bounds: (row, col) => pipeline.activeMerge(row, col) ?? { row, col, endRow: row, endCol: col },
    visibleRect: (node) => pipeline.traceVisibleRect(node),
    setRefs: (refs) => {
      S.traceRefs = refs;
      ++S.decorVersion;
      for (const node of document.querySelectorAll<HTMLElement>(
        '.grid-region .grid-cell[data-row][data-col]',
      )) {
        node.classList.toggle(
          'precedent',
          nav.isPrecedent(Number(node.dataset.row), Number(node.dataset.col)),
        );
      }
    },
  });
  traceUIRef = traceUI;

  const archiveCenter = new Archives({
    api,
    toast,
    open: (data) => workbookIO.acceptOpenedWorkbook(data),
    fmtBytes: formatBytes,
    canOpen: () => workbookIO.canOpenWorkbook(),
  });
  archiveCenterRef = archiveCenter;

  const autoFilterUI = new AutoFilterDropdown({
    state: S,
    api,
    colName: columnName,
    toast,
    onApply: (options) => sheetController.applyAutoFilter(options),
  });
  autoFilterUIRef = autoFilterUI;

  const exportCenter = new ExportCenter({
    state: S,
    api,
    toast,
    colName: columnName,
    openDialog: (id) => gridControls.openDialog(id),
    excludeThroughRow: () => frozenPanes.effective().rows,
    sourceCapabilities: () => fileFormats.source(S.source?.format),
  });
  exportCenterRef = exportCenter;

  const taskRecovery = new TaskRecovery({
    state: S,
    api,
    toast,
    openDialog: (id) => gridControls.openDialog(id),
    maximum: fileFormats.taskRecovery.tasks,
    openSource: async (payload, current) => {
      await workbookIO.openTaskSource(payload, current);
    },
    adopt: (start, item) => sheetController.adoptRecoveredTask(start, item),
    context: () =>
      [
        S.workbookKey,
        S.book,
        S.sheetEpoch,
        S.searchRequestID,
        S.filterRequest,
        exportCenter.serial,
      ].join('|'),
    canResume: (item, action) => {
      if (action === 'open') return !S.opening && workbookIO.canOpenWorkbook();
      if (S.opening || S.sheetLoadingEpoch) {
        toast('请先完成文件加载', 'warning');
        return false;
      }
      if (
        item.kind === 'export'
          ? exportCenter.running || exportCenter.busy
          : Boolean(S.searchJob || S.filterJob)
      ) {
        toast('请先完成或取消当前任务', 'warning');
        return false;
      }
      return true;
    },
  });
  taskRecoveryRef = taskRecovery;

  requireEl('taskRecoveryHome').addEventListener('click', () => {
    void taskRecovery.open();
  });
  requireEl('recentRefreshBtn').addEventListener('click', () => {
    void taskRecovery.refreshHome();
  });
  window.addEventListener('focus', () => {
    void taskRecovery.refreshHome();
  });

  const copySpecial = new CopySpecialController({
    capture: () => gridClipboard.captureClipboardSelection({ withMerges: true }),
    copy: (content) =>
      content.html
        ? gridClipboard.writeClipboardPayload({ text: content.text, html: content.html })
        : gridClipboard.copyText(content.text),
    toast,
    limits: fileFormats.clipboard,
  });
  copySpecialRef = copySpecial;

  const transposeCenter = new Transpose({
    capture: (opts) => gridClipboard.captureClipboardSelection(opts),
    copy: (val) => gridClipboard.copyText(val),
    toast,
    navigate: (sheet, ref, guard) => nav.navigateToCell(sheet, ref, false, guard),
    colName: columnName,
    fontStack,
    limits: fileFormats.transpose,
    richLimits: fileFormats.richText,
  });
  transposeCenterRef = transposeCenter;

  const analytics = new AnalysisWorkspace(requireEl('analysisRoot'), {
    api,
    post: transport.post,
    session,
    activity,
    formats: fileFormats,
    getSourceContext: gridSourceContext,
    exportIssues: (jobId, protectCSV) => {
      void exportCenter.startGenerated(
        '/api/analysis/issues/export',
        { jobId, protectCSV },
        '异常核对清单（完整）',
      );
    },
    exportClusters: (jobId, protectCSV) => {
      void exportCenter.startGenerated(
        '/api/analysis/clusters/export',
        { jobId, protectCSV },
        '疑似重复候选（完整计数、有限位置样例）',
      );
    },
    onNavigate: (sheet, ref) => {
      nav.navigateToCell(sheet, ref).catch((err: unknown) => toast(errorMessage(err), 'error'));
    },
    workbookReady: new Promise<void>((resolve) => {
      queueMicrotask(() => {
        void workbookReady.then(resolve);
      });
    }),
  });
  analyticsRef = analytics;

  exposeAnalyticsDiagnosticsBridge({
    diagnostics: () => analytics.diagnostics(),
    refresh: () => {
      void analytics.refresh();
    },
  });

  const reconcileCenter = new Reconcile({
    state: S,
    api,
    toast,
    colName: columnName,
    navigate: (sheet, ref) => nav.navigateToCell(sheet, ref),
    navigateAnalysis: (sheet, row, col) => analytics.navigateSheet(sheet, row, col),
    closeAnalysis: () => analytics.close(),
    onExport: (jobId, protectCSV, label, options = {}) =>
      exportCenter.startGenerated('/api/reconcile/export', { jobId, protectCSV, ...options }, label),
    onAuditExport: (path, payload, label) => exportCenter.startGenerated(path, payload, label),
    canRun: (label) => workbookIO.canOpenWorkbook(label),
    recordBytes: fileFormats.reconcileRecordBytes,
  });
  reconcileCenterRef = reconcileCenter;

  const relationsCenter = new Relations({
    api,
    toast,
    formats: fileFormats,
    fieldLimits: fileFormats.composition,
    navigate: (sheet, ref) => nav.navigateToCell(sheet, ref),
    navigateAnalysis: (sheet, row, col) => analytics.navigateSheet(sheet, row, col),
    closeAnalysis: () => analytics.close(),
    canRun: () => workbookIO.canOpenWorkbook(),
    context: () => ({
      bookKey: S.workbookKey,
      book: S.book,
      opened: S.opened,
      source: `wb:${S.sheet ?? ''}`,
    }),
    onExport: (payload, name) =>
      exportCenter.startGenerated('/api/relation/export', { ...payload }, name),
  });
  relationsCenterRef = relationsCenter;

  const namedViews = new NamedViews({
    state: S,
    api,
    toast,
    capture: () => sheetController.captureNamedView(),
    apply: (prepared, data, current) => sheetController.applyNamedView(prepared, data, current),
    supportsRowVisibility: () => true,
    canApply: () => {
      if (S.filterJob || S.opening || S.sheetLoadingEpoch || rowViews.busy) {
        toast('请先完成当前加载、筛选或行显隐更新', 'warning');
        return false;
      }
      return true;
    },
  });
  namedViewsRef = namedViews;

  const catalogueCenter = new Catalogue({
    state: S,
    api,
    toast,
    limits: fileFormats.catalogue,
    supported: () => fileFormats.source(S.source?.format).catalogueMetadata,
    navigate: (area, current) => nav.navigateCatalogueArea(area, current),
    canNavigate: () =>
      S.opened &&
      !S.opening &&
      !S.sheetLoadingEpoch &&
      !S.filterJob &&
      !rowViews.busy,
  });
  catalogueCenterRef = catalogueCenter;

  const dataRulesCenter = new DataRules({
    state: S,
    api,
    toast,
    limits: fileFormats.dataRules,
    supported: () => fileFormats.source(S.source?.format).dataRuleMetadata,
    navigate: (area, current) => nav.navigateCatalogueArea(area, current),
    canNavigate: () =>
      S.opened &&
      !S.opening &&
      !S.sheetLoadingEpoch &&
      !S.filterJob &&
      !rowViews.busy,
  });
  dataRulesCenterRef = dataRulesCenter;

  const printingCenter = new Printing({
    state: S,
    api,
    toast,
    source: () => fileFormats.source(S.source?.format),
    limits: fileFormats.printing,
    richLimits: fileFormats.richText,
    fontStack,
  });
  printingCenterRef = printingCenter;

  const recordCardCenter = new RecordCard({
    state: S,
    api,
    toast,
    copy: (val) => gridClipboard.copyText(val),
    colName: columnName,
    parseColumn,
    limits: fileFormats.record,
    source: () => fileFormats.source(S.source?.format),
    pinnedThroughRow: () => frozenPanes.effective().rows,
    canRead: () => !S.filterJob && !rowViews.busy,
    navigate: (sheet, ref, guard) => nav.navigateToCell(sheet, ref, false, guard),
  });
  recordCardCenterRef = recordCardCenter;

  const profileCenter = new ProfileCenter({
    state: S,
    api,
    toast,
    openDialog: (id) => gridControls.openDialog(id),
    navigateToCell: (sheet, ref, preserveTrace, guard) =>
      nav.navigateToCell(sheet, ref, preserveTrace, guard),
    colName: columnName,
    limits: fileFormats.profile,
    source: () => fileFormats.source(S.source?.format),
    canRead: () => !S.filterJob && !rowViews.busy,
    excludeThroughRow: () => frozenPanes.effective().rows,
    numbers: AnalysisNumbers,
  });
  profileCenterRef = profileCenter;

  const selectionQuick = new SelectionQuick({
    state: S,
    api,
    toast,
    colName: columnName,
    limits: fileFormats.selectionQuick,
    pinnedThroughRow: () => frozenPanes.effective().rows,
    canRead: () => !S.filterJob && !rowViews.busy,
    navigate: (sheet, ref, guard) => nav.navigateToCell(sheet, ref, false, guard),
    formatNumber: (value) => formatAnalysisNumber(value),
    drawChart: (canvas, data, mode, onSelect) => {
      AnalyticsCharts.draw(canvas, data, mode, onSelect);
    },
    getChartExport: () => getChartExportController(fileFormats.chartExport, activity),
  });
  selectionQuickRef = selectionQuick;

  const cellExplanation = new CellExplanation({
    state: S,
    api,
    toast,
    colName: columnName,
    parseRef,
    limits: fileFormats.cellExplanation,
    source: () => fileFormats.source(S.source?.format),
    pinnedThroughRow: () => frozenPanes.effective().rows,
    canRead: () => !S.filterJob && !rowViews.busy,
    notice: (cell: CellData) => {
      const notices: string[] = [];
      for (const node of document.querySelectorAll<GridCellElement>(
        '.grid-region .grid-cell.rich-text-notice,.grid-region .grid-cell.cell-presentation-notice',
      )) {
        if (node.dataset.ref !== cell.ref) continue;
        const text = node._xlRichNotice || node._xlPresentationNotice;
        if (text) notices.push(text);
      }
      if (notices.length) return notices.join('；');
      return inspectRichText(cell, fileFormats.richText)?.notice ?? '';
    },
  });
  cellExplanationRef = cellExplanation;

  const bookmarkCenter = new Bookmarks({
    state: S,
    api,
    toast,
    openDialog: (id) => gridControls.openDialog(id),
    navigateToCell: (sheet, ref) => nav.navigateToCell(sheet, ref),
    canNotes: () => fileFormats.source(S.source?.format).noteMetadata,
    applyNotes: async (book, notes) => {
      const firstNote = notes[0];
      if (!firstNote) return;
      if (!editCopy || editCopy.pending || editCopy.abort || editCopy.grid.blocked) {
        throw new Error('请先完成当前编辑操作');
      }
      if (!editCopy.grid.active) await editCopy.show();
      if (!editCopy.grid.active || S.book !== book || editCopy.data?.book !== book) {
        throw new Error('编辑来源已变化，请重新选择标记');
      }
      const ok = await editCopy.commit('structure', [], {
        direct: true,
        structure: { kind: 'reviewNotes', sheet: firstNote.sheet, notes },
      });
      if (!ok) {
        throw new Error(byId('editCopyStatus')?.textContent || '批注写入失败');
      }
      toast('批注已写入当前修订，可撤销或生成副本', 'success');
    },
    onChange: () => {
      ++S.decorVersion;
      pipeline.renderGridFromCurrentWindow();
      frozenPanes.schedule(true);
    },
  });
  bookmarkCenterRef = bookmarkCenter;

  // ── Command Palette Actions ───────────────────────────────────────────────
  const ACTIONS: readonly CommandAction[] = [
    buttonCommand('打开文件', 'openLocalMenuBtn', '工作簿 选择 文件', 'Ctrl+O'),
    buttonCommand(
      '连接在线数据库',
      'openDatabaseBtn',
      '数据库 MySQL PostgreSQL Oracle SQL Server ClickHouse',
    ),
    buttonCommand('数据库快照', 'databaseSnapshotsBtn', '固化 保存 核验 数据库'),
    buttonCommand('搜索', 'searchOpenBtn', '查找公式或值 查找 公式 值', 'Ctrl+F'),
    buttonCommand('筛选', 'filterOpenBtn', '自动筛选 表头 下拉', 'Ctrl+Shift+L'),
    buttonCommand('清除筛选', 'clearFilterBtn', '清除 筛选 恢复 原序'),
    buttonCommand('排序', 'multiSortBtn', '多级自定义排序 多级 多列 升序 降序 自定义'),
    buttonCommand('冻结窗格', 'freezeBtn', '表头 行 列 固定'),
    buttonCommand('导出', 'exportOpenBtn', '保存 CSV XLSX HTML 数据 导出中心', 'Ctrl+Shift+E'),
    { name: '导出当前选区', shortcut: '', tags: '区域 导出', run: () => exportCenter.open('selection') },
    { name: '导出整个工作簿', shortcut: '', tags: '全部 Sheet 工作表', run: () => exportCenter.open('all') },
    buttonCommand('编辑副本', 'editCopyOpen', '改值 粘贴 另存 撤销 修改'),
    buttonCommand('分析', 'analysisOpenBtn', '数据分析 透视 汇总 图表 分析表'),
    buttonCommand(
      '统计选区',
      'profileOpenBtn',
      '数据洞察 精确统计 汇总 求和 公式 异常 计数',
      'Ctrl+Alt+S',
    ),
    buttonCommand('计算', 'calculatorOpenBtn', '公式计算 加减乘除 金额', 'Ctrl+Alt+C'),
    buttonCommand('复制选区', 'copySelectionBtn', 'Excel 格式 复制 保留颜色 粘贴', 'Ctrl+C'),
    buttonCommand('复制为纯文本', 'copyPlainBtn', '复制选区为纯文本 TSV 不带格式 粘贴 值'),
    buttonCommand(
      '选择性复制',
      'copySpecialBtn',
      '表头 TSV Markdown JSON 原值 公式 复制',
      'Ctrl+Alt+Shift+C',
    ),
    buttonCommand('复制当前值', 'copyValueBtn', '复制当前单元格值 复制 值 单格'),
    buttonCommand('复制公式', 'copyFormulaBtn', '复制当前公式 复制 公式 原文', 'Ctrl+Shift+C'),
    buttonCommand('复制地址', 'copyAddressBtn', '复制跨 Sheet 地址 引用 定位 复制'),
    buttonCommand('复制证据摘要', 'copyEvidenceBtn', '复制审计证据摘要 审计 数值 公式 复制 底稿 来源'),
    buttonCommand('压缩包里的其他文件', 'archiveBtn', '浏览当前压缩包 压缩包 文件 成员 浏览'),
    buttonCommand('高级筛选', 'advancedFilterBtn', '高级条件筛选 筛选 条件 AND 多条件'),
    {
      name: '按当前单元格的值筛选',
      shortcut: '',
      tags: '根据当前单元格值筛选 快捷筛选 等于 当前值',
      run: () => sheetController.filterSelectedValue(),
    },
    buttonCommand('TXT 表头', 'textHeaderBtn', '设置文本表头 文本 表头 字段 设置'),
    buttonCommand(
      '查找重复值',
      'duplicateOpenBtn',
      '高亮重复值 查重 高亮 重复单号 收款人 账号 重复数据',
      'Ctrl+Shift+D',
    ),
    buttonCommand('定位条件', 'inspectOpenBtn', '查找 定位特殊单元格 公式 常量 错误 空值', 'Ctrl+Alt+G'),
    buttonCommand('数据规则', 'dataRulesOpenBtn', '查看数据规则 数据验证 有效性 下拉列表 验证条件'),
    buttonCommand('记录卡片', 'recordCardOpenBtn', '查看记录卡片 宽表 字段 长文本 原值 记录'),
    buttonCommand(
      '快速分析选区',
      'selectionQuickBtn',
      '选区快速分析 分布 差额 趋势 小图 同行 比较',
      'Ctrl+Q',
    ),
    buttonCommand('转置查看选区', 'transposeOpenBtn', '转置阅读当前选区 行列 交换 宽表 原坐标'),
    buttonCommand('显示公式', 'formulaViewBtn', '显示或隐藏全部公式 公式视图 显示 隐藏 计算结果', 'Ctrl+`'),
    buttonCommand('追踪引用', 'tracePrecedentsBtn', '公式追踪 引用 上游'),
    buttonCommand('追踪从属', 'traceDependentsBtn', '追踪从属单元格 公式 下游 依赖 从属'),
    buttonCommand('名称与表', 'catalogueOpenBtn', '名称与表目录 定义名称 表对象 结构化引用 区域', 'Ctrl+F3'),
    buttonCommand('单元格信息', 'cellDetailBtn', '原值 源类型 公式 缓存 统计 解释', 'Ctrl+1'),
    buttonCommand('显示或隐藏列', 'columnsBtn', '列 显隐 隐藏 显示'),
    buttonCommand('行与大纲', 'rowsBtn', '行 隐藏 显示 分组 折叠 展开'),
    buttonCommand('原格式', 'formatBtn', '显示/隐藏原格式 显示 隐藏 背景 颜色 字体 边框'),
    buttonCommand('显示或隐藏图片', 'imagesBtn', '图片 图形 显示 隐藏'),
    buttonCommand('命名视图', 'namedViewOpenBtn', '保存 应用 视图 筛选 排序 布局'),
    buttonCommand('分页预览', 'printPreviewOpenBtn', '打印 纸张 页边距 缩放 重复标题', 'Ctrl+P'),
    buttonCommand('全屏', 'fullscreenBtn', '窗口', 'F11'),
    { name: '设置当前行高', shortcut: 'Alt+H,O,H', tags: '行高 调整 大小', run: () => pipeline.promptResize('row') },
    { name: '设置当前列宽', shortcut: 'Alt+H,O,W', tags: '列宽 调整 大小', run: () => pipeline.promptResize('col') },
    {
      name: '当前列宽自动适应',
      shortcut: 'Alt+H,O,I',
      tags: '列宽 自动 调整',
      run: () => pipeline.autoFitSize('col', S.selected?.col || 1),
    },
    {
      name: '当前行高自动适应',
      shortcut: 'Alt+H,O,A',
      tags: '行高 自动 调整',
      run: () => pipeline.autoFitSize('row', S.selected?.row || 1),
    },
    buttonCommand('对账', 'reconcileOpenBtn', '双表对账 主键 差异 未匹配 重复键'),
    buttonCommand('关联', 'relationsOpenBtn', '关联提取 连接 左表 右表 未匹配'),
    buttonCommand(
      '标记与备注',
      'bookmarkOpenBtn',
      '审计标记 备注复核 异常 核实 审核 提醒 标签 待复核',
      'Ctrl+Alt+B',
    ),
    buttonCommand('比较快照', 'snapshotsOpenBtn', '保存 分析 趋势 差额 历史 比较'),
    buttonCommand('本地数据', 'storageOpenBtn', '数据管理 备份 迁入 缓存 清理 存储'),
    buttonCommand('任务恢复', 'taskRecoveryOpenBtn', '继续 查询 筛选 排序 导出 下载 中断'),
    {
      name: '跳转到单元格',
      shortcut: 'Ctrl+G',
      tags: '定位 行 列 名称框',
      run: () => {
        const box = requireInput('nameBox');
        box.focus();
        box.select();
      },
    },
    {
      name: '跳到数据区域边界',
      shortcut: 'Ctrl+方向键',
      tags: '跳至数据区域边界 移动 跳过 空值 快速定位',
      run: () => nav.jumpDataEdge('down'),
    },
    buttonCommand('返回上一位置', 'historyBackBtn', '历史 返回 位置', 'Alt+←'),
    buttonCommand('前进到下一位置', 'historyForwardBtn', '历史 前进 位置', 'Alt+→'),
    {
      name: '打开所选单元格的超链接',
      shortcut: 'Ctrl+Enter',
      tags: '打开所选单元格超链接 跳转 工作表 网页',
      run: () => nav.followCellHyperlink(S.selected?.hyperlink),
    },
    {
      name: '全部工作表',
      shortcut: '',
      tags: '工作表列表 Sheet 导航',
      run: () => requireButton('sheetListBtn').click(),
    },
    buttonCommand('返回首页', 'homeBtn', '返回最近文件 最近打开 历史 工作簿'),
    buttonCommand('快捷键', 'helpBtn', '快捷键与操作帮助 帮助 操作说明'),
    buttonCommand('退出程序', 'exitBtn', '退出 关闭 程序'),
  ];

  const commandPalette = new CommandPalette(ACTIONS, {
    node: byId,
    sheets: () => S.sheets,
    selectSheet: (name) => sheetController.selectSheet(name),
    canOpen: () => S.opened,
    openDialog: (id) => gridControls.openDialog(id),
    closeDialog: (id) => gridControls.closeDialog(id),
    toast,
    reveal: (id) => ribbon?.reveal(id),
  });
  commandPaletteRef = commandPalette;

  // ── Background Tasks, Local Storage, Online DB, Snapshots ─────────────────
  initStorageDialog({
    api,
    openDialog: (id) => gridControls.openDialog(id),
    background: createBackgroundMonitor({
      api,
      session,
      state: S,
      toast,
      adoptTask: (start, item) => sheetController.adoptRecoveredTask(start, item),
      analysis: (job, status) => analytics.reopenJob(job, status),
      reconcile: (job, status) => reconcileCenter.reopenJob(job, status),
      workbookReady: () => workbookReady,
    }),
  });

  const databasePanel = new DatabasePanel({
    api,
    onOpened: async (result) => {
      const current = await api<WorkbookStatusResponse>('/api/status');
      if (!current.opened || current.workbookKey !== result.workbookKey) {
        throw new Error('工作簿已切换');
      }
      await workbookIO.acceptOpenedWorkbook(current);
    },
  });
  databasePanelRef = databasePanel;

  const databaseSnapshots = new DatabaseSnapshots({
    api,
    fmtBytes: formatBytes,
    openWorkbook: (loader, fileName, fileSize, guard) =>
      workbookIO.openWorkbook(loader, fileName, fileSize, guard),
    onRetained: (key) => {
      if (S.workbookKey === key) requireEl('retainDatabaseSnapshot').hidden = true;
    },
  });
  databaseSnapshotsRef = databaseSnapshots;

  // ── Grid Interactions ─────────────────────────────────────────────────────
  const gridInteractions = new GridInteractions({
    S,
    api,
    toast,
    setStatus,
    colName: columnName,
    parseRef,
    cellAt: (row, col) => nav.cellAt(row, col),
    tiles,
    mergedCells,
    virtualScroll,
    motion,
    controls: gridControls,
    clipboard: gridClipboard,
    workbookIO,
    panels: interactionPanels,
    rowHead: () => pipeline.rowHeadWidth,
    rowHeight: (row) => pipeline.rowHeight(row),
    colWidth: (col) => pipeline.colWidth(col),
    headerHeight: () => pipeline.headerHeight(),
    rowTop: (row) => pipeline.rowTop(row),
    colLeft: (col) => pipeline.colLeft(col),
    getVirtualScrollTop: () => pipeline.getVirtualScrollTop(),
    getViewportRequest: () => pipeline.getViewportRequest(),
    viewportContext: () => pipeline.viewportContext(),
    activeMerge: (row, col) => pipeline.activeMerge(row, col),
    selectCell: (
      cell,
      rowNumber,
      colNumber,
      ref,
      visualIndex,
      preserveTrace,
      keepSelection,
      crosshair,
    ) =>
      nav.selectCell(
        cell,
        rowNumber,
        colNumber,
        ref,
        visualIndex,
        preserveTrace,
        keepSelection,
        crosshair,
      ),
    extendRange: (anchor, row, col, visualIndex) => nav.extendRange(anchor, row, col, visualIndex),
    applySelectionClasses: () => pipeline.applySelectionClasses(),
    selectWholeRow: (row, visualIndex) => nav.selectWholeRow(row, visualIndex),
    selectColumnSpan: (anchor, end) => columnController.selectSpan(anchor, end),
    captureColumnSelection: () => columnController.captureSelection(),
    selectSheet: (name) => sheetController.selectSheet(name),
    selectSheetTab: (name) => sheetController.selectSheetTab(name),
    navigateToCell: (sheet, ref) => nav.navigateToCell(sheet, ref),
    navigateNameBox: (value) => nav.navigateNameBox(value),
    moveSelection: (rowDelta, colDelta, extend, keepAnchor) =>
      nav.moveSelection(rowDelta, colDelta, extend, keepAnchor),
    jumpDataEdge: (direction, extend) => nav.jumpDataEdge(direction, extend),
    goHistory: (delta) => nav.goHistory(delta),
    followCellHyperlink: (link) => nav.followCellHyperlink(link),
    showWelcome,
    showCellDetail: () => {
      void cellExplanation.open();
    },
    openCommandPalette: () => commandPalette.open(),
    openSearch: (mode, query) => searchPanel.open(mode, query),
    stepSearch: (delta) => searchPanel.step(delta),
    toggleAutoFilter: () => sheetController.toggleAutoFilter(),
    toggleFormulaView: () => pipeline.toggleFormulaView(),
    toggleOriginalFormat: () => sheetController.toggleOriginalFormat(),
    changeDisplayFont: (font) => pipeline.changeDisplayFont(font),
    openFilter: (column) => sheetController.openFilter(column),
    addFilterRule: (column, operator, value) =>
      sheetController.addFilterRule(column, operator, value),
    applyFilter: () => sheetController.applyFilter(),
    cancelFilter: (jobID) => sheetController.cancelFilter(jobID),
    clearFilter: () => sheetController.clearFilter(),
    filterSelectedValue: () => sheetController.filterSelectedValue(),
    openFreezeDialog: () => sheetController.openFreezeDialog(),
    applyFreeze: (rows, cols) => sheetController.applyFreeze(rows, cols),
    applyTextTableHeader: (number, automatic) =>
      sheetController.applyTextTableHeader(number, automatic),
    disableTextTableHeader: () => sheetController.disableTextTableHeader(),
    promptResize: (kind, explicitIndex) => pipeline.promptResize(kind, explicitIndex),
    commitResize: (kind, index, size) => pipeline.commitResize(kind, index, size),
    autoFitSize: (kind, index) => pipeline.autoFitSize(kind, index),
    setColumnVisibility: (mode, index, hidden, end) =>
      columnController.setVisibility(mode, index, hidden, end),
    tracePrecedents: () => traceUI.open('precedents'),
    traceDependents: async () => {
      if (!S.selected) return;
      await traceUI.open('dependents');
    },
    followGridScroll: () => pipeline.followGridScroll(),
    scheduleRangeFrames: () => pipeline.scheduleRangeFrames(),
    syncTableColumnHeaders: () => sheetController.syncTableColumnHeaders(),
    syncFreezeScrollGutters: () => pipeline.syncFreezeScrollGutters(),
    updateCanvasSize: () => pipeline.updateCanvasSize(),
    scheduleInitialPreview: () => pipeline.scheduleInitialPreview(),
    renderGridFromCurrentWindow: () => pipeline.renderGridFromCurrentWindow(),
    paintRangeFrames: () => pipeline.paintRangeFrames(),
    scheduleViewport: (force) => pipeline.scheduleViewport(force),
  });
  gridInteractionsRef = gridInteractions;

  // Performance telemetry bridge (counters only; no spreadsheet content)
  exposePerformanceBridge(() => ({
    ...S.renderMetrics,
    virtualScrollScale: virtualScroll.scale,
    currentRows: S.renderedRows.size,
    visibleNodes: requireEl('rowsLayer').querySelectorAll('.grid-cell').length,
    bufferedTiles: tiles.size,
    bufferedApproxBytes: tiles.bytes,
    cachedSheetViews: S.sheetViews.size,
    cachedSheetBytes: S.sheetViewsBytes,
    sheetLoading: Boolean(S.sheetLoadingEpoch),
    conditionalStatsState: S.conditionalStatsState,
    conditionalStatsRunning: Boolean(S.conditionalStatsJob),
    hasPendingRequest: pipeline.scheduler.active.size > 0,
    foregroundHits: pipeline.foregroundHits,
    foregroundLookups: pipeline.foregroundLookups,
    requests: { ...pipeline.scheduler.stats },
    hasVisibleCoverage:
      S.indexReady && viewportCoversVisible(S.window, pipeline.getViewportRequest()),
  }));

  byId('conditionalStatsCancel')?.addEventListener('click', () => {
    sheetController.cancelConditionalStats(true, true);
  });
  byId('conditionalStatsRetry')?.addEventListener('click', () => {
    if (!S.indexReady || !S.sheet || !S.showStyles) return;
    S.conditionalStatsState = '';
    void sheetController.startConditionalStats(S.sheet, S.sheetEpoch, { refreshOnReady: true });
  });

  gridInteractions.bindEvents();
  workbookReady = workbookIO.loadStatus();
  exposeWorkbookReadyBridge(workbookReady);
}

bootstrap();
