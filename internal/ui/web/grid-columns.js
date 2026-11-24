/* Column-header span selection, drag selection and serialized column visibility overrides. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id);

  class ColumnController {
    constructor({state, api, toast, geometry, baseRow, baseCol, colName, parseColumn, relayoutGrid, selectCellByAddress, hideCellMenu, selectionStats, calculator}) {
      Object.assign(this, {s: state, api, toast, geometry, baseRow, baseCol, colName, parseColumn, relayoutGrid, selectCellByAddress, hideCellMenu, selectionStats, calculator});
      this.dragAnchor = 0;
      this.dragEnd = 0;
      this.dragFrame = 0;
      this.visibilitySaveQueue = Promise.resolve();
      this.visibilityRevision = 0;
    }

    // A contiguous column-header selection includes hidden columns between the
    // visible endpoints (e.g. dragging A to C selects A:B:C when B is hidden).
    // The selection is UI state, never a mutation of the original workbook.
    selectedSpan() {
      const p = this.s.columnSelection;
      if (!p) return null;
      return {start: Math.min(p.start, p.end), end: Math.max(p.start, p.end)};
    }

    spanLabel(span) {
      if (!span) return '';
      return span.start === span.end ? this.colName(span.start) : `${this.colName(span.start)}:${this.colName(span.end)}`;
    }

    parseSpan(value) {
      const parts = String(value || '').replaceAll('$', '').trim().split(/\s*:\s*/);
      if (parts.length < 1 || parts.length > 2) return null;
      const first = this.parseColumn(parts[0]), last = this.parseColumn(parts.at(-1));
      return first && last ? {start: Math.min(first, last), end: Math.max(first, last)} : null;
    }

    hasOfType(span, hidden) {
      if (!span) return false;
      for (let col = span.start; col <= span.end; col++) if (this.s.hiddenColumns.has(col) === hidden) return true;
      return false;
    }

    paintSelection() {
      const span = this.selectedSpan();
      for (const head of document.querySelectorAll('.grid-region .column-head[data-col]')) {
        const col = Number(head.dataset.col);
        head.classList.toggle('column-selected', Boolean(span && col >= span.start && col <= span.end));
      }
      // Only virtualized cells are touched; no full-sheet DOM materialization.
      for (const row of this.s.renderedRows.values()) {
        for (const [col, cell] of row._xlCells || []) {
          cell.classList.toggle('column-range-cell', Boolean(span && col >= span.start && col <= span.end));
        }
      }
      for (const cell of document.querySelectorAll('.grid-region .pinned-cell[data-col]')) {
        const col = Number(cell.dataset.col);
        cell.classList.toggle('column-range-cell', Boolean(span && col >= span.start && col <= span.end));
      }
    }

    selectSpan(anchor, end) {
      if (!this.s.indexReady || !Number.isInteger(anchor) || !Number.isInteger(end) || anchor < 1 || end < 1 || anchor > 16384 || end > 16384) return;
      this.s.columnSelection = {start: anchor, end};
      this.s.columnAnchor = anchor;
      this.s.range = null; this.s.multiRanges = []; this.s.rangeAnchor = null; this.s.wholeRowSelected = false;
      const span = this.selectedSpan();
      $('selectionStatus').textContent = `${this.spanLabel(span)} · ${span.end - span.start + 1} 列`;
      this.selectionStats()?.clear();
      this.paintSelection();
    }

    beginDrag(event) {
      if (event.button !== 0 || !this.s.indexReady || event.target.closest?.('.column-resize-grip,.column-filter,.auto-filter-button')) return;
      const head = event.target.closest?.('.column-head[data-col]');
      if (!head) return;
      const col = Number(head.dataset.col);
      if (!col) return;
      this.hideCellMenu(); this.hideMenu();
      const anchor = event.shiftKey ? (this.s.columnAnchor || col) : col;
      this.dragAnchor = anchor; this.dragEnd = col;
      this.selectSpan(anchor, col);
      event.preventDefault();
    }

    updateDrag(event) {
      if (!this.dragAnchor || !(event.buttons & 1)) return;
      const el = document.elementFromPoint(event.clientX, event.clientY);
      const head = el?.closest?.('.column-head[data-col]');
      if (!head) return;
      const col = Number(head.dataset.col);
      if (!col || col === this.dragEnd) return;
      this.dragEnd = col;
      if (this.dragFrame) return;
      this.dragFrame = requestAnimationFrame(() => {
        this.dragFrame = 0;
        if (this.dragAnchor) this.selectSpan(this.dragAnchor, this.dragEnd);
      });
    }

    captureSelection() {
      const span = this.selectedSpan();
      const calc = this.calculator();
      if (span && calc?.picking) calc.captureRange(`${this.colName(span.start)}:${this.colName(span.end)}`);
    }

    endDrag(event) {
      if (this.dragFrame) { cancelAnimationFrame(this.dragFrame); this.dragFrame = 0; }
      if (this.dragAnchor) {
        this.selectSpan(this.dragAnchor, this.dragEnd);
        if (event?.type !== 'pointercancel') this.captureSelection();
      }
      this.dragAnchor = this.dragEnd = 0;
    }

    // The original <cols hidden=...> metadata and user overrides are two
    // different concerns. Neither writes to the XLSX, and query/search/export
    // are independent of visibility. A zero-size override removes a column
    // from geometry rather than merely making its text transparent.
    applyVisibility() {
      const hidden = new Set();
      if (this.s.columnMode === 'hide-all') {
        for (let c = 1; c <= 16384; c++) hidden.add(c);
      } else if (this.s.columnMode !== 'show-all') {
        for (const range of this.s.columnSourceRanges) {
          const min = Math.max(1, Number(range.min) || 1), max = Math.min(16384, Number(range.max) || 0);
          for (let c = min; c <= max; c++) hidden.add(c);
        }
      }
      for (const [raw, flag] of Object.entries(this.s.columnOverrides)) {
        const c = Number(raw);
        if (!Number.isInteger(c) || c < 1 || c > 16384) continue;
        if (flag) hidden.add(c); else hidden.delete(c);
      }
      this.s.hiddenColumns = hidden;
      const effective = {...this.s.sourceColumnWidths, ...this.s.columnWidths};
      for (const col of hidden) effective[String(col)] = 0;
      this.geometry.rows.setBase(this.s.sourceDefaultRow || this.baseRow);
      this.geometry.cols.setBase(this.s.sourceDefaultCol || this.baseCol);
      this.geometry.rows.load(this.s.rowSizes, this.s.sourceRowSizes);
      this.geometry.cols.load(effective);
    }

    nextVisible(from) {
      for (let c = Math.max(1, Math.trunc(from) || 1); c <= Math.max(1, this.s.maxCol); c++) {
        if (!this.s.hiddenColumns.has(c)) return c;
      }
      return Math.min(16384, Math.max(1, Math.trunc(from) || 1));
    }

    previousVisible(from) {
      for (let c = Math.min(this.s.maxCol, Math.trunc(from) || 1); c >= 1; c--) {
        if (!this.s.hiddenColumns.has(c)) return c;
      }
      return 0;
    }

    hideMenu() {
      const menus = window.ExcelLensOverlays?.menus;
      if (menus?.current?.menu === $('columnsMenu')) menus.close(false);
    }

    refreshMenuTarget() {
      const span = this.parseSpan($('columnTarget').value);
      $('hideSelectedColumnBtn').disabled = !this.hasOfType(span, false);
      $('showSelectedColumnBtn').disabled = !this.hasOfType(span, true);
      const multiple = span && span.start !== span.end;
      $('hideSelectedColumnBtn').textContent = multiple ? '隐藏选中列' : '隐藏此列';
      $('showSelectedColumnBtn').textContent = '显示隐藏列';
    }

    targetSpan() {
      const span = this.parseSpan($('columnTarget').value);
      if (!span) this.toast('请输入有效的列或区域，如 A、AB、A:C', 'warning');
      return span;
    }

    prepareMenu(event) {
      if (!this.s.indexReady) { event.preventDefault(); return; }
      const selectedCol = this.s.selected?.col || this.nextVisible(1);
      const label = this.spanLabel(this.selectedSpan()) || this.colName(selectedCol);
      $('columnMenuSelection').textContent = '选择列';
      $('columnTarget').value = label;
      this.refreshMenuTarget();
    }

    // Serialize visibility writes: fast consecutive clicks must persist in the
    // same order as the optimistic UI state. Stale failures may not roll back
    // newer successful user edits.
    async setVisibility(mode, index, hidden, end = index) {
      if (!this.s.indexReady || !this.s.sheet) return;
      const sheet = this.s.sheet, epoch = this.s.sheetEpoch;
      const previousMode = this.s.columnMode, previousOverrides = {...this.s.columnOverrides};
      if (mode === 'column' || mode === 'range') {
        const first = Math.min(index, end), last = Math.max(index, end);
        if (!Number.isInteger(first) || !Number.isInteger(last) || first < 1 || last > 16384) return;
        for (let col = first; col <= last; col++) this.s.columnOverrides[String(col)] = Boolean(hidden);
        index = first; end = last;
      } else { this.s.columnMode = mode; this.s.columnOverrides = {}; }
      this.applyVisibility(); this.hideMenu();
      if (this.s.selected && this.s.hiddenColumns.has(this.s.selected.col)) {
        let next = this.nextVisible(this.s.selected.col + 1);
        if (this.s.hiddenColumns.has(next)) next = this.previousVisible(this.s.selected.col - 1) || this.s.selected.col;
        if (!this.s.hiddenColumns.has(next)) this.selectCellByAddress(`${this.colName(next)}${this.s.selected.row}`, this.s.selectedVisualIndex);
      }
      this.relayoutGrid();
      const revision = ++this.visibilityRevision;
      const save = () => this.api('/api/layout', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({sheet, kind: 'visibility', mode, index: index || 0, end: end || 0, hidden: Boolean(hidden)})
      });
      const pending = this.visibilitySaveQueue.then(save, save);
      this.visibilitySaveQueue = pending.catch(() => {});
      try {
        await pending;
      } catch (error) {
        if (this.s.sheet === sheet && this.s.sheetEpoch === epoch && this.visibilityRevision === revision) {
          this.s.columnMode = previousMode; this.s.columnOverrides = previousOverrides;
          this.applyVisibility(); this.relayoutGrid();
        }
        this.toast('保存列显隐失败：' + error.message, 'error');
      }
    }

    bind(region) {
      $('columnsMenu').addEventListener('menuopen', event => this.prepareMenu(event));
      $('columnTarget').addEventListener('input', () => this.refreshMenuTarget());
      $('hideSelectedColumnBtn').addEventListener('click', () => {
        const span = this.targetSpan();
        if (span) this.setVisibility(span.start === span.end ? 'column' : 'range', span.start, true, span.end);
      });
      $('showSelectedColumnBtn').addEventListener('click', () => {
        const span = this.targetSpan();
        if (span) this.setVisibility(span.start === span.end ? 'column' : 'range', span.start, false, span.end);
      });
      $('showAllColumnsBtn').addEventListener('click', () => this.setVisibility('show-all'));
      $('hideAllColumnsBtn').addEventListener('click', () => this.setVisibility('hide-all'));
      $('restoreColumnsBtn').addEventListener('click', () => this.setVisibility('original'));
      region.addEventListener('pointerdown', event => this.beginDrag(event), true);
      document.addEventListener('pointermove', event => this.updateDrag(event), true);
      document.addEventListener('pointerup', event => this.endDrag(event), true);
      document.addEventListener('pointercancel', event => this.endDrag(event), true);
    }
  }

  window.ExcelLensColumnController = ColumnController;
})();
