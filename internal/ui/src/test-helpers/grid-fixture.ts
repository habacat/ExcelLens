import type { ApiClient } from '../contracts/common.ts';
import type { FileFormatsConfig, SourceCapabilities } from '../contracts/formats.ts';
import type { CellData, RowViewState } from '../contracts/workbook.ts';
import { columnName, columnNumber, parseRef } from '../core/cell-address.ts';
import { createFileFormats } from '../core/formats.ts';
import type { TransportService } from '../core/transport.ts';
import { AutoFilterDropdown } from '../grid/autofilter.ts';
import { ColumnController } from '../grid/columns.ts';
import { FrozenPanes } from '../grid/freeze.ts';
import { Geometry } from '../grid/geometry.ts';
import { GridClipboard } from '../grid/grid-clipboard.ts';
import { GridControls, type GridControlsDeps } from '../grid/grid-context-menu.ts';
import { GridInteractions, type GridInteractionsDeps } from '../grid/grid-interactions.ts';
import { WorkbookIO, type WorkbookIODeps } from '../grid/grid-workbook-io.ts';
import { MergedCells } from '../grid/merges.ts';
import { MultiSelection } from '../grid/multi-selection.ts';
import { GridNavigation, type GridNavigationDeps } from '../grid/navigation.ts';
import {
  SheetController,
  type SheetControllerDeps,
  type SheetControllerPanels,
} from '../grid/sheet-controller.ts';
import { createInitialWorkbookState, type WorkbookState } from '../grid/state.ts';
import { TileCache } from '../grid/viewport-cache.ts';
import {
  ViewportPipeline,
  type ViewportPipelineDeps,
  type ViewportPipelinePanels,
} from '../grid/viewport-pipeline.ts';
import { ScrollMotion, VirtualScroll } from '../grid/virtual-scroll.ts';

export function makeDefaultCapabilities(
  overrides: Partial<SourceCapabilities> = {},
): SourceCapabilities {
  return {
    id: 'xlsx',
    maxRows: 1_048_576,
    maxColumns: 16_384,
    styled: true,
    textAmounts: false,
    conditionalMetadata: true,
    catalogueMetadata: true,
    dataRuleMetadata: true,
    noteMetadata: true,
    pictureMetadata: true,
    printMetadata: true,
    textClustering: true,
    ...overrides,
  };
}

