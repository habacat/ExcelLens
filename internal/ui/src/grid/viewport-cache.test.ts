import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { SheetViewportResponse } from '../contracts/workbook.ts';
import type { ViewportRequest } from './state.ts';
import { estimateViewportBytes, TileCache } from './viewport-cache.ts';

function makeRequest(row: number, end = row + 1, cols = 1): ViewportRequest {
  return {
    context: 'ctx',
    filtered: false,
    filterId: '',
    viewId: '',
    excludeThroughRow: 0,
    displayStart: row,
    displayEnd: end,
    visibleStart: row,
    visibleEnd: end,
    visibleColStart: 1,
    visibleColEnd: cols,
    row,
    offset: row - 1,
    limit: end - row + 1,
    startCol: 1,
    cols,
    selectedCols: null,
    leadRow: 0,
    leadCol: 0,
  };
}

function makeTile(n: number): SheetViewportResponse {
  return {
    sheet: 'S1',
    offset: 0,
    limit: 95,
    filtered: false,
    maxRow: 95,
    maxCol: 40,
    startCol: 1,
    colCount: 40,
    totalRows: 95,
    rows: Array.from({ length: 95 }, (_, i) => ({
      number: i + 1,
      cells: Array.from({ length: 40 }, (_, col) => ({
        ref: `${col}:${i}`,
        col: col + 1,
        raw: 'A'.repeat(n),
        display: 'B'.repeat(n),
        formula: 'C'.repeat(n),
        hasFormula: false,
      })),
    })),
  };
}

test('bounded viewport cache handles LRU reuse, replacement accounting, oversized tiles and payload fidelity', () => {
  const budget = 24 * 1024 * 1024;
  const cache = new TileCache(budget);
  const small = new TileCache(budget, 2);
  const payload: SheetViewportResponse = {
    sheet: 'S1',
    offset: 0,
    limit: 1,
    filtered: false,
    maxRow: 1,
    maxCol: 1,
    startCol: 1,
    colCount: 1,
    totalRows: 0,
    rows: [],
  };
  const reused = small.store('first', payload, makeRequest(1));
  small.store('second', payload, makeRequest(20));
  assert.equal(small.find('inside', makeRequest(1, 1)), reused);
  small.store('third', payload, makeRequest(40));
  assert.equal(small.find('first', makeRequest(1)), reused);
  assert.equal(small.find('second', makeRequest(20)), null);

  const big = makeTile(800);
  assert.ok(estimateViewportBytes(big) > 9 * 1024 * 1024);
  for (let i = 0; i < 15; i++) {
    cache.store(`tile-${i}`, big, makeRequest(i * 95 + 1, (i + 1) * 95, 40));
    assert.ok(cache.size <= 8);
    assert.ok(cache.bytes <= budget);
  }
  const oldBytes = cache.bytes;
  cache.store('tile-14', makeTile(1), makeRequest(14 * 95 + 1, 15 * 95, 40));
  assert.ok(cache.bytes < oldBytes, 'replaced tile bytes were not subtracted');
  let sum = 0;
  for (const x of cache.entries.values()) sum += x.bytes;
  assert.equal(sum, cache.bytes, 'cache accounting inconsistent');

  const giant = makeTile(3000);
  const transient = cache.store('giant', giant, makeRequest(1, 95, 40));
  assert.equal(transient.data, giant);
  assert.ok(
    !cache.entries.has('giant') && cache.bytes <= budget,
    'oversized tiles are usable only by the requesting foreground operation',
  );
  assert.equal(giant.rows[0]?.cells[0]?.raw, 'A'.repeat(3000), 'cache mutated source data');
});
