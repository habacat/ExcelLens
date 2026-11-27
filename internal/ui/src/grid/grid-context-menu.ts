/**
 * Grid context menus, cell hover tooltip, zoom, fullscreen, and modal dialog helpers.
 */
import type { CellRef } from '../contracts/common.ts';
import type { CellData, CellHyperlink } from '../contracts/workbook.ts';
import { errorMessage } from '../core/display-format.ts';
import { requireButton, requireDialog, requireEl } from '../core/dom.ts';
import type { FrozenPanes } from './freeze.ts';
import type { Pictures } from './images.ts';
import type { ColumnSelectionSpan, WorkbookState } from './state.ts';

export interface SizeContextTarget {
  readonly kind: 'row' | 'col';
  readonly index: number;
  readonly end: number;
}

export interface GridControlsEditCopyBridge {
  readonly grid: {
    readonly active: boolean;
    readonly ranges: {
      showMenu(x: number, y: number): void;
    };
  };
}

export interface GridControlsDeps {
  readonly S: WorkbookState;
  readonly icon: (name: string) => string;
  readonly toast: (message: string, tone?: 'info' | 'success' | 'warning' | 'error') => void;
  readonly colName: (col: number) => string;
  readonly parseRef: (text: string | null | undefined) => CellRef | null;
  readonly cellAt: (row: number, col: number) => CellData;
  readonly inSelectedRange: (row: number, col: number, visualIndex?: number) => boolean;
  readonly linkDescription: (link: CellHyperlink | null | undefined) => string;
  readonly selectedColumnSpan: () => ColumnSelectionSpan | null;
  readonly selectColumnSpan: (anchor: number, end: number) => void;
  readonly hasColumnOfType: (span: ColumnSelectionSpan | null, hidden: boolean) => boolean;
  readonly selectPointedCell: (
    cell: CellData,
    point: CellRef & { readonly visual: number },
    event: MouseEvent,
  ) => void;
  readonly rowHeight: (row?: number) => number;
  readonly colWidth: (col?: number) => number;
  readonly headerHeight: () => number;
  readonly rowTop: (row: number) => number;
  readonly colLeft: (col: number) => number;
  readonly rowHead: () => number;
  readonly resetGridDOM: () => void;
  readonly updateCanvasSize: () => void;
  readonly renderInitialPreview: () => void;
  readonly paintRangeFrames: () => void;
  readonly invalidateViewport: (options?: { readonly preserveDOM?: boolean }) => void;
  readonly setVirtualScrollTop: (logical: number) => void;
  readonly scheduleViewport: (force?: boolean) => void;
  readonly getEditCopy?: () => GridControlsEditCopyBridge | null | undefined;
  readonly getPictures?: () => Pictures | null | undefined;
  readonly getFrozenPanes: () => FrozenPanes;
  readonly isDragging: () => boolean;
}

export interface GridContextMenuActions {
  readonly promptResize: (kind: 'row' | 'col', index?: number) => Promise<void> | void;
  readonly commitResize: (kind: 'row' | 'col', index: number, size: number) => Promise<void> | void;
  readonly autoFitSize: (kind: 'row' | 'col', index: number) => Promise<void> | void;
  readonly setColumnVisibility: (
    mode: string,
    index: number,
    hidden: boolean,
    end?: number,
  ) => Promise<void> | void;
  readonly copySelection: (plain?: boolean, cut?: boolean) => Promise<boolean | undefined>;
  readonly copyText: (val: string, msg?: string) => Promise<boolean>;
  readonly copyQualifiedAddress: () => Promise<boolean> | undefined;
  readonly copyCellEvidence: () => Promise<boolean> | undefined;
  readonly openCopySpecial: () => void;
  readonly filterSelectedValue: () => Promise<void> | void;
  readonly followCellHyperlink: (link: CellHyperlink | null | undefined) => Promise<void>;
  readonly openSearch: (mode?: string | null, query?: string | null) => void;
  readonly tracePrecedents: () => Promise<void> | void;
  readonly openInspector: (mode?: string) => void;
  readonly openDataRules: () => void;
  readonly openRecordCard: () => void;
  readonly openPrintPreview: () => void;
  readonly applyFreeze: (rows: number, cols: number) => Promise<void> | void;
  readonly openExport: (mode?: string) => void;
  readonly openProfile: () => void;
  readonly openSelectionQuick: () => void;
  readonly openTranspose: () => void;
  readonly openBookmark: () => void;
}

export class GridControls {
  private readonly d: GridControlsDeps;
  public hoverRef = '';
  public sizeContextTarget: SizeContextTarget | null = null;

  constructor(deps: GridControlsDeps) {
    this.d = deps;
  }

