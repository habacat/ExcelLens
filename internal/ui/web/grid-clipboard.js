(() => {
  'use strict';

  class ExcelLensGridClipboard {
    constructor(deps) {
      this.d = deps;
    }

    async copyText(value, successMessage = '已复制') {
      const { toast } = this.d;
      const text = String(value ?? '');
      try {
        if (navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(text);
        } else {
          const helper = document.createElement('textarea');
          helper.value = text;
          helper.style.position = 'fixed';
          helper.style.opacity = '0';
          document.body.appendChild(helper);
          helper.select();
          try {
            if (!document.execCommand('copy')) {
              throw new Error('浏览器未能写入剪贴板');
            }
          } finally {
            helper.remove();
          }
        }
        toast(successMessage, 'success');
        return true;
      } catch (error) {
        toast(`复制失败：${error.message}`, 'error');
        return false;
      }
    }

    async writeClipboardPayload(content, cut = false) {
      const { toast } = this.d;
      if (navigator.clipboard?.write && typeof ClipboardItem !== 'undefined') {
        try {
          await navigator.clipboard.write([
            new ClipboardItem({
              'text/plain': new Blob([content.text], { type: 'text/plain' }),
              'text/html': new Blob([content.html], { type: 'text/html' }),
            }),
          ]);
          if (!cut) toast('已复制', 'success');
          return true;
        } catch {
          /* Some browsers only allow HTML during a native copy event. */
        }
      }
      let rich = false;
      const onCopy = event => {
        if (!event.clipboardData) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        event.clipboardData.setData('text/plain', content.text);
        event.clipboardData.setData('text/html', content.html);
        rich = true;
      };
      document.addEventListener('copy', onCopy, true);
      try {
        if (document.execCommand('copy') && rich) {
          if (!cut) toast('已复制', 'success');
          return true;
        }
      } catch {
        /* A text-only clipboard can still accept the TSV. */
      } finally {
        document.removeEventListener('copy', onCopy, true);
      }
      if (cut) throw Error('浏览器未写入剪切区域，请重新剪切');
      const copied = await this.copyText(content.text);
      if (copied) toast('已复制为纯文本，当前浏览器未写入表格格式', 'warning');
      return copied;
    }

    clipboardIdentity() {
      const { S, getEditCopy } = this.d;
      const editCopy = getEditCopy?.();
      return JSON.stringify([
        S.book, S.workbookKey, S.sheet, S.sheetEpoch, S.indexReady, S.filtered,
        S.filterId, S.rowView?.id, S.selected?.ref, S.selected?.row, S.selected?.col,
        S.selectedVisualIndex, S.range, S.multiRanges, S.showStyles, S.fontOverride,
        S.freeze, editCopy?.grid.active && editCopy.data.document.revision,
      ]);
    }

    selectionSourceIdentity() {
      const { S } = this.d;
      return JSON.stringify([
        S.book, S.workbookKey, S.sheet, S.sheetEpoch, S.indexReady, S.filtered,
        S.filterId, S.rowView?.id, S.freeze,
      ]);
    }

    captureClipboardSelection({ purpose = '复制', limits = this.d.fileFormats.clipboard, withMerges = false } = {}) {
      const { S, api, colName, mergedCells, sourceRowLimit, getEditCopy, getFrozenPanes } = this.d;
      const editCopy = getEditCopy?.();
      const frozenPanes = getFrozenPanes?.();
      if (!S.indexReady || !S.selected || S.sheetLoadingEpoch) {
        throw Error('请先选择一个已加载的单元格或区域');
      }
      if (editCopy?.grid.active && (editCopy.grid.stale || editCopy.pending || editCopy.abort)) {
        throw Error('请先确认当前编辑修订');
      }
      if (S.multiRanges.length) {
        throw Error(`请选择一个连续区域进行${purpose}`);
      }
      const model = window.ExcelLensMultiSelection;
      const visibility = S.rowVisibility;
      let area = model.areas(S, true).at(-1);
      if (withMerges && !S.filtered) area = mergedCells.expand(area, visibility);
      if (withMerges && S.filtered && !S.range) {
        const m = mergedCells.at(S.selected.row, S.selected.col);
        if (m && m.row === S.selected.row) area = { ...area, col1: m.col, col2: m.endCol };
      }
      const points = model.points(area, colName);
      const col1 = Math.min(area.col1, area.col2);
      const col2 = Math.max(area.col1, area.col2);
      const excluded = frozenPanes.effective().rows;
      const rowCount = S.rowVisibility
        ? S.rowVisibility.selectionCount(area, S.filtered, excluded)
        : model.rowCount(area, S.filtered, excluded);
      const colCount = col2 - col1 + 1;
      if (rowCount < 1) throw Error('选区没有可见行');
      if (limits && (rowCount > limits.rows || colCount > limits.columns || rowCount * colCount > limits.cells)) {
        throw Error(`${purpose}最多${limits.rows}行、${limits.columns}列、${limits.cells}格，请缩小选区`);
      }
      const sheet = S.sheet;
      const identity = this.clipboardIdentity();
      const selected = { ...S.selected };
      const viewId = S.rowView?.id || '';
      const filterId = S.filtered ? S.filterId : '';
      const book = S.book;
      const workbookKey = S.workbookKey;
      const styled = S.showStyles;
      const styles = S.styles;
      const fontOverride = S.fontOverride;
      const current = () => identity === this.clipboardIdentity();
      const sourceIdentity = this.selectionSourceIdentity();
      const sourceCurrent = () => sourceIdentity === this.selectionSourceIdentity();
      const mergeAt = withMerges && mergedCells.root ? (row, col) => mergedCells.copyAnchor(row, col, visibility) : undefined;
      const rangeParams = () => {
        const params = new URLSearchParams({ sheet, start: points.start, end: points.end, book, workbookKey });
        if (viewId) params.set('viewId', viewId);
        if (filterId) {
          params.set('filterId', filterId);
          params.set('visualStart', String(points.visualStart));
          params.set('visualEnd', String(points.visualEnd));
        }
        if (viewId || filterId) params.set('excludeThroughRow', String(excluded));
        return params;
      };
      return {
        book,
        workbookKey,
        sheet,
        styled,
        styles,
        fontOverride,
        current,
        sourceCurrent,
        mergeAt,
        rows: rowCount,
        firstColumn: col1,
        lastColumn: col2,
        ...points,
        maxRow: sourceRowLimit(),
        headerRow: S.autoFilter?.row || S.sortHeaderBySheet.get(sheet) || S.source?.tableHeader?.row || 1,
        label: `${sheet} · ${points.start}:${points.end} · ${rowCount}行 × ${colCount}列`,
        readTSV: () => {
          const params = rangeParams();
          params.set('format', 'tsv');
          return api(`/api/range?${params}`, {}, 'text');
        },
        read: async ({ headerRow = 0, richText = true, conditional = styled, force = false, portable = false } = {}, signal) => {
          if (!current()) throw Error('来源或选区已变化，请重新选择');
          // A lone cell already holds the current edit projection.
          if (rowCount === 1 && colCount === 1 && !headerRow && !force && !portable) {
            const cell = { ...selected };
            if (!richText) delete cell.richText;
            if (!conditional) delete cell.conditional;
            return { book, workbookKey, sheet, startCol: col1, endCol: col2, rows: [{ row: selected.row, cells: [cell] }] };
          }
          const params = rangeParams();
          params.set('format', 'json');
          params.set('richText', richText ? '1' : '0');
          params.set('conditional', conditional ? '1' : '0');
          if (portable) params.set('portable', '1');
          if (headerRow) params.set('headerRow', String(headerRow));
          const payload = await api(`/api/range?${params}`, { signal });
          if (!current()) throw Object.assign(Error('复制已取消，来源或选区已变化'), { name: 'AbortError' });
          const activeEdit = getEditCopy?.();
          if (activeEdit?.grid.active) {
            for (const row of payload.rows) row.cells = activeEdit.grid.projectRow(sheet, row, col1, col2);
            if (payload.header) payload.header.cells = activeEdit.grid.projectRow(sheet, payload.header, col1, col2);
          }
          return payload;
        },
      };
    }

    async copySelection(plain = false, cut = false) {
      const { fileFormats, toast, getEditCopy, getExportCenter } = this.d;
      const activity = globalThis.ExcelLensActivity?.begin(cut ? '正在剪切选区' : '正在复制选区');
      const editCopy = getEditCopy?.();
      const ranges = editCopy?.grid.active ? editCopy.grid.ranges : null;
      const epoch = ranges ? (ranges.copyEpoch = (ranges.copyEpoch || 0) + 1) : 0;
      try {
        const progressive = !ranges && !cut;
        const snapshot = this.captureClipboardSelection({ withMerges: !plain, limits: progressive ? null : fileFormats.clipboard });
        if (!progressive) return await this.copyGridPayload(snapshot, plain, cut, ranges, epoch);
        return await window.ExcelLensClipboard.copyByBudget(
          snapshot,
          fileFormats.clipboard,
          plain,
          () => this.copyGridPayload(snapshot, false, false, null, 0),
          async () => {
            const text = await snapshot.readTSV();
            return snapshot.current() && this.copyText(text, '已复制为纯文本');
          },
          () => {
            const exportCenter = getExportCenter?.();
            if (exportCenter.running || exportCenter.busy) {
              toast('导出中心正在处理任务，请在完成后导出选区', 'warning');
              return;
            }
            exportCenter.open('selection', snapshot);
            toast('选区超过复制容量，可导出为文件', 'warning');
          }
        );
      } catch (err) {
        if (err.name !== 'AbortError') toast(err.message, 'error');
      } finally {
        activity?.end();
      }
    }

    async copyGridPayload(snapshot, plain, cut, ranges, epoch) {
      const { fileFormats, toast, getEditCopy } = this.d;
      const editCopy = getEditCopy?.();
      const [payload, initial] = await Promise.all([
        snapshot.read({ richText: !plain && snapshot.styled, conditional: !plain && snapshot.styled, portable: !plain && !ranges }),
        !plain && editCopy?.grid.active && !ranges.view.mappedRows() ? editCopy.grid.ranges.copySnapshot(snapshot, cut) : null,
      ]);
      const typed = !plain && ranges && ranges.view.mappedRows() && snapshot.current()
        ? await ranges.copySnapshot(snapshot, cut, payload.rows.map(r => r.row))
        : initial;
      if (!snapshot.current() || (ranges && ranges.copyEpoch !== epoch)) return;
      if (cut && !typed) throw Error('剪切需要一个连续、完整可见且未超出编辑容量的工作表区域');
      const content = window.ExcelLensClipboard.build(
        payload,
        snapshot.styles,
        snapshot.styled && !plain,
        snapshot.fontOverride,
        fileFormats.richText,
        fileFormats.clipboard,
        snapshot.mergeAt
      );
      if (typed) {
        typed.text = content.text.replaceAll('\r\n', '\n');
        content.html = content.html.replace('<table ', '<table id="' + typed.marker + '"' + window.ExcelLensGridPaste.markup(typed) + ' ');
      }
      if (!typed && payload.portable) {
        content.html = content.html.replace('<table ', '<table' + window.ExcelLensGridPaste.markup({ clip: { portable: payload.portable } }) + ' ');
      }
      if ((typed || payload.portable) && window.ExcelLensClipboard.utf8Length(content.html) > fileFormats.clipboard.htmlBytes) {
        throw Object.assign(Error('复制内容超过本次容量'), { status: 413 });
      }
      if (payload.portableNotice) content.notices.push(payload.portableNotice);
      const copied = plain ? await this.copyText(content.text) : await this.writeClipboardPayload(content, cut);
      if (cut && !snapshot.current()) return false;
      if (copied && snapshot.current() && editCopy?.grid.active && ranges) {
        if (ranges.copyEpoch !== epoch) {
          if (cut && typed && !ranges.copied) ranges.copied = { ...typed, inactive: true };
          return false;
        }
        const repaintCut = typed?.cut || (ranges.copied?.cut && !ranges.copied.inactive);
        ranges.copied = typed;
        if (repaintCut) editCopy.grid.view.redraw();
      }
      if (!plain && content.notices.length) toast(content.notices.join('；'), 'warning');
      return copied;
    }

    copyCellEvidence() {
      const { S, toast } = this.d;
      const c = S.selected;
      if (!c) {
        toast('请先选中一个单元格', 'warning');
        return;
      }
      const text = [
        `工作簿：${S.fileName}`,
        `工作表：${S.sheet}`,
        `单元格：${c.ref}`,
        `显示值：${c.display ?? ''}`,
        `缓存原始值：${c.raw ?? ''}`,
        `公式：${c.formula ?? '（非公式单元格）'}`,
        `标识：${c.cacheMissing ? '公式缺少缓存值' : '只读读取原文件保存的值（未重算）'}`,
      ].join('\n');
      return this.copyText(text);
    }

    copyQualifiedAddress() {
      const { S, toast } = this.d;
      if (!S.selected) return toast('请先选择单元格', 'warning');
      const sheet = "'" + String(S.sheet).replaceAll("'", "''") + "'";
      return this.copyText(`${sheet}!${S.selected.ref}`);
    }
  }

  window.ExcelLensGridClipboard = ExcelLensGridClipboard;
})();
