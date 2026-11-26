(() => {
  'use strict';

  class ExcelLensWorkbookIO {
    constructor(deps) {
      this.d = deps;
    }

    reportIndexProgress(activity, progress) {
      const { fmtBytes } = this.d;
      const stats = progress.statisticsProgress;
      if (stats && stats.total > 0) {
        activity?.report(stats.done, stats.total, progress.stage,
          stats.done.toLocaleString() + ' / ' + stats.total.toLocaleString() + ' 行');
      } else {
        activity?.report(progress.doneBytes, progress.totalBytes, progress.stage || '正在建立索引',
          progress.totalBytes > 0 ? fmtBytes(progress.doneBytes) + ' / ' + fmtBytes(progress.totalBytes) : '');
      }
    }

    renderSheetTabs() {
      const { $, S, selectSheetTab } = this.d;
      const tabs = $('sheetTabs');
      const fragment = document.createDocumentFragment();
      S.sheets.forEach((sheet, index) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'sheet-tab' + (S.sheet === sheet.name ? ' active' : '') + (sheet.state && sheet.state !== 'visible' ? ' hidden-sheet' : '');
        if (/^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor || '')) {
          button.classList.add('has-tab-color');
          button.style.setProperty('--sheet-tab-color', sheet.tabColor);
        }
        button.textContent = sheet.name;
        button.title = (sheet.sourceName || sheet.name) + (sheet.state && sheet.state !== 'visible' ? '（隐藏工作表）' : '');
        button.setAttribute('role', 'tab');
        button.setAttribute('aria-selected', S.sheet === sheet.name ? 'true' : 'false');
        button.addEventListener('click', () => selectSheetTab(sheet.name));
        fragment.appendChild(button);
        if (index === S.activeTab) button.dataset.activeIndex = String(index);
      });
      tabs.replaceChildren(fragment);
      const active = tabs.querySelector('.sheet-tab.active');
      if (active) active.scrollIntoView({ block: 'nearest', inline: 'nearest' });
      requestAnimationFrame(() => this.updateSheetPager());
    }

    updateSheetPager() {
      const { $ } = this.d;
      const tabs = $('sheetTabs');
      const hasOverflow = tabs.scrollWidth > tabs.clientWidth + 2;
      $('sheetPrevBtn').hidden = !hasOverflow;
      $('sheetNextBtn').hidden = !hasOverflow;
      $('sheetPrevBtn').disabled = tabs.scrollLeft <= 1;
      $('sheetNextBtn').disabled = tabs.scrollLeft >= tabs.scrollWidth - tabs.clientWidth - 2;
    }

    pageSheetTabs(direction) {
      const tabs = this.d.$('sheetTabs');
      const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
      tabs.scrollBy({
        left: direction * Math.max(110, Math.round(tabs.clientWidth * 0.72)),
        behavior: reduced ? 'auto' : 'smooth',
      });
    }

    renderSheetPicker(query = '') {
      const { $, S, selectSheetTab } = this.d;
      const list = $('sheetPickerList');
      const q = query.trim().toLocaleLowerCase();
      const fragment = document.createDocumentFragment();
      let shown = 0;
      S.sheets.forEach((sheet, index) => {
        if (q && !sheet.name.toLocaleLowerCase().includes(q)) return;
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'sheet-picker-item' + (S.sheet === sheet.name ? ' active' : '');
        const number = document.createElement('span');
        number.className = 'sheet-number';
        number.textContent = String(index + 1).padStart(2, '0');
        const name = document.createElement('span');
        name.textContent = sheet.name;
        if (/^#[0-9A-Fa-f]{6}$/.test(sheet.tabColor || '')) {
          const swatch = document.createElement('span');
          swatch.className = 'sheet-picker-color';
          swatch.style.backgroundColor = sheet.tabColor;
          button.append(number, swatch, name);
        } else {
          button.append(number, name);
        }
        button.addEventListener('click', async () => {
          $('sheetDialog').close();
          await selectSheetTab(sheet.name);
        });
        fragment.appendChild(button);
        shown++;
      });
      if (!shown) {
        const empty = document.createElement('div');
        empty.className = 'dialog-summary';
        empty.textContent = '没有匹配的工作表';
        fragment.appendChild(empty);
      }
      list.replaceChildren(fragment);
    }

    updateFileTitle() {
      const { $, S } = this.d;
      $('fileBadge').textContent = S.fileName;
      $('fileBadge').title = S.fileName;
      document.title = S.fileName || 'ExcelLens';
    }

    async loadStatus() {
      const {
        $, S, api, toast, setStatus, showWelcome, showWorkspace,
        loadStylePalette, selectSheet, refreshSearchHistory, panels,
      } = this.d;
      const launch = window.ExcelLensSession?.nativeLaunch || window.ExcelLensSession?.restoring;
      const activity = launch ? globalThis.ExcelLensActivity?.begin('正在打开文件', { priority: 2 }) : null;
      if (launch) {
        S.opening = true;
        $('openBtn').disabled = $('chooseFileBtn').disabled = true;
        activity?.stage('正在打开文件', 0, 60);
      }
      try {
        const data = await api('/api/status');
        panels.archiveCenter?.use(data.archive);
        if (!data.opened) {
          if (data.archive) panels.archiveCenter.present(data.archive);
          S.opened = false;
          panels.catalogueCenter?.resetBook();
          panels.dataRulesCenter?.resetBook();
          panels.printingCenter?.resetBook();
          panels.recordCardCenter?.resetBook();
          panels.selectionQuick?.resetBook();
          panels.transposeCenter?.resetBook();
          panels.cellExplanation?.resetBook();
          showWelcome();
          panels.recentManager?.load();
          if (data.error) {
            $('welcomeError').textContent = data.error;
            $('welcomeError').hidden = false;
            setStatus('打开失败');
          } else {
            setStatus('等待选择文件');
          }
          return;
        }
        S.opened = true;
        activity?.stage('正在准备工作表', 60, 65);
        S.sheets = data.sheets || [];
        S.activeTab = Number.isInteger(data.activeTab) ? data.activeTab : 0;
        S.fileName = data.fileName || '';
        S.book = data.book || '';
        S.fileSize = data.fileSize || 0;
        S.source = data.source || { format: 'xlsx' };
        if (S.workbookKey !== (data.workbookKey || '')) panels.bookmarkCenter?.resetBook();
        S.workbookKey = data.workbookKey || '';
        panels.calculator?.bookChanged();
        window.ExcelLensAnalytics?.bookChanged(S.workbookKey, S.book);
        panels.catalogueCenter?.controls();
        panels.dataRulesCenter?.controls();
        panels.printingCenter?.controls();
        panels.recordCardCenter?.controls();
        panels.selectionQuick?.changed();
        panels.transposeCenter?.changed();
        panels.cellExplanation?.changed();
        panels.profileCenter?.changed();
        this.updateFileTitle();
        showWorkspace();
        await loadStylePalette();
        const launchParams = new URLSearchParams(window.location?.search || '');
        const requested = launchParams.get('sourceSheet');
        const active = S.sheets.find(sheet => sheet.name === requested) || S.sheets[S.activeTab] || S.sheets[0];
        if (active) {
          await selectSheet(active.name, requested === active.name ? (launchParams.get('sourceRef') || '') : '', false, null, activity);
        } else {
          this.renderSheetTabs();
          setStatus('工作簿中没有工作表');
        }
        panels.bookmarkCenter.load(true).catch(() => {});
        refreshSearchHistory().catch(() => {});
      } catch (error) {
        showWelcome();
        panels.recentManager?.load();
        setStatus('本机服务连接失败');
        toast(error.message, 'error');
      } finally {
        activity?.end();
        if (launch) {
          this.finishOpening();
          $('openBtn').disabled = $('chooseFileBtn').disabled = false;
        }
      }
    }

    // Panels that stay unavailable while a file opens are refreshed when it ends;
    // otherwise their buttons remain disabled until the next sheet switch.
    finishOpening() {
      const { S, panels } = this.d;
      S.opening = false;
      panels.dataRulesCenter?.controls();
      panels.printingCenter?.controls();
      panels.recordCardCenter?.controls();
      panels.selectionQuick?.changed();
      panels.cellExplanation?.changed();
      panels.profileCenter?.changed();
    }

    chooseFile() {
      const { $, S } = this.d;
      if (!S.opening && this.canOpenWorkbook()) $('fileInput').click();
    }

    canOpenWorkbook(action = '切换文件') {
      const { S, toast, panels } = this.d;
      if (S.opening) return false;
      if (panels.editCopy?.grid.active && panels.editCopy.grid.blocked) {
        toast('请先确认当前单元格的输入');
        return false;
      }
      if (window.ExcelLensAnalytics?.hasUnsaved()) {
        toast(`分析表仍有未保存内容，请先在分析中重试保存，再${action}`, 'warning');
        return false;
      }
      return true;
    }

    async openWorkbook(load, name = '', size = 0, current = () => true) {
      const { $, S, setStatus } = this.d;
      if (S.opening || !this.canOpenWorkbook()) return false;
      S.opening = true;
      $('openBtn').disabled = $('chooseFileBtn').disabled = $('homeBtn').disabled = true;
      $('welcomeError').hidden = true;
      setStatus('正在打开文件');
      const activity = window.ExcelLensActivity?.begin('正在打开文件', { priority: 2 });
      activity?.stage('正在打开文件', 0, 60);
      try {
        await window.ExcelLensSession?.ready;
        const data = await load(activity);
        if (!current()) return false;
        activity?.stage('正在准备工作表', 60, 65);
        await this.acceptOpenedWorkbook(data, { fallbackName: name, fallbackSize: size, activity });
        await activity?.paintIfDue?.();
        return true;
      } catch (error) {
        if (!current()) return false;
        $('welcomeError').textContent = error.message;
        $('welcomeError').hidden = false;
        setStatus('打开失败');
        throw error;
      } finally {
        activity?.end();
        this.finishOpening();
        $('openBtn').disabled = $('chooseFileBtn').disabled = $('homeBtn').disabled = false;
      }
    }

    async uploadFile(file) {
      const { S, fileFormats, fmtBytes, toast } = this.d;
      if (!file) return;
      await window.ExcelLensWorkbookReady;
      if (S.opening) return;
      if (!fileFormats.supports(file.name)) {
        toast('请选择受支持的数据文件或压缩包', 'warning');
        return;
      }
      return this.openWorkbook(
        activity => window.ExcelLensTransport.upload(file, activity, fmtBytes),
        file.name,
        file.size
      ).catch(error => toast(error.message, 'error'));
    }

    async acceptOpenedWorkbook(data, { fallbackName = '', fallbackSize = 0, activity, revisionRef = '' } = {}) {
      const {
        S, setStatus, invalidateViewport, cancelFilter, showWorkspace,
        loadStylePalette, selectSheet, refreshSearchHistory, panels,
      } = this.d;
      if (panels.editCopy?.grid.active) panels.editCopy.grid.leave();
      panels.archiveCenter?.use(data.archive);
      if (data.archiveOnly) {
        panels.archiveCenter.present(data.archive);
        setStatus('请选择压缩包内的文件');
        return;
      }
      S.opened = true;
      S.sheets = data.sheets || [];
      S.activeTab = Number.isInteger(data.activeTab) ? data.activeTab : 0;
      S.fileName = data.fileName || fallbackName;
      S.fileSize = data.fileSize || fallbackSize;
      ++S.sheetEpoch;
      S.sheet = null;
      S.indexReady = false;
      S.index = null;
      // Only a confirmed workbook handoff discards the old cells. In
      // particular, don't lay out the previous grid again on leaving Home.
      invalidateViewport();
      panels.frozenPanes?.reset();
      panels.searchPanel.resetWorkbook();
      panels.traceUI?.reset();
      panels.formulaInspector?.resetWorkbook();
      panels.selectionInspector?.resetWorkbook();
      panels.profileCenter?.resetBook();
      panels.exportCenter?.resetBook();
      panels.reconcileCenter?.resetBook();
      panels.relationsCenter?.resetBook();
      panels.namedViews?.resetBook();
      panels.catalogueCenter?.resetBook();
      panels.dataRulesCenter?.resetBook();
      panels.printingCenter?.resetBook();
      panels.recordCardCenter?.resetBook();
      panels.selectionQuick?.resetBook();
      panels.transposeCenter?.resetBook();
      panels.cellExplanation?.resetBook();
      panels.rowViews?.resetBook();
      if (S.filterJob) cancelFilter(S.filterJob);
      S.history = [];
      S.historyIndex = -1;
      S.filtersBySheet.clear();
      S.positionsBySheet.clear();
      S.sheetViews.clear();
      S.sheetViewsBytes = 0;
      S.filterTotalsBySheet.clear();
      S.autoFiltersBySheet.clear();
      panels.bookmarkCenter.resetBook();
      S.autoFilter = null;
      S.sortKeysBySheet.clear();
      S.sortHeaderBySheet.clear();
      S.lastRulesBySheet.clear();
      panels.selectionStats?.resetBook();
      S.filterId = '';
      S.filtered = false;

      S.source = data.source || { format: 'xlsx' };
      S.workbookKey = data.workbookKey || '';
      panels.calculator?.bookChanged();
      S.book = data.book || '';
      panels.catalogueCenter?.controls();
      panels.dataRulesCenter?.controls();
      panels.printingCenter?.controls();
      panels.recordCardCenter?.controls();
      panels.selectionQuick?.changed();
      panels.transposeCenter?.changed();
      panels.cellExplanation?.changed();
      panels.profileCenter?.changed();
      window.ExcelLensAnalytics?.bookChanged(S.workbookKey, S.book);
      this.updateFileTitle();
      showWorkspace();
      await loadStylePalette();
      const active = S.sheets[S.activeTab] || S.sheets[0];
      if (active) {
        await selectSheet(active.name, revisionRef, false, null, activity);
      } else {
        this.renderSheetTabs();
        setStatus('工作簿中没有工作表');
      }
      panels.bookmarkCenter.load(true).catch(() => {});
      refreshSearchHistory().catch(() => {});
    }

    async openTaskSource(payload, current) {
      const { api } = this.d;
      return this.openWorkbook(
        () => api('/api/tasks/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }),
        '',
        0,
        current
      );
    }

    async cancelIndex(jobID) {
      const { api } = this.d;
      if (!jobID) return;
      try {
        await api('/api/index/cancel', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: jobID }),
        });
      } catch {
        /* job may have finished */
      }
    }
  }

  window.ExcelLensWorkbookIO = ExcelLensWorkbookIO;
})();
