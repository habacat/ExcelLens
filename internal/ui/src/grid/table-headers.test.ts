import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  decorateTableHeader,
  isTableHeader,
  tableHeaderColumnName,
  tableHeaderFromSource,
  useStickyTableLabels,
} from './table-headers.ts';

const colName = (n: number): string => String.fromCharCode(64 + n);

test('sticky TXT field labels remain independent of virtual row tiles and use literal text', () => {
  const dom = installDom();
  try {
    const columns = ['凭证来源', '<img onerror=bad>', '金额'];
    const config = tableHeaderFromSource({ row: 1, detected: true, columns });
    assert.ok(config);
    columns[0] = 'changed';
    const head = document.createElement('div');
    assert.equal(decorateTableHeader(head, 2, config, colName, true), true);
    assert.equal(head.children[0]?.textContent, 'B');
    assert.equal(head.children[1]?.textContent, '<img onerror=bad>');
    const filterBtn = head.children[2] as HTMLButtonElement;
    assert.equal(filterBtn.dataset.column, '2');
    assert.equal(filterBtn.disabled, false);
    assert.equal(tableHeaderColumnName(config, 1, colName), '凭证来源');
    assert.equal(config.row, 1);
    assert.equal(config.rules.size, 0);
  } finally {
    dom.restore();
  }
});

test('preview header buttons wait for the full index, filtered fields stay marked', () => {
  const dom = installDom();
  try {
    const config = tableHeaderFromSource({ row: 3, detected: true, columns: ['日期', '金额', ''] });
    assert.ok(config);
    config.rules.set(2, { column: 2, operator: 'equals', value: '1' });
    const preview = document.createElement('div');
    decorateTableHeader(preview, 2, config, colName, false);
    const btn = preview.children[2] as HTMLButtonElement;
    assert.equal(btn.disabled, true);
    assert.match(btn.className, /is-filtered/);
    assert.equal(tableHeaderColumnName(config, 3, colName), 'C 列');
    assert.equal(decorateTableHeader(document.createElement('div'), 4, config, colName, true), false);
  } finally {
    dom.restore();
  }
});

test('ordinary views and ambiguous input keep ordinary column letters', () => {
  const dom = installDom();
  try {
    assert.equal(isTableHeader(null), false);
    assert.equal(decorateTableHeader(document.createElement('div'), 1, null, colName, true), false);
    for (const header of [
      null,
      { row: 0, detected: true, columns: ['x'] },
      { row: 1, detected: true, columns: [] },
    ]) {
      assert.equal(tableHeaderFromSource(header), null);
    }
  } finally {
    dom.restore();
  }
});

test('TXT labels appear only after the source header clears the strip, and reset on return', () => {
  const config = tableHeaderFromSource({ row: 1, detected: true, columns: ['账号', '金额'] });
  assert.ok(config);
  for (const [top, expected] of [
    [0, false],
    [0.5, false],
    [24.99, false],
    [25, true],
    [50_000_000, true],
    [24, false],
    [0, false],
  ] as const) {
    assert.equal(useStickyTableLabels(config, top, 25), expected, `scrollTop=${top}`);
  }
  config.row = 3;
  assert.equal(useStickyTableLabels(config, 119.99, 120), false);
  assert.equal(useStickyTableLabels(config, 120, 120), true);
  assert.equal(useStickyTableLabels(config, 89.99, 90, 1), false);
  assert.equal(useStickyTableLabels(config, 90, 90, 1), true);
  assert.equal(useStickyTableLabels(config, 50_000_000, 0, 3), false);
  assert.equal(useStickyTableLabels(null, 50_000_000, 0), false);
  assert.equal(
    useStickyTableLabels(
      { row: 1, startCol: 1, endCol: 1, rules: new Map(), sortColumn: 0, descending: false },
      50_000_000,
      25,
    ),
    false,
  );
});