export function makeTestFileFormatsConfig(
  overrides: Partial<FileFormatsConfig> = {},
): FileFormatsConfig {
  const xlsx = makeDefaultCapabilities({ id: 'xlsx' });
  const txt = makeDefaultCapabilities({
    id: 'txt',
    maxRows: 1_000_000_000,
    styled: false,
    textAmounts: true,
  });
  return {
    extensions: ['.xlsx', '.xls', '.xlsb', '.csv', '.tsv', '.txt', '.gz'],
    sources: { xlsx, txt },
    largeDataRows: 5_000_000,
    queryTempBytes: 8 * 1024 ** 3,
    richText: { cellRuns: 4096, viewRuns: 8192 },
    clipboard: {
      rows: 5000,
      columns: 200,
      cells: 200000,
      textBytes: 8_000_000,
      htmlBytes: 16_000_000,
      tsvRows: 50000,
      tsvColumns: 200,
      tsvCells: 500000,
    },
    reconcileRecordBytes: 1_048_576,
    printing: {
      outputPages: 200,
      outputBytes: 16_777_216,
      pages: 200,
      rows: 5000,
      columns: 200,
      cells: 100000,
      areas: 16,
      parts: 64,
      bytes: 16_777_216,
      requestBytes: 1_048_576,
      notices: 32,
      domPixels: 16_777_216,
      unitsPerPoint: 1.333333,
      firstPageMin: 1,
      firstPageMax: 9999,
    },
    record: { columns: 100, bytes: 1_048_576, requestBytes: 65536, textUnits: 4000 },
    cellExplanation: { bytes: 65536, requestBytes: 65536 },
    profile: { cells: 5_000_000, requestBytes: 65536, bytes: 4_194_304, bins: 32 },
    clustering: { pageGroups: 50, pageVariants: 50 },
    composition: {
      sources: 8,
      columns: 256,
      previewRows: 100,
      requestBytes: 1_048_576,
      resultBytes: 8_388_608,
      attachments: 8,
      fieldPageRows: 40,
      pickerRows: 40,
    },
    selectionQuick: {
      cells: 200000,
      columns: 20,
      segments: 24,
      requestBytes: 65536,
      bytes: 1_048_576,
    },
    chartExport: {
      types: ['png', 'svg'],
      scales: [1, 2],
      minWidth: 320,
      maxWidth: 4096,
      minHeight: 240,
      maxHeight: 4096,
      defaultWidth: 1200,
      defaultHeight: 600,
      pixels: 16_777_216,
      dataBytes: 4_194_304,
      imageBytes: 16_777_216,
      svgBytes: 4_194_304,
      svgNodes: 50000,
      packageBytes: 16_777_216,
    },
    transpose: { rows: 500, columns: 200, cells: 20000, bytes: 4_194_304, pageColumns: 40 },
    snapshots: {
      entries: 100,
      rows: 2000,
      series: 16,
      bytes: 4_194_304,
      storeBytes: 33_554_432,
      pageRows: 50,
    },
    editing: {
      extensions: ['.xlsx', '.csv'],
      cellBytes: 32768,
      formulaBytes: 32768,
      batchCells: 10000,
      requestBytes: 4_194_304,
    },
    relationExports: [],
    taskRecovery: { tasks: 50 },
    catalogue: { pageRows: 50, searchBytes: 256 },
    dataRules: { pageRows: 50 },
    analysisTree: { nodes: 20000, bytes: 16_777_216, pageRows: 200, active: 2 },
    ...overrides,
  };
}

export function makeRowViewState(overrides: Partial<RowViewState> = {}): RowViewState {
  return {
    id: 'view-1',
    sheet: 'A',
    filterId: '',
    workbookKey: 'book',
    book: 'book',
    revision: 0,
    settings: { mode: 'original' },
    totalRows: 10,
    maxRow: 10,
    digest: '',
    notice: '',
    settingsWarning: '',
    matchedPinnedRows: null,
    hiddenRanges: [],
    groupCount: 0,
    ...overrides,
  };
}

export function makeCellData(
  col: number,
  raw = '',
  overrides: Partial<CellData> = {},
): CellData {
  return {
    ref: `${columnName(col)}1`,
    col,
    raw,
    display: raw,
    hasFormula: false,
    ...overrides,
  };
}

export function makeNoopApi(): ApiClient {
  return async <T = unknown>(): Promise<T> => {
    const empty: unknown = {};
    return empty as T;
  };
}

export function makeTestTransport(api: ApiClient = makeNoopApi()): TransportService {
  return {
    api,
    post: async (url: string, body?: unknown): Promise<unknown> =>
      api(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body ?? {}),
      }),
    upload: async () => ({ opened: true }),
  };
}

export function makeTestFrozenPanes(
  state: WorkbookState,
  api: ApiClient = makeNoopApi(),
): FrozenPanes {
  const grid = document.getElementById('grid') ?? document.createElement('div');
  return new FrozenPanes({
    state,
    grid,
    rowHead: 26,
    api,
    getRequest: () => ({
      context: '',
      filtered: false,
      filterId: '',
      viewId: '',
      excludeThroughRow: 0,
      displayStart: 1,
      displayEnd: 10,
      visibleStart: 1,
      visibleEnd: 10,
      visibleColStart: 1,
      visibleColEnd: 10,
      row: 1,
      offset: 0,
      limit: 10,
      startCol: 1,
      cols: 10,
      selectedCols: null,
      leadRow: 0,
      leadCol: 0,
    }),
    rowHeight: () => 25,
    colWidth: () => 132,
    rowTop: (r) => (r - 1) * 25,
    colLeft: (c) => (c - 1) * 132,
    headerHeight: () => 27,
    colName: columnName,
    bindResizeGrip: () => undefined,
    getVirtualScrollTop: () => 0,
    paintRowHeaderBorder: () => undefined,
    updateSharedBorders: () => undefined,
    isPrecedent: () => false,
    isSearchMatch: () => false,
    applyCellStyle: () => undefined,
    onRender: () => undefined,
    paintCell: () => undefined,
    onError: () => undefined,
  });
}

