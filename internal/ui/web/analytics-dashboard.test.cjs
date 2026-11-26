const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function makeNode(tag = 'div') {
  return {
    tagName: tag,
    children: [],
    className: '',
    textContent: '',
    innerHTML: '',
    replaceChildren(...items) { this.children = items; },
    append(...items) { this.children.push(...items); },
  };
}

function setup(fields, templates = []) {
  const nodes = new Map();
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, makeNode());
    return nodes.get(id);
  };
  const state = {
    opened: true,
    tab: 'templates',
    source: 'wb:明细',
    fields,
    query: { layout: 'tree', sourceScope: { mode: 'filtered' } },
    templateCategory: '全部',
    templateText: '',
    activeReportID: 'prev',
  };
  const toasts = [];
  const tabs = [];
  let runCount = 0;
  let renderBuilderCount = 0;
  const ctx = {
    window: {},
    document: { createElement: tag => makeNode(tag) },
    JSON,
    Set,
    Math,
  };
  vm.runInNewContext(fs.readFileSync(__dirname + '/analytics-dashboard.js', 'utf8'), ctx);
  const dashboard = new ctx.window.ExcelLensAnalyticsDashboard({
    root: makeNode(),
    $: node,
    state,
    templates,
    charts: null,
    esc: s => String(s ?? ''),
    post: async () => ({}),
    toast: (msg, tone) => toasts.push({ msg, tone }),
    askText: async () => null,
    setTab: t => { state.tab = t; tabs.push(t); },
    loadFields: async () => true,
    run: () => { runCount++; },
    executeQuery: async () => ({ current: () => false }),
    activateResult: () => false,
    renderResult: () => {},
    drill: () => {},
    openResultImage: () => {},
    effectiveMetrics: q => q.metrics || [],
    projectMetric: r => r,
    allFields: () => state.fields,
    queryBase: source => ({ source, headerRow: 1, rows: [], columns: [], value: '', aggregate: 'count', metrics: [], filters: [], transforms: [], sort: 'value_desc', top: 30, precision: 2, chart: 'bar', chartMetric: 0, layout: '' }),
    renderFields: () => {},
    renderBuilder: () => { renderBuilderCount++; },
    refreshWorkspace: async () => true,
    setAnalysisBusy: () => {},
  });
  return { dashboard, state, nodes, node, toasts, tabs, runs: () => runCount, builders: () => renderBuilderCount };
}

test('template application never substitutes an unrelated numeric field when value hints fail to match', () => {
  const h = setup([
    { id: 'C1', name: '机构名称', numeric: false },
    { id: 'C2', name: '保单件数', numeric: true },
  ]);
  h.dashboard.applyTemplate({
    title: '机构费用执行',
    row: ['机构'],
    value: ['销售费用', '金额'],
    aggregate: 'sum',
    chart: 'bar',
    top: 20,
  });
  assert.equal(h.runs(), 0, 'unmatched numeric hint must not auto-run with wrong column');
  assert.deepEqual(Array.from(h.state.query.rows), ['C1']);
  assert.equal(h.state.query.value, '');
  assert.equal(h.state.query.layout, 'tree');
  assert.deepEqual({ ...h.state.query.sourceScope }, { mode: 'filtered' });
  assert.equal(h.state.activeReportID, '');
  assert.equal(h.toasts.length, 1);
  assert.equal(h.toasts[0].tone, 'warning');
  assert.match(h.toasts[0].msg, /数值指标/);
});

test('monthly date template derives yearmonth transform and runs when row and metric match', () => {
  const h = setup([
    { id: 'C1', name: '入账日期', numeric: false },
    { id: 'C2', name: '发生金额', numeric: true },
  ]);
  h.dashboard.applyTemplate({
    title: '月度发生额趋势',
    row: ['日期'],
    value: ['金额'],
    aggregate: 'sum',
    chart: 'line',
    top: 12,
  });
  assert.equal(h.runs(), 1);
  assert.equal(h.state.query.transforms.length, 1);
  assert.equal(h.state.query.transforms[0].op, 'yearmonth');
  assert.deepEqual(Array.from(h.state.query.rows), ['D_template_date']);
  assert.equal(h.state.query.value, 'C2');
  assert.equal(h.builders(), 1);
});

test('template gallery filters by category and keyword search', () => {
  const templates = [
    { category: '财务', icon: '¥', title: '费用趋势', description: '按月汇总费用' },
    { category: '审计', icon: '!', title: '异常排查', description: '定位高额单据' },
  ];
  const h = setup([], templates);
  h.dashboard.renderTemplates();
  assert.equal(h.node('analysisTemplateCategories').children.length, 3);
  assert.equal(h.node('analysisTemplateGrid').children.length, 2);

  h.state.templateCategory = '审计';
  h.dashboard.renderTemplates();
  assert.equal(h.node('analysisTemplateGrid').children.length, 1);

  h.state.templateCategory = '全部';
  h.state.templateText = '费用';
  h.dashboard.renderTemplates();
  assert.equal(h.node('analysisTemplateGrid').children.length, 1);
});
