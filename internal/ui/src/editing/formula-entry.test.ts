import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installDom } from '../test-helpers/dom-mock.ts';
import {
  WorksheetFormulaEntry,
  type FormulaEntryHost,
} from './edit-formula-entry.ts';
import { visibleAreas } from './edit-reference-view.ts';
import { FormulaInput, type FormulaPoint } from './formula-entry-model.ts';

test('the active function argument ignores nested commas, quoted names and table fields', () => {
  for (const [s, fn, arg] of [
    ['=IF(SUM(A1,B2),', 'IF', 1],
    ['=IF(A1,"a,""b",', 'IF', 2],
    ["=SUM('O''Brien, ('!B1,", 'SUM', 1],
    ['=SUM(T[[a,b]],', 'SUM', 1],
    ['=SUM({1,2;3,4},', 'SUM', 1],
    ['=IF((A1+B1),SU', 'IF', 1],
  ] as const) {
    const c = FormulaInput.context(s, s.length);
    assert.ok(c, s);
    assert.equal(c.fn, fn, s);
    assert.equal(c.arg, arg, s);
  }
  for (const s of ['="a,SU', "='O''Brien", '=Table[[a,SU']) {
    assert.equal(FormulaInput.context(s, s.length), null, s);
  }
  assert.equal(
    FormulaInput.close('=SUM(Table[Turnover (2026)]'),
    '=SUM(Table[Turnover (2026)])',
  );
  assert.equal(FormulaInput.close('=)('), '=)(');
});

test('pointed ranges retain their sheet and lock mode, and F4 can target a single endpoint', () => {
  const a: FormulaPoint = { kind: 'cell', row: 4, col: 3 };
  const b: FormulaPoint = { kind: 'cell', row: 2, col: 2 };
  const ref = FormulaInput.reference(a, b, "O'Brien 明细", '原表', "'前表'!$D4");
  assert.equal(ref, "'O''Brien 明细'!$B2:$C4");
  const s = `=SUM(${ref})`;
  const from = s.indexOf("'");
  const end = s.indexOf(')');
  const all = FormulaInput.absolute(s, from, end, '原表');
  assert.ok(all);
  assert.equal(all.value, 'B2:C4');
  const last = FormulaInput.absolute(s, end, end, '原表');
  assert.ok(last);
  assert.equal(last.value, 'C4');
  assert.equal(
    FormulaInput.reference(
      { kind: 'column', col: 4 },
      { kind: 'column', col: 1 },
      '手续费',
      '原表',
    ),
    '手续费!A:D',
  );
  assert.equal(FormulaInput.absolute('="B3"', 4, 4, '原表'), null);
});

test('date-sorted or filtered worksheet references paint only the visible source members', () => {
  const ref = { row1: 5, row2: 8, col1: 2, col2: 3, color: 'blue' };
  const areas = visibleAreas(
    [ref],
    true,
    1,
    new Map([
      [1, 8],
      [2, 6],
      [3, 2],
      [4, 7],
    ]),
    () => null,
  );
  assert.equal(
    JSON.stringify(areas.map((a) => [a.visual1, a.visual2])),
    JSON.stringify([
      [1, 2],
      [4, 4],
    ]),
  );
});

test('a delayed reference lookup cannot overwrite newer typing; repeated arrows follow in order', async () => {
  const dom = installDom({ fromIndexHtml: true });
  try {
    const input = document.createElement('textarea');
    input.value = '=';
    input.setSelectionRange(1, 1);
    const formula = document.createElement('textarea');
    formula.value = '=';
    formula.setSelectionRange(1, 1);
    const region = document.createElement('div');
    let release: (() => void) | undefined;
    let calls = 0;

    const host: FormulaEntryHost = {
      input,
      formula,
      region,
      composing: false,
      moving: false,
      stale: false,
      draft: {
        address: { sheet: 'A', row: 1, col: 1 },
        ref: 'A1',
        text: '=',
        field: input,
        pointKeys: true,
      },
      selected: { row: 1, col: 1, ref: 'A1' },
      owner: {
        api: async <T = unknown>(): Promise<T> => {
          const empty: unknown = {};
          return empty as T;
        },
        post: async <T = unknown>(): Promise<T> => {
          const empty: unknown = {};
          return empty as T;
        },
        pending: null,
        data: { book: 'book', document: { id: 'doc', binding: 'bound', revision: '1' } },
        context: () => ({ sheet: 'A' }),
      },
      view: {
        ready: () => true,
        paintReferences: () => undefined,
        bounds: () => null,
        dragPoint: () => ({ x: 0, y: 0, scrolling: false }),
        referenceStep: (base) => {
          calls++;
          const next: FormulaPoint = { kind: 'cell', row: base.row + 1, col: 1 };
          if (calls === 1) {
            return new Promise<FormulaPoint>((resolve) => {
              release = () => resolve(next);
            });
          }
          return next;
        },
      },
      focus: () => undefined,
      status: () => undefined,
      schedulePosition: () => undefined,
    };

    const entry = new WorksheetFormulaEntry(host);
    const written: number[] = [];
    const origWrite = entry.writePoint.bind(entry);
    entry.writePoint = (anchor, point, append) => {
      origWrite(anchor, point, append);
      if (point.kind === 'cell') written.push(point.row);
    };

    entry.step('ArrowDown', false);
    entry.step('ArrowDown', false);
    release?.();
    await entry.pendingStep;
    assert.deepEqual(written, [2, 3]);

    calls = 0;
    entry.step('ArrowDown', false);
    entry.sequence++;
    release?.();
    await entry.pendingStep;
    assert.deepEqual(written, [2, 3]);
    entry.reset();
  } finally {
    dom.restore();
  }
});

test('formula input displays workbook prefixes only outside literal text and field names', () => {
  const s =
    '=_xlfn.LET(_xlpm.a,2,_xlfn.TEXTJOIN(",",TRUE,"_xlpm.a",Sales[_xlfn.LET(],_xlpm.a))';
  assert.equal(
    FormulaInput.display(s),
    '=LET(a,2,TEXTJOIN(",",TRUE,"_xlpm.a",Sales[_xlfn.LET(],a))',
  );
  assert.equal(FormulaInput.display("='_xlfn.LET('!A1"), "='_xlfn.LET('!A1");
});

test('column completion keeps the outer table and existing closing brackets', () => {
  for (const [text, caret, table, prefix, closures] of [
    ['=SUM(销售表[[#Data],[金', null, '销售表', '金', 2],
    ['=SUM(销售表[[#Data],[金额]])', '=SUM(销售表[[#Data],[金'.length, '销售表', '金', 1],
    ["=SUM(T[费']用", null, 'T', '费]用', 1],
    ['=[@收', null, '', '收', 1],
  ] as const) {
    const c = FormulaInput.columnContext(text, caret ?? text.length);
    assert.ok(c, text);
    assert.equal(c.table, table);
    assert.equal(c.prefix, prefix);
    assert.equal(c.closures, closures);
  }
  assert.equal(FormulaInput.columnContext('="销售表[金', 7), null);
});