export function makeDefaultViewportPipelineDeps(
  overrides: Partial<ViewportPipelineDeps> = {},
): ViewportPipelineDeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const api = overrides.api ?? makeNoopApi();
  const fileFormats = overrides.fileFormats ?? createFileFormats(makeTestFileFormatsConfig());
  const geometry = overrides.geometry ?? new Geometry(1_048_576, 16_384);
  const mergedCells = overrides.mergedCells ?? new MergedCells();
  const virtualScroll = overrides.virtualScroll ?? new VirtualScroll();
  const motion = overrides.motion ?? new ScrollMotion();
  const tiles = overrides.tiles ?? new TileCache(24 * 1024 * 1024);
  const frozenPanes = makeTestFrozenPanes(S, api);
  const panels: ViewportPipelinePanels = overrides.panels ?? {};
  return {
    S,
    api,
    fileFormats,
    geometry,
    mergedCells,
    virtualScroll,
    motion,
    tiles,
    toast: () => undefined,
    colName: columnName,
    sourceRowLimit: () => 1_048_576,
    sourceColLimit: () => 16_384,
    getFrozenPanes: () => frozenPanes,
    selectedColumnSpan: () => null,
    paintColumnSelection: () => undefined,
    selectColumnSpan: () => undefined,
    captureColumnSelection: () => undefined,
    crosshairCell: () => S.selected?.crosshair ?? S.selected ?? null,
    selectionHighlight: (areas) => MultiSelection.highlight(S, S.selected, areas),
    isPrecedent: () => false,
    isSearchMatch: () => false,
    updateSearchNavButtons: () => undefined,
    syncFilteredSelection: () => undefined,
    syncTableColumnHeaders: () => undefined,
    decorateTableColumnHeader: () => false,
    finishSheetSwitch: () => undefined,
    startConditionalStats: async () => undefined,
    panels,
    ...overrides,
  };
}

export function makeDefaultNavigationDeps(
  overrides: Partial<GridNavigationDeps> = {},
): GridNavigationDeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const api = overrides.api ?? makeNoopApi();
  const fileFormats = overrides.fileFormats ?? createFileFormats(makeTestFileFormatsConfig());
  const mergedCells = overrides.mergedCells ?? new MergedCells();
  const frozenPanes = makeTestFrozenPanes(S, api);
  return {
    S,
    api,
    fileFormats,
    mergedCells,
    toast: () => undefined,
    setStatus: () => undefined,
    colName: columnName,
    parseColumn: columnNumber,
    parseRef: (value) => parseRef(value, 1_048_576, 16_384),
    sourceRowLimit: () => 1_048_576,
    sourceColLimit: () => 16_384,
    rowVisible: () => true,
    nextVisibleRow: (r) => r,
    previousVisibleRow: (r) => r,
    advanceVisibleRow: (r, d) => Math.max(1, r + d),
    nextVisibleColumn: (c) => c,
    previousVisibleColumn: (c) => c,
    headerHeight: () => 27,
    rowHead: () => 36,
    sourceRowTop: (r) => (r - 1) * 25,
    rowTop: (r) => (r - 1) * 25,
    rowHeight: () => 25,
    colLeft: (c) => (c - 1) * 80,
    colWidth: () => 80,
    getVirtualScrollTop: () => 0,
    scrollTopToPhysical: (v) => v,
    activeMerge: () => null,
    ensureExtent: () => undefined,
    getViewportRequest: () => ({
      context: '',
      filtered: false,
      filterId: '',
      viewId: '',
      excludeThroughRow: 0,
      displayStart: 1,
      displayEnd: 20,
      visibleStart: 1,
      visibleEnd: 20,
      visibleColStart: 1,
      visibleColEnd: 10,
      row: 1,
      offset: 0,
      limit: 20,
      startCol: 1,
      cols: 10,
      selectedCols: null,
      leadRow: 0,
      leadCol: 0,
    }),
    windowContains: () => true,
    loadViewport: async () => undefined,
    applySelectionClasses: () => undefined,
    paintColumnSelection: () => undefined,
    showWorkspace: () => undefined,
    selectSheet: async () => true,
    getFrozenPanes: () => frozenPanes,
    panels: {},
    ...overrides,
  };
}

