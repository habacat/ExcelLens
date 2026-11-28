/**
 * Application composition root for ExcelLens (`internal/ui/web/dist/app.js`).
 * Wires Layer 1 core services, Layer 2 grid/viewport controllers, and Layer 3
 * editing, analysis, and feature modules using explicit ES imports and
 * dependency injection. Exposes ONLY the narrow QA bridge in `runtime-bridge.ts`.
 */
import { t as localize } from '../core/i18n.ts';
import type { ToastTone } from '../contracts/common.ts';
import type { CellData } from '../contracts/workbook.ts';
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
  bindSourceIntegrityControls,
  hideSourceIntegrity,
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
import { decodeSheetViewport } from '../core/viewport-response.ts';
import { decodeOpenResult, decodeWorkbookStatus } from '../core/workbook-response.ts';

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

  // ── Layer 2: Shared workbook state & geometry primitives ──────────────────
  const S = createInitialWorkbookState(
    fileFormats.source('xlsx').maxRows,
    fileFormats.maxColumns,
  );
  const interfaceSettings = ribbon ? initToolbarSettings({
    ribbon, session, menus,
    canReloadLanguage: () => !S.opening && document.body.classList.contains('home-view') &&
      !document.querySelector('dialog[open], #tabActivity:not([hidden])'),
  }) : null;
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

  const setStatus = (text: string, hidden = false): void => {
    const el = requireEl('globalStatus');
    el.textContent = text;
    el.hidden = hidden;
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
    renderSourceIntegrityBanner(S.source, S.workbookKey);
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
    hideSourceIntegrity();
    picturesRef?.close();
    document.body.classList.add('home-view');
    requireEl('workspace').hidden = true;
    requireEl('welcome').hidden = false;
    interfaceSettings?.applyLanguageOnHome();
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
      getSheetController().conditional.startConditionalStats(sheetName, epoch, options),
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
    setDisplayFontSelection: (font) => pipeline.appearance.setDisplayFontSelection(font),
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
    loadStylePalette: () => pipeline.appearance.loadStylePalette(),
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
      sheetController.conditional.cancelConditionalStats(sendCancel, showCancelled),
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
    bindResizeGrip: (grip, kind, index) => pipeline.sizing.bindResizeGrip(grip, kind, index),
    sourceRowTop: (row) => pipeline.sourceRowTop(row),
    sourceRowHeight: (row) => pipeline.sourceRowHeight(row),
    getVirtualScrollTop: () => pipeline.getVirtualScrollTop(),
    paintRowHeaderBorder: (el, cell, row, belowCell, belowRow) =>
      pipeline.appearance.paintRowHeaderBorder(el, cell, row, belowCell, belowRow),
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
      pipeline.appearance.updateSharedBorders(
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
    applyCellStyle: (node, cell) => pipeline.appearance.applyCellStyle(node, cell),
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
      const pinnedThrough = Number(params.get('pinnedThroughRow'));
      const visuals =
        name === 'left'
          ? Array.from({ length: base.rowCount ? base.limit : 0 }, (_, i) => {
              const ordered = base.rowOrder?.at(startRow + i - 1);
              return ordered || startRow + i;
            })
          : Array.from({ length: pinnedThrough || Number(params.get('limit')) }, (_, i) => i + 1).filter((r) =>
              pipeline.rowVisible(r),
            );
      if (params.has('pinnedBorderNeighbor')) {
        const next = pipeline.nextVisibleRow(pinnedThrough + 1);
        if (next > pinnedThrough && next <= S.maxRow) visuals.push(next);
      }
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
    onError: (error) => toast(localize('ui.main.failedFreezePanesReading', { detail: error.message }), 'error'),
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
          api('/api/recent', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ action: 'open', id: item.id }),
          }, decodeOpenResult),
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
            purpose: localize('ui.main.paste'),
            limits: {
              rows: limits.batchCells,
              columns: limits.batchCells,
              cells: limits.batchCells,
            },
          });
          if (snapshot.rows > 1) {
            const data = await snapshot.read({ richText: false, conditional: false });
            if (!snapshot.current()) throw new Error(localize('ui.common.electoralDistrictChangedPasteInformation'));
            return data.rows.map((r) => r.row);
          }
          const through = frozenPanes.effective().rows;
          const selected = S.selected;
          if (!selected) throw new Error(localize('ui.main.selectTargetCellFirst'));
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
          if (offset < 0) throw new Error(localize('ui.main.currentCellLeftFilterResults'));
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
            if (!snapshot.current()) throw new Error(localize('ui.common.electoralDistrictChangedPasteInformation'));
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
            const data = await api(`/api/sheet?${params.toString()}`, undefined, decodeSheetViewport);
            if (!snapshot.current()) throw new Error(localize('ui.common.electoralDistrictChangedPasteInformation'));
            rows.push(...data.rows.map((row) => row.number));
            if (data.rows.length < size) break;
            offset += size;
          }
          return rows;
        },
        visibleSelection: () => {
          const cells = fileFormats.editing.batchCells;
          return gridClipboard.captureClipboardSelection({
            purpose: localize('ui.main.editing'),
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
      throw new Error(localize('ui.main.completeWorksheetLoadingFilteringOr'));
    }
    if (mode === 'filtered' && (!S.filtered || !S.filterId)) {
      throw new Error(localize('ui.main.currentWorksheetDoesNotHave'));
    }
    if (mode === 'visible' && !S.rowView?.id) {
      throw new Error(localize('ui.main.currentlyVisibleRowsNotReady'));
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
      if (areas.length !== 1) throw new Error(localize('ui.main.maintainContinuousSelectionWorkbook'));
      const only = areas[0];
      if (!only) throw new Error(localize('ui.main.maintainContinuousSelectionWorkbook'));
      const pts = MultiSelection.points(only, columnName);
      return { ...base, ...pts };
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
        toast(localize('ui.main.completeFileLoadingFirst'), 'warning');
        return false;
      }
      if (
        item.kind === 'export'
          ? exportCenter.running || exportCenter.busy
          : Boolean(S.searchJob || S.filterJob)
      ) {
        toast(localize('ui.main.completeOrCancelCurrentTask'), 'warning');
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
        localize('ui.main.exceptionVerificationListComplete'),
      );
    },
    exportClusters: (jobId, protectCSV) => {
      void exportCenter.startGenerated(
        '/api/analysis/clusters/export',
        { jobId, protectCSV },
        localize('ui.main.suspectedDuplicateCandidatesCompleteCount'),
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
    refresh: () => analytics.refresh(),
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
        toast(localize('ui.main.completeLoadingFilteringOrRow'), 'warning');
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
        throw new Error(localize('ui.main.completeCurrentEditingOperationsFirst'));
      }
      if (!editCopy.grid.active) await editCopy.show();
      if (!editCopy.grid.active || S.book !== book || editCopy.data?.book !== book) {
        throw new Error(localize('ui.main.sourceDataChangedReselectTag'));
      }
      const ok = await editCopy.commit('structure', [], {
        direct: true,
        structure: { kind: 'reviewNotes', sheet: firstNote.sheet, notes },
      });
      if (!ok) {
        throw new Error(byId('editCopyStatus')?.textContent || localize('ui.main.annotationWritingFailed'));
      }
      toast(localize('ui.main.annotationWrittenCurrentRevisionCan'), 'success');
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
    buttonCommand(localize('ui.main.openFile'), 'openLocalMenuBtn', localize('ui.main.workbookSelectionFile'), 'Ctrl+O'),
    buttonCommand(
      localize('ui.main.connectDatabase'),
      'openDatabaseBtn',
      localize('ui.main.databasesMysqlPostgresqlOracleSql'),
    ),
    buttonCommand(localize('ui.main.databaseSnapshots'), 'databaseSnapshotsBtn', localize('ui.main.solidificationSavingVerificationDatabase')),
    buttonCommand(localize('ui.common.search'), 'searchOpenBtn', localize('ui.main.searchFormulaOrValueSearch'), 'Ctrl+F'),
    buttonCommand(localize('ui.main.filter'), 'filterOpenBtn', localize('ui.main.automaticFilteringHeaderDropdown'), 'Ctrl+Shift+L'),
    buttonCommand(localize('ui.common.clearFilters'), 'clearFilterBtn', localize('ui.main.clearFilterRestoreOriginalOrder')),
    buttonCommand(localize('ui.main.sort'), 'multiSortBtn', localize('ui.main.multiLevelCustomSortingMulti')),
    buttonCommand(localize('ui.main.freezePane'), 'freezeBtn', localize('ui.main.headingsRowsColumnsFixed')),
    buttonCommand(localize('ui.main.export'), 'exportOpenBtn', localize('ui.main.saveCsvXlsxHtmlData'), 'Ctrl+Shift+E'),
    { name: localize('ui.main.exportCurrentSelection'), shortcut: '', tags: localize('ui.main.areaExport'), run: () => exportCenter.open('selection') },
    { name: localize('ui.main.exportEntireWorkbook'), shortcut: '', tags: localize('ui.main.allSheetWorksheets'), run: () => exportCenter.open('all') },
    buttonCommand(localize('ui.common.editCopy'), 'editCopyOpen', localize('ui.main.changeValuePasteSaveAs')),
    buttonCommand(localize('ui.common.analysis'), 'analysisOpenBtn', localize('ui.main.dataAnalysisPivotTableSummary')),
    buttonCommand(
      localize('ui.main.statisticalElectoralDistricts'),
      'profileOpenBtn',
      localize('ui.main.dataInsightPreciseStatisticsSummary'),
      'Ctrl+Alt+S',
    ),
    buttonCommand(localize('ui.main.calculation'), 'calculatorOpenBtn', localize('ui.main.formulaCalculationAdditionSubtractionMultiplication'), 'Ctrl+Alt+C'),
    buttonCommand(localize('ui.main.copySelectionArea'), 'copySelectionBtn', localize('ui.main.excelFormatCopyPreserveColors'), 'Ctrl+C'),
    buttonCommand(localize('ui.main.copyPlainText'), 'copyPlainBtn', localize('ui.main.copySelectedAreaAsPlain')),
    buttonCommand(
      localize('ui.main.selectiveReplication'),
      'copySpecialBtn',
      localize('ui.main.tableHeadersTsvMarkdownJson'),
      'Ctrl+Alt+Shift+C',
    ),
    buttonCommand(localize('ui.main.copyCurrentValue'), 'copyValueBtn', localize('ui.main.copyCurrentCellValueCopy')),
    buttonCommand(localize('ui.main.copyFormula'), 'copyFormulaBtn', localize('ui.main.copyCurrentFormulaCopyFormula'), 'Ctrl+Shift+C'),
    buttonCommand(localize('ui.main.copyAddress'), 'copyAddressBtn', localize('ui.main.copyingAcrossSheetAddressesReferences')),
    buttonCommand(localize('ui.main.copyEvidenceSummary'), 'copyEvidenceBtn', localize('ui.main.copyAuditEvidenceSummaryAudit')),
    buttonCommand(localize('ui.main.otherFilesCompressedPackage'), 'archiveBtn', localize('ui.main.viewCurrentCompressedPackageCompressed')),
    buttonCommand(localize('ui.main.advancedFiltering'), 'advancedFilterBtn', localize('ui.main.advancedConditionalFilteringFilterConditions')),
    {
      name: localize('ui.main.filterValueCurrentCell'),
      shortcut: '',
      tags: localize('ui.main.filterCurrentCellValueQuick'),
      run: () => sheetController.filterSelectedValue(),
    },
    buttonCommand(localize('ui.main.txtHeader'), 'textHeaderBtn', localize('ui.main.setTextHeadersTextHeaders')),
    buttonCommand(
      localize('ui.main.searchingDuplicateValues'),
      'duplicateOpenBtn',
      localize('ui.main.highlightDuplicateValuesCheckDuplicates'),
      'Ctrl+Shift+D',
    ),
    buttonCommand(localize('ui.main.locationCriteria'), 'inspectOpenBtn', localize('ui.main.searchSpecialCellsFormulasConstants'), 'Ctrl+Alt+G'),
    buttonCommand(localize('ui.main.dataRules'), 'dataRulesOpenBtn', localize('ui.main.viewDataRulesDataValidation')),
    buttonCommand(localize('ui.main.recordCards'), 'recordCardOpenBtn', localize('ui.main.viewRecordCardsWideTables')),
    buttonCommand(
      localize('ui.main.quickAnalysisSelection'),
      'selectionQuickBtn',
      localize('ui.main.quickAnalysisSelectionDistributionDifferences'),
      'Ctrl+Q',
    ),
    buttonCommand(localize('ui.main.invertViewSelection'), 'transposeOpenBtn', localize('ui.main.transposeReadCurrentSelectionRow')),
    buttonCommand(localize('ui.main.displayFormula'), 'formulaViewBtn', localize('ui.main.showOrHideAllFormulas'), 'Ctrl+`'),
    buttonCommand(localize('ui.main.trackingReferences'), 'tracePrecedentsBtn', localize('ui.main.formulaTrackingReferencingUpstream')),
    buttonCommand(localize('ui.main.tracingSubordinateRelations'), 'traceDependentsBtn', localize('ui.main.trackDependentCellsFormulasDownstream')),
    buttonCommand(localize('ui.main.namesTables'), 'catalogueOpenBtn', localize('ui.main.nameTableDirectoryDefineName'), 'Ctrl+F3'),
    buttonCommand(localize('ui.main.cellInformation'), 'cellDetailBtn', localize('ui.main.originalValueSourceTypeFormula'), 'Ctrl+1'),
    buttonCommand(localize('ui.main.showOrHideColumns'), 'columnsBtn', localize('ui.main.columnShowHideHideShow')),
    buttonCommand(localize('ui.main.actionsOverallPlan'), 'rowsBtn', localize('ui.main.rowHideShowGroupCollapse')),
    buttonCommand(localize('ui.main.originalFormat'), 'formatBtn', localize('ui.main.displayHideOriginalFormatDisplay')),
    buttonCommand(localize('ui.main.showOrHideImages'), 'imagesBtn', localize('ui.main.imageGraphicDisplayHide')),
    buttonCommand(localize('ui.main.namedViews'), 'namedViewOpenBtn', localize('ui.main.saveApplyViewFilterSort')),
    buttonCommand(localize('ui.main.pageBreakPreview'), 'printPreviewOpenBtn', localize('ui.main.printingPaperPageMarginsScaling'), 'Ctrl+P'),
    buttonCommand(localize('ui.main.fullScreen'), 'fullscreenBtn', localize('ui.main.window'), 'F11'),
    { name: localize('ui.main.setCurrentRowHeight'), shortcut: 'Alt+H,O,H', tags: localize('ui.main.rowHeightAdjustedSize'), run: () => pipeline.sizing.promptResize('row') },
    { name: localize('ui.main.setCurrentColumnWidth'), shortcut: 'Alt+H,O,W', tags: localize('ui.main.columnWidthAdjustedSize'), run: () => pipeline.sizing.promptResize('col') },
    {
      name: localize('ui.main.currentColumnWidthAutomaticallyAdjusted'),
      shortcut: 'Alt+H,O,I',
      tags: localize('ui.main.columnWidthAutomaticallyAdjusted'),
      run: () => pipeline.sizing.autoFitSize('col', S.selected?.col || 1),
    },
    {
      name: localize('ui.main.rowHeightAutomaticallyAdapts'),
      shortcut: 'Alt+H,O,A',
      tags: localize('ui.main.rowHeightAutomaticallyAdjusted'),
      run: () => pipeline.sizing.autoFitSize('row', S.selected?.row || 1),
    },
    buttonCommand(localize('ui.main.reconcilingAccounts'), 'reconcileOpenBtn', localize('ui.main.doubleTableReconciliationPrimaryKey')),
    buttonCommand(localize('ui.main.association'), 'relationsOpenBtn', localize('ui.main.associatedExtractionConnectionLeftTable')),
    buttonCommand(
      localize('ui.main.marksRemarks'),
      'bookmarkOpenBtn',
      localize('ui.main.auditMarkRemarksReviewException'),
      'Ctrl+Alt+B',
    ),
    buttonCommand(localize('ui.main.compareSnapshots'), 'snapshotsOpenBtn', localize('ui.main.saveAnalyzeTrendDifferenceHistory')),
    buttonCommand(localize('ui.main.localData'), 'storageOpenBtn', localize('ui.main.dataManagementBackupImportCache')),
    buttonCommand(localize('ui.main.taskRestored'), 'taskRecoveryOpenBtn', localize('ui.main.continueQueryFilterSortExport')),
    {
      name: localize('ui.main.jumpCell'),
      shortcut: 'Ctrl+G',
      tags: localize('ui.main.locateRowColumnNameBox'),
      run: () => {
        const box = requireInput('nameBox');
        box.focus();
        box.select();
      },
    },
    {
      name: localize('ui.main.jumpDataAreaBoundary'),
      shortcut: localize('ui.main.ctrlDirectionKeys'),
      tags: localize('ui.main.skipDataAreaBoundaryMove'),
      run: () => nav.jumpDataEdge('down'),
    },
    buttonCommand(localize('ui.main.returnPreviousPosition'), 'historyBackBtn', localize('ui.main.historyReturnLocation'), 'Alt+←'),
    buttonCommand(localize('ui.main.moveNextPosition'), 'historyForwardBtn', localize('ui.main.historyMoveForwardPosition'), 'Alt+→'),
    {
      name: localize('ui.main.openHyperlinkSelectedCells'),
      shortcut: 'Ctrl+Enter',
      tags: localize('ui.main.openingHyperlinkSelectedCellWill'),
      run: () =>
        nav.followCellHyperlink(
          S.selected?.hyperlink,
          S.selected ? { sheet: S.selected.sheet, ref: S.selected.ref } : null,
        ),
    },
    {
      name: localize('ui.common.allWorksheets'),
      shortcut: '',
      tags: localize('ui.main.worksheetListSheetNavigation'),
      run: () => requireButton('sheetListBtn').click(),
    },
    buttonCommand(localize('ui.main.backHome'), 'homeBtn', localize('ui.main.returnRecentlyOpenedFilesHistory')),
    buttonCommand(localize('ui.main.keyboardShortcuts'), 'helpBtn', localize('ui.main.shortcutKeysOperationInstructionsHelp')),
    buttonCommand(localize('ui.main.exitProgram'), 'exitBtn', localize('ui.main.exitCloseProgram')),
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
      const current = await api('/api/status', undefined, decodeWorkbookStatus);
      if (!current.opened || current.workbookKey !== result.workbookKey) {
        throw new Error(localize('ui.common.workbookSwitched'));
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
    followCellHyperlink: (link, origin) => nav.followCellHyperlink(link, origin),
    showWelcome,
    showCellDetail: () => {
      void cellExplanation.open();
    },
    openCommandPalette: () => commandPalette.open(),
    openSearch: (mode, query) => searchPanel.open(mode, query),
    stepSearch: (delta) => searchPanel.step(delta),
    toggleAutoFilter: () => sheetController.toggleAutoFilter(),
    toggleFormulaView: () => pipeline.toggleFormulaView(),
    toggleOriginalFormat: () => sheetController.conditional.toggleOriginalFormat(),
    changeDisplayFont: (font) => pipeline.appearance.changeDisplayFont(font),
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
    promptResize: (kind, explicitIndex) => pipeline.sizing.promptResize(kind, explicitIndex),
    commitResize: async (kind, index, size) => { await pipeline.sizing.commitResize(kind, index, size); },
    autoFitSize: (kind, index) => pipeline.sizing.autoFitSize(kind, index),
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
    sheetController.conditional.cancelConditionalStats(true, true);
  });
  byId('conditionalStatsRetry')?.addEventListener('click', () => {
    if (!S.indexReady || !S.sheet || !S.showStyles) return;
    S.conditionalStatsState = '';
    void sheetController.conditional.startConditionalStats(S.sheet, S.sheetEpoch, { refreshOnReady: true });
  });

  bindSourceIntegrityControls();
  gridInteractions.bindEvents();
  workbookReady = workbookIO.loadStatus();
  exposeWorkbookReadyBridge(workbookReady);
}

bootstrap();
