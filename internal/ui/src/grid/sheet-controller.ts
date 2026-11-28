/**
 * Worksheet lifecycle, conditional stats polling, freeze panes, AutoFilter /
 * advanced filter & multi-sort orchestration, text table headers, row-view
 * adoption, named view capture/apply, task recovery adoption, and search execution.
 */
import { interfaceLocale, t as localize } from '../core/i18n.ts';
import { SheetConditionalStats, type ConditionalStatsDeps } from './sheet-conditional-stats.ts';
import type { ActivityHandle, SelectionArea } from '../contracts/common.ts';
import type {
  AxisViewSettings,
  SourceFilterRule,
  SourceFilterSpec,
  SourceSortKey,
} from '../contracts/analysis.ts';
import type {
  NamedView,
  NamedViewConfig,
  TaskCheckpointInfo,
} from '../contracts/features.ts';
import type {
  AutoFilterRule,
  AutoFilterState,
  CellData,
  FilterStatusResponse,
  IndexStartResponse,
  IndexStatusResponse,
  RowViewState,
  SearchStatusResponse,
  SheetLayoutResponse,
  TextTableHeader,
} from '../contracts/workbook.ts';
import { beginActivity } from '../core/activity.ts';
import { errorMessage, icon } from '../core/display-format.ts';
import { requireButton, requireEl, requireInput, requireSelect } from '../core/dom.ts';
import type { AutoFilterDropdown } from './autofilter.ts';
import { estimateConditionalBytes } from './conditional.ts';
import type { Geometry } from './geometry.ts';
import { paintBackdrop } from './grid-backdrop.ts';
import type { WorkbookIO } from './grid-workbook-io.ts';
import type { Pictures } from './images.ts';
import type { MergedCells } from './merges.ts';
import type { ReadingMode } from './reading-mode.ts';
import { estimateRichTextBytes } from './richtext.ts';
import type { ViewportRequest } from './state.ts';
import {
  decorateTableHeader,
  isTableHeader,
  tableHeaderFromSource,
  useStickyTableLabels,
} from './table-headers.ts';
import { estimateViewportBytes, type TileCache } from './viewport-cache.ts';
import {
  decodeCachedJobStart,
  decodeFilterStartResponse,
  decodeFilterStatusResponse,
  decodeFreezeViewResponse,
  decodeIndexStartResponse,
  decodeIndexStatusResponse,
  decodeSheetLayoutResponse,
  decodeTextTableHeader,
} from '../core/http-decoders.generated.ts';

/** @httpResponse CachedJobStart */
export interface CachedJobStart {
  readonly jobId: string;
  readonly cached?: boolean;
}

export interface NamedViewPrepared {
  readonly view: NamedView;
  readonly tableHeader?: TextTableHeader | null;
}

const reread = <T>(read: () => T): T => read();

function isExpiredFilter(error: unknown): boolean {
  return error instanceof Error && 'code' in error && error.code === 'service.filter.expired';
}

export const FILTER_OPERATORS: readonly (readonly [string, string])[] = [
  ['contains', localize('ui.common.including')],
  ['notContains', localize('ui.common.notIncluded')],
  ['equals', localize('ui.common.equal')],
  ['notEquals', localize('ui.common.notEqual')],
  ['startsWith', localize('ui.common.beginning')],
  ['endsWith', localize('ui.common.ending')],
  ['gt', localize('ui.common.greaterThan')],
  ['gte', localize('ui.common.greaterThanOrEqual')],
  ['lt', localize('ui.common.lessThan')],
  ['lte', localize('ui.common.lessThanOrEqual')],
  ['blank', localize('ui.common.ifEmptyThenLeaveEmpty')],
  ['notBlank', localize('ui.common.notEmpty')],
];

export interface SheetControllerPanels {
  readonly editCopy?: {
    readonly grid: {
      readonly active: boolean;
      readonly draft?: unknown;
      controls(): void;
      selection(): void;
      act(): Promise<boolean>;
      readonly entry: {
        focusAfterSwitch(): void;
        switchDraft(name: string): unknown;
      };
    };
  } | null;
  readonly traceUI?: { clearPulse(): void; selectionChanged(): void } | null;
  readonly selectionStats?: { clear(): void } | null;
  readonly formulaInspector?: { clear(): void } | null;
  readonly catalogueCenter?: { controls(): void } | null;
  readonly dataRulesCenter?: { controls(): void; resetBook(): void } | null;
  readonly printingCenter?: { controls(): void; resetBook(): void } | null;
  readonly recordCardCenter?: { controls(): void; resetBook(): void } | null;
  readonly selectionQuick?: { changed(): void; resetBook(): void } | null;
  readonly transposeCenter?: { changed(): void; resetBook(): void } | null;
  readonly cellExplanation?: { changed(): void; resetBook(): void } | null;
  readonly profileCenter?: { changed(): void; resetBook(): void } | null;
  readonly rowViews?: {
    readonly busy: boolean;
    readonly serial: number;
    readonly cache: Map<string, { readonly settings?: AxisViewSettings }>;
    cancel(): void;
    prepare(options: { readonly signal?: AbortSignal; readonly settings?: AxisViewSettings }): Promise<unknown>;
    prepareFilter(
      filterId: string,
      options: { readonly guard?: () => boolean; readonly signal?: AbortSignal },
    ): Promise<RowViewState>;
    build(options: {
      readonly sheet: string;
      readonly filterId: string;
      readonly settings: AxisViewSettings;
      readonly guard?: () => boolean;
    }): Promise<RowViewState>;
    install(data: RowViewState): void;
    validate(data: RowViewState, sheet: string, filterId: string): void;
    cleanup(id: string): void;
  } | null;
  readonly searchPanel: {
    leavePages(): void;
    buildHits(matches: readonly SearchStatusResponse['matches'][number][]): void;
    applyHighlightClasses(): void;
    displaySummary(message: string): void;
    renderResults(data: {
      readonly matches: readonly SearchStatusResponse['matches'][number][];
      readonly status?: string;
      readonly mode: string;
      readonly truncated?: boolean;
    }): void;
    syncMode(): void;
    poll(
      jobID: string,
      requestID: number,
      alreadyCached: boolean,
      activity?: ActivityHandle | null,
    ): Promise<void>;
  };
  readonly exportCenter: {
    adoptRecovered(start: RecoveredTaskStart, item: TaskCheckpointInfo): Promise<boolean> | boolean;
  };
  readonly autoFilterUI?: AutoFilterDropdown | null;
  readonly readingMode?: ReadingMode | null;
  readonly pictures?: Pictures | null;
}

/** @httpResponse RecoveredTaskStart */
export interface RecoveredTaskStart {
  readonly operation: string;
  readonly jobId: string;
  readonly filter?: SourceFilterSpec;
  readonly downloadUrl?: string;
  readonly fileName?: string;
}

export interface SheetControllerDeps extends ConditionalStatsDeps {
  readonly geometry: Geometry;
  readonly mergedCells: MergedCells;
  readonly tiles: TileCache;
  readonly setStatus: (text: string, hidden?: boolean) => void;
  readonly colName: (col: number) => string;
  readonly parseColumn: (text: string) => number;
  readonly parseRef: (text: string | null | undefined) => {
    readonly row: number;
    readonly col: number;
    readonly ref: string;
  } | null;
  readonly sourceRowLimit: () => number;
  readonly sourceColLimit: () => number;
  readonly fontExists: (font: string) => boolean;
  readonly setDisplayFontSelection: (font: string) => void;
  readonly rowHeight: (row?: number) => number;
  readonly colWidth: (col?: number) => number;
  readonly headerHeight: () => number;
  readonly rowHead: () => number;
  readonly sourceRowTop: (row: number) => number;
  readonly rowTop: (row: number) => number;
  readonly rowTotal: (count: number) => number;
  readonly colTotal: (count: number) => number;
  readonly rowAtOffset: (y: number) => number;
  readonly rowVisible: (row: number) => boolean;
  readonly nextVisibleRow: (row: number) => number;
  readonly previousVisibleRow: (row: number) => number;
  readonly filteredBodyCount: () => number;
  readonly getVirtualScrollTop: () => number;
  readonly setVirtualScrollTop: (logical: number) => void;
  readonly updateGeometryBase: () => void;
  readonly updateCanvasSize: () => void;
  readonly viewportKey: (request: ViewportRequest) => string;
  readonly paintInitialPreview: (name: string, epoch: number) => Promise<void>;
  readonly paintRangeFrames: (areas?: readonly SelectionArea[]) => void;
  readonly applyColumnVisibility: () => void;
  readonly openDialog: (id: string) => void;
  readonly closeDialog: (id: string) => void;
  readonly showWorkspace: () => void;
  readonly cellAt: (row: number, col: number) => CellData;
  readonly selectInitialCell: () => void;
  readonly navigateToCell: (
    sheet: string,
    ref: string,
    preserveTrace?: boolean,
    guard?: (() => boolean) | null,
  ) => Promise<boolean>;
  readonly getWorkbookIO: () => WorkbookIO;
  readonly delay?: (ms: number) => Promise<void>;
  readonly panels: SheetControllerPanels;
}

const delay = (ms: number): Promise<void> =>
  new Promise((resolve) => {
    setTimeout(resolve, ms);
  });

export function searchKey(mode: string, scope: string, query: string, sheet: string): string {
  const text = query.trim();
  return [
    mode,
    scope,
    mode === 'advanced' ? text : text.toLowerCase(),
    scope === 'all' ? '*' : sheet,
  ].join('\u0000');
}

export class SheetController {
  readonly conditional: SheetConditionalStats;
  private readonly d: SheetControllerDeps;

  constructor(deps: SheetControllerDeps) {
    this.d = deps;
    this.conditional = new SheetConditionalStats(deps);
  }

  private sheetPresentationKey(): string {
    const { S } = this.d;
    return [S.zoom, S.showStyles, S.showFormulas, window.devicePixelRatio || 1].join('|');
  }