export function makeDefaultSheetControllerDeps(
  overrides: Partial<SheetControllerDeps> = {},
): SheetControllerDeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const api = overrides.api ?? makeNoopApi();
  const fileFormats = overrides.fileFormats ?? createFileFormats(makeTestFileFormatsConfig());
  const geometry = overrides.geometry ?? new Geometry(1_048_576, 16_384);
  const mergedCells = overrides.mergedCells ?? new MergedCells();
  const tiles = overrides.tiles ?? new TileCache(24 * 1024 * 1024);
  const frozenPanes = makeTestFrozenPanes(S, api);
  const workbookIO = new WorkbookIO(makeDefaultWorkbookIODeps({ S, api, fileFormats }));
  const panels: SheetControllerPanels = {
    searchPanel: {
      leavePages: () => undefined,
      buildHits: () => undefined,
      applyHighlightClasses: () => undefined,
      displaySummary: () => undefined,
      renderResults: () => undefined,
      syncMode: () => undefined,
      poll: async () => undefined,
    },
    exportCenter: {
      adoptRecovered: () => false,
    },
    ...overrides.panels,
  };
  return {
    S,
    api,
    fileFormats,
    geometry,
    mergedCells,
    tiles,
    toast: () => undefined,
    setStatus: () => undefined,
    colName: columnName,
    parseColumn: columnNumber,
    parseRef: (value) => parseRef(value, 1_048_576, 16_384),
    sourceRowLimit: () => 50_000_000,
    sourceColLimit: () => 16_384,
    fontExists: () => false,
    setDisplayFontSelection: () => undefined,
    rowHeight: () => 25,
    colWidth: () => 132,
    headerHeight: () => 27,
    rowHead: () => 26,
    sourceRowTop: (r) => (r - 1) * 25,
    rowTop: (r) => (r - 1) * 25,
    rowTotal: (n) => n * 25,
    colTotal: (n) => n * 132,
    rowAtOffset: (y) => Math.max(1, Math.floor(y / 25) + 1),
    rowVisible: () => true,
    nextVisibleRow: (r) => r,
    previousVisibleRow: (r) => r,
    filteredBodyCount: () => S.rowView?.totalRows ?? S.filterTotal,
    getVirtualScrollTop: () => 0,
    setVirtualScrollTop: () => undefined,
    updateGeometryBase: () => undefined,
    updateCanvasSize: () => undefined,
    resetGridDOM: () => undefined,
    renderGridFromCurrentWindow: () => undefined,
    invalidateViewport: () => undefined,
    loadViewport: async () => undefined,
    viewportKey: () => S.sheet ?? '',
    paintInitialPreview: async () => undefined,
    paintRangeFrames: () => undefined,
    applyColumnVisibility: () => undefined,
    openDialog: () => undefined,
    closeDialog: () => undefined,
    showWorkspace: () => undefined,
    getCell: async (_sheet, ref) => makeCellData(1, '', { ref }),
    cellAt: (_r, c) => makeCellData(c),
    selectCell: (cell, row, col, ref, visual) => {
      S.selected = {
        ...(cell ?? makeCellData(col, '', { ref })),
        ref,
        row,
        col,
        sheet: S.sheet ?? '',
      };
      S.selectedVisualIndex = visual;
    },
    selectInitialCell: () => undefined,
    navigateToCell: async () => true,
    getFrozenPanes: () => frozenPanes,
    getWorkbookIO: () => workbookIO,
    delay: () => Promise.resolve(),
    ...overrides,
    panels,
  };
}

