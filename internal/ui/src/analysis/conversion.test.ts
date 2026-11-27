import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AnalysisTransform } from '../contracts/analysis.ts';
import { createFileFormats } from '../core/formats.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { makeTestFileFormatsConfig } from '../test-helpers/grid-fixture.ts';
import type { EditableAnalysisQuery } from './pivot-view.ts';
import { AnalysisWorkspace } from './workspace.ts';

function setupWorkspace(query: EditableAnalysisQuery) {
  const dom = installDom({ fromIndexHtml: true });
  const root = document.getElementById('analysisRoot');
  assert.ok(root);
  const calls: string[] = [];
  const ws = new AnalysisWorkspace(root, {
    api: async <T = unknown>(url: string): Promise<T> => {
      calls.push(url);
      throw new Error(`Unexpected scan call: ${url}`);
    },
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
  ws.state.fields = [
    { id: 'C1', name: '原日期', column: 1 },
    { id: 'C2', name: '金额', column: 2, numeric: true },
  ];
  ws.state.query = query;
  return {
    ws,
    calls,
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

test('analysis conversion controls select explicit date order without starting analysis', () => {
  const step: AnalysisTransform = {
    id: 'D_date',
    name: '转换日期',
    source: 'C1',
    op: 'date',
    arg: 'auto',
  };
  const h = setupWorkspace(makeQuery({ transforms: [step] }));
  try {
    h.ws.queryEditor.renderTransforms();
    const list = document.getElementById('analysisTransformList');
    assert.ok(list);
    const box = list.children[0];
    assert.ok(box);
    const formats = box.children[2];
    assert.ok(formats instanceof HTMLSelectElement);
    assert.equal(formats.value, 'auto');
    assert.deepEqual(
      [...formats.children].map((x) => (x instanceof HTMLOptionElement ? x.value : '')),
      ['auto', 'compact', 'dmy', 'mdy'],
    );
    formats.value = 'dmy';
    formats.onchange?.(new Event('change'));
    assert.equal(step.arg, 'dmy');

    const operation = box.children[1]?.children[1];
    assert.ok(operation instanceof HTMLSelectElement);
    operation.value = 'time';
    operation.onchange?.(new Event('change'));
    assert.equal(step.op, 'time');
    assert.equal(step.arg, 'auto');
    assert.equal(h.calls.length, 0);
  } finally {
    h.restore();
  }
});

test('removing a conversion also removes dependent fields and invalid query references', () => {
  const query = makeQuery({
    transforms: [
      { id: 'D_date', name: '日期', source: 'C1', op: 'date', arg: 'auto' },
      { id: 'D_year', name: '年', source: 'D_date', op: 'year' },
      { id: 'D_keep', name: '保留', source: 'C2', op: 'abs' },
    ],
    filters: [
      { field: 'D_year', op: 'eq', value: '2026' },
      { field: 'C1', op: 'notblank', value: '' },
    ],
    rows: ['D_date'],
    columns: ['D_year'],
    value: 'D_year',
    aggregate: 'sum',
    metrics: [
      { field: 'D_year', aggregate: 'sum' },
      { field: 'C2', aggregate: 'sum' },
    ],
  });
  const h = setupWorkspace(query);
  try {
    h.ws.queryEditor.removeTransform('D_date');
    assert.deepEqual(
      query.transforms.map((t) => t.id),
      ['D_keep'],
    );
    assert.deepEqual(
      query.filters.map((f) => f.field),
      ['C1'],
    );
    assert.equal(query.rows.length, 0);
    assert.equal(query.columns.length, 0);
    assert.equal(query.value, 'C2');
    assert.equal(query.aggregate, 'sum');
    assert.deepEqual(
      query.metrics.map((m) => m.field),
      ['C2'],
    );
  } finally {
    h.restore();
  }
});