  openDialog(id: string): void {
    const dialog = requireDialog(id);
    if (typeof dialog.showModal === 'function') dialog.showModal();
    else dialog.setAttribute('open', '');
  }

  closeDialog(id: string): void {
    const dialog = requireDialog(id);
    if (dialog.open) dialog.close();
  }

  hideTooltip(): void {
    const tooltip = requireEl('cellTooltip');
    if (this.hoverRef || !tooltip.hidden) {
      tooltip.hidden = true;
      this.hoverRef = '';
    }
  }

  showTooltip(event: Pick<MouseEvent, 'target' | 'clientX' | 'clientY'>): void {
    const { S, linkDescription, getEditCopy, isDragging } = this.d;
    if (getEditCopy?.()?.grid.active) return;
    const target = event.target;
    const cellEl = target instanceof Element ? target.closest('.grid-cell') : null;
    const tooltip = requireEl('cellTooltip');
    if (
      isDragging() ||
      ((S.range || S.multiRanges.length > 0) && cellEl?.classList.contains('range-cell'))
    ) {
      tooltip.hidden = true;
      this.hoverRef = '';
      return;
    }
    if (!(cellEl instanceof HTMLElement)) {
      tooltip.hidden = true;
      this.hoverRef = '';
      return;
    }
    const ref = cellEl.dataset.ref ?? '';
    if (ref !== this.hoverRef || tooltip.hidden) {
      this.hoverRef = ref;
      const cell: Partial<CellData> =
        S.cellMap.get(ref) ?? S.frozenCellMap.get(ref) ?? { ref, display: '', raw: '' };
      const value = cell.display ?? '';
      const head = document.createElement('b');
      head.textContent = `${ref}  `;
      const text = document.createElement('span');
      text.textContent = value === '' ? '（空白）' : value;
      const content: Node[] = [head, text];
      if (cell.hyperlink) {
        const hint = document.createElement('span');
        hint.className = 'tooltip-formula tooltip-hyperlink';
        hint.textContent = `跳转至：${linkDescription(cell.hyperlink)}`;
        content.push(hint);
      }
      if (cell.raw !== undefined && cell.raw !== '' && cell.raw !== value) {
        const raw = document.createElement('span');
        raw.className = 'tooltip-formula';
        raw.textContent = `原始值：${cell.raw}`;
        content.push(raw);
      }
      if (cell.hasFormula && cell.formula) {
        const formula = document.createElement('span');
        formula.className = 'tooltip-formula';
        formula.textContent = cell.formula;
        content.push(formula);
      }
      tooltip.replaceChildren(...content);
      tooltip.hidden = false;
    }
    tooltip.style.left = `${Math.max(8, Math.min(window.innerWidth - 440, event.clientX + 14))}px`;
    tooltip.style.top = `${Math.max(8, Math.min(window.innerHeight - 160, event.clientY + 16))}px`;
  }

  setZoom(next: number): void {
    const {
      S,
      rowHeight,
      colWidth,
      headerHeight,
      rowTop,
      colLeft,
      rowHead,
      resetGridDOM,
      updateCanvasSize,
      renderInitialPreview,
      paintRangeFrames,
      invalidateViewport,
      setVirtualScrollTop,
      scheduleViewport,
      getPictures,
      getFrozenPanes,
    } = this.d;
    const value = Math.max(70, Math.min(125, Math.round(next / 5) * 5));
    if (value === S.zoom) return;
    const grid = requireEl('grid');
    const row = S.selected?.row ?? 1;
    const col = S.selected?.col ?? 1;
    S.zoom = value;
    getPictures?.()?.schedule();
    requireEl('zoomValue').textContent = `${value}%`;
    document.documentElement.style.setProperty('--row-height', `${rowHeight()}px`);
    document.documentElement.style.setProperty('--col-width', `${colWidth()}px`);
    document.documentElement.style.setProperty('--header-height', `${headerHeight()}px`);
    if (S.previewOnly && S.previewData) {
      resetGridDOM();
      updateCanvasSize();
      renderInitialPreview();
      paintRangeFrames();
      return;
    }
    invalidateViewport({ preserveDOM: true });
    updateCanvasSize();
    const frozenPanes = getFrozenPanes();
    frozenPanes.reset({ preserveDOM: true });
    frozenPanes.schedule(true);
    if (S.selected) {
      setVirtualScrollTop(headerHeight() + rowTop(row) - rowHeight(row) * 2);
      grid.scrollLeft = rowHead() + colLeft(col) - colWidth(col) * 2;
    }
    scheduleViewport(true);
    paintRangeFrames();
  }