export function makeDefaultWorkbookIODeps(
  overrides: Partial<WorkbookIODeps> = {},
): WorkbookIODeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const api = overrides.api ?? makeNoopApi();
  const fileFormats = overrides.fileFormats ?? createFileFormats(makeTestFileFormatsConfig());
  return {
    S,
    api,
    transport: makeTestTransport(api),
    toast: () => undefined,
    fmtBytes: (n) => `${Number(n ?? 0)} B`,
    fileFormats,
    setStatus: () => undefined,
    showWelcome: () => undefined,
    showWorkspace: () => undefined,
    loadStylePalette: async () => undefined,
    selectSheet: async () => true,
    selectSheetTab: async () => true,
    refreshSearchHistory: async () => undefined,
    invalidateViewport: () => undefined,
    cancelFilter: async () => undefined,
    panels: {},
    ...overrides,
  };
}

export function makeDefaultGridControlsDeps(
  overrides: Partial<GridControlsDeps> = {},
): GridControlsDeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const frozenPanes = makeTestFrozenPanes(S);
  return {
    S,
    icon: () => '',
    toast: () => undefined,
    colName: columnName,
    parseRef: (value) => parseRef(value, 1_048_576, 16_384),
    cellAt: (_r, c) => makeCellData(c),
    inSelectedRange: () => false,
    linkDescription: () => '',
    selectedColumnSpan: () => null,
    selectColumnSpan: () => undefined,
    hasColumnOfType: () => false,
    selectPointedCell: () => undefined,
    rowHeight: () => Math.round((25 * S.zoom) / 100),
    colWidth: () => Math.round((132 * S.zoom) / 100),
    headerHeight: () => Math.round((27 * S.zoom) / 100),
    rowTop: (r) => (r - 1) * 25,
    colLeft: (c) => (c - 1) * 132,
    rowHead: () => 26,
    resetGridDOM: () => undefined,
    updateCanvasSize: () => undefined,
    renderInitialPreview: () => undefined,
    paintRangeFrames: () => undefined,
    invalidateViewport: () => undefined,
    setVirtualScrollTop: () => undefined,
    scheduleViewport: () => undefined,
    getFrozenPanes: () => frozenPanes,
    isDragging: () => false,
    ...overrides,
  };
}

