/* Search panel UI, chunked result list, advanced syntax validation, history and navigation. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const CHUNK_SIZE = 200;
  const SNIPPETS = [
    ['并且', ' AND '], ['或者', ' OR '], ['排除', ' NOT '],
    ['范围', ' BETWEEN 100 AND 200'], ['多选', ' IN ("债券","基金")'],
    ['空值', ' IS EMPTY'], ['正则', '~"^BX[0-9]+$"'], ['公式', 'formula:SUMIF']
  ];
  const isComposing = event => window.ExcelLensDisplayFormat?.isComposing?.(event) ?? Boolean(event?.isComposing || event?.keyCode === 229);

  class SearchPanel {
    constructor({state, api, toast, delay, copyText, navigateToCell, runSearch, cancelSearch}) {
      Object.assign(this, {s: state, api, toast, delay, copyText, navigateToCell, runSearch, cancelSearch});
      this.positions = new Map();
      this.syntaxSeq = 0;
      this.syntaxTimer = 0;
      this.renderedCount = 0;
      this.renderedData = null;
      this.filterTimer = 0;
    }

    buildHits(matches) {
      this.s.searchHitMap = new Map();
      for (const match of matches) {
        const sheet = match.sheet || this.s.searchSession?.sheet || this.s.sheet;
        if (!this.s.searchHitMap.has(sheet)) this.s.searchHitMap.set(sheet, new Set());
        this.s.searchHitMap.get(sheet).add(match.ref);
      }
    }

    isMatch(sheet, ref) {
      return this.s.searchHitMap?.get(sheet)?.has(ref) || false;
    }

    updateNavButtons() {
      const count = this.s.visibleSearchIndexes.length || 0;
      $('searchPrevBtn').disabled = !count;
      $('searchNextBtn').disabled = !count;
      $('copySearchResultsBtn').disabled = !count;
      $('saveSearchResultsBtn').disabled = !count;
    }

    resetWorkbook() {
      clearTimeout(this.syntaxTimer);
      clearTimeout(this.filterTimer);
      this.renderedCount = 0;
      this.renderedData = null;
      this.positions.clear();
      this.s.searchCache.clear();
      this.s.searchHitMap?.clear();
      ++this.syntaxSeq;
      this.s.searchSession = null;
      this.s.searchActivity?.end();
      ++this.s.searchRequestID;
      this.s.searchSelectedIndex = -1;
      this.s.searchJob = null;
      this.s.serverSearchHistory = [];
      this.s.visibleSearchIndexes = [];
      this.updateNavButtons();
      $('searchResults').replaceChildren();
      $('searchInput').value = '';
      $('searchResultFilter').value = '';
      $('searchResultSheet').value = '';
      $('searchHistoryPanel').hidden = true;
      $('searchSummary').textContent = '输入内容并搜索';
    }

    async refreshHistory() {
      if (!this.s.opened) return;
      const data = await this.api('/api/search/history');
      this.s.serverSearchHistory = data.items || [];
      const list = $('searchHistoryList');
      list.replaceChildren();
      if (!this.s.serverSearchHistory.length) {
        const message = document.createElement('div');
        message.className = 'history-empty';
        message.textContent = '尚无已完成的搜索';
        list.append(message);
        return;
      }
      const fragment = document.createDocumentFragment();
      for (const item of this.s.serverSearchHistory) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'history-item';
        button.title = `${item.query} · ${item.scope === 'all' ? '全部工作表' : item.sheet} · ${item.hits} 条`;
        const query = document.createElement('span');
        query.className = 'history-query';
        query.textContent = `${item.mode === 'value' ? '内容' : item.mode === 'reference' ? '引用' : item.mode === 'advanced' ? '高级' : '公式'} · ${item.query}`;
        const meta = document.createElement('span');
        meta.className = 'history-meta';
        meta.textContent = `${item.scope === 'all' ? '全表' : '当前表'} · ${item.hits} 条`;
        button.append(query, meta);
        button.addEventListener('click', async () => {
          $('searchHistoryPanel').hidden = true;
          $('searchHistoryBtn').setAttribute('aria-expanded', 'false');
          $('searchMode').value = item.mode;
          $('searchScope').value = item.scope;
          $('searchInput').value = item.query;
          $('searchAdvancedInput').value = item.query;
          this.syncMode();
          $('searchResultFilter').value = '';
          $('searchResultSheet').value = '';
          // Restore cached results without forcing the original worksheet to be
          // decompressed; navigation happens only when a hit is clicked.
          await this.runSearch(item.scope === 'current' ? item.sheet : null);
        });
        fragment.append(button);
      }
      list.append(fragment);
    }

    buildSheetChoices(matches) {
      const select = $('searchResultSheet');
      const old = select.value;
      const names = new Set(matches.map(item => item.sheet || this.s.searchSession?.sheet || this.s.sheet));
      select.replaceChildren();
      const all = document.createElement('option');
      all.value = ''; all.textContent = '全部 Sheet';
      select.append(all);
      for (const sheet of this.s.sheets) {
        if (!names.has(sheet.name)) continue;
        const option = document.createElement('option');
        option.value = sheet.name;
        option.textContent = sheet.name;
        select.append(option);
      }
      select.value = names.has(old) ? old : '';
    }

    filteredIndexes(matches) {
      const needle = $('searchResultFilter').value.trim().toLocaleLowerCase();
      const sheetOnly = $('searchResultSheet').value;
      const found = [];
      this.positions.clear();
      for (let i = 0; i < matches.length; i++) {
        const item = matches[i];
        const sheet = item.sheet || this.s.searchSession?.sheet || this.s.sheet;
        if (sheetOnly && sheet !== sheetOnly) continue;
        if (needle && ![sheet, item.ref, item.formula || '', item.value || ''].some(text => String(text).toLocaleLowerCase().includes(needle))) continue;
        this.positions.set(i, found.length);
        found.push(i);
      }
      return found;
    }

    copyVisible() {
      const matches = this.s.searchSession?.matches || [];
      const rows = [['Sheet', '单元格', '缓存值', '公式']];
      const clean = value => String(value ?? '').replaceAll('\t', ' ').replaceAll('\r', ' ').replaceAll('\n', ' ');
      for (const i of this.s.visibleSearchIndexes) {
        const hit = matches[i];
        rows.push([hit.sheet || this.s.searchSession.sheet, hit.ref, hit.value, hit.formula].map(clean));
      }
      this.copyText(rows.map(row => row.join('\t')).join('\n'));
    }

    downloadVisible() {
      const matches = this.s.searchSession?.matches || [];
      if (!this.s.visibleSearchIndexes.length) { this.toast('没有可导出的搜索结果', 'warning'); return; }
      const guard = text => {
        let value = String(text ?? '');
        if (/^[\s]*[=+\-@]/.test(value)) value = "'" + value;
        return '"' + value.replaceAll('"', '""') + '"';
      };
      const rows = [['工作表', '单元格', '缓存显示值', '原始公式']];
      for (const i of this.s.visibleSearchIndexes) {
        const hit = matches[i];
        rows.push([hit.sheet || this.s.searchSession?.sheet || this.s.sheet, hit.ref, hit.value, hit.formula]);
      }
      const csv = '\ufeff' + rows.map(row => row.map(guard).join(',')).join('\r\n');
      const url = URL.createObjectURL(new Blob([csv], {type: 'text/csv;charset=utf-8'}));
      const link = document.createElement('a'); link.href = url; link.download = 'ExcelLens_搜索结果.csv'; link.click();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
      this.toast(`已导出 ${this.s.visibleSearchIndexes.length} 条结果`, 'success');
    }

    displaySummary(message) {
      $('searchSummary').textContent = message;
      $('searchSummary').hidden = !message;
      if (this.s.searchSession) this.s.searchSession.summary = message;
    }

    syncMode() {
      const advanced = $('searchMode').value === 'advanced';
      $('searchAdvanced').hidden = !advanced;
      $('searchInput').hidden = advanced;
      if (advanced) {
        // Keep the primary action aligned: the advanced expression already has
        // its own edit area; don't duplicate hints in a second input.
        if (!$('searchAdvancedInput').value && $('searchInput').value) $('searchAdvancedInput').value = $('searchInput').value;
        this.populateFields();
        this.validateAdvanced();
      }
    }

    async validateAdvanced() {
      if ($('searchMode').value !== 'advanced') return;
      clearTimeout(this.syntaxTimer);
      const token = ++this.syntaxSeq;
      const source = $('searchAdvancedInput').value.trim();
      const feedback = $('searchAdvancedFeedback');
      feedback.classList.remove('is-invalid');
      if (!source) { feedback.textContent = '输入表达式，或选择下面的示例'; return; }
      this.syntaxTimer = setTimeout(async () => {
        try {
          await this.api('/api/search/validate', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({query: source})});
          if (token === this.syntaxSeq) { feedback.classList.remove('is-invalid'); feedback.textContent = '语法正确 · 运行时将逐行联合判断'; }
        } catch (e) {
          if (token === this.syntaxSeq) { feedback.classList.add('is-invalid'); feedback.textContent = e.message; }
        }
      }, 350);
    }

    insertAdvanced(text, replace = false) {
      const input = $('searchAdvancedInput');
      const start = input.selectionStart ?? input.value.length, end = input.selectionEnd ?? start;
      input.value = replace ? text : input.value.slice(0, start) + text + input.value.slice(end);
      const caret = replace ? input.value.length : start + text.length;
      input.focus(); input.setSelectionRange(caret, caret);
      this.validateAdvanced();
    }

    async populateFields() {
      const host = $('searchAdvancedFields'); if (!host) return;
      const sheet = this.s.sheet; if (!sheet || host.dataset.sheet === sheet) return;
      host.dataset.sheet = sheet; host.replaceChildren();
      try {
        const data = await this.api('/api/analysis/fields?' + new URLSearchParams({source: 'wb:' + sheet, headerRow: '1'}));
        if (host.dataset.sheet !== sheet) return;
        const fragment = document.createDocumentFragment();
        for (const f of (data.fields || []).slice(0, 96)) {
          const btn = document.createElement('button'); btn.type = 'button'; btn.textContent = f.name;
          btn.title = '插入字段 ' + f.id; btn.addEventListener('click', () => this.insertAdvanced('[' + f.name + ']:', false));
          fragment.append(btn);
        }
        host.append(fragment);
      } catch { host.replaceChildren(); }
    }

    open(mode = null, query = null) {
      if (mode !== null) $('searchMode').value = mode;
      if (query !== null) { $('searchInput').value = query; $('searchAdvancedInput').value = query; }
      if (this.s.searchSession && mode === null && query === null) {
        $('searchMode').value = this.s.searchSession.mode;
        $('searchScope').value = this.s.searchSession.scope;
        $('searchInput').value = this.s.searchSession.input;
        $('searchAdvancedInput').value = this.s.searchSession.input;
        this.displaySummary(this.s.searchSession.summary || '搜索结果已保留');
        this.renderResults({matches: this.s.searchSession.matches, mode: this.s.searchSession.mode, status: this.s.searchSession.status});
      }
      if (!this.s.searchSession && mode === null && query === null) {
        $('searchMode').value = 'value';
        $('searchSummary').hidden = true;
        $('searchResultsPane').hidden = true;
      }
      if (!$('searchDialog').open) $('searchDialog').show(); // Non-modal: grid remains clickable.
      this.syncMode(); this.updateNavButtons();
      setTimeout(() => ($('searchMode').value === 'advanced' ? $('searchAdvancedInput') : $('searchInput')).focus(), 20);
      if (query !== null && query !== '') this.runSearch();
    }

    async poll(jobID, requestID, alreadyCached, activity = globalThis.ExcelLensActivity?.begin('正在搜索', {slot: 'searchActivity'})) {
      this.s.searchActivity = activity;
      try {
        while (this.s.searchJob === jobID && requestID === this.s.searchRequestID) {
          if (!alreadyCached) await this.delay(350);
          let data;
          try { data = await this.api(`/api/search/status?id=${encodeURIComponent(jobID)}`); }
          catch (error) {
            if (requestID !== this.s.searchRequestID) return;
            this.displaySummary(error.message);
            this.s.searchJob = null;
            this.s.searchSession.status = 'error';
            $('searchBtn').disabled = false;
            $('cancelSearchBtn').hidden = true;
            return;
          }
          if (requestID !== this.s.searchRequestID || this.s.searchJob !== jobID) return;
          activity?.report(data.progress, 100, data.stage || '正在搜索');
          const matches = data.matches || [];
          const changed = this.s.searchSession.matches.length !== matches.length ||
            (this.s.searchSession.matches.length && matches.length && this.s.searchSession.matches.at(-1)?.ref !== matches.at(-1)?.ref);
          this.s.searchSession.matches = matches;
          this.s.searchSession.status = data.status;
          this.s.searchSession.truncated = Boolean(data.truncated);
          if (changed || data.status !== 'running') {
            this.buildHits(matches);
            this.renderResults(data);
            this.applyHighlightClasses();
          }
          if (data.status === 'running') {
            if (data.scope === 'all') this.displaySummary(`${Number(data.progress || 0).toFixed(0)}% · ${data.stage || '搜索中'} · ${matches.length} 条`);
            else this.displaySummary(`${Number(data.progress || 0).toFixed(0)}% · ${data.scanned || 0}/${data.total || 0} 行 · ${matches.length} 条`);
            continue;
          }
          if (data.status === 'done') {
            this.s.searchCache.set(this.s.searchSession.key, {matches, truncated: Boolean(data.truncated), status: 'done'});
            if (this.s.searchCache.size > 32) this.s.searchCache.delete(this.s.searchCache.keys().next().value);
            const elapsed = alreadyCached ? ' · 本机缓存' : ` · ${(Number(data.durationMs || 0) / 1000).toFixed(2)}秒`;
            this.displaySummary(`${matches.length} 条匹配${data.truncated ? '（最多显示500条）' : ''}${elapsed}`);
            if (!alreadyCached) this.refreshHistory().catch(() => {});
          } else if (data.status === 'cancelled') this.displaySummary('已取消搜索 · 已找到的结果仍可访问');
          else this.displaySummary(data.error || '搜索失败');
          this.s.searchJob = null;
          $('searchBtn').disabled = false;
          $('cancelSearchBtn').hidden = true;
          return;
        }
      } finally { activity?.end(); }
    }

    applyHighlightClasses() {
      document.querySelectorAll('.grid-region .grid-cell').forEach(node => {
        node.classList.toggle('search-match', this.isMatch(this.s.sheet, node.dataset.ref));
      });
    }

    makeResultItem(match, index, mode) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'result-item' + (index === this.s.searchSelectedIndex ? ' active' : '');
      button.dataset.matchIndex = String(index);
      button.setAttribute('role', 'option');
      button.setAttribute('aria-selected', String(index === this.s.searchSelectedIndex));
      const sheet = document.createElement('span');
      sheet.className = 'result-sheet';
      sheet.textContent = match.sheet || this.s.searchSession?.sheet || this.s.sheet;
      sheet.title = sheet.textContent;
      const ref = document.createElement('span');
      ref.className = 'result-ref'; ref.textContent = match.ref;
      const snippet = document.createElement('span');
      snippet.className = 'result-snippet';
      snippet.textContent = mode === 'value' ? match.value : match.formula;
      snippet.title = snippet.textContent;
      button.append(sheet, ref, snippet);
      return button;
    }

    appendResultChunk(targetCount = this.renderedCount + CHUNK_SIZE) {
      const box = $('searchResults'), data = this.renderedData;
      if (!data || this.renderedCount >= this.s.visibleSearchIndexes.length) return;
      const matches = data.matches || [];
      const end = Math.min(this.s.visibleSearchIndexes.length, Math.max(this.renderedCount, targetCount));
      const fragment = document.createDocumentFragment();
      for (let i = this.renderedCount; i < end; i++) {
        const index = this.s.visibleSearchIndexes[i];
        fragment.appendChild(this.makeResultItem(matches[index], index, data.mode));
      }
      this.renderedCount = end;
      box.appendChild(fragment);
    }

    renderResults(data) {
      const box = $('searchResults');
      const matches = data.matches || [];
      $('searchResultsPane').hidden = !matches.length;
      if (!matches.length) {
        this.positions.clear();
        this.s.visibleSearchIndexes = [];
        this.renderedCount = 0; this.renderedData = null;
        $('searchVisibleCount').hidden = true;
        this.buildSheetChoices([]);
        box.textContent = data.status === 'done' ? '没有找到匹配项' : '正在搜索…';
        this.updateNavButtons();
        return;
      }
      this.buildSheetChoices(matches);
      this.s.visibleSearchIndexes = this.filteredIndexes(matches);
      const counter = $('searchVisibleCount');
      counter.hidden = !matches.length;
      counter.textContent = this.s.visibleSearchIndexes.length === matches.length ? `${matches.length} 条结果` : `显示 ${this.s.visibleSearchIndexes.length} / ${matches.length} 条结果`;
      const prevScroll = box.scrollTop;
      this.renderedData = data;
      this.renderedCount = 0;
      if (!this.s.visibleSearchIndexes.length) {
        const empty = document.createElement('div');
        empty.className = 'history-empty';
        empty.textContent = '没有符合结果筛选条件的记录';
        box.replaceChildren(empty);
        this.updateNavButtons();
        return;
      }
      const selectedPos = this.positions.get(this.s.searchSelectedIndex) ?? -1;
      const initialCount = Math.max(CHUNK_SIZE, selectedPos >= 0 ? selectedPos + 20 : 0);
      box.replaceChildren();
      this.appendResultChunk(initialCount);
      box.scrollTop = prevScroll;
      this.updateNavButtons();
    }

    async goResult(index) {
      const matches = this.s.searchSession?.matches || [];
      if (!matches.length) return;
      const normalized = (index + matches.length) % matches.length;
      const match = matches[normalized];
      const box = $('searchResults');
      const old = box.children[this.positions.get(this.s.searchSelectedIndex)];
      if (old) {
        old.classList.remove('active');
        old.setAttribute('aria-selected', 'false');
      }
      this.s.searchSelectedIndex = normalized;
      this.s.searchSession.selected = normalized;
      const pos = this.positions.get(normalized);
      if (pos >= this.renderedCount) this.appendResultChunk(pos + 40);
      const nextNode = box.children[pos];
      if (nextNode) {
        nextNode.classList.add('active');
        nextNode.setAttribute('aria-selected', 'true');
        nextNode.scrollIntoView({block: 'nearest', inline: 'nearest'});
      }
      // Keep panel and all search results alive while the workbook switches sheets.
      const destination = match.sheet || this.s.searchSession.sheet;
      try { await this.navigateToCell(destination, match.ref); }
      catch (error) { this.toast(error.message, 'error'); }
    }

    step(delta) {
      const visible = this.s.visibleSearchIndexes;
      if (!visible.length) { this.open(); return; }
      const at = this.positions.get(this.s.searchSelectedIndex) ?? -1;
      const next = at < 0 ? (delta > 0 ? 0 : visible.length - 1) : (at + delta + visible.length) % visible.length;
      this.goResult(visible[next]);
    }

    bind() {
      $('searchOpenBtn').addEventListener('click', () => this.open());
      $('searchBtn').addEventListener('click', () => this.runSearch());
      $('searchHistoryBtn').addEventListener('click', () => {
        $('searchHistoryPanel').hidden = !$('searchHistoryPanel').hidden;
        $('searchHistoryBtn').setAttribute('aria-expanded', String(!$('searchHistoryPanel').hidden));
        if (!$('searchHistoryPanel').hidden) this.refreshHistory().catch(error => this.toast(error.message, 'error'));
      });
      $('searchHistoryRefreshBtn').addEventListener('click', () => this.refreshHistory().catch(error => this.toast(error.message, 'error')));
      $('searchResults').addEventListener('click', event => {
        const item = event.target.closest?.('.result-item[data-match-index]');
        if (item) this.goResult(Number(item.dataset.matchIndex));
      });
      $('searchResults').addEventListener('scroll', () => {
        const box = $('searchResults');
        if (this.renderedCount < this.s.visibleSearchIndexes.length && box.scrollTop + box.clientHeight >= box.scrollHeight - 160) {
          this.appendResultChunk();
        }
      }, {passive: true});
      $('searchResultFilter').addEventListener('input', () => {
        clearTimeout(this.filterTimer);
        const matches = this.s.searchSession?.matches || [];
        const apply = () => this.renderResults({matches: this.s.searchSession?.matches || [], mode: this.s.searchSession?.mode, status: this.s.searchSession?.status});
        if (matches.length <= 250) apply();
        else this.filterTimer = setTimeout(apply, 80);
      });
      $('searchResultSheet').addEventListener('change', () => {
        clearTimeout(this.filterTimer);
        this.renderResults({matches: this.s.searchSession?.matches || [], mode: this.s.searchSession?.mode, status: this.s.searchSession?.status});
      });
      $('copySearchResultsBtn').addEventListener('click', () => this.copyVisible());
      $('saveSearchResultsBtn').addEventListener('click', () => this.downloadVisible());
      $('searchPrevBtn').addEventListener('click', () => this.step(-1));
      $('searchNextBtn').addEventListener('click', () => this.step(1));
      $('cancelSearchBtn').addEventListener('click', () => this.cancelSearch(this.s.searchJob));
      $('searchMode').addEventListener('change', () => this.syncMode());
      $('searchAdvancedInput').addEventListener('input', () => this.validateAdvanced());
      $('searchAdvancedInput').addEventListener('keydown', event => {
        if (!isComposing(event) && event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); this.runSearch(); }
      });
      $('searchSyntaxHelp').addEventListener('click', () => { $('searchAdvancedHelp').hidden = !$('searchAdvancedHelp').hidden; });
      const chips = $('searchAdvancedSnippets');
      for (const [label, syntax] of SNIPPETS) {
        const b = document.createElement('button');
        b.type = 'button'; b.textContent = label; b.title = syntax;
        b.onclick = () => this.insertAdvanced(syntax);
        chips.append(b);
      }
      $('searchInput').addEventListener('keydown', event => {
        if (!isComposing(event) && event.key === 'Enter') this.runSearch();
      });
    }
  }

  window.ExcelLensSearchPanel = SearchPanel;
})();