  rememberSheetView(): void {
    const { S, getFrozenPanes, panels } = this.d;
    if (panels.editCopy?.grid.active) return;
    if (
      !S.sheet ||
      !S.indexReady ||
      !S.index ||
      !S.sheetLayout ||
      !S.viewportData ||
      S.filterJob ||
      S.viewportData.data.conditionalStatsPending
    ) {
      return;
    }
    const layout: SheetLayoutResponse = {
      ...S.sheetLayout,
      rows: S.rowSizes,
      cols: S.columnWidths,
      font: S.fontOverride,
      columnMode: S.columnMode,
      columnVisibility: S.columnOverrides,
    };
    const frozen = new Map([...getFrozenPanes().cache].slice(-3));
    let nodes = S.renderedRows.size;
    let retainedText = 0;
    const textBytes = (cell: CellData | null | undefined): number =>
      2 * ((cell?.raw.length ?? 0) + (cell?.display.length ?? 0) + (cell?.formula?.length ?? 0));
    for (const row of S.renderedRows.values()) {
      nodes += row._xlCells?.size ?? 0;
      for (const cell of row._xlCells?.values() ?? []) {
        nodes += (cell.childElementCount || 0) + (cell._xlRichNode?.childNodes.length ?? 0) * 2;
        nodes +=
          (cell._xlborderRightParts?.childElementCount ?? 0) +
          (cell._xlborderBottomParts?.childElementCount ?? 0);
        retainedText +=
          textBytes(cell._xlModel) +
          estimateRichTextBytes(cell._xlModel?.richText) +
          estimateConditionalBytes(cell._xlModel?.conditional);
      }
    }
    const bytes =
      estimateViewportBytes(S.viewportData.data) +
      [...frozen.values()].reduce((n, data) => n + estimateViewportBytes(data), 0) +
      512 +
      nodes * 1024 +
      retainedText +
      textBytes(S.selected) +
      estimateRichTextBytes(S.selected?.richText) +
      estimateConditionalBytes(S.selected?.conditional) +
      (layout.sourceMerges?.length ?? 0) * 224 +
      (layout.sourceRows?.length ?? 0) * 128 +
      (Object.keys(layout.rows ?? {}).length +
        Object.keys(layout.cols ?? {}).length +
        Object.keys(layout.sourceCols ?? {}).length) *
        96;
    const old = S.sheetViews.get(S.sheet);
    if (old) S.sheetViewsBytes -= old.bytes;
    S.sheetViews.delete(S.sheet);
    if (bytes > 16 * 1024 * 1024) return;
    S.sheetViews.set(S.sheet, {
      bytes,
      index: S.index,
      layout,
      freeze: { ...S.freeze },
      entry: S.viewportData,
      frozen,
      selected: S.selected,
      filterId: S.filterId,
      maxRow: S.maxRow,
      maxCol: S.maxCol,
      rows: S.renderedRows,
      presentation: this.sheetPresentationKey(),
      hasConditionalData: !S.tilesSkippedConditional,
      conditionalStatsReady:
        S.conditionalStatsState === 'done' || S.conditionalStatsState === 'not_needed',
    });
    S.sheetViewsBytes += bytes;
    while (S.sheetViews.size > 6 || S.sheetViewsBytes > 16 * 1024 * 1024) {
      const oldestKey = S.sheetViews.keys().next().value;
      if (oldestKey === undefined) break;
      const oldest = S.sheetViews.get(oldestKey);
      if (oldest) S.sheetViewsBytes -= oldest.bytes;
      S.sheetViews.delete(oldestKey);
    }
  }

  finishSheetSwitch(epoch: number, ready = true): void {
    const { S, panels } = this.d;
    if (epoch !== S.sheetEpoch) return;
    S.sheetLoadingEpoch = 0;
    const grid = requireEl('grid');
    grid.inert = !ready;
    grid.parentElement?.classList.remove('sheet-switching');
    panels.editCopy?.grid.controls();
    if (ready && panels.editCopy?.grid.active) {
      panels.editCopy.grid.selection();
      panels.editCopy.grid.entry.focusAfterSwitch();
    }
    this.refreshReadControls();
  }

  selectSheetTab(name: string): Promise<boolean | undefined> {
    const editor = this.d.panels.editCopy?.grid;
    const draft = editor?.active ? editor.entry.switchDraft(name) : null;
    return this.selectSheet(name, '', false, null, null, draft);
  }

  private async pollIndex(
    jobID: string,
    epoch: number,
    activity: ActivityHandle | null | undefined,
  ): Promise<IndexStatusResponse | null> {
    const { S, api, getWorkbookIO } = this.d;
    while (epoch === S.sheetEpoch) {
      await delay(320);
      const progress = await api(
        `/api/index/status?id=${encodeURIComponent(jobID)}`,
        undefined,
        decodeIndexStatusResponse,
      );
      if (epoch !== S.sheetEpoch) return null;
      getWorkbookIO().reportIndexProgress(activity, progress);
      if (progress.status === 'error') throw new Error(progress.error || localize('ui.sheetController.indexCreationFailed'));
      if (progress.status === 'cancelled') throw new Error(localize('ui.sheetController.indexTaskCancelled'));
      if (progress.status === 'done') return progress;
    }
    return null;
  }

