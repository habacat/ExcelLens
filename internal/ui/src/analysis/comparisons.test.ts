import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisCell, ComparisonMode } from '../contracts/analysis.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeNoopApi, makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import { AnalysisComparisons } from './comparisons.ts';
import type { EditableAnalysisQuery } from './pivot-view.ts';
import { AnalysisWorkspace } from './workspace.ts';

test('comparison explanations preserve the exact ratio, original aggregate and current-layer scope', () => {
  const cell: AnalysisCell = {
    exact: '9007199254740993.31',
    display: '9007199254740993',
    count: 1,
    comparison: {
      mode: 'running_percent',
      axis: 'rows',
      exact: '1/3',
      display: '33.33%',
      percent: true,
      numerator: '3002399751580331.103333',
      denominator: '9007199254740993.31',
    },
  };
  const before = JSON.stringify(cell);
  assert.equal(AnalysisComparisons.selected(cell), cell.comparison);
  assert.match(AnalysisComparisons.title(cell), /精确比值：1\/3（1=100%）/);
  assert.match(AnalysisComparisons.title(cell), /本分组原始聚合：9007199254740993\.31/);
  assert.match(AnalysisComparisons.title(cell), /明细仅查看当前分组/);
  assert.equal(
    AnalysisComparisons.label({ comparison: 'running_percent', comparisonAxis: 'columns' }),
    '沿列累计占比',
  );
  assert.equal(JSON.stringify(cell), before);

  const scope = AnalysisComparisons.scope({
    layout: 'tree',
    metrics: [{ aggregate: 'average', comparison: 'running_percent' }],
  });
  assert.match(scope, /每层范围为该父分组/);
  assert.match(scope, /再截取前 N 项/);
  assert.match(scope, /不保证相加为100%/);
  assert.equal(AnalysisComparisons.scope({ metrics: [{ aggregate: 'sum' }] }), '');
});

test('comparison states remain nonnumeric and unknown names cannot resolve prototype properties', () => {
  const cases: readonly (readonly [string, string])[] = [
    ['zero_denominator', '分母为0'],
    ['empty', '无有效值'],
    ['empty_denominator', '分母无有效值'],
    ['constructor', '不可用'],
  ];
  for (const [status, label] of cases) {
    const rawCell: unknown = {
      exact: '0',
      display: '0',
      count: 0,
      comparison: {
        mode: 'percent_grand',
        status,
        exact: '',
        display: label,
        numerator: '0',
        denominator: '0',
      },
    };
    const cell = rawCell as AnalysisCell;
    assert.match(AnalysisComparisons.title(cell), new RegExp(label));
    assert.equal(AnalysisComparisons.selected(cell).exact, '');
  }
  for (const name of ['constructor', '__proto__', 'toString']) {
    assert.equal(
      AnalysisComparisons.label({ comparison: name as ComparisonMode }),
      '未知比较方式',
    );
  }
});

function setupEditor(query: EditableAnalysisQuery) {
  const dom = installDom({ fromIndexHtml: true });
  const root = document.getElementById('analysisRoot');
  assert.ok(root);
  const ws = new AnalysisWorkspace(root, {
    api: makeNoopApi(),
    formats: createFileFormats(makeTestFileFormatsConfig()),
    getSourceContext: () => ({
      workbookKey: 'book',
      book: 'path',
      source: 'wb:源',
      filterId: '',
      viewId: 'view',
      excludeThroughRow: 0,
    }),
    onToast: () => undefined,
  });
  ws.state.fields = [{ id: 'C3', name: '金额', column: 3, numeric: true }];
  ws.state.query = query;
  ws.renderMetricEditor();
  const list = document.getElementById('analysisMetricEditor');
  assert.ok(list);
  return {
    ws,
    list,
    restore: () => {
      ws.close();
      dom.restore();
    },
  };
}

function makeQuery(overrides: Partial<EditableAnalysisQuery>): EditableAnalysisQuery {
  return {
    source: 'wb:源',
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
    ...overrides,
  };
}

test('legacy single metrics gain saved comparison settings only on explicit user choice', () => {
  const q = makeQuery({ value: 'C3', aggregate: 'sum', metrics: [] });
  const complete = JSON.parse(JSON.stringify(q)) as EditableAnalysisQuery;
  const h = setupEditor(q);
  try {
    assert.equal(q.metrics.length, 0);
    const select = h.list.querySelector('.analysis-comparison-mode');
    assert.ok(select instanceof HTMLSelectElement);
    select.value = 'running_percent';
    select.onchange?.(new Event('change'));
    assert.equal(q.metrics[0]?.comparison, 'running_percent');
    assert.equal(q.metrics[0].field, 'C3');
    assert.deepEqual(complete.metrics, []);

    h.ws.renderMetricEditor();
    const axis = h.list.querySelector('.analysis-comparison-axis');
    assert.ok(axis instanceof HTMLSelectElement);
    assert.equal(axis.value, 'rows');
    axis.value = 'columns';
    axis.onchange?.(new Event('change'));
    assert.equal(q.metrics[0].comparisonAxis, 'columns');

    const mode = h.list.querySelector('.analysis-comparison-mode');
    assert.ok(mode instanceof HTMLSelectElement);
    mode.value = 'percent_row';
    mode.onchange?.(new Event('change'));
    assert.equal(q.metrics[0].comparisonAxis, undefined);
    assert.equal(q.metrics[0].aggregate, 'sum');
    assert.equal(q.aggregate, 'sum');
  } finally {
    h.restore();
  }
});

test('record counts support comparison without requiring an arbitrary numeric field', () => {
  const q = makeQuery({ value: '', aggregate: 'count', metrics: [] });
  const h = setupEditor(q);
  try {
    const select = h.list.querySelector('.analysis-comparison-mode');
    assert.ok(select instanceof HTMLSelectElement);
    select.value = 'percent_grand';
    select.onchange?.(new Event('change'));
    assert.equal(q.metrics[0]?.aggregate, 'count');
    assert.equal(q.metrics[0].field, '');
    assert.equal(q.metrics[0].comparison, 'percent_grand');
  } finally {
    h.restore();
  }
});
