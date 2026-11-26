(() => {
  'use strict';

  class ExcelLensAnalyticsDashboard {
    constructor(deps) {
      this.d = deps;
    }

    async renderDashboard() {
      const {
        root, $, state, charts, esc, post, toast, askText,
        refreshWorkspace, openReport, executeQuery, effectiveMetrics,
        projectMetric, activateResult, renderResult, drill, openResultImage,
      } = this.d;
      if (!state.opened) return;
      const book = state.fingerprint;
      const pathBook = state.book;
      const opened = state.openSerial;
      const host = $('analysisDashboardCards');
      host.replaceChildren();
      window.ExcelLensChartExport?.revalidate();

      const reports = (state.workspace?.reports || []).slice().reverse();
      const savedOrder = state.workspace?.dashboardOrder || [];
      if (savedOrder.length) {
        reports.sort((a, b) => {
          const ai = savedOrder.indexOf(a.id);
          const bi = savedOrder.indexOf(b.id);
          return ai < 0 && bi < 0 ? 0 : ai < 0 ? 1 : bi < 0 ? -1 : ai - bi;
        });
      }

      // Render all report shells, but only query charts as they approach the
      // viewport. A large dashboard must not sequentially rescan 80 workbooks.
      state.dashboardObserver?.disconnect();
      state.dashboardObserver = null;
      if (!reports.length) {
        const el = document.createElement('div');
        el.className = 'analysis-empty';
        el.textContent = '还没有报表。在“透视分析”里点“保存报表”，报表会出现在这里。';
        host.append(el);
        return;
      }

      const contentScroll = root.querySelector('.analysis-main');
      const lazyObserver = ('IntersectionObserver' in window)
        ? new IntersectionObserver(entries => {
            for (const entry of entries) {
              if (entry.isIntersecting) {
                lazyObserver.unobserve(entry.target);
                entry.target.__loadAnalysisCard?.();
              }
            }
          }, { root: contentScroll, rootMargin: '200px' })
        : null;
      state.dashboardObserver = lazyObserver;

      for (const report of reports) {
        if (!state.opened || state.tab !== 'dashboard') return;
        const card = document.createElement('div');
        card.className = 'analysis-dashboard-card';
        card.dataset.reportId = report.id;
        card.draggable = true;
        card.title = '拖动调整仪表盘顺序';
        card.innerHTML = `<div class="analysis-dashboard-card-head"><h3><span class="analysis-dashboard-grip" aria-hidden="true"><svg class="icon icon-small" aria-hidden="true" focusable="false"><use href="#i-grip"/></svg></span><span>${esc(report.name)}</span></h3><div class="analysis-dashboard-actions"><button type="button" data-report-image disabled title="导出已加载图表及精确附件">图片</button><button type="button" data-report-open>打开 ↗</button><button type="button" class="analysis-dashboard-remove" data-report-remove title="删除该报表" aria-label="删除报表">×</button></div></div><div class="analysis-dashboard-plot"><canvas></canvas><div class="analysis-chart-tooltip" hidden></div></div>`;
        host.append(card);

        card.addEventListener('dragstart', event => {
          if (event.target.closest('button')) {
            event.preventDefault();
            return;
          }
          event.dataTransfer.setData('text/plain', report.id);
          event.dataTransfer.effectAllowed = 'move';
          card.classList.add('is-dragging');
        });
        card.addEventListener('dragend', () => {
          host.querySelectorAll('.analysis-dashboard-card').forEach(c => c.classList.remove('is-dragging', 'is-drop-target'));
        });
        card.addEventListener('dragover', event => {
          event.preventDefault();
          card.classList.add('is-drop-target');
        });
        card.addEventListener('dragleave', () => card.classList.remove('is-drop-target'));
        card.addEventListener('drop', async event => {
          event.preventDefault();
          card.classList.remove('is-drop-target');
          const draggedID = event.dataTransfer.getData('text/plain');
          const selected = host.querySelector(`.analysis-dashboard-card[data-report-id="${CSS.escape(draggedID)}"]`);
          if (!selected || selected === card) return;
          const bounds = card.getBoundingClientRect();
          host.insertBefore(selected, event.clientX < bounds.left + bounds.width / 2 ? card : card.nextSibling);
          const ids = [...host.querySelectorAll('.analysis-dashboard-card')].map(el => el.dataset.reportId);
          try {
            await post('/api/analysis/dashboard/order', { ids });
            state.workspace.dashboardOrder = ids;
            toast('仪表盘顺序已保存', 'success');
          } catch (e) {
            toast('排序保存失败：' + e.message, 'error');
            this.renderDashboard();
          }
        });

        card.querySelector('[data-report-remove]').onclick = async () => {
          const typed = await askText('删除分析报表', '', `输入“${report.name}”以确认`);
          if (typed !== report.name) return;
          try {
            await post('/api/analysis/delete', { kind: 'report', id: report.id });
            await refreshWorkspace();
            toast('报表已删除', 'success');
          } catch (e) {
            toast(e.message, 'error');
          }
        };

        const openButton = card.querySelector('[data-report-open]');
        openButton.onclick = () => openReport(report);

        const info = document.createElement('span');
        info.className = 'analysis-card-loading';
        info.textContent = '接近视口时加载';
        card.querySelector('.analysis-dashboard-plot').append(info);

        card.__loadAnalysisCard = async () => {
          if (!card.isConnected || state.tab !== 'dashboard') return;
          const load = (card.__chartLoad = (card.__chartLoad || 0) + 1);
          const viewQuery = JSON.parse(JSON.stringify(report.query));
          card.__chartImageToken = null;
          card.querySelector('[data-report-image]').disabled = true;
          window.ExcelLensChartExport?.revalidate();
          info.textContent = '正在计算…';
          try {
            let snapshot = state.reportCache.get(book, viewQuery);
            if (!snapshot) {
              const fetched = await executeQuery(viewQuery);
              if (!fetched.current() || !card.isConnected) return;
              snapshot = state.reportCache.set(book, fetched.query || viewQuery, fetched);
            }
            if (
              card.isConnected &&
              state.tab === 'dashboard' &&
              book === state.fingerprint &&
              pathBook === state.book &&
              opened === state.openSerial &&
              load === card.__chartLoad
            ) {
              const mode = viewQuery.chart || 'bar';
              const metric = Math.min(effectiveMetrics(snapshot.query).length - 1, Math.max(0, Number(viewQuery.chartMetric) || 0));
              const canvas = card.querySelector('canvas');
              const projected = projectMetric(snapshot.result, metric);
              const token = { snapshot, mode, metric };
              info.remove();
              charts?.draw(canvas, projected, mode, (keys, col) => {
                if (activateResult(snapshot)) {
                  renderResult(snapshot.result, state.activeResultQuery);
                  drill(keys, col, 0, Boolean(state.activeResultQuery.columns?.length));
                }
              });
              card.__chartImageToken = token;
              window.ExcelLensChartExport?.revalidate();
              const current = () =>
                state.opened &&
                state.tab === 'dashboard' &&
                card.isConnected &&
                book === state.fingerprint &&
                pathBook === state.book &&
                opened === state.openSerial &&
                card.__chartImageToken === token;
              const image = card.querySelector('[data-report-image]');
              image.disabled = false;
              image.onclick = () => {
                if (current()) openResultImage(snapshot, canvas, projected, mode, metric, report.name, current);
              };
            }
          } catch (e) {
            if (card.isConnected) {
              info.textContent = '无法更新：' + e.message;
              info.classList.add('analysis-card-error');
            }
          }
        };

        if (lazyObserver) lazyObserver.observe(card);
        else card.__loadAnalysisCard();
      }
    }

    findMatch(hints, exclude = []) {
      const all = this.d.allFields().filter(f => !exclude.includes(f.id));
      for (const hint of hints || []) {
        const lower = hint.toLocaleLowerCase();
        const m = all.find(f => f.name.toLocaleLowerCase().includes(lower));
        if (m) return m;
      }
      return null;
    }

    renderTemplates() {
      const { $, state, templates, esc } = this.d;
      const categories = ['全部', ...new Set(templates.map(t => t.category))];
      const cats = $('analysisTemplateCategories');
      cats.replaceChildren();
      categories.forEach(name => {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = name;
        btn.className = name === state.templateCategory ? 'is-active' : '';
        btn.onclick = () => {
          state.templateCategory = name;
          this.renderTemplates();
        };
        cats.append(btn);
      });

      const container = $('analysisTemplateGrid');
      container.replaceChildren();
      const search = state.templateText.toLocaleLowerCase();
      templates
        .filter(
          t =>
            (state.templateCategory === '全部' || t.category === state.templateCategory) &&
            (!search || (t.title + ' ' + t.description).toLocaleLowerCase().includes(search))
        )
        .forEach(t => {
          const card = document.createElement('button');
          card.type = 'button';
          card.className = 'analysis-template-card';
          card.innerHTML = `<span class="analysis-template-ico">${esc(t.icon)}</span><strong>${esc(t.title)}</strong><small>${esc(t.description)}</small>`;
          card.onclick = () => this.applyTemplate(t);
          container.append(card);
        });
    }

    applyTemplate(template) {
      const { state, toast, setTab, queryBase, renderFields, renderBuilder, run } = this.d;
      if (!state.fields.length) {
        toast('请先选择一张包含数据的工作表', 'warning');
        setTab('pivot');
        return;
      }
      state.activeReportID = '';
      const q = queryBase(state.source);
      q.layout = state.query?.layout || '';
      if (state.query?.sourceScope) {
        q.sourceScope = JSON.parse(JSON.stringify(state.query.sourceScope));
      }
      const main = this.findMatch(template.row);
      // No silent substitution of an unrelated numerical column. Audit and
      // financial templates must never produce a plausible but wrong report.
      const metric = this.findMatch(template.value || [], main ? [main.id] : []) || null;
      if (main) q.rows = [main.id];
      const column = this.findMatch(template.col || [], q.rows);
      if (column) q.columns = [column.id];
      q.value = template.aggregate === 'count' ? '' : metric?.id || '';
      q.aggregate = template.aggregate;
      const ready = Boolean(main) && (q.aggregate === 'count' || Boolean(metric));
      q.chart = template.chart;
      q.top = template.top || 30;
      q.title = template.title;
      if (
        main &&
        (template.title.includes('月度') || template.title.includes('到期年份')) &&
        /日期|时间|日|月|年/.test(main.name)
      ) {
        const fieldID = 'D_template_date';
        const op = template.title.includes('到期年份') ? 'year' : 'yearmonth';
        q.transforms = [{ id: fieldID, name: op === 'year' ? '年份' : '年月', source: main.id, op, arg: '' }];
        q.rows = [fieldID];
      }
      state.query = q;
      setTab('pivot');
      renderFields();
      renderBuilder();
      if (ready) {
        run();
      } else {
        toast(
          '模板已载入；没有匹配到' + (!main ? '分类字段' : !metric ? '数值指标' : '必需字段') + '，请在左侧确认字段后运行',
          'warning'
        );
      }
    }
  }

  window.ExcelLensAnalyticsDashboard = ExcelLensAnalyticsDashboard;
})();