  async toggleFullscreen(): Promise<void> {
    const { toast } = this.d;
    try {
      if (!document.fullscreenElement) await document.documentElement.requestFullscreen();
      else await document.exitFullscreen();
    } catch (error: unknown) {
      toast(`无法切换全屏：${errorMessage(error)}`, 'error');
    }
  }

  updateFullscreenButton(): void {
    const { icon, scheduleViewport } = this.d;
    const full = Boolean(document.fullscreenElement);
    requireButton('fullscreenBtn').innerHTML = icon('fullscreen') + (full ? '退出全屏' : '全屏');
    setTimeout(() => scheduleViewport(true), 80);
  }

  hideCellMenu(): void {
    requireEl('cellMenu').hidden = true;
    requireEl('sizeContextMenu').hidden = true;
    requireEl('editGridMenu').hidden = true;
  }

  positionCellMenu(menu: HTMLElement, x: number, y: number): void {
    this.hideCellMenu();
    menu.hidden = false;
    menu.style.left = `${Math.max(5, Math.min(x, window.innerWidth - menu.offsetWidth - 8))}px`;
    menu.style.top = `${Math.max(5, Math.min(y, window.innerHeight - menu.offsetHeight - 8))}px`;
  }

  openCellMenu(event: MouseEvent): void {
    const {
      S,
      colName,
      parseRef,
      cellAt,
      inSelectedRange,
      selectedColumnSpan,
      selectColumnSpan,
      hasColumnOfType,
      selectPointedCell,
      getEditCopy,
    } = this.d;
    if (!S.indexReady) {
      this.hideCellMenu();
      return;
    }
    const eventTarget = event.target;
    const head =
      eventTarget instanceof Element
        ? eventTarget.closest('.row-head,.pinned-row-head,.column-head[data-col]')
        : null;
    if (head instanceof HTMLElement) {
      const kind: 'row' | 'col' = head.dataset.col ? 'col' : 'row';
      const parentRow = head.closest('.grid-row');
      const rawIdx =
        head.dataset.col ??
        head.dataset.row ??
        (parentRow instanceof HTMLElement ? parentRow.dataset.row : undefined);
      const index = Number(rawIdx);
      if (!Number.isInteger(index) || index < 1) {
        this.hideCellMenu();
        return;
      }
      event.preventDefault();
      this.hideCellMenu();
      if (kind === 'col') {
        const previous = selectedColumnSpan();
        if (!previous || index < previous.start || index > previous.end) {
          selectColumnSpan(index, index);
        }
      }
      const span = kind === 'col' ? selectedColumnSpan() : null;
      this.sizeContextTarget = { kind, index: span?.start ?? index, end: span?.end ?? index };
      const menu = requireEl('sizeContextMenu');
      const multi = Boolean(span && span.start !== span.end);
      requireButton('sizeContextAdjust').textContent =
        kind === 'row' ? `设置第 ${index} 行高度…` : `设置 ${colName(index)} 列宽度…`;
      for (const id of ['sizeContextAdjust', 'sizeContextFit', 'sizeContextReset']) {
        requireButton(id).hidden = multi;
      }
      requireButton('sizeContextAdjust').disabled = kind === 'row' && S.filtered;
      requireButton('sizeContextReset').disabled = kind === 'row' && S.filtered;
      requireButton('sizeContextHideCol').textContent = multi ? '隐藏选中列' : '隐藏此列';
      requireButton('sizeContextShowCol').textContent = '显示隐藏列';
      requireButton('sizeContextHideCol').hidden = kind !== 'col' || !hasColumnOfType(span, false);
      requireButton('sizeContextShowCol').hidden = kind !== 'col' || !hasColumnOfType(span, true);
      this.positionCellMenu(menu, event.clientX, event.clientY);
      return;
    }
    const cell =
      eventTarget instanceof Element ? eventTarget.closest('.grid-cell,.pinned-cell') : null;
    if (!(cell instanceof HTMLElement)) {
      this.hideCellMenu();
      return;
    }
    event.preventDefault();
    const target = parseRef(cell.dataset.ref);
    if (!target) return;
    const visual = Number(cell.dataset.visualIndex);
    if (!S.range || !inSelectedRange(target.row, target.col, visual)) {
      selectPointedCell(cellAt(target.row, target.col), { ...target, visual }, event);
    }
    const editCopy = getEditCopy?.();
    if (editCopy?.grid.active) {
      editCopy.grid.ranges.showMenu(event.clientX, event.clientY);
      return;
    }
    requireButton('menuCopyFormula').disabled = !S.selected?.hasFormula;
    requireButton('menuFilterValue').disabled =
      !S.selected || S.selected.row <= (S.autoFilter?.row ?? 1);
    requireButton('menuTrace').disabled = !S.selected?.hasFormula;
    requireButton('menuOpenHyperlink').hidden = !S.selected?.hyperlink;
    if (S.selected?.hyperlink) {
      requireButton('menuOpenHyperlink').textContent =
        S.selected.hyperlink.kind === 'internal' ? '跳转到链接位置' : '打开超链接';
    }
    this.positionCellMenu(requireEl('cellMenu'), event.clientX, event.clientY);
  }