export function makeDefaultGridInteractionsDeps(
  overrides: Partial<GridInteractionsDeps> = {},
): GridInteractionsDeps {
  const S = overrides.S ?? createInitialWorkbookState(1_048_576, 16_384);
  const api = overrides.api ?? makeNoopApi();
  const fileFormats = createFileFormats(makeTestFileFormatsConfig());
  const geometry = new Geometry(1_048_576, 16_384);
  const mergedCells = new MergedCells();
  const tiles = new TileCache(24 * 1024 * 1024);
  const virtualScroll = new VirtualScroll();
  const motion = new ScrollMotion();
  const frozenPanes = makeTestFrozenPanes(S, api);
  const controls = new GridControls(makeDefaultGridControlsDeps({ S }));
  const workbookIO = new WorkbookIO(makeDefaultWorkbookIODeps({ S, api, fileFormats }));
  const clipboard = new GridClipboard({
    S,
    api,
    fileFormats,
    mergedCells,
    toast: () => undefined,
    colName: columnName,
    getFrozenPanes: () => frozenPanes,
    sourceRowLimit: () => 1_048_576,
  });
  const autoFilterUI = new AutoFilterDropdown({
    state: S,
    api,
    colName: columnName,
    toast: () => undefined,
    onApply: async () => undefined,
  });
  const columnController = new ColumnController({
    state: S,
    api,
    toast: () => undefined,
    geometry,
    baseRow: 25,
    baseCol: 132,
    colName: columnName,
    parseColumn: columnNumber,
    colLimit: () => 16_384,
    relayoutGrid: () => undefined,
    selectCellByAddress: () => undefined,
    hideCellMenu: () => undefined,
    selectionStats: () => null,
    calculator: () => null,
  });
  return {
    S,
    api,
    toast: () => undefined,
    setStatus: () => undefined,
    colName: columnName,
    parseRef: (value) => parseRef(value, 1_048_576, 16_384),
    cellAt: (_r, c) => makeCellData(c),
    tiles,
    mergedCells,
    virtualScroll,
    motion,
    controls,
    clipboard,
    workbookIO,
    panels: {
      frozenPanes,
      recentManager: { load: () => undefined },
      profileCenter: { open: () => undefined },
      exportCenter: { open: () => undefined },
      copySpecial: { open: () => undefined },
      bookmarkCenter: { open: () => undefined },
      autoFilterUI,
      selectionInspector: { open: () => undefined },
      multiSort: { show: () => undefined },
      dataRulesCenter: { open: () => undefined },
      catalogueCenter: { open: () => undefined },
      taskRecovery: { open: () => undefined },
      searchPanel: {},
      commandPalette: { bind: () => undefined },
      columnController,
      databasePanel: { open: () => undefined },
      databaseSnapshots: { show: () => undefined },
    },
    rowHead: () => 26,
    rowHeight: () => 25,
    colWidth: () => 132,
    headerHeight: () => 27,
    rowTop: (r) => (r - 1) * 25,
    colLeft: (c) => (c - 1) * 132,
    getVirtualScrollTop: () => 0,
    getViewportRequest: () => ({
      context: '',
      filtered: false,
      filterId: '',
      viewId: '',
      excludeThroughRow: 0,
      displayStart: 1,
      displayEnd: 20,
      visibleStart: 1,
      visibleEnd: 20,
      visibleColStart: 1,
      visibleColEnd: 10,
      row: 1,
      offset: 0,
      limit: 20,
      startCol: 1,
      cols: 10,
      selectedCols: null,
      leadRow: 0,
      leadCol: 0,
    }),
    viewportContext: () => '',
    activeMerge: () => null,
    selectCell: () => undefined,
    extendRange: () => undefined,
    applySelectionClasses: () => undefined,
    selectWholeRow: () => undefined,
    selectColumnSpan: () => undefined,
    captureColumnSelection: () => undefined,
    selectSheet: async () => true,
    selectSheetTab: async () => true,
    navigateToCell: async () => true,
    navigateNameBox: async () => undefined,
    moveSelection: async () => undefined,
    jumpDataEdge: async () => undefined,
    goHistory: async () => undefined,
    followCellHyperlink: async () => undefined,
    showWelcome: () => undefined,
    showCellDetail: () => undefined,
    openCommandPalette: () => undefined,
    openSearch: () => undefined,
    stepSearch: () => undefined,
    toggleAutoFilter: async () => undefined,
    toggleFormulaView: () => undefined,
    toggleOriginalFormat: () => undefined,
    changeDisplayFont: async () => undefined,
    openFilter: () => undefined,
    addFilterRule: () => undefined,
    applyFilter: async () => undefined,
    cancelFilter: async () => undefined,
    clearFilter: async () => undefined,
    filterSelectedValue: async () => undefined,
    openFreezeDialog: () => undefined,
    applyFreeze: async () => undefined,
    applyTextTableHeader: async () => undefined,
    disableTextTableHeader: async () => undefined,
    promptResize: async () => undefined,
    commitResize: async () => undefined,
    autoFitSize: async () => undefined,
    setColumnVisibility: async () => undefined,
    tracePrecedents: async () => undefined,
    traceDependents: async () => undefined,
    followGridScroll: () => undefined,
    scheduleRangeFrames: () => undefined,
    syncTableColumnHeaders: () => undefined,
    syncFreezeScrollGutters: () => undefined,
    updateCanvasSize: () => undefined,
    scheduleInitialPreview: () => undefined,
    renderGridFromCurrentWindow: () => undefined,
    paintRangeFrames: () => undefined,
    scheduleViewport: () => undefined,
    ...overrides,
  };
}

export { GridControls, GridInteractions, GridNavigation, SheetController, ViewportPipeline };