  async selectSheet(
    name: string,
    targetRef = '',
    preserveTrace = false,
    navigationGuard: (() => boolean) | null = null,
    openingActivity: ActivityHandle | null = null,
    retainedDraft: unknown = null,
  ): Promise<boolean | undefined> {
    const {
      S,
      api,
      geometry,
      mergedCells,
      tiles,
      toast,
      setStatus,
      colName,
      parseRef,
      sourceRowLimit,
      sourceColLimit,
      fontExists,
      setDisplayFontSelection,
      rowVisible,
      filteredBodyCount,
      getVirtualScrollTop,
      setVirtualScrollTop,
      updateGeometryBase,
      updateCanvasSize,
      invalidateViewport,
      loadViewport,
      viewportKey,
      paintInitialPreview,
      paintRangeFrames,
      applyColumnVisibility,
      getCell,
      cellAt,
      selectCell,
      selectInitialCell,
      navigateToCell,
      getFrozenPanes,
      getWorkbookIO,
      panels,
    } = this.d;

    if (
      panels.editCopy?.grid.active &&
      panels.editCopy.grid.draft &&
      panels.editCopy.grid.draft !== retainedDraft &&
      !(await panels.editCopy.grid.act())
    ) {
      return false;
    }
    if (navigationGuard && !navigationGuard()) return undefined;
    if (!name || !S.opened) return undefined;
    if (S.sheet === name && S.indexReady) {
      return targetRef ? navigateToCell(name, targetRef, preserveTrace, navigationGuard) : true;
    }
    panels.dataRulesCenter?.resetBook();
    panels.printingCenter?.resetBook();
    panels.recordCardCenter?.resetBook();
    panels.selectionQuick?.resetBook();
    panels.transposeCenter?.resetBook();
    panels.cellExplanation?.resetBook();
    panels.profileCenter?.resetBook();

    const grid = requireEl('grid');
    if (S.sheet && S.indexReady) {
      S.positionsBySheet.set(S.sheet, {
        top: getVirtualScrollTop(),
        left: grid.scrollLeft,
        ref: S.selected?.ref ?? 'A1',
        visual: S.selectedVisualIndex,
        frozenThrough: S.selectedFrozenThrough,
      });
      this.rememberSheetView();
    }
    const previousView = S.positionsBySheet.get(name);
    if (preserveTrace) panels.traceUI?.clearPulse();
    else panels.traceUI?.selectionChanged();
    let warm = S.sheetViews.get(name) ?? null;
    if (warm && warm.filterId !== (S.filtersBySheet.get(name) ?? '')) warm = null;
    S.sheetLoadAbort?.abort();
    const opening = new AbortController();
    S.sheetLoadAbort = opening;
    const workbookIO = getWorkbookIO();
    if (S.indexJob) void workbookIO.cancelIndex(S.indexJob);
    if (S.filterJob) void this.cancelFilter(S.filterJob);
    this.conditional.cancelConditionalStats(true, false);
    S.tilesSkippedConditional = false;
    invalidateViewport({ preserveDOM: true });
    S.reusableSheetRows = warm?.presentation === this.sheetPresentationKey() ? warm.rows : null;
    S.reusableSheetData = S.reusableSheetRows && warm ? warm.entry.data : null;
    S.navigationAbort?.abort();
    ++S.navigationTicket;
    const epoch = ++S.sheetEpoch;
    panels.rowViews?.cancel();
    S.rowView = null;
    S.rowVisibility = null;
    S.rowViewFailure = false;
    geometry.rows.setHidden([]);
    S.sheetLoadingEpoch = epoch;
    panels.editCopy?.grid.controls();
    grid.inert = true;
    grid.parentElement?.classList.add('sheet-switching');
    panels.pictures?.reset();
    S.sheet = name;
    S.sourceRowSizes = [];
    S.sourceColumnWidths = {};
    S.sourceDefaultRow = 0;
    S.sourceDefaultCol = 0;
    S.sourceGridLines = S.source?.format !== 'xls-html';
    this.conditional.updateSourceAppearance();
    S.index = null;
    S.indexReady = false;
    S.sheetLayout = null;
    mergedCells.load([]);
    S.window = null;
    const frozenPanes = getFrozenPanes();
    frozenPanes.reset();
    S.freeze = { rows: 0, cols: 0 };
    setDisplayFontSelection('');
    S.cellMap.clear();
    S.rowNumberByVisual.clear();
    if (!preserveTrace) S.traceRefs = [];
    S.filterId = S.filtersBySheet.get(name) ?? '';
    S.filtered = Boolean(S.filterId);
    S.filterTotal = S.filterTotalsBySheet.get(name) ?? 0;
    if (S.source?.format === 'txt' && !S.autoFiltersBySheet.has(name)) {
      S.autoFiltersBySheet.set(name, tableHeaderFromSource(S.source.tableHeader));
    }
    S.autoFilter = S.autoFiltersBySheet.get(name) ?? null;
    ++S.textHeaderRevision;
    S.selected = null;
    S.wholeRowSelected = false;
    S.range = null;
    S.multiRanges = [];
    S.rangeAnchor = null;
    paintRangeFrames([]);
    S.columnSelection = null;
    S.columnAnchor = 0;
    panels.selectionStats?.clear();
    S.selectedVisualIndex = 1;
    requireInput('nameBox').value = '';
    panels.formulaInspector?.clear();
    requireEl('formulaValue').textContent = '-';
    requireButton('tracePrecedentsBtn').disabled = true;
    requireButton('traceDependentsBtn').disabled = true;
    requireButton('cellDetailBtn').disabled = true;
    requireEl('filterStatus').hidden = !S.filtered;
    requireButton('clearFilterBtn').disabled = !S.filtered;
    this.updateAutoFilterButton();
    requireEl('gridEmpty').hidden = true;
    requireEl('indexOverlay').hidden = true;
    S.previewOnly = false;
    requireEl('retryIndexBtn').hidden = true;
    requireEl('showRowsRecoveryBtn').hidden = true;
    workbookIO.renderSheetTabs();
    setStatus(localize('ui.sheetController.opening', { name }));
    const activity =
      openingActivity ?? beginActivity(localize('ui.common.preparingScheduleProgress'), { priority: 2, signal: opening.signal });
    const onOpeningAbort = (): void => {
      activity?.end();
    };
    if (openingActivity) opening.signal.addEventListener('abort', onOpeningAbort, { once: true });
    activity?.stage(localize('ui.sheetController.readingWorksheets'), openingActivity ? 65 : 0, 85);
    try {
      const start: IndexStartResponse = warm
        ? { ...(warm.index as IndexStartResponse), ready: true }
        : await api('/api/index/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ sheet: name }),
            signal: opening.signal,
          }, decodeIndexStartResponse);
      if (epoch !== S.sheetEpoch) return undefined;
      if (start.ready) {
        S.index = start;
      } else {
        S.indexJob = start.jobId;
        activity?.onCancel(() => {
          void workbookIO.cancelIndex(start.jobId);
        });
        void paintInitialPreview(name, epoch);
        S.index = await this.pollIndex(start.jobId, epoch, activity);
      }
      if (epoch !== S.sheetEpoch || !S.index) return undefined;
      S.indexJob = null;
      if (reread(() => S.previewOnly)) {
        for (const row of S.renderedRows.values()) row._xlSheetEpoch = -1;
      }
      S.previewAbort?.abort();
      S.previewAbort = null;
      S.previewOnly = false;
      S.previewData = null;
      cancelAnimationFrame(S.previewFrame);
      S.previewFrame = 0;
      activity?.stage(localize('ui.sheetController.readingWorksheetLayoutProgress'), 85, 95);
      S.usedMaxRow = Math.max(1, S.index.maxRow ?? 1);
      S.usedMaxCol = Math.max(1, S.index.maxCol ?? 1);
      S.dimension =
        S.index.usedDimension || S.index.dimension || `A1:${colName(S.usedMaxCol)}${S.usedMaxRow}`;
      try {
        const sizes =
          warm?.layout ??
          (await api(`/api/layout?sheet=${encodeURIComponent(name)}`, {
            signal: opening.signal,
          }, decodeSheetLayoutResponse));
        if (epoch !== S.sheetEpoch) return undefined;
        S.sheetLayout = sizes;
        mergedCells.load(sizes.sourceMerges);
        S.rowSizes = { ...(sizes.rows ?? {}) };
        S.columnWidths = { ...(sizes.cols ?? {}) };
        S.sourceRowSizes = sizes.sourceRows ? [...sizes.sourceRows] : [];
        S.sourceColumnWidths = { ...(sizes.sourceCols ?? {}) };
        S.sourceGridLines = sizes.sourceGridLines ?? S.source?.format !== 'xls-html';
        S.sourceDefaultRow = sizes.sourceDefaultRow ?? 0;
        S.sourceDefaultCol = sizes.sourceDefaultCol ?? 0;
        this.conditional.updateSourceAppearance();
        S.columnSourceRanges = sizes.sourceHidden ? [...sizes.sourceHidden] : [];
        S.columnMode = sizes.columnMode || 'original';
        S.columnOverrides = { ...(sizes.columnVisibility ?? {}) };
        applyColumnVisibility();
        setDisplayFontSelection(sizes.font && fontExists(sizes.font) ? sizes.font : '');
      } catch (error: unknown) {
        if (epoch !== S.sheetEpoch || opening.signal.aborted) return undefined;
        mergedCells.load([]);
        S.rowSizes = {};
        S.columnWidths = {};
        S.columnSourceRanges = [];
        S.columnMode = 'original';
        S.columnOverrides = {};
        applyColumnVisibility();
        setDisplayFontSelection('');
        toast(localize('ui.sheetController.failedReadColumnLayout', { detail: errorMessage(error) }), 'error');
      }
      try {
        const view =
          warm?.freeze ??
          (await api(`/api/view?sheet=${encodeURIComponent(name)}`, {
            signal: opening.signal,
          }, decodeFreezeViewResponse));
        if (epoch !== S.sheetEpoch) return undefined;
        S.freeze = { rows: view.rows || 0, cols: view.cols || 0 };
      } catch {
        if (epoch !== S.sheetEpoch || opening.signal.aborted) return undefined;
        S.freeze = { rows: 0, cols: 0 };
      }
      if (epoch !== S.sheetEpoch) return undefined;
      S.indexReady = true;
      this.updateAutoFilterButton();
      this.updateFreezeUI();
      S.maxRow = Math.min(
        sourceRowLimit(),
        Math.max(S.usedMaxRow + 18, Math.ceil(grid.clientHeight / 25) + 4),
      );
      S.maxCol = Math.min(
        sourceColLimit(),
        Math.max(S.usedMaxCol + 3, Math.ceil(grid.clientWidth / 132) + 2),
      );
      if (warm) {
        S.maxRow = Math.max(S.maxRow, warm.maxRow);
        S.maxCol = Math.min(sourceColLimit(), Math.max(S.maxCol, warm.maxCol));
      }
      if (panels.rowViews) {
        const previousRows = panels.rowViews.cache.get(name);
        try {
          await panels.rowViews.prepare({ signal: opening.signal });
        } catch (error: unknown) {
          if (epoch !== S.sheetEpoch || opening.signal.aborted) return undefined;
          if (!S.filtered || !isExpiredFilter(error)) throw error;
          S.filtersBySheet.delete(name);
          S.filterTotalsBySheet.delete(name);
          S.filterId = '';
          S.filtered = false;
          S.filterTotal = 0;
          if (S.autoFilter) {
            S.autoFilter.rules.clear();
            S.autoFilter.sortColumn = 0;
            S.autoFilter.descending = false;
          }
          requireEl('filterStatus').hidden = true;
          requireButton('clearFilterBtn').disabled = true;
          await panels.rowViews.prepare({
            signal: opening.signal,
            settings: previousRows?.settings,
          });
          if (epoch !== S.sheetEpoch) return undefined;
          toast(localize('ui.sheetController.previousFilterResultsHaveBecome'), 'warning');
        }
        if (epoch !== S.sheetEpoch) return undefined;
        const preparedVisibility = this.d.S.rowVisibility;
        if (preparedVisibility) geometry.rows.setHidden(preparedVisibility.hidden);
        if (S.filtered) S.filterTotal = filteredBodyCount();
      }
      updateGeometryBase();
      updateCanvasSize();
      panels.pictures
        ?.load(name, epoch)
        .then(() => panels.pictures?.schedule())
        .catch((error: unknown) => {
          if (epoch === S.sheetEpoch) toast(localize('ui.sheetController.imageReadingFailed', { detail: errorMessage(error) }), 'error');
        });
      if (previousView && !targetRef) {
        setVirtualScrollTop(previousView.top);
        grid.scrollLeft = previousView.left;
      } else {
        setVirtualScrollTop(0);
        grid.scrollLeft = 0;
      }
      const activeRowView = this.d.S.rowView;
      if (
        warm &&
        (!S.showStyles || warm.hasConditionalData) &&
        (!activeRowView || warm.entry.request.viewId === activeRowView.id)
      ) {
        for (const [key, data] of warm.frozen) frozenPanes.cachePart(key, data);
        tiles.store(viewportKey(warm.entry.request), warm.entry.data, warm.entry.request);
      }
      try {
        activity?.stage(localize('ui.sheetController.displayingCells'), 95, 99);
        await loadViewport(true, epoch, true);
      } catch (error: unknown) {
        if (S.filtered && isExpiredFilter(error)) {
          if (S.sheet) {
            S.filtersBySheet.delete(S.sheet);
            S.filterTotalsBySheet.delete(S.sheet);
          }
          S.filterId = '';
          S.filtered = false;
          S.filterTotal = 0;
          requireEl('filterStatus').hidden = true;
          requireButton('clearFilterBtn').disabled = true;
          updateCanvasSize();
          await loadViewport(true, epoch, true);
        } else {
          throw error;
        }
      }
      if (epoch !== S.sheetEpoch) return undefined;
      requireEl('indexOverlay').hidden = true;
      setStatus(localize('ui.sheetController.opened', { name }), true);
      let arrived = true;
      if (targetRef) {
        try {
          arrived = await navigateToCell(name, targetRef, preserveTrace, navigationGuard);
        } catch (error: unknown) {
          if (epoch !== S.sheetEpoch) return false;
          toast(errorMessage(error), 'error');
          arrived = false;
        }
        if (epoch !== S.sheetEpoch) return false;
        if (!arrived && !reread(() => S.selected)) selectInitialCell();
      } else if (previousView?.ref) {
        const ref = parseRef(previousView.ref);
        if (
          ref &&
          rowVisible(ref.row) &&
          S.filtered &&
          (!previousView.visual || previousView.frozenThrough !== frozenPanes.effective().rows)
        ) {
          if (!(await navigateToCell(name, ref.ref)) && epoch === S.sheetEpoch && !reread(() => S.selected)) {
            selectInitialCell();
          }
        } else if (ref && rowVisible(ref.row)) {
          const cell =
            S.cellMap.get(ref.ref) ??
            S.frozenCellMap.get(ref.ref) ??
            (warm?.selected?.ref === ref.ref ? warm.selected : null) ??
            (await getCell(name, ref.ref).catch(() => cellAt(ref.row, ref.col)));
          if (epoch !== S.sheetEpoch) return undefined;
          selectCell(cell, ref.row, ref.col, ref.ref, previousView.visual || ref.row);
          grid.focus({ preventScroll: true });
        } else {
          selectInitialCell();
        }
      } else {
        selectInitialCell();
      }
      if (warm) await frozenPanes.refresh(true);
      else frozenPanes.schedule(true);
      if (epoch !== S.sheetEpoch) return undefined;
      this.finishSheetSwitch(epoch);
      if (S.showStyles && (!warm?.conditionalStatsReady || !warm.hasConditionalData)) {
        void this.conditional.startConditionalStats(name, epoch, {
          refreshOnReady: Boolean(S.viewportData?.data.conditionalStatsPending),
        });
      } else if (warm?.conditionalStatsReady) {
        S.conditionalStatsState = 'done';
      }
      return arrived;
    } catch (error: unknown) {
      if (epoch !== S.sheetEpoch) return undefined;
      S.previewAbort?.abort();
      S.previewOnly = false;
      requireEl('indexOverlay').hidden = false;
      S.indexReady = false;
      this.finishSheetSwitch(epoch, false);
      requireEl('indexTitle').textContent = localize('ui.sheetController.readFailure', { name });
      requireEl('indexDetail').textContent = errorMessage(error);
      requireEl('retryIndexBtn').hidden = false;
      requireEl('showRowsRecoveryBtn').hidden = !S.rowViewFailure;
      setStatus(localize('ui.sheetController.readingFailure'));
      toast(errorMessage(error), 'error');
      return undefined;
    } finally {
      if (openingActivity) opening.signal.removeEventListener('abort', onOpeningAbort);
      else {
        await activity?.paintIfDue();
        activity?.end();
      }
    }
  }

  refreshReadControls(): void {
    const { panels } = this.d;
    panels.catalogueCenter?.controls();
    panels.dataRulesCenter?.controls();
    panels.printingCenter?.controls();
    panels.recordCardCenter?.controls();
    panels.selectionQuick?.changed();
    panels.transposeCenter?.changed();
    panels.cellExplanation?.changed();
    panels.profileCenter?.changed();
  }

  updateFreezeUI(): void {
    const { S } = this.d;
    this.refreshReadControls();
    const rows = S.freeze.rows;
    const cols = S.freeze.cols;
    const label =
      !rows && !cols ? localize('ui.sheetController.freeze') : localize('ui.sheetController.freeze0dbdef2', { detail: rows ? localize('ui.sheetController.row', { rows }) : '', detail2: cols ? ` ${localize('ui.sheetController.column', { cols })}` : '' });
    requireEl('freezeBtnLabel').textContent = label;
    const status = requireEl('freezeStatus');
    status.hidden = !rows && !cols;
    status.textContent = label;
  }

  openFreezeDialog(): void {
    const { S, openDialog } = this.d;
    if (!S.indexReady) return;
    requireInput('freezeRowsInput').value = String(S.freeze.rows);
    requireInput('freezeColsInput').value = String(S.freeze.cols);
    openDialog('freezeDialog');
  }

  async applyFreeze(rawRows: number, rawCols: number): Promise<void> {
    const {
      S,
      api,
      toast,
      invalidateViewport,
      updateCanvasSize,
      loadViewport,
      closeDialog,
      getFrozenPanes,
      panels,
    } = this.d;
    const rows = rawRows;
    const cols = rawCols;
    if (
      !Number.isSafeInteger(rows) ||
      !Number.isSafeInteger(cols) ||
      rows < 0 ||
      cols < 0 ||
      rows > 30 ||
      cols > 12
    ) {
      toast(localize('ui.sheetController.numberFrozenRowsShouldBe'), 'warning');
      return;
    }
    if (!S.indexReady || !S.sheet) return;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    try {
      await api('/api/view', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sheet, rows, cols }),
      });
      if (epoch !== S.sheetEpoch) return;
      S.freeze = { rows, cols };
      const frozenPanes = getFrozenPanes();
      frozenPanes.reset();
      this.updateFreezeUI();
      invalidateViewport();
      updateCanvasSize();
      await loadViewport(true);
      frozenPanes.schedule(true);
      panels.pictures?.schedule();
      closeDialog('freezeDialog');
    } catch (error: unknown) {
      toast(localize('ui.sheetController.failedSaveFrozenPane', { detail: errorMessage(error) }), 'error');
    }
  }

  currentQueryRules(): readonly SourceFilterRule[] {
    const { S } = this.d;
    if (S.filtered && S.sheet && S.lastRulesBySheet.has(S.sheet)) {
      return S.lastRulesBySheet.get(S.sheet) ?? [];
    }
    if (!S.autoFilter) return [];
    return [...S.autoFilter.rules.values()].map((rule) => ({
      column: rule.column,
      operator: rule.operator,
      value: rule.value ?? '',
      values: rule.values,
    }));
  }

  async runSearch(preferredSheet: string | null = null): Promise<void> {
    const { S, api, panels } = this.d;
    const modeEl = requireSelect('searchMode');
    const mode = modeEl.value;
    const inputEl = requireInput(mode === 'advanced' ? 'searchAdvancedInput' : 'searchInput');
    const input = inputEl.value.trim();
    const currentSheet = preferredSheet || S.sheet || '';
    if (!input || !currentSheet) {
      inputEl.focus();
      return;
    }
    const scope = requireSelect('searchScope').value;
    let query = input;
    if (mode === 'reference' && scope === 'all' && !query.includes('!')) {
      query = `'${currentSheet.replaceAll("'", "''")}'!${query}`;
    }
    const full = requireInput('searchFull').checked;
    const key = (full ? 'full:' : '') + searchKey(mode, scope, query, currentSheet);
    const recoverable = requireInput('searchRecoverable').checked;
    const book = S.book;
    const workbookKey = S.workbookKey;
    const requestID = ++S.searchRequestID;
    S.searchActivity?.end();
    const current = (): boolean =>
      requestID === S.searchRequestID && book === S.book && workbookKey === S.workbookKey;
    if (S.searchJob) await this.cancelSearch(S.searchJob, false);
    if (!current()) return;
    const oldSession = S.searchSession;
    if (!full && !recoverable && oldSession?.key === key && oldSession.status === 'done') {
      panels.searchPanel.displaySummary(localize('ui.sheetController.itemsMatchedRetained', { count: oldSession.matches.length }));
      panels.searchPanel.renderResults({ matches: oldSession.matches, status: 'done', mode });
      return;
    }
    this.beginSearchSession({ key, sheet: currentSheet, input, query, mode, scope, full });
    const cached = !full && !recoverable ? S.searchCache.get(key) : undefined;
    if (cached && S.searchSession) {
      S.searchSession.matches = [...cached.matches];
      panels.searchPanel.buildHits(S.searchSession.matches);
      panels.searchPanel.renderResults({ ...cached, mode });
      panels.searchPanel.applyHighlightClasses();
      S.searchSession.status = 'done';
      S.searchSession.truncated = Boolean(cached.truncated);
      panels.searchPanel.displaySummary(
        localize('ui.sheetController.itemsMatchedLocalCache', { count: cached.matches.length, detail: cached.truncated ? localize('ui.sheetController.truncated') : '' }),
      );
      requireButton('searchBtn').disabled = false;
      requireEl('cancelSearchBtn').hidden = true;
      return;
    }
    const activity = beginActivity(localize('ui.common.searching'), { slot: 'searchActivity' });
    S.searchActivity = activity;
    try {
      const start = await api(
        '/api/search/start',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            sheet: currentSheet,
            query,
            mode,
            scope,
            recoverable,
            full,
            book,
            workbookKey,
          }),
        },
        decodeCachedJobStart,
      );
      if (!current()) {
        void this.cancelSearch(start.jobId, false);
        return;
      }
      S.searchJob = start.jobId;
      await panels.searchPanel.poll(start.jobId, requestID, Boolean(start.cached), activity);
    } catch (error: unknown) {
      if (!current()) return;
      if (S.searchSession) S.searchSession.status = 'error';
      panels.searchPanel.displaySummary(errorMessage(error));
      requireButton('searchBtn').disabled = false;
      requireEl('cancelSearchBtn').hidden = true;
    } finally {
      activity?.end();
    }
  }

  beginSearchSession(spec: {
    readonly key: string;
    readonly sheet: string;
    readonly input: string;
    readonly query: string;
    readonly mode: string;
    readonly scope: string;
    readonly full: boolean;
  }): void {
    const { S, panels } = this.d;
    panels.searchPanel.leavePages();
    S.searchSession = {
      ...spec,
      matches: [],
      status: 'running',
      truncated: false,
      summary: localize('ui.common.searching39739fa'),
      selected: -1,
    };
    requireInput('searchResultFilter').value = '';
    requireSelect('searchResultSheet').value = '';
    S.visibleSearchIndexes = [];
    S.searchSelectedIndex = -1;
    panels.searchPanel.buildHits([]);
    panels.searchPanel.applyHighlightClasses();
    panels.searchPanel.displaySummary(localize('ui.common.searching39739fa'));
    requireEl('searchResults').replaceChildren();
    requireEl('searchResultsPane').hidden = true;
    requireButton('searchBtn').disabled = true;
    requireEl('cancelSearchBtn').hidden = false;
  }

  async cancelSearch(jobID: string | null | undefined, updateUI = true): Promise<void> {
    const { S, api, panels } = this.d;
    if (!jobID) return;
    const owned = S.searchJob === jobID;
    if (owned) {
      S.searchJob = null;
      S.searchActivity?.end();
    }
    if (owned && updateUI) {
      ++S.searchRequestID;
      if (S.searchSession) S.searchSession.status = 'cancelled';
      requireButton('searchBtn').disabled = false;
      requireEl('cancelSearchBtn').hidden = true;
      panels.searchPanel.displaySummary(localize('ui.sheetController.searchCanceledFoundResultsStill'));
    }
    try {
      await api('/api/search/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: jobID }),
      });
    } catch {
      // Already completed.
    }
  }

  async adoptRecoveredTask(start: RecoveredTaskStart, item: TaskCheckpointInfo): Promise<boolean> {
    const { S, showWorkspace, openDialog, toast, panels } = this.d;
    if (item.book !== S.book || item.workbookKey !== S.workbookKey) return false;
    showWorkspace();
    if (start.operation === 'export') return panels.exportCenter.adoptRecovered(start, item);
    if (start.operation === 'search') {
      if (S.searchJob) return false;
      const query = item.query;
      const mode = item.mode || 'value';
      const scope = item.scope || 'current';
      const sheet = item.sheet;
      const key = searchKey(mode, scope, query, sheet);
      const request = ++S.searchRequestID;
      requireSelect('searchMode').value = mode;
      requireSelect('searchScope').value = scope;
      requireInput(mode === 'advanced' ? 'searchAdvancedInput' : 'searchInput').value = query;
      panels.searchPanel.syncMode();
      requireInput('searchFull').checked = Boolean(item.full);
      this.beginSearchSession({
        key: (item.full ? 'full:' : '') + key,
        sheet,
        input: query,
        query,
        mode,
        scope,
        full: Boolean(item.full),
      });
      S.searchJob = start.jobId;
      openDialog('searchDialog');
      panels.searchPanel.poll(start.jobId, request, false).catch((error: unknown) => {
        toast(errorMessage(error), 'error');
      });
      return true;
    }
    if (start.operation === 'filter') {
      const spec = start.filter;
      if (S.filterJob || !spec || !Number.isSafeInteger(spec.headerRow)) return false;
      const book = S.book;
      const key = S.workbookKey;
      const request = S.filterRequest;
      const search = S.searchRequestID;
      const opening = S.sheet !== item.sheet ? this.selectSheet(item.sheet) : null;
      const expectedEpoch = S.sheetEpoch;
      if (opening) await opening;
      const activeSheet = S.sheet;
      if (
        !activeSheet ||
        book !== S.book ||
        key !== S.workbookKey ||
        expectedEpoch !== S.sheetEpoch ||
        request !== S.filterRequest ||
        search !== S.searchRequestID ||
        activeSheet !== item.sheet ||
        !S.indexReady ||
        S.filterJob ||
        S.searchJob
      ) {
        return false;
      }
      const sheet = activeSheet;
      const epoch = S.sheetEpoch;
      const specRules = spec.rules ?? [];
      ++S.filterRequest;
      S.filterJob = start.jobId;
      S.pendingFilterSpec = {
        sheet,
        rules: specRules,
        options: { headerRow: spec.headerRow, sortColumn: spec.sortColumn, descending: spec.descending },
        restore: spec,
      };
      this.filterRecoveryMode(true);
      requireEl('filterProgress').hidden = false;
      requireButton('applyFilterBtn').disabled = true;
      requireEl('cancelFilterJobBtn').hidden = false;
      openDialog('filterDialog');
      this.pollFilter(start.jobId, sheet, epoch).catch((error: unknown) => {
        if (S.filterJob === start.jobId) {
          S.filterJob = null;
          S.pendingFilterSpec = null;
          requireButton('applyFilterBtn').disabled = false;
          requireEl('cancelFilterJobBtn').hidden = true;
          requireEl('filterProgressText').textContent = errorMessage(error);
          toast(errorMessage(error), 'error');
        }
      });
      return true;
    }
    return false;
  }

  private installRestoredFilter(sheet: string, spec: SourceFilterSpec): void {
    const { S } = this.d;
    const keys: SourceSortKey[] = spec.sortKeys?.length
      ? [...spec.sortKeys]
      : spec.sortColumn
        ? [{ column: spec.sortColumn, descending: Boolean(spec.descending), mode: '' }]
        : [];
    if (keys.length) {
      S.sortKeysBySheet.set(sheet, keys);
      S.sortHeaderBySheet.set(sheet, spec.headerRow);
    } else {
      S.sortKeysBySheet.delete(sheet);
      S.sortHeaderBySheet.delete(sheet);
    }
    const specRules = spec.rules ?? [];
    S.lastRulesBySheet.set(sheet, [...specRules]);
    if (spec.headerRow) {
      const nextRules = new Map<number, AutoFilterRule>(
        specRules.map((rule) => [
          rule.column,
          { column: rule.column, operator: rule.operator, value: rule.value, values: rule.values },
        ]),
      );
      S.autoFilter = {
        ...(S.autoFilter ?? { row: spec.headerRow, startCol: 1, endCol: S.usedMaxCol }),
        row: spec.headerRow,
        startCol: 1,
        endCol: S.usedMaxCol,
        rules: nextRules,
        sortColumn: spec.sortColumn ?? 0,
        descending: Boolean(spec.descending),
      };
      S.autoFiltersBySheet.set(sheet, S.autoFilter);
    } else if (S.autoFilter) {
      S.autoFilter.rules.clear();
      S.autoFilter.sortColumn = 0;
      S.autoFilter.descending = false;
    }
    this.refreshAutoFilterHeaders();
    this.updateAutoFilterButton();
  }

  addFilterRule(
    column: number = this.d.S.selected?.col ?? 1,
    operator = 'contains',
    value = '',
  ): void {
    const { colName } = this.d;
    const row = document.createElement('div');
    row.className = 'filter-rule';
    const colInput = document.createElement('input');
    colInput.className = 'field-control filter-column';
    colInput.value = colName(column);
    colInput.placeholder = localize('ui.sheetController.column52fc639');
    colInput.title = localize('ui.sheetController.columnIdentifierSuchAsOr');
    const op = document.createElement('select');
    op.className = 'field-control filter-operator';
    for (const [v, label] of FILTER_OPERATORS) {
      const option = document.createElement('option');
      option.value = v;
      option.textContent = label;
      op.appendChild(option);
    }
    op.value = operator;
    const val = document.createElement('input');
    val.className = 'field-control filter-value';
    val.value = value;
    val.placeholder = localize('ui.common.filteringValues');
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'remove-rule';
    remove.textContent = '×';
    remove.title = localize('ui.sheetController.deletionConditions');
    remove.setAttribute('aria-label', localize('ui.sheetController.deletionConditions'));
    remove.addEventListener('click', () => row.remove());
    op.addEventListener('change', () => {
      val.disabled = ['blank', 'notBlank'].includes(op.value);
      val.placeholder = val.disabled ? localize('ui.sheetController.noNeedFill') : localize('ui.common.filteringValues');
    });
    const fields: readonly (readonly [string, HTMLElement])[] = [
      [localize('ui.common.column'), colInput],
      [localize('ui.sheetController.conditions'), op],
      [localize('ui.common.filteringValues'), val],
    ];
    for (const [caption, control] of fields) {
      const label = document.createElement('label');
      const text = document.createElement('span');
      text.textContent = caption;
      label.append(text, control);
      row.appendChild(label);
    }
    row.appendChild(remove);
    requireEl('filterRules').appendChild(row);
    op.dispatchEvent(new Event('change'));
  }

  updateAutoFilterButton(): void {
    const { S, fileFormats } = this.d;
    S.largeData = S.indexReady && (S.index?.totalRows ?? 0) > fileFormats.largeDataRows;
    requireButton('multiSortBtn').disabled = !S.indexReady;
    for (const id of ['autoFilterSortAsc', 'autoFilterSortDesc']) {
      requireButton(id).disabled = !S.indexReady;
    }
    const largeStatus = requireEl('largeDataStatus');
    largeStatus.hidden = !S.largeData;
    largeStatus.title = localize('ui.sheetController.largeTableModeEnabledDisabling', { largeDataRows: fileFormats.largeDataRows.toLocaleString(interfaceLocale()), queryTempBytes: (fileFormats.queryTempBytes / 1024 ** 3).toLocaleString(interfaceLocale()) });
    const enabled = Boolean(S.autoFilter);
    const button = requireButton('filterOpenBtn');
    button.classList.toggle('format-active', enabled);
    button.setAttribute('aria-pressed', String(enabled));
    button.title = S.autoFilter
      ? localize('ui.sheetController.disableFilteringHeaderAppearsRow', { row: S.autoFilter.row })
      : localize('ui.sheetController.filterCurrentRowAsHeader');
    button.innerHTML = localize('ui.sheetController.filtering', { icon: icon('filter') });
    const headerButton = requireButton('textHeaderBtn');
    headerButton.hidden = S.source?.format !== 'txt';
    headerButton.disabled = !S.indexReady;
    const tableMode = isTableHeader(S.autoFilter);
    headerButton.classList.toggle('format-active', tableMode);
    headerButton.title =
      tableMode && S.autoFilter
        ? localize('ui.sheetController.rowHeaderClickChangeOr', { row: S.autoFilter.row, detail: S.autoFilter.detected ? localize('ui.sheetController.automaticallyRecognized') : '' })
        : localize('ui.sheetController.automaticallyIdentifiedOrSpecifiedTxt');
    document.querySelector('.grid-region')?.classList.toggle('text-table-view', tableMode);
    this.syncTableColumnHeaders();
  }

  syncTableColumnHeaders(): void {
    const { S, getVirtualScrollTop, sourceRowTop, getFrozenPanes } = this.d;
    const config = S.autoFilter;
    const host = requireEl('grid').parentElement;
    if (host) {
      if (config) {
        const pinned = getFrozenPanes().effective().rows;
        host.classList.toggle(
          'table-header-scrolled',
          useStickyTableLabels(
            config,
            getVirtualScrollTop(),
            sourceRowTop(config.row + 1) - sourceRowTop(pinned + 1),
            pinned,
          ),
        );
      } else if (host.classList.contains('table-header-scrolled')) {
        host.classList.remove('table-header-scrolled');
      }
    }
    this.syncGridBackdrop();
  }

  syncGridBackdrop(): void {
    const {
      S,
      headerHeight,
      rowHead,
      colTotal,
      sourceRowTop,
      rowTotal,
      getVirtualScrollTop,
      getFrozenPanes,
      panels,
    } = this.d;
    const backdrop = requireEl('gridBackdrop');
    backdrop.hidden = !panels.readingMode?.textureEnabled || !S.indexReady;
    if (backdrop.hidden) return;
    const grid = requireEl('grid');
    const hh = headerHeight();
    const pinned = getFrozenPanes().effective();
    const rows = S.filtered ? S.filterTotal + pinned.rows : S.usedMaxRow;
    const rh = rowHead();
    const fixedRight = rh + colTotal(S.usedMaxCol);
    paintBackdrop(backdrop, {
      width: grid.clientWidth,
      height: grid.clientHeight,
      rowHead: rh,
      header: hh,
      pinWidth: colTotal(pinned.cols),
      pinHeight: sourceRowTop(pinned.rows + 1),
      right: fixedRight - grid.scrollLeft,
      bottom: hh + rowTotal(rows) - getVirtualScrollTop(),
      fixedRight,
      fixedBottom: hh + rowTotal(S.usedMaxRow),
    });
  }

  decorateTableColumnHeader(head: HTMLElement, col: number): boolean {
    const { S, colName } = this.d;
    return decorateTableHeader(head, col, S.autoFilter, colName, S.indexReady);
  }

  refreshAutoFilterHeaders(): void {
    const { resetGridDOM, renderGridFromCurrentWindow, getFrozenPanes } = this.d;
    resetGridDOM();
    renderGridFromCurrentWindow();
    getFrozenPanes().schedule(true);
  }

  async applyTextTableHeader(number: number, automatic = false): Promise<void> {
    const { S, api, toast, closeDialog, panels } = this.d;
    if (!S.indexReady || !S.sheet || S.source?.format !== 'txt') return;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const revision = ++S.textHeaderRevision;
    panels.autoFilterUI?.hide();
    if (S.filterJob) await this.cancelFilter(S.filterJob);
    if (epoch !== S.sheetEpoch || sheet !== S.sheet || revision !== S.textHeaderRevision) return;
    let header: TextTableHeader | undefined;
    if (automatic) {
      header = S.source.tableHeader;
      if (!header) {
        toast(localize('ui.sheetController.noReliableHeaderWasIdentified'), 'warning');
        return;
      }
    } else {
      if (!Number.isInteger(number) || number < 1 || number > S.usedMaxRow) {
        toast(localize('ui.sheetController.headerRowsMustBeWithin'), 'warning');
        return;
      }
      try {
        header = await api(
          `/api/table/header?${new URLSearchParams({ sheet, row: String(number) }).toString()}`,
          undefined,
          decodeTextTableHeader,
        );
      } catch (error: unknown) {
        if (epoch === S.sheetEpoch) toast(localize('ui.sheetController.headerReadingFailed', { detail: errorMessage(error) }), 'error');
        return;
      }
    }
    if (epoch !== S.sheetEpoch || sheet !== S.sheet || revision !== S.textHeaderRevision) return;
    if (S.filterId) await this.clearFilter();
    if (epoch !== S.sheetEpoch || sheet !== S.sheet || revision !== S.textHeaderRevision) return;
    S.sortKeysBySheet.delete(sheet);
    S.sortHeaderBySheet.delete(sheet);
    S.lastRulesBySheet.delete(sheet);
    S.autoFilter = tableHeaderFromSource(header);
    S.autoFiltersBySheet.set(sheet, S.autoFilter);
    this.updateAutoFilterButton();
    this.refreshAutoFilterHeaders();
    closeDialog('textHeaderDialog');
    toast(localize('ui.sheetController.rowSetAsHeaderTop', { row: header.row }), 'success');
  }

  async disableTextTableHeader(): Promise<void> {
    const { S, closeDialog, panels } = this.d;
    if (!S.indexReady || !S.sheet) return;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const revision = ++S.textHeaderRevision;
    panels.autoFilterUI?.hide();
    if (S.filterJob) await this.cancelFilter(S.filterJob);
    if (epoch !== S.sheetEpoch || sheet !== S.sheet || revision !== S.textHeaderRevision) return;
    if (S.filterId) await this.clearFilter();
    if (epoch !== S.sheetEpoch || sheet !== S.sheet || revision !== S.textHeaderRevision) return;
    S.autoFilter = null;
    S.autoFiltersBySheet.set(sheet, null);
    S.sortKeysBySheet.delete(sheet);
    S.sortHeaderBySheet.delete(sheet);
    S.lastRulesBySheet.delete(sheet);
    this.updateAutoFilterButton();
    this.refreshAutoFilterHeaders();
    closeDialog('textHeaderDialog');
  }

  async toggleAutoFilter(): Promise<void> {
    const { S, toast, panels } = this.d;
    if (!S.indexReady || !S.sheet) return;
    panels.autoFilterUI?.hide();
    if (S.autoFilter && S.source?.format === 'txt') {
      await this.disableTextTableHeader();
      toast(localize('ui.sheetController.switchedNormalTextView'));
      return;
    }
    if (S.autoFilter) {
      ++S.textHeaderRevision;
      S.autoFilter = null;
      S.autoFiltersBySheet.set(S.sheet, null);
      S.sortKeysBySheet.delete(S.sheet);
      S.sortHeaderBySheet.delete(S.sheet);
      if (S.filterId) await this.clearFilter();
      this.refreshAutoFilterHeaders();
      toast(localize('ui.sheetController.automaticFilterDisabled'));
    } else {
      const row = Math.max(1, Math.min(S.usedMaxRow, S.selected?.row ?? 1));
      if (S.source?.format === 'txt') {
        await this.applyTextTableHeader(row);
        return;
      }
      if (S.filterId) await this.clearFilter();
      S.autoFilter = {
        row,
        startCol: 1,
        endCol: Math.max(1, S.usedMaxCol),
        rules: new Map(),
        sortColumn: 0,
        descending: false,
      };
      S.autoFiltersBySheet.set(S.sheet, S.autoFilter);
      this.refreshAutoFilterHeaders();
      toast(localize('ui.sheetController.rowSetAsHeaderClick', { row }), 'success');
    }
    this.updateAutoFilterButton();
  }

  async applyAutoFilter(
    options: { readonly preserveRules?: boolean; readonly recoverable?: boolean } = {},
  ): Promise<void> {
    const { S, toast } = this.d;
    if (!S.autoFilter || !S.sheet) return;
    const c = S.autoFilter;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const multi = S.sortKeysBySheet.get(sheet) ?? [];
    const rules: readonly SourceFilterRule[] = options.preserveRules
      ? this.currentQueryRules()
      : [...c.rules.values()].map((r) => ({
          column: r.column,
          operator: r.operator,
          value: r.value ?? '',
          values: r.values,
        }));
    if (rules.length === 0 && !c.sortColumn && !multi.length) {
      if (S.filterId) await this.clearFilter();
      if (S.sheet === sheet && S.sheetEpoch === epoch && S.autoFilter === c) {
        this.refreshAutoFilterHeaders();
      }
      return;
    }
    try {
      await this.submitFilterRules(rules, {
        headerRow: c.row,
        sortColumn: c.sortColumn,
        descending: c.descending,
        sortKeys: multi,
        recoverable: Boolean(options.recoverable),
      });
      if (S.sheet === sheet && S.sheetEpoch === epoch && S.autoFilter === c) {
        this.refreshAutoFilterHeaders();
      }
    } catch (err: unknown) {
      toast(localize('ui.sheetController.filteringFailed', { detail: errorMessage(err) }), 'error');
    }
  }

  async filterSelectedValue(): Promise<void> {
    const { S, toast } = this.d;
    if (!S.indexReady || !S.selected || !S.sheet) return;
    const selected = { ...S.selected };
    const value = selected.display;
    if (value.length > 1024) {
      toast(localize('ui.sheetController.currentCellExceedsCharactersUse'), 'warning');
      return;
    }
    if (!S.autoFilter) {
      S.autoFilter = {
        row: 1,
        startCol: 1,
        endCol: Math.max(1, S.usedMaxCol),
        rules: new Map(),
        sortColumn: 0,
        descending: false,
      };
      S.autoFiltersBySheet.set(S.sheet, S.autoFilter);
    }
    const config = S.autoFilter;
    if (selected.row <= config.row) {
      toast(localize('ui.sheetController.selectDataCellsBelowHeader', { row: config.row }), 'warning');
      return;
    }
    config.rules.set(
      selected.col,
      value === ''
        ? { column: selected.col, operator: 'blank', value: '' }
        : { column: selected.col, operator: 'in', values: [value] },
    );
    this.updateAutoFilterButton();
    this.refreshAutoFilterHeaders();
    await this.applyAutoFilter();
  }

  openFilter(column: number = this.d.S.selected?.col ?? 1): void {
    const { S, openDialog } = this.d;
    if (!S.sheet) return;
    this.filterRecoveryMode(false);
    requireEl('filterRules').replaceChildren();
    this.addFilterRule(column);
    requireEl('filterProgress').hidden = true;
    requireButton('applyFilterBtn').disabled = false;
    requireEl('cancelFilterJobBtn').hidden = true;
    openDialog('filterDialog');
  }

  private filterRecoveryMode(recovered: boolean): void {
    for (const id of [
      'filterRules',
      'addFilterRuleBtn',
      'applyFilterBtn',
      'filterHelp',
      'filterProgressChoice',
    ]) {
      requireEl(id).hidden = recovered;
    }
  }

  private collectFilterRules(): SourceFilterRule[] {
    const { parseColumn } = this.d;
    const rows = [...requireEl('filterRules').querySelectorAll('.filter-rule')];
    if (!rows.length) throw new Error(localize('ui.sheetController.addAtLeastOneFilter'));
    if (rows.length > 32) throw new Error(localize('ui.sheetController.supportsUpFilteringConditions'));
    return rows.map((row, index) => {
      const colEl = row.querySelector<HTMLInputElement>('.filter-column');
      const opEl = row.querySelector<HTMLSelectElement>('.filter-operator');
      const valEl = row.querySelector<HTMLInputElement>('.filter-value');
      const column = parseColumn(colEl?.value ?? '');
      const operator = opEl?.value ?? 'contains';
      const value = valEl?.value ?? '';
      if (!column) throw new Error(localize('ui.sheetController.columnIdentifierThConditionInvalid', { index: index + 1 }));
      if (!['blank', 'notBlank'].includes(operator) && !value.trim()) {
        throw new Error(localize('ui.sheetController.thConditionRequiresFillingFilter', { index: index + 1 }));
      }
      return { column, operator, value };
    });
  }

  async applyFilter(): Promise<void> {
    const { S, toast } = this.d;
    let rules: SourceFilterRule[];
    try {
      rules = this.collectFilterRules();
    } catch (error: unknown) {
      toast(errorMessage(error), 'error');
      return;
    }
    await this.submitFilterRules(rules, {
      headerRow: S.autoFilter?.row ?? 0,
      recoverable: requireInput('filterRecoverable').checked,
    });
  }

  async submitFilterRules(
    rules: readonly SourceFilterRule[],
    options: Readonly<Record<string, unknown>> = {},
  ): Promise<boolean> {
    const { S, api, setStatus, toast } = this.d;
    const requestedSheet = S.sheet;
    const requestedEpoch = S.sheetEpoch;
    const requestedBook = S.workbookKey;
    if (S.filterJob) await this.cancelFilter(S.filterJob);
    if (
      !S.sheet ||
      S.sheet !== requestedSheet ||
      S.sheetEpoch !== requestedEpoch ||
      S.workbookKey !== requestedBook
    ) {
      return false;
    }
    const ticket = (S.filterRequest = (S.filterRequest || 0) + 1);
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const book = S.workbookKey;
    const current = (): boolean =>
      ticket === S.filterRequest &&
      sheet === S.sheet &&
      epoch === S.sheetEpoch &&
      book === S.workbookKey;
    S.filterJob = `pending:${ticket}`;
    requireButton('applyFilterBtn').disabled = true;
    requireEl('filterProgress').hidden = false;
    requireEl('filterBar').style.width = '0%';
    requireEl('filterProgressText').textContent = localize('ui.sheetController.scanningCurrentWorksheet');
    setStatus(localize('ui.sheetController.filtering5cfa1ef', { sheet }));
    S.filterActivity?.end();
    const activity = beginActivity(localize('ui.common.filteringDataProgress'), { slot: 'filterActivity' });
    S.filterActivity = activity;
    try {
      const start = await api('/api/filter/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sheet, rules, ...options, workbookKey: book, book: S.book }),
      }, decodeFilterStartResponse);
      if (!current()) {
        if (S.filterJob === `pending:${ticket}`) S.filterJob = null;
        api('/api/filter/cancel', {
          method: 'POST',
          body: JSON.stringify({ id: start.jobId }),
        }).catch(() => undefined);
        return false;
      }
      S.filterJob = start.jobId;
      S.pendingFilterSpec = { sheet, rules, options };
      requireEl('cancelFilterJobBtn').hidden = false;
      return await this.pollFilter(start.jobId, sheet, epoch, activity);
    } catch (error: unknown) {
      if (!current()) return false;
      S.filterJob = null;
      requireEl('filterProgressText').textContent = errorMessage(error);
      requireButton('applyFilterBtn').disabled = false;
      requireEl('cancelFilterJobBtn').hidden = true;
      setStatus(localize('ui.sheetController.openedbb2d14e', { sheet }), true);
      toast(errorMessage(error), 'error');
      return false;
    } finally {
      activity?.end();
    }
  }

  async commitRowView(data: RowViewState, guard: () => boolean = () => true): Promise<boolean> {
    const {
      S,
      geometry,
      rowAtOffset,
      getVirtualScrollTop,
      setVirtualScrollTop,
      sourceRowTop,
      filteredBodyCount,
      invalidateViewport,
      updateCanvasSize,
      nextVisibleRow,
      previousVisibleRow,
      loadViewport,
      parseRef,
      rowVisible,
      navigateToCell,
      selectInitialCell,
      getFrozenPanes,
      panels,
    } = this.d;
    if (!guard() || !S.sheet || !panels.rowViews) return false;
    panels.recordCardCenter?.resetBook();
    panels.selectionQuick?.resetBook();
    panels.transposeCenter?.resetBook();
    panels.cellExplanation?.resetBook();
    panels.profileCenter?.resetBook();
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const anchor = S.selected?.ref;
    const current = (): boolean =>
      guard() && sheet === S.sheet && epoch === S.sheetEpoch && S.rowView?.id === data.id;
    const frozenPanes = getFrozenPanes();
    const topSource = S.filtered
      ? 0
      : rowAtOffset(getVirtualScrollTop() + sourceRowTop(frozenPanes.effective().rows + 1));
    panels.rowViews.install(data);
    if (S.rowVisibility) geometry.rows.setHidden(S.rowVisibility.hidden);
    if (S.filtered) S.filterTotal = filteredBodyCount();
    S.sheetViews.clear();
    S.sheetViewsBytes = 0;
    invalidateViewport();
    frozenPanes.reset();
    panels.selectionStats?.clear();
    S.selected = null;
    S.range = null;
    S.multiRanges = [];
    S.rangeAnchor = null;
    S.wholeRowSelected = false;
    updateCanvasSize();
    if (topSource) {
      const next = nextVisibleRow(topSource) || previousVisibleRow(topSource);
      setVirtualScrollTop(
        Math.max(0, sourceRowTop(next || 1) - sourceRowTop(frozenPanes.effective().rows + 1)),
      );
    }
    await loadViewport(true);
    if (!current()) return false;
    if (anchor) {
      const parsed = parseRef(anchor);
      if (parsed && rowVisible(parsed.row)) {
        await navigateToCell(sheet, anchor, false, current);
        return current();
      }
    }
    selectInitialCell();
    frozenPanes.schedule(true);
    return true;
  }

  async adoptFilterResult(
    data: Pick<FilterStatusResponse, 'filterId' | 'matchedRows'>,
    sheet: string = this.d.S.sheet ?? '',
    initialPreparedRows: RowViewState | null = null,
    options: {
      readonly guard?: () => boolean;
      readonly signal?: AbortSignal;
      readonly onCommit?: () => void;
    } = {},
  ): Promise<boolean> {
    const { guard = () => true, signal, onCommit } = options;
    const {
      S,
      api,
      geometry,
      filteredBodyCount,
      invalidateViewport,
      updateCanvasSize,
      loadViewport,
      selectInitialCell,
      getFrozenPanes,
      panels,
    } = this.d;
    const filterId = data.filterId ?? '';
    const epoch = S.sheetEpoch;
    const book = S.workbookKey;
    const request = S.filterRequest;
    let rowTicket: number | undefined;
    const current = (): boolean =>
      sheet === S.sheet &&
      epoch === S.sheetEpoch &&
      book === S.workbookKey &&
      request === S.filterRequest &&
      (rowTicket === undefined || rowTicket === panels.rowViews?.serial) &&
      !signal?.aborted &&
      guard();
    let preparedRows = initialPreparedRows;
    try {
      if (!current()) return false;
      if (panels.rowViews && !preparedRows) {
        const pending = panels.rowViews.prepareFilter(filterId, { guard: current, signal });
        rowTicket = panels.rowViews.serial;
        preparedRows = await pending;
      }
      if (!current()) return false;
      if (preparedRows && panels.rowViews) panels.rowViews.validate(preparedRows, sheet, filterId);
      onCommit?.();
      panels.recordCardCenter?.resetBook();
      panels.selectionQuick?.resetBook();
      panels.transposeCenter?.resetBook();
      panels.cellExplanation?.resetBook();
      panels.profileCenter?.resetBook();
      if (S.filterId && S.filterId !== filterId) {
        api('/api/filter/clear', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: S.filterId }),
        }).catch(() => undefined);
      }
      S.filterId = filterId;
      S.filtersBySheet.set(sheet, filterId);
      S.filtered = true;
      S.filterTotal = data.matchedRows ?? 0;
      S.filterTotalsBySheet.set(sheet, S.filterTotal);
      if (preparedRows && panels.rowViews) {
        panels.rowViews.install(preparedRows);
        if (S.rowVisibility) geometry.rows.setHidden(S.rowVisibility.hidden);
        S.filterTotal = filteredBodyCount();
      }
      invalidateViewport();
      S.selected = null;
      S.traceRefs = [];
      const grid = requireEl('grid');
      grid.scrollTop = 0;
      grid.scrollLeft = 0;
      updateCanvasSize();
      await loadViewport(true);
      if (
        !current() ||
        S.filterId !== filterId ||
        (preparedRows && S.rowView?.id !== preparedRows.id)
      ) {
        return false;
      }
      selectInitialCell();
      getFrozenPanes().schedule(true);
      requireEl('filterStatus').hidden = false;
      requireButton('clearFilterBtn').disabled = false;
      return true;
    } finally {
      if (preparedRows && panels.rowViews && S.rowView?.id !== preparedRows.id) {
        panels.rowViews.cleanup(preparedRows.id);
      }
    }
  }

  captureNamedView(): {
    readonly sheet: string;
    readonly filterId: string;
    readonly viewId?: string;
    readonly headerRow: number;
    readonly config: NamedViewConfig;
  } {
    const { S } = this.d;
    if (!S.opened || !S.indexReady || !S.sheet || S.opening || S.sheetLoadingEpoch || S.filterJob) {
      throw new Error(localize('ui.sheetController.completeWorksheetLoadingOrFiltering'));
    }
    const table = S.autoFilter;
    return {
      sheet: S.sheet,
      filterId: S.filtered ? S.filterId : '',
      ...(S.rowView ? { viewId: S.rowView.id } : {}),
      headerRow: S.sortHeaderBySheet.get(S.sheet) ?? table?.row ?? 0,
      config: {
        freeze: { ...S.freeze },
        layout: {
          rows: { ...S.rowSizes },
          cols: { ...S.columnWidths },
          font: S.fontOverride,
          columnMode: S.columnMode,
          columnVisibility: { ...S.columnOverrides },
        },
        table: table
          ? {
              row: table.row,
              startCol: table.startCol,
              endCol: table.endCol,
              sticky: Boolean(table.table),
            }
          : null,
        zoom: S.zoom,
        showStyles: S.showStyles,
        showFormulas: S.showFormulas,
      },
    };
  }

  async applyNamedView(
    prepared: NamedViewPrepared,
    data: Pick<FilterStatusResponse, 'filterId' | 'matchedRows' | 'sheet'> | null,
    current: () => boolean,
  ): Promise<boolean> {
    const {
      S,
      api,
      geometry,
      setDisplayFontSelection,
      applyColumnVisibility,
      rowHeight,
      colWidth,
      headerHeight,
      invalidateViewport,
      updateCanvasSize,
      loadViewport,
      selectInitialCell,
      getFrozenPanes,
      panels,
    } = this.d;
    if (!current()) return false;
    if (S.filterJob) throw new Error(localize('ui.sheetController.thereFilteringTaskProgressView'));
    const view = prepared.view;
    const sheet = view.source.sheet;
    const config = view.config;
    const query = view.query;
    if (data && (!data.filterId || data.sheet !== sheet)) throw new Error(localize('ui.sheetController.viewQuerySourcesInconsistent'));
    let preparedRows: RowViewState | null = null;
    if (panels.rowViews) {
      preparedRows = await panels.rowViews.build({
        sheet,
        filterId: data?.filterId ?? '',
        settings: config.rowVisibility ?? { mode: 'original' },
        guard: current,
      });
      if (!current()) {
        panels.rowViews.cleanup(preparedRows.id);
        return false;
      }
    }
    try {
      let table: AutoFilterState | null = null;
      if (config.table) {
        table = config.table.sticky
          ? tableHeaderFromSource(prepared.tableHeader)
          : {
              row: config.table.row,
              startCol: config.table.startCol,
              endCol: config.table.endCol,
              rules: new Map(),
              sortColumn: 0,
              descending: false,
            };
        if (!table) throw new Error(localize('ui.sheetController.viewHeaderInvalid'));
        const queryRules = query.rules ?? [];
        table.rules = new Map(
          queryRules.map((rule) => [
            rule.column,
            { column: rule.column, operator: rule.operator, value: rule.value, values: rule.values },
          ]),
        );
        table.sortColumn = query.sortColumn ?? 0;
        table.descending = Boolean(query.descending);
      }
      const opening = this.selectSheet(sheet);
      const epoch = S.sheetEpoch;
      await opening;
      if (!current() || S.sheet !== sheet || S.sheetEpoch !== epoch || !S.indexReady) return false;
      S.filterRequest = (S.filterRequest || 0) + 1;
      S.sheetViews.clear();
      S.sheetViewsBytes = 0;
      S.rowSizes = { ...config.layout.rows };
      S.columnWidths = { ...config.layout.cols };
      S.columnMode = config.layout.columnMode || 'original';
      S.columnOverrides = { ...config.layout.columnVisibility };
      setDisplayFontSelection(config.layout.font);
      S.showStyles = config.showStyles;
      S.showFormulas = config.showFormulas;
      S.zoom = config.zoom;
      S.freeze = { ...config.freeze };
      S.sheetLayout = {
        ...(S.sheetLayout ?? {}),
        rows: S.rowSizes,
        cols: S.columnWidths,
        font: S.fontOverride,
        columnMode: S.columnMode,
        columnVisibility: S.columnOverrides,
      };
      S.autoFilter = table;
      S.autoFiltersBySheet.set(sheet, table);
      const keys: SourceSortKey[] = query.sortKeys?.length
        ? [...query.sortKeys]
        : query.sortColumn
          ? [{ column: query.sortColumn, descending: Boolean(query.descending), mode: '' }]
          : [];
      if (keys.length) {
        S.sortKeysBySheet.set(sheet, keys);
        S.sortHeaderBySheet.set(sheet, query.headerRow);
      } else {
        S.sortKeysBySheet.delete(sheet);
        S.sortHeaderBySheet.delete(sheet);
      }
      S.lastRulesBySheet.set(sheet, [...(query.rules ?? [])]);
      S.pendingFilterSpec = null;
      requireEl('zoomValue').textContent = `${S.zoom}%`;
      const formatBtn = requireButton('formatBtn');
      formatBtn.classList.toggle('format-active', S.showStyles);
      formatBtn.setAttribute('aria-pressed', String(S.showStyles));
      requireButton('formulaViewBtn').setAttribute('aria-pressed', String(S.showFormulas));
      applyColumnVisibility();
      this.conditional.updateSourceAppearance();
      this.updateFreezeUI();
      this.updateAutoFilterButton();
      const frozenPanes = getFrozenPanes();
      frozenPanes.reset();
      document.documentElement.style.setProperty('--row-height', `${rowHeight()}px`);
      document.documentElement.style.setProperty('--col-width', `${colWidth()}px`);
      document.documentElement.style.setProperty('--header-height', `${headerHeight()}px`);
      S.range = null;
      S.multiRanges = [];
      S.rangeAnchor = null;
      S.traceRefs = [];
      if (data) {
        if (!(await this.adoptFilterResult(data, sheet, preparedRows, { guard: current }))) {
          return false;
        }
      } else {
        const old = S.filterId;
        if (old) {
          api('/api/filter/clear', {
            method: 'POST',
            body: JSON.stringify({ id: old }),
          }).catch(() => undefined);
        }
        S.filtersBySheet.delete(sheet);
        S.filterTotalsBySheet.delete(sheet);
        S.filterId = '';
        S.filtered = false;
        S.filterTotal = 0;
        S.selected = null;
        if (preparedRows && panels.rowViews) {
          panels.rowViews.install(preparedRows);
          if (S.rowVisibility) geometry.rows.setHidden(S.rowVisibility.hidden);
        }
        invalidateViewport();
        const grid = requireEl('grid');
        grid.scrollTop = 0;
        grid.scrollLeft = 0;
        updateCanvasSize();
        await loadViewport(true);
        if (
          !current() ||
          S.sheet !== sheet ||
          S.sheetEpoch !== epoch ||
          (preparedRows && S.rowView?.id !== preparedRows.id)
        ) {
          return false;
        }
        selectInitialCell();
        frozenPanes.schedule(true);
        requireEl('filterStatus').hidden = true;
        requireButton('clearFilterBtn').disabled = true;
      }
      panels.pictures?.schedule();
      return true;
    } finally {
      if (preparedRows && panels.rowViews && S.rowView?.id !== preparedRows.id) {
        panels.rowViews.cleanup(preparedRows.id);
      }
    }
  }

  public async pollFilter(
    jobID: string,
    sheet: string = this.d.S.sheet ?? '',
    epoch: number = this.d.S.sheetEpoch,
    initialActivity?: ActivityHandle | null,
  ): Promise<boolean> {
    const { S, api, setStatus, toast } = this.d;
    const activity = initialActivity ?? beginActivity(localize('ui.common.filteringDataProgress'), { slot: 'filterActivity' });
    S.filterActivity = activity;
    try {
      const wait = this.d.delay ?? delay;
      while (S.filterJob === jobID) {
        await wait(350);
        const data = await api(
          `/api/filter/status?id=${encodeURIComponent(jobID)}`,
          undefined,
          decodeFilterStatusResponse,
        );
        if (S.filterJob !== jobID) return false;
        if (S.sheet !== sheet || S.sheetEpoch !== epoch) {
          S.filterJob = null;
          return false;
        }
        const percent = data.progress ?? 0;
        activity?.job({
          stage: data.stage,
          progress: data.progress,
          scanned: data.scanned,
          total: data.total,
        });
        requireEl('filterBar').style.width = `${percent}%`;
        requireEl('filterProgressText').textContent =
          data.status === 'done'
            ? localize('ui.sheetController.displayingFilteredResults614770e')
            : localize('ui.sheetController.row72d7d52', { stage: data.stage || localize('ui.sheetController.filteringProgress'), detail: data.scanned ?? 0, detail2: data.total ?? 0 });
        if (data.status === 'done') {
          activity?.stage(localize('ui.sheetController.displayingFilteredResults'), 95, 99);
          const abort = new AbortController();
          S.filterCommitAbort = abort;
          let accepted = false;
          try {
            accepted = await this.adoptFilterResult(data, sheet, null, {
              guard: () => S.filterJob === jobID,
              signal: abort.signal,
              onCommit: () => {
                if (S.pendingFilterSpec?.restore) {
                  this.installRestoredFilter(sheet, S.pendingFilterSpec.restore);
                }
              },
            });
          } finally {
            if (S.filterCommitAbort === abort) S.filterCommitAbort = null;
          }
          if (S.filterJob !== jobID || S.sheet !== sheet || S.sheetEpoch !== epoch) return false;
          if (!accepted) {
            S.filterJob = null;
            S.pendingFilterSpec = null;
            requireButton('applyFilterBtn').disabled = false;
            requireEl('cancelFilterJobBtn').hidden = true;
            requireEl('filterProgressText').textContent = localize('ui.sheetController.viewChangedFilteringTaskCompleted');
            setStatus(localize('ui.sheetController.openedbb2d14e', { sheet }), true);
            return false;
          }
          if (S.pendingFilterSpec?.sheet === sheet) {
            S.lastRulesBySheet.set(sheet, [...S.pendingFilterSpec.rules]);
            S.pendingFilterSpec = null;
          }
          requireEl('filterProgressText').textContent =
            localize('ui.sheetController.filterCompletedRowsMeetConditions', { filterTotal: S.filterTotal.toLocaleString(interfaceLocale()) });
          requireEl('filterStatus').hidden = false;
          requireButton('clearFilterBtn').disabled = false;
          requireButton('applyFilterBtn').disabled = false;
          requireEl('cancelFilterJobBtn').hidden = true;
          S.filterJob = null;
          setStatus(localize('ui.sheetController.openedbb2d14e', { sheet }), true);
          toast(
            localize('ui.sheetController.filterCompletedRow', { detail: Math.max(0, S.filterTotal - (S.autoFilter?.row ?? 0)).toLocaleString(interfaceLocale()) }),
            'success',
          );
          return true;
        }
        if (data.status === 'error' || data.status === 'cancelled') {
          requireEl('filterProgressText').textContent = data.error || localize('ui.sheetController.filterCancelled');
          requireButton('applyFilterBtn').disabled = false;
          requireEl('cancelFilterJobBtn').hidden = true;
          S.filterJob = null;
          setStatus(localize('ui.sheetController.openedbb2d14e', { sheet }), true);
          if (data.status === 'error') toast(data.error || localize('ui.sheetController.filterFailure'), 'error');
          return false;
        }
      }
      return false;
    } finally {
      activity?.end();
    }
  }

  async cancelFilter(jobID: string | null | undefined): Promise<void> {
    const { S, api, setStatus } = this.d;
    if (!jobID) return;
    if (S.filterJob === jobID) {
      S.filterActivity?.end();
      S.filterRequest = (S.filterRequest || 0) + 1;
      S.filterCommitAbort?.abort();
      S.filterCommitAbort = null;
      S.filterJob = null;
      requireButton('applyFilterBtn').disabled = false;
      requireEl('cancelFilterJobBtn').hidden = true;
      requireEl('filterProgressText').textContent = localize('ui.sheetController.filterCancelled');
      setStatus(localize('ui.sheetController.openeda77e173', { detail: S.sheet ?? '' }), true);
    }
    try {
      if (!jobID.startsWith('pending:')) {
        await api('/api/filter/cancel', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: jobID }),
        });
      }
    } catch {
      // Ignore already finished filter job.
    }
  }

  async clearFilter(): Promise<void> {
    const {
      S,
      api,
      geometry,
      toast,
      invalidateViewport,
      updateCanvasSize,
      loadViewport,
      selectInitialCell,
      getFrozenPanes,
      panels,
    } = this.d;
    if (!S.filterId || !S.sheet) return;
    const oldID = S.filterId;
    const sheet = S.sheet;
    const epoch = S.sheetEpoch;
    const book = S.workbookKey;
    if (S.filterJob) await this.cancelFilter(S.filterJob);
    if (sheet !== S.sheet || epoch !== S.sheetEpoch || book !== S.workbookKey || S.filterId !== oldID) {
      return;
    }
    const ticket = (S.filterRequest = (S.filterRequest || 0) + 1);
    let rowTicket: number | undefined;
    const current = (): boolean =>
      sheet === S.sheet &&
      epoch === S.sheetEpoch &&
      book === S.workbookKey &&
      ticket === S.filterRequest &&
      (rowTicket === undefined || rowTicket === panels.rowViews?.serial);
    let preparedRows: RowViewState | null = null;
    try {
      if (panels.rowViews) {
        try {
          const pending = panels.rowViews.prepareFilter('', { guard: current });
          rowTicket = panels.rowViews.serial;
          preparedRows = await pending;
        } catch (error: unknown) {
          if (current() && !(error instanceof Error && error.name === 'AbortError')) {
            toast(localize('ui.sheetController.filterClearingFailed', { detail: errorMessage(error) }), 'error');
          }
          return;
        }
      }
      if (!current()) return;
      try {
        await api('/api/filter/clear', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: oldID }),
        });
      } catch {
        // Expired filter results are also cleared locally.
      }
      if (!current()) return;
      S.filtersBySheet.delete(sheet);
      S.filterTotalsBySheet.delete(sheet);
      S.filterId = '';
      S.filtered = false;
      S.filterTotal = 0;
      if (preparedRows && panels.rowViews) {
        panels.rowViews.install(preparedRows);
        if (S.rowVisibility) geometry.rows.setHidden(S.rowVisibility.hidden);
      }
      invalidateViewport();
      S.selected = null;
      S.traceRefs = [];
      const grid = requireEl('grid');
      grid.scrollTop = 0;
      grid.scrollLeft = 0;
      updateCanvasSize();
      await loadViewport(true);
      if (
        !current() ||
        S.filterId ||
        (preparedRows && S.rowView?.id !== preparedRows.id)
      ) {
        return;
      }
      selectInitialCell();
      getFrozenPanes().schedule(true);
      requireEl('filterStatus').hidden = true;
      requireButton('clearFilterBtn').disabled = true;
      if (S.autoFilter) {
        S.autoFilter.rules.clear();
        S.autoFilter.sortColumn = 0;
        S.autoFilter.descending = false;
        this.refreshAutoFilterHeaders();
      }
    } finally {
      if (preparedRows && panels.rowViews && S.rowView?.id !== preparedRows.id) {
        panels.rowViews.cleanup(preparedRows.id);
      }
    }
  }
}