  bindMenuEvents(actions: GridContextMenuActions): void {
    const { S } = this.d;
    document.addEventListener('click', (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Element && target.closest('.cell-context-menu'))) {
        this.hideCellMenu();
      }
    });
    requireButton('sizeContextAdjust').addEventListener('click', () => {
      const target = this.sizeContextTarget;
      this.hideCellMenu();
      if (target) void actions.promptResize(target.kind, target.index);
    });
    requireButton('sizeContextReset').addEventListener('click', () => {
      const target = this.sizeContextTarget;
      this.hideCellMenu();
      if (target) void actions.commitResize(target.kind, target.index, 0);
    });
    requireButton('sizeContextFit').addEventListener('click', () => {
      const target = this.sizeContextTarget;
      this.hideCellMenu();
      if (target) void actions.autoFitSize(target.kind, target.index);
    });
    requireButton('sizeContextHideCol').addEventListener('click', () => {
      const target = this.sizeContextTarget;
      this.hideCellMenu();
      if (target?.kind === 'col') {
        void actions.setColumnVisibility(
          target.index === target.end ? 'column' : 'range',
          target.index,
          true,
          target.end,
        );
      }
    });
    requireButton('sizeContextShowCol').addEventListener('click', () => {
      const target = this.sizeContextTarget;
      this.hideCellMenu();
      if (target?.kind === 'col') {
        void actions.setColumnVisibility(
          target.index === target.end ? 'column' : 'range',
          target.index,
          false,
          target.end,
        );
      }
    });
    requireButton('menuCopyValue').addEventListener('click', () => {
      void actions.copySelection();
      this.hideCellMenu();
    });
    requireButton('menuCopyPlain').addEventListener('click', () => {
      void actions.copySelection(true);
      this.hideCellMenu();
    });
    requireButton('menuCopySpecial').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openCopySpecial();
    });
    requireButton('menuFilterValue').addEventListener('click', () => {
      this.hideCellMenu();
      void actions.filterSelectedValue();
    });
    requireButton('menuCopyFormula').addEventListener('click', () => {
      void actions.copyText(S.selected?.formula ?? '');
      this.hideCellMenu();
    });
    requireButton('menuOpenHyperlink').addEventListener('click', () => {
      const link = S.selected?.hyperlink;
      this.hideCellMenu();
      actions.followCellHyperlink(link).catch((error: unknown) => {
        this.d.toast(errorMessage(error), 'error');
      });
    });
    requireButton('menuCopyAddress').addEventListener('click', () => {
      this.hideCellMenu();
      void actions.copyQualifiedAddress();
    });
    requireButton('menuCopyEvidence').addEventListener('click', () => {
      this.hideCellMenu();
      void actions.copyCellEvidence();
    });
    requireButton('menuSearchValue').addEventListener('click', () => {
      const q = S.selected?.display ?? '';
      this.hideCellMenu();
      actions.openSearch('value', q);
    });
    requireButton('menuTrace').addEventListener('click', () => {
      this.hideCellMenu();
      void actions.tracePrecedents();
    });
    requireButton('menuInspect').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openInspector('formulas');
    });
    requireButton('menuDuplicates').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openInspector('duplicates');
    });
    requireButton('menuDataRules').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openDataRules();
    });
    requireButton('menuRecordCard').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openRecordCard();
    });
    requireButton('menuPrintPreview').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openPrintPreview();
    });
    requireButton('menuFreezeHere').addEventListener('click', () => {
      this.hideCellMenu();
      void actions.applyFreeze(
        Math.max(0, (S.selected?.row ?? 1) - 1),
        Math.max(0, (S.selected?.col ?? 1) - 1),
      );
    });
    requireButton('menuExportRange').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openExport('selection');
    });
    requireButton('menuProfileRange').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openProfile();
    });
    requireButton('menuSelectionQuick').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openSelectionQuick();
    });
    requireButton('menuTranspose').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openTranspose();
    });
    requireButton('profileQuickBtn').addEventListener('click', () => {
      requireDialog('profileDialog').close();
      actions.openSelectionQuick();
    });
    requireButton('menuBookmark').addEventListener('click', () => {
      this.hideCellMenu();
      actions.openBookmark();
    });
  }
}
