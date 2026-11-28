import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisField } from '../contracts/analysis.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  AnalysisDashboardController,
  type AnalysisDashboardState,
} from './dashboard.ts';
import { AnalysisResultCache, type WorkspaceAnalysisQuery } from './results.ts';
import type { AnalysisTemplate } from './templates.ts';

function setup(fields: readonly AnalysisField[], templates: readonly AnalysisTemplate[] = []) {
  const dom = installDom({ fromIndexHtml: true });
  const root = document.getElementById('analysisRoot') ?? document.createElement('div');
  const state: AnalysisDashboardState = {
    opened: true,
    tab: 'templates',
    fingerprint: 'book',
    book: 'path',
    openSerial: 1,
    source: 'wb:明细',
    activeReportID: 'prev',
    workspace: null,
    dashboardObserver: null,
    reportCache: new AnalysisResultCache(),
    fields,
    query: {
      source: 'wb:明细',
      rows: [],
      aggregate: 'count',
      layout: 'tree',
      sourceScope: { version: 1, mode: 'filtered', fingerprint: 'book' },
    },
    activeResultQuery: null,
    templateCategory: 'all',
    templateText: '',
  };
  const toasts: { readonly msg: string; readonly tone: string | undefined }[] = [];
  const tabs: string[] = [];
  let runCount = 0;
  let renderBuilderCount = 0;

  const dashboard = new AnalysisDashboardController({
    root,
    state,
    templates,
    post: async () => ({}),
    toast: (msg, tone) => {
      toasts.push({ msg, tone });
    },
    askText: async () => null,
    setTab: (t) => {
      state.tab = t;
      tabs.push(t);
    },
    run: async () => {
      runCount++;
    },
    executeQuery: async () => ({
      result: {
        rowFields: [],
        columnKeys: [],
        rows: [],
        totals: [],
        grand: { display: '0', exact: '0', count: 0 },
        scanned: 0,
        included: 0,
        invalidNumbers: 0,
        missingFormulaCaches: 0,
        aggregation: 'count',
      },
      job: 'job',
      current: () => false,
    }),
    activateResult: () => false,
    renderResult: () => undefined,
    drill: async () => undefined,
    openResultImage: () => undefined,
    effectiveMetrics: (q) => q?.metrics ?? [],
    allFields: () => state.fields,
    queryBase: (source): WorkspaceAnalysisQuery => ({
      source,
      headerRow: 1,
      rows: [],
      columns: [],
      value: '',
      aggregate: 'count',
      metrics: [],
      filters: [],
      transforms: [],
      sort: 'value_desc',
      top: 30,
      precision: 2,
      chart: 'bar',
      chartMetric: 0,
      layout: '',
    }),
    renderFields: () => undefined,
    renderBuilder: () => {
      renderBuilderCount++;
    },
    refreshWorkspace: async () => true,
    openReport: async () => undefined,
  });

  return {
    dashboard,
    state,
    toasts,
    tabs,
    runs: () => runCount,
    builders: () => renderBuilderCount,
    restore: () => dom.restore(),
  };
}

test('template application never substitutes an unrelated numeric field when value hints fail to match', () => {
  const h = setup([
    { id: 'C1', name: '机构名称', column: 1, numeric: false },
    { id: 'C2', name: '保单件数', column: 2, numeric: true },
  ]);
  try {
    h.dashboard.applyTemplate({
      category: 'finance',
      icon: '¥',
      title: '机构费用执行',
      description: '测试模板',
      row: ['机构'],
      col: [],
      value: ['销售费用', '金额'],
      aggregate: 'sum',
      chart: 'bar',
      top: 20,
    });
    assert.equal(h.runs(), 0, 'unmatched numeric hint must not auto-run with wrong column');
    assert.ok(h.state.query);
    assert.deepEqual([...h.state.query.rows], ['C1']);
    assert.equal(h.state.query.value, '');
    assert.equal(h.state.query.layout, 'tree');
    assert.equal(h.state.query.sourceScope?.mode, 'filtered');
    assert.equal(h.state.activeReportID, '');
    assert.equal(h.toasts.length, 1);
    assert.equal(h.toasts[0]?.tone, 'warning');
    assert.match(h.toasts[0].msg, /数值指标/);
  } finally {
    h.restore();
  }
});

test('monthly date template derives yearmonth transform and runs when row and metric match', () => {
  const h = setup([
    { id: 'C1', name: '入账日期', column: 1, numeric: false },
    { id: 'C2', name: '发生金额', column: 2, numeric: true },
  ]);
  try {
    h.dashboard.applyTemplate({
      category: 'finance',
      icon: '¥',
      title: 'Monthly activity',
      description: '测试月度趋势',
      row: ['日期'],
      col: [],
      value: ['金额'],
      aggregate: 'sum',
      chart: 'line',
      top: 12,
      dateGrouping: 'yearmonth',
    });
    assert.equal(h.runs(), 1);
    assert.ok(h.state.query);
    assert.equal(h.state.query.transforms?.length, 1);
    assert.equal(h.state.query.transforms[0]?.op, 'yearmonth');
    assert.deepEqual([...h.state.query.rows], ['D_template_date']);
    assert.equal(h.state.query.value, 'C2');
    assert.equal(h.builders(), 1);
  } finally {
    h.restore();
  }
});

test('template gallery filters by category and keyword search', () => {
  const templates: readonly AnalysisTemplate[] = [
    {
      category: 'finance',
      icon: '¥',
      title: '费用趋势',
      description: '按月汇总费用',
      row: [],
      col: [],
      value: [],
      aggregate: 'sum',
      chart: 'bar',
      top: 20,
    },
    {
      category: 'audit',
      icon: '!',
      title: '异常排查',
      description: '定位高额单据',
      row: [],
      col: [],
      value: [],
      aggregate: 'count',
      chart: 'bar',
      top: 20,
    },
  ];
  const h = setup([], templates);
  try {
    h.dashboard.renderTemplates();
    assert.equal(document.getElementById('analysisTemplateCategories')?.children.length, 3);
    assert.equal(document.getElementById('analysisTemplateGrid')?.children.length, 2);

    h.state.templateCategory = 'audit';
    h.dashboard.renderTemplates();
    assert.equal(document.getElementById('analysisTemplateGrid')?.children.length, 1);

    h.state.templateCategory = 'all';
    h.state.templateText = '费用';
    h.dashboard.renderTemplates();
    assert.equal(document.getElementById('analysisTemplateGrid')?.children.length, 1);
  } finally {
    h.restore();
  }
});
