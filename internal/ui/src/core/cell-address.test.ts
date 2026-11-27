import { test } from 'node:test';
import assert from 'node:assert/strict';
import { columnName, columnNumber, parseColumn, parseRef } from './cell-address.ts';
import { errorMessage, escapeHtml, formatBytes, isComposing } from './display-format.ts';

test('columnName and columnNumber round-trip Excel column boundaries and reject invalid inputs', () => {
  const cases: readonly (readonly [number, string])[] = [
    [1, 'A'],
    [26, 'Z'],
    [27, 'AA'],
    [52, 'AZ'],
    [53, 'BA'],
    [702, 'ZZ'],
    [703, 'AAA'],
    [16_384, 'XFD'],
  ];
  for (const [num, label] of cases) {
    assert.equal(columnName(num), label);
    assert.equal(columnNumber(label), num);
    assert.equal(columnNumber(label.toLowerCase()), num);
  }
  for (const bad of [0, -1, 1.5, NaN, Infinity]) {
    assert.equal(columnName(bad), '');
  }
  for (const bad of ['', 'A1', '1', '$A', 'A B', 'ZZZZZZZZZZZZZZZ']) {
    assert.equal(columnNumber(bad), 0);
  }
});

test('parseColumn and parseRef strip anchors, enforce workbook bounds, and reject malformed coordinates', () => {
  assert.equal(parseColumn(' $xfd ', 16_384), 16_384);
  assert.equal(parseColumn('16384', 16_384), 16_384);
  assert.equal(parseColumn('XFE', 16_384), 0);
  assert.equal(parseColumn('16385', 16_384), 0);
  assert.equal(parseColumn('0', 16_384), 0);
  assert.equal(parseColumn(null, 16_384), 0);

  assert.deepEqual(parseRef(' $ab$1048576 ', 1_048_576, 16_384), {
    col: 28,
    row: 1_048_576,
    ref: 'AB1048576',
  });
  assert.equal(parseRef('A0', 1_048_576, 16_384), null);
  assert.equal(parseRef('A01', 1_048_576, 16_384), null);
  assert.equal(parseRef('A1048577', 1_048_576, 16_384), null);
  assert.equal(parseRef('XFE1', 1_048_576, 16_384), null);
  assert.equal(parseRef('A1:B2', 1_048_576, 16_384), null);
  assert.equal(parseRef(undefined, 1_048_576, 16_384), null);
});

test('display-format helpers enforce KB/MB/GB sizing, HTML escaping, IME guards, and error extraction', () => {
  assert.equal(formatBytes(-1), '—');
  assert.equal(formatBytes(NaN), '—');
  assert.equal(formatBytes('1024'), '—');
  assert.equal(formatBytes(0), '0 KB');
  assert.equal(formatBytes(51), '0.05 KB');
  assert.equal(formatBytes(1024), '1 KB');
  assert.equal(formatBytes(1024 * 1024), '1 MB');
  assert.equal(formatBytes(2.5 * 1024 ** 3), '2.5 GB');

  assert.equal(
    escapeHtml(`<img src="x" onerror='alert(&)'>`),
    '&lt;img src=&quot;x&quot; onerror=&#39;alert(&amp;)&#39;&gt;',
  );
  assert.equal(escapeHtml(null), '');
  assert.equal(escapeHtml(42), '42');

  assert.equal(isComposing({ isComposing: true }), true);
  assert.equal(isComposing({ keyCode: 229 }), true);
  assert.equal(isComposing({ isComposing: false, keyCode: 13 }), false);
  assert.equal(isComposing(null), false);

  assert.equal(errorMessage(new Error('boom')), 'boom');
  assert.equal(errorMessage('raw'), 'raw');
  assert.equal(errorMessage(404), '404');
  assert.equal(errorMessage({}), '');
});
