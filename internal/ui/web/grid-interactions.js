(() => {
  'use strict';

  class ExcelLensGridInteractions {
    constructor(deps) {
      this.d = deps;
      this.dragAnchor = null;
      this.dragMoved = false;
      this.dragBase = [];
      this.suppressGridClick = false;
      this.dragFrame = 0;
      this.dragPending = null;
      this.scrollPress = null;
      this.ribbonSequence = '';
      this.ribbonExpires = 0;
    }

    isDragging() {
      return Boolean(this.dragAnchor);
    }

    selectPointedCell(cell, point, event) {
      const { selectCell, mergedCells, colLeft, rowTop } = this.d;
      selectCell(cell, point.row, point.col, point.ref, point.visual, false, false, mergedCells.pointerCell(event, colLeft, rowTop));
    }

    // Ctrl-click adds an independently selected cell; Ctrl-drag adds a rectangle.
    // Shift extends the active rectangle. Source cells remain immutable.
    selectWithModifiers(dataCell, point, event) {
      const { $, S, colName, toast, cellAt, selectCell, extendRange, applySelectionClasses, panels } = this.d;
      const selection = window.ExcelLensMultiSelection;
      const additive = Boolean(event.ctrlKey || event.metaKey);
      const extend = Boolean(event.shiftKey);
      const oldAnchor = extend
        ? (S.rangeAnchor || {
            row: S.selected?.row || point.row,
            col: S.selected?.col || point.col,
            visual: S.selectedVisualIndex || point.visual,
          })
        : null;
      const previous = additive
        ? (extend ? S.multiRanges.slice() : selection.areas(S, true))
        : (extend ? S.multiRanges.slice() : []);
      if (additive && !extend) {
        const removed = selection.removeSingleton(previous, point.row, point.col, point.visual, S.filtered);
        if (removed && removed.length === 0) {
          // Keep one active cell: a completely empty selection is not useful.
          panels.selectionStats?.schedule();
          return;
        }
        if (removed && removed.length) {
          const last = removed[removed.length - 1];
          const row = last.row2;
          const col = last.col2;
          const visual = last.visual2 || row;
          selectCell(cellAt(row, col), row, col, `${colName(col)}${row}`, visual);
          S.multiRanges = removed.slice(0, -1);
          S.range = last;
          $('selectionStatus').textContent = removed.length > 1 ? `${removed.length} 处选区` : '1 处选区';
          applySelectionClasses();
          panels.selectionStats?.schedule();
          return;
        }
      }
      if (previous.length >= selection.LIMIT) {
        toast('最多同时选择256处区域', 'warning');
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
        S.range = selection.rectangle(point, point, S.filtered);
        $('selectionStatus').textContent = `${previous.length + 1} 处选区`;
        applySelectionClasses();
        panels.selectionStats?.schedule();
      }
    }

    flushRangeDrag() {
      const { S, cellAt, selectCell, extendRange } = this.d;
      this.dragFrame = 0;
      const pending = this.dragPending;
      this.dragPending = null;
      if (!pending || !this.dragAnchor) return;
      if (!this.dragMoved) {
        this.dragMoved = true;
        selectCell(
          this.dragAnchor.cell || cellAt(this.dragAnchor.row, this.dragAnchor.col),
          this.dragAnchor.row,
          this.dragAnchor.col,
          this.dragAnchor.ref,
          this.dragAnchor.visual,
          false,
          false,
          this.dragAnchor.crosshair
        );
      }
      S.multiRanges = this.dragBase;
      extendRange(this.dragAnchor, pending.row, pending.col, pending.visual);
    }

    // A styled tile may be on screen one frame before its interactive DOM.
    // Resolve a body press against the same snapped edges and source data as
    // the renderer. A missing tile has no known cell to select.
    cachedBodyPress(event) {
      const { $, S, tiles, getViewportRequest, rowHead, headerHeight, rowTop, colLeft, activeMerge, colName } = this.d;
      if (!['rowsLayer', 'gridCanvas', 'viewportFallback', 'grid'].includes(event.target.id)) return null;
      const grid = $('grid');
      const rect = grid.getBoundingClientRect();
      const request = getViewportRequest();
      const { pw, ph } = request.position;
      const rh = rowHead();
      const localX = event.clientX - rect.left - grid.clientLeft - rh;
      const localY = event.clientY - rect.top - grid.clientTop - headerHeight();
      if (localX < pw || localY < ph || localX + rh >= grid.clientWidth || localY + headerHeight() >= grid.clientHeight) return null;
      const hit = tiles.sourceAtPoint(request, {
        x: request.position.x + localX,
        y: request.position.y + localY - (S.filtered ? ph : 0),
        lastRow: S.filtered ? S.filterTotal : S.maxRow,
        lastCol: S.maxCol,
        rowTop,
        colLeft,
        mergeAt: activeMerge,
      });
      if (hit) hit.ref = colName(hit.col) + hit.row;
      return hit;
    }

    beginRangeDrag(event) {
      const { $, S, parseRef, cellAt, selectCell, viewportContext, toast, controls, panels } = this.d;
      this.scrollPress = null;
      if (event.button !== 0 || !S.indexReady || event.target.closest('.column-filter,.auto-filter-button,.column-resize-grip')) return;
      const cell = event.target.closest?.('.grid-cell');
      const readingPress =
        event.pointerType === 'mouse' &&
        !event.shiftKey &&
        !event.ctrlKey &&
        !event.metaKey &&
        !event.altKey &&
        !panels.editCopy?.grid.active &&
        !panels.calculator?.picking &&
        !S.sheetLoadingEpoch &&
        event.target.closest('#grid');
      const cached = !cell && readingPress ? this.cachedBodyPress(event) : null;
      const parsed = cached || parseRef(cell?.dataset.ref);
      if (!parsed) return;
      controls.hideCellMenu();
      $('cellTooltip').hidden = true;
      this.dragAnchor = {
        row: parsed.row,
        col: parsed.col,
        ref: parsed.ref,
        visual: cached?.visual || Number(cell?.dataset.visualIndex) || parsed.row,
        cell: cached?.cell || cell?._xlModel,
        crosshair: cached?.crosshair,
        clientX: event.clientX,
        clientY: event.clientY,
      };
      this.dragBase =
        event.ctrlKey || event.metaKey
          ? window.ExcelLensMultiSelection.areas(S, true)
          : (event.shiftKey ? S.multiRanges.slice() : []);
      if (this.dragBase.length >= window.ExcelLensMultiSelection.LIMIT) {
        this.dragAnchor = null;
        toast('最多同时选择256处区域', 'warning');
        return;
      }
      this.dragPending = null;
      if (this.dragFrame) {
        cancelAnimationFrame(this.dragFrame);
        this.dragFrame = 0;
      }
      this.dragMoved = false;
      // During native scrolling the release can land on a different row or the
      // common rows layer. Bind a plain mouse selection to the source actually
      // pressed. Activating a link still requires a click on the original cell.
      if (readingPress) {
        if (cached) selectCell(cached.cell, cached.row, cached.col, cached.ref, cached.visual, false, false, cached.crosshair);
        else this.selectWithModifiers(cell._xlModel || cellAt(parsed.row, parsed.col), this.dragAnchor, event);
        this.scrollPress = { ref: parsed.ref, epoch: S.sheetEpoch, context: viewportContext(), view: S.rowView?.id, filter: S.filterId };
        $('grid').focus({ preventScroll: true });
      }
    }

    updateRangeDrag(event) {
      const { parseRef } = this.d;
      if (!this.dragAnchor || !(event.buttons & 1)) return;
      if (!this.dragMoved && event.clientX === this.dragAnchor.clientX && event.clientY === this.dragAnchor.clientY) return;
      const cell = event.target.closest?.('.grid-cell');
      const target = parseRef(cell?.dataset.ref);
      if (!target || (target.row === this.dragAnchor.row && target.col === this.dragAnchor.col)) return;
      this.dragPending = { row: target.row, col: target.col, visual: Number(cell.dataset.visualIndex) || target.row };
      if (!this.dragFrame) this.dragFrame = requestAnimationFrame(() => this.flushRangeDrag());
    }

    endRangeDrag(event) {
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
            `${colName(Math.max(area.col1, area.col2))}${Math.max(area.row1, area.row2)}`
          );
        }
        this.suppressGridClick = true;
        setTimeout(() => { this.suppressGridClick = false; }, 0);
      }
      if (this.dragMoved || event?.type === 'pointercancel') this.scrollPress = null;
      this.dragAnchor = null;
      this.dragBase = [];
    }

    selectNativeRange(kind) {
      const { $, S, extendRange } = this.d;
      if (!S.indexReady || !S.selected) return;
      const row = S.selected.row;
      const col = S.selected.col;
      const visual = S.filtered ? S.selectedVisualIndex : row;
      if (kind === 'row') {
        extendRange({ row, col: 1, visual }, row, Math.max(1, S.usedMaxCol), visual);
      } else if (kind === 'column') {
        extendRange(
          { row: S.filtered ? row : 1, col, visual: S.filtered ? 1 : 1 },
          S.filtered ? row : Math.max(1, S.usedMaxRow),
          col,
          S.filtered ? S.filterTotal : S.usedMaxRow
        );
      } else {
        extendRange(
          { row: 1, col: 1, visual: 1 },
          Math.max(1, S.usedMaxRow),
          Math.max(1, S.usedMaxCol),
          S.filtered ? S.filterTotal : S.usedMaxRow
        );
      }
      $('grid').focus({ preventScroll: true });
    }

    switchSheet(delta) {
      const { S, selectSheetTab, toast } = this.d;
      if (!S.sheet || !S.sheets.length) return;
      const index = S.sheets.findIndex(x => x.name === S.sheet);
      const next = (index + delta + S.sheets.length) % S.sheets.length;
      selectSheetTab(S.sheets[next].name).catch(error => toast(error.message, 'error'));
    }

    // Excel Home ribbon access keys: Alt+H, O, I/A/W/H. Unlike a global
    // keylogger this tiny state machine is active only while the grid is focused
    // and expires, so text fields and browser navigation remain unmodified.
    handleRibbonSequence(event, key, gridFocused, take) {
      const { S, autoFitSize, promptResize } = this.d;
      if (!gridFocused || event.ctrlKey || event.metaKey || event.shiftKey) return false;
      const now = Date.now();
      if (now > this.ribbonExpires) this.ribbonSequence = '';
      if (event.altKey && key === 'h') {
        this.ribbonSequence = 'H';
        this.ribbonExpires = now + 3000;
        take(() => {});
        return true;
      }
      if (!this.ribbonSequence || event.key.length !== 1) return false;
      const next = this.ribbonSequence + key.toUpperCase();
      if (next === 'HO') {
        this.ribbonSequence = next;
        this.ribbonExpires = now + 3000;
        take(() => {});
        return true;
      }
      if (this.ribbonSequence === 'HO') {
        this.ribbonSequence = '';
        const target = {
          I: () => autoFitSize('col', S.selected?.col || 1),
          A: () => autoFitSize('row', S.selected?.row || 1),
          W: () => promptResize('col', S.selected?.col || 1),
          H: () => promptResize('row', S.selected?.row || 1),
        }[key.toUpperCase()];
        if (target) {
          take(target);
          return true;
        }
      } else {
        this.ribbonSequence = '';
      }
      return false;
    }

    // Excel-style keyboard routing: do not hijack editing, browser selection or
    // modal dialog controls. Only the grid receives caret/navigation keys;
    // read-only application commands also work after toolbar clicks.
    handleSpreadsheetKeydown(event) {
      const {
        $, S, toast, colName, rowHead, rowHeight, colWidth,
        controls, clipboard, workbookIO, panels,
        openCommandPalette, openSearch, toggleAutoFilter, toggleFormulaView,
        showCellDetail, stepSearch, goHistory, jumpDataEdge, moveSelection,
        navigateToCell, extendRange, followCellHyperlink,
      } = this.d;
      if (window.ExcelLensDisplayFormat?.isComposing?.(event) || event.isComposing || event.keyCode === 229) return;
      if (panels.editCopy?.grid.active && !document.querySelector('dialog:modal')) return;
      const key = event.key.toLowerCase();
      const ctrl = event.ctrlKey || event.metaKey;
      const alt = event.altKey;
      const shift = event.shiftKey;
      const active = document.activeElement;
      const editing = active?.matches?.('input,textarea,select,[contenteditable="true"],[role="searchbox"]') || active?.isContentEditable;
      const modal = document.querySelector('dialog:modal');
      const insideFormula = active === $('formulaText') || $('formulaText').contains(active);
      const gridFocused = active === $('grid') || insideFormula || active?.closest?.('.grid-region');
      const take = callback => {
        event.preventDefault();
        event.stopPropagation();
        Promise.resolve().then(callback).catch(err => toast(err.message, 'error'));
      };
      const readOnlyHint = () =>
        $('editCopyOpen').hidden ? '当前是只读浏览，原文件不会被修改' : '当前是只读浏览。要修改内容，请选择“编辑副本”';

      if (!modal && ctrl && alt && !shift && key === 'c') { take(() => panels.calculator.toggle()); return; }
      if (!modal && panels.calculator?.handleKey(event)) return;
      if (event.key === 'Escape') {
        this.ribbonSequence = '';
        controls.hideCellMenu();
        panels.autoFilterUI?.hide();
        panels.readingMode?.close();
        panels.highlightMode?.close();
        if ($('traceDialog').open && !modal) panels.traceUI?.minimize();
        if ($('searchDialog').open && !modal) {
          event.preventDefault();
          controls.closeDialog('searchDialog');
        }
        return;
      }
      if (modal || event.defaultPrevented) { this.ribbonSequence = ''; return; }
      if (ctrl && !alt && !shift && key === 'enter' && gridFocused && !editing && S.selected?.hyperlink) {
        take(() => followCellHyperlink(S.selected.hyperlink));
        return;
      }
      if (ctrl && !alt && !shift && key === 'o' && !editing) { take(() => workbookIO.chooseFile()); return; }
      if (!$('welcome').hidden || !S.opened || !S.indexReady) return;
      if (ctrl && !alt && !shift && key === 'c' && window.getSelection()?.toString()) return;
      if (insideFormula && ctrl && !alt && !shift && key === 'a') {
        take(() => {
          const range = document.createRange();
          range.selectNodeContents($('formulaText'));
          const selection = window.getSelection();
          selection?.removeAllRanges();
          selection?.addRange(range);
        });
        return;
      }
      if (editing) return;
      if (this.handleRibbonSequence(event, key, gridFocused, take)) return;

      if (ctrl && !alt && !shift && key === 'k') { take(openCommandPalette); return; }
      if (ctrl && !alt && !shift && key === 's') { take(() => panels.exportCenter.open()); return; }
      if (ctrl && !alt && !shift && key === 'f') { take(() => openSearch()); return; }
      if ((ctrl && !alt && !shift && key === 'g') || event.key === 'F5') {
        take(() => { $('nameBox').focus(); $('nameBox').select(); });
        return;
      }
      if (ctrl && !alt && !shift && key === 'c') { take(() => clipboard.copySelection()); return; }
      if (ctrl && alt && shift && key === 'c') { take(() => panels.copySpecial.open()); return; }
      if (ctrl && !alt && shift && key === 'c') {
        take(() => (S.selected?.hasFormula ? clipboard.copyText(S.selected.formula || '') : toast('当前单元格不是公式')));
        return;
      }
      if (ctrl && !alt && !shift && key === 'a' && gridFocused) { take(() => this.selectNativeRange('all')); return; }
      if (ctrl && !alt && !shift && event.code === 'Space' && gridFocused) { take(() => this.selectNativeRange('column')); return; }
      if (ctrl && !alt && shift && event.code === 'Space' && gridFocused) { take(() => this.selectNativeRange('row')); return; }
      if (!ctrl && !alt && shift && event.code === 'Space' && gridFocused) { take(() => this.selectNativeRange('row')); return; }
      if (ctrl && shift && !alt && key === 'l') { take(toggleAutoFilter); return; }
      if (ctrl && !alt && !shift && (event.code === 'Backquote' || event.key === '`')) { take(toggleFormulaView); return; }
      if (ctrl && alt && key === 'g') { take(() => panels.selectionInspector.open('formulas')); return; }
      if (ctrl && shift && !alt && key === 'd') { take(() => panels.selectionInspector.open('duplicates')); return; }
      if (ctrl && shift && !alt && key === 'e') { take(() => panels.exportCenter.open()); return; }
      if (ctrl && alt && key === 's') { take(() => panels.profileCenter.open()); return; }
      if (ctrl && alt && key === 'b') { take(() => panels.bookmarkCenter.open()); return; }
      if (ctrl && !shift && !alt && key === 'q') { take(() => panels.selectionQuick?.open()); return; }
      if (ctrl && !alt && !shift && key === '1') { take(showCellDetail); return; }
      if (event.key === 'F2') { take(showCellDetail); return; }
      if (ctrl && !alt && !shift && (event.key === 'PageUp' || event.key === 'PageDown')) {
        take(() => this.switchSheet(event.key === 'PageUp' ? -1 : 1));
        return;
      }
      if (!ctrl && !alt && event.key === 'F3') { take(() => stepSearch(shift ? -1 : 1)); return; }
      if (ctrl && !alt && !shift && event.key === 'F3') { take(() => panels.catalogueCenter.open()); return; }
      if (alt && !ctrl && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
        take(() => goHistory(event.key === 'ArrowLeft' ? -1 : 1));
        return;
      }
      if (alt && !ctrl && !shift && event.key === 'ArrowDown' && S.selected) {
        take(() => {
          if (!S.autoFilter) {
            toast('先按 Ctrl+Shift+L 开启自动筛选', 'warning');
            return;
          }
          const c = S.selected.col;
          const bounds = $('grid').getBoundingClientRect();
          const anchor =
            [...document.querySelectorAll(`.auto-filter-button[data-column="${c}"]`)].find(node => {
              const rect = node.getBoundingClientRect();
              return rect.width > 0 && rect.top >= bounds.top && rect.bottom <= bounds.bottom && rect.left >= bounds.left && rect.right <= bounds.right;
            }) || $('filterOpenBtn');
          return panels.autoFilterUI.open(c, anchor);
        });
        return;
      }
      if (!gridFocused) return;
      if ((ctrl && !alt && ['x', 'v', 'z', 'y'].includes(key)) || (!ctrl && !alt && (event.key === 'Backspace' || event.key === 'Delete'))) {
        take(() => toast(readOnlyHint()));
        return;
      }
      if (ctrl && !alt && ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) {
        const direction = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' }[event.key];
        take(() => jumpDataEdge(direction, shift));
        return;
      }
      if (!ctrl && !alt && ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key)) {
        const [row, col] = { ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1] }[event.key];
        take(() => moveSelection(row, col, shift));
        return;
      }
      if (!ctrl && !alt && (event.key === 'Tab' || event.key === 'Enter')) {
        const sign = shift ? -1 : 1;
        take(() => (event.key === 'Tab' ? moveSelection(0, sign) : moveSelection(sign, 0)));
        return;
      }
      // Selection extension uses the ORIGINAL anchor, even though navigation
      // itself refreshes the active cell and would otherwise discard the range.
      if ((event.key === 'Home' || event.key === 'End') && !alt) {
        if (event.key === 'End' && !ctrl) return;
        const targetRow = event.key === 'End' ? S.usedMaxRow : (ctrl ? 1 : S.selected?.row || 1);
        const targetCol = event.key === 'End' ? S.usedMaxCol : 1;
        take(async () => {
          const anchor = shift ? (S.rangeAnchor || { row: S.selected.row, col: S.selected.col, visual: S.selectedVisualIndex }) : null;
          if (anchor && S.filtered) {
            toast('筛选后请通过可见行拖选扩展区域', 'warning');
            return;
          }
          await navigateToCell(S.sheet, `${colName(targetCol)}${targetRow}`);
          if (anchor) extendRange(anchor, targetRow, targetCol);
        });
        return;
      }
      if (alt && !ctrl && (event.key === 'PageUp' || event.key === 'PageDown')) {
        const width = $('grid').clientWidth - rowHead();
        const count = Math.max(1, Math.floor(width / Math.max(32, colWidth(S.selected?.col || 1))) - 1);
        take(() => moveSelection(0, (event.key === 'PageUp' ? -1 : 1) * count, shift));
        return;
      }
      if (!ctrl && !alt && (event.key === 'PageUp' || event.key === 'PageDown')) {
        const step = Math.max(1, Math.floor($('grid').clientHeight / rowHeight()) - 2);
        take(() => moveSelection(event.key === 'PageDown' ? step : -step, 0, shift));
        return;
      }
    }

    bindHomeAndFileEvents() {
      const { $, S, api, toast, setStatus, showWelcome, workbookIO, panels } = this.d;
      const homeMenu = $('homeDataMenu');
      const menuTrigger = $('homeDataTrigger');
      homeMenu.addEventListener('click', event => {
        if (!event.target.closest('[role="button"]')) return;
        const selection = window.getSelection();
        if (event.detail && !selection.isCollapsed && homeMenu.contains(selection.anchorNode)) {
          event.stopPropagation();
          return;
        }
        homeMenu.open = false;
        menuTrigger.focus();
      }, true);
      homeMenu.addEventListener('keydown', event => {
        if (event.target.matches('[role="button"]') && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          event.stopPropagation();
          if (!event.repeat) event.target.click();
          return;
        }
        if (event.key === 'Escape' && homeMenu.open) {
          event.preventDefault();
          event.stopPropagation();
          homeMenu.open = false;
          menuTrigger.focus();
        }
      });
      document.addEventListener('pointerdown', event => {
        if (homeMenu.open && !homeMenu.contains(event.target)) homeMenu.open = false;
      });
      window.addEventListener('excellens:session-expired', event => {
        setStatus(event.detail);
        toast(event.detail, 'warning');
      });
      document.addEventListener('toolbar-setting-error', event => toast(event.detail, 'error'));
      panels.commandPalette.bind();
      $('openLocalMenuBtn').addEventListener('click', () => workbookIO.chooseFile());
      for (const id of ['openDatabaseBtn', 'openDatabaseHome']) {
        $(id).addEventListener('click', () => {
          if (workbookIO.canOpenWorkbook('连接数据库')) void panels.databasePanel.open();
        });
      }
      for (const id of ['databaseSnapshotsBtn', 'databaseSnapshotsHome']) {
        $(id).addEventListener('click', () => panels.databaseSnapshots.show());
      }
      $('retainDatabaseSnapshot').addEventListener('click', () => panels.databaseSnapshots.show(S.workbookKey));
      $('homeBtn').addEventListener('click', () => {
        if (workbookIO.canOpenWorkbook('返回首页')) {
          showWelcome();
          panels.recentManager.load();
        }
      });
      $('chooseFileBtn').addEventListener('click', () => workbookIO.chooseFile());
      $('fileInput').addEventListener('change', event => {
        const file = event.target.files?.[0];
        event.target.value = '';
        workbookIO.uploadFile(file);
      });
      for (const id of ['exitBtn', 'homeExitBtn']) {
        $(id).addEventListener('click', async () => {
          if (!workbookIO.canOpenWorkbook('退出程序')) return;
          try {
            await api('/api/exit', { method: 'POST' });
          } catch {
            /* process may close immediately */
          }
          setStatus('程序已退出');
        });
      }
    }

    bindToolbarAndDialogEvents() {
      const {
        $, S, toast, controls, clipboard, workbookIO, panels,
        navigateNameBox, toggleFormulaView, openCommandPalette, toggleAutoFilter,
        applyTextTableHeader, disableTextTableHeader, openFilter, openFreezeDialog,
        toggleOriginalFormat, changeDisplayFont, applyFreeze, goHistory,
        addFilterRule, applyFilter, cancelFilter, clearFilter,
        tracePrecedents, traceDependents, showCellDetail, selectSheet,
      } = this.d;
      const isComposing = event =>
        window.ExcelLensDisplayFormat?.isComposing?.(event) ?? (event.isComposing || event.keyCode === 229);

      $('nameBox').addEventListener('keydown', event => {
        if (isComposing(event)) return;
        if (event.key === 'Enter') {
          event.preventDefault();
          const address = $('nameBox').value;
          const navigate = () =>
            navigateNameBox(address).then(() => (panels.editCopy?.grid.active ? panels.editCopy.grid.focus() : $('grid').focus({ preventScroll: true })));
          if (panels.editCopy?.grid.active) panels.editCopy.grid.navigate(navigate);
          else navigate().catch(err => toast(err.message, 'error'));
        } else if (event.key === 'Escape') {
          $('nameBox').value = S.selected?.ref || '';
          $('nameBox').blur();
        }
      });
      panels.searchPanel.bind?.();
      $('multiSortBtn').addEventListener('click', () => panels.multiSort.show());
      $('inspectOpenBtn').addEventListener('click', () => panels.selectionInspector.open());
      $('duplicateOpenBtn').addEventListener('click', () => panels.selectionInspector.open('duplicates'));
      $('formulaViewBtn').addEventListener('click', toggleFormulaView);
      $('exportOpenBtn').addEventListener('click', () => panels.exportCenter.open());
      $('profileOpenBtn').addEventListener('click', () => panels.profileCenter.open());
      $('commandOpenBtn').addEventListener('click', () => openCommandPalette());
      $('selectionStatus').addEventListener('click', () => panels.profileCenter.open());
      $('filterOpenBtn').addEventListener('click', toggleAutoFilter);
      $('textHeaderBtn').addEventListener('click', () => {
        if (!S.indexReady) return;
        $('textHeaderRow').value = String(S.autoFilter?.row || S.selected?.row || 1);
        $('textHeaderAuto').disabled = !S.source?.tableHeader;
        controls.openDialog('textHeaderDialog');
      });
      $('textHeaderApply').addEventListener('click', () => applyTextTableHeader(Number($('textHeaderRow').value)));
      $('textHeaderRow').addEventListener('keydown', event => {
        if (!isComposing(event) && event.key === 'Enter') {
          event.preventDefault();
          applyTextTableHeader(Number(event.target.value));
        }
      });
      $('textHeaderAuto').addEventListener('click', () => applyTextTableHeader(0, true));
      $('textHeaderPlain').addEventListener('click', disableTextTableHeader);
      $('advancedFilterBtn').addEventListener('click', () => openFilter());
      $('freezeBtn').addEventListener('click', openFreezeDialog);
      $('formatBtn').addEventListener('click', toggleOriginalFormat);
      $('fontSelect').addEventListener('change', event => changeDisplayFont(event.target.value));
      $('freezeFirstRowBtn').addEventListener('click', () => applyFreeze(1, S.freeze.cols));
      $('freezeFirstColBtn').addEventListener('click', () => applyFreeze(S.freeze.rows, 1));
      $('freezeAtCellBtn').addEventListener('click', () => applyFreeze(Math.max(0, (S.selected?.row || 1) - 1), Math.max(0, (S.selected?.col || 1) - 1)));
      $('freezeResetBtn').addEventListener('click', () => applyFreeze(0, 0));
      $('freezeApplyBtn').addEventListener('click', () => applyFreeze($('freezeRowsInput').value, $('freezeColsInput').value));
      $('historyBackBtn').addEventListener('click', () => goHistory(-1));
      $('historyForwardBtn').addEventListener('click', () => goHistory(1));
      $('helpBtn').addEventListener('click', () => controls.openDialog('helpDialog'));
      $('addFilterRuleBtn').addEventListener('click', () => addFilterRule());
      $('applyFilterBtn').addEventListener('click', applyFilter);
      $('cancelFilterJobBtn').addEventListener('click', async () => {
        const jobID = S.filterJob;
        await cancelFilter(jobID);
        $('filterProgressText').textContent = '已请求取消筛选任务';
        $('applyFilterBtn').disabled = false;
        $('cancelFilterJobBtn').hidden = true;
      });
      $('clearFilterBtn').addEventListener('click', () => clearFilter().catch(error => toast(error.message, 'error')));
      $('sheetListBtn').addEventListener('click', () => {
        workbookIO.renderSheetPicker();
        controls.openDialog('sheetDialog');
        $('sheetPickerSearch').focus();
      });
      $('sheetPrevBtn').addEventListener('click', () => workbookIO.pageSheetTabs(-1));
      $('sheetNextBtn').addEventListener('click', () => workbookIO.pageSheetTabs(1));
      $('sheetTabs').addEventListener('scroll', () => workbookIO.updateSheetPager(), { passive: true });
      new ResizeObserver(() => workbookIO.updateSheetPager()).observe($('sheetTabs'));
      $('sheetPickerSearch').addEventListener('input', event => workbookIO.renderSheetPicker(event.target.value));
      $('tracePrecedentsBtn').addEventListener('click', tracePrecedents);
      $('traceDependentsBtn').addEventListener('click', traceDependents);
      $('cellDetailBtn').addEventListener('click', showCellDetail);
      $('copyFormulaBtn').addEventListener('click', () => clipboard.copyText(S.selected?.formula || ''));
      $('copyValueBtn').addEventListener('click', () => clipboard.copyText(S.selected?.display ?? S.selected?.raw ?? ''));
      $('copySelectionBtn').addEventListener('click', () => clipboard.copySelection());
      $('copyPlainBtn').addEventListener('click', () => clipboard.copySelection(true));
      $('copySpecialBtn').addEventListener('click', () => panels.copySpecial.open());
      $('copyAddressBtn').addEventListener('click', () => clipboard.copyQualifiedAddress());
      $('copyEvidenceBtn').addEventListener('click', () => clipboard.copyCellEvidence());
      $('selectionQuickBtn').addEventListener('click', () => panels.selectionQuick?.open());
      $('transposeOpenBtn').addEventListener('click', () => panels.transposeCenter?.open());
      $('snapshotsOpenBtn').addEventListener('click', () => window.ExcelLensSnapshots?.open());
      $('taskRecoveryOpenBtn').addEventListener('click', () => panels.taskRecovery.open());
      $('fullscreenBtn').addEventListener('click', () => controls.toggleFullscreen());
      document.addEventListener('fullscreenchange', () => controls.updateFullscreenButton());
      $('zoomOutBtn').addEventListener('click', () => controls.setZoom(S.zoom - 5));
      $('zoomInBtn').addEventListener('click', () => controls.setZoom(S.zoom + 5));
      $('retryIndexBtn').addEventListener('click', () => selectSheet(S.sheet));
    }

    bindGridPointerAndScrollEvents() {
      const {
        $, S, toast, virtualScroll, motion, controls, panels,
        rowHeight, headerHeight, getVirtualScrollTop,
        followGridScroll, scheduleRangeFrames, syncTableColumnHeaders, scheduleViewport,
        selectWholeRow, selectColumnSpan, captureColumnSelection,
        parseRef, cellAt, viewportContext, followCellHyperlink, showCellDetail,
      } = this.d;

      $('grid').addEventListener('wheel', event => {
        if (event.ctrlKey) {
          event.preventDefault();
          controls.setZoom(S.zoom + (event.deltaY < 0 ? 5 : -5));
          return;
        }
        if (virtualScroll.scale === 1 || !event.cancelable) return;
        event.preventDefault();
        const grid = $('grid');
        const next = virtualScroll.wheel(event, grid.scrollTop, rowHeight(), grid.clientHeight - headerHeight());
        grid.scrollLeft += next.left;
        grid.scrollTop = next.top;
        followGridScroll();
      }, { passive: false });

      $('grid').addEventListener('scroll', () => {
        scheduleRangeFrames();
        if (S.sheetLoadingEpoch) return;
        if (!$('cellMenu').hidden || !$('sizeContextMenu').hidden || !$('editGridMenu').hidden) controls.hideCellMenu();
        followGridScroll();
        syncTableColumnHeaders();
        panels.frozenPanes?.followScroll();
        if (S.freeze.rows || S.freeze.cols) panels.frozenPanes.schedule();
        controls.hideTooltip();
        motion.sample($('grid').scrollLeft, getVirtualScrollTop(), performance.now());
        scheduleViewport(false);
      }, { passive: true });

      $('grid').addEventListener('click', event => {
        if (this.suppressGridClick) {
          this.suppressGridClick = false;
          return;
        }
        const rowHead = event.target.closest?.('.row-head');
        if (rowHead) {
          if (event.target.closest?.('.row-resize-grip')) return;
          const owner = rowHead.closest('.grid-row');
          selectWholeRow(Number(owner?.dataset.row), Number(owner?.dataset.visualIndex));
          return;
        }
        const cell = event.target.closest?.('.grid-cell');
        if (!cell) return;
        const ref = cell.dataset.ref;
        const parsed = parseRef(ref);
        if (!parsed) return;
        const visual = Number(cell.dataset.visualIndex);
        const dataCell = cellAt(parsed.row, parsed.col);
        const pressed = this.scrollPress;
        this.scrollPress = null;
        if (pressed && event.detail) {
          if (pressed.epoch !== S.sheetEpoch || pressed.context !== viewportContext() || pressed.view !== S.rowView?.id || pressed.filter !== S.filterId || pressed.ref !== ref) {
            return;
          }
        } else {
          this.selectWithModifiers(dataCell, { ...parsed, visual }, event);
        }
        const calculating = panels.calculator?.captureCell(parsed.ref, event);
        if (!panels.calculator?.picking && !event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey && dataCell.hyperlink) {
          followCellHyperlink(dataCell.hyperlink).catch(error => toast(error.message, 'error'));
        }
        if(!calculating)$("grid").focus({ preventScroll: true });
        $('cellTooltip').hidden = true;
      });

      const region = document.querySelector('.grid-region');
      region.addEventListener('click', event => {
        if (this.suppressGridClick) {
          this.suppressGridClick = false;
          return;
        }
        if (event.target.closest('#grid')) return;
        const frozenRowHead = event.target.closest('.pinned-row-head');
        if (frozenRowHead) {
          if (!event.target.closest?.('.row-resize-grip')) selectWholeRow(Number(frozenRowHead.dataset.row));
          return;
        }
        const column = event.target.closest('.pinned-head');
        if (column) {
          if (event.target.closest?.('.column-resize-grip,.column-filter,.auto-filter-button')) return;
          const col = Number(column.dataset.col);
          if (event.detail === 0 || !S.columnSelection) {
            selectColumnSpan(event.shiftKey ? (S.columnAnchor || col) : col, col);
            captureColumnSelection();
          }
          return;
        }
        const cell = event.target.closest('.pinned-cell');
        if (!cell) return;
        const parsed = parseRef(cell.dataset.ref);
        if (!parsed) return;
        const visual = Number(cell.dataset.visualIndex) || parsed.row;
        const dataCell = cellAt(parsed.row, parsed.col);
        this.selectWithModifiers(dataCell, { ...parsed, visual }, event);
        const calculating = panels.calculator?.captureCell(parsed.ref, event);
        if (!panels.calculator?.picking && !event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey && dataCell.hyperlink) {
          followCellHyperlink(dataCell.hyperlink).catch(error => toast(error.message, 'error'));
        }
        if(!calculating)$("grid").focus({ preventScroll: true });
      });

      region.addEventListener('pointermove', event => {
        if (event.target.closest('#grid')) return;
        controls.showTooltip(event);
      });
      region.addEventListener('contextmenu', event => controls.openCellMenu(event));
      region.addEventListener('dblclick', event => {
        if (panels.calculator?.picking) {
          event.preventDefault();
          return;
        }
        if (event.target.closest?.('.grid-cell')) showCellDetail();
      });
      panels.columnController.bind?.(region);
      region.addEventListener('pointerdown', event => this.beginRangeDrag(event));
      document.addEventListener('pointermove', event => this.updateRangeDrag(event));
      document.addEventListener('pointerup', event => this.endRangeDrag(event));
      document.addEventListener('pointercancel', event => this.endRangeDrag(event));
      $('grid').addEventListener('pointermove', event => controls.showTooltip(event));
      $('grid').addEventListener('pointerleave', () => { $('cellTooltip').hidden = true; });
    }

    bindDragDropAndResizeEvents() {
      const {
        $, S, workbookIO, panels,
        syncFreezeScrollGutters, scheduleRangeFrames, updateCanvasSize,
        scheduleInitialPreview, renderGridFromCurrentWindow, paintRangeFrames, scheduleViewport,
      } = this.d;
      const dropZone = $('dropZone');
      for (const type of ['dragenter', 'dragover']) {
        dropZone.addEventListener(type, event => {
          event.preventDefault();
          event.stopPropagation();
          dropZone.classList.add('dragging');
        });
      }
      for (const type of ['dragleave', 'dragend']) {
        dropZone.addEventListener(type, event => {
          event.preventDefault();
          event.stopPropagation();
          dropZone.classList.remove('dragging');
        });
      }
      dropZone.addEventListener('drop', event => {
        event.preventDefault();
        event.stopPropagation();
        dropZone.classList.remove('dragging');
        workbookIO.uploadFile(event.dataTransfer.files?.[0]);
      });
      document.addEventListener('dragover', event => event.preventDefault());
      document.addEventListener('drop', event => {
        if (event.target.closest('#dropZone')) return;
        event.preventDefault();
        const file = event.dataTransfer?.files?.[0];
        if (file) workbookIO.uploadFile(file);
      });

      new ResizeObserver(() => {
        syncFreezeScrollGutters();
        scheduleRangeFrames();
      }).observe($('grid'));

      let gridPixelRatio = window.devicePixelRatio || 1;
      let resizeFrame = 0;
      const resizeWorkspace = () => {
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
        // Ordinary window resizes reuse existing buffered tiles whenever they
        // still cover the visible area; only a DPR change forces a full repaint.
        scheduleViewport(changed);
        panels.frozenPanes.schedule(changed);
      };
      const scheduleResizeWorkspace = () => {
        if (resizeFrame) return;
        resizeFrame = requestAnimationFrame(() => {
          resizeFrame = 0;
          resizeWorkspace();
        });
      };
      const watchPixelRatio = () => {
        if ((window.devicePixelRatio || 1) !== gridPixelRatio) resizeWorkspace();
        window.matchMedia?.(`(resolution: ${gridPixelRatio}dppx)`).addEventListener('change', watchPixelRatio, { once: true });
      };
      watchPixelRatio();
      window.addEventListener('resize', scheduleResizeWorkspace);
    }

    bindEvents() {
      const { controls, clipboard, panels, promptResize, commitResize, autoFitSize, setColumnVisibility, filterSelectedValue, followCellHyperlink, openSearch, tracePrecedents, applyFreeze } = this.d;
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
        openInspector: mode => panels.selectionInspector.open(mode),
        openDataRules: () => panels.dataRulesCenter.open(),
        openRecordCard: () => panels.recordCardCenter?.open(),
        openPrintPreview: () => panels.printingCenter?.open(),
        applyFreeze,
        openExport: mode => panels.exportCenter.open(mode),
        openProfile: () => panels.profileCenter.open(),
        openSelectionQuick: () => panels.selectionQuick?.open(),
        openTranspose: () => panels.transposeCenter?.open(),
        openBookmark: () => panels.bookmarkCenter.open(),
      });
      this.bindDragDropAndResizeEvents();
      // Exactly one keyboard dispatcher. Grid shortcuts bubble here, so a
      // non-modal search panel cannot trigger two actions or open a JS prompt.
      document.addEventListener('keydown', event => this.handleSpreadsheetKeydown(event));
    }
  }

  window.ExcelLensGridInteractions = ExcelLensGridInteractions;
})();
