import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { TransposeLimits } from '../contracts/formats.ts';
import {
  TransposeModel,
  type TransposedItem,
  type TransposeSnapshotLike,
} from './transpose-model.ts';

const col = (n: number): string => String.fromCharCode(64 + n);
const limits: TransposeLimits = {
  rows: 250,
  columns: 50,
  cells: 5000,
  bytes: 1 << 20,
  pageColumns: 16,
};
const snapshot: TransposeSnapshotLike = {
  book: 'path',
  workbookKey: 'content',
  sheet: '明细',
  rows: 3,
  firstColumn: 1,
  lastColumn: 2,
  maxRow: 1_000_000_000,
};

interface MutableTransposeCell {
  ref: string;
  col: number;
  raw: string | number;
  display: string;
  type: string;
  hasFormula: boolean | string;
  cacheMissing?: boolean | string;
  style: number;
}

interface MutableTransposeRow {
  row: number;
  style: number;
  cells: MutableTransposeCell[] | null;
}

interface MutableTransposePayload {
  book: string;
  workbookKey: string;
  sheet: string;
  rows: MutableTransposeRow[];
  firstColumn: number;
  lastColumn: number;
  maxRow: number;
  startCol: number;
  endCol: number;
}

function makeFixture(): MutableTransposePayload {
  return {
    ...snapshot,
    startCol: 1,
    endCol: 2,
    rows: [7, 2, 10].map((row, i) => ({
      row,
      style: 3,
      cells: [
        {
          ref: `A${row}`,
          col: 1,
          raw: i ? '0000123' : '9007199254740993.31',
          display: i ? '0000123' : '9,007,199,254,740,993.31',
          type: i ? 's' : 'n',
          hasFormula: false,
          style: 3,
        },
      ],
    })),
  };
}

test('transpose preserves sorted source order, exact values, cell identity and sparse blanks', () => {
  const p = makeFixture();
  const before = JSON.stringify(p);
  const data = TransposeModel.validate(p, snapshot, limits, col);
  const first = TransposeModel.at(data, 1, 0, col);
  assert.ok(first);
  assert.equal(first.ref, 'A7');
  assert.equal(first.cell, p.rows[0]?.cells?.[0]);
  assert.equal(TransposeModel.value(first, 'raw', true), '9007199254740993.31');
  assert.equal(TransposeModel.value(first, 'display', true), '9,007,199,254,740,993.31');

  const second = TransposeModel.at(data, 1, 1, col);
  assert.ok(second);
  assert.equal(second.ref, 'A2');
  assert.equal(TransposeModel.value(second, 'raw', true), '0000123');

  const blank = TransposeModel.at(data, 2, 1, col);
  assert.ok(blank);
  assert.equal(blank.cell, null);
  assert.equal(blank.style, 3);
  assert.equal(JSON.stringify(p), before);
});

test('foreign bindings, forged coordinates, duplicate rows/cells, rounded values and response excess are rejected', () => {
  const mutations: readonly ((p: MutableTransposePayload) => void)[] = [
    (p) => {
      p.book = 'copy';
    },
    (p) => {
      p.workbookKey = 'new';
    },
    (p) => {
      p.sheet = 'other';
    },
    (p) => {
      p.startCol = 2;
    },
    (p) => {
      p.rows.pop();
    },
    (p) => {
      if (p.rows[1]) p.rows[1].row = 7;
    },
    (p) => {
      if (p.rows[0]?.cells?.[0]) p.rows[0].cells[0].ref = 'A2';
    },
    (p) => {
      const c = p.rows[0]?.cells?.[0];
      if (c) p.rows[0]?.cells?.push(c);
    },
    (p) => {
      if (p.rows[0]?.cells?.[0]) p.rows[0].cells[0].raw = Number('9007199254740993');
    },
    (p) => {
      if (p.rows[0]?.cells?.[0]) p.rows[0].cells[0].hasFormula = 'false';
    },
    (p) => {
      if (p.rows[0]?.cells?.[0]) p.rows[0].cells[0].cacheMissing = 'true';
    },
    (p) => {
      if (p.rows[0]) p.rows[0].style = -1;
    },
  ];
  for (const mutate of mutations) {
    const p = makeFixture();
    mutate(p);
    assert.throws(() => TransposeModel.validate(p, snapshot, limits, col));
  }
  assert.throws(() => TransposeModel.validate(makeFixture(), snapshot, { ...limits, bytes: 5 }, col));
  assert.throws(() => TransposeModel.validate(makeFixture(), snapshot, { ...limits, cells: 5 }, col));
});

test('missing formula caches stay unknown and unsupported formula metadata never becomes an expression', () => {
  const missingItem: TransposedItem = {
    row: 1,
    column: 1,
    ref: 'A1',
    style: 0,
    notice: '',
    cell: {
      ref: 'A1',
      col: 1,
      raw: '',
      display: '',
      style: 0,
      hasFormula: true,
      cacheMissing: true,
      formula: '=1+1',
    },
  };
  assert.equal(TransposeModel.value(missingItem, 'display', true), null);
  assert.equal(TransposeModel.value(missingItem, 'raw', true), null);
  assert.equal(TransposeModel.value(missingItem, 'formula', true), '=1+1');

  const unsupportedItem: TransposedItem = {
    ...missingItem,
    cell: {
      ref: 'A1',
      col: 1,
      raw: '17',
      display: '17',
      style: 0,
      hasFormula: true,
      formula: '[共享公式组缺失]',
    },
  };
  assert.equal(TransposeModel.value(unsupportedItem, 'formula', true), null);
  assert.equal(TransposeModel.value(unsupportedItem, 'raw', true), '17');
});

test('conditional display is a view choice and never replaces original text or source types', () => {
  const item: TransposedItem = {
    row: 1,
    column: 1,
    ref: 'A1',
    style: 0,
    notice: '',
    cell: {
      ref: 'A1',
      col: 1,
      raw: '12.340',
      display: '12.34',
      type: 's',
      style: 0,
      hasFormula: false,
      conditional: { display: '12.3元' },
    },
  };
  assert.equal(TransposeModel.value(item, 'display', true), '12.3元');
  assert.equal(TransposeModel.value(item, 'display', false), '12.34');
  assert.equal(TransposeModel.value(item, 'raw', true), '12.340');
  assert.equal(item.cell?.type, 's');
});

test('page size follows the actual container and remains bounded at zero and very large widths', () => {
  const cases: readonly (readonly [number, number])[] = [
    [0, 1],
    [396, 2],
    [1000, 6],
    [100_000, 16],
  ];
  for (const [width, want] of cases) {
    assert.equal(TransposeModel.pageSize(width, 16), want);
  }
});

test('an actual unindexed blank row encoded with null cells stays a sparse source coordinate', () => {
  const p = makeFixture();
  if (p.rows[0]) p.rows[0].cells = null;
  const data = TransposeModel.validate(p, snapshot, limits, col);
  const blank = TransposeModel.at(data, 2, 0, col);
  assert.ok(blank);
  assert.equal(blank.ref, 'B7');
  assert.equal(blank.cell, null);
  assert.equal(TransposeModel.value(blank, 'raw', true), '');
  assert.equal(p.rows[0]?.cells, null);
});
