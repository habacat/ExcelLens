(() => {
  'use strict';

  class ExcelLensAnalyticsSheets {
    constructor(deps) {
      this.d = deps;
      this.saveChain = Promise.resolve();
      this.pendingWrites = 0;
      this.pasteSpecial = null;
    }

    setPasteSpecial(pasteSpecial) {
      this.pasteSpecial = pasteSpecial;
    }

    flush() {
      return this.saveChain.catch(() => {});
    }

    hasUnsaved() {
      const { state, drafts } = this.d;
      return this.pendingWrites > 0 || drafts.entries(state.currentBook ?? state.fingerprint).length > 0;
    }

    updateSaveStatus() {
      const { $, state, drafts, setText } = this.d;
      const count = drafts.entries(state.fingerprint).length;
      setText('analysisAutoSave', this.pendingWrites ? '保存中…' : count ? `${count}格未保存` : '已保存');
      $('analysisRetrySave').hidden = !count || this.pendingWrites > 0;
      $('analysisRetrySave').disabled = this.pendingWrites > 0;
    }

    paintDraft(input, row, col, saved) {
      const { state, drafts } = this.d;
      const draft = drafts.get(state.fingerprint, state.loadedSheet, row, col);
      input.classList.toggle('is-unsaved', !!draft);
      input.setAttribute('aria-invalid', draft ? 'true' : 'false');
      input.title = draft
        ? '未保存草稿；当前已保存值：' + String(saved ?? '')
        : (input.dataset.savedTitle || '');
      return draft;
    }

    queueWrite(run) {
      const { drafts, toast } = this.d;
      ++this.pendingWrites;
      this.updateSaveStatus();
      this.saveChain = this.saveChain
        .catch(() => {})
        .then(async () => {
          try {
            return await run();
          } catch (err) {
            toast('保存失败：' + err.message + (drafts.size ? '；草稿已保留' : ''), 'error');
            throw err;
          } finally {
            --this.pendingWrites;
            this.updateSaveStatus();
          }
        });
      return this.saveChain;
    }

    async createSheet() {
      const { state, post, askText, refreshWorkspace, setTab, toast } = this.d;
      const name = await askText('新建可编辑工作表', '数据分析表 ' + ((state.workspace?.sheets?.length || 0) + 1));
      if (!name) return;
      try {
        const data = await post('/api/analysis/sheet/create', { name });
        state.loadedSheet = data.id;
        state.sheetPage = 0;
        await refreshWorkspace();
        setTab('sheets');
        toast('已新建工作表，所有改动独立保存', 'success');
      } catch (err) {
        toast(err.message, 'error');
      }
    }

    async renderSheets() {
      const { $, state, setText, setTab } = this.d;
      if (!state.opened || state.tab !== 'sheets') return;
      const host = $('analysisSheetList');
      host.replaceChildren();
      const sheets = state.workspace?.sheets || [];
      for (const sheet of sheets) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = sheet.name;
        btn.title = sheet.name;
        btn.className = sheet.id === state.loadedSheet ? 'is-active' : '';
        btn.onclick = () => {
          state.loadedSheet = sheet.id;
          state.sheetPage = 0;
          this.renderSheets();
        };
        host.append(btn);
      }
      if (!sheets.length) {
        state.sheetLoadSeq++;
        state.sheetAbortController?.abort();
        state.sheetAbortController = null;
        state.loadedSheet = null;
        state.gridData = null;
        setText('analysisSheetTitle', '编辑工作表');
        for (const id of [
          'analysisPasteSpecial',
          'analysisUndo',
          'analysisRedo',
          'analysisAddColumn',
          'analysisAddRows',
          'analysisDeleteSheet',
        ]) {
          $(id).disabled = true;
        }
        $('analysisSheetPagination').replaceChildren();
        const empty = document.createElement('div');
        empty.className = 'analysis-sheet-empty';
        const glyph = document.createElement('div');
        glyph.className = 'analysis-sheet-empty-icon';
        glyph.setAttribute('aria-hidden', 'true');
        glyph.innerHTML = window.ExcelLensDisplayFormat.icon('header');
        const title = document.createElement('h3');
        title.textContent = '暂无工作表';
        const actions = document.createElement('div');
        actions.className = 'analysis-sheet-empty-actions';
        const create = document.createElement('button');
        create.type = 'button';
        create.className = 'analysis-primary-button';
        create.innerHTML = window.ExcelLensDisplayFormat.icon('plus');
        create.append('新建工作表');
        create.addEventListener('click', () => this.createSheet());
        const pivot = document.createElement('button');
        pivot.type = 'button';
        pivot.className = 'analysis-outline-button';
        pivot.textContent = '转到透视分析';
        pivot.addEventListener('click', () => setTab('pivot'));
        actions.append(create, pivot);
        empty.append(glyph, title, actions);
        $('analysisEditableGrid').replaceChildren(empty);
        const hint = document.createElement('div');
        hint.className = 'analysis-sheet-list-hint';
        hint.textContent = '尚无编辑表';
        host.append(hint);
        return;
      }
      if (!sheets.some(s => s.id === state.loadedSheet)) {
        state.loadedSheet = sheets[0].id;
      }
      host.querySelectorAll('button').forEach((btn, i) => btn.classList.toggle('is-active', sheets[i].id === state.loadedSheet));
      await this.loadSheet(state.loadedSheet, state.sheetPage);
    }

    getSheetHistory(id) {
      const { state } = this.d;
      if (!state.undoHistory.has(id)) {
        state.undoHistory.set(id, { undo: [], redo: [] });
      }
      return state.undoHistory.get(id);
    }

    updateSheetHistoryButtons() {
      const { $, state } = this.d;
      const hist = state.loadedSheet ? this.getSheetHistory(state.loadedSheet) : { undo: [], redo: [] };
      $('analysisUndo').disabled = !hist.undo.length;
      $('analysisRedo').disabled = !hist.redo.length;
    }

    async commitBatch(id, changes, { track = true, refresh = false, guard } = {}) {
      const { $, state, drafts, post } = this.d;
      if (!id || !changes?.length) return;
      if (guard && !guard()) {
        throw Object.assign(Error('粘贴目标或版本已变化，请重新读取目标'), { status: 409 });
      }
      const book = state.fingerprint;
      const staged = drafts.stage(book, id, changes);
      const result = await this.queueWrite(async () => {
        if (book !== state.fingerprint) throw Error('工作簿已切换');
        if (guard && !guard()) {
          throw Object.assign(Error('粘贴目标或版本已变化，整批未写入；输入已保留'), { status: 409 });
        }
        window.ExcelLensClipboard.validateChanges(changes);
        // Resolve expected version INSIDE the serialized write queue.
        const expectedRevision = state.sheetRevisions.get(id) ?? 0;
        const saved = await post('/api/analysis/sheet/batch', { id, workbookKey: book, expectedRevision, changes });
        if (book === state.fingerprint) {
          state.sheetRevisions.set(id, saved.revision);
          state.reportCache.clear();
        }
        return saved;
      });
      drafts.acknowledge(staged);
      this.updateSaveStatus();
      if (book !== state.fingerprint) return result;
      if (track) {
        const hist = this.getSheetHistory(id);
        hist.undo.push({ before: result.previous, after: changes.map(c => ({ ...c })) });
        if (hist.undo.length > 100) hist.undo.shift();
        hist.redo = [];
      }
      this.updateSheetHistoryButtons();
      if (state.loadedSheet === id) {
        for (const input of $('analysisEditableGrid').querySelectorAll('textarea[data-row][data-col]')) {
          const row = Number(input.dataset.row);
          const col = Number(input.dataset.col);
          this.paintDraft(input, row, col, this.editGridCellValue(row, col));
        }
      }
      if (refresh && state.loadedSheet === id) {
        await this.loadSheet(id, state.sheetPage);
      }
      return result;
    }

    async retryDrafts() {
      const { state, drafts, setTab, toast } = this.d;
      if (this.pendingWrites) return;
      const book = state.fingerprint;
      const groups = new Map();
      for (const entry of drafts.entries(book)) {
        if (!groups.has(entry.id)) groups.set(entry.id, []);
        groups.get(entry.id).push(entry);
      }
      for (const [id, entries] of groups) {
        try {
          for (const batch of window.ExcelLensClipboard.batches(entries)) {
            if (book !== state.fingerprint) return;
            const active = batch.filter(entry => drafts.get(book, id, entry.row, entry.col) === entry);
            if (!active.length) continue;
            try {
              await this.commitBatch(
                id,
                active.map(({ row, col, value }) => ({ row, col, value })),
                { refresh: state.loadedSheet === id }
              );
            } catch (err) {
              if (err.status === 409) {
                state.undoHistory.delete(id);
                state.loadedSheet = id;
                state.sheetPage = Math.floor(active[0].row / this.sheetPageSize(id));
                setTab('sheets');
                await this.loadSheet(id, state.sheetPage);
                toast('版本冲突，草稿已保留；悬停可核对已保存值，再点击重试保存', 'warning');
              }
              return;
            }
          }
        } catch (err) {
          toast(err.message, 'warning');
          return;
        }
      }
      if (book === state.fingerprint && !drafts.entries(book).length) {
        toast('草稿已保存', 'success');
      }
    }

    async changeSheetHistory(direction) {
      const { state, toast } = this.d;
      const id = state.loadedSheet;
      if (!id) return;
      const hist = this.getSheetHistory(id);
      const from = direction === 'undo' ? hist.undo : hist.redo;
      if (!from.length) return;
      const item = from.pop();
      this.updateSheetHistoryButtons();
      try {
        await this.commitBatch(id, direction === 'undo' ? item.before : item.after, { track: false, refresh: false });
        (direction === 'undo' ? hist.redo : hist.undo).push(item);
        this.updateSheetHistoryButtons();
        await this.loadSheet(id, state.sheetPage);
      } catch (err) {
        from.push(item);
        this.updateSheetHistoryButtons();
        if (err.status === 409) {
          hist.undo = [];
          hist.redo = [];
          await this.loadSheet(id, state.sheetPage);
          toast('编辑版本已变化，操作未执行，已重新载入', 'warning');
        } else {
          toast(err.message, 'error');
        }
      }
    }

    async loadSheet(id, page) {
      const { $, state, api, post, setText, fmtNum, refreshWorkspace, toast } = this.d;
      if (!id) return;
      $('analysisPasteSpecial').disabled = true;
      const seq = ++state.sheetLoadSeq;
      state.sheetAbortController?.abort();
      const controller = new AbortController();
      state.sheetAbortController = controller;
      try {
        // Reuse the fetch page size across loads; cap DOM nodes for wide grids.
        const pageSize = this.sheetPageSize(id);
        const data = await api(
          '/api/analysis/sheet?' + new URLSearchParams({ id, offset: String(page * pageSize), limit: String(pageSize) }),
          { signal: controller.signal }
        );
        if (seq !== state.sheetLoadSeq || state.loadedSheet !== id || state.sheetPage !== page) return;
        state.sheetRevisions.set(id, data.revision ?? 0);
        state.gridData = data;
        $('analysisPasteSpecial').disabled = false;
        setText('analysisSheetTitle', data.name);
        $('analysisAddColumn').disabled = false;
        $('analysisAddRows').disabled = false;
        $('analysisDeleteSheet').disabled = false;
        this.updateSheetHistoryButtons();

        const area = $('analysisEditableGrid');
        area.replaceChildren();
        const table = document.createElement('table');
        const head = document.createElement('thead');
        const tr = document.createElement('tr');
        const zero = document.createElement('th');
        zero.textContent = '';
        tr.append(zero);
        data.columns.forEach((name, j) => {
          const th = document.createElement('th');
          const input = document.createElement('input');
          input.value = name;
          input.title = '编辑列名，失焦后保存';
          input.addEventListener('change', () => {
            const cols = [...data.columns];
            cols[j] = input.value.trim();
            if (!cols[j]) {
              input.value = name;
              return;
            }
            this.queueWrite(async () => {
              const rev = state.sheetRevisions.get(id) ?? 0;
              const result = await post('/api/analysis/sheet/columns', { id, columns: cols, expectedRevision: rev });
              state.sheetRevisions.set(id, rev + 1);
              state.reportCache.clear();
              return result;
            })
              .then(() => {
                data.columns = cols;
                state.undoHistory.delete(id);
                this.updateSheetHistoryButtons();
                refreshWorkspace();
              })
              .catch(async () => {
                input.value = name;
                await this.loadSheet(id, page);
              });
          });
          th.append(input);
          tr.append(th);
        });
        head.append(tr);
        table.append(head);

        const body = document.createElement('tbody');
        const rows = Math.min(Math.max(data.rows?.length || 0, Math.min(25, pageSize)), Math.min(pageSize, 10000 - data.offset));
        const fragment = document.createDocumentFragment();
        for (let i = 0; i < rows; i++) {
          const row = document.createElement('tr');
          const n = document.createElement('td');
          n.textContent = String(data.offset + i + 1);
          row.append(n);
          for (let j = 0; j < data.columns.length; j++) {
            const td = document.createElement('td');
            const input = document.createElement('textarea');
            input.rows = 1;
            const saved = data.rows?.[i]?.[j] ?? '';
            input.value = saved;
            input.dataset.row = String(data.offset + i);
            input.dataset.col = String(j);
            input.title = `${data.columns[j]} · 第 ${data.offset + i + 1} 行`;
            input.dataset.savedTitle = input.title;
            const draft = this.paintDraft(input, data.offset + i, j, saved);
            if (draft) input.value = draft.value;
            td.append(input);
            row.append(td);
          }
          fragment.append(row);
        }
        body.append(fragment);
        table.append(body);
        area.append(table);

        const pagination = $('analysisSheetPagination');
        pagination.replaceChildren();
        const prev = document.createElement('button');
        prev.type = 'button';
        prev.textContent = '‹ 上一页';
        prev.disabled = page === 0;
        prev.onclick = async () => {
          await this.flush();
          state.sheetPage = page - 1;
          this.loadSheet(id, state.sheetPage);
        };
        pagination.append(prev);
        const span = document.createElement('span');
        span.textContent = `第 ${page + 1} 页 · 已保存 ${fmtNum(data.total)} 行 · 每页 ${pageSize} 行`;
        pagination.append(span);
        const next = document.createElement('button');
        next.type = 'button';
        next.textContent = '下一页 ›';
        const lastAllowed = Math.min(9999, Math.max(data.total, 1) + pageSize - 1);
        next.disabled = (page + 1) * pageSize > lastAllowed;
        next.onclick = async () => {
          await this.flush();
          state.sheetPage = page + 1;
          this.loadSheet(id, state.sheetPage);
        };
        pagination.append(next);
      } catch (e) {
        if (e.name !== 'AbortError' && seq === state.sheetLoadSeq) {
          toast(e.message, 'error');
        }
      } finally {
        if (state.sheetAbortController === controller) {
          state.sheetAbortController = null;
        }
      }
    }

    editGridCellValue(row, col) {
      const data = this.d.state.gridData;
      if (!data) return '';
      const index = row - data.offset;
      return data.rows?.[index]?.[col] ?? '';
    }

    sheetPageSize(id) {
      const info = this.d.state.workspace?.sheets?.find(x => x.id === id);
      return Math.max(30, Math.min(80, Math.floor(2600 / Math.max(1, info?.columns?.length || 10))));
    }

    editGridWriteLocal(row, col, value) {
      const data = this.d.state.gridData;
      if (!data || row < data.offset) return;
      const index = row - data.offset;
      if (index >= 300) return;
      if (!data.rows) data.rows = [];
      while (data.rows.length <= index) data.rows.push([]);
      while (data.rows[index].length <= col) data.rows[index].push('');
      data.rows[index][col] = value;
    }

    bindSheetGrid() {
      const { $, state, drafts, toast } = this.d;
      const area = $('analysisEditableGrid');
      area.addEventListener('focusin', event => {
        const input = event.target.closest('textarea[data-row][data-col]');
        if (input) {
          state.sheetCell = {
            id: state.loadedSheet,
            page: state.sheetPage,
            row: Number(input.dataset.row),
            col: Number(input.dataset.col),
          };
        }
      });
      area.addEventListener('input', event => {
        const input = event.target.closest('textarea[data-row][data-col]');
        if (!input || !state.loadedSheet) return;
        const row = Number(input.dataset.row);
        const col = Number(input.dataset.col);
        const saved = this.editGridCellValue(row, col);
        const staged = drafts.stage(state.fingerprint, state.loadedSheet, [{ row, col, value: input.value }]);
        if (input.value === saved && !this.pendingWrites) {
          drafts.acknowledge(staged);
        }
        this.paintDraft(input, row, col, saved);
        this.updateSaveStatus();
      });
      area.addEventListener('change', event => {
        const input = event.target.closest('textarea[data-row][data-col]');
        if (!input || !state.loadedSheet) return;
        const row = Number(input.dataset.row);
        const col = Number(input.dataset.col);
        const previous = this.editGridCellValue(row, col);
        if (previous === input.value && !this.pendingWrites) {
          const draft = drafts.get(state.fingerprint, state.loadedSheet, row, col);
          if (draft) drafts.acknowledge([draft]);
          this.updateSaveStatus();
          return;
        }
        const id = state.loadedSheet;
        const next = input.value;
        this.commitBatch(id, [{ row, col, value: next }])
          .then(() => {
            if (state.loadedSheet === id) this.editGridWriteLocal(row, col, next);
          })
          .catch(async err => {
            if (err.status === 409 && state.loadedSheet === id) {
              state.undoHistory.delete(id);
              await this.loadSheet(id, state.sheetPage);
              toast('版本冲突，草稿已保留；请核对已保存值后重试', 'warning');
            }
          });
      });
      area.addEventListener('paste', event => {
        const input = event.target.closest('textarea[data-row][data-col]');
        if (!input || !state.loadedSheet) return;
        const text = event.clipboardData?.getData('text/plain') || '';
        if (!text.includes('\t') && !/[\r\n]/.test(text)) {
          const value = input.value.slice(0, input.selectionStart) + text + input.value.slice(input.selectionEnd);
          try {
            window.ExcelLensClipboard.validateChanges([{ row: Number(input.dataset.row), col: Number(input.dataset.col), value }]);
          } catch (err) {
            event.preventDefault();
            toast(err.message, 'warning');
          }
          return;
        }
        event.preventDefault();
        const startRow = Number(input.dataset.row);
        const startCol = Number(input.dataset.col);
        const cols = state.gridData?.columns?.length || 0;
        let changes;
        try {
          changes = window.ExcelLensClipboard.pasteChanges(text, { startRow, startCol, rowLimit: 10000, columns: cols });
        } catch (err) {
          toast(err.message, 'warning');
          return;
        }
        const id = state.loadedSheet;
        this.commitBatch(id, changes, { refresh: true }).catch(async err => {
          if (err.status === 409 && state.loadedSheet === id) {
            state.undoHistory.delete(id);
            await this.loadSheet(id, state.sheetPage);
            toast('版本冲突，粘贴草稿已保留；请核对后重试保存', 'warning');
          } else {
            toast('粘贴未保存：' + err.message, 'error');
          }
        });
      });
      area.addEventListener('keydown', event => {
        const input = event.target.closest('textarea[data-row][data-col]');
        if (!input || event.isComposing) return;
        const mod = event.ctrlKey || event.metaKey;
        if (mod && event.shiftKey && !event.altKey && event.key.toLowerCase() === 'v') {
          event.preventDefault();
          event.stopPropagation();
          this.openPasteSpecial();
          return;
        }
        if (mod && !event.altKey && ['z', 'y'].includes(event.key.toLowerCase())) {
          const row = Number(input.dataset.row);
          const col = Number(input.dataset.col);
          if (input.value !== this.editGridCellValue(row, col)) return; // native undo while typing
          event.preventDefault();
          this.changeSheetHistory(event.key.toLowerCase() === 'z' && !event.shiftKey ? 'undo' : 'redo');
          return;
        }
        if (mod || event.altKey) return;
        let nr = Number(input.dataset.row);
        let nc = Number(input.dataset.col);
        if (event.key === 'Enter' && !event.shiftKey) nr++;
        else if (event.key === 'Tab') nc += event.shiftKey ? -1 : 1;
        else return;
        const data = state.gridData;
        if (!data || nr < data.offset || nr >= data.offset + 300 || nc < 0 || nc >= data.columns.length) return;
        event.preventDefault();
        input.blur();
        const next = area.querySelector(`textarea[data-row="${nr}"][data-col="${nc}"]`);
        next?.focus();
        next?.select();
      });
    }

    async addColumn() {
      const { state, api, post, askText, refreshWorkspace, toast } = this.d;
      if (!state.loadedSheet) return;
      const id = state.loadedSheet;
      const book = state.fingerprint;
      const data = await api('/api/analysis/sheet?' + new URLSearchParams({ id, offset: '0', limit: '1' }));
      if (data.columns.length >= 80) {
        toast('编辑工作表最多80列', 'warning');
        return;
      }
      const name = await askText('添加新列', '字段' + (data.columns.length + 1));
      if (!name) return;
      try {
        await this.queueWrite(async () => {
          if (book !== state.fingerprint) throw Error('工作簿已切换');
          const result = await post('/api/analysis/sheet/columns', {
            id,
            workbookKey: book,
            expectedRevision: data.revision,
            columns: [...data.columns, name],
          });
          state.sheetRevisions.set(id, result.revision);
        });
        if (book === state.fingerprint) {
          await refreshWorkspace();
          this.renderSheets();
        }
      } catch {
        // Error surfaced by queueWrite.
      }
    }

    async addRows() {
      const { state, post, refreshWorkspace } = this.d;
      if (!state.loadedSheet) return;
      const id = state.loadedSheet;
      const book = state.fingerprint;
      try {
        await this.queueWrite(async () => {
          if (book !== state.fingerprint) throw Error('工作簿已切换');
          const result = await post('/api/analysis/sheet/rows', {
            id,
            workbookKey: book,
            expectedRevision: state.sheetRevisions.get(id) ?? 0,
            count: 10,
          });
          state.sheetRevisions.set(id, result.revision);
        });
        if (book === state.fingerprint) {
          await refreshWorkspace();
          this.renderSheets();
        }
      } catch {
        // Error surfaced by queueWrite.
      }
    }

    async deleteSheet() {
      const { state, post, askText, refreshWorkspace, setTab, toast } = this.d;
      if (this.hasUnsaved()) {
        toast('仍有未保存的分析内容，请先重试保存', 'warning');
        return;
      }
      const sheet = state.workspace?.sheets?.find(s => s.id === state.loadedSheet);
      if (!sheet) return;
      const confirmation = await askText('确认删除编辑工作表', '', `输入“${sheet.name}”以确认`);
      if (confirmation !== sheet.name) {
        if (confirmation !== null) toast('名称不匹配，已取消删除', 'warning');
        return;
      }
      try {
        await post('/api/analysis/delete', { kind: 'sheet', id: sheet.id });
        state.loadedSheet = null;
        await refreshWorkspace();
        setTab('sheets');
        toast('已删除所选工作表', 'success');
      } catch (e) {
        toast(e.message, 'error');
      }
    }

    async navigateSheet(id, row, column) {
      const { $, state, open, setTab } = this.d;
      if (this.hasUnsaved()) throw Error('分析表仍有未保存内容，请先重试保存后再定位');
      if (!Number.isInteger(row) || !Number.isInteger(column) || row < 1 || column < 1) {
        throw Error('编辑表定位坐标无效');
      }
      await open();
      const sheet = state.workspace?.sheets?.find(s => s.id === id);
      if (!state.opened || !sheet || row > sheet.rows || column > sheet.columns.length) {
        throw Error('编辑表或来源位置已变化');
      }
      state.loadedSheet = id;
      state.sheetPage = Math.floor((row - 1) / this.sheetPageSize(id));
      state.gridData = null;
      await setTab('sheets');
      if (state.gridData?.id !== id) throw Error('编辑表读取失败，请重试定位');
      const target = $('analysisEditableGrid').querySelector(`textarea[data-row="${row - 1}"][data-col="${column - 1}"]`);
      if (!target) throw Error('未能读取所选来源位置');
      target.focus();
      target.scrollIntoView({ block: 'center', inline: 'nearest' });
    }

    capturePasteTarget(previous) {
      const { state, drafts } = this.d;
      const data = state.gridData;
      if (!state.opened || state.tab !== 'sheets' || !data || data.id !== state.loadedSheet) {
        throw Error('请先打开独立编辑表');
      }
      const id = state.loadedSheet;
      const book = state.fingerprint;
      const pathBook = state.book;
      const page = state.sheetPage;
      const revision = state.sheetRevisions.get(id);
      if (previous && (previous.id !== id || previous.book !== book || previous.pathBook !== pathBook || previous.page !== page)) {
        throw Error('目标表或页面已变化，请关闭后重新选择');
      }
      if (!previous && drafts.entries(book).some(entry => entry.id === id)) {
        throw Error('此表还有未保存草稿，请先重试保存后再选择性粘贴');
      }
      const selected = previous || (state.sheetCell?.id === id && state.sheetCell.page === page ? state.sheetCell : { row: data.offset, col: 0 });
      const row = selected.row;
      const col = selected.col;
      return {
        id,
        book,
        pathBook,
        page,
        revision,
        row,
        col,
        columns: data.columns.length,
        label: `${data.name} · ${window.ExcelLensAddress.columnName(col + 1)}${row + 1}`,
        current: () =>
          state.opened &&
          state.tab === 'sheets' &&
          state.fingerprint === book &&
          state.book === pathBook &&
          state.loadedSheet === id &&
          state.sheetPage === page &&
          state.gridData?.id === id &&
          state.sheetRevisions.get(id) === revision,
      };
    }

    async refreshPasteTarget(previous) {
      const { state } = this.d;
      if (
        state.fingerprint !== previous.book ||
        state.book !== previous.pathBook ||
        state.loadedSheet !== previous.id ||
        state.sheetPage !== previous.page
      ) {
        throw Error('目标已变化，请关闭后重新选择');
      }
      await this.flush();
      await this.loadSheet(previous.id, previous.page);
    }

    async openPasteSpecial() {
      const { state } = this.d;
      const book = state.fingerprint;
      const pathBook = state.book;
      const id = state.loadedSheet;
      const page = state.sheetPage;
      await this.flush();
      if (book !== state.fingerprint || pathBook !== state.book || id !== state.loadedSheet || page !== state.sheetPage) return;
      this.pasteSpecial?.open();
    }
  }

  window.ExcelLensAnalyticsSheets = ExcelLensAnalyticsSheets;
})();
