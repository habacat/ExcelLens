import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createTransport } from '../core/transport.ts';
import { createInitialWorkbookState } from '../grid/state.ts';
import { installDom } from '../test-helpers/dom-mock.ts';
import { FormulaInspector, PALETTE, parseFormula } from './formula-inspector.ts';

test('parseFormula resolves cells, ranges, whole rows/columns, quoted sheets, and external workbooks while assigning shared colors', () => {
  const formula = `=SUM(A1:$B$3, A1, 'O''Brien'!$C$2, 'Sales 2026'!D:F, 2:5, '[budget.xlsx]Q1'!G9)`;
  const refs = parseFormula(formula, '当前表');
  assert.deepEqual(
    refs.map((r) => ({
      kind: r.kind,
      sheet: r.sheet,
      external: r.external,
      label: r.label,
      start: r.start,
      end: r.end,
    })),
    [
      {
        kind: 'range',
        sheet: '当前表',
        external: false,
        label: 'A1:$B$3',
        start: { row: 1, col: 1 },
        end: { row: 3, col: 2 },
      },
      {
        kind: 'cell',
        sheet: '当前表',
        external: false,
        label: 'A1',
        start: { row: 1, col: 1 },
        end: { row: 1, col: 1 },
      },
      {
        kind: 'cell',
        sheet: "O'Brien",
        external: false,
        label: "'O''Brien'!$C$2",
        start: { row: 2, col: 3 },
        end: { row: 2, col: 3 },
      },
      {
        kind: 'column',
        sheet: 'Sales 2026',
        external: false,
        label: "'Sales 2026'!D:F",
        start: { row: 1, col: 4 },
        end: { row: 1_048_576, col: 6 },
      },
      {
        kind: 'row',
        sheet: '当前表',
        external: false,
        label: '2:5',
        start: { row: 2, col: 1 },
        end: { row: 5, col: 16_384 },
      },
      {
        kind: 'cell',
        sheet: '[budget.xlsx]Q1',
        external: true,
        label: "'[budget.xlsx]Q1'!G9",
        start: { row: 9, col: 7 },
        end: { row: 9, col: 7 },
      },
    ],
  );

  const repeated = parseFormula('=A1+B2+A1+$A$1', 'S1');
  assert.equal(repeated.length, 4);
  assert.equal(repeated[0]?.colorIndex, repeated[2]?.colorIndex);
  assert.equal(repeated[0]?.colorIndex, repeated[3]?.colorIndex);
  assert.notEqual(repeated[0]?.colorIndex, repeated[1]?.colorIndex);
  assert.ok((repeated[1]?.colorIndex ?? -1) < PALETTE.length);
});

test('parseFormula ignores string literals, structured brackets, function calls, 3D references, and out-of-bounds coordinates', () => {
  const formula = `=IF(LOG10(A2)>0, "B2:C3 ""D4""", Table1[E5]) + Sheet1:Sheet3!F6 + XFE1 + A1048577`;
  const refs = parseFormula(formula, 'Main');
  assert.deepEqual(
    refs.map((r) => r.label),
    ['A2'],
  );
});

test('parseFormula bounds extracted token count to maxRefs on wide formulas', () => {
  const wide = '=' + Array.from({ length: 400 }, (_, i) => `A${i + 1}`).join('+');
  const refs = parseFormula(wide, 'Main', 32);
  assert.equal(refs.length, 32);
  assert.equal(refs[0]?.label, 'A1');
  assert.equal(refs[31]?.label, 'A32');
});

test('a previous workbook index reply cannot read a cell or evict a pending lookup for the new book', async (t) => {
  const dom = installDom();
  const oldIndex = Promise.withResolvers<Response>();
  const newIndex = Promise.withResolvers<Response>();
  let starts = 0;
  let reads = 0;
  const oldStarted = Promise.withResolvers<undefined>();
  t.mock.method(globalThis, 'fetch', () => {
    if (++starts === 1) {
      oldStarted.resolve(undefined);
      return oldIndex.promise;
    }
    return newIndex.promise;
  });
  try {
    const inspector = new FormulaInspector({
      state: createInitialWorkbookState(100, 20),
      element: dom.register('formula', 'div'),
      tooltip: dom.register('popover', 'div'),
      api: createTransport({ token: 'test' }).api,
      getCell: async () => {
        ++reads;
        return { ref: 'A1', col: 1, raw: '0012', display: '0012', hasFormula: false };
      },
      sheetExists: () => true, notify: () => undefined, copy: () => true, navigate: async () => true,
    });
    const previous = inspector.readCell('明细', 'A1', () => undefined);
    await oldStarted.promise;
    inspector.resetWorkbook();
    const next = inspector.readCell('明细', 'A1', () => undefined);
    oldIndex.resolve(Response.json({ ready: true, jobId: '' }));
    await assert.rejects(previous, { name: 'AbortError' });
    assert.equal(reads, 0);
    const joined = inspector.readCell('明细', 'A1', () => undefined);
    newIndex.resolve(Response.json({ ready: true, jobId: '' }));
    const values = await Promise.all([next, joined]);
    assert.strictEqual(values[0], values[1]);
    assert.equal(starts, 2);
    assert.equal(reads, 1);
  } finally {
    dom.restore();
  }
});
