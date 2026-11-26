(() => {
  'use strict';

  class ExcelLensGridControls {
    constructor(deps) {
      this.d = deps;
      this.hoverRef = '';
      this.sizeContextTarget = null;
    }

    openDialog(id) {
      const dialog = this.d.$(id);
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    }

    closeDialog(id) {
      const dialog = this.d.$(id);
      if (dialog.open) dialog.close();
    }

    hideTooltip() {
      const tooltip = this.d.$('cellTooltip');
      if (this.hoverRef || !tooltip.hidden) {
        tooltip.hidden = true;
        this.hoverRef = '';
      }
    }

    showTooltip(event) {
      const { $, S, linkDescription, getEditCopy, isDragging } = this.d;
      if (getEditCopy?.()?.grid.active) return;
      const cellEl = event.target.closest?.('.grid-cell');
      const tooltip = $('cellTooltip');
      if (isDragging?.() || ((S.range || S.multiRanges.length) && cellEl?.classList.contains('range-cell'))) {
        tooltip.hidden = true;
        this.hoverRef = '';
        return;
      }
      if (!cellEl) {
        tooltip.hidden = true;
        this.hoverRef = '';
        return;
      }
      const ref = cellEl.dataset.ref;
      if (ref !== this.hoverRef || tooltip.hidden) {
        this.hoverRef = ref;
        const cell = S.cellMap.get(ref) || S.frozenCellMap.get(ref) || { ref, display: '', raw: '' };
        const value = cell.display ?? '';
        const head = document.createElement('b');
        head.textContent = ref + '  ';
        const text = document.createElement('span');
        text.textContent = value === '' ? '（空白）' : value;
        const content = [head, text];
        if (cell.hyperlink) {
          const hint = document.createElement('span');
          hint.className = 'tooltip-formula tooltip-hyperlink';
          hint.textContent = '\u8df3\u8f6c\u81f3\uff1a' + linkDescription(cell.hyperlink);
          content.push(hint);
        }
        if (cell.raw !== undefined && cell.raw !== '' && String(cell.raw) !== String(value)) {
          const raw = document.createElement('span');
          raw.className = 'tooltip-formula';
          raw.textContent = '原始值：' + cell.raw;
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
      // Position without forced layout; browser clamps the fixed-width tooltip.
      tooltip.style.left = Math.max(8, Math.min(window.innerWidth - 440, event.clientX + 14)) + 'px';
      tooltip.style.top = Math.max(8, Math.min(window.innerHeight - 160, event.clientY + 16)) + 'px';
    }

    setZoom(next) {
      const {
        $, S, rowHeight, colWidth, headerHeight, rowTop, colLeft, rowHead,
        resetGridDOM, updateCanvasSize, renderInitialPreview, paintRangeFrames,
        invalidateViewport, setVirtualScrollTop, scheduleViewport,
        getPictures, getFrozenPanes,
      } = this.d;
      const value = Math.max(70, Math.min(125, Math.round(next / 5) * 5));
      if (value === S.zoom) return;
      const grid = $('grid');
      const row = S.selected?.row || 1;
      const col = S.selected?.col || 1;
      S.zoom = value;
      getPictures?.()?.schedule();
      $('zoomValue').textContent = `${value}%`;
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
      const frozenPanes = getFrozenPanes?.();
      frozenPanes.reset({ preserveDOM: true });
      frozenPanes.schedule(true);
      if (S.selected) {
        setVirtualScrollTop(headerHeight() + rowTop(row) - rowHeight(row) * 2);
        grid.scrollLeft = rowHead() + colLeft(col) - colWidth(col) * 2;
      }
      scheduleViewport(true);
      // A sheet that fits the window raises no scroll event when it is zoomed.
      paintRangeFrames();
    }

    async toggleFullscreen() {
      const { toast } = this.d;
      try {
        if (!document.fullscreenElement) await document.documentElement.requestFullscreen();
        else await document.exitFullscreen();
      } catch (error) {
        toast(`无法切换全屏：${error.message}`, 'error');
      }
    }

    updateFullscreenButton() {
      const { $, icon, scheduleViewport } = this.d;
      const full = Boolean(document.fullscreenElement);
      $('fullscreenBtn').innerHTML = icon('fullscreen') + (full ? '退出全屏' : '全屏');
      setTimeout(() => scheduleViewport(true), 80);
    }

    hideCellMenu() {
      const { $ } = this.d;
      $('cellMenu').hidden = true;
      $('sizeContextMenu').hidden = true;
      $('editGridMenu').hidden = true;
    }

    positionCellMenu(menu, x, y) {
      this.hideCellMenu();
      menu.hidden = false;
      menu.style.left = Math.max(5, Math.min(x, window.innerWidth - menu.offsetWidth - 8)) + 'px';
      menu.style.top = Math.max(5, Math.min(y, window.innerHeight - menu.offsetHeight - 8)) + 'px';
    }

    openCellMenu(event) {
      const {
        $, S, colName, parseRef, cellAt, inSelectedRange,
        selectedColumnSpan, selectColumnSpan, hasColumnOfType,
        selectPointedCell, getEditCopy,
      } = this.d;
      if (!S.indexReady) {
        this.hideCellMenu();
        return;
      }
      const head = event.target.closest?.('.row-head,.pinned-row-head,.column-head[data-col]');
      if (head) {
        const kind = head.dataset.col ? 'col' : 'row';
        const index = Number(head.dataset.col || head.dataset.row || head.closest('.grid-row')?.dataset.row);
        if (!Number.isInteger(index) || index < 1) {
          this.hideCellMenu();
          return;
        }
        event.preventDefault();
        this.hideCellMenu();
        if (kind === 'col') {
          const previous = selectedColumnSpan();
          if (!previous || index < previous.start || index > previous.end) selectColumnSpan(index, index);
        }
        const span = kind === 'col' ? selectedColumnSpan() : null;
        this.sizeContextTarget = { kind, index: span?.start || index, end: span?.end || index };
        const menu = $('sizeContextMenu');
        const multi = span && span.start !== span.end;
        $('sizeContextAdjust').textContent = kind === 'row' ? '设置第 ' + index + ' 行高度…' : '设置 ' + colName(index) + ' 列宽度…';
        for (const id of ['sizeContextAdjust', 'sizeContextFit', 'sizeContextReset']) $(id).hidden = Boolean(multi);
        $('sizeContextAdjust').disabled = kind === 'row' && S.filtered;
        $('sizeContextReset').disabled = kind === 'row' && S.filtered;
        $('sizeContextHideCol').textContent = multi ? '隐藏选中列' : '隐藏此列';
        $('sizeContextShowCol').textContent = '显示隐藏列';
        $('sizeContextHideCol').hidden = kind !== 'col' || !hasColumnOfType(span, false);
        $('sizeContextShowCol').hidden = kind !== 'col' || !hasColumnOfType(span, true);
        this.positionCellMenu(menu, event.clientX, event.clientY);
        return;
      }
      const cell = event.target.closest?.('.grid-cell,.pinned-cell');
      if (!cell) {
        this.hideCellMenu();
        return;
      }
      event.preventDefault();
      const target = parseRef(cell.dataset.ref);
      if (!target) return;
      // Excel preserves an existing range when right-clicking inside it.
      if (!S.range || !inSelectedRange(target.row, target.col, Number(cell.dataset.visualIndex))) {
        selectPointedCell(cellAt(target.row, target.col), { ...target, visual: Number(cell.dataset.visualIndex) }, event);
      }
      const editCopy = getEditCopy?.();
      if (editCopy?.grid.active) {
        editCopy.grid.ranges.showMenu(event.clientX, event.clientY);
        return;
      }
      $('menuCopyFormula').disabled = !S.selected?.hasFormula;
      $('menuFilterValue').disabled = !S.selected || S.selected.row <= (S.autoFilter?.row || 1);
      $('menuTrace').disabled = !S.selected?.hasFormula;
      $('menuOpenHyperlink').hidden = !S.selected?.hyperlink;
      if (S.selected?.hyperlink) {
        $('menuOpenHyperlink').textContent =
          S.selected.hyperlink.kind === 'internal' ? '\u8df3\u8f6c\u5230\u94fe\u63a5\u4f4d\u7f6e' : '\u6253\u5f00\u8d85\u94fe\u63a5';
      }
      this.positionCellMenu($('cellMenu'), event.clientX, event.clientY);
    }

    bindMenuEvents(actions) {
      const { $, S } = this.d;
      document.addEventListener('click', event => {
        if (!event.target.closest?.('.cell-context-menu')) this.hideCellMenu();
      });
      $('sizeContextAdjust').addEventListener('click', () => {
        const target = this.sizeContextTarget;
        this.hideCellMenu();
        if (target) actions.promptResize(target.kind, target.index);
      });
      $('sizeContextReset').addEventListener('click', () => {
        const target = this.sizeContextTarget;
        this.hideCellMenu();
        if (target) actions.commitResize(target.kind, target.index, 0);
      });
      $('sizeContextFit').addEventListener('click', () => {
        const target = this.sizeContextTarget;
        this.hideCellMenu();
        if (target) actions.autoFitSize(target.kind, target.index);
      });
      $('sizeContextHideCol').addEventListener('click', () => {
        const target = this.sizeContextTarget;
        this.hideCellMenu();
        if (target?.kind === 'col') actions.setColumnVisibility(target.index === target.end ? 'column' : 'range', target.index, true, target.end);
      });
      $('sizeContextShowCol').addEventListener('click', () => {
        const target = this.sizeContextTarget;
        this.hideCellMenu();
        if (target?.kind === 'col') actions.setColumnVisibility(target.index === target.end ? 'column' : 'range', target.index, false, target.end);
      });
      $('menuCopyValue').addEventListener('click', () => { actions.copySelection(); this.hideCellMenu(); });
      $('menuCopyPlain').addEventListener('click', () => { actions.copySelection(true); this.hideCellMenu(); });
      $('menuCopySpecial').addEventListener('click', () => { this.hideCellMenu(); actions.openCopySpecial(); });
      $('menuFilterValue').addEventListener('click', () => { this.hideCellMenu(); actions.filterSelectedValue(); });
      $('menuCopyFormula').addEventListener('click', () => { actions.copyText(S.selected?.formula || ''); this.hideCellMenu(); });
      $('menuOpenHyperlink').addEventListener('click', () => {
        const link = S.selected?.hyperlink;
        this.hideCellMenu();
        actions.followCellHyperlink(link).catch(error => this.d.toast(error.message, 'error'));
      });
      $('menuCopyAddress').addEventListener('click', () => { this.hideCellMenu(); actions.copyQualifiedAddress(); });
      $('menuCopyEvidence').addEventListener('click', () => { this.hideCellMenu(); actions.copyCellEvidence(); });
      $('menuSearchValue').addEventListener('click', () => { const q = S.selected?.display ?? ''; this.hideCellMenu(); actions.openSearch('value', q); });
      $('menuTrace').addEventListener('click', () => { this.hideCellMenu(); actions.tracePrecedents(); });
      $('menuInspect').addEventListener('click', () => { this.hideCellMenu(); actions.openInspector('formulas'); });
      $('menuDuplicates').addEventListener('click', () => { this.hideCellMenu(); actions.openInspector('duplicates'); });
      $('menuDataRules').addEventListener('click', () => { this.hideCellMenu(); actions.openDataRules(); });
      $('menuRecordCard').addEventListener('click', () => { this.hideCellMenu(); actions.openRecordCard(); });
      $('menuPrintPreview').addEventListener('click', () => { this.hideCellMenu(); actions.openPrintPreview(); });
      $('menuFreezeHere').addEventListener('click', () => {
        this.hideCellMenu();
        actions.applyFreeze(Math.max(0, (S.selected?.row || 1) - 1), Math.max(0, (S.selected?.col || 1) - 1));
      });
      $('menuExportRange').addEventListener('click', () => { this.hideCellMenu(); actions.openExport('selection'); });
      $('menuProfileRange').addEventListener('click', () => { this.hideCellMenu(); actions.openProfile(); });
      $('menuSelectionQuick').addEventListener('click', () => { this.hideCellMenu(); actions.openSelectionQuick(); });
      $('menuTranspose').addEventListener('click', () => { this.hideCellMenu(); actions.openTranspose(); });
      $('profileQuickBtn').addEventListener('click', () => { $('profileDialog').close(); actions.openSelectionQuick(); });
      $('menuBookmark').addEventListener('click', () => { this.hideCellMenu(); actions.openBookmark(); });
    }
  }

  window.ExcelLensGridControls = ExcelLensGridControls;
})();
